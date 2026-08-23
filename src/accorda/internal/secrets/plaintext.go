package secrets

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// RuntimeDir is the memory-backed runtime directory used when a target
	// requires plaintext to be materialized as a file (docs/ACCORDA.md §18).
	RuntimeDir = "/run/accorda"

	plaintextFilePrefix = "plaintext-"
	runtimeDirMode      = 0o700
	plaintextFileMode   = 0o600
)

// WithPlaintextFile materializes plaintext in RuntimeDir for the duration of
// use. The file is created with mode 0600 and removed immediately after use
// returns or panics. Callers should prefer keeping plaintext in memory and use
// this function only when an external target renderer requires a file path.
//
// The callback must not retain, copy, or move the file. A cleanup failure is
// returned together with any callback error so plaintext cannot be silently
// left behind.
func WithPlaintextFile(plaintext []byte, use func(path string) error) error {
	return withPlaintextFile(RuntimeDir, plaintext, use)
}

func withPlaintextFile(runtimeDir string, plaintext []byte, use func(path string) error) (err error) {
	if use == nil {
		return errors.New("secrets: plaintext file callback is nil")
	}
	if err := prepareRuntimeDir(runtimeDir); err != nil {
		return err
	}

	root, err := os.OpenRoot(runtimeDir)
	if err != nil {
		return fmt.Errorf("secrets: open runtime directory: %w", err)
	}
	defer root.Close()

	name := plaintextFilePrefix + rand.Text()
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, plaintextFileMode)
	if err != nil {
		return fmt.Errorf("secrets: create plaintext runtime file: %w", err)
	}
	defer func() {
		removeErr := root.Remove(name)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("secrets: remove plaintext runtime file: %w", removeErr))
		}
	}()

	if err := writePlaintext(file, plaintext); err != nil {
		return err
	}
	return use(filepath.Join(runtimeDir, name))
}

func prepareRuntimeDir(runtimeDir string) error {
	if runtimeDir == "" {
		return errors.New("secrets: runtime directory is empty")
	}
	if err := os.MkdirAll(runtimeDir, runtimeDirMode); err != nil {
		return fmt.Errorf("secrets: create runtime directory: %w", err)
	}
	info, err := os.Lstat(runtimeDir)
	if err != nil {
		return fmt.Errorf("secrets: inspect runtime directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("secrets: runtime path must be a directory, not a symbolic link")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secrets: runtime directory must have permissions 0700 or stricter, got %04o", info.Mode().Perm())
	}
	return nil
}

type plaintextFile interface {
	Chmod(mode os.FileMode) error
	Write(data []byte) (int, error)
	Close() error
}

func writePlaintext(file plaintextFile, plaintext []byte) error {
	if err := file.Chmod(plaintextFileMode); err != nil {
		_ = file.Close()
		return fmt.Errorf("secrets: secure plaintext runtime file: %w", err)
	}
	written, err := file.Write(plaintext)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("secrets: write plaintext runtime file: %w", err)
	}
	if written != len(plaintext) {
		_ = file.Close()
		return fmt.Errorf("secrets: write plaintext runtime file: %w", io.ErrShortWrite)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("secrets: close plaintext runtime file: %w", err)
	}
	return nil
}

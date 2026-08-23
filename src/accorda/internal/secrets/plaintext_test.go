package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const plaintextFixture = "database-password"

func TestWithPlaintextFile_CreatesPrivateFileAndCleansUp(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "accorda")
	var plaintextPath string

	err := withPlaintextFile(runtimeDir, []byte(plaintextFixture), func(path string) error {
		plaintextPath = path
		assertPlaintextFile(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("withPlaintextFile() error = %v", err)
	}
	assertPlaintextRemoved(t, plaintextPath)
	assertMode(t, runtimeDir, runtimeDirMode)
}

func TestWithPlaintextFile_CleansUpAfterCallbackError(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "accorda")
	wantErr := errors.New("renderer failed")
	var plaintextPath string

	err := withPlaintextFile(runtimeDir, []byte(plaintextFixture), func(path string) error {
		plaintextPath = path
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("withPlaintextFile() error = %v, want %v", err, wantErr)
	}
	assertPlaintextRemoved(t, plaintextPath)
}

func TestWithPlaintextFile_ReportsCleanupFailure(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "accorda")
	wantErr := errors.New("renderer failed")

	err := withPlaintextFile(runtimeDir, []byte(plaintextFixture), func(path string) error {
		replacePlaintextWithNonEmptyDir(t, path)
		return wantErr
	})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "remove plaintext runtime file") {
		t.Fatalf("withPlaintextFile() error = %v, want callback and cleanup errors", err)
	}
}

func TestWithPlaintextFile_CleansUpAfterPanic(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "accorda")
	var plaintextPath string
	wantPanic := "renderer panicked"

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = withPlaintextFile(runtimeDir, []byte(plaintextFixture), func(path string) error {
			plaintextPath = path
			panic(wantPanic)
		})
	}()

	if recovered != wantPanic {
		t.Fatalf("recovered value = %v, want original panic %q", recovered, wantPanic)
	}
	assertPlaintextRemoved(t, plaintextPath)
}

func TestWithPlaintextFile_PreservesPanicAndCleanupFailure(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "accorda")
	wantPanic := errors.New("renderer panicked")

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = withPlaintextFile(runtimeDir, []byte(plaintextFixture), func(path string) error {
			replacePlaintextWithNonEmptyDir(t, path)
			panic(wantPanic)
		})
	}()

	got, ok := recovered.(*PanicCleanupError)
	if !ok {
		t.Fatalf("recovered value = %T(%v), want *PanicCleanupError", recovered, recovered)
	}
	if got.PanicValue != wantPanic {
		t.Fatalf("PanicValue = %v, want original panic %v", got.PanicValue, wantPanic)
	}
	if got.CleanupError == nil || !strings.Contains(got.CleanupError.Error(), "remove plaintext runtime file") {
		t.Fatalf("CleanupError = %v, want removal failure", got.CleanupError)
	}
	if !errors.Is(got, got.CleanupError) {
		t.Fatal("PanicCleanupError does not unwrap to CleanupError")
	}
}

func TestWithPlaintextFile_RejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name       string
		prepareDir func(t *testing.T) string
		use        func(string) error
		want       string
	}{
		{
			name:       "empty runtime directory",
			prepareDir: func(*testing.T) string { return "" },
			use:        func(string) error { return nil },
			want:       "runtime directory is empty",
		},
		{
			name: "readable runtime directory",
			prepareDir: func(t *testing.T) string {
				dir := filepath.Join(t.TempDir(), "accorda")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
				return dir
			},
			use:  func(string) error { return nil },
			want: "permissions 0700",
		},
		{
			name:       "nil callback",
			prepareDir: func(t *testing.T) string { return filepath.Join(t.TempDir(), "accorda") },
			use:        nil,
			want:       "callback is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := withPlaintextFile(tt.prepareDir(t), []byte(plaintextFixture), tt.use)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("withPlaintextFile() error = %v, want containing %q", err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), plaintextFixture) {
				t.Fatalf("withPlaintextFile() error leaked plaintext: %v", err)
			}
		})
	}
}

func TestWithPlaintextFile_RejectsSymlinkRuntimeDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows systems")
	}
	parent := t.TempDir()
	realDir := filepath.Join(parent, "real")
	if err := os.Mkdir(realDir, runtimeDirMode); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	symlink := filepath.Join(parent, "accorda")
	if err := os.Symlink(realDir, symlink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := withPlaintextFile(symlink, []byte(plaintextFixture), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("withPlaintextFile() error = %v, want symbolic-link failure", err)
	}
}

func TestWritePlaintext_Errors(t *testing.T) {
	wantErr := errors.New("fixture failure")
	tests := []struct {
		name string
		file *fakePlaintextFile
		want string
	}{
		{name: "chmod", file: &fakePlaintextFile{chmodErr: wantErr}, want: "secure plaintext"},
		{name: "write", file: &fakePlaintextFile{writeErr: wantErr}, want: "write plaintext"},
		{name: "short write", file: &fakePlaintextFile{written: 1}, want: "write plaintext"},
		{name: "close", file: &fakePlaintextFile{closeErr: wantErr}, want: "close plaintext"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writePlaintext(tt.file, []byte(plaintextFixture))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("writePlaintext() error = %v, want containing %q", err, tt.want)
			}
			if tt.file.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", tt.file.closeCalls)
			}
		})
	}
}

type fakePlaintextFile struct {
	chmodErr   error
	writeErr   error
	closeErr   error
	written    int
	closeCalls int
}

func (f *fakePlaintextFile) Chmod(os.FileMode) error {
	return f.chmodErr
}

func (f *fakePlaintextFile) Write(data []byte) (int, error) {
	if f.writeErr != nil || f.written != 0 {
		return f.written, f.writeErr
	}
	return len(data), nil
}

func (f *fakePlaintextFile) Close() error {
	f.closeCalls++
	return f.closeErr
}

func replacePlaintextWithNonEmptyDir(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.Mkdir(path, runtimeDirMode); err != nil {
		t.Fatalf("Mkdir replacement: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "occupied"), nil, plaintextFileMode); err != nil {
		t.Fatalf("WriteFile replacement: %v", err)
	}
}

func assertPlaintextFile(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(got) != plaintextFixture {
		t.Fatalf("plaintext file = %q, want %q", got, plaintextFixture)
	}
	assertMode(t, path, plaintextFileMode)
}

func assertPlaintextRemoved(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		t.Fatal("callback did not receive a plaintext path")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(%q) error = %v, want not exist", path, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%q) = %04o, want %04o", path, got, want)
	}
}

package locking

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const lockPollInterval = 100 * time.Millisecond

// UnlockFunc releases an acquired deployment lock. It is safe to call more
// than once.
type UnlockFunc func() error

// Locker serializes reconciliation for one deployment target.
type Locker interface {
	Lock(ctx context.Context) (UnlockFunc, error)
}

// FileLocker is a cross-process advisory lock backed by a persistent file.
// The operating system owns the lock lifetime and releases it when the owning
// process exits, so crash recovery is not vulnerable to PID reuse.
type FileLocker struct {
	path string
}

var _ Locker = (*FileLocker)(nil)

// NewFileLocker returns a target-scoped locker stored at path.
func NewFileLocker(path string) *FileLocker {
	return &FileLocker{path: path}
}

// Lock waits until the operating-system advisory lock is available or ctx is
// cancelled. The lock file remains on disk after release; ownership is the
// kernel lock on its open handle, which is released automatically on crash.
func (l *FileLocker) Lock(ctx context.Context) (UnlockFunc, error) {
	if l == nil || l.path == "" {
		return nil, errors.New("locking: file lock path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return nil, fmt.Errorf("locking: create lock dir: %w", err)
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("locking: open lock file: %w", err)
	}
	return waitForLock(ctx, file)
}

func waitForLock(ctx context.Context, file *os.File) (UnlockFunc, error) {
	for {
		acquired, err := tryAdvisoryLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("locking: acquire deployment lock: %w", err)
		}
		if acquired {
			return unlockFile(file), nil
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, fmt.Errorf("locking: wait for deployment lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func unlockFile(file *os.File) UnlockFunc {
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			if err := releaseAdvisoryLock(file); err != nil {
				releaseErr = fmt.Errorf("locking: release deployment lock: %w", err)
			}
			if err := file.Close(); releaseErr == nil && err != nil {
				releaseErr = fmt.Errorf("locking: close lock file: %w", err)
			}
		})
		return releaseErr
	}
}

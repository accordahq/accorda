package locking

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	lockPollInterval = 100 * time.Millisecond
	ownerWriteGrace  = 2 * time.Second
)

// UnlockFunc releases an acquired deployment lock. It is safe to call more
// than once.
type UnlockFunc func() error

// Locker serializes reconciliation for one deployment target.
type Locker interface {
	Lock(ctx context.Context) (UnlockFunc, error)
}

// FileLocker is a cross-process lock backed by an atomically-created owner
// file. A dead owner's file is reclaimed, allowing a restarted agent to
// continue reconciliation after a crash.
type FileLocker struct {
	path string
}

type owner struct {
	PID   int    `json:"pid"`
	Token string `json:"token"`
}

var _ Locker = (*FileLocker)(nil)

// NewFileLocker returns a target-scoped locker stored at path.
func NewFileLocker(path string) *FileLocker {
	return &FileLocker{path: path}
}

// Lock waits until the lock is available or ctx is cancelled. Creation with
// O_EXCL makes acquisition atomic across processes. When an owner was killed,
// its PID is no longer alive and the stale file is removed before retrying.
func (l *FileLocker) Lock(ctx context.Context) (UnlockFunc, error) {
	if l == nil || l.path == "" {
		return nil, errors.New("locking: file lock path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return nil, fmt.Errorf("locking: create lock dir: %w", err)
	}
	for {
		unlock, acquired, err := l.tryLock()
		if err != nil {
			return nil, err
		}
		if acquired {
			return unlock, nil
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("locking: wait for deployment lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (l *FileLocker) tryLock() (UnlockFunc, bool, error) {
	token, err := randomToken()
	if err != nil {
		return nil, false, err
	}
	current := owner{PID: os.Getpid(), Token: token}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return l.finishLock(f, current)
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, false, fmt.Errorf("locking: create lock: %w", err)
	}
	if err := l.reclaimStale(); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

func (l *FileLocker) finishLock(f *os.File, current owner) (UnlockFunc, bool, error) {
	data, err := json.Marshal(current)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(l.path)
		return nil, false, fmt.Errorf("locking: write owner: %w", err)
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() { releaseErr = l.release(current.Token) })
		return releaseErr
	}, true, nil
}

func (l *FileLocker) reclaimStale() error {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("locking: read owner: %w", err)
	}
	var current owner
	if err := json.Unmarshal(data, &current); err != nil || current.PID <= 0 || current.Token == "" {
		info, statErr := os.Stat(l.path)
		if statErr == nil && time.Since(info.ModTime()) < ownerWriteGrace {
			return nil
		}
		return removeLockFile(l.path)
	}
	if processAlive(current.PID) {
		return nil
	}
	return removeLockFile(l.path)
}

func (l *FileLocker) release(token string) error {
	data, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("locking: read lock for release: %w", err)
	}
	var current owner
	if err := json.Unmarshal(data, &current); err != nil || current.Token != token {
		return errors.New("locking: lock ownership changed before release")
	}
	return removeLockFile(l.path)
}

func removeLockFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("locking: remove stale lock: %w", err)
	}
	return nil
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("locking: generate owner token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

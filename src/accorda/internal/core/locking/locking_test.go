package locking

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileLockerSerializesAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.lock")
	first := NewFileLocker(path)
	second := NewFileLocker(path)
	unlock, err := first.Lock(context.Background())
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	t.Cleanup(func() { _ = unlock() })

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := second.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock error = %v, want deadline exceeded", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	secondUnlock, err := second.Lock(context.Background())
	if err != nil {
		t.Fatalf("second Lock after release: %v", err)
	}
	if err := secondUnlock(); err != nil {
		t.Fatalf("second unlock: %v", err)
	}
}

func TestFileLockerUsesPersistentUnlockedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.lock")
	if err := os.WriteFile(path, []byte("persistent lock file"), 0o600); err != nil {
		t.Fatal(err)
	}

	unlock, err := NewFileLocker(path).Lock(context.Background())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

func TestFileLockerRejectsMissingPath(t *testing.T) {
	var nilLocker *FileLocker
	for name, locker := range map[string]*FileLocker{
		"nil receiver": nilLocker,
		"empty path":   NewFileLocker(""),
	} {
		t.Run(name, func(t *testing.T) {
			if unlock, err := locker.Lock(context.Background()); err == nil || unlock != nil {
				t.Errorf("Lock() = %v, %v; want nil, error", unlock, err)
			}
		})
	}
}

func TestFileLockerFilesystemErrors(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write parent: %v", err)
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "create directory", path: filepath.Join(parent, "target.lock"), want: "create lock dir"},
		{name: "open file", path: t.TempDir(), want: "open lock file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewFileLocker(tt.path).Lock(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Lock() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWaitForLockClosedFileIsError(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-lock")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if unlock, err := waitForLock(context.Background(), file); err == nil || unlock != nil {
		t.Errorf("waitForLock() = %v, %v; want nil, acquire error", unlock, err)
	}
}

func TestUnlockFileReportsClosedFileError(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-lock")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	unlock := unlockFile(file)
	first := unlock()
	if first == nil {
		t.Error("unlock() error = nil for closed file")
	}
	if second := unlock(); second == nil || second.Error() != first.Error() {
		t.Errorf("second unlock() error = %v, want stable first error", second)
	}
}

func TestFileLockerReleasedWhenOwnerProcessExits(t *testing.T) {
	if os.Getenv("ACCORDA_LOCK_HELPER") == "1" {
		if _, err := NewFileLocker(os.Getenv("ACCORDA_LOCK_PATH")).Lock(context.Background()); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "target.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestFileLockerReleasedWhenOwnerProcessExits$")
	cmd.Env = append(os.Environ(), "ACCORDA_LOCK_HELPER=1", "ACCORDA_LOCK_PATH="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock owner process: %v: %s", err, output)
	}

	unlock, err := NewFileLocker(path).Lock(context.Background())
	if err != nil {
		t.Fatalf("Lock after owner exit: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

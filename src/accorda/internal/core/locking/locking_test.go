package locking

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

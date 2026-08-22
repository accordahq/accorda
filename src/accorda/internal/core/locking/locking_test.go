package locking

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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

func TestFileLockerReclaimsDeadOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.lock")
	data, err := json.Marshal(owner{PID: 1 << 30, Token: "dead"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
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

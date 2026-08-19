package history

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReceipt_Clone_IsDeepCopy(t *testing.T) {
	original := Receipt{
		DeploymentID: "dep_01Kabc",
		Repository:   "acme/backend",
		Environment:  "production",
		Commit:       "d71b2e4",
		StartedAt:    time.Unix(1700000000, 0),
		CompletedAt:  time.Unix(1700000008, 0),
		Services: map[string]ServiceReceipt{
			"api": {Image: "ghcr.io/acme/api:2.4.1", Digest: "sha256:91a"},
		},
	}
	clone := original.Clone()
	clone.Services["api"] = ServiceReceipt{Image: "mutated", Digest: "sha256:zzz"}
	clone.Services["worker"] = ServiceReceipt{Image: "ghcr.io/acme/worker:1.0"}

	if got := original.Services["api"].Image; got != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("original image mutated by clone: got %q", got)
	}
	if _, ok := original.Services["worker"]; ok {
		t.Errorf("original gained service from clone: %v", original.Services)
	}
}

func TestReceipt_Clone_PreservesNilMap(t *testing.T) {
	var zero Receipt
	if clone := zero.Clone(); clone.Services != nil {
		t.Errorf("Clone of zero-value Services = %v, want nil", clone.Services)
	}
}

func TestReceipt_SortedServiceNames(t *testing.T) {
	r := Receipt{Services: map[string]ServiceReceipt{
		"worker": {},
		"api":    {},
		"redis":  {},
	}}
	want := []string{"api", "redis", "worker"}
	if got := r.SortedServiceNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("SortedServiceNames = %v, want %v", got, want)
	}
}

func TestFileStore_AppendAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts", "proj.jsonl")
	store := NewFileStore(path)

	r1 := Receipt{
		DeploymentID: "dep_1",
		Repository:   "acme/backend",
		Environment:  "production",
		Commit:       "d71b2e4",
		StartedAt:    time.Unix(1700000000, 0),
		CompletedAt:  time.Unix(1700000008, 0),
		Services:     map[string]ServiceReceipt{"api": {Image: "api:2.4.1", Digest: "sha256:91a"}},
	}
	r2 := Receipt{
		DeploymentID: "dep_2",
		Repository:   "acme/backend",
		Environment:  "production",
		Commit:       "a01fd92",
		StartedAt:    time.Unix(1700000100, 0),
		CompletedAt:  time.Unix(1700000108, 0),
		Services:     map[string]ServiceReceipt{"worker": {Image: "worker:1.0", Digest: "sha256:a42"}},
	}

	if err := store.Append(context.Background(), r1); err != nil {
		t.Fatalf("Append r1: %v", err)
	}
	if err := store.Append(context.Background(), r2); err != nil {
		t.Fatalf("Append r2: %v", err)
	}

	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
	// Append order is preserved (oldest first).
	if got[0].DeploymentID != "dep_1" || got[1].DeploymentID != "dep_2" {
		t.Errorf("List order = %v, want [dep_1 dep_2]", []string{got[0].DeploymentID, got[1].DeploymentID})
	}
	if !reflect.DeepEqual(got[0], r1) {
		t.Errorf("List[0] = %+v, want %+v", got[0], r1)
	}
}

func TestFileStore_List_MissingFile_IsEmpty(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "nope.jsonl"))
	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List len = %d, want 0", len(got))
	}
}

func TestFileStore_Append_EmptyPath_IsError(t *testing.T) {
	store := NewFileStore("")
	if err := store.Append(context.Background(), Receipt{}); err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestFileStore_Append_IsAppendOnly(t *testing.T) {
	// Appending must not rewrite earlier lines: the journal grows, so a
	// receipt once written is never mutated (audit-trail property).
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	store := NewFileStore(path)
	r1 := Receipt{DeploymentID: "dep_1", Services: map[string]ServiceReceipt{"api": {Image: "api:1"}}}
	r2 := Receipt{DeploymentID: "dep_2", Services: map[string]ServiceReceipt{"api": {Image: "api:2"}}}
	if err := store.Append(context.Background(), r1); err != nil {
		t.Fatalf("Append r1: %v", err)
	}
	if err := store.Append(context.Background(), r2); err != nil {
		t.Fatalf("Append r2: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Two lines, each ending in a newline.
	if got := string(data); len(got) == 0 || got[len(got)-1] != '\n' {
		t.Errorf("journal does not end with newline: %q", got)
	}
}

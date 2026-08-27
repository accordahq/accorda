package sources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStub_SatisfiesSource(t *testing.T) {
	var src Source = NewStub()

	ctx := context.Background()

	if err := src.Validate(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Validate: err = %v, want ErrNotImplemented", err)
	}
	if c, err := src.Fetch(ctx); !errors.Is(err, ErrNotImplemented) || c != (Commit{}) {
		t.Errorf("Fetch: c=%v err=%v, want zero Commit, ErrNotImplemented", c, err)
	}
	if r, err := src.Revision(ctx, nil); !errors.Is(err, ErrNotImplemented) || r != nil {
		t.Errorf("Revision: r=%v err=%v, want nil, ErrNotImplemented", r, err)
	}
	if got := ErrNotImplemented.Error(); got != "source: not implemented" {
		t.Errorf("ErrNotImplemented.Error() = %q", got)
	}
}

func TestRevisionPathRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	revision := NewRevision(Commit{}, "", root, nil, nil)
	if _, err := revision.Path("escape/secret"); err == nil {
		t.Fatal("Path through escaping symlink succeeded, want error")
	}
}

func TestRevisionCloseReleasesOnce(t *testing.T) {
	calls := 0
	revision := NewRevision(Commit{}, "", t.TempDir(), nil, func() error {
		calls++
		return nil
	})
	if err := revision.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := revision.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if calls != 1 {
		t.Fatalf("release calls = %d, want 1", calls)
	}
}

func TestSourceInterfaceContract(t *testing.T) {
	var src Source = NewStub()
	ctx := context.Background()
	_ = src.Validate
	_ = src.Fetch
	_ = src.Revision
	// Touch each method on a non-nil instance to confirm the interface is
	// usable without panicking.
	_ = src.Validate(ctx)
}

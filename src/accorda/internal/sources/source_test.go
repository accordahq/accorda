package sources

import (
	"context"
	"errors"
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
	if d, err := src.Desired(ctx, nil); !errors.Is(err, ErrNotImplemented) || d != nil {
		t.Errorf("Desired: d=%v err=%v, want nil, ErrNotImplemented", d, err)
	}
	if got := ErrNotImplemented.Error(); got != "source: not implemented" {
		t.Errorf("ErrNotImplemented.Error() = %q", got)
	}
}

func TestSourceInterfaceContract(t *testing.T) {
	var src Source = NewStub()
	ctx := context.Background()
	_ = src.Validate
	_ = src.Fetch
	_ = src.Desired
	// Touch each method on a non-nil instance to confirm the interface is
	// usable without panicking.
	_ = src.Validate(ctx)
}

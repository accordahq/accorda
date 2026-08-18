package targets

import (
	"context"
	"errors"
	"testing"

	"accorda/internal/core/plan"
	"accorda/internal/core/state"
)

// TestStubSatisfiesTarget guards the Target interface at compile time via the
// var _ Target = (*Stub)(nil) assertion in target.go. This test additionally
// verifies the runtime behavior of the stub so that ErrNotImplemented is the
// documented sentinel.
func TestStub_SatisfiesTarget(t *testing.T) {
	var tgt Target = NewStub()

	ctx := context.Background()
	desired := &state.DesiredState{Commit: "abc"}

	if err := tgt.Validate(ctx); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Validate: err = %v, want ErrNotImplemented", err)
	}
	if rs, err := tgt.Current(ctx); !errors.Is(err, ErrNotImplemented) || rs != nil {
		t.Errorf("Current: rs=%v err=%v, want nil, ErrNotImplemented", rs, err)
	}
	if p, err := tgt.Plan(ctx, desired, nil); !errors.Is(err, ErrNotImplemented) || p != nil {
		t.Errorf("Plan: p=%v err=%v, want nil, ErrNotImplemented", p, err)
	}
	if err := tgt.Apply(ctx, &plan.Plan{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Apply: err = %v, want ErrNotImplemented", err)
	}
	if h, err := tgt.Health(ctx); !errors.Is(err, ErrNotImplemented) || h != nil {
		t.Errorf("Health: h=%v err=%v, want nil, ErrNotImplemented", h, err)
	}
}

// TestTargetInterfaceContract documents the five methods the spec
// (docs/ACCORDA.md §12) requires. The compile-time assertion
// `var _ Target = (*Stub)(nil)` in target.go enforces that Stub implements
// the full interface; this test keeps a named reference to each method so a
// rename or removal surfaces here too.
func TestTargetInterfaceContract(t *testing.T) {
	var tgt Target = NewStub()
	ctx := context.Background()
	_ = tgt.Validate
	_ = tgt.Current
	_ = tgt.Plan
	_ = tgt.Apply
	_ = tgt.Health
	// Touch each method on a non-nil instance to confirm the interface is
	// usable without panicking.
	_ = tgt.Validate(ctx)
}

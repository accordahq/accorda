package targets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"accorda/internal/core/plan"
	"accorda/internal/core/state"
)

func TestApplyError(t *testing.T) {
	cause := errors.New("compose exited")
	err := &ApplyError{
		Completed: []plan.Action{{Service: "api", Kind: plan.ActionRecreate}},
		Failed:    plan.Action{Service: "worker", Kind: plan.ActionStart},
		Err:       cause,
	}

	for _, want := range []string{"api:recreate", "worker:start", cause.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
		}
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(%v, cause) = false, want true", err)
	}
}

func TestNilApplyError(t *testing.T) {
	var err *ApplyError
	if got := err.Error(); got != "target apply failed" {
		t.Errorf("Error() = %q, want %q", got, "target apply failed")
	}
	if got := err.Unwrap(); got != nil {
		t.Errorf("Unwrap() = %v, want nil", got)
	}
}

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
	if got := ErrNotImplemented.Error(); got != "target: not implemented" {
		t.Errorf("ErrNotImplemented.Error() = %q", got)
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

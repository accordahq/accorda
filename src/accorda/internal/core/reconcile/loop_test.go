package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"accorda/internal/core/events"
	"accorda/internal/core/state"
	"accorda/internal/sources"
)

const testPollInterval = 10 * time.Millisecond

func TestRun_UnchangedHeadDoesNotMutateTarget(t *testing.T) {
	src := &fakeSource{commit: sources.Commit{SHA: "abc123"}, desired: healthyDesired()}
	tgt := &fakeTarget{health: healthyHealth(), runtime: healthyRuntime()}
	ctx, cancel := context.WithCancel(t.Context())
	cycles := 0

	err := New(src, tgt, events.NewBus()).Run(ctx, testPollInterval, func(result *Result) {
		if result.Phase != PhaseSynced {
			t.Errorf("Phase = %q, want %q", result.Phase, PhaseSynced)
		}
		cycles++
		if cycles == 2 {
			cancel()
		}
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tgt.applyCalls != 0 {
		t.Errorf("Apply calls = %d, want 0 for unchanged HEAD", tgt.applyCalls)
	}
	if tgt.planCalls != 1 {
		t.Errorf("Plan calls = %d, want 1 (initial cycle only)", tgt.planCalls)
	}
}

func TestRun_NewCommitIsReconciled(t *testing.T) {
	desired := healthyDesired()
	src := &fakeSource{commit: sources.Commit{SHA: desired.Commit}, desired: desired}
	tgt := &fakeTarget{health: healthyHealth(), runtime: healthyRuntime()}
	ctx, cancel := context.WithCancel(t.Context())
	cycles := 0

	err := New(src, tgt, events.NewBus()).Run(ctx, testPollInterval, func(result *Result) {
		cycles++
		if cycles == 1 {
			next := healthyDesired()
			next.Commit = "def456"
			next.Services["api"] = state.Service{Image: "api:3"}
			src.commit = sources.Commit{SHA: next.Commit}
			src.desired = next
			tgt.changedPlan = true
			return
		}
		cancel()
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tgt.applyCalls != 1 {
		t.Errorf("Apply calls = %d, want 1 for the new commit", tgt.applyCalls)
	}
	if tgt.planCalls != 2 {
		t.Errorf("Plan calls = %d, want 2 (initial and new commits)", tgt.planCalls)
	}
}

func TestRun_UnchangedHeadRepairsDrift(t *testing.T) {
	src := &fakeSource{commit: sources.Commit{SHA: "abc123"}, desired: healthyDesired()}
	tgt := &fakeTarget{health: healthyHealth(), runtime: healthyRuntime()}
	ctx, cancel := context.WithCancel(t.Context())
	cycles := 0

	err := New(src, tgt, events.NewBus()).WithDriftPolicy(DriftRepair).
		Run(ctx, testPollInterval, func(*Result) {
			cycles++
			if cycles == 1 {
				tgt.runtime.Services["api"] = state.RuntimeService{Status: "exited", Image: "api:2"}
				tgt.changedPlan = true
				return
			}
			cancel()
		})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tgt.applyCalls != 1 {
		t.Errorf("Apply calls = %d, want 1 drift repair", tgt.applyCalls)
	}
	if got := tgt.runtime.Services["api"].Status; got != state.RunningStatus {
		t.Errorf("runtime status = %q, want %q", got, state.RunningStatus)
	}
}

func TestRun_HonorsInterval(t *testing.T) {
	src := &fakeSource{commit: sources.Commit{SHA: "abc123"}, desired: healthyDesired()}
	tgt := &fakeTarget{health: healthyHealth(), runtime: healthyRuntime()}
	ctx, cancel := context.WithCancel(t.Context())
	started := time.Now()
	cycles := 0

	err := New(src, tgt, nil).Run(ctx, testPollInterval, func(*Result) {
		cycles++
		if cycles == 2 {
			cancel()
		}
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < testPollInterval {
		t.Errorf("two cycles completed in %s, want at least %s", elapsed, testPollInterval)
	}
}

func TestRun_ContinuesAfterFailedCycle(t *testing.T) {
	src := &fakeSource{fetchErr: errors.New("temporary fetch failure")}
	ctx, cancel := context.WithCancel(t.Context())
	cycles := 0

	err := New(src, &fakeTarget{}, nil).Run(ctx, testPollInterval, func(result *Result) {
		if result.Phase != PhaseFailed {
			t.Errorf("Phase = %q, want %q", result.Phase, PhaseFailed)
		}
		cycles++
		if cycles == 2 {
			cancel()
		}
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if cycles != 2 {
		t.Errorf("cycles = %d, want 2", cycles)
	}
}

func TestRun_CancelledContextStopsGracefully(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := New(&fakeSource{}, &fakeTarget{}, nil).Run(ctx, time.Second, nil)

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

func TestRun_RejectsNonPositiveInterval(t *testing.T) {
	err := New(&fakeSource{}, &fakeTarget{}, nil).Run(t.Context(), 0, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want interval validation error")
	}
}

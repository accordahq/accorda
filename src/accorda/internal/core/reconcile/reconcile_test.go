package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"accorda/internal/core/events"
	"accorda/internal/core/health"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
	"accorda/internal/sources"
	"accorda/internal/targets"
)

// fakeSource is a controllable Source for tests.
type fakeSource struct {
	fetchErr   error
	desiredErr error
	commit     sources.Commit
	desired    *state.DesiredState
}

func (f *fakeSource) Validate(context.Context) error { return nil }
func (f *fakeSource) Fetch(context.Context) (sources.Commit, error) {
	if f.fetchErr != nil {
		return sources.Commit{}, f.fetchErr
	}
	return f.commit, nil
}
func (f *fakeSource) Desired(context.Context, *sources.Commit) (*state.DesiredState, error) {
	if f.desiredErr != nil {
		return nil, f.desiredErr
	}
	return f.desired, nil
}

// fakeTarget is a controllable Target for tests.
type fakeTarget struct {
	validateErr error
	planErr     error
	applyErr    error
	healthErr   error
	currentErr  error
	health      *health.Health
	runtime     *state.RuntimeState
	applied     []*plan.Plan
	applyCalls  int
}

func (f *fakeTarget) Validate(context.Context) error { return f.validateErr }
func (f *fakeTarget) Current(context.Context) (*state.RuntimeState, error) {
	if f.currentErr != nil {
		return nil, f.currentErr
	}
	return f.runtime, nil
}
func (f *fakeTarget) Plan(_ context.Context, desired *state.DesiredState, _ *state.DeployedState) (*plan.Plan, error) {
	if f.planErr != nil {
		return nil, f.planErr
	}
	return plan.New("dep_1", desired.Repository, desired.Commit, time.Unix(0, 0)), nil
}
func (f *fakeTarget) Apply(_ context.Context, p *plan.Plan) error {
	f.applyCalls++
	if f.applyErr != nil && f.applyCalls == 1 {
		return f.applyErr
	}
	f.applied = append(f.applied, p)
	return nil
}
func (f *fakeTarget) Health(context.Context) (*health.Health, error) {
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	return f.health, nil
}

func healthyDesired() *state.DesiredState {
	return &state.DesiredState{
		Repository: "acme/infra",
		Branch:     "main",
		Commit:     "abc123",
		Services: map[string]state.Service{
			"api": {Image: "api:2"},
		},
	}
}

func healthyRuntime() *state.RuntimeState {
	return &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"api": {Status: "running", Image: "api:2"},
		},
	}
}

func healthyHealth() *health.Health {
	h := health.New(time.Unix(0, 0))
	h.SetService("api", health.StatusHealthy, "")
	h.Summarize()
	return &h
}

func TestReconcile_HappyPath_ReachesSynced(t *testing.T) {
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123", Branch: "main"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health:  healthyHealth(),
		runtime: healthyRuntime(),
	}
	bus := events.NewBus()
	var transitions []StateTransition
	bus.Subscribe(func(_ context.Context, e events.Event) {
		if e.Type == events.EventStateTransition {
			transitions = append(transitions, e.Payload.(StateTransition))
		}
	})

	r := New(src, tgt, bus)
	res := r.Reconcile(context.Background())

	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q (err=%v)", res.Phase, PhaseSynced, res.Err)
	}
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil", res.Err)
	}
	if res.Comparison.Result != state.ResultSynced {
		t.Errorf("Comparison.Result = %q, want %q", res.Comparison.Result, state.ResultSynced)
	}
	if res.Health == nil || !res.Health.Synced {
		t.Errorf("Health.Synced = %v, want true", res.Health != nil && res.Health.Synced)
	}

	// Verify the full phase sequence was emitted.
	wantPhases := []Phase{
		PhaseFetching, PhaseValidating, PhasePlanning, PhasePulling,
		PhaseDeploying, PhaseVerifying, PhaseHealthy, PhaseSynced,
	}
	if len(transitions) != len(wantPhases) {
		t.Fatalf("got %d transitions, want %d: %v", len(transitions), len(wantPhases), transitions)
	}
	for i, want := range wantPhases {
		if transitions[i].To != want {
			t.Errorf("transition[%d].To = %q, want %q", i, transitions[i].To, want)
		}
	}
}

func TestReconcile_FetchFailure(t *testing.T) {
	src := &fakeSource{fetchErr: errors.New("fetch boom")}
	tgt := &fakeTarget{}
	r := New(src, tgt, events.NewBus())

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
	if res.Err == nil {
		t.Fatal("Err = nil, want non-nil")
	}
}

func TestReconcile_ValidateFailure(t *testing.T) {
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{validateErr: errors.New("validate boom")}
	r := New(src, tgt, events.NewBus())

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
}

func TestReconcile_ApplyFailure_RollsBack(t *testing.T) {
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{applyErr: errors.New("apply boom")}
	prev := &state.DeployedState{
		DeploymentID: "dep_0",
		Commit:       "prev123",
		Services:     map[string]state.Service{"api": {Image: "api:1"}},
	}
	r := New(src, tgt, events.NewBus()).WithPrevious(prev)

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
	if !res.RolledBack {
		t.Error("RolledBack = false, want true")
	}
	// The rollback should have applied the previous deployment.
	if len(tgt.applied) != 1 {
		t.Fatalf("applied plans = %d, want 1 (rollback)", len(tgt.applied))
	}
	if tgt.applied[0].Commit != "prev123" {
		t.Errorf("rollback commit = %q, want %q", tgt.applied[0].Commit, "prev123")
	}
}

func TestReconcile_HealthFailure_RollsBack(t *testing.T) {
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	unhealthy := health.New(time.Unix(0, 0))
	unhealthy.SetService("api", health.StatusUnhealthy, "exit 1")
	unhealthy.Summarize()
	tgt := &fakeTarget{health: &unhealthy}
	prev := &state.DeployedState{
		DeploymentID: "dep_0",
		Commit:       "prev123",
		Services:     map[string]state.Service{"api": {Image: "api:1"}},
	}
	r := New(src, tgt, events.NewBus()).WithPrevious(prev)

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
	if !res.RolledBack {
		t.Error("RolledBack = false, want true")
	}
}

func TestReconcile_HealthNotImplemented_Proceeds(t *testing.T) {
	// When the target has not implemented Health (returns ErrNotImplemented),
	// the lifecycle treats the deployment as healthy and proceeds to SYNCED.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		healthErr: targets.ErrNotImplemented,
		runtime:   healthyRuntime(),
	}
	r := New(src, tgt, events.NewBus())

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q (err=%v)", res.Phase, PhaseSynced, res.Err)
	}
}

func TestReconcile_NilDependencies(t *testing.T) {
	r := New(nil, nil, events.NewBus())
	res := r.Reconcile(context.Background())
	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
}

func TestReconcile_NoBus_DoesNotPanic(t *testing.T) {
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health:  healthyHealth(),
		runtime: healthyRuntime(),
	}
	r := New(src, tgt, nil)
	res := r.Reconcile(context.Background())
	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseSynced)
	}
}

func TestReconcile_NilHealth_TreatedAsHealthy(t *testing.T) {
	// A target returning (nil, nil) from Health means "no health data"
	// (e.g. no healthchecks declared). It must not be failed and rolled
	// back.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health:  nil,
		runtime: healthyRuntime(),
	}
	r := New(src, tgt, events.NewBus())

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q (err=%v)", res.Phase, PhaseSynced, res.Err)
	}
	if res.RolledBack {
		t.Error("RolledBack = true, want false")
	}
}

func TestReconcile_UnknownHealth_Proceeds(t *testing.T) {
	// A target whose services have no healthchecks reports Overall ==
	// StatusUnknown. This is not a failure: DEPLOYED, HEALTHY, and SYNCED are
	// distinct outcomes (docs/ACCORDA.md §19), so a no-healthcheck deployment
	// must proceed to SYNCED rather than be rolled back.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	unknown := health.New(time.Unix(0, 0))
	unknown.SetService("api", health.StatusUnknown, "")
	unknown.Summarize()
	tgt := &fakeTarget{
		health:  &unknown,
		runtime: healthyRuntime(),
	}
	r := New(src, tgt, events.NewBus())

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q (err=%v)", res.Phase, PhaseSynced, res.Err)
	}
	if res.RolledBack {
		t.Error("RolledBack = true, want false")
	}
}

func TestReconcile_Drift_EmitsDriftDetected(t *testing.T) {
	// When the runtime has drifted (desired == deployed but runtime differs),
	// the reconciler must emit EventDriftDetected and not report SYNCED.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	// api is stopped at runtime: drift.
	tgt := &fakeTarget{
		health: healthyHealth(),
		runtime: &state.RuntimeState{
			Services: map[string]state.RuntimeService{
				"api": {Status: "exited", Image: "api:2"},
			},
		},
	}
	bus := events.NewBus()
	var driftEvents int
	bus.Subscribe(func(_ context.Context, e events.Event) {
		if e.Type == events.EventDriftDetected {
			driftEvents++
		}
	})

	r := New(src, tgt, bus)
	res := r.Reconcile(context.Background())

	if res.Phase != PhaseHealthy {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseHealthy)
	}
	if res.Comparison.Result != state.ResultDrifted {
		t.Fatalf("Comparison.Result = %q, want %q", res.Comparison.Result, state.ResultDrifted)
	}
	if driftEvents != 1 {
		t.Errorf("drift events = %d, want 1", driftEvents)
	}
}

func TestReconcile_DriftRepair_EmitsDetectedAndReconciled(t *testing.T) {
	// With DriftRepair, a drifted runtime must emit DriftDetected, re-plan and
	// re-apply to restore the desired runtime, then emit DriftReconciled.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health: healthyHealth(),
		runtime: &state.RuntimeState{
			Services: map[string]state.RuntimeService{
				"api": {Status: "exited", Image: "api:2"},
			},
		},
	}
	bus := events.NewBus()
	var detected, reconciled int
	bus.Subscribe(func(_ context.Context, e events.Event) {
		switch e.Type {
		case events.EventDriftDetected:
			detected++
		case events.EventDriftReconciled:
			reconciled++
		}
	})

	r := New(src, tgt, bus).WithDriftPolicy(DriftRepair)
	res := r.Reconcile(context.Background())

	if res.Phase != PhaseHealthy {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseHealthy)
	}
	if res.Comparison.Result != state.ResultDrifted {
		t.Fatalf("Comparison.Result = %q, want %q", res.Comparison.Result, state.ResultDrifted)
	}
	if detected != 1 {
		t.Errorf("drift.detected events = %d, want 1", detected)
	}
	if reconciled != 1 {
		t.Errorf("drift.reconciled events = %d, want 1", reconciled)
	}
	// Repair re-plans and re-applies: one Apply from the deploy phase plus
	// one from the repair.
	if tgt.applyCalls != 2 {
		t.Errorf("apply calls = %d, want 2 (deploy + repair)", tgt.applyCalls)
	}
}

func TestReconcile_DriftDisabled_NoEventsNoRepair(t *testing.T) {
	// With DriftDisabled, drift is ignored entirely: no events and no repair.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health: healthyHealth(),
		runtime: &state.RuntimeState{
			Services: map[string]state.RuntimeService{
				"api": {Status: "exited", Image: "api:2"},
			},
		},
	}
	bus := events.NewBus()
	var detected, reconciled int
	bus.Subscribe(func(_ context.Context, e events.Event) {
		switch e.Type {
		case events.EventDriftDetected:
			detected++
		case events.EventDriftReconciled:
			reconciled++
		}
	})

	r := New(src, tgt, bus).WithDriftPolicy(DriftDisabled)
	res := r.Reconcile(context.Background())

	if res.Phase != PhaseHealthy {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseHealthy)
	}
	if detected != 0 {
		t.Errorf("drift.detected events = %d, want 0", detected)
	}
	if reconciled != 0 {
		t.Errorf("drift.reconciled events = %d, want 0", reconciled)
	}
	// Only the deploy-phase Apply runs; no repair Apply.
	if tgt.applyCalls != 1 {
		t.Errorf("apply calls = %d, want 1 (deploy only)", tgt.applyCalls)
	}
}

func TestReconcile_DriftReport_DetectedOnly(t *testing.T) {
	// The default policy (DriftReport) emits DriftDetected but does not repair.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health: healthyHealth(),
		runtime: &state.RuntimeState{
			Services: map[string]state.RuntimeService{
				"api": {Status: "exited", Image: "api:2"},
			},
		},
	}
	bus := events.NewBus()
	var detected, reconciled int
	bus.Subscribe(func(_ context.Context, e events.Event) {
		switch e.Type {
		case events.EventDriftDetected:
			detected++
		case events.EventDriftReconciled:
			reconciled++
		}
	})

	r := New(src, tgt, bus)
	res := r.Reconcile(context.Background())

	if res.Phase != PhaseHealthy {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseHealthy)
	}
	if detected != 1 {
		t.Errorf("drift.detected events = %d, want 1", detected)
	}
	if reconciled != 0 {
		t.Errorf("drift.reconciled events = %d, want 0", reconciled)
	}
	if tgt.applyCalls != 1 {
		t.Errorf("apply calls = %d, want 1 (deploy only)", tgt.applyCalls)
	}
}

func TestReconcile_NoopPlan_NoDeploymentEvents(t *testing.T) {
	// A no-op plan (only noop actions) must not emit deployment.started or
	// deployment.succeeded, since nothing was deployed.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health:  healthyHealth(),
		runtime: healthyRuntime(),
	}
	bus := events.NewBus()
	var started, succeeded int
	bus.Subscribe(func(_ context.Context, e events.Event) {
		switch e.Type {
		case events.EventDeploymentStarted:
			started++
		case events.EventDeploymentSucceeded:
			succeeded++
		}
	})

	r := New(src, tgt, bus)
	res := r.Reconcile(context.Background())

	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseSynced)
	}
	if started != 0 {
		t.Errorf("deployment.started events = %d, want 0", started)
	}
	if succeeded != 0 {
		t.Errorf("deployment.succeeded events = %d, want 0", succeeded)
	}
}

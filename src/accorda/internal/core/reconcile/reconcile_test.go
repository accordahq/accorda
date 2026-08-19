package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"accorda/internal/core/events"
	"accorda/internal/core/health"
	"accorda/internal/core/history"
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
	// repairApplyErr fails the repair-phase Apply (the Apply that follows a
	// successful deploy-phase Apply) so a failed drift repair can be
	// exercised. It is keyed off deployDone rather than a call count so the
	// test does not depend on the exact ordering of Apply calls.
	repairApplyErr error
	healthErr      error
	currentErr     error
	health         *health.Health
	runtime        *state.RuntimeState
	applied        []*plan.Plan
	applyCalls     int
	deployDone     bool
	// changedPlan makes Plan return a plan with a non-noop action so the
	// reconciler treats the deployment as changed (and records a receipt).
	changedPlan bool
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
	p := plan.New("dep_1", desired.Repository, desired.Commit, time.Unix(0, 0))
	if f.changedPlan {
		p.AddAction(plan.Action{Kind: plan.ActionRecreate, Service: "api", Image: "api:2"})
	}
	return p, nil
}
func (f *fakeTarget) Apply(_ context.Context, p *plan.Plan) error {
	f.applyCalls++
	if f.applyErr != nil && f.applyCalls == 1 {
		return f.applyErr
	}
	if f.repairApplyErr != nil && f.deployDone {
		return f.repairApplyErr
	}
	f.applied = append(f.applied, p)
	f.deployDone = true
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

func TestReconcile_DriftRepair_ApplyFails_NoReconciled(t *testing.T) {
	// A failed repair (Apply error) must not emit DriftReconciled and must
	// leave the result DRIFTED (docs/DECISIONS.md #22).
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
		repairApplyErr: errors.New("repair boom"),
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
	if reconciled != 0 {
		t.Errorf("drift.reconciled events = %d, want 0", reconciled)
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

// fakeStore is a controllable history.Store for tests.
type fakeStore struct {
	appended    []history.Receipt
	err         error
	appendCalls int
}

func (f *fakeStore) Append(_ context.Context, r history.Receipt) error {
	f.appendCalls++
	if f.err != nil {
		return f.err
	}
	f.appended = append(f.appended, r)
	return nil
}
func (f *fakeStore) List(context.Context) ([]history.Receipt, error) { return f.appended, nil }

func TestReconcile_RecordsReceiptOnChangedDeployment(t *testing.T) {
	// A deployment that changes the target must record a receipt carrying
	// the deployment id, repository, environment, commit, and per-service
	// image + digest read back from the runtime (docs/ACCORDA.md §7).
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health: healthyHealth(),
		runtime: &state.RuntimeState{
			Services: map[string]state.RuntimeService{
				"api": {Status: "running", Image: "api:2", Digest: "sha256:91a"},
			},
		},
		changedPlan: true,
	}
	store := &fakeStore{}
	r := New(src, tgt, events.NewBus()).
		WithEnvironment("production").
		WithReceiptStore(store)

	res := r.Reconcile(context.Background())
	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q (err=%v)", res.Phase, PhaseSynced, res.Err)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended receipts = %d, want 1", len(store.appended))
	}
	rc := store.appended[0]
	if rc.DeploymentID == "" {
		t.Error("receipt deployment id is empty")
	}
	if rc.Repository != "acme/infra" {
		t.Errorf("receipt repository = %q, want acme/infra", rc.Repository)
	}
	if rc.Environment != "production" {
		t.Errorf("receipt environment = %q, want production", rc.Environment)
	}
	if rc.Commit != "abc123" {
		t.Errorf("receipt commit = %q, want abc123", rc.Commit)
	}
	if rc.StartedAt.IsZero() || rc.CompletedAt.IsZero() {
		t.Error("receipt timestamps must be set")
	}
	if rc.CompletedAt.Before(rc.StartedAt) {
		t.Errorf("receipt completed_at %v before started_at %v", rc.CompletedAt, rc.StartedAt)
	}
	if rc.Result != history.OutcomeHealthy {
		t.Errorf("receipt result = %q, want %q", rc.Result, history.OutcomeHealthy)
	}
	if want := []string{"api"}; !reflect.DeepEqual(rc.Changes, want) {
		t.Errorf("receipt changes = %v, want %v", rc.Changes, want)
	}
	svc, ok := rc.Services["api"]
	if !ok {
		t.Fatalf("receipt services missing api: %+v", rc.Services)
	}
	if svc.Image != "api:2" {
		t.Errorf("receipt api image = %q, want api:2", svc.Image)
	}
	if svc.Digest != "sha256:91a" {
		t.Errorf("receipt api digest = %q, want sha256:91a", svc.Digest)
	}
}

func TestReconcile_NoopPlan_NoReceipt(t *testing.T) {
	// A no-op cycle (plan unchanged) must not record a receipt, since nothing
	// was deployed (docs/ACCORDA.md §7: "every successful deployment").
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health:  healthyHealth(),
		runtime: healthyRuntime(),
	}
	store := &fakeStore{}
	r := New(src, tgt, events.NewBus()).WithReceiptStore(store)

	res := r.Reconcile(context.Background())
	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseSynced)
	}
	if len(store.appended) != 0 {
		t.Errorf("appended receipts = %d, want 0", len(store.appended))
	}
}

func TestReconcile_NoStore_NoReceipt(t *testing.T) {
	// Without a configured store, a changed deployment must not panic and
	// must not record a receipt. changedPlan is set so the store path (which
	// is nil here) is actually reached; otherwise the test would pass without
	// exercising recordReceipt at all.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health:      healthyHealth(),
		runtime:     healthyRuntime(),
		changedPlan: true,
	}
	r := New(src, tgt, events.NewBus())
	res := r.Reconcile(context.Background())
	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q (err=%v)", res.Phase, PhaseSynced, res.Err)
	}
}

func TestReconcile_StoreError_StillSynced(t *testing.T) {
	// A store failure is not a deployment failure: the cycle still reports
	// SYNCED (receipt recording is best-effort). changedPlan is set so the
	// store is actually consulted; the assertion that Append was attempted
	// proves recordReceipt ran despite the nil store path being unreachable.
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{
		health:      healthyHealth(),
		runtime:     healthyRuntime(),
		changedPlan: true,
	}
	store := &fakeStore{err: errors.New("store boom")}
	r := New(src, tgt, events.NewBus()).WithReceiptStore(store)

	res := r.Reconcile(context.Background())
	if res.Phase != PhaseSynced {
		t.Fatalf("Phase = %q, want %q (err=%v)", res.Phase, PhaseSynced, res.Err)
	}
	if len(store.appended) != 0 {
		t.Errorf("appended receipts = %d, want 0 (the store errored)", len(store.appended))
	}
	if store.appendCalls != 1 {
		t.Errorf("store append calls = %d, want 1 (recordReceipt must consult the store)", store.appendCalls)
	}
}

func TestReconcile_ApplyFailure_RecordsFailedReceipt(t *testing.T) {
	// A deployment that fails during apply must be recorded as OutcomeFailed
	// in the history, even when no runtime digest is available (docs/ACCORDA.md
	// §11: a failed cycle is part of the deployment history).
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{applyErr: errors.New("apply boom")}
	store := &fakeStore{}
	r := New(src, tgt, events.NewBus()).
		WithEnvironment("production").
		WithReceiptStore(store)

	res := r.Reconcile(context.Background())
	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended receipts = %d, want 1", len(store.appended))
	}
	rc := store.appended[0]
	if rc.Result != history.OutcomeFailed {
		t.Errorf("receipt result = %q, want %q", rc.Result, history.OutcomeFailed)
	}
	if rc.Repository != "acme/infra" {
		t.Errorf("receipt repository = %q, want acme/infra", rc.Repository)
	}
	if rc.Environment != "production" {
		t.Errorf("receipt environment = %q, want production", rc.Environment)
	}
	if rc.Commit != "abc123" {
		t.Errorf("receipt commit = %q, want abc123", rc.Commit)
	}
	if rc.StartedAt.IsZero() || rc.CompletedAt.IsZero() {
		t.Error("receipt timestamps must be set")
	}
	if len(rc.Services) != 0 {
		t.Errorf("failed receipt services = %v, want empty (never converged)", rc.Services)
	}
}

func TestReconcile_HealthFailure_RecordsFailedReceipt(t *testing.T) {
	// A deployment that fails health verification is recorded as OutcomeFailed,
	// since the cycle did not converge (docs/ACCORDA.md §11).
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	unhealthy := health.New(time.Unix(0, 0))
	unhealthy.SetService("api", health.StatusUnhealthy, "exit 1")
	unhealthy.Summarize()
	tgt := &fakeTarget{health: &unhealthy, changedPlan: true}
	store := &fakeStore{}
	r := New(src, tgt, events.NewBus()).
		WithEnvironment("production").
		WithReceiptStore(store)

	res := r.Reconcile(context.Background())
	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended receipts = %d, want 1", len(store.appended))
	}
	if rc := store.appended[0]; rc.Result != history.OutcomeFailed {
		t.Errorf("receipt result = %q, want %q", rc.Result, history.OutcomeFailed)
	}
}

func TestChangedServices_SortedAndDeduped(t *testing.T) {
	// changedServices returns the sorted, unique changed service names
	// regardless of action order or duplicates (docs/DECISIONS.md #12). The
	// raw order must be asserted (not re-sorted after the fact) so a
	// non-deterministic helper fails the test.
	p := plan.New("dep_1", "acme/infra", "abc123", time.Unix(0, 0)).
		AddAction(plan.Action{Kind: plan.ActionRecreate, Service: "worker"}).
		AddAction(plan.Action{Kind: plan.ActionNoop, Service: "redis"}).
		AddAction(plan.Action{Kind: plan.ActionPull, Service: "api"}).
		AddAction(plan.Action{Kind: plan.ActionRecreate, Service: "api"})
	want := []string{"api", "worker"}
	if got := changedServices(p); !reflect.DeepEqual(got, want) {
		t.Errorf("changedServices = %v, want %v", got, want)
	}
}

func TestChangedServices_Empty(t *testing.T) {
	// A no-op plan has no changed services.
	p := plan.New("dep_1", "acme/infra", "abc123", time.Unix(0, 0)).
		AddAction(plan.Action{Kind: plan.ActionNoop, Service: "api"})
	if got := changedServices(p); len(got) != 0 {
		t.Errorf("changedServices(noop) = %v, want empty", got)
	}
}

func TestNewDeploymentID_IsUniqueAndPrefixed(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := newDeploymentID()
		if len(id) < 4 || id[:4] != "dep_" {
			t.Fatalf("deployment id %q does not start with dep_", id)
		}
		if seen[id] {
			t.Fatalf("deployment id %q collided", id)
		}
		seen[id] = true
	}
}

func TestReconcile_ApplyFailure_RollsBackAndRecordsReceipt(t *testing.T) {
	// A deployment that fails during apply, with a previous deployment
	// available, must be rolled back to the previous commit and record an
	// OutcomeRolledBack receipt carrying the restored commit
	// (docs/ACCORDA.md §20).
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &fakeTarget{applyErr: errors.New("apply boom"), changedPlan: true}
	prev := &state.DeployedState{
		DeploymentID: "dep_0",
		Commit:       "prev123",
		Services:     map[string]state.Service{"api": {Image: "api:1"}},
	}
	store := &fakeStore{}
	r := New(src, tgt, events.NewBus()).
		WithEnvironment("production").
		WithReceiptStore(store).
		WithPrevious(prev)

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
	if !res.RolledBack {
		t.Fatal("RolledBack = false, want true")
	}
	if res.RolledBackTo != "prev123" {
		t.Errorf("RolledBackTo = %q, want prev123", res.RolledBackTo)
	}
	// The rollback re-planned and re-applied the previous services.
	if len(tgt.applied) != 1 {
		t.Fatalf("applied plans = %d, want 1 (rollback)", len(tgt.applied))
	}
	if tgt.applied[0].Commit != "prev123" {
		t.Errorf("rollback commit = %q, want %q", tgt.applied[0].Commit, "prev123")
	}

	// History records the failed deployment and the rollback.
	if len(store.appended) != 2 {
		t.Fatalf("appended receipts = %d, want 2 (failed + rolled_back)", len(store.appended))
	}
	rb := store.appended[1]
	if rb.Result != history.OutcomeRolledBack {
		t.Errorf("receipt result = %q, want %q", rb.Result, history.OutcomeRolledBack)
	}
	if rb.Commit != "prev123" {
		t.Errorf("receipt commit = %q, want prev123", rb.Commit)
	}
	if rb.DeploymentID == "" {
		t.Error("rollback receipt deployment id should reuse the failed cycle id")
	}
	if rb.Environment != "production" {
		t.Errorf("receipt environment = %q, want production", rb.Environment)
	}
}

func TestReconcile_HealthFailure_NoPrevious_NoRollback(t *testing.T) {
	// When a deployment fails health verification but there is no previous
	// deployment (empty history), rollback is unsafe and the failure must
	// stand: no rollback, no rolled-back receipt (docs/ACCORDA.md §20
	// "where safely possible").
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	unhealthy := health.New(time.Unix(0, 0))
	unhealthy.SetService("api", health.StatusUnhealthy, "exit 1")
	unhealthy.Summarize()
	tgt := &fakeTarget{health: &unhealthy, changedPlan: true}
	store := &fakeStore{}
	r := New(src, tgt, events.NewBus()).WithReceiptStore(store)

	res := r.Reconcile(context.Background())

	if res.Phase != PhaseFailed {
		t.Fatalf("Phase = %q, want %q", res.Phase, PhaseFailed)
	}
	if res.RolledBack {
		t.Error("RolledBack = true, want false (no previous deployment)")
	}
	if res.RolledBackTo != "" {
		t.Errorf("RolledBackTo = %q, want empty", res.RolledBackTo)
	}
	// Only the deploy-phase Apply runs; the failure is not rolled back, so
	// no second Apply is issued for the previous deployment.
	if len(tgt.applied) != 1 {
		t.Errorf("applied plans = %d, want 1 (deploy only, no rollback)", len(tgt.applied))
	}
	// Only the failed deployment is recorded; no rollback receipt.
	if len(store.appended) != 1 {
		t.Fatalf("appended receipts = %d, want 1 (failed only)", len(store.appended))
	}
	if rc := store.appended[0]; rc.Result != history.OutcomeFailed {
		t.Errorf("receipt result = %q, want %q", rc.Result, history.OutcomeFailed)
	}
}

// applyDesiredTarget is a fakeTarget that also implements the desiredApplier
// capability, recording the desired states passed to ApplyDesired.
type applyDesiredTarget struct {
	fakeTarget
	applyDesired []*state.DesiredState
}

func (f *applyDesiredTarget) ApplyDesired(_ context.Context, desired *state.DesiredState) (*plan.Plan, error) {
	f.applyDesired = append(f.applyDesired, desired)
	p := plan.New("", desired.Repository, desired.Commit, time.Unix(0, 0))
	for name, svc := range desired.Services {
		p.AddAction(plan.Action{Kind: plan.ActionRecreate, Service: name, Image: svc.Image})
	}
	return p, nil
}

func TestReconcile_ApplyFailure_RollsBackViaApplyDesired(t *testing.T) {
	// A target that implements the desiredApplier capability is rolled back
	// via ApplyDesired, which receives the previous desired state (restored
	// commit + services) directly (docs/ACCORDA.md §20).
	src := &fakeSource{
		commit:  sources.Commit{SHA: "abc123"},
		desired: healthyDesired(),
	}
	tgt := &applyDesiredTarget{}
	tgt.applyErr = errors.New("apply boom")
	tgt.changedPlan = true
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
		t.Fatal("RolledBack = false, want true")
	}
	if res.RolledBackTo != "prev123" {
		t.Errorf("RolledBackTo = %q, want prev123", res.RolledBackTo)
	}
	// The rollback went through ApplyDesired with the previous state.
	if len(tgt.applyDesired) != 1 {
		t.Fatalf("ApplyDesired calls = %d, want 1", len(tgt.applyDesired))
	}
	got := tgt.applyDesired[0]
	if got.Commit != "prev123" {
		t.Errorf("ApplyDesired commit = %q, want prev123", got.Commit)
	}
	if got.Services["api"].Image != "api:1" {
		t.Errorf("ApplyDesired api.Image = %q, want api:1", got.Services["api"].Image)
	}
	// The fake's Plan path must not have been used for the rollback.
	if len(tgt.fakeTarget.applied) != 0 {
		t.Errorf("Plan-based rollback applied %d plans, want 0 (ApplyDesired used)", len(tgt.fakeTarget.applied))
	}
}

package reconcile

import (
	"context"
	"errors"
	"fmt"

	"accorda/internal/core/events"
	"accorda/internal/core/health"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
	"accorda/internal/sources"
	"accorda/internal/targets"
)

// Phase is a step in the reconciliation lifecycle (docs/ACCORDA.md §6). The
// lifecycle walks DETECTED → FETCHING → VALIDATING → PLANNING → PULLING →
// DEPLOYING → VERIFYING → HEALTHY → SYNCED, with failure paths to FAILED and
// rollback to a known previous deployment.
type Phase string

const (
	// PhaseDetected marks the start of a reconciliation cycle.
	PhaseDetected Phase = "DETECTED"
	// PhaseFetching marks the source being fetched.
	PhaseFetching Phase = "FETCHING"
	// PhaseValidating marks desired state and target validation.
	PhaseValidating Phase = "VALIDATING"
	// PhasePlanning marks plan generation.
	PhasePlanning Phase = "PLANNING"
	// PhasePulling marks image pulling before deployment.
	PhasePulling Phase = "PULLING"
	// PhaseDeploying marks the apply step.
	PhaseDeploying Phase = "DEPLOYING"
	// PhaseVerifying marks health verification.
	PhaseVerifying Phase = "VERIFYING"
	// PhaseHealthy marks a healthy deployment.
	PhaseHealthy Phase = "HEALTHY"
	// PhaseSynced marks a fully converged deployment.
	PhaseSynced Phase = "SYNCED"
	// PhaseFailed marks a failed deployment.
	PhaseFailed Phase = "FAILED"
)

// DriftPolicy selects how the reconciler reacts to runtime drift
// (docs/ACCORDA.md §5.3). Drift is the situation where Git and Accorda agree
// but the runtime has diverged (for example a service was stopped manually).
type DriftPolicy string

const (
	// DriftReport emits DriftDetected but does not repair. It is the default,
	// mirroring the config default (docs/ACCORDA.md §5.3).
	DriftReport DriftPolicy = "report"
	// DriftRepair emits DriftDetected, re-plans and re-applies to restore the
	// desired runtime, then emits DriftReconciled.
	DriftRepair DriftPolicy = "repair"
	// DriftDisabled ignores drift entirely: no events, no repair.
	DriftDisabled DriftPolicy = "disabled"
)

// StateTransition is the payload of an EventStateTransition event. It records
// a lifecycle phase change so consumers can observe the reconciliation
// progress (docs/ACCORDA.md §6, §21).
type StateTransition struct {
	// From is the phase being left.
	From Phase
	// To is the phase being entered.
	To Phase
	// Commit is the Git commit being reconciled, when known.
	Commit string
	// DeploymentID is the deployment identifier, when assigned.
	DeploymentID string
	// Err is the failure cause when transitioning to PhaseFailed.
	Err error
}

// Result is the outcome of a reconciliation cycle. It reports the final
// phase, the desired/deployed/runtime comparison, the health assessment, and
// whether a rollback occurred (docs/ACCORDA.md §6, §20).
type Result struct {
	// Phase is the terminal phase reached.
	Phase Phase
	// Comparison is the desired/deployed/runtime comparison, populated once
	// the deployment has been applied and verified.
	Comparison state.Comparison
	// Health is the health assessment, populated after verification.
	Health *health.Health
	// RolledBack is true when a failed deployment was rolled back to the
	// previous deployment.
	RolledBack bool
	// Err is the failure cause when Phase is PhaseFailed.
	Err error
}

// Reconciler drives the reconciliation lifecycle (docs/ACCORDA.md §6). It
// orchestrates a Source and a Target through the lifecycle phases, emitting
// state transitions and deployment events on a Bus, and handling failure
// paths including rollback to a known previous deployment (§20).
//
// Reconciler depends only on the Source and Target interfaces, never on a
// concrete provider (docs/DECISIONS.md #3).
type Reconciler struct {
	source sources.Source
	target targets.Target
	bus    events.Bus
	// previous is the last successfully deployed state, used for rollback
	// when a deployment fails (docs/ACCORDA.md §20). It may be nil when
	// there is no prior deployment.
	previous *state.DeployedState
	// driftPolicy selects how the reconciler reacts to runtime drift
	// (docs/ACCORDA.md §5.3). It defaults to DriftReport.
	driftPolicy DriftPolicy
}

// New returns a Reconciler that orchestrates src and tgt, publishing events
// on bus. bus may be nil, in which case events are dropped.
func New(src sources.Source, tgt targets.Target, bus events.Bus) *Reconciler {
	return &Reconciler{source: src, target: tgt, bus: bus, driftPolicy: DriftReport}
}

// WithDriftPolicy sets how the reconciler reacts to runtime drift
// (docs/ACCORDA.md §5.3). It accepts DriftReport, DriftRepair, or
// DriftDisabled. The default is DriftReport, mirroring the config default.
func (r *Reconciler) WithDriftPolicy(policy DriftPolicy) *Reconciler {
	r.driftPolicy = policy
	return r
}

// WithPrevious sets the last successfully deployed state used for rollback
// (docs/ACCORDA.md §20). It is primarily useful for callers that persist
// deployment history; the reconcile loop supplies the previous deployment
// before each cycle.
func (r *Reconciler) WithPrevious(prev *state.DeployedState) *Reconciler {
	r.previous = prev
	return r
}

// Reconcile runs one full reconciliation cycle and returns its Result. It
// never panics on a nil source or target; a nil dependency is reported as a
// validation failure.
func (r *Reconciler) Reconcile(ctx context.Context) *Result {
	res := &Result{Phase: PhaseDetected}
	r.emit(ctx, events.EventDeploymentDetected, nil)

	if r.source == nil || r.target == nil {
		return r.fail(ctx, res, PhaseDetected, "", "", errors.New("reconcile: source and target are required"))
	}

	desired, commit, ok := r.fetchAndValidate(ctx, res)
	if !ok {
		return res
	}

	p, ok := r.plan(ctx, res, desired, commit)
	if !ok {
		return res
	}

	if !r.deploy(ctx, res, desired, commit, p) {
		return res
	}

	h, ok := r.verify(ctx, res, desired, commit, p)
	if !ok {
		return res
	}

	return r.sync(ctx, res, desired, commit, p, h)
}

// fetchAndValidate runs the FETCHING and VALIDATING phases. It returns the
// desired state and commit on success, or false when the cycle failed (res
// is already populated with the failure).
func (r *Reconciler) fetchAndValidate(ctx context.Context, res *Result) (*state.DesiredState, sources.Commit, bool) {
	r.transition(ctx, PhaseDetected, PhaseFetching, "", "", nil)
	commit, err := r.source.Fetch(ctx)
	if err != nil {
		r.fail(ctx, res, PhaseFetching, "", "", err)
		return nil, sources.Commit{}, false
	}

	r.transition(ctx, PhaseFetching, PhaseValidating, commit.SHA, "", nil)
	desired, err := r.source.Desired(ctx, &commit)
	if err != nil {
		r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
		return nil, sources.Commit{}, false
	}
	if err := desired.Validate(); err != nil {
		r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
		return nil, sources.Commit{}, false
	}
	if err := r.target.Validate(ctx); err != nil {
		r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
		return nil, sources.Commit{}, false
	}
	return desired, commit, true
}

// plan runs the PLANNING phase. It returns the plan on success, or false
// when the cycle failed.
func (r *Reconciler) plan(ctx context.Context, res *Result, desired *state.DesiredState, commit sources.Commit) (*plan.Plan, bool) {
	r.transition(ctx, PhaseValidating, PhasePlanning, commit.SHA, "", nil)
	p, err := r.target.Plan(ctx, desired, r.previous)
	if err != nil {
		r.fail(ctx, res, PhasePlanning, commit.SHA, "", err)
		return nil, false
	}
	return p, true
}

// deploy runs the PULLING and DEPLOYING phases. It returns false when the
// cycle failed (res is populated and a rollback was attempted).
func (r *Reconciler) deploy(ctx context.Context, res *Result, desired *state.DesiredState, commit sources.Commit, p *plan.Plan) bool {
	r.transition(ctx, PhasePlanning, PhasePulling, commit.SHA, p.DeploymentID, nil)
	r.transition(ctx, PhasePulling, PhaseDeploying, commit.SHA, p.DeploymentID, nil)
	// A no-op plan (only noop actions) performs no deployment work, so a
	// "deployment started" event would be misleading to consumers. Gate the
	// event on the plan actually changing the target (docs/DECISIONS.md #16).
	if p.Changed() {
		r.emit(ctx, events.EventDeploymentStarted, nil)
	}
	if err := r.target.Apply(ctx, p); err != nil {
		r.fail(ctx, res, PhaseDeploying, commit.SHA, p.DeploymentID, err)
		r.rollback(ctx, res, desired)
		return false
	}
	return true
}

// verify runs the VERIFYING phase. It returns the health assessment on
// success, or false when the cycle failed (res is populated and a rollback
// was attempted).
func (r *Reconciler) verify(ctx context.Context, res *Result, desired *state.DesiredState, commit sources.Commit, p *plan.Plan) (*health.Health, bool) {
	r.transition(ctx, PhaseDeploying, PhaseVerifying, commit.SHA, p.DeploymentID, nil)
	h, err := r.target.Health(ctx)
	if err != nil {
		if !errors.Is(err, targets.ErrNotImplemented) {
			r.fail(ctx, res, PhaseVerifying, commit.SHA, p.DeploymentID, err)
			r.rollback(ctx, res, desired)
			return nil, false
		}
		// Health verification is not implemented by this target (for example
		// the Stub, or a driver that has not yet landed its Health method).
		// Treat the deployment as healthy so the lifecycle can proceed to
		// SYNCED; the health gate is active for targets that implement
		// Health (docs/ACCORDA.md §19).
		return &health.Health{Deployed: true, Healthy: true}, true
	}
	if h == nil {
		// A nil Health with no error means the target has no health data to
		// report (for example no healthchecks are declared). Treat it as
		// healthy so a target without health checks is not failed and rolled
		// back; this mirrors the ErrNotImplemented bypass above.
		return &health.Health{Deployed: true, Healthy: true}, true
	}
	h.Deployed = true
	h.Summarize()
	// Only a genuinely unhealthy deployment fails verification and triggers
	// rollback. A deployment whose services have no healthchecks reports
	// Overall == StatusUnknown, which is not a failure: DEPLOYED, HEALTHY,
	// and SYNCED are distinct outcomes (docs/ACCORDA.md §19), and a target
	// without healthchecks is deployed but not health-verifiable, so it must
	// proceed rather than be rolled back. StatusStarting is likewise not a
	// failure; the target's Health is responsible for waiting out the
	// starting window (and reporting unhealthy on timeout).
	if h.Overall == health.StatusUnhealthy {
		r.fail(ctx, res, PhaseVerifying, commit.SHA, p.DeploymentID,
			fmt.Errorf("reconcile: health check failed: %s", h.Overall))
		r.rollback(ctx, res, desired)
		return nil, false
	}
	return h, true
}

// sync runs the HEALTHY and SYNCED phases, comparing desired against deployed
// and runtime, and returns the final result.
func (r *Reconciler) sync(ctx context.Context, res *Result, desired *state.DesiredState, commit sources.Commit, p *plan.Plan, h *health.Health) *Result {
	r.transition(ctx, PhaseVerifying, PhaseHealthy, commit.SHA, p.DeploymentID, nil)
	r.emit(ctx, events.EventHealthChanged, h)

	runtime, err := r.target.Current(ctx)
	if err != nil {
		return r.fail(ctx, res, PhaseHealthy, commit.SHA, p.DeploymentID, err)
	}
	// Note: DeployedState is synthesized from the freshly-fetched desired
	// state because there is no persisted deployment history yet. As a
	// result state.Compare can only observe DRIFTED (runtime divergence),
	// never OUT_OF_SYNC (desired != deployed), since desired and deployed
	// always agree by construction. This resolves once deployment receipts
	// and history are wired in (the WithPrevious seam already exists for
	// that); revisit when §7 receipts land.
	deployed := &state.DeployedState{
		DeploymentID: p.DeploymentID,
		Commit:       commit.SHA,
		Services:     desired.Services,
	}
	res.Comparison = state.Compare(desired, deployed, runtime)
	res.Health = h
	switch res.Comparison.Result {
	case state.ResultDrifted:
		res.Phase = PhaseHealthy
		r.handleDrift(ctx, res, desired, deployed)
		return res
	case state.ResultOutOfSync:
		res.Phase = PhaseHealthy
		return res
	}
	h.Synced = true
	res.Phase = PhaseSynced
	r.transition(ctx, PhaseHealthy, PhaseSynced, commit.SHA, p.DeploymentID, nil)
	// A no-op cycle (plan unchanged) still converges to SYNCED, but nothing
	// was actually deployed, so a "deployment succeeded" event would be
	// misleading. Gate it on the plan changing the target.
	if p.Changed() {
		r.emit(ctx, events.EventDeploymentSucceeded, nil)
	}
	return res
}

// handleDrift reacts to a drifted runtime according to the configured drift
// policy (docs/ACCORDA.md §5.3). Drift is an observable outcome (§21), so
// the DriftDetected event is emitted unless the policy is disabled; when the
// policy is repair, the desired runtime is restored and DriftReconciled is
// emitted on success.
//
// res.Comparison reflects the drift that was detected before repair ran: a
// successful repair applies the plan but does not re-read the runtime, so the
// Result still reports DRIFTED even though DriftReconciled was emitted. The
// repair is applied asynchronously (e.g. `docker compose up -d` returns before
// containers are running), so convergence is confirmed on the next
// reconciliation cycle rather than asserted here.
func (r *Reconciler) handleDrift(ctx context.Context, res *Result, desired *state.DesiredState, deployed *state.DeployedState) {
	switch r.driftPolicy {
	case DriftDisabled:
		return
	case DriftRepair:
		r.emit(ctx, events.EventDriftDetected, res.Comparison)
		if r.repairDrift(ctx, desired, deployed) {
			r.emit(ctx, events.EventDriftReconciled, res.Comparison)
		}
	default: // DriftReport; any unknown value degrades to report-only (the
		// config loader validates the policy upstream, so this is defensive).
		r.emit(ctx, events.EventDriftDetected, res.Comparison)
	}
}

// repairDrift re-plans and re-applies to restore the desired runtime after
// drift was detected (docs/ACCORDA.md §5.3). It returns true when the repair
// was applied successfully.
func (r *Reconciler) repairDrift(ctx context.Context, desired *state.DesiredState, deployed *state.DeployedState) bool {
	p, err := r.target.Plan(ctx, desired, deployed)
	if err != nil {
		return false
	}
	if err := r.target.Apply(ctx, p); err != nil {
		return false
	}
	return true
}

// fail records a failure, emits the transition to PhaseFailed and the
// DeploymentFailed event, and returns the updated result.
func (r *Reconciler) fail(ctx context.Context, res *Result, from Phase, commit, depID string, err error) *Result {
	res.Phase = PhaseFailed
	res.Err = err
	r.transition(ctx, from, PhaseFailed, commit, depID, err)
	r.emit(ctx, events.EventDeploymentFailed, nil)
	return res
}

// rollback restores the previous deployment when a deployment fails
// (docs/ACCORDA.md §20). It re-plans and re-applies the previous deployed
// services. When there is no previous deployment, or the rollback itself
// fails, it is a no-op and the failure stands.
func (r *Reconciler) rollback(ctx context.Context, res *Result, failed *state.DesiredState) {
	if r.previous == nil || r.previous.Commit == "" {
		return
	}
	prevDesired := &state.DesiredState{
		Repository: failed.Repository,
		Branch:     failed.Branch,
		Commit:     r.previous.Commit,
		Services:   r.previous.Services,
	}
	p, err := r.target.Plan(ctx, prevDesired, nil)
	if err != nil {
		return
	}
	if err := r.target.Apply(ctx, p); err != nil {
		return
	}
	res.RolledBack = true
	r.emit(ctx, events.EventDeploymentRolledBack, nil)
	// Note: §20 requires "Rollback events must be recorded in deployment
	// history", and internal/core/history documents that obligation. History
	// is not yet wired into the reconciler; when it is, this is where the
	// rollback record must be written (tracked by issue #14's follow-up).
}

// transition emits a state-transition event on the bus.
func (r *Reconciler) transition(ctx context.Context, from, to Phase, commit, depID string, err error) {
	r.emit(ctx, events.EventStateTransition, StateTransition{
		From:         from,
		To:           to,
		Commit:       commit,
		DeploymentID: depID,
		Err:          err,
	})
}

// emit publishes an event on the bus, dropping it when no bus is configured.
func (r *Reconciler) emit(ctx context.Context, eventType string, payload any) {
	if r.bus == nil {
		return
	}
	r.bus.Publish(ctx, events.Event{Type: eventType, Payload: payload})
}

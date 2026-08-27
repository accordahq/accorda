package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"accorda/internal/core/events"
	"accorda/internal/core/health"
	"accorda/internal/core/history"
	"accorda/internal/core/locking"
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
	// Commit is the newest Git commit this cycle converged or attempted.
	Commit string
	// Comparison is the desired/deployed/runtime comparison, populated once
	// the deployment has been applied and verified.
	Comparison state.Comparison
	// Health is the health assessment, populated after verification.
	Health *health.Health
	// RolledBack is true when a failed deployment was rolled back to the
	// previous deployment.
	RolledBack bool
	// RolledBackTo is the Git commit the failed deployment was rolled back
	// to, populated when RolledBack is true. It lets callers report exactly
	// which known previous deployment was restored (docs/ACCORDA.md §20).
	RolledBackTo string
	// Err is the failure cause when Phase is PhaseFailed.
	Err error
}

// desiredApplier is the optional capability a target may implement to apply
// an arbitrary desired state directly, bypassing the on-disk artifact that
// Plan/Apply operate on. Core depends on this interface (not a concrete
// driver) so rollback stays target-agnostic (docs/ACCORDA.md §20,
// docs/DECISIONS.md #3). A target that does not implement it is rolled back
// via Plan+Apply against the previously deployed services.
//
// Targets that materialize the desired state into an on-disk artifact (for
// example the Compose target writes the services file before `docker compose
// up -d`) must implement this so a rollback can restore the previous image
// rather than re-applying the failed one. The compose driver's Plan/Apply
// reads the on-disk file, so without this seam a rollback would recreate the
// failed deployment.
type desiredApplier interface {
	// ApplyDesired applies the given desired state to the target, returning
	// the plan that was applied (or an error). It is used for rollback, when
	// the target must converge to a known previous state that differs from
	// the state on disk.
	ApplyDesired(ctx context.Context, desired *state.DesiredState) (*plan.Plan, error)
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
	// previousNeedsHydration marks a deployment reconstructed from the
	// image-only receipt journal. Before planning, the reconciler replaces
	// that partial state with the complete desired model from its Git commit
	// so unchanged service configuration is not mistaken for drift.
	previousNeedsHydration bool
	// driftPolicy selects how the reconciler reacts to runtime drift
	// (docs/ACCORDA.md §5.3). It defaults to DriftReport.
	driftPolicy DriftPolicy
	// environment is the target environment the deployment applies to. It is
	// recorded in deployment receipts (docs/ACCORDA.md §7).
	environment string
	// receipts is the store deployment receipts are written to on a
	// successful deployment (docs/ACCORDA.md §7). It may be nil, in which
	// case receipts are not recorded.
	receipts history.Store
	// locker serializes complete cycles for a deployment target across
	// reconciler instances and processes (docs/ACCORDA.md §47).
	locker locking.Locker
	// cycleMu protects mutable per-cycle fields when one Reconciler is called
	// concurrently in-process, even when no external locker is configured.
	cycleMu sync.Mutex
	// pending is an unfinished deployment recovered from the receipt journal.
	pending *history.Receipt
	// recovering is true while the current plan resumes pending.
	recovering bool
	// startedAt is when the current reconciliation cycle began. It is set at
	// the start of Reconcile and used as the receipt's StartedAt timestamp.
	startedAt time.Time
	// failedDeploymentID is the deployment identifier of the current cycle
	// when it fails. A rollback receipt reuses it so the history links the
	// rollback to the failed deployment that triggered it
	// (docs/ACCORDA.md §20). It is empty when the cycle failed before a
	// deployment identifier was assigned.
	failedDeploymentID string
	// lastDesired is the last desired state successfully reconciled by this
	// process. It lets continuous polling distinguish an unchanged branch HEAD
	// and run only drift detection instead of planning target mutations again.
	lastDesired *state.DesiredState
	// unchanged is set by reconcileOnce when the current cycle took the
	// unchanged-HEAD drift path. Reconcile uses it to avoid the extra
	// in-flight-commit fetch that only full deployment cycles require.
	unchanged bool
}

// New returns a Reconciler that orchestrates src and tgt, publishing events
// on bus. bus may be nil, in which case events are dropped.
func New(src sources.Source, tgt targets.Target, bus events.Bus) *Reconciler {
	return &Reconciler{source: src, target: tgt, bus: bus, driftPolicy: DriftReport}
}

// WithEnvironment sets the target environment recorded in deployment receipts
// (docs/ACCORDA.md §7). It is informational and target-agnostic.
func (r *Reconciler) WithEnvironment(env string) *Reconciler {
	r.environment = env
	return r
}

// WithReceiptStore sets the store deployment receipts are written to on a
// successful deployment (docs/ACCORDA.md §7). A nil store disables receipt
// recording.
func (r *Reconciler) WithReceiptStore(s history.Store) *Reconciler {
	r.receipts = s
	return r
}

// WithLocker sets the target-scoped deployment lock. The lock covers fetch,
// apply, verification, and the final concurrent-commit check, ensuring two
// reconciliation cycles cannot race on the same target (docs/ACCORDA.md §47).
func (r *Reconciler) WithLocker(locker locking.Locker) *Reconciler {
	r.locker = locker
	return r
}

// WithDriftPolicy sets how the reconciler reacts to runtime drift
// (docs/ACCORDA.md §5.3). It accepts DriftReport, DriftRepair, or
// DriftDisabled. The default is DriftReport, mirroring the config default.
func (r *Reconciler) WithDriftPolicy(policy DriftPolicy) *Reconciler {
	r.driftPolicy = policy
	return r
}

// WithPrevious sets the last successfully deployed state used for rollback
// (docs/ACCORDA.md §20). When a receipt store contains a healthy deployment,
// the reconciler refreshes this value from that store after acquiring its
// target lock; the explicit value is the fallback for callers without history.
func (r *Reconciler) WithPrevious(prev *state.DeployedState) *Reconciler {
	r.previous = prev
	return r
}

// Reconcile runs one full reconciliation cycle and returns its Result. It
// never panics on a nil source or target; a nil dependency is reported as a
// validation failure.
func (r *Reconciler) Reconcile(ctx context.Context) *Result {
	r.cycleMu.Lock()
	defer r.cycleMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	unlock, err := r.acquireLock(ctx)
	if err != nil {
		res := &Result{Phase: PhaseDetected}
		return r.fail(ctx, res, PhaseDetected, "", "", err)
	}
	defer func() { _ = unlock() }()
	pending, previous, err := r.recoveryState(ctx)
	if err != nil {
		res := &Result{Phase: PhaseDetected}
		return r.fail(ctx, res, PhaseDetected, "", "", err)
	}
	r.pending = pending
	if previous != nil {
		r.previous = previous
		r.previousNeedsHydration = true
	}

	for {
		r.unchanged = false
		res := r.reconcileOnce(ctx)
		if res.Phase != PhaseSynced || ctx.Err() != nil {
			return res
		}
		if r.unchanged {
			return res
		}
		latest, fetchErr := r.source.Fetch(ctx)
		if fetchErr != nil || latest.SHA == "" || latest.SHA == res.Commit {
			return res
		}
		// A commit arrived while the deployment was in flight. Keep the target
		// lock and immediately reconcile the newest fetched HEAD. The next
		// cycle fetches again, so bursts collapse naturally to the latest SHA.
	}
}

func (r *Reconciler) reconcileOnce(ctx context.Context) *Result {
	res := &Result{Phase: PhaseDetected}
	r.startedAt = time.Now()
	r.failedDeploymentID = ""
	r.recovering = false
	r.emit(ctx, events.EventDeploymentDetected, nil)

	if r.source == nil || r.target == nil {
		return r.fail(ctx, res, PhaseDetected, "", "", errors.New("reconcile: source and target are required"))
	}

	commit, ok := r.fetch(ctx, res)
	if !ok {
		return res
	}
	res.Commit = commit.SHA
	// Durable recovery takes precedence over the cached-HEAD shortcut so an
	// unfinished receipt can be resumed and closed even when Git is unchanged.
	if r.pending == nil && r.lastDesired != nil && commit.SHA == r.lastDesired.Commit {
		r.unchanged = true
		return r.checkDrift(ctx, res, commit)
	}
	desired, ok := r.validate(ctx, res, commit)
	if !ok {
		return res
	}
	if err := r.hydratePrevious(ctx, desired); err != nil {
		return r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
	}
	if err := r.prepareRecovery(ctx, commit); err != nil {
		return r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
	}

	p, ok := r.plan(ctx, res, desired, commit)
	if !ok {
		return res
	}

	// Assign the deployment identifier before the deploy phase so the plan,
	// state transitions, and the eventual receipt all share one identifier
	// (docs/ACCORDA.md §7). The target's Plan leaves DeploymentID empty; the
	// reconcile loop owns identifier assignment.
	if p.DeploymentID == "" {
		p.DeploymentID = newDeploymentID()
	}
	if r.pending != nil && r.pending.Commit == commit.SHA {
		p.DeploymentID = r.pending.DeploymentID
		r.recovering = true
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

func (r *Reconciler) acquireLock(ctx context.Context) (locking.UnlockFunc, error) {
	if r.locker == nil {
		return func() error { return nil }, nil
	}
	return r.locker.Lock(ctx)
}

func (r *Reconciler) recoveryState(ctx context.Context) (*history.Receipt, *state.DeployedState, error) {
	if r.receipts == nil {
		return nil, nil, nil
	}
	receipts, err := r.receipts.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("reconcile: read deployment recovery journal: %w", err)
	}
	return history.Unfinished(receipts), deployedFromReceipts(receipts), nil
}

func deployedFromReceipts(receipts []history.Receipt) *state.DeployedState {
	for i := len(receipts) - 1; i >= 0; i-- {
		receipt := receipts[i]
		if receipt.Result != history.OutcomeHealthy {
			continue
		}
		services := make(map[string]state.Service, len(receipt.Services))
		for name, service := range receipt.Services {
			services[name] = state.Service{Image: service.Image}
		}
		return &state.DeployedState{
			DeploymentID: receipt.DeploymentID,
			Commit:       receipt.Commit,
			Services:     services,
		}
	}
	return nil
}

// hydratePrevious replaces an image-only receipt baseline with the complete
// service model declared at the deployed commit. When the current and deployed
// commits match, the already validated desired state is the exact baseline and
// no second source read is needed. For different commits, the source opens a
// historical revision and the target reloads it. Failing closed prevents a partial baseline from
// causing an unnecessary full recreation.
func (r *Reconciler) hydratePrevious(ctx context.Context, desired *state.DesiredState) error {
	if !r.previousNeedsHydration || r.previous == nil {
		return nil
	}
	previousDesired := desired
	if r.previous.Commit != desired.Commit {
		var err error
		previousDesired, err = r.loadDesired(ctx, &sources.Commit{SHA: r.previous.Commit})
		if err != nil && previousDesired == nil {
			return fmt.Errorf("reconcile: read previous desired state at %s: %w", r.previous.Commit, err)
		}
		if previousDesired == nil {
			return fmt.Errorf("reconcile: previous desired state at %s is nil", r.previous.Commit)
		}
		if err := previousDesired.Validate(); err != nil {
			return fmt.Errorf("reconcile: validate previous desired state at %s: %w", r.previous.Commit, err)
		}
	}
	services := previousDesired.Clone().Services
	r.previous = &state.DeployedState{
		DeploymentID: r.previous.DeploymentID,
		Commit:       r.previous.Commit,
		DeployedAt:   r.previous.DeployedAt,
		Services:     services,
	}
	r.previousNeedsHydration = false
	return nil
}

// prepareRecovery closes a pending deployment as interrupted when Git has
// moved on. If the commit is unchanged, the pending receipt stays active and
// its deployment ID is reused after planning so idempotent target operations
// resume only the work still required by current runtime state.
func (r *Reconciler) prepareRecovery(ctx context.Context, commit sources.Commit) error {
	if r.pending == nil || r.pending.Commit == commit.SHA {
		return nil
	}
	interrupted := r.pending.Clone()
	interrupted.Result = history.OutcomeInterrupted
	interrupted.CompletedAt = time.Now()
	if err := r.receipts.Append(ctx, interrupted); err != nil {
		return fmt.Errorf("reconcile: close superseded deployment %s: %w", interrupted.DeploymentID, err)
	}
	r.pending = nil
	return nil
}

// fetch runs the FETCHING phase and returns the tracked branch HEAD.
func (r *Reconciler) fetch(ctx context.Context, res *Result) (sources.Commit, bool) {
	r.transition(ctx, PhaseDetected, PhaseFetching, "", "", nil)
	commit, err := r.source.Fetch(ctx)
	if err != nil {
		r.fail(ctx, res, PhaseFetching, "", "", err)
		return sources.Commit{}, false
	}
	return commit, true
}

// validate asks the target to load and normalize desired state from the
// source-owned revision view, then validates both model and target runtime.
func (r *Reconciler) validate(ctx context.Context, res *Result, commit sources.Commit) (*state.DesiredState, bool) {
	r.transition(ctx, PhaseFetching, PhaseValidating, commit.SHA, "", nil)
	desired, err := r.loadDesired(ctx, &commit)
	if err != nil && desired == nil {
		r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
		return nil, false
	}
	if err != nil {
		r.emit(ctx, events.EventStateTransition, StateTransition{
			From: PhaseValidating, To: PhaseValidating,
			Commit: commit.SHA, Err: fmt.Errorf("revision cleanup: %w", err),
		})
	}
	if err := desired.Validate(); err != nil {
		r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
		return nil, false
	}
	if err := r.target.Validate(ctx); err != nil {
		r.fail(ctx, res, PhaseValidating, commit.SHA, "", err)
		return nil, false
	}
	return desired, true
}

func (r *Reconciler) loadDesired(ctx context.Context, commit *sources.Commit) (_ *state.DesiredState, err error) {
	revision, err := r.source.Revision(ctx, commit)
	if err != nil {
		return nil, err
	}
	desired, derr := r.target.Desired(ctx, revision)
	cerr := revision.Close()
	if derr != nil {
		return nil, errors.Join(derr, cerr)
	}
	if desired == nil {
		return nil, errors.Join(errors.New("reconcile: target desired state is nil"), cerr)
	}
	return desired, cerr
}

// checkDrift handles a polling cycle whose Git HEAD is unchanged. It reads
// health and runtime state and applies only the configured drift policy,
// avoiding plan, pull, and deploy operations that belong to a new Git
// revision.
func (r *Reconciler) checkDrift(ctx context.Context, res *Result, commit sources.Commit) *Result {
	cloned := r.lastDesired.Clone()
	desired := &cloned
	h, err := r.assessHealth(ctx)
	if err != nil {
		return r.fail(ctx, res, PhaseFetching, commit.SHA, "", err)
	}
	res.Health = h
	r.emit(ctx, events.EventHealthChanged, h)
	runtime, err := r.target.Current(ctx)
	if err != nil {
		return r.fail(ctx, res, PhaseFetching, commit.SHA, "", err)
	}
	deployed := &state.DeployedState{Commit: commit.SHA, Services: desired.Services}
	res.Comparison = state.Compare(desired, deployed, runtime)
	if res.Comparison.Result == state.ResultDrifted {
		res.Phase = PhaseHealthy
		if r.handleDrift(ctx, res, desired, deployed) {
			// A repair was applied; the pre-repair health snapshot is stale.
			// The repair is asynchronous (containers start after `up -d`
			// returns), so convergence is confirmed on the next cycle rather
			// than failing on the old health reading.
			return res
		}
		if h.Overall != health.StatusUnhealthy {
			return res
		}
	}
	if h.Overall == health.StatusUnhealthy {
		return r.fail(ctx, res, PhaseFetching, commit.SHA, "",
			fmt.Errorf("reconcile: health check failed: %s", h.Overall))
	}
	h.Synced = true
	res.Phase = PhaseSynced
	r.transition(ctx, PhaseFetching, PhaseSynced, commit.SHA, "", nil)
	return res
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
	changed := p.Changed()
	if changed && !r.recovering {
		if err := r.recordPending(ctx, desired, commit, p); err != nil {
			r.fail(ctx, res, PhasePulling, commit.SHA, p.DeploymentID, err)
			return false
		}
	}
	r.transition(ctx, PhasePulling, PhaseDeploying, commit.SHA, p.DeploymentID, nil)
	// A no-op plan (only noop actions) performs no deployment work, so a
	// "deployment started" event would be misleading to consumers. Gate the
	// event on the plan actually changing the target (docs/DECISIONS.md #15).
	if !changed {
		return true
	}
	r.emit(ctx, events.EventDeploymentStarted, nil)
	if err := r.target.Apply(ctx, p); err != nil {
		r.failedDeploymentID = p.DeploymentID
		r.fail(ctx, res, PhaseDeploying, commit.SHA, p.DeploymentID, err)
		r.recordReceipt(ctx, desired, commit, nil, p, history.OutcomeFailed)
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
	h, err := r.assessHealth(ctx)
	if err != nil {
		r.failedDeploymentID = p.DeploymentID
		r.fail(ctx, res, PhaseVerifying, commit.SHA, p.DeploymentID, err)
		r.recordReceipt(ctx, desired, commit, nil, p, history.OutcomeFailed)
		r.rollback(ctx, res, desired)
		return nil, false
	}
	// Only a genuinely unhealthy deployment fails verification and triggers
	// rollback. A deployment whose services have no healthchecks reports
	// Overall == StatusUnknown, which is not a failure: DEPLOYED, HEALTHY,
	// and SYNCED are distinct outcomes (docs/ACCORDA.md §19), and a target
	// without healthchecks is deployed but not health-verifiable, so it must
	// proceed rather than be rolled back. StatusStarting is likewise not a
	// failure; the target's Health is responsible for waiting out the
	// starting window (and reporting unhealthy on timeout).
	if h.Overall == health.StatusUnhealthy {
		r.failedDeploymentID = p.DeploymentID
		r.fail(ctx, res, PhaseVerifying, commit.SHA, p.DeploymentID,
			fmt.Errorf("reconcile: health check failed: %s", h.Overall))
		r.recordReceipt(ctx, desired, commit, nil, p, history.OutcomeFailed)
		r.rollback(ctx, res, desired)
		return nil, false
	}
	return h, true
}

// assessHealth reads and normalizes target health for both new deployments
// and unchanged-HEAD polling cycles. Targets without health data remain
// deployable and syncable, matching the lifecycle's existing behavior.
func (r *Reconciler) assessHealth(ctx context.Context) (*health.Health, error) {
	h, err := r.target.Health(ctx)
	if err != nil {
		if errors.Is(err, targets.ErrNotImplemented) {
			return &health.Health{Deployed: true, Healthy: true}, nil
		}
		return nil, err
	}
	if h == nil {
		return &health.Health{Deployed: true, Healthy: true}, nil
	}
	h.Deployed = true
	h.Summarize()
	return h, nil
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
	cloned := desired.Clone()
	r.lastDesired = &cloned
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
	if p.Changed() || r.recovering {
		r.emit(ctx, events.EventDeploymentSucceeded, nil)
		r.recordReceipt(ctx, desired, commit, runtime, p, history.OutcomeHealthy)
		r.pending = nil
	}
	r.previous = &state.DeployedState{
		DeploymentID: p.DeploymentID,
		Commit:       commit.SHA,
		Services:     desired.Clone().Services,
	}
	return res
}

// recordPending durably checkpoints a changed deployment before any target
// mutation. Unlike terminal receipt recording, checkpoint failure aborts the
// deployment: proceeding without it would make restart recovery ambiguous.
func (r *Reconciler) recordPending(ctx context.Context, desired *state.DesiredState, commit sources.Commit, p *plan.Plan) error {
	if r.receipts == nil {
		return nil
	}
	services := make(map[string]history.ServiceReceipt, len(desired.Services))
	for name, service := range desired.Services {
		services[name] = history.ServiceReceipt{Image: service.Image}
	}
	receipt := history.Receipt{
		DeploymentID: p.DeploymentID,
		Repository:   desired.Repository,
		Environment:  r.environment,
		Commit:       commit.SHA,
		StartedAt:    r.startedAt,
		Result:       history.OutcomeInProgress,
		Changes:      changedServices(p),
		Services:     services,
	}
	if err := r.receipts.Append(ctx, receipt); err != nil {
		return fmt.Errorf("reconcile: persist deployment checkpoint: %w", err)
	}
	r.pending = &receipt
	return nil
}

// recordReceipt writes a deployment receipt for a deployment
// (docs/ACCORDA.md §7, §11). A healthy receipt is recorded only when the plan
// actually changed the target, so a no-op cycle does not produce one; a failed
// receipt is always recorded, because the deploy phase was attempted and
// failed (a no-op plan never reaches a failure path). For a healthy deployment
// the receipt carries OutcomeHealthy, the changed service names, and the
// per-service image reference and resolved manifest digest read back from the
// runtime. For a failed deployment it carries OutcomeFailed and no digest data,
// so the history reflects the cycle that did not converge (docs/ACCORDA.md §11).
//
// Recording is best-effort: a store failure is not a deployment failure, so
// the cycle still reports its real outcome. The receipt is built from the
// runtime state (which carries the resolved digests) rather than the desired
// state, so the recorded digest reflects what is actually running.
func (r *Reconciler) recordReceipt(ctx context.Context, desired *state.DesiredState, commit sources.Commit, runtime *state.RuntimeState, p *plan.Plan, result history.Outcome) {
	if r.receipts == nil || (result == history.OutcomeHealthy && !p.Changed() && !r.recovering) {
		return
	}
	var services map[string]history.ServiceReceipt
	if runtime != nil {
		services = make(map[string]history.ServiceReceipt, len(runtime.Services))
		for name, svc := range runtime.Services {
			services[name] = history.ServiceReceipt{Image: svc.Image, Digest: svc.Digest}
		}
	}
	receipt := history.Receipt{
		DeploymentID: p.DeploymentID,
		Repository:   desired.Repository,
		Environment:  r.environment,
		Commit:       commit.SHA,
		StartedAt:    r.startedAt,
		CompletedAt:  time.Now(),
		Result:       result,
		Changes:      r.receiptChanges(p),
		Services:     services,
	}
	_ = r.receipts.Append(ctx, receipt)
}

func (r *Reconciler) receiptChanges(p *plan.Plan) []string {
	changes := changedServices(p)
	if !r.recovering || r.pending == nil {
		return changes
	}
	seen := make(map[string]struct{}, len(changes)+len(r.pending.Changes))
	for _, name := range append(changes, r.pending.Changes...) {
		seen[name] = struct{}{}
	}
	merged := make([]string, 0, len(seen))
	for name := range seen {
		merged = append(merged, name)
	}
	sort.Strings(merged)
	return merged
}

// changedServices returns the sorted, unique service names the plan changes
// (every action that is not a noop), so a receipt's Changes field is
// deterministic regardless of action order or duplicates
// (docs/ACCORDA.md §11, docs/DECISIONS.md #7).
func changedServices(p *plan.Plan) []string {
	if p == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, a := range p.Actions {
		if a.Kind == plan.ActionNoop {
			continue
		}
		seen[a.Service] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// handleDrift reacts to a drifted runtime according to the configured drift
// policy (docs/ACCORDA.md §5.3). Drift is an observable outcome (§21), so
// the DriftDetected event is emitted unless the policy is disabled; when the
// policy is repair, the desired runtime is restored and DriftReconciled is
// emitted on success. It returns true when a repair was applied successfully,
// so the caller can avoid failing on the pre-repair health snapshot — the
// repair is asynchronous (e.g. `docker compose up -d` returns before
// containers are running), so convergence is confirmed on the next
// reconciliation cycle rather than asserted here.
func (r *Reconciler) handleDrift(ctx context.Context, res *Result, desired *state.DesiredState, deployed *state.DeployedState) bool {
	switch r.driftPolicy {
	case DriftDisabled:
		return false
	case DriftRepair:
		r.emit(ctx, events.EventDriftDetected, res.Comparison)
		if r.repairDrift(ctx, desired, deployed) {
			r.emit(ctx, events.EventDriftReconciled, res.Comparison)
			return true
		}
		return false
	default: // DriftReport; any unknown value degrades to report-only (the
		// config loader validates the policy upstream, so this is defensive).
		r.emit(ctx, events.EventDriftDetected, res.Comparison)
		return false
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
//
// A target that implements the desiredApplier capability (for example the
// Compose target, which materializes the desired services into the on-disk
// Compose file before `docker compose up -d`) is rolled back by applying the
// previous desired state directly, so the on-disk artifact reflects the
// restored services. A target that only implements the Target interface is
// rolled back by re-planning and re-applying the previous deployed services.
//
// A successful rollback is recorded in the deployment history as an
// OutcomeRolledBack receipt (docs/ACCORDA.md §20: "Rollback events must be
// recorded in deployment history").
func (r *Reconciler) rollback(ctx context.Context, res *Result, failed *state.DesiredState) {
	if r.previous == nil || r.previous.Commit == "" {
		return
	}
	prevDesired := r.resolvePrevDesired(ctx, failed)
	applied, ok := r.applyRollback(ctx, prevDesired)
	if !ok {
		return
	}
	res.RolledBack = true
	res.RolledBackTo = r.previous.Commit
	r.emit(ctx, events.EventDeploymentRolledBack, nil)
	r.recordRollbackReceipt(ctx, prevDesired, applied)
}

// resolvePrevDesired restores the full desired state at the previous commit
// from the source so the rollback restores the complete service model
// (command, env, ports, volumes, healthcheck, ...), not just the image
// reference recorded in the receipt. It falls back to the recorded services
// if the source cannot be read (the "where safely possible" qualifier in
// docs/ACCORDA.md §20).
func (r *Reconciler) resolvePrevDesired(ctx context.Context, failed *state.DesiredState) *state.DesiredState {
	prevDesired := &state.DesiredState{
		Repository: failed.Repository,
		Branch:     failed.Branch,
		Commit:     r.previous.Commit,
		Services:   r.previous.Services,
	}
	if r.source == nil {
		return prevDesired
	}
	if ds, _ := r.loadDesired(ctx, &sources.Commit{SHA: r.previous.Commit}); ds != nil && len(ds.Services) > 0 {
		return ds
	}
	return prevDesired
}

// applyRollback converges the target to the previous desired state and returns
// the plan that was applied. A target that implements the desiredApplier
// capability (for example the Compose target, which materializes the desired
// services into the on-disk Compose file before `docker compose up -d`) is
// rolled back by applying the previous desired state directly, so the on-disk
// artifact reflects the restored services. A target that only implements the
// Target interface is rolled back by re-planning and re-applying the previous
// deployed services. It reports ok=false when the rollback cannot be applied.
func (r *Reconciler) applyRollback(ctx context.Context, prevDesired *state.DesiredState) (*plan.Plan, bool) {
	if applier, ok := r.target.(desiredApplier); ok {
		return r.applyDesiredRollback(ctx, applier, prevDesired)
	}
	p, err := r.target.Plan(ctx, prevDesired, nil)
	if err != nil {
		return nil, false
	}
	if err := r.target.Apply(ctx, p); err != nil {
		return nil, false
	}
	return p, true
}

// applyDesiredRollback rolls back through the desiredApplier capability,
// materializing the previous revision first when the source supports it.
func (r *Reconciler) applyDesiredRollback(ctx context.Context, applier desiredApplier, prevDesired *state.DesiredState) (*plan.Plan, bool) {
	if materializer, supported := r.source.(sources.RevisionMaterializer); supported {
		if err := materializer.Materialize(ctx, &sources.Commit{SHA: r.previous.Commit}); err != nil {
			return nil, false
		}
	}
	applied, err := applier.ApplyDesired(ctx, prevDesired)
	if err != nil {
		return nil, false
	}
	return applied, true
}

// recordRollbackReceipt writes a rollback receipt so the deployment history
// records that a failed deployment was restored to a known previous commit
// (docs/ACCORDA.md §20). It is best-effort: a store failure does not change
// the rollback outcome.
func (r *Reconciler) recordRollbackReceipt(ctx context.Context, desired *state.DesiredState, p *plan.Plan) {
	if r.receipts == nil {
		return
	}
	// Record the restored services so the rollback receipt reflects what was
	// actually restored, mirroring the healthy-receipt shape (docs/ACCORDA.md
	// §7, §20).
	services := make(map[string]history.ServiceReceipt, len(desired.Services))
	for name, svc := range desired.Services {
		services[name] = history.ServiceReceipt{Image: svc.Image}
	}
	receipt := history.Receipt{
		DeploymentID: r.failedDeploymentID,
		Repository:   desired.Repository,
		Environment:  r.environment,
		Commit:       desired.Commit,
		StartedAt:    r.startedAt,
		CompletedAt:  time.Now(),
		Result:       history.OutcomeRolledBack,
		Changes:      changedServices(p),
		Services:     services,
	}
	_ = r.receipts.Append(ctx, receipt)
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

// newDeploymentID returns a fresh, collision-resistant deployment identifier
// of the form "dep_<hex>", matching the spec's example "dep_01K..."
// (docs/ACCORDA.md §7). It is assigned by the reconcile loop, which owns
// deployment identifier assignment (docs/DECISIONS.md #15).
func newDeploymentID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively unreachable; fall back to a
		// time-based suffix so the identifier is still unique in practice.
		return fmt.Sprintf("dep_%d", time.Now().UnixNano())
	}
	return "dep_" + hex.EncodeToString(b[:])
}

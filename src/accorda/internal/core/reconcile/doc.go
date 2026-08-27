// Package reconcile drives the reconciliation lifecycle that turns a desired
// state into a converged runtime.
//
// Reconciliation walks the lifecycle described in the spec: detect, fetch,
// validate, plan, pull, deploy, verify, and report synced. It owns the
// failure paths, including rollback to a known previous deployment when
// verification or health checks fail, and drift repair when runtime state
// diverges from the desired state while Git is unchanged.
//
// This package contains the core reconciliation loop and state-machine logic
// only; the actual work against a deployment target is performed through the
// Target interface and target drivers under internal/targets.
//
// The Reconciler walks the lifecycle phases (DETECTED → FETCHING →
// VALIDATING → PLANNING → PULLING → DEPLOYING → VERIFYING → HEALTHY →
// SYNCED) and handles the failure paths to FAILED and rollback, emitting
// state transitions and deployment events on an events.Bus.
//
// When the runtime has drifted (desired == deployed but the runtime diverged,
// docs/ACCORDA.md §5.3), the Reconciler reacts according to its drift policy
// (WithDriftPolicy): report emits DriftDetected, repair additionally re-plans
// and re-applies to restore the desired runtime and emits DriftReconciled,
// and disabled ignores drift entirely.
//
// Deployment lifecycle is recorded through the history.Store configured via
// WithReceiptStore. Before target mutation, a changed deployment durably
// records OutcomeInProgress. A restart finds an unmatched checkpoint,
// re-plans against live runtime state, and resumes with the same deployment
// ID; a newer Git commit closes the old attempt as OutcomeInterrupted and is
// reconciled instead. Terminal outcomes are OutcomeHealthy, OutcomeFailed,
// and OutcomeRolledBack (docs/ACCORDA.md §7, §11, §47).
//
// WithLocker serializes the complete cycle for a target. After reaching
// SYNCED, the reconciler fetches once more while retaining the lock and
// immediately runs another cycle when Git changed during deployment
// (docs/ACCORDA.md §47).
//
// Run adds continuous polling around that lifecycle. It reconciles once
// immediately and then at sync.interval until its context is cancelled. An
// unchanged Git HEAD produces no target mutation but still evaluates health
// and reaches runtime comparison so configured drift reporting or repair
// remains active. Failed cycles do not stop the loop, allowing transient
// dependencies to recover.
//
// Ensemble extends the same lifecycle to several independent targets at once
// (docs/ACCORDA.md §49, Phase 5 — Multi-Project / Multi-Target Compose). An
// Ensemble fans cycles out to member Reconcilers concurrently and aggregates
// their results, so one agent can manage several Compose projects,
// repositories, and environments without sharing state between them; each
// member keeps its own lock and receipt store, so independent workloads
// reconcile concurrently and a failure in one does not block the others.
//
// Rollback restores the last known-healthy deployment when a deploy or
// health-verification phase fails (docs/ACCORDA.md §20). The previous
// deployment is supplied via WithPrevious; the full previous service model is
// restored by opening the previous source revision and asking the target to
// load it, falling back to the recorded image-only services when the revision
// cannot be read. A target that additionally implements the desiredApplier
// capability (for example the Compose target, which materializes the desired
// services into the on-disk Compose file) is restored by applying the
// previous desired state directly, so the on-disk artifact reflects the
// restored services rather than the failed ones. When there is no previous
// deployment, the failure stands and no rollback is attempted (the "where
// safely possible" qualifier in §20).
//
// See docs/ACCORDA.md §6 (Reconciliation Lifecycle), §7 (Deployment Receipts),
// §11 (history), §20 (Rollback), §47 (Reconciliation Hardening), and
// §5.3 (Runtime State) for the authoritative description.
package reconcile

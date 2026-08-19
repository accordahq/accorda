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
// Successful and failed deployments are recorded as deployment receipts
// (docs/ACCORDA.md §7, §11) through the history.Store configured via
// WithReceiptStore: a changed, SYNCED deployment records an OutcomeHealthy
// receipt with the runtime digests and changed services, a deploy or
// health-verification failure records an OutcomeFailed receipt, and a
// successful rollback records an OutcomeRolledBack receipt carrying the
// restored commit (docs/ACCORDA.md §20). Recording is best-effort; healthy
// receipts are gated on the plan changing the target.
//
// Rollback restores the last known-healthy deployment when a deploy or
// health-verification phase fails (docs/ACCORDA.md §20). The previous
// deployment is supplied via WithPrevious; a target that additionally
// implements the desiredApplier capability (for example the Compose target,
// which materializes the desired services into the on-disk Compose file) is
// restored by applying the previous desired state directly, so the on-disk
// artifact reflects the restored services rather than the failed ones. When
// there is no previous deployment, the failure stands and no rollback is
// attempted (the "where safely possible" qualifier in §20).
//
// See docs/ACCORDA.md §6 (Reconciliation Lifecycle), §7 (Deployment Receipts),
// §11 (history), §20 (Rollback), and §5.3 (Runtime State) for the
// authoritative description.
package reconcile

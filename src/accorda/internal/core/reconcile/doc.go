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
// See docs/ACCORDA.md §6 (Reconciliation Lifecycle) and §20 (Rollback) for
// the authoritative description.
package reconcile

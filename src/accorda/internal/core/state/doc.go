// Package state models the three states Accorda reasons about while
// reconciling a target: the desired state declared in Git, the state Accorda
// has successfully deployed, and the runtime state that is actually running.
//
// The distinction between desired, deployed, and runtime state is what allows
// Accorda to detect drift even when Git has not changed, for example when a
// service is stopped manually while the desired commit remains the same.
//
// Types and helpers live here so that the rest of core (plan, reconcile,
// health, history) can share a single representation of state instead of each
// package redefining its own.
//
// The Compare helper produces the SYNCED / OUT_OF_SYNC / DRIFTED result that
// the reconcile loop uses to decide whether to deploy, repair drift, or do
// nothing.
//
// Service.Hash computes the canonical SHA-256 hash of a service's normalized
// configuration (docs/ACCORDA.md §10). Compare uses it to detect definition
// changes beyond image and env (command, ports, volumes, networks, labels,
// healthcheck, depends_on) so a service is recreated when its configuration
// changed even though its image reference did not.
//
// See docs/ACCORDA.md §5 (Core Reconciliation Model) and §10 (Service
// Hashing) for the authoritative description of these concepts.
package state

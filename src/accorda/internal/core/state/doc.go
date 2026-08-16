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
// See docs/ACCORDA.md §5 (Core Reconciliation Model) for the authoritative
// description of these concepts.
package state

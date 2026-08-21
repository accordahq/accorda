// Package targets contains target drivers that reconcile desired state
// against concrete deployment targets.
//
// Accorda core is target-independent and interacts with targets through the
// Target interface (Validate, Current, Plan, Apply, Health). Concrete drivers
// such as Docker Compose and Kubernetes live here; additional target types
// (for example Helm) may be added later without changing core concepts.
// Operational capabilities outside reconciliation, such as fetching or
// following service logs, use focused optional interfaces such as LogTarget.
//
// See docs/ACCORDA.md §3 (Fundamental Architecture) and §45 (Phase 1 —
// Docker Compose OSS MVP) for the authoritative description.
package targets

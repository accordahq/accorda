// Package targets contains target drivers that reconcile desired state
// against concrete deployment targets.
//
// Accorda core is target-independent and interacts with targets through the
// Target interface (Desired, Validate, Current, Plan, Apply, Health). Concrete drivers
// such as Docker Compose and Kubernetes live here; additional target types
// (for example Helm) may be added later without changing core concepts.
// Operational capabilities outside reconciliation, such as fetching or
// following service logs, use focused optional interfaces such as LogTarget.
//
// In a multi-target project, TargetContext carries the specific config.Target
// being constructed (issue #103, docs/DECISIONS.md #53); builders read
// ctx.Target rather than ctx.Project.Target so the same driver serves both the
// legacy single-target and the plural targets: shapes.
//
// See docs/ACCORDA.md §3 (Fundamental Architecture) and §45 (Phase 1 —
// Docker Compose OSS MVP) for the authoritative description.
package targets

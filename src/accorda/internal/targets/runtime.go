package targets

import (
	"time"

	"accorda/internal/core/health"
	"accorda/internal/core/state"
)

// RuntimeHealth is the optional capability a target implements to derive an
// aggregate health.Health from a runtime state read via Target.Current
// (docs/ACCORDA.md §19). It lets read-only commands like `accorda status`
// render the same per-service and aggregate health the reconcile loop's
// Health phase uses, without the command layer importing a concrete driver
// or the shared Docker operations layer.
//
// A Docker target maps Docker healthcheck statuses; a Kubernetes target would
// map probe results; a systemd target would map service-unit states. A target
// that does not implement RuntimeHealth leaves the command to fall back to
// Target.Health (the live health phase) or report health as unknown.
type RuntimeHealth interface {
	// HealthFromRuntime maps the target's runtime state to a health.Health at
	// the given check time. It must not mutate runtime.
	HealthFromRuntime(runtime *state.RuntimeState, checkedAt time.Time) *health.Health
}

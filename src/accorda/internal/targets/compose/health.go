package compose

import (
	"context"
	"errors"
	"time"

	"accorda/internal/core/health"
	"accorda/internal/core/state"
	shareddocker "accorda/internal/docker"
)

// Health verifies the health of the currently deployed workloads
// (docs/ACCORDA.md §19). It reads the runtime state via Current and maps each
// service's Docker healthcheck status to a health.Status, then summarizes the
// per-service results into an aggregate health.Health.
//
// The mapping distinguishes the three deployment outcomes the spec calls out
// (deployed, healthy, synced) from the raw runtime state:
//
//   - a service with no healthcheck (Health == "") is reported as
//     health.StatusUnknown: Accorda cannot determine its health, so it is
//     neither healthy nor unhealthy.
//   - a service whose healthcheck is still "starting" is reported as
//     health.StatusStarting.
//   - a service whose healthcheck reports "healthy" is reported as
//     health.StatusHealthy.
//   - any other healthcheck status (for example "unhealthy") is reported as
//     health.StatusUnhealthy.
//
// Health honors the target's health timeout (docs/ACCORDA.md §19): it polls
// Current until every service is healthy, unhealthy, or unknown, or until the
// timeout elapses, whichever comes first. When the timeout elapses while
// services are still starting, those services are reported as unhealthy with
// a message naming the timeout, so a deployment that never becomes healthy is
// not silently declared successful. A service with no healthcheck is
// immediately unknown and does not block the wait.
//
// Health makes no changes to the target. It returns a non-nil error only when
// reading the runtime state fails; a deployment that is unhealthy is reported
// through the returned Health value, not an error, so the reconcile loop can
// distinguish "could not verify" from "verified and unhealthy".
func (t *Target) Health(ctx context.Context) (*health.Health, error) {
	if t == nil || t.docker == nil {
		return nil, errors.New("compose target: docker client is nil")
	}
	return shareddocker.WaitForHealthy(ctx, t.Current, t.healthTimeout)
}

// HealthFromRuntime maps a RuntimeState to a health.Health. It delegates to
// the shared Docker health mapping so the Compose target, the image target,
// and the `accorda status` CLI command all agree on what "healthy" means
// (docs/ACCORDA.md §19).
//
// It is exported so callers outside the compose package (for example the
// `accorda status` CLI command) can derive the current per-service and
// aggregate health from a runtime state read via Target.Current, matching the
// same mapping the reconcile loop's Health phase uses (docs/ACCORDA.md §19).
func HealthFromRuntime(runtime *state.RuntimeState, checkedAt time.Time) *health.Health {
	return shareddocker.HealthFromRuntime(runtime, checkedAt)
}

// HealthFromRuntime implements targets.RuntimeHealth so read-only commands
// like `accorda status` can derive health from a runtime state without
// importing the shared Docker operations layer (docs/ACCORDA.md §19).
func (t *Target) HealthFromRuntime(runtime *state.RuntimeState, checkedAt time.Time) *health.Health {
	return HealthFromRuntime(runtime, checkedAt)
}

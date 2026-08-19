package compose

import (
	"context"
	"errors"
	"time"

	"accorda/internal/core/health"
	"accorda/internal/core/state"
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
	if t == nil {
		return nil, errors.New("compose target: nil target")
	}
	if t.docker == nil {
		return nil, errors.New("compose target: docker client is nil")
	}

	timeout := t.healthTimeout
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}

	deadline := time.Now().Add(timeout)
	for {
		runtime, err := t.Current(ctx)
		if err != nil {
			return nil, err
		}
		h := healthFromRuntime(runtime, time.Now())
		if !hasStarting(h) {
			return h, nil
		}
		if time.Now().After(deadline) {
			markTimedOut(h, timeout)
			return h, nil
		}
		if err := sleepCtx(ctx, healthPollInterval); err != nil {
			return nil, err
		}
	}
}

// healthPollInterval is how long Health waits between polls of the runtime
// state while services are still starting (docs/ACCORDA.md §19). It is a var
// so tests can shorten it.
var healthPollInterval = 2 * time.Second

// healthFromRuntime maps a RuntimeState to a health.Health. Each service's
// Docker healthcheck status is translated to a health.Status; services with
// no healthcheck are reported as unknown.
func healthFromRuntime(runtime *state.RuntimeState, checkedAt time.Time) *health.Health {
	h := health.New(checkedAt)
	if runtime == nil {
		return &h
	}
	for name, svc := range runtime.Services {
		h.SetService(name, healthStatus(svc.Health), "")
	}
	h.Summarize()
	return &h
}

// markTimedOut converts every still-starting service to unhealthy with a
// message naming the timeout, then re-summarizes so the aggregate reflects
// the failure (docs/ACCORDA.md §19).
func markTimedOut(h *health.Health, timeout time.Duration) {
	for name, sh := range h.Services {
		if sh.Status == health.StatusStarting {
			h.SetService(name, health.StatusUnhealthy, "health check timed out after "+timeout.String())
		}
	}
	h.Summarize()
}

// healthStatus maps a Docker healthcheck status string to a health.Status.
// An empty status means the service has no healthcheck, which Accorda reports
// as unknown rather than healthy or unhealthy (docs/ACCORDA.md §19).
func healthStatus(s string) health.Status {
	switch s {
	case "healthy":
		return health.StatusHealthy
	case "starting":
		return health.StatusStarting
	case "":
		return health.StatusUnknown
	default:
		return health.StatusUnhealthy
	}
}

// hasStarting reports whether any service in h is still starting.
func hasStarting(h *health.Health) bool {
	if h == nil {
		return false
	}
	for _, sh := range h.Services {
		if sh.Status == health.StatusStarting {
			return true
		}
	}
	return false
}

// sleepCtx sleeps for d, returning early with ctx.Err when ctx is canceled or
// its deadline expires before d elapses.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

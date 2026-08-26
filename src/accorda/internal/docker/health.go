package docker

import (
	"context"
	"time"

	"accorda/internal/core/health"
	"accorda/internal/core/state"
)

// healthPollInterval is how long Health waits between polls of the runtime
// state while services are still starting (docs/ACCORDA.md §19). It is a var
// so tests can shorten it.
var healthPollInterval = 2 * time.Second

// HealthFromRuntime maps a RuntimeState to a health.Health. Each service's
// Docker healthcheck status is translated to a health.Status; services with
// no healthcheck are reported as unknown.
//
// It is exported so callers outside the target packages (for example the
// `accorda status` CLI command) can derive the current per-service and
// aggregate health from a runtime state read via Target.Current, matching the
// same mapping the reconcile loop's Health phase uses (docs/ACCORDA.md §19).
func HealthFromRuntime(runtime *state.RuntimeState, checkedAt time.Time) *health.Health {
	h := health.New(checkedAt)
	if runtime == nil {
		return &h
	}
	for name, svc := range runtime.Services {
		h.SetService(name, HealthStatus(svc.Health), "")
	}
	h.Summarize()
	return &h
}

// MarkTimedOut converts every still-starting service to unhealthy with a
// message naming the timeout, then re-summarizes so the aggregate reflects
// the failure (docs/ACCORDA.md §19).
func MarkTimedOut(h *health.Health, timeout time.Duration) {
	for name, sh := range h.Services {
		if sh.Status == health.StatusStarting {
			h.SetService(name, health.StatusUnhealthy, "health check timed out after "+timeout.String())
		}
	}
	h.Summarize()
}

// HealthStatus maps a Docker healthcheck status string to a health.Status.
// An empty status means the service has no healthcheck, which Accorda reports
// as unknown rather than healthy or unhealthy (docs/ACCORDA.md §19).
func HealthStatus(s string) health.Status {
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

// HasStarting reports whether any service in h is still starting.
func HasStarting(h *health.Health) bool {
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

// SleepCtx sleeps for d, returning early with ctx.Err when ctx is canceled or
// its deadline expires before d elapses.
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// HealthPollInterval returns the poll interval Health uses while services are
// still starting. It is exported so tests can inspect and shorten it.
func HealthPollInterval() time.Duration { return healthPollInterval }

// SetHealthPollInterval overrides the poll interval. It is intended for tests
// that need to shorten the wait; production callers leave the default.
func SetHealthPollInterval(d time.Duration) { healthPollInterval = d }

// WaitForHealthy polls current until no service is still starting or the
// timeout elapses, returning the final health mapping (docs/ACCORDA.md §19).
// It is the shared poll loop both Docker targets use so the Health method
// does not duplicate the deadline → Current → HealthFromRuntime →
// HasStarting → MarkTimedOut → Sleep sequence.
//
// current reads the runtime state for one poll. timeout is the maximum time to
// wait; when it elapses while services are still starting, they are marked
// unhealthy with a timeout message. The health timeout defaults to
// DefaultHealthTimeout when zero.
func WaitForHealthy(ctx context.Context, current func(context.Context) (*state.RuntimeState, error), timeout time.Duration) (*health.Health, error) {
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		runtime, err := current(ctx)
		if err != nil {
			return nil, err
		}
		h := HealthFromRuntime(runtime, time.Now())
		if !HasStarting(h) {
			return h, nil
		}
		if time.Now().After(deadline) {
			MarkTimedOut(h, timeout)
			return h, nil
		}
		if err := SleepCtx(ctx, healthPollInterval); err != nil {
			return nil, err
		}
	}
}

// DefaultHealthTimeout is the maximum time Health waits for services to become
// healthy when no explicit timeout is configured (docs/ACCORDA.md §19).
const DefaultHealthTimeout = 120 * time.Second

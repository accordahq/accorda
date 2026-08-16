package health

import (
	"fmt"
	"time"
)

// Status is the health outcome for a service or deployment. The spec
// (docs/ACCORDA.md §19) stresses that DEPLOYED, HEALTHY, and SYNCED are
// distinct outcomes, not synonyms.
type Status string

const (
	// StatusUnknown means health could not be determined, for example
	// because the target has no health check for the service.
	StatusUnknown Status = "unknown"
	// StatusHealthy means the service passed its health check.
	StatusHealthy Status = "healthy"
	// StatusUnhealthy means the service failed its health check.
	StatusUnhealthy Status = "unhealthy"
	// StatusStarting means the service is running but its health check has
	// not yet passed or failed conclusively.
	StatusStarting Status = "starting"
)

// Health is the result of verifying that deployed workloads are actually
// healthy (docs/ACCORDA.md §19). It is a value type returned by
// Target.Health.
//
// Health distinguishes the three deployment outcomes the spec calls out:
// deployed (the apply step returned), healthy (health checks passed), and
// synced (deployed == desired and healthy). The Overall field captures the
// aggregate; the Services map captures per-service detail.
type Health struct {
	// Overall is the aggregate health of the deployment.
	Overall Status
	// Deployed is true when the apply step completed, regardless of health.
	Deployed bool
	// Healthy is true when all health checks have passed.
	Healthy bool
	// Synced is true when the deployment is both healthy and matches the
	// desired state.
	Synced bool
	// CheckedAt is when the health was assessed.
	CheckedAt time.Time
	// Services reports per-service health, keyed by service name.
	Services map[string]ServiceHealth
}

// ServiceHealth is the health of a single service.
type ServiceHealth struct {
	Status Status
	// Message is an optional human-readable detail, for example the
	// failing health check output.
	Message string
}

// New returns a Health value with Overall defaulted to StatusUnknown and the
// given checked-at time. Callers populate Services and then call Summarize
// to derive the aggregate fields.
func New(checkedAt time.Time) Health {
	return Health{
		Overall:   StatusUnknown,
		CheckedAt: checkedAt,
		Services:  make(map[string]ServiceHealth),
	}
}

// SetService records the health of a single service.
func (h *Health) SetService(name string, status Status, message string) {
	if h.Services == nil {
		h.Services = make(map[string]ServiceHealth)
	}
	h.Services[name] = ServiceHealth{Status: status, Message: message}
}

// Summarize derives the Overall, Healthy, and Synced fields from the
// per-service map. A deployment is healthy only when every service is
// healthy; if any service is unhealthy Overall is unhealthy; otherwise, if
// any service is still starting, Overall is starting; otherwise, if there
// are services and all are healthy, Overall is healthy. With no services
// Overall remains unknown.
//
// Summarize does not set Deployed or Synced; the caller sets Deployed from
// the apply result and Synced from the desired comparison, because those
// depend on information outside the health check.
func (h *Health) Summarize() {
	if len(h.Services) == 0 {
		h.Overall = StatusUnknown
		h.Healthy = false
		return
	}
	allHealthy := true
	anyStarting := false
	for _, sh := range h.Services {
		switch sh.Status {
		case StatusUnhealthy:
			h.Overall = StatusUnhealthy
			h.Healthy = false
			return
		case StatusStarting:
			anyStarting = true
			allHealthy = false
		case StatusHealthy:
			// counted below
		default:
			allHealthy = false
		}
	}
	switch {
	case anyStarting:
		h.Overall = StatusStarting
		h.Healthy = false
	case allHealthy:
		h.Overall = StatusHealthy
		h.Healthy = true
	default:
		h.Overall = StatusUnknown
		h.Healthy = false
	}
}

// Clone returns a deep copy of h.
func (h Health) Clone() Health {
	out := h
	out.Services = make(map[string]ServiceHealth, len(h.Services))
	for k, v := range h.Services {
		out.Services[k] = v
	}
	return out
}

// String returns a compact, human-readable summary suitable for CLI output.
func (h Health) String() string {
	return fmt.Sprintf("overall=%s deployed=%v healthy=%v synced=%v services=%d",
		h.Overall, h.Deployed, h.Healthy, h.Synced, len(h.Services))
}

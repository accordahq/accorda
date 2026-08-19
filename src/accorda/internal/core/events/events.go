package events

import (
	"context"
	"sync"
)

// Event type names for the core events described in docs/ACCORDA.md §21.
// Consumers (local journal, webhooks, chat tools, Accorda Cloud) subscribe to
// these instead of core depending on any one of them.
const (
	// EventDeploymentDetected marks the start of a reconciliation cycle.
	EventDeploymentDetected = "deployment.detected"
	// EventDeploymentStarted marks the deploy phase beginning.
	EventDeploymentStarted = "deployment.started"
	// EventDeploymentSucceeded marks a successful, healthy deployment.
	EventDeploymentSucceeded = "deployment.succeeded"
	// EventDeploymentFailed marks a failed deployment.
	EventDeploymentFailed = "deployment.failed"
	// EventDeploymentRolledBack marks a rollback to a previous deployment.
	EventDeploymentRolledBack = "deployment.rolled_back"
	// EventDriftDetected marks runtime drift being observed.
	EventDriftDetected = "drift.detected"
	// EventDriftReconciled marks drift being repaired.
	EventDriftReconciled = "drift.reconciled"
	// EventHealthChanged marks a change in workload health.
	EventHealthChanged = "health.changed"
	// EventAuthorizationRequired marks an authorization request.
	EventAuthorizationRequired = "authorization.required"
	// EventAuthorizationGranted marks an authorization grant.
	EventAuthorizationGranted = "authorization.granted"
	// EventAuthorizationRejected marks an authorization rejection.
	EventAuthorizationRejected = "authorization.rejected"
	// EventStateTransition marks a reconciliation lifecycle phase change
	// (docs/ACCORDA.md §6). The payload is a StateTransition.
	EventStateTransition = "state.transition"
)

// Event is a generic core event (docs/ACCORDA.md §21). Type identifies the
// event kind; Payload carries kind-specific data. Payload is opaque so the
// bus stays provider- and phase-agnostic.
type Event struct {
	Type    string
	Payload any
}

// Handler receives events published on a Bus. Handlers must be safe for
// concurrent use because Publish may invoke them from multiple goroutines.
type Handler func(ctx context.Context, e Event)

// Bus publishes events to subscribers. Subscribers receive events in
// subscription order, synchronously, so a handler observes events in the
// order they were published.
type Bus interface {
	// Publish delivers e to every current subscriber in subscription order.
	Publish(ctx context.Context, e Event)
	// Subscribe registers h and returns an unsubscribe function. Calling the
	// returned function removes h from the bus; it is safe to call more than
	// once.
	Subscribe(h Handler) func()
}

// NewBus returns an in-memory Bus. It is the default publication mechanism
// used by core; concrete delivery adapters live under internal/notifications.
func NewBus() Bus { return &bus{} }

// bus is the in-memory Bus implementation. It is safe for concurrent use.
type bus struct {
	mu       sync.Mutex
	handlers []Handler
}

func (b *bus) Publish(ctx context.Context, e Event) {
	b.mu.Lock()
	handlers := append([]Handler(nil), b.handlers...)
	b.mu.Unlock()
	for _, h := range handlers {
		if h != nil {
			h(ctx, e)
		}
	}
}

func (b *bus) Subscribe(h Handler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, h)
	idx := len(b.handlers) - 1
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.handlers[idx] = nil
	}
}

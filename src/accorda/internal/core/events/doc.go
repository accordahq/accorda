// Package events exposes generic core events rather than provider-specific
// callbacks.
//
// Examples of core events include DeploymentDetected,
// DeploymentStarted, DeploymentSucceeded, DeploymentFailed,
// DeploymentRolledBack, DriftDetected, DriftReconciled, HealthChanged, and
// authorization events. Consumers such as the local journal, generic
// webhooks, Git provider integrations, chat tools, and Accorda Cloud subscribe
// to these events instead of core depending on any one of them.
//
// This package defines the event types and an in-memory publication
// mechanism (Bus) that core uses to emit lifecycle and deployment events.
// Concrete notification delivery lives under internal/notifications.
//
// See docs/ACCORDA.md §21 (Events) for the authoritative description.
package events

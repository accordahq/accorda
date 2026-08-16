// Package notifications delivers core events to external destinations.
//
// Core exposes generic events through internal/core/events; this package
// contains the delivery adapters that forward those events to destinations
// such as generic webhooks, chat tools (Slack, Discord, ntfy), Git provider
// statuses, and Accorda Cloud. Core does not depend on any specific
// notification destination.
//
// See docs/ACCORDA.md §21 (Events) and §39 (Cloud Notifications) for the
// authoritative description.
package notifications

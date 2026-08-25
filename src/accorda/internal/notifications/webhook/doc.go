// Package webhook implements the generic outbound webhook notification
// target (docs/ACCORDA.md §21). It subscribes to the core event bus and
// delivers each event as a JSON POST to a configurable URL, with bounded
// retry on transient failures and redaction of secret values in the payload.
//
// The consumer depends only on internal/core/events, internal/secrets, and
// the value types core publishes; it never imports a concrete source, target,
// or provider, so core stays provider-agnostic (docs/DECISIONS.md #3).
// Delivery is best-effort and asynchronous: Handle dispatches each event to a
// concurrency-bounded goroutine so a slow or unreachable endpoint never
// blocks reconciliation, and errors are reported to the caller-supplied error
// sink rather than returned to the reconciler.
//
// When an operator enables the shared-secret signature, the receiver should
// verify the X-Accorda-Signature header using a constant-time comparison
// (crypto/hmac.Equal) rather than an ordinary string == comparison, so the
// HMAC comparison does not leak timing information.
package webhook

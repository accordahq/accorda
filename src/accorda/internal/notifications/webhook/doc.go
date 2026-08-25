// Package webhook implements the generic outbound webhook notification
// target (docs/ACCORDA.md §21). It subscribes to the core event bus and
// delivers each event as a JSON POST to a configurable URL, with bounded
// retry on transient failures and redaction of secret values in the payload.
//
// The consumer depends only on internal/core/events and internal/secrets; it
// never imports a concrete source, target, or provider, so core stays
// provider-agnostic (docs/DECISIONS.md #3). Delivery is best-effort: a
// failed webhook does not block reconciliation, and errors are reported to
// the caller-supplied error sink rather than returned to the reconciler.
package webhook

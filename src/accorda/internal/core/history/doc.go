// Package history records deployment history so that Accorda can prove what
// was actually deployed, support rollback, and answer queries such as
// `accorda history`.
//
// A Receipt is the immutable record of a deployment (docs/ACCORDA.md §7, §11):
// the deployment identifier, repository, environment, commit, start/completion
// timestamps, the deployment Result (healthy or failed), the service names
// the deployment changed, and per-service image reference and resolved
// manifest digest. The digest is the point of the receipt — Git may declare a
// mutable tag, but Accorda records the immutable digest so it can answer
// "exactly which commit and image digest was running at time Y?". Both
// successful and failed deployments are recorded, so the history (the
// `accorda history` surface, §11) shows the RESULT of every cycle.
//
// Receipts are persisted through the Store interface. The default
// implementation, FileStore, writes an append-only JSON-lines journal on the
// local filesystem (docs/ACCORDA.md §21 "local journal", §42 "local history",
// §28 "local filesystem"), adding no dependency beyond the standard library
// (docs/DECISIONS.md #1).
//
// Rollback events must also be recorded here so the audit trail reflects
// attempted and active deployments accurately (docs/ACCORDA.md §20).
//
// See docs/ACCORDA.md §5.2 (Deployed State), §7 (Deployment Receipts), §11
// (history), §20 (Rollback), and the `accorda history` CLI surface in §45 for
// the authoritative description.
package history

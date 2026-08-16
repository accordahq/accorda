// Package history records deployment history so that Accorda can prove what
// was actually deployed, support rollback, and answer queries such as
// `accorda history`.
//
// History records include the deployed commit, deployment identifier, target,
// health outcome, and any rollback events. Rollback events must be recorded
// here so the audit trail reflects attempted and active deployments
// accurately.
//
// See docs/ACCORDA.md §5.2 (Deployed State), §20 (Rollback), and the
// `accorda history` CLI surface in §45 for the authoritative description.
package history

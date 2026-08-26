package targets

import (
	"context"

	"accorda/internal/core/state"
)

// DesiredProvider is the optional capability a target implements when its
// desired state is derived from its own configuration rather than from a
// Git-parsed artifact (docs/DECISIONS.md #24). A raw image target is the
// motivating case: its desired state is a single service built from the
// accorda.yaml `target.image`/`env`/`ports` fields, not a Compose file the
// Git source parses.
//
// When a target implements DesiredProvider, the reconcile loop uses the
// target's Desired result in place of the Git source's desired state for the
// planning, deployment, and drift-detection phases. The Git source still
// supplies the commit metadata (SHA, branch, repository) so receipts and
// history remain anchored to a Git revision; the target supplies the service
// model. A target that does not implement DesiredProvider keeps the existing
// source-driven behavior unchanged.
//
// desired is the desired state the source produced (carrying commit metadata
// and, for file-backed targets, the parsed services). A DesiredProvider may
// adopt the source's identifying fields (Repository, Branch, Commit,
// CommitTime) and replace only the Services with its config-derived model, so
// the rest of the loop sees a coherent desired state anchored to the same
// commit. It must not return a nil Services map.
type DesiredProvider interface {
	// Desired returns the target's config-derived desired state, anchored to
	// the commit metadata carried by desired. It must not mutate desired.
	Desired(ctx context.Context, desired *state.DesiredState) (*state.DesiredState, error)
}

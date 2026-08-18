// Package compose implements the Docker Compose target driver
// (docs/ACCORDA.md §8).
//
// The Compose target is the first production-quality target. Its load entry
// point, LoadFile, reads a Compose file from disk, normalizes it into
// Accorda's service model (state.Service), and validates required fields
// without executing anything. Parsing is delegated to the compose-go loader
// (github.com/compose-spec/compose-go/v2), which handles the full Compose
// schema including interpolation, extends, and profiles.
//
// The driver is exposed as Target, which implements the targets.Target
// interface (docs/ACCORDA.md §12). The Validate phase loads the Compose file
// and pings the Docker engine; the Current phase reads the runtime state of
// the project's containers via the Docker engine API and maps it back to
// Accorda service names using the Compose labels, returning a
// state.RuntimeState (docs/ACCORDA.md §5.3). The Plan phase computes the
// desired-vs-deployed diff (docs/ACCORDA.md §9) by delegating to
// plan.DriftActions, producing a per-service CHANGED/UNCHANGED plan without
// applying it. The Apply phase executes a plan by running the equivalent of
// `docker compose up -d` scoped to the changed services (docs/ACCORDA.md §9),
// delegating to a composeRunner seam so the `docker compose` CLI stays
// confined to this adapter. The Docker engine is reached through the Docker
// SDK (github.com/docker/docker), confined to this adapter via a dockerClient
// seam so core never imports it (docs/DECISIONS.md #3). Health remains
// stubbed until a later milestone.
package compose

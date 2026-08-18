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
// state.RuntimeState (docs/ACCORDA.md §5.3). The Docker engine is reached
// through the Docker SDK (github.com/docker/docker), confined to this adapter
// via a dockerClient seam so core never imports it (docs/DECISIONS.md #3).
// Plan, Apply, and Health remain stubbed until later milestones.
package compose

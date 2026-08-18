// Package compose implements the Docker Compose target driver
// (docs/ACCORDA.md §8).
//
// The Compose target is the first production-quality target. Its load entry
// point, LoadFile, reads a Compose file from disk, normalizes it into
// Accorda's service model (state.Service), and validates required fields
// without executing anything. This package only depends on the standard
// library and the shared core/state types so that Compose parsing stays
// free of target-specific runtime dependencies.
//
// The remainder of the Compose driver — reading runtime state, planning,
// applying, and health verification — is built on the structures produced
// here in later milestones.
package compose

// Package docker provides the shared Docker engine operations layer used by
// the Compose and image target drivers.
//
// Accorda reconciles desired state against a Docker engine through two
// adapters: the Compose target (docs/ACCORDA.md §8), which drives a
// multi-service Compose project, and the image target, which drives a single
// raw container from config fields. Both talk to the same Docker engine, so
// the client seam, runtime-state mapping, image digest resolution, and health
// mapping are shared rather than duplicated (docs/ACCORDA.md §12,
// docs/DECISIONS.md #3).
//
// The package owns the Docker SDK dependency
// (github.com/docker/docker): it exposes a narrow Client seam (a subset of
// the SDK APIClient surface) so core never imports the Docker SDK and tests
// can substitute a fake client without a running daemon. It also owns the
// mapping from an engine inspect response to Accorda's target-agnostic
// state.RuntimeService, the manifest-digest resolution used by deployment
// receipts (docs/ACCORDA.md §7), and the healthcheck-to-health.Status
// mapping used by the Health phase (docs/ACCORDA.md §19).
package docker

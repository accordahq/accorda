// Package image implements the raw single-image target driver
// (docs/DECISIONS.md #24).
//
// The image target reconciles a single container image declared directly in
// accorda.yaml, without a Compose file. It is a sibling of the Compose target
// (target.type: compose), not a "docker" type with file/image modes: the two
// targets have different DesiredState origins. The Compose driver builds a
// multi-service DesiredState by parsing a Compose file; the image driver
// builds a single-service DesiredState from the target.image, target.env,
// and target.ports config fields. Keeping them as separate target types
// avoids conflating the file-render, env-merge, and per-service override
// machinery (which an image target does not want) with the simpler per-image
// model (docs/DECISIONS.md #24).
//
// The driver implements the targets.Target interface
// (Validate, Current, Plan, Apply, Health) and the optional
// targets.DesiredProvider and targets.LogTarget capabilities. It shares the
// Docker engine client seam, runtime-state mapping, image digest resolution,
// health mapping, and image pull policy with the Compose driver through the
// internal/docker package so the Docker SDK dependency stays confined to the
// adapters and the two drivers do not diverge (docs/ACCORDA.md §12,
// docs/DECISIONS.md #3).
//
// Desired state is config-driven: the reconcile loop calls DesiredProvider to
// obtain a one-service DesiredState anchored to the Git commit the source
// fetched, so receipts and history stay Git-anchored while the service model
// comes from accorda.yaml. Apply runs the single container with `docker run`
// through a runner seam so the Docker CLI stays confined to this adapter,
// mirroring the Compose driver's composeRunner seam.
package image

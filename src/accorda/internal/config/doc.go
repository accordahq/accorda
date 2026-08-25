// Package config defines and loads the unified Accorda project file
// (accorda.yaml) described in docs/ACCORDA.md §8 (Docker Compose Target)
// and §25 (Unified Project Format).
//
// The project file is the single, target-agnostic description of an Accorda
// project: the version of the format, the environment name, the Git source to
// reconcile from, the deployment target, the sync cadence, the image pull
// policy, the reconciliation behavior, the health verification timeout, the
// secret references, and the notification channels.
//
// Load (and Parse) decode the YAML strictly — unknown fields are rejected —
// apply a small set of defaults, and then Validate the result, returning
// clear, field-oriented errors for invalid configuration. The loader does not
// assume a specific target type; target-specific requirements (for example
// that a Compose target names a compose file) are checked generically from the
// declared target type.
//
// A document is either a single Project or a multi-project Ensemble
// (docs/ACCORDA.md §49). ParseDocument and LoadDocument dispatch between the
// two shapes: a top-level projects: list selects the Ensemble, otherwise the
// document is a single Project. In an Ensemble the schema version, the sync
// cadence, and the image/drift/health defaults are shared at the document
// root and resolved into every named member (docs/DECISIONS.md #48); each
// member carries an operator-chosen name so one agent can reconcile several
// workloads (api, worker, monitoring, internal-tools) concurrently, each with
// its own source, target, and state paths.
package config

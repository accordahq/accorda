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
package config

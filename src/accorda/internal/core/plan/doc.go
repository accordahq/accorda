// Package plan builds the deployment plan that reconciles a desired state with
// a target's current state.
//
// A plan describes the concrete actions Accorda intends to take (for example
// pull an image, recreate a service, remove orphans) before any action is
// applied. Plans are produced by comparing desired state against deployed and
// runtime state, and are consumed by the reconcile package.
//
// The plan object is intended to be deterministic enough to eventually be
// hashed and signed, so that the same plan can be shared and audited across
// the OSS agent and Accorda Cloud.
//
// See docs/ACCORDA.md §6 (Reconciliation Lifecycle) and §31 (Deployment
// Plans) for the authoritative description.
package plan

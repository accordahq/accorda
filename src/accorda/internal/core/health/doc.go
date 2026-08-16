// Package health verifies that deployed workloads are actually healthy, which
// the spec treats as distinct from merely being deployed.
//
// A deployment is not considered successful solely because the apply step
// returned exit code zero. Accorda waits for target-defined health checks
// (for example Compose healthcheck directives) before declaring a deployment
// healthy, and reports the three distinct outcomes deployed, healthy, and
// synced separately.
//
// See docs/ACCORDA.md §19 (Health Verification) for the authoritative
// description.
package health

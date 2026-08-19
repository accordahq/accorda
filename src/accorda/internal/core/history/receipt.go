package history

import (
	"sort"
	"time"
)

// Receipt is the immutable record of a successful deployment
// (docs/ACCORDA.md §7). It captures the deployment identifier, the Git
// repository and commit that were deployed, the environment, the start and
// completion timestamps, and the per-service image reference and resolved
// manifest digest.
//
// The digest is the point of the receipt: Git may declare a mutable tag
// (e.g. "ghcr.io/acme/api:latest"), but Accorda records the immutable digest
// (e.g. "sha256:82af...") so it can answer "exactly which commit and image
// digest was running on target X at time Y?" (docs/ACCORDA.md §7).
//
// Receipt is a value type. The Services map is owned by the value; callers
// that need a snapshot must copy it (see Clone).
type Receipt struct {
	// DeploymentID is the unique identifier Accorda assigned to the
	// deployment, e.g. "dep_01K...".
	DeploymentID string `json:"deployment_id"`
	// Repository identifies the Git repository the deployment was read from.
	Repository string `json:"repository"`
	// Environment is the target environment the deployment applied to.
	Environment string `json:"environment"`
	// Commit is the Git commit SHA that was deployed.
	Commit string `json:"commit"`
	// StartedAt is when the deployment began.
	StartedAt time.Time `json:"started_at"`
	// CompletedAt is when the deployment completed successfully.
	CompletedAt time.Time `json:"completed_at"`
	// Services records, per service name, the image reference and resolved
	// manifest digest that were deployed.
	Services map[string]ServiceReceipt `json:"services"`
}

// ServiceReceipt records the image reference and resolved manifest digest for
// a single deployed service (docs/ACCORDA.md §7).
type ServiceReceipt struct {
	// Image is the image reference declared in Git, e.g.
	// "ghcr.io/acme/api:2.4.1".
	Image string `json:"image"`
	// Digest is the resolved manifest digest of the deployed image, e.g.
	// "sha256:91a...". It is empty when the target could not resolve it.
	Digest string `json:"digest"`
}

// Clone returns a deep copy of r so callers can mutate the copy without
// aliasing the original. A nil Services map stays nil.
func (r Receipt) Clone() Receipt {
	out := r
	if r.Services != nil {
		out.Services = make(map[string]ServiceReceipt, len(r.Services))
		for k, v := range r.Services {
			out.Services[k] = v
		}
	}
	return out
}

// SortedServiceNames returns the service names of the receipt in sorted
// order so serialization and display are deterministic regardless of Go's
// randomized map iteration order (docs/DECISIONS.md #12).
func (r Receipt) SortedServiceNames() []string {
	names := make([]string, 0, len(r.Services))
	for name := range r.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

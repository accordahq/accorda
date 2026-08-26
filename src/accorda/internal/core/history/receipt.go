package history

import (
	"sort"
	"time"
)

// Receipt is the immutable record of a deployment
// (docs/ACCORDA.md §7, §11). It captures the deployment identifier, the Git
// repository and commit that were deployed, the environment, the start and
// completion timestamps, the deployment outcome, the services that changed,
// and the per-service image reference and resolved manifest digest.
//
// A receipt records lifecycle checkpoints and terminal outcomes so a restart
// can distinguish unfinished work from healthy, failed, rolled-back, or
// superseded deployments (docs/ACCORDA.md §47).
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
	// CompletedAt is when the deployment finished (successfully or not).
	CompletedAt time.Time `json:"completed_at"`
	// Result is the deployment checkpoint or terminal outcome.
	Result Outcome `json:"result"`
	// Changes lists the service names the deployment changed, in sorted
	// order. It records the services the plan intended to change, so it is
	// populated even for a failed deployment (docs/ACCORDA.md §11 shows the
	// affected services on a failed row). It is empty only when the plan is
	// a no-op.
	Changes []string `json:"changes,omitempty"`
	// Services records, per service name, the image reference and resolved
	// manifest digest that were deployed. It is nil for a failed deployment.
	Services map[string]ServiceReceipt `json:"services,omitempty"`
}

// Outcome is the result of a deployment cycle as recorded in the deployment
// history (docs/ACCORDA.md §11: "✓ healthy" / "✗ failed").
type Outcome string

const (
	// OutcomeInProgress is written durably before target mutation begins. If
	// the agent exits without a terminal receipt for the same deployment ID,
	// the next cycle resumes it by re-planning against live runtime state.
	OutcomeInProgress Outcome = "in_progress"
	// OutcomeHealthy marks a deployment that converged and is healthy.
	OutcomeHealthy Outcome = "healthy"
	// OutcomeFailed marks a deployment that failed during apply or health
	// verification. Failures earlier in the lifecycle (fetch, validate,
	// plan) return before a deployment is attempted and do not record a
	// receipt.
	OutcomeFailed Outcome = "failed"
	// OutcomeRolledBack marks a deployment that failed and was rolled back to
	// a known previous healthy deployment (docs/ACCORDA.md §20). The receipt
	// carries the commit that was restored, so the history records both the
	// failed cycle and the rollback that followed it.
	OutcomeRolledBack Outcome = "rolled_back"
	// OutcomeInterrupted closes an in-progress deployment when recovery finds
	// that a newer commit superseded it. The newer commit is then reconciled.
	OutcomeInterrupted Outcome = "interrupted"
)

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
	if r.Changes != nil {
		out.Changes = append([]string(nil), r.Changes...)
	}
	return out
}

// SortedServiceNames returns the service names of the receipt in sorted
// order so serialization and display are deterministic regardless of Go's
// randomized map iteration order (docs/DECISIONS.md #7).
func (r Receipt) SortedServiceNames() []string {
	names := make([]string, 0, len(r.Services))
	for name := range r.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Unfinished returns the newest in-progress receipt without a later terminal
// receipt carrying the same deployment ID. It returns a clone so callers can
// safely prepare a recovery record without mutating journal data.
func Unfinished(receipts []Receipt) *Receipt {
	closed := make(map[string]struct{})
	for i := len(receipts) - 1; i >= 0; i-- {
		receipt := receipts[i]
		if receipt.DeploymentID == "" {
			continue
		}
		if receipt.Result == OutcomeInProgress {
			if _, ok := closed[receipt.DeploymentID]; !ok {
				clone := receipt.Clone()
				return &clone
			}
			continue
		}
		closed[receipt.DeploymentID] = struct{}{}
	}
	return nil
}

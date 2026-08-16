package state

import (
	"errors"
	"fmt"
	"time"
)

// DesiredState is what Git currently declares (docs/ACCORDA.md §5.1). It is
// the source of truth that Accorda drives the runtime toward.
//
// DesiredState is a value type: callers must be able to copy and compare it
// without aliasing mutable state. All fields are value types or pointers to
// data that the caller does not mutate after handing the value to core.
type DesiredState struct {
	// Repository identifies the Git repository the desired state was read
	// from, e.g. "acme/infra" or the full URL. It is informational and
	// target-agnostic.
	Repository string
	// Branch is the Git branch the commit was read from.
	Branch string
	// Commit is the Git commit SHA declaring the desired state.
	Commit string
	// CommitTime is the authored/committed timestamp of Commit, if known.
	CommitTime time.Time
	// Services is the set of services declared in Git at Commit, keyed by
	// service name. The map is owned by the DesiredState value; callers
	// that need to preserve a snapshot must copy it (see Clone).
	Services map[string]Service
}

// Service describes a single service as declared in the desired state. It is
// target-agnostic: a Compose service and a Kubernetes deployment both surface
// as a Service with an image reference and environment variables.
type Service struct {
	// Image is the container image reference declared in Git, e.g.
	// "ghcr.io/acme/api:2.4.1" or "ghcr.io/acme/api@sha256:91a...".
	Image string
	// Env is the environment variables declared for the service, keyed by
	// variable name. Secret values are referenced, never inlined.
	Env map[string]string
}

// DeployedState is what Accorda has successfully deployed
// (docs/ACCORDA.md §5.2). It records the commit and deployment identifier
// that the last successful reconciliation converged on.
type DeployedState struct {
	// DeploymentID is the unique identifier Accorda assigned to the
	// deployment, e.g. "dep_01K...".
	DeploymentID string
	// Commit is the Git commit that was deployed.
	Commit string
	// DeployedAt is when the deployment completed successfully.
	DeployedAt time.Time
	// Services is the set of services that were deployed, keyed by service
	// name.
	Services map[string]Service
}

// RuntimeState is what is actually running on the target right now
// (docs/ACCORDA.md §5.3). It is read back from the target via Target.Current
// and is what Accorda compares against DesiredState to detect drift even
// when Git has not changed.
type RuntimeState struct {
	// Services is the set of services actually running, keyed by service
	// name. A service present in DesiredState but absent here is drifted
	// (stopped or removed).
	Services map[string]RuntimeService
}

// RuntimeService describes a single running service. Its Status and Health
// fields distinguish "deployed" from "healthy" (docs/ACCORDA.md §19).
type RuntimeService struct {
	// Status is the runtime status reported by the target, e.g. "running",
	// "stopped", or "exited".
	Status string
	// Health is the health outcome for the running service, e.g. "healthy",
	// "unhealthy", or "" when the target has no health check.
	Health string
	// Image is the image reference actually running, which may differ from
	// the desired image when drift has occurred.
	Image string
}

// Snapshot is a combined view of the three states Accorda reasons about. It
// is the value the reconcile loop passes between phases so that each phase
// operates on a consistent, immutable picture of the world.
type Snapshot struct {
	Desired  *DesiredState
	Deployed *DeployedState
	Runtime  *RuntimeState
}

// Clone returns a deep copy of s. It allows callers to snapshot a desired
// state and mutate the copy without aliasing the original. Clone preserves
// the zero value: Clone of a zero-value DesiredState is safe and returns a
// usable empty copy.
func (s DesiredState) Clone() DesiredState {
	return DesiredState{
		Repository: s.Repository,
		Branch:     s.Branch,
		Commit:     s.Commit,
		CommitTime: s.CommitTime,
		Services:   cloneServices(s.Services),
	}
}

// Clone returns a deep copy of s. See DesiredState.Clone for the rationale.
func (s DeployedState) Clone() DeployedState {
	return DeployedState{
		DeploymentID: s.DeploymentID,
		Commit:       s.Commit,
		DeployedAt:   s.DeployedAt,
		Services:     cloneServices(s.Services),
	}
}

// Clone returns a deep copy of s. See DesiredState.Clone for the rationale.
func (s RuntimeState) Clone() RuntimeState {
	out := RuntimeState{Services: make(map[string]RuntimeService, len(s.Services))}
	for k, v := range s.Services {
		out.Services[k] = v.Clone()
	}
	return out
}

// Clone returns a deep copy of the service.
func (s Service) Clone() Service {
	return Service{
		Image: s.Image,
		Env:   cloneStringMap(s.Env),
	}
}

// Clone returns a deep copy of the runtime service.
func (s RuntimeService) Clone() RuntimeService {
	return s // RuntimeService has no reference-type fields.
}

// Validate reports whether s is internally consistent. It checks that the
// identifying fields are present and that every declared service has an
// image. It does not compare against any other state.
func (s DesiredState) Validate() error {
	if s.Commit == "" {
		return errors.New("desired state: commit is required")
	}
	for name, svc := range s.Services {
		if svc.Image == "" {
			return fmt.Errorf("desired state: service %q has no image", name)
		}
	}
	return nil
}

// Validate reports whether s is internally consistent. A deployed state must
// record both the deployment identifier and the commit that was deployed.
func (s DeployedState) Validate() error {
	if s.DeploymentID == "" {
		return errors.New("deployed state: deployment id is required")
	}
	if s.Commit == "" {
		return errors.New("deployed state: commit is required")
	}
	return nil
}

// Validate reports whether s is internally consistent. A runtime state is
// valid as long as every reported service has a status; an empty runtime
// state (no services running) is valid and represents a drifted or empty
// target.
func (s RuntimeState) Validate() error {
	for name, svc := range s.Services {
		if svc.Status == "" {
			return fmt.Errorf("runtime state: service %q has no status", name)
		}
	}
	return nil
}

// cloneServices returns a deep copy of m. A nil map stays nil so that a
// zero-value state round-trips through Clone without allocating.
func cloneServices(m map[string]Service) map[string]Service {
	if m == nil {
		return nil
	}
	out := make(map[string]Service, len(m))
	for k, v := range m {
		out[k] = v.Clone()
	}
	return out
}

// cloneStringMap returns a deep copy of m. A nil map stays nil.
func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

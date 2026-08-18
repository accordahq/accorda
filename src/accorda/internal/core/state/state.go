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
//
// The fields beyond Image and Env hold the normalized service definition
// loaded from a Docker Compose file (docs/ACCORDA.md §8): command, ports,
// volumes, networks, labels, healthcheck, and service dependencies. Core
// reasons about these to plan recreation and health verification; the git
// source adapter currently populates only Image and Env, while the Compose
// target driver populates the full set. The fields are value types or slices
// of value types so a Service copies without aliasing mutable state.
type Service struct {
	// Image is the container image reference declared in Git, e.g.
	// "ghcr.io/acme/api:2.4.1" or "ghcr.io/acme/api@sha256:91a...".
	Image string
	// Command is the normalized command for the service. Compose's shell
	// form (a single string) is stored as a one-element slice; the exec
	// form (a list) is stored verbatim.
	Command []string
	// Env is the environment variables declared for the service, keyed by
	// variable name. Secret values are referenced, never inlined.
	Env map[string]string
	// Ports is the set of ports the service exposes, normalized from
	// Compose's short and long forms.
	Ports []Port
	// Volumes is the set of volumes mounted into the service, normalized
	// from Compose's short and long forms.
	Volumes []Volume
	// Networks is the set of network names the service is attached to.
	Networks []string
	// Labels is the set of labels applied to the service, keyed by label
	// name.
	Labels map[string]string
	// Healthcheck is the service health check, if declared. A zero value
	// means no healthcheck was declared.
	Healthcheck Healthcheck
	// DependsOn is the set of service names this service depends on, in
	// declaration order.
	DependsOn []string
}

// Port is a normalized container port mapping. Host and Container are kept as
// strings so Compose port ranges (e.g. "8080-8085") and host bind addresses
// round-trip without loss.
type Port struct {
	// HostIP is the host IP the port is published on, when specified, e.g.
	// "127.0.0.1". Empty means all interfaces.
	HostIP string
	// Host is the published host port or range, e.g. "8080" or "8080-8085".
	// Empty means the host port is assigned by the target.
	Host string
	// Container is the container port or range the service listens on,
	// e.g. "8080" or "8080-8085".
	Container string
	// Protocol is the IP protocol, defaulting to "tcp".
	Protocol string
}

// Volume is a normalized volume mount. Type distinguishes bind mounts,
// named volumes, and anonymous volumes.
type Volume struct {
	// Type is "bind", "volume", or "tmpfs". It is inferred from the source
	// for short-form mounts when Compose does not state it explicitly.
	Type string
	// Source is the host path (for binds) or named volume name. Empty for
	// anonymous volumes.
	Source string
	// Target is the in-container mount path.
	Target string
	// ReadOnly is true when the mount is read-only.
	ReadOnly bool
}

// Healthcheck is a normalized Compose healthcheck. It captures the fields
// Accorda needs to wait for a service to become healthy
// (docs/ACCORDA.md §19).
type Healthcheck struct {
	// Test is the normalized healthcheck command. Compose's scalar form is
	// stored as ["CMD-SHELL", <string>]; the list form is stored verbatim.
	// Nil when the healthcheck is disabled.
	Test []string
	// Interval is the time between health checks.
	Interval time.Duration
	// Timeout is the time a single health check may take before it fails.
	Timeout time.Duration
	// Retries is the number of consecutive failures before the service is
	// considered unhealthy.
	Retries int
	// StartPeriod is the grace period during which health check failures
	// do not count toward retries.
	StartPeriod time.Duration
	// Disable is true when the healthcheck is explicitly disabled.
	Disable bool
}

// Clone returns a deep copy of the healthcheck so callers can mutate the copy
// without aliasing the original.
func (h Healthcheck) Clone() Healthcheck {
	return Healthcheck{
		Test:        append([]string(nil), h.Test...),
		Interval:    h.Interval,
		Timeout:     h.Timeout,
		Retries:     h.Retries,
		StartPeriod: h.StartPeriod,
		Disable:     h.Disable,
	}
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
		Image:       s.Image,
		Command:     append([]string(nil), s.Command...),
		Env:         cloneStringMap(s.Env),
		Ports:       clonePorts(s.Ports),
		Volumes:     cloneVolumes(s.Volumes),
		Networks:    append([]string(nil), s.Networks...),
		Labels:      cloneStringMap(s.Labels),
		Healthcheck: s.Healthcheck.Clone(),
		DependsOn:   append([]string(nil), s.DependsOn...),
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

// clonePorts returns a deep copy of p. A nil slice stays nil.
func clonePorts(p []Port) []Port {
	if p == nil {
		return nil
	}
	return append([]Port(nil), p...)
}

// cloneVolumes returns a deep copy of v. A nil slice stays nil.
func cloneVolumes(v []Volume) []Volume {
	if v == nil {
		return nil
	}
	return append([]Volume(nil), v...)
}

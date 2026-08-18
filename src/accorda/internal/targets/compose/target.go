package compose

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"

	"accorda/internal/config"
	"accorda/internal/core/health"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
	"accorda/internal/targets"
)

// Compile-time interface check: Target satisfies targets.Target here so a
// missing method is caught at build time, not at runtime.
var _ targets.Target = (*Target)(nil)

// Target is the Docker Compose target driver (docs/ACCORDA.md §8). It
// reconciles the desired state declared in a Compose file against the
// containers actually running on a Docker engine.
//
// The driver is constructed from an Accorda project's target configuration
// (config.Target) and talks to the Docker engine through a dockerClient
// seam. The Docker SDK dependency is confined to this adapter; core never
// imports it (docs/DECISIONS.md #3).
//
// This implementation provides the Validate and Current phases of the
// reconciliation lifecycle (docs/ACCORDA.md §6):
//
//   - Validate loads and validates the Compose file (reusing LoadFile) and
//     pings the Docker engine to confirm connectivity. It makes no changes.
//   - Current reads the runtime state of the project's containers and maps
//     them back to Accorda service names via the Compose labels, returning a
//     state.RuntimeState. It makes no changes.
//
// Plan, Apply, and Health return targets.ErrNotImplemented until later
// milestones.
type Target struct {
	// file is the Compose file path resolved from config.Target (File, or
	// Path when File is empty).
	file string
	// project is the Compose project name used to filter containers by the
	// com.docker.compose.project label. It is normalized to match the label
	// Compose v2 applies.
	project string
	// docker is the Docker engine client seam. It is injected so tests can
	// substitute a fake client without a running daemon.
	docker dockerClient
}

// Option configures a Compose Target.
type Option func(*Target)

// WithDockerClient injects a Docker engine client for the target to use. It
// is primarily intended for tests; production callers leave it unset and New
// builds a real client from the environment.
func WithDockerClient(c dockerClient) Option {
	return func(t *Target) { t.docker = c }
}

// New constructs a Compose Target from an Accorda project's target
// configuration. It does not touch the Docker engine or the filesystem;
// Validate performs those checks.
//
// The Compose file path is taken from cfg.File (§8 example) or cfg.Path
// (§25 example); at least one must be set. The project name is derived from
// the file path's directory basename, normalized to match the
// com.docker.compose.project label Compose v2 applies. An explicit project
// name can be supplied via the WithProjectName option (useful when the
// Compose file declares a top-level `name:` or COMPOSE_PROJECT_NAME is set).
func New(cfg config.Target, opts ...Option) (*Target, error) {
	if cfg.Type != "" && cfg.Type != config.TargetCompose {
		return nil, fmt.Errorf("compose target: target.type %q is not %q", cfg.Type, config.TargetCompose)
	}
	file := cfg.File
	if file == "" {
		file = cfg.Path
	}
	if file == "" {
		return nil, errors.New("compose target: target.file or target.path is required")
	}
	t := &Target{file: file, project: composeProjectName(file)}
	for _, opt := range opts {
		opt(t)
	}
	if t.docker == nil {
		cli, err := newDockerClient()
		if err != nil {
			return nil, fmt.Errorf("compose target: docker client: %w", err)
		}
		t.docker = cli
	}
	return t, nil
}

// WithProjectName sets the Compose project name used to filter containers,
// overriding the directory-basename default. This is useful when the Compose
// file declares a top-level `name:` or COMPOSE_PROJECT_NAME is set.
func WithProjectName(name string) Option {
	return func(t *Target) { t.project = normalizeProjectName(name) }
}

// Validate checks that the Compose file is loadable and that the Docker
// engine is reachable. It does not mutate the target
// (docs/ACCORDA.md §6 validate phase).
func (t *Target) Validate(ctx context.Context) error {
	if t == nil {
		return errors.New("compose target: nil target")
	}
	if _, err := LoadFile(t.file); err != nil {
		return err
	}
	if t.docker == nil {
		return errors.New("compose target: docker client is nil")
	}
	if _, err := t.docker.Ping(ctx); err != nil {
		return fmt.Errorf("compose target: docker ping: %w", err)
	}
	return nil
}

// Current returns the runtime state of the Compose project's containers
// (docs/ACCORDA.md §5.3). It lists all containers carrying the project's
// com.docker.compose.project label (including stopped ones, so a manually
// stopped service surfaces as drift rather than being hidden) and maps each
// container's state and image back to Accorda service names via the
// com.docker.compose.service label.
//
// Current makes no changes to the target. A service present in the desired
// state but absent from the returned RuntimeState is drifted (stopped or
// removed); a service present with a Status other than state.RunningStatus
// is drifted (docs/ACCORDA.md §5.3, docs/DECISIONS.md #3).
//
// Current reads state from the single ContainerList response (Summary.State
// and Summary.Image), so it issues exactly one Docker API call regardless of
// fleet size. Health is not part of runtime state here: it is a distinct
// concern (docs/ACCORDA.md §19) reported by the Health method, which is
// tracked separately (issue #15).
//
// A service scaled to multiple replicas shares one service name; when the
// replicas' runtime states disagree (for example one running and one
// stopped), Current reports a degraded status rather than silently letting
// the last container win, so per-replica drift is not hidden.
func (t *Target) Current(ctx context.Context) (*state.RuntimeState, error) {
	if t == nil {
		return nil, errors.New("compose target: nil target")
	}
	if t.docker == nil {
		return nil, errors.New("compose target: docker client is nil")
	}
	containers, err := t.docker.ContainerList(ctx, container.ListOptions{
		All:     true, // include stopped containers so drift is observable
		Filters: projectFilters(t.project),
	})
	if err != nil {
		return nil, fmt.Errorf("compose target: list containers: %w", err)
	}
	services := make(map[string]state.RuntimeService, len(containers))
	for _, c := range containers {
		name := serviceName(c.Labels)
		if name == "" {
			continue
		}
		rs := toRuntimeService(c)
		if prev, ok := services[name]; ok {
			rs = mergeRuntime(prev, rs)
		}
		services[name] = rs
	}
	return &state.RuntimeState{Services: services}, nil
}

// Plan computes the deployment plan that reconciles desired state with the
// target's current state. Not yet implemented (docs/ACCORDA.md §6 plan phase;
// tracked by issue #10).
func (t *Target) Plan(_ context.Context, _ *state.DesiredState) (*plan.Plan, error) {
	return nil, targets.ErrNotImplemented
}

// Apply applies the given plan to the target. Not yet implemented
// (docs/ACCORDA.md §6 deploy phase).
func (t *Target) Apply(_ context.Context, _ *plan.Plan) error {
	return targets.ErrNotImplemented
}

// Health verifies the health of the currently deployed workloads. Not yet
// implemented (docs/ACCORDA.md §19; tracked by issue #15).
func (t *Target) Health(_ context.Context) (*health.Health, error) {
	return nil, targets.ErrNotImplemented
}

// serviceName returns the Compose service name from a container's labels, or
// "" when the label is absent (for example a container not managed by
// Compose).
func serviceName(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	return labels[composeServiceLabel]
}

// toRuntimeService maps a Docker container summary to Accorda's
// RuntimeService. The Status field uses Docker's ContainerState string
// ("running", "exited", ...); the Image field is the image reference the
// container is running. Health is not part of runtime state (docs/ACCORDA.md
// §19) and is left empty here; it is reported by the Health method.
func toRuntimeService(c container.Summary) state.RuntimeService {
	return state.RuntimeService{
		Status: string(c.State),
		Image:  c.Image,
	}
}

// degradedStatus is the runtime status reported for a service whose replicas
// disagree (for example one running and one stopped). It signals drift that
// a single last-wins entry would otherwise hide (docs/ACCORDA.md §5.3).
const degradedStatus = "degraded"

// mergeRuntime combines two RuntimeService values for the same service name
// (multiple replicas). When the replicas agree, the merged value is that
// shared state; when they disagree on status, the merged value reports
// degradedStatus so per-replica drift is surfaced rather than silently
// overwritten.
func mergeRuntime(a, b state.RuntimeService) state.RuntimeService {
	if a.Status != b.Status {
		return state.RuntimeService{Status: degradedStatus, Image: a.Image}
	}
	return a
}

// composeProjectName derives the Compose project name from the Compose file
// path, matching the directory-basename heuristic Compose v2 uses when no
// explicit name is set: the base name of the file's directory, normalized.
// The path is resolved to absolute first so a bare filename (e.g.
// "compose.yaml", the §8 example) falls back to the working-directory
// basename rather than producing an empty name.
func composeProjectName(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		abs = file
	}
	return normalizeProjectName(filepath.Base(filepath.Dir(abs)))
}

// normalizeProjectName lowercases s and keeps only [a-z0-9_-], matching the
// compose-go NormalizeProjectName behavior so the derived name matches the
// com.docker.compose.project label Compose v2 applies.
func normalizeProjectName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(b.String(), "_-")
	return out
}

package compose

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"accorda/internal/config"
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
//   - Plan computes the desired-vs-deployed diff (docs/ACCORDA.md §9) by
//     reading the runtime state and delegating to plan.DriftActions,
//     producing a per-service CHANGED/UNCHANGED plan without applying it.
//   - Apply executes a plan by running the equivalent of `docker compose up
//     -d` scoped to the changed services (docs/ACCORDA.md §9), delegating to
//     a composeRunner seam so the `docker compose` CLI stays confined to
//     this adapter.
//   - Health verifies the health of the deployed workloads by reading the
//     runtime state and mapping each service's Docker healthcheck status to
//     a health.Status, waiting up to the health timeout (docs/ACCORDA.md §19).
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
	// runner executes `docker compose` subcommands for Apply. It is injected
	// so tests can substitute a fake without a `docker compose` binary.
	runner composeRunner
	// pullPolicy selects which images to pull before deployment
	// (docs/ACCORDA.md §9). It defaults to config.PullChanged.
	pullPolicy string
	// healthTimeout is the maximum time Health waits for services to become
	// healthy (docs/ACCORDA.md §19). It defaults to defaultHealthTimeout.
	healthTimeout time.Duration
}

// Option configures a Compose Target.
type Option func(*Target)

// WithDockerClient injects a Docker engine client for the target to use. It
// is primarily intended for tests; production callers leave it unset and New
// builds a real client from the environment.
func WithDockerClient(c dockerClient) Option {
	return func(t *Target) { t.docker = c }
}

// WithRunner injects a composeRunner for the target to use. It is primarily
// intended for tests; production callers leave it unset and New builds a
// cliRunner that shells out to `docker compose`.
func WithRunner(r composeRunner) Option {
	return func(t *Target) { t.runner = r }
}

// WithPullPolicy sets the image pull policy the target uses to decide which
// images to pull before deployment (docs/ACCORDA.md §9). It accepts one of
// config.PullChanged, config.PullMissing, config.PullAlways, or
// config.PullNever. It is primarily intended for tests and for callers that
// construct a Target directly from a config.Target without a full
// config.Project; the reconcile loop supplies the policy from the project's
// images.pull setting.
func WithPullPolicy(policy string) Option {
	return func(t *Target) { t.pullPolicy = policy }
}

// WithHealthTimeout sets the maximum time Health waits for services to become
// healthy (docs/ACCORDA.md §19). It defaults to defaultHealthTimeout; the
// reconcile loop supplies it from the project's health.timeout setting.
func WithHealthTimeout(d time.Duration) Option {
	return func(t *Target) { t.healthTimeout = d }
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
	t := &Target{file: file, project: composeProjectName(file), pullPolicy: config.PullChanged, healthTimeout: defaultHealthTimeout}
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
	if t.runner == nil {
		t.runner = cliRunner{file: t.file, project: t.project}
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
// stopped service surfaces as drift rather than being hidden), inspects
// each for its state and health, and maps the results back to Accorda
// service names via the com.docker.compose.service label.
//
// Current makes no changes to the target. A service present in the desired
// state but absent from the returned RuntimeState is drifted (stopped or
// removed); a service present with a Status other than state.RunningStatus
// is drifted (docs/ACCORDA.md §5.3, docs/DECISIONS.md #3).
//
// Health is part of runtime state (docs/ACCORDA.md §5.3, issue #9), so
// Current inspects each container to read its healthcheck status. This is an
// N+1 pattern against the Docker API; it is accepted for the MVP because
// ContainerList's Summary does not carry health, and moving health out of
// runtime state would require a spec change that docs/ACCORDA.md does not
// currently authorize.
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
		inspected, err := t.docker.ContainerInspect(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("compose target: inspect %q: %w", name, err)
		}
		rs := toRuntimeService(inspected)
		if prev, ok := services[name]; ok {
			rs = mergeRuntime(prev, rs)
		}
		services[name] = rs
	}
	return &state.RuntimeState{Services: services}, nil
}

// Plan computes the deployment plan that reconciles desired state with the
// target's current state (docs/ACCORDA.md §6 plan phase, §9, §12). It reads
// the runtime state via Current and delegates the desired-vs-deployed diff to
// the target-agnostic plan.DriftActions helper, producing a per-service
// CHANGED/UNCHANGED plan without applying anything. It then prepends pull
// actions according to the target's image pull policy (docs/ACCORDA.md §9):
// changed pulls only changed services' images, missing pulls only images not
// already local, always pulls every image, and never pulls nothing.
//
// Plan is safe and idempotent: it makes no changes to the target and returns
// the same action set for the same desired and runtime states. The plan's
// identifying fields are populated from the desired state: Commit is the
// desired commit, and Environment is a stand-in holding the repository
// because DesiredState has no environment field yet (see the TODO below).
// DeploymentID is empty because the Compose target does not yet assign
// deployment identifiers (that is the reconcile loop's responsibility,
// docs/ACCORDA.md §7). CreatedAt is wall-clock time, so two runs differ only
// in that field; the action ordering is deterministic (docs/DECISIONS.md #12).
//
// deployed supplies the previously deployed service configuration so
// DriftActions can compare the canonical service hash (docs/ACCORDA.md §10)
// and recreate a service whose command, ports, volumes, networks, labels,
// healthcheck, or depends_on changed even when its image did not.
func (t *Target) Plan(ctx context.Context, desired *state.DesiredState, deployed *state.DeployedState) (*plan.Plan, error) {
	if t == nil {
		return nil, errors.New("compose target: nil target")
	}
	if desired == nil {
		return nil, errors.New("compose target: desired state is nil")
	}
	runtime, err := t.Current(ctx)
	if err != nil {
		return nil, err
	}
	// TODO: Environment is a stand-in for the repository until DesiredState
	// carries an environment concept; the field is documented as "the target
	// environment the plan applies to" (docs/ACCORDA.md §31).
	p := plan.New("", desired.Repository, desired.Commit, time.Now())
	drift := plan.DriftActions(desired, deployed, runtime)
	// Inject pull actions according to the image pull policy
	// (docs/ACCORDA.md §9). Pulls are added before the drift actions so
	// images are fetched before the services that depend on them are created
	// or recreated.
	pulls, err := t.selectPulls(ctx, desired, drift)
	if err != nil {
		return nil, err
	}
	for _, a := range pulls {
		p.AddAction(a)
	}
	for _, a := range drift {
		p.AddAction(a)
	}
	return p, nil
}

// Apply applies the given plan to the target (docs/ACCORDA.md §6 deploy
// phase, §9). It runs the equivalent of `docker compose up -d` scoped to
// only the services the plan marks as changed, so unchanged services are
// left untouched (docs/ACCORDA.md §9: "docker compose up -d api").
//
// The action kinds map to Compose operations as follows:
//
//   - ActionCreate, ActionRecreate, ActionStart: the service is brought up
//     with `docker compose up -d <service>`. Compose creates missing
//     containers, recreates changed ones, and starts stopped ones, so a
//     single `up -d` covers all three.
//   - ActionRemove: orphaned services (present at runtime but absent from
//     the Compose file) are removed with `docker compose up -d
//     --remove-orphans`. `rm` cannot be used here because the orphan's
//     service name is no longer defined in the Compose file, so `rm` would
//     fail with "no such service".
//   - ActionPull: the service's image is pulled with
//     `docker compose pull <service>`.
//   - ActionStop: the service is stopped with `docker compose stop
//     <service>`.
//   - ActionNoop: skipped; the service is already converged.
//
// Apply is idempotent where possible: `up -d` and `up -d --remove-orphans`
// are safe to retry, and a plan with no changed services performs no work.
// It handles partial failures by returning an error that names the first
// failing service and its underlying cause, so the reconcile loop can
// surface which service failed rather than a bare exit code
// (docs/ACCORDA.md §6).
//
// A plan may carry one ActionRemove per orphan service, but `up -d
// --remove-orphans` removes every orphan in a single invocation. Apply
// therefore issues that command at most once, skipping any subsequent
// ActionRemove actions so N orphans do not trigger N redundant full `up -d`
// runs.
func (t *Target) Apply(ctx context.Context, p *plan.Plan) error {
	if t == nil {
		return errors.New("compose target: nil target")
	}
	if p == nil {
		return errors.New("compose target: plan is nil")
	}
	if t.runner == nil {
		return errors.New("compose target: compose runner is nil")
	}
	removedOrphans := false
	for _, a := range p.Actions {
		if a.Kind == plan.ActionRemove {
			if removedOrphans {
				continue
			}
			removedOrphans = true
		}
		if err := t.applyAction(ctx, a); err != nil {
			return err
		}
	}
	return nil
}

// applyAction executes a single plan action against the Compose project. It
// returns an error naming the service and action so a partial failure is
// attributable to the specific service that failed.
func (t *Target) applyAction(ctx context.Context, a plan.Action) error {
	switch a.Kind {
	case plan.ActionCreate, plan.ActionRecreate, plan.ActionStart:
		if err := t.runner.Run(ctx, "up", "-d", a.Service); err != nil {
			return fmt.Errorf("compose target: %s %q: %w", a.Kind, a.Service, err)
		}
	case plan.ActionRemove:
		// Orphans are removed with `up -d --remove-orphans`, which removes
		// every container not defined in the Compose file. The service name
		// is not passed because the orphan is no longer a defined service,
		// so `rm <service>` would fail with "no such service".
		if err := t.runner.Run(ctx, "up", "-d", "--remove-orphans"); err != nil {
			return fmt.Errorf("compose target: %s %q: %w", a.Kind, a.Service, err)
		}
	case plan.ActionPull:
		if err := t.runner.Run(ctx, "pull", a.Service); err != nil {
			return fmt.Errorf("compose target: %s %q: %w", a.Kind, a.Service, err)
		}
	case plan.ActionStop:
		if err := t.runner.Run(ctx, "stop", a.Service); err != nil {
			return fmt.Errorf("compose target: %s %q: %w", a.Kind, a.Service, err)
		}
	case plan.ActionNoop:
		// Already converged; nothing to do.
	}
	return nil
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

// toRuntimeService maps a Docker container inspect response to Accorda's
// RuntimeService. The Status field uses Docker's ContainerState string
// ("running", "exited", ...); the Health field uses the healthcheck status
// ("healthy", "unhealthy", "starting") or "" when there is no healthcheck.
func toRuntimeService(c container.InspectResponse) state.RuntimeService {
	svc := state.RuntimeService{}
	if c.ContainerJSONBase != nil {
		svc.Image = c.ContainerJSONBase.Image
		if c.ContainerJSONBase.State != nil {
			svc.Status = string(c.ContainerJSONBase.State.Status)
			if c.ContainerJSONBase.State.Health != nil {
				h := string(c.ContainerJSONBase.State.Health.Status)
				// "none" means no healthcheck; surface it as empty so callers
				// can treat "" as "no health information".
				if h != string(container.NoHealthcheck) {
					svc.Health = h
				}
			}
		}
	}
	return svc
}

// degradedStatus is the runtime status reported for a service whose replicas
// disagree (for example one running and one stopped). It signals drift that
// a single last-wins entry would otherwise hide (docs/ACCORDA.md §5.3).
const degradedStatus = "degraded"

// defaultHealthTimeout is the maximum time Health waits for services to
// become healthy when no explicit timeout is configured. It mirrors the
// config default for health.timeout (docs/ACCORDA.md §19).
const defaultHealthTimeout = 120 * time.Second

// mergeRuntime combines two RuntimeService values for the same service name
// (multiple replicas). When the replicas agree, the merged value is that
// shared state; when they disagree on status or health, the merged value
// reports degradedStatus so per-replica drift is surfaced rather than
// silently overwritten.
func mergeRuntime(a, b state.RuntimeService) state.RuntimeService {
	if a.Status != b.Status || a.Health != b.Health {
		return state.RuntimeService{Status: degradedStatus, Health: "", Image: a.Image}
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

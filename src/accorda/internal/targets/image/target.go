package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/health"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
	shareddocker "accorda/internal/docker"
	"accorda/internal/sources"
	"accorda/internal/targets"
)

// Compile-time interface checks: the image Target satisfies the
// targets.Target interface and its optional capabilities so a missing method
// is caught at build time, not at runtime.
var (
	_ targets.Target        = (*Target)(nil)
	_ targets.LogTarget     = (*Target)(nil)
	_ targets.RuntimeHealth = (*Target)(nil)
)

// containerNameLabel is the Docker label the image driver sets on the
// container it manages, carrying the Accorda service name. Filtering on it
// lets the driver enumerate exactly the container belonging to this image
// target without relying on Compose labels.
const containerNameLabel = "accorda.image.service"

// projectLabel is the Docker label carrying the Accorda project (group)
// name, so a multi-target project's image containers can be grouped and
// attributed to their project (issue #103, docs/DECISIONS.md #53).
const projectLabel = "accorda.image.project"

// Target is the raw single-image target driver (docs/DECISIONS.md #24). It
// reconciles a single container image declared in accorda.yaml against the
// container actually running on a Docker engine.
//
// The driver is constructed from an Accorda project's target configuration
// and talks to the Docker engine through the shared docker.Client seam. The
// Docker SDK dependency is confined to the adapters; core never imports it
// (docs/DECISIONS.md #3). Apply runs the container through a runner seam so
// the `docker` CLI stays confined to this adapter.
type Target struct {
	// name is the Accorda service name the single container is managed
	// under. It is the target's own name when set, otherwise the ensemble
	// member name, so two image targets in one project do not collide
	// (issue #103, docs/DECISIONS.md #53).
	name string
	// project is the Accorda project (group) name, empty for a standalone
	// project. It is set as the accorda.image.project Docker label so a
	// multi-target project's image containers are grouped and attributable.
	project string
	// image is the desired container image reference.
	image string
	// env is the desired environment, keyed by variable name.
	env map[string]string
	// ports are the desired published port mappings in Docker short form.
	ports []string
	// docker is the Docker engine client seam. It is injected so tests can
	// substitute a fake client without a running daemon.
	docker shareddocker.Client
	// runner runs `docker` subcommands for Apply. It is injected so tests
	// can substitute a fake without a `docker` binary.
	runner Runner
	// pullPolicy selects which images to pull before deployment
	// (docs/ACCORDA.md §9). It defaults to config.PullChanged.
	pullPolicy string
	// healthTimeout is the maximum time Health waits for the container to
	// become healthy (docs/ACCORDA.md §19). It defaults to
	// defaultHealthTimeout.
	healthTimeout time.Duration
	// environment is the target environment the plan applies to
	// (docs/ACCORDA.md §25, §31), threaded from the project's top-level
	// environment field.
	environment string
}

// Option configures an image Target.
type Option func(*Target)

// WithDockerClient injects a Docker engine client for the target to use. It
// is primarily intended for tests; production callers leave it unset and New
// builds a real client from the environment.
func WithDockerClient(c shareddocker.Client) Option {
	return func(t *Target) { t.docker = c }
}

// WithRunner injects a Runner for the target to use. It is primarily
// intended for tests; production callers leave it unset and New builds a
// cliRunner that shells out to `docker`.
func WithRunner(r Runner) Option {
	return func(t *Target) { t.runner = r }
}

// WithPullPolicy sets the image pull policy the target uses to decide which
// images to pull before deployment (docs/ACCORDA.md §9).
func WithPullPolicy(policy string) Option {
	return func(t *Target) { t.pullPolicy = policy }
}

// WithHealthTimeout sets the maximum time Health waits for the container to
// become healthy (docs/ACCORDA.md §19).
func WithHealthTimeout(d time.Duration) Option {
	return func(t *Target) { t.healthTimeout = d }
}

// WithEnvironment sets the target environment recorded on every plan the
// target generates (docs/ACCORDA.md §25, §31).
func WithEnvironment(env string) Option {
	return func(t *Target) { t.environment = env }
}

// WithProject sets the Accorda project (group) name, emitted as the
// accorda.image.project Docker label so a multi-target project's image
// containers are grouped and attributable (issue #103, docs/DECISIONS.md
// #53). It is optional: a standalone project may omit it.
func WithProject(project string) Option {
	return func(t *Target) { t.project = project }
}

// New constructs an image Target from an Accorda project's target
// configuration. It does not touch the Docker engine; Validate performs
// that check. name is the service name the single container is managed
// under (the ensemble project name, or a derived default for a standalone
// project).
func New(cfg config.Target, name string, opts ...Option) (*Target, error) {
	if cfg.Type != "" && cfg.Type != config.TargetImage {
		return nil, fmt.Errorf("image target: target.type %q is not %q", cfg.Type, config.TargetImage)
	}
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, errors.New("image target: target.image is required")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("image target: service name is required")
	}
	t := &Target{
		name:          name,
		image:         cfg.Image,
		env:           cfg.Env,
		ports:         cfg.Ports,
		pullPolicy:    config.PullChanged,
		healthTimeout: shareddocker.DefaultHealthTimeout,
		environment:   "",
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.docker == nil {
		cli, err := shareddocker.NewClient()
		if err != nil {
			return nil, fmt.Errorf("image target: docker client: %w", err)
		}
		t.docker = cli
	}
	if t.runner == nil {
		t.runner = cliRunner{name: t.name, image: t.image}
	}
	return t, nil
}

// Validate checks that the Docker engine is reachable. It does not mutate the
// target (docs/ACCORDA.md §6 validate phase).
func (t *Target) Validate(ctx context.Context) error {
	if t == nil {
		return errors.New("image target: nil target")
	}
	if t.docker == nil {
		return errors.New("image target: docker client is nil")
	}
	if _, err := t.docker.Ping(ctx); err != nil {
		return fmt.Errorf("image target: docker ping: %w", err)
	}
	if t.runner == nil {
		return errors.New("image target: runner is nil")
	}
	return nil
}

// Desired returns the target's config-derived desired state, anchored to the
// commit metadata carried by sourceDesired (docs/DECISIONS.md #24). It
// builds a single-service DesiredState from the image, env, and ports config
// fields; no Compose file is parsed. The source's Repository, Branch, Commit,
// and CommitTime are preserved so receipts and history stay Git-anchored.
func (t *Target) Desired(_ context.Context, revision *sources.Revision) (*state.DesiredState, error) {
	if t == nil {
		return nil, errors.New("image target: nil target")
	}
	desired := &state.DesiredState{Services: map[string]state.Service{
		t.name: {
			Image: t.image,
			Env:   t.env,
			Ports: parsePorts(t.ports),
		},
	}}
	if revision != nil {
		desired.Repository = revision.Repository
		desired.Branch = revision.Commit.Branch
		desired.Commit = revision.Commit.SHA
		desired.CommitTime = revision.Commit.Time
	}
	return desired, nil
}

// Current returns the runtime state of the single container the image target
// manages (docs/ACCORDA.md §5.3). It lists containers carrying the
// accorda.image.service label matching the target's service name, inspects
// each for its state and health, and maps the result to a one-service
// RuntimeState. A missing container surfaces as an empty RuntimeState so the
// plan reports it as drifted (a create action).
func (t *Target) Current(ctx context.Context) (*state.RuntimeState, error) {
	if t == nil {
		return nil, errors.New("image target: nil target")
	}
	if t.docker == nil {
		return nil, errors.New("image target: docker client is nil")
	}
	containers, err := t.docker.ContainerList(ctx, containerListOptions(t.name))
	if err != nil {
		return nil, fmt.Errorf("image target: list containers: %w", err)
	}
	services := make(map[string]state.RuntimeService, len(containers))
	for _, c := range containers {
		inspected, err := t.docker.ContainerInspect(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("image target: inspect %q: %w", t.name, err)
		}
		rs := shareddocker.RuntimeService(inspected)
		if prev, ok := services[t.name]; ok {
			rs = shareddocker.MergeRuntime(prev, rs)
		}
		services[t.name] = rs
	}
	shareddocker.ResolveDigests(ctx, t.docker, services)
	return &state.RuntimeState{Services: services}, nil
}

// Plan computes the deployment plan that reconciles the config-derived
// desired state with the target's current state (docs/ACCORDA.md §6 plan
// phase, §9, §12). It delegates the desired-vs-deployed diff to the
// target-agnostic plan.DriftActions helper and prepends pull actions
// according to the image pull policy.
func (t *Target) Plan(ctx context.Context, desired *state.DesiredState, deployed *state.DeployedState) (*plan.Plan, error) {
	if t == nil {
		return nil, errors.New("image target: nil target")
	}
	if desired == nil {
		return nil, errors.New("image target: desired state is nil")
	}
	runtime, err := t.Current(ctx)
	if err != nil {
		return nil, err
	}
	p := plan.New("", t.environment, desired.Commit, time.Now())
	drift := plan.DriftActions(desired, deployed, runtime)
	pulls, err := shareddocker.SelectPulls(ctx, t.docker, t.pullPolicy, desired, drift)
	if err != nil {
		return nil, fmt.Errorf("image target: %w", err)
	}
	for _, pull := range pulls {
		p.AddAction(plan.Action{Kind: plan.ActionPull, Service: pull.Service, Image: pull.Image})
	}
	for _, a := range drift {
		p.AddAction(a)
	}
	return p, nil
}

// Apply applies the given plan to the target (docs/ACCORDA.md §6 deploy
// phase, §9). The image target runs a single container, so Apply removes any
// existing container for the service name and runs a fresh one with the
// desired image, env, and ports. Pull actions are issued as `docker pull`
// before the container is (re)created.
func (t *Target) Apply(ctx context.Context, p *plan.Plan) error {
	if err := t.validateApply(p); err != nil {
		return err
	}
	completed := make([]plan.Action, 0, len(p.Actions))
	for _, a := range p.Actions {
		if a.Kind == plan.ActionNoop {
			continue
		}
		if err := t.applyAction(ctx, a); err != nil {
			return &targets.ApplyError{Completed: completed, Failed: a, Err: err}
		}
		completed = append(completed, a)
	}
	return nil
}

func (t *Target) validateApply(p *plan.Plan) error {
	if t == nil {
		return errors.New("image target: nil target")
	}
	if p == nil {
		return errors.New("image target: plan is nil")
	}
	if t.runner == nil {
		return errors.New("image target: runner is nil")
	}
	return nil
}

// applyAction executes a single plan action against the container. The image
// target maps the target-agnostic action kinds to Docker CLI operations:
//
//   - Pull: `docker pull <image>`.
//   - Create/Recreate/Start: remove any existing container for the service
//     name, then `docker run -d` with the desired image, env, and ports.
//   - Remove: `docker rm -f` the container for the service name.
//   - Stop: `docker stop` the container for the service name.
func (t *Target) applyAction(ctx context.Context, a plan.Action) error {
	switch a.Kind {
	case plan.ActionPull:
		if err := t.runner.Run(ctx, "pull", t.image); err != nil {
			return fmt.Errorf("image target: pull %q: %w", t.image, err)
		}
	case plan.ActionCreate, plan.ActionRecreate, plan.ActionStart:
		if err := t.runner.Run(ctx, "rm", "-f", t.name); err != nil {
			return fmt.Errorf("image target: remove old %q: %w", t.name, err)
		}
		args := t.runArgs()
		if err := t.runner.Run(ctx, args...); err != nil {
			return fmt.Errorf("image target: %s %q: %w", a.Kind, t.name, err)
		}
	case plan.ActionRemove:
		if err := t.runner.Run(ctx, "rm", "-f", t.name); err != nil {
			return fmt.Errorf("image target: remove %q: %w", t.name, err)
		}
	case plan.ActionStop:
		if err := t.runner.Run(ctx, "stop", t.name); err != nil {
			return fmt.Errorf("image target: stop %q: %w", t.name, err)
		}
	case plan.ActionNoop:
	}
	return nil
}

// runArgs builds the `docker run -d` argument list for the desired image,
// env, ports, and service name. Env and ports are emitted in sorted order so
// the command is deterministic (docs/DECISIONS.md #7).
func (t *Target) runArgs() []string {
	args := []string{"run", "-d", "--name", t.name, "--label", containerNameLabel + "=" + t.name}
	if t.project != "" {
		args = append(args, "--label", projectLabel+"="+t.project)
	}
	for _, k := range sortedEnvKeys(t.env) {
		args = append(args, "-e", k+"="+t.env[k])
	}
	for _, p := range t.ports {
		args = append(args, "-p", p)
	}
	args = append(args, t.image)
	return args
}

// Health verifies the health of the managed container
// (docs/ACCORDA.md §19). It reads the runtime state, maps the container's
// Docker healthcheck status to a health.Status, and waits up to the health
// timeout for the container to leave the starting state.
func (t *Target) Health(ctx context.Context) (*health.Health, error) {
	if t == nil || t.docker == nil {
		return nil, errors.New("image target: docker client is nil")
	}
	return shareddocker.WaitForHealthy(ctx, t.Current, t.healthTimeout)
}

// HealthFromRuntime implements targets.RuntimeHealth so read-only commands
// like `accorda status` can derive health from a runtime state without
// importing the shared Docker operations layer (docs/ACCORDA.md §19).
func (t *Target) HealthFromRuntime(runtime *state.RuntimeState, checkedAt time.Time) *health.Health {
	return shareddocker.HealthFromRuntime(runtime, checkedAt)
}

// Logs fetches or follows logs for the managed container
// (docs/ACCORDA.md §11). service must match the target's service name; the
// image target manages a single container, so the service selector is
// validated against the configured name.
func (t *Target) Logs(ctx context.Context, service string, opts targets.LogOptions, stdout, stderr io.Writer) error {
	if t == nil {
		return errors.New("image target: nil target")
	}
	service = strings.TrimSpace(service)
	if service != t.name {
		return fmt.Errorf("image target: service %q is not managed by this target (want %q)", service, t.name)
	}
	logClient, ok := t.docker.(shareddocker.LogClient)
	if !ok {
		return errors.New("image target: docker client does not support container logs")
	}
	containers, err := t.docker.ContainerList(ctx, containerListOptions(t.name))
	if err != nil {
		return fmt.Errorf("image target: list containers: %w", err)
	}
	if len(containers) == 0 {
		return fmt.Errorf("image target: service %q has no container", service)
	}
	id := containers[0].ID
	if opts.Tail == "" {
		opts.Tail = shareddocker.AllLogLines
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	inspected, err := t.docker.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("image target: inspect container %q for logs: %w", id, err)
	}
	stream, err := logClient.ContainerLogs(ctx, id, dockerLogsOptions(opts.Follow, opts.Tail))
	if err != nil {
		return fmt.Errorf("image target: read container %q logs: %w", id, err)
	}
	tty := inspected.Config != nil && inspected.Config.Tty
	if err := stdcopyLogs(stdout, stderr, stream, tty); err != nil {
		return fmt.Errorf("image target: stream container %q logs: %w", id, err)
	}
	return nil
}

package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"accorda/internal/config"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
	"accorda/internal/targets"
)

// fakeDockerClient is a test double for the dockerClient seam. It returns
// canned responses for Ping, ContainerList, ContainerInspect, ImageList, and
// ImageInspect so the Compose target can be exercised without a running
// Docker daemon.
type fakeDockerClient struct {
	pingErr    error
	containers []container.Summary
	inspected  map[string]container.InspectResponse
	inspectErr map[string]error
	listErr    error
	images     []image.Summary
	imageErr   error
	// imageInspected maps an image reference to its InspectResponse, used by
	// ImageInspect to resolve manifest digests (docs/ACCORDA.md §7).
	imageInspected map[string]image.InspectResponse
	// lastOptions captures the full ListOptions passed to ContainerList so
	// tests can assert the All flag (drift visibility) and the label filter.
	lastOptions container.ListOptions
}

func (f *fakeDockerClient) Ping(_ context.Context) (types.Ping, error) {
	return types.Ping{}, f.pingErr
}

func (f *fakeDockerClient) ContainerList(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
	f.lastOptions = options
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeDockerClient) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	if err, ok := f.inspectErr[id]; ok {
		return container.InspectResponse{}, err
	}
	return f.inspected[id], nil
}

func (f *fakeDockerClient) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	if f.imageErr != nil {
		return nil, f.imageErr
	}
	return f.images, nil
}

func (f *fakeDockerClient) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	if f.imageInspected == nil {
		return image.InspectResponse{}, nil
	}
	return f.imageInspected[ref], nil
}

// fakeRunner is a test double for the composeRunner seam. It records every
// invocation so tests can assert the exact `docker compose` subcommands
// Apply issued, and returns a canned error to exercise the failure path.
type fakeRunner struct {
	calls [][]string
	err   error
	errs  []error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) error {
	f.calls = append(f.calls, args)
	if len(f.errs) >= len(f.calls) {
		return f.errs[len(f.calls)-1]
	}
	return f.err
}

// writeComposeFile writes a minimal valid Compose file in a temp dir and
// returns its path so Target.Validate can load it.
func writeComposeFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, config.DefaultComposeFile)
	if err := os.WriteFile(path, []byte("services:\n  api:\n    image: api:1\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return path
}

// newTarget builds a Target with a fake client and the Compose file at path,
// using the project name derived from the file's directory basename.
func newTarget(t *testing.T, path string, cli dockerClient) *Target {
	t.Helper()
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithRunner(&fakeRunner{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tgt
}

// summary builds a container.Summary with the given service label, tagged
// with the project label so Current() selects it. The ID is derived from the
// service name so tests can key inspect responses by it.
func summary(project, service string) container.Summary {
	return container.Summary{
		ID:     "id-" + service,
		Labels: map[string]string{composeProjectLabel: project, composeServiceLabel: service},
	}
}

// inspect builds a container.InspectResponse with the given image, state,
// and health status. It sets ContainerJSONBase.Image to the resolved image ID
// ("sha256:<image>") and Config.Image to the reference the operator passed,
// mirroring a real Docker inspect response, so the runtime-state reader
// exercises its reference-preferring lookup.
func inspect(image, state, health string) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Image: "sha256:" + image,
			State: &container.State{Status: state, Health: &container.Health{Status: health}},
		},
		Config: &container.Config{Image: image},
	}
}

func TestImageReference_PrefersConfigImage(t *testing.T) {
	// The runtime image must be the reference the operator passed (Config.Image,
	// e.g. "busybox:1.36"), not the resolved image ID (ContainerJSONBase.Image,
	// e.g. "sha256:91a..."), so desired-vs-runtime comparison agrees
	// (docs/ACCORDA.md §5.3, §8).
	got := imageReference(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{Image: "sha256:91a"},
		Config:            &container.Config{Image: "busybox:1.36"},
	})
	if got != "busybox:1.36" {
		t.Errorf("imageReference = %q, want %q", got, "busybox:1.36")
	}
}

func TestImageReferenceFallsBackToImageID(t *testing.T) {
	// When Config is absent (some engine responses), fall back to the image ID.
	got := imageReference(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{Image: "sha256:91a"},
	})
	if got != "sha256:91a" {
		t.Errorf("imageReference = %q, want %q", got, "sha256:91a")
	}
}

func TestImageReferenceEmpty(t *testing.T) {
	if got := imageReference(container.InspectResponse{}); got != "" {
		t.Errorf("imageReference = %q, want empty", got)
	}
}

func TestNew_RequiresFile(t *testing.T) {
	_, err := New(config.Target{Type: config.TargetCompose}, WithDockerClient(&fakeDockerClient{}))
	if err == nil {
		t.Fatal("expected error for empty file/path, got nil")
	}
	if !strings.Contains(err.Error(), "file or target.path is required") {
		t.Errorf("err = %v, want one mentioning file or path required", err)
	}
}

func TestCompileTime_TargetImplementsInterface(t *testing.T) {
	var _ targets.Target = (*Target)(nil)
}

func TestValidate_LoadsFileAndPings(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	tgt := newTarget(t, path, cli)

	if err := tgt.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_MissingFile_IsError(t *testing.T) {
	cli := &fakeDockerClient{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: "/nonexistent/x.yaml"},
		WithDockerClient(cli))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tgt.Validate(context.Background()); err == nil {
		t.Fatal("expected error for missing compose file, got nil")
	}
}

func TestValidate_PingFails_IsError(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{pingErr: errors.New("connection refused")}
	tgt := newTarget(t, path, cli)

	err := tgt.Validate(context.Background())
	if err == nil {
		t.Fatal("expected error when docker ping fails, got nil")
	}
	if !strings.Contains(err.Error(), "docker ping") {
		t.Errorf("err = %v, want one mentioning docker ping", err)
	}
}

func TestValidate_ComposeCLIFails_IsError(t *testing.T) {
	path := writeComposeFile(t)
	runner := &fakeRunner{err: errors.New("compose plugin missing")}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(&fakeDockerClient{}), WithRunner(runner))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = tgt.Validate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker compose CLI") {
		t.Fatalf("Validate() error = %v, want Docker Compose CLI failure", err)
	}
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0], []string{"version"}) {
		t.Fatalf("runner calls = %v, want [[version]]", runner.calls)
	}
}

func TestCurrent_MapsContainersToRuntimeState(t *testing.T) {
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
			summary(project, "worker"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api":    inspect("api:1", "running", "healthy"),
			"id-worker": inspect("worker:1", "running", "none"),
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(rs.Services) != 2 {
		t.Fatalf("got %d services, want 2: %+v", len(rs.Services), rs.Services)
	}

	api := rs.Services["api"]
	wantAPI := state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"}
	if api != wantAPI {
		t.Errorf("api = %+v, want %+v", api, wantAPI)
	}

	worker := rs.Services["worker"]
	wantWorker := state.RuntimeService{Status: "running", Health: "", Image: "worker:1"}
	if worker != wantWorker {
		t.Errorf("worker = %+v, want %+v", worker, wantWorker)
	}

	// The list filter must select the project's containers.
	if got := cli.lastOptions.Filters.Get("label"); len(got) == 0 {
		t.Error("ContainerList called with no label filter")
	} else if !strings.Contains(got[0], composeProjectLabel) || !strings.Contains(got[0], project) {
		t.Errorf("label filter = %v, want one referencing %q for %q", got, composeProjectLabel, project)
	}
}

func TestCurrent_IncludesStoppedContainers(t *testing.T) {
	// A stopped service must appear with its exited status so drift is
	// observable (docs/ACCORDA.md §5.3: a manually stopped container is
	// drift even when its image is unchanged).
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "exited", "none"),
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(rs.Services) != 1 {
		t.Fatalf("got %d services, want 1: %+v", len(rs.Services), rs.Services)
	}
	if got := rs.Services["api"].Status; got != "exited" {
		t.Errorf("api.Status = %q, want exited", got)
	}
}

func TestCurrent_ResolvesImageDigest(t *testing.T) {
	// Current must resolve the manifest digest of each running image so
	// deployment receipts can record the immutable digest rather than a
	// mutable tag (docs/ACCORDA.md §7).
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "healthy"),
		},
		imageInspected: map[string]image.InspectResponse{
			"api:1": {RepoDigests: []string{"ghcr.io/acme/api@sha256:91a"}},
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := rs.Services["api"].Digest; got != "ghcr.io/acme/api@sha256:91a" {
		t.Errorf("api.Digest = %q, want %q", got, "ghcr.io/acme/api@sha256:91a")
	}
}

func TestCurrent_UnresolvableDigest_IsEmpty(t *testing.T) {
	// An image that cannot be inspected (e.g. locally built, no registry
	// manifest) must yield an empty digest rather than failing Current.
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "healthy"),
		},
		// No imageInspected entry: ImageInspect returns an empty response.
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := rs.Services["api"].Digest; got != "" {
		t.Errorf("api.Digest = %q, want empty", got)
	}
}

func TestCurrent_SkipsContainerWithoutServiceLabel(t *testing.T) {
	// A container with the project label but no service label is not a
	// Compose service and must be skipped.
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "c1", Labels: map[string]string{composeProjectLabel: project}},
		},
		inspected: map[string]container.InspectResponse{},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(rs.Services) != 0 {
		t.Errorf("got %d services, want 0: %+v", len(rs.Services), rs.Services)
	}
}

func TestCurrent_InspectFails_IsError(t *testing.T) {
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspectErr: map[string]error{"id-api": errors.New("inspect boom")},
	}
	tgt := newTarget(t, path, cli)

	if _, err := tgt.Current(context.Background()); err == nil {
		t.Fatal("expected error when inspect fails, got nil")
	}
}

func TestCurrent_ListFails_IsError(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{listErr: errors.New("list boom")}
	tgt := newTarget(t, path, cli)

	if _, err := tgt.Current(context.Background()); err == nil {
		t.Fatal("expected error when list fails, got nil")
	}
}

func TestCurrent_EmptyProject_ReturnsEmptyState(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{containers: nil}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if rs == nil {
		t.Fatal("RuntimeState is nil")
	}
	if len(rs.Services) != 0 {
		t.Errorf("got %d services, want 0", len(rs.Services))
	}
}

func TestCurrent_ScaledReplicasDisagree_IsDegraded(t *testing.T) {
	// Two replicas of the same service with different states must surface a
	// degraded status rather than silently letting the last one win.
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "id-api-1", Labels: map[string]string{composeProjectLabel: project, composeServiceLabel: "api"}},
			{ID: "id-api-2", Labels: map[string]string{composeProjectLabel: project, composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"id-api-1": inspect("api:1", "running", "healthy"),
			"id-api-2": inspect("api:1", "exited", "none"),
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(rs.Services) != 1 {
		t.Fatalf("got %d services, want 1: %+v", len(rs.Services), rs.Services)
	}
	if got := rs.Services["api"].Status; got != degradedStatus {
		t.Errorf("api.Status = %q, want %q", got, degradedStatus)
	}
}

func TestCurrent_ScaledReplicasAgree_IsSingleEntry(t *testing.T) {
	// Two replicas of the same service in the same state collapse to one
	// entry with that shared state.
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "id-api-1", Labels: map[string]string{composeProjectLabel: project, composeServiceLabel: "api"}},
			{ID: "id-api-2", Labels: map[string]string{composeProjectLabel: project, composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"id-api-1": inspect("api:1", "running", "healthy"),
			"id-api-2": inspect("api:1", "running", "healthy"),
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(rs.Services) != 1 {
		t.Fatalf("got %d services, want 1: %+v", len(rs.Services), rs.Services)
	}
	if got := rs.Services["api"].Status; got != "running" {
		t.Errorf("api.Status = %q, want running", got)
	}
}

func TestPlan_ComputesDesiredVsDeployedDiff(t *testing.T) {
	// Desired declares api (changed image) and worker (new); runtime has api
	// running an old image and an orphan. Plan must produce per-service
	// CHANGED/UNCHANGED actions without applying anything.
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
			summary(project, "orphan"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api":    inspect("api:1", "running", "healthy"),
			"id-orphan": inspect("old:1", "running", "none"),
		},
	}
	tgt := newTarget(t, path, cli)

	desired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "abc123",
		Services: map[string]state.Service{
			"api":    {Image: "api:2"},
			"worker": {Image: "worker:1"},
		},
	}
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p == nil {
		t.Fatal("Plan returned nil")
	}
	if p.Commit != "abc123" {
		t.Errorf("Commit = %q, want %q", p.Commit, "abc123")
	}
	if p.Environment != "" {
		t.Errorf("Environment = %q, want empty when WithEnvironment is not set", p.Environment)
	}

	kinds := map[string]plan.ActionKind{}
	for _, a := range p.Actions {
		kinds[a.Service] = a.Kind
	}
	if kinds["api"] != plan.ActionRecreate {
		t.Errorf("api kind = %q, want %q", kinds["api"], plan.ActionRecreate)
	}
	if kinds["worker"] != plan.ActionCreate {
		t.Errorf("worker kind = %q, want %q", kinds["worker"], plan.ActionCreate)
	}
	if kinds["orphan"] != plan.ActionRemove {
		t.Errorf("orphan kind = %q, want %q", kinds["orphan"], plan.ActionRemove)
	}
	if !p.Changed() {
		t.Error("Changed() = false, want true for a plan with changes")
	}
}

func TestPlan_EnvironmentFromProject(t *testing.T) {
	// Plan.Environment must reflect the project's environment threaded in via
	// WithEnvironment (docs/ACCORDA.md §25, §31), not the Git-declared
	// desired-state repository. The environment is a top-level project field
	// in accorda.yaml, so the target carries it rather than deriving it from
	// the desired state.
	path := writeComposeFile(t)
	cli := &fakeDockerClient{
		containers: []container.Summary{summary(normalizeProjectName(filepath.Base(filepath.Dir(path))), "api")},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "healthy"),
		},
	}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithEnvironment("production"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	desired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "abc123",
		Services:   map[string]state.Service{"api": {Image: "api:1"}},
	}
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Environment != "production" {
		t.Errorf("Environment = %q, want %q", p.Environment, "production")
	}
	// The repository must never leak into Environment as a stand-in.
	if p.Environment == desired.Repository {
		t.Errorf("Environment = %q matches repository stand-in, want the project environment", p.Environment)
	}
}

func TestPlan_Converged_IsUnchanged(t *testing.T) {
	// Desired and runtime agree: the plan must contain only Noop actions and
	// report unchanged.
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "healthy"),
		},
	}
	tgt := newTarget(t, path, cli)

	desired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "abc123",
		Services:   map[string]state.Service{"api": {Image: "api:1"}},
	}
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(p.Actions), p.Actions)
	}
	if p.Actions[0].Kind != plan.ActionNoop {
		t.Errorf("kind = %q, want %q", p.Actions[0].Kind, plan.ActionNoop)
	}
	if p.Changed() {
		t.Error("Changed() = true, want false for a converged plan")
	}
}

func TestPlan_RecreatesOnConfigChange(t *testing.T) {
	// A service whose image is unchanged but whose configuration hash differs
	// (e.g. command changed) must be recreated through Target.Plan, exercising
	// the forwarding of the deployed state to plan.DriftActions
	// (docs/ACCORDA.md §10).
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "healthy"),
		},
	}
	tgt := newTarget(t, path, cli)

	desired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "abc123",
		Services: map[string]state.Service{
			"api": {Image: "api:1", Command: []string{"./api", "--port", "8080"}},
		},
	}
	deployed := &state.DeployedState{
		DeploymentID: "dep_1",
		Commit:       "abc123",
		Services: map[string]state.Service{
			"api": {Image: "api:1", Command: []string{"./api", "--port", "9090"}},
		},
	}
	p, err := tgt.Plan(context.Background(), desired, deployed)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	kinds := map[string]plan.ActionKind{}
	for _, a := range p.Actions {
		kinds[a.Service] = a.Kind
	}
	if kinds["api"] != plan.ActionRecreate {
		t.Errorf("api kind = %q, want %q", kinds["api"], plan.ActionRecreate)
	}
}

func TestPlan_NilDesired_IsError(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	tgt := newTarget(t, path, cli)

	if _, err := tgt.Plan(context.Background(), nil, nil); err == nil {
		t.Fatal("expected error for nil desired state, got nil")
	}
}

func TestApply_MapsActionsToComposeCommands(t *testing.T) {
	// Each non-noop action kind must map to the expected `docker compose`
	// subcommand scoped to the changed service; noop actions are skipped.
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	runner := &fakeRunner{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithRunner(runner))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := plan.New("", "acme/infra", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionCreate, Service: "api"})
	p.AddAction(plan.Action{Kind: plan.ActionRecreate, Service: "worker"})
	p.AddAction(plan.Action{Kind: plan.ActionStart, Service: "db"})
	p.AddAction(plan.Action{Kind: plan.ActionRemove, Service: "orphan"})
	p.AddAction(plan.Action{Kind: plan.ActionPull, Service: "api"})
	p.AddAction(plan.Action{Kind: plan.ActionStop, Service: "legacy"})
	p.AddAction(plan.Action{Kind: plan.ActionNoop, Service: "postgres"})

	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := [][]string{
		{"up", "-d", "api"},
		{"up", "-d", "worker"},
		{"up", "-d", "db"},
		{"up", "-d", "--remove-orphans"},
		{"pull", "api"},
		{"stop", "legacy"},
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("got %d runner calls, want %d: %v", len(runner.calls), len(want), runner.calls)
	}
	for i, w := range want {
		if !reflect.DeepEqual(runner.calls[i], w) {
			t.Errorf("call[%d] = %v, want %v", i, runner.calls[i], w)
		}
	}
}

func TestApply_NoopOnly_DoesNothing(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	runner := &fakeRunner{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithRunner(runner))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := plan.New("", "acme/infra", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionNoop, Service: "api"})

	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("got %d runner calls, want 0: %v", len(runner.calls), runner.calls)
	}
}

func TestApply_MultipleOrphans_SingleRemoveOrphansCall(t *testing.T) {
	// A plan with N orphans must issue `up -d --remove-orphans` exactly
	// once, not once per orphan, since a single invocation removes all of
	// them.
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	runner := &fakeRunner{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithRunner(runner))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := plan.New("", "acme/infra", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionRemove, Service: "orphan-a"})
	p.AddAction(plan.Action{Kind: plan.ActionRemove, Service: "orphan-b"})
	p.AddAction(plan.Action{Kind: plan.ActionRemove, Service: "orphan-c"})

	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := [][]string{{"up", "-d", "--remove-orphans"}}
	if len(runner.calls) != len(want) {
		t.Fatalf("got %d runner calls, want %d: %v", len(runner.calls), len(want), runner.calls)
	}
	if !reflect.DeepEqual(runner.calls[0], want[0]) {
		t.Errorf("call[0] = %v, want %v", runner.calls[0], want[0])
	}
}

func TestApply_RunnerFails_IsError(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	runner := &fakeRunner{err: errors.New("up boom")}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithRunner(runner))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	p := plan.New("", "acme/infra", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionCreate, Service: "api"})

	err = tgt.Apply(context.Background(), p)
	if err == nil {
		t.Fatal("expected error when runner fails, got nil")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("err = %v, want one naming the failing service", err)
	}
}

func TestApply_RunnerFailureReportsCompletedAndFailedActions(t *testing.T) {
	path := writeComposeFile(t)
	runner := &fakeRunner{errs: []error{nil, errors.New("worker boom")}}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(&fakeDockerClient{}), WithRunner(runner))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := plan.New("", "production", "abc123", time.Now()).
		AddAction(plan.Action{Kind: plan.ActionCreate, Service: "api"}).
		AddAction(plan.Action{Kind: plan.ActionRecreate, Service: "worker"})

	err = tgt.Apply(context.Background(), p)
	var applyErr *targets.ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("Apply error = %T %v, want *targets.ApplyError", err, err)
	}
	if len(applyErr.Completed) != 1 || applyErr.Completed[0].Service != "api" {
		t.Errorf("completed = %+v, want api", applyErr.Completed)
	}
	if applyErr.Failed.Service != "worker" {
		t.Errorf("failed = %+v, want worker", applyErr.Failed)
	}
	if !strings.Contains(err.Error(), "api:create") || !strings.Contains(err.Error(), "worker:recreate") {
		t.Errorf("error %q does not report completed and failed services", err)
	}
}

func TestApply_PartialFailureReportsAllBatchedOrphanRemovals(t *testing.T) {
	path := writeComposeFile(t)
	runner := &fakeRunner{errs: []error{nil, errors.New("worker boom")}}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(&fakeDockerClient{}), WithRunner(runner))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := plan.New("", "production", "abc123", time.Now()).
		AddAction(plan.Action{Kind: plan.ActionRemove, Service: "orphan-a"}).
		AddAction(plan.Action{Kind: plan.ActionRemove, Service: "orphan-b"}).
		AddAction(plan.Action{Kind: plan.ActionRecreate, Service: "worker"})

	err = tgt.Apply(context.Background(), p)
	var applyErr *targets.ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("Apply error = %T %v, want *targets.ApplyError", err, err)
	}
	if got := actionServices(applyErr.Completed); !reflect.DeepEqual(got, []string{"orphan-a", "orphan-b"}) {
		t.Errorf("completed services = %v, want both removed orphans", got)
	}
	if applyErr.Failed.Service != "worker" {
		t.Errorf("failed = %+v, want worker", applyErr.Failed)
	}
}

func actionServices(actions []plan.Action) []string {
	services := make([]string, 0, len(actions))
	for _, action := range actions {
		services = append(services, action.Service)
	}
	return services
}

func TestApply_NilPlan_IsError(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	tgt := newTarget(t, path, cli)

	if err := tgt.Apply(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil plan, got nil")
	}
}

func TestWithProjectName_OverridesDerived(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary("explicit", "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "none"),
		},
	}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithProjectName("explicit"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if _, ok := rs.Services["api"]; !ok {
		t.Errorf("api not found with explicit project name; services=%+v", rs.Services)
	}
}

func TestNormalizeProjectName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"MyApp", "myapp"},
		{"my-app_2", "my-app_2"},
		{"My App!", "myapp"},
		{"_-_foo", "foo"},
		{"", ""},
		{"UPPER-Case_42", "upper-case_42"},
	}
	for _, c := range cases {
		if got := normalizeProjectName(c.in); got != c.want {
			t.Errorf("normalizeProjectName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComposeProjectName_FromFilePath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{filepath.Join("/srv/app", config.DefaultComposeFile), "app"},
		{filepath.Join("/home/user/My Service", config.DefaultComposeFile), "myservice"},
		{filepath.Join("/root", config.DefaultComposeFile), "root"},
	}
	for _, c := range cases {
		if got := composeProjectName(c.path); got != c.want {
			t.Errorf("composeProjectName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestProjectNameUsesComposeProjectIdentity(t *testing.T) {
	dir := t.TempDir()
	fileName := filepath.Join(dir, "compose-a.yaml")
	pathName := filepath.Join(dir, "compose-b.yaml")

	fromFile := ProjectName(config.Target{File: fileName})
	fromPath := ProjectName(config.Target{Path: pathName})
	if fromFile != fromPath {
		t.Errorf("ProjectName differs for files in the same directory: %q != %q", fromFile, fromPath)
	}
}

func TestComposeProjectName_BareFilenameFallsBackToWorkingDir(t *testing.T) {
	// The §8 example uses the default target.file with no directory
	// component. The derived project name must fall back to the working
	// directory basename rather than empty, so Current() filters on a real
	// project label instead of matching nothing.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	want := normalizeProjectName(filepath.Base(wd))
	if got := composeProjectName(config.DefaultComposeFile); got != want {
		t.Errorf("composeProjectName(%q) = %q, want %q", config.DefaultComposeFile, got, want)
	}
	if got := composeProjectName(config.DefaultComposeFile); got == "" {
		t.Errorf("composeProjectName(%s) is empty, want working-dir basename", config.DefaultComposeFile)
	}
}

func TestToRuntimeService_StatusAndHealth(t *testing.T) {
	cases := []struct {
		name    string
		inspect container.InspectResponse
		want    state.RuntimeService
	}{
		{
			name:    "running healthy",
			inspect: inspect("api:1", "running", "healthy"),
			want:    state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"},
		},
		{
			name:    "exited no health",
			inspect: inspect("api:1", "exited", "none"),
			want:    state.RuntimeService{Status: "exited", Health: "", Image: "api:1"},
		},
		{
			name:    "running unhealthy",
			inspect: inspect("api:1", "running", "unhealthy"),
			want:    state.RuntimeService{Status: "running", Health: "unhealthy", Image: "api:1"},
		},
		{
			name:    "running starting",
			inspect: inspect("api:1", "running", "starting"),
			want:    state.RuntimeService{Status: "running", Health: "starting", Image: "api:1"},
		},
		{
			name:    "no state",
			inspect: container.InspectResponse{},
			want:    state.RuntimeService{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toRuntimeService(c.inspect); !reflect.DeepEqual(got, c.want) {
				t.Errorf("toRuntimeService = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestNew_UsesPathWhenFileEmpty(t *testing.T) {
	// cfg.Path is the §25 fallback when cfg.File is empty.
	path := writeComposeFile(t)
	tgt, err := New(config.Target{Type: config.TargetCompose, Path: path},
		WithDockerClient(&fakeDockerClient{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tgt.file != path {
		t.Errorf("file = %q, want %q", tgt.file, path)
	}
}

func TestNew_EmptyTypeAllowed(t *testing.T) {
	// An empty target type is accepted (the config loader defaults it); the
	// driver assumes compose.
	path := writeComposeFile(t)
	tgt, err := New(config.Target{File: path}, WithDockerClient(&fakeDockerClient{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tgt.file != path {
		t.Errorf("file = %q, want %q", tgt.file, path)
	}
}

func TestNew_RejectsNonComposeType(t *testing.T) {
	cases := []string{config.TargetKubernetes, config.TargetHelm, "weird"}
	for _, ty := range cases {
		_, err := New(config.Target{Type: ty, File: "x.yaml"},
			WithDockerClient(&fakeDockerClient{}))
		if err == nil {
			t.Errorf("type %q: expected error, got nil", ty)
		}
	}
}

func TestValidate_NilReceiver_IsError(t *testing.T) {
	var tgt *Target
	if err := tgt.Validate(context.Background()); err == nil {
		t.Fatal("expected error for nil receiver, got nil")
	}
}

func TestValidate_NilDockerClient_IsError(t *testing.T) {
	path := writeComposeFile(t)
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(&fakeDockerClient{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tgt.docker = nil
	if err := tgt.Validate(context.Background()); err == nil {
		t.Fatal("expected error for nil docker client, got nil")
	}
}

func TestValidate_NilRunner_IsError(t *testing.T) {
	path := writeComposeFile(t)
	tgt := newTarget(t, path, &fakeDockerClient{})
	tgt.runner = nil
	if err := tgt.Validate(context.Background()); err == nil {
		t.Fatal("expected error for nil compose runner, got nil")
	}
}

func TestCurrent_NilReceiver_IsError(t *testing.T) {
	var tgt *Target
	if _, err := tgt.Current(context.Background()); err == nil {
		t.Fatal("expected error for nil receiver, got nil")
	}
}

func TestCurrent_NilDockerClient_IsError(t *testing.T) {
	path := writeComposeFile(t)
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(&fakeDockerClient{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tgt.docker = nil
	if _, err := tgt.Current(context.Background()); err == nil {
		t.Fatal("expected error for nil docker client, got nil")
	}
}

func TestCurrent_ListIncludesStoppedContainers_AllTrue(t *testing.T) {
	// The drift-visibility guarantee depends on ContainerList being called
	// with All=true so stopped containers are returned (docs/ACCORDA.md §5.3).
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	tgt := newTarget(t, path, cli)

	if _, err := tgt.Current(context.Background()); err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !cli.lastOptions.All {
		t.Errorf("ContainerList All = false, want true (stopped containers must be included)")
	}
}

func TestWithProjectName_NormalizesOverride(t *testing.T) {
	// WithProjectName must normalize so an override with uppercase/space
	// matches the com.docker.compose.project label Compose applies.
	path := writeComposeFile(t)
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary("myapp", "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "none"),
		},
	}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithProjectName("My App!"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tgt.project != "myapp" {
		t.Fatalf("project = %q, want myapp after normalization", tgt.project)
	}
	if _, err := tgt.Current(context.Background()); err != nil {
		t.Fatalf("Current: %v", err)
	}
}

func TestProjectFilters_SelectsProjectLabel(t *testing.T) {
	args := projectFilters("myapp")
	values := args.Get("label")
	if len(values) != 1 {
		t.Fatalf("got %d label filters, want 1", len(values))
	}
	want := composeProjectLabel + "=myapp"
	if values[0] != want {
		t.Errorf("label = %q, want %q", values[0], want)
	}
}

func TestProjectFilters_ProjectWithHyphens(t *testing.T) {
	args := projectFilters("my-app_2")
	values := args.Get("label")
	if len(values) != 1 {
		t.Fatalf("got %d label filters, want 1", len(values))
	}
	want := composeProjectLabel + "=my-app_2"
	if values[0] != want {
		t.Errorf("label = %q, want %q", values[0], want)
	}
}

func TestServiceName_NilLabelsReturnsEmpty(t *testing.T) {
	if got := serviceName(nil); got != "" {
		t.Errorf("serviceName(nil) = %q, want empty", got)
	}
	if got := serviceName(map[string]string{}); got != "" {
		t.Errorf("serviceName(empty) = %q, want empty", got)
	}
	if got := serviceName(map[string]string{composeServiceLabel: "api"}); got != "api" {
		t.Errorf("serviceName = %q, want api", got)
	}
}

func TestApplyDesired_MaterializesAndApplies(t *testing.T) {
	// ApplyDesired must materialize the given services into the on-disk
	// Compose file, then plan and apply them, so the runner operates against
	// the restored image rather than the file's previous content
	// (docs/ACCORDA.md §20).
	path := writeComposeFile(t) // contains api:1
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary("compose", "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:2", "running", "none"),
		},
	}
	runner := &fakeRunner{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithRunner(runner), WithProjectName("compose"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	desired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "prev123",
		Services: map[string]state.Service{
			"api": {Image: "api:1"},
		},
	}
	p, err := tgt.ApplyDesired(context.Background(), desired)
	if err != nil {
		t.Fatalf("ApplyDesired: %v", err)
	}
	if p == nil {
		t.Fatal("ApplyDesired returned nil plan")
	}

	// The on-disk file must now declare api:1 (the restored image).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if !strings.Contains(string(data), "api:1") {
		t.Errorf("compose file after ApplyDesired = %q, want it to contain api:1", data)
	}
	// The runner must have issued an `up -d api` against the restored file.
	if len(runner.calls) == 0 {
		t.Fatal("runner had no calls, want an up -d")
	}
}

func TestApplyDesired_NilDesired(t *testing.T) {
	path := writeComposeFile(t)
	tgt := newTarget(t, path, &fakeDockerClient{})
	if _, err := tgt.ApplyDesired(context.Background(), nil); err == nil {
		t.Fatal("ApplyDesired(nil) expected error, got nil")
	}
}

func TestWriteComposeServices_RoundTripsImage(t *testing.T) {
	// writeComposeServices must write the image reference into the file so a
	// later LoadFile reads it back (docs/ACCORDA.md §20).
	path := filepath.Join(t.TempDir(), config.DefaultComposeFile)
	services := map[string]state.Service{
		"api": {Image: "busybox:1.36", Command: []string{"sh", "-c", "sleep 300"}},
	}
	if err := writeComposeServices(path, services); err != nil {
		t.Fatalf("writeComposeServices: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := loaded["api"].Image; got != "busybox:1.36" {
		t.Errorf("api.Image after round-trip = %q, want busybox:1.36", got)
	}
}

func TestMergeRuntime_DisagreeIsDegraded(t *testing.T) {
	cases := []struct {
		name string
		a, b state.RuntimeService
	}{
		{
			name: "status disagreement",
			a:    state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"},
			b:    state.RuntimeService{Status: "exited", Health: "", Image: "api:1"},
		},
		{
			name: "health disagreement",
			a:    state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"},
			b:    state.RuntimeService{Status: "running", Health: "unhealthy", Image: "api:1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := state.RuntimeService{Status: degradedStatus, Health: "", Image: "api:1"}
			if got := mergeRuntime(c.a, c.b); !reflect.DeepEqual(got, want) {
				t.Errorf("mergeRuntime = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMergeRuntime_AgreeIsShared(t *testing.T) {
	a := state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"}
	b := state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"}
	if got := mergeRuntime(a, b); !reflect.DeepEqual(got, a) {
		t.Errorf("mergeRuntime = %+v, want %+v", got, a)
	}
}

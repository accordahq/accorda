package image

import (
	"context"
	"errors"
	"io"
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
	"accorda/internal/sources"
	"accorda/internal/targets"
)

// fakeDockerClient is a test double for the docker.Client seam, mirroring the
// compose package's fake so the image target can be exercised without a
// running Docker daemon.
type fakeDockerClient struct {
	pingErr        error
	containers     []container.Summary
	inspected      map[string]container.InspectResponse
	inspectErr     map[string]error
	listErr        error
	images         []image.Summary
	imageErr       error
	imageInspected map[string]image.InspectResponse
	logsStream     io.ReadCloser
	logsErr        error
	lastOptions    container.ListOptions
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

func (f *fakeDockerClient) ContainerLogs(_ context.Context, _ string, _ container.LogsOptions) (io.ReadCloser, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	return f.logsStream, nil
}

// fakeRunner is a test double for the Runner seam. It records every
// invocation so tests can assert the exact `docker` subcommands Apply issued.
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

// summary builds a container.Summary carrying the accorda.image.service
// label so Current selects it.
func summary(name string) container.Summary {
	return container.Summary{
		ID:     "id-" + name,
		Labels: map[string]string{containerNameLabel: name},
	}
}

// inspect builds a container.InspectResponse with the given image, state, and
// health status, mirroring the compose test helper.
func inspect(img, status, health string) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Image: "sha256:" + img,
			State: &container.State{Status: status, Health: &container.Health{Status: health}},
		},
		Config: &container.Config{Image: img},
	}
}

func newTarget(t *testing.T, cli *fakeDockerClient, runner *fakeRunner) *Target {
	t.Helper()
	tgt, err := New(
		config.Target{Type: config.TargetImage, Image: "edge-agent:1.2.3"},
		"edge-agent",
		WithDockerClient(cli), WithRunner(runner),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tgt
}

func TestCompileTime_TargetImplementsInterface(t *testing.T) {
	var _ targets.Target = (*Target)(nil)
	var _ targets.LogTarget = (*Target)(nil)
}

func TestNew_RequiresImage(t *testing.T) {
	_, err := New(config.Target{Type: config.TargetImage}, "svc", WithDockerClient(&fakeDockerClient{}))
	if err == nil {
		t.Fatal("expected error for empty image, got nil")
	}
	if !strings.Contains(err.Error(), "image is required") {
		t.Errorf("err = %v, want one mentioning image required", err)
	}
}

func TestNew_RequiresName(t *testing.T) {
	_, err := New(config.Target{Type: config.TargetImage, Image: "img:1"}, "", WithDockerClient(&fakeDockerClient{}))
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	if !strings.Contains(err.Error(), "service name is required") {
		t.Errorf("err = %v, want one mentioning service name required", err)
	}
}

func TestNew_RejectsNonImageType(t *testing.T) {
	cases := []string{config.TargetCompose, config.TargetKubernetes, "weird"}
	for _, ty := range cases {
		_, err := New(config.Target{Type: ty, Image: "img:1"}, "svc", WithDockerClient(&fakeDockerClient{}))
		if err == nil {
			t.Errorf("type %q: expected error, got nil", ty)
		}
	}
}

func TestValidate_PingsDocker(t *testing.T) {
	tgt := newTarget(t, &fakeDockerClient{}, &fakeRunner{})
	if err := tgt.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_PingFails_IsError(t *testing.T) {
	tgt := newTarget(t, &fakeDockerClient{pingErr: errors.New("connection refused")}, &fakeRunner{})
	err := tgt.Validate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker ping") {
		t.Fatalf("Validate() error = %v, want docker ping failure", err)
	}
}

func TestDesired_BuildsSingleServiceFromConfig(t *testing.T) {
	tgt, err := New(
		config.Target{
			Type:  config.TargetImage,
			Image: "edge-agent:1.2.3",
			Env:   map[string]string{"REGION": "eu-west-1", "LOG_LEVEL": "info"},
			Ports: []string{"8080:8080"},
		},
		"edge-agent",
		WithDockerClient(&fakeDockerClient{}), WithRunner(&fakeRunner{}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	revision := sources.NewRevision(sources.Commit{SHA: "abc123", Branch: "main"}, "acme/infra", t.TempDir(), nil, nil)
	got, err := tgt.Desired(context.Background(), revision)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if got.Repository != "acme/infra" || got.Branch != "main" || got.Commit != "abc123" {
		t.Errorf("Desired metadata = %+v, want source metadata preserved", got)
	}
	svc, ok := got.Services["edge-agent"]
	if !ok {
		t.Fatalf("Services missing edge-agent: %+v", got.Services)
	}
	if svc.Image != "edge-agent:1.2.3" {
		t.Errorf("Image = %q, want edge-agent:1.2.3", svc.Image)
	}
	if svc.Env["REGION"] != "eu-west-1" || svc.Env["LOG_LEVEL"] != "info" {
		t.Errorf("Env = %+v, want REGION and LOG_LEVEL", svc.Env)
	}
	if len(svc.Ports) != 1 || svc.Ports[0].Host != "8080" || svc.Ports[0].Container != "8080" {
		t.Errorf("Ports = %+v, want 8080:8080", svc.Ports)
	}
}

func TestDesired_NilSourceKeepsEmptyMetadata(t *testing.T) {
	tgt := newTarget(t, &fakeDockerClient{}, &fakeRunner{})
	got, err := tgt.Desired(context.Background(), nil)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if got.Commit != "" {
		t.Errorf("Commit = %q, want empty with nil source", got.Commit)
	}
	if len(got.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(got.Services))
	}
}

func TestCurrent_MapsContainerToRuntimeState(t *testing.T) {
	cli := &fakeDockerClient{
		containers: []container.Summary{summary("edge-agent")},
		inspected: map[string]container.InspectResponse{
			"id-edge-agent": inspect("edge-agent:1.2.3", "running", "healthy"),
		},
	}
	tgt := newTarget(t, cli, &fakeRunner{})

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	want := state.RuntimeService{Status: "running", Health: "healthy", Image: "edge-agent:1.2.3"}
	if got := rs.Services["edge-agent"]; !reflect.DeepEqual(got, want) {
		t.Errorf("edge-agent = %+v, want %+v", got, want)
	}
	// The list filter must select the target's container by label.
	if got := cli.lastOptions.Filters.Get("label"); len(got) == 0 {
		t.Error("ContainerList called with no label filter")
	} else if !strings.Contains(got[0], containerNameLabel) || !strings.Contains(got[0], "edge-agent") {
		t.Errorf("label filter = %v, want one referencing edge-agent", got)
	}
}

func TestCurrent_EmptyWhenNoContainer(t *testing.T) {
	cli := &fakeDockerClient{containers: nil}
	tgt := newTarget(t, cli, &fakeRunner{})
	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(rs.Services) != 0 {
		t.Errorf("Services = %d, want 0 for missing container", len(rs.Services))
	}
}

func TestPlan_CreateWhenAbsent(t *testing.T) {
	cli := &fakeDockerClient{containers: nil}
	tgt := newTarget(t, cli, &fakeRunner{})
	desired := &state.DesiredState{
		Commit:   "abc123",
		Services: map[string]state.Service{"edge-agent": {Image: "edge-agent:1.2.3"}},
	}
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planHas(p, plan.ActionCreate, "edge-agent") {
		t.Errorf("plan = %v, want a create action for edge-agent", p.Actions)
	}
}

func TestPlan_NoopWhenConverged(t *testing.T) {
	cli := &fakeDockerClient{
		containers: []container.Summary{summary("edge-agent")},
		inspected: map[string]container.InspectResponse{
			"id-edge-agent": inspect("edge-agent:1.2.3", "running", ""),
		},
	}
	tgt := newTarget(t, cli, &fakeRunner{})
	desired := &state.DesiredState{
		Commit:   "abc123",
		Services: map[string]state.Service{"edge-agent": {Image: "edge-agent:1.2.3"}},
	}
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planHas(p, plan.ActionNoop, "edge-agent") {
		t.Errorf("plan = %v, want a noop action for converged edge-agent", p.Actions)
	}
	if p.Changed() {
		t.Errorf("plan.Changed = true, want false for converged target")
	}
}

func TestPlan_RecreateWhenImageChanged(t *testing.T) {
	cli := &fakeDockerClient{
		containers: []container.Summary{summary("edge-agent")},
		inspected: map[string]container.InspectResponse{
			"id-edge-agent": inspect("edge-agent:1.2.2", "running", ""),
		},
	}
	tgt := newTarget(t, cli, &fakeRunner{})
	desired := &state.DesiredState{
		Commit:   "abc123",
		Services: map[string]state.Service{"edge-agent": {Image: "edge-agent:1.2.3"}},
	}
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planHas(p, plan.ActionRecreate, "edge-agent") {
		t.Errorf("plan = %v, want a recreate action for changed image", p.Actions)
	}
}

func TestApply_RunsContainerWithImageEnvPorts(t *testing.T) {
	runner := &fakeRunner{}
	tgt, err := New(
		config.Target{
			Type:  config.TargetImage,
			Image: "edge-agent:1.2.3",
			Env:   map[string]string{"REGION": "eu-west-1"},
			Ports: []string{"8080:8080"},
		},
		"edge-agent",
		WithDockerClient(&fakeDockerClient{}), WithRunner(runner),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := plan.New("dep-1", "", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionCreate, Service: "edge-agent", Image: "edge-agent:1.2.3"})

	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Expect: rm -f <name>, then run -d --name <name> --label ... -e ... -p ... <image>
	if len(runner.calls) < 2 {
		t.Fatalf("runner calls = %v, want at least 2", runner.calls)
	}
	rmCall := runner.calls[0]
	if rmCall[0] != "rm" || rmCall[1] != "-f" || rmCall[2] != "edge-agent" {
		t.Errorf("rm call = %v, want [rm -f edge-agent]", rmCall)
	}
	runCall := runner.calls[1]
	if runCall[0] != "run" || runCall[1] != "-d" {
		t.Errorf("run call = %v, want run -d ...", runCall)
	}
	if !slices.Contains(runCall, "edge-agent:1.2.3") {
		t.Errorf("run call = %v, want image edge-agent:1.2.3", runCall)
	}
	if !slices.Contains(runCall, "REGION=eu-west-1") {
		t.Errorf("run call = %v, want env REGION=eu-west-1", runCall)
	}
	if !slices.Contains(runCall, "8080:8080") {
		t.Errorf("run call = %v, want port 8080:8080", runCall)
	}
}

func TestApply_PullBeforeRun(t *testing.T) {
	runner := &fakeRunner{}
	tgt := newTarget(t, &fakeDockerClient{}, runner)
	p := plan.New("dep-1", "", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionPull, Service: "edge-agent", Image: "edge-agent:1.2.3"})
	p.AddAction(plan.Action{Kind: plan.ActionCreate, Service: "edge-agent", Image: "edge-agent:1.2.3"})

	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(runner.calls) < 1 || runner.calls[0][0] != "pull" || runner.calls[0][1] != "edge-agent:1.2.3" {
		t.Errorf("first call = %v, want [pull edge-agent:1.2.3]", runner.calls)
	}
}

func TestApply_PartialFailureReturnsApplyError(t *testing.T) {
	runner := &fakeRunner{errs: []error{nil, errors.New("run boom")}}
	tgt := newTarget(t, &fakeDockerClient{}, runner)
	p := plan.New("dep-1", "", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionCreate, Service: "edge-agent", Image: "edge-agent:1.2.3"})
	p.AddAction(plan.Action{Kind: plan.ActionStart, Service: "edge-agent", Image: "edge-agent:1.2.3"})

	err := tgt.Apply(context.Background(), p)
	if err == nil {
		t.Fatal("expected ApplyError, got nil")
	}
	var ae *targets.ApplyError
	if !errors.As(err, &ae) {
		t.Errorf("err = %v, want *targets.ApplyError", err)
	}
}

func TestApply_NoopPlanDoesNothing(t *testing.T) {
	runner := &fakeRunner{}
	tgt := newTarget(t, &fakeDockerClient{}, runner)
	p := plan.New("dep-1", "", "abc123", time.Now())
	p.AddAction(plan.NoopFor("edge-agent"))
	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner calls = %v, want none for noop plan", runner.calls)
	}
}

func TestParsePorts(t *testing.T) {
	cases := []struct {
		in   string
		want state.Port
	}{
		{"8080:8080", state.Port{Host: "8080", Container: "8080", Protocol: "tcp"}},
		{"8080", state.Port{Container: "8080", Protocol: "tcp"}},
		{"127.0.0.1:8080:8080", state.Port{HostIP: "127.0.0.1", Host: "8080", Container: "8080", Protocol: "tcp"}},
		{"8080/udp", state.Port{Container: "8080", Protocol: "udp"}},
	}
	for _, c := range cases {
		got := parsePort(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePort(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// planHas reports whether p contains an action of kind for service.
func planHas(p *plan.Plan, kind plan.ActionKind, service string) bool {
	for _, a := range p.Actions {
		if a.Kind == kind && a.Service == service {
			return true
		}
	}
	return false
}

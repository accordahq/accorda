package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"

	"accorda/internal/config"
	"accorda/internal/core/state"
	"accorda/internal/targets"
)

// fakeDockerClient is a test double for the dockerClient seam. It returns
// canned responses for Ping, ContainerList, and ContainerInspect so the
// Compose target can be exercised without a running Docker daemon.
type fakeDockerClient struct {
	pingErr    error
	containers []container.Summary
	inspected  map[string]container.InspectResponse
	inspectErr map[string]error
	listErr    error
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

// writeComposeFile writes a minimal valid Compose file in a temp dir and
// returns its path so Target.Validate can load it.
func writeComposeFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("services:\n  api:\n    image: api:1\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return path
}

// newTarget builds a Target with a fake client and the Compose file at path,
// using the project name derived from the file's directory basename.
func newTarget(t *testing.T, path string, cli *fakeDockerClient) *Target {
	t.Helper()
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path}, WithDockerClient(cli))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tgt
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

func TestCurrent_MapsContainersToRuntimeState(t *testing.T) {
	path := writeComposeFile(t)
	dir := filepath.Base(filepath.Dir(path))
	project := normalizeProjectName(dir)
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{
				ID: "c1",
				Labels: map[string]string{
					composeProjectLabel: project,
					composeServiceLabel: "api",
				},
			},
			{
				ID: "c2",
				Labels: map[string]string{
					composeProjectLabel: project,
					composeServiceLabel: "worker",
				},
			},
		},
		inspected: map[string]container.InspectResponse{
			"c1": {ContainerJSONBase: &container.ContainerJSONBase{
				Image: "api:1",
				State: &container.State{Status: "running", Health: &container.Health{Status: "healthy"}},
			}},
			"c2": {ContainerJSONBase: &container.ContainerJSONBase{
				Image: "worker:1",
				State: &container.State{Status: "running"},
			}},
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
			{ID: "c1", Labels: map[string]string{
				composeProjectLabel: project, composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"c1": {ContainerJSONBase: &container.ContainerJSONBase{
				Image: "api:1",
				State: &container.State{Status: "exited"},
			}},
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

func TestCurrent_NoHealthcheckLabel_IsEmpty(t *testing.T) {
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "c1", Labels: map[string]string{
				composeProjectLabel: project, composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"c1": {ContainerJSONBase: &container.ContainerJSONBase{
				Image: "api:1",
				State: &container.State{Status: "running", Health: &container.Health{Status: "none"}},
			}},
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := rs.Services["api"].Health; got != "" {
		t.Errorf("Health = %q, want empty for no healthcheck", got)
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
			{ID: "c1", Labels: map[string]string{
				composeProjectLabel: project, composeServiceLabel: "api"}},
		},
		inspectErr: map[string]error{"c1": errors.New("inspect boom")},
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

func TestPlan_Apply_Health_NotImplemented(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	tgt := newTarget(t, path, cli)

	if _, err := tgt.Plan(context.Background(), &state.DesiredState{}); !errors.Is(err, targets.ErrNotImplemented) {
		t.Errorf("Plan err = %v, want ErrNotImplemented", err)
	}
	if err := tgt.Apply(context.Background(), nil); !errors.Is(err, targets.ErrNotImplemented) {
		t.Errorf("Apply err = %v, want ErrNotImplemented", err)
	}
	if _, err := tgt.Health(context.Background()); !errors.Is(err, targets.ErrNotImplemented) {
		t.Errorf("Health err = %v, want ErrNotImplemented", err)
	}
}

func TestWithProjectName_OverridesDerived(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "c1", Labels: map[string]string{
				composeProjectLabel: "explicit", composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"c1": {ContainerJSONBase: &container.ContainerJSONBase{
				Image: "api:1",
				State: &container.State{Status: "running"},
			}},
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
		{"/srv/app/compose.yaml", "app"},
		{"compose.yaml", ""},
		{"/home/user/My Service/compose.yaml", "myservice"},
	}
	for _, c := range cases {
		if got := composeProjectName(c.path); got != c.want {
			t.Errorf("composeProjectName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestToRuntimeService_StatusAndHealth(t *testing.T) {
	cases := []struct {
		name    string
		inspect container.InspectResponse
		want    state.RuntimeService
	}{
		{
			name: "running healthy",
			inspect: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Image: "api:1",
					State: &container.State{Status: "running", Health: &container.Health{Status: "healthy"}},
				},
			},
			want: state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"},
		},
		{
			name: "exited no health",
			inspect: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Image: "api:1",
					State: &container.State{Status: "exited"},
				},
			},
			want: state.RuntimeService{Status: "exited", Health: "", Image: "api:1"},
		},
		{
			name: "running unhealthy",
			inspect: container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Image: "api:1",
					State: &container.State{Status: "running", Health: &container.Health{Status: "unhealthy"}},
				},
			},
			want: state.RuntimeService{Status: "running", Health: "unhealthy", Image: "api:1"},
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

func TestCurrent_HealthStarting_Mapped(t *testing.T) {
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "c1", Labels: map[string]string{
				composeProjectLabel: project, composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"c1": {ContainerJSONBase: &container.ContainerJSONBase{
				Image: "api:1",
				State: &container.State{Status: "running", Health: &container.Health{Status: "starting"}},
			}},
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := rs.Services["api"].Health; got != "starting" {
		t.Errorf("Health = %q, want starting", got)
	}
}

func TestCurrent_OnlyProjectLabelContainersUsed(t *testing.T) {
	// Containers are pre-filtered by the engine via the label filter; the
	// returned set must include every listed container that carries the
	// service label, and the inspect image is what surfaces.
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "c1", Labels: map[string]string{
				composeProjectLabel: project, composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"c1": {ContainerJSONBase: &container.ContainerJSONBase{
				Image: "ghcr.io/acme/api:2.4.1",
				State: &container.State{Status: "running"},
			}},
		},
	}
	tgt := newTarget(t, path, cli)

	rs, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := rs.Services["api"].Image; got != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("Image = %q, want ghcr.io/acme/api:2.4.1", got)
	}
}

func TestWithProjectName_NormalizesOverride(t *testing.T) {
	// WithProjectName must normalize so an override with uppercase/space
	// matches the com.docker.compose.project label Compose applies.
	path := writeComposeFile(t)
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{ID: "c1", Labels: map[string]string{
				composeProjectLabel: "myapp", composeServiceLabel: "api"}},
		},
		inspected: map[string]container.InspectResponse{
			"c1": {ContainerJSONBase: &container.ContainerJSONBase{
				State: &container.State{Status: "running"},
			}},
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

func TestToRuntimeService_StartingHealth(t *testing.T) {
	inspect := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Image: "api:1",
			State: &container.State{Status: "running", Health: &container.Health{Status: "starting"}},
		},
	}
	want := state.RuntimeService{Status: "running", Health: "starting", Image: "api:1"}
	if got := toRuntimeService(inspect); !reflect.DeepEqual(got, want) {
		t.Errorf("toRuntimeService = %+v, want %+v", got, want)
	}
}

func TestToRuntimeService_NoHealthcheckFieldIsEmpty(t *testing.T) {
	inspect := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Image: "api:1",
			State: &container.State{Status: "running", Health: &container.Health{Status: container.NoHealthcheck}},
		},
	}
	want := state.RuntimeService{Status: "running", Health: "", Image: "api:1"}
	if got := toRuntimeService(inspect); !reflect.DeepEqual(got, want) {
		t.Errorf("toRuntimeService = %+v, want %+v", got, want)
	}
}

func TestBaseName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"compose.yaml", "compose.yaml"},
		{"/srv/app/compose.yaml", "compose.yaml"},
		{"a/b/c", "c"},
		{"C:\\Users\\me\\compose.yaml", "compose.yaml"},
		{"no-path", "no-path"},
		{"", ""},
	}
	for _, c := range cases {
		if got := baseName(c.in); got != c.want {
			t.Errorf("baseName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTrimSlashes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/srv/app/", "/srv/app"},
		{"compose.yaml", "compose.yaml"},
		{"a/b\\", "a/b"},
		{"", ""},
	}
	for _, c := range cases {
		if got := trimSlashes(c.in); got != c.want {
			t.Errorf("trimSlashes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFileDir(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/srv/app/compose.yaml", "app"},
		{"compose.yaml", ""},
		{"/home/user/My Service/compose.yaml", "myservice"},
		{"", ""},
		{"/root/compose.yaml", "root"},
	}
	for _, c := range cases {
		if got := fileDir(c.in); got != c.want {
			t.Errorf("fileDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

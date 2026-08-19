package compose

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"accorda/internal/config"
	"accorda/internal/core/health"
	"accorda/internal/core/state"
)

func TestHealth_AllHealthy(t *testing.T) {
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
			summary(project, "worker"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api":    inspect("api:1", "running", "healthy"),
			"id-worker": inspect("worker:1", "running", "healthy"),
		},
	}
	tgt := newTarget(t, path, cli)

	h, err := tgt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall != health.StatusHealthy {
		t.Errorf("Overall = %q, want %q", h.Overall, health.StatusHealthy)
	}
	if !h.Healthy {
		t.Error("Healthy = false, want true")
	}
	if h.Services["api"].Status != health.StatusHealthy {
		t.Errorf("api status = %q, want %q", h.Services["api"].Status, health.StatusHealthy)
	}
}

func TestHealth_Unhealthy(t *testing.T) {
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "unhealthy"),
		},
	}
	tgt := newTarget(t, path, cli)

	h, err := tgt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall != health.StatusUnhealthy {
		t.Errorf("Overall = %q, want %q", h.Overall, health.StatusUnhealthy)
	}
	if h.Healthy {
		t.Error("Healthy = true, want false")
	}
}

func TestHealth_NoHealthcheck_IsUnknown(t *testing.T) {
	// A service with no healthcheck (Health == "") must be reported as
	// unknown, not healthy or unhealthy (docs/ACCORDA.md §19).
	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "none"),
		},
	}
	tgt := newTarget(t, path, cli)

	h, err := tgt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall != health.StatusUnknown {
		t.Errorf("Overall = %q, want %q", h.Overall, health.StatusUnknown)
	}
	if h.Healthy {
		t.Error("Healthy = true, want false")
	}
}

func TestHealth_EmptyRuntime_IsUnknown(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{containers: nil}
	tgt := newTarget(t, path, cli)

	h, err := tgt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall != health.StatusUnknown {
		t.Errorf("Overall = %q, want %q", h.Overall, health.StatusUnknown)
	}
}

func TestHealth_CurrentFails_IsError(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{listErr: errors.New("list boom")}
	tgt := newTarget(t, path, cli)

	if _, err := tgt.Health(context.Background()); err == nil {
		t.Fatal("expected error when Current fails, got nil")
	}
}

func TestHealth_NilTarget_IsError(t *testing.T) {
	var tgt *Target
	if _, err := tgt.Health(context.Background()); err == nil {
		t.Fatal("expected error for nil target, got nil")
	}
}

func TestHealth_NilDockerClient_IsError(t *testing.T) {
	path := writeComposeFile(t)
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tgt.docker = nil
	if _, err := tgt.Health(context.Background()); err == nil {
		t.Fatal("expected error for nil docker client, got nil")
	}
}

func TestHealth_StartingThenHealthy_Converges(t *testing.T) {
	// A service that is starting on the first poll and healthy on the second
	// must converge to healthy without waiting for the full timeout.
	old := healthPollInterval
	healthPollInterval = time.Millisecond
	t.Cleanup(func() { healthPollInterval = old })

	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &transitionDockerClient{
		project: project,
		healths: []string{"starting", "healthy"},
	}
	tgt := newTarget(t, path, cli)
	tgt.healthTimeout = 5 * time.Second

	h, err := tgt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall != health.StatusHealthy {
		t.Errorf("Overall = %q, want %q", h.Overall, health.StatusHealthy)
	}
	if cli.inspectCalls != 2 {
		t.Errorf("inspect calls = %d, want 2", cli.inspectCalls)
	}
}

// transitionDockerClient is a dockerClient fake that returns a different
// health status on each ContainerInspect call, so tests can exercise the
// poll-until-converged path of Health.
type transitionDockerClient struct {
	project      string
	healths      []string
	inspectCalls int
}

func (f *transitionDockerClient) Ping(context.Context) (types.Ping, error) {
	return types.Ping{}, nil
}

func (f *transitionDockerClient) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	return []container.Summary{summary(f.project, "api")}, nil
}

func (f *transitionDockerClient) ContainerInspect(_ context.Context, _ string) (container.InspectResponse, error) {
	idx := f.inspectCalls
	if idx >= len(f.healths) {
		idx = len(f.healths) - 1
	}
	f.inspectCalls++
	return inspect("api:1", "running", f.healths[idx]), nil
}

func (f *transitionDockerClient) ImageList(context.Context, image.ListOptions) ([]image.Summary, error) {
	return nil, nil
}

func (f *transitionDockerClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	return image.InspectResponse{}, nil
}

func TestHealth_Timeout_ReportsUnhealthy(t *testing.T) {
	// A service stuck in "starting" past the timeout must be reported as
	// unhealthy with a timeout message, not silently declared healthy.
	old := healthPollInterval
	healthPollInterval = time.Millisecond
	t.Cleanup(func() { healthPollInterval = old })

	path := writeComposeFile(t)
	project := normalizeProjectName(filepath.Base(filepath.Dir(path)))
	cli := &fakeDockerClient{
		containers: []container.Summary{
			summary(project, "api"),
		},
		inspected: map[string]container.InspectResponse{
			"id-api": inspect("api:1", "running", "starting"),
		},
	}
	tgt := newTarget(t, path, cli)
	tgt.healthTimeout = 1 * time.Millisecond

	h, err := tgt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall != health.StatusUnhealthy {
		t.Errorf("Overall = %q, want %q", h.Overall, health.StatusUnhealthy)
	}
	if h.Services["api"].Status != health.StatusUnhealthy {
		t.Errorf("api status = %q, want %q", h.Services["api"].Status, health.StatusUnhealthy)
	}
	if h.Services["api"].Message == "" {
		t.Error("api message is empty, want a timeout message")
	}
}

func TestHealthStatus_Mapping(t *testing.T) {
	cases := []struct {
		in   string
		want health.Status
	}{
		{"healthy", health.StatusHealthy},
		{"starting", health.StatusStarting},
		{"unhealthy", health.StatusUnhealthy},
		{"", health.StatusUnknown},
		{"none", health.StatusUnhealthy},
	}
	for _, c := range cases {
		if got := healthStatus(c.in); got != c.want {
			t.Errorf("healthStatus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHealthFromRuntime_NilRuntime(t *testing.T) {
	h := healthFromRuntime(nil, time.Unix(0, 0))
	if h.Overall != health.StatusUnknown {
		t.Errorf("Overall = %q, want %q", h.Overall, health.StatusUnknown)
	}
}

func TestHealthFromRuntime_MapsServices(t *testing.T) {
	runtime := &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"api":    {Health: "healthy"},
			"worker": {Health: ""},
		},
	}
	h := healthFromRuntime(runtime, time.Unix(0, 0))
	if h.Services["api"].Status != health.StatusHealthy {
		t.Errorf("api = %q, want %q", h.Services["api"].Status, health.StatusHealthy)
	}
	if h.Services["worker"].Status != health.StatusUnknown {
		t.Errorf("worker = %q, want %q", h.Services["worker"].Status, health.StatusUnknown)
	}
}

func TestWithHealthTimeout_OverridesDefault(t *testing.T) {
	// WithHealthTimeout must be honored through New so a caller can supply a
	// non-default health.timeout (docs/ACCORDA.md §19).
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithHealthTimeout(300*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tgt.healthTimeout != 300*time.Second {
		t.Errorf("healthTimeout = %v, want %v", tgt.healthTimeout, 300*time.Second)
	}
}

func TestNew_DefaultHealthTimeout(t *testing.T) {
	path := writeComposeFile(t)
	cli := &fakeDockerClient{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path}, WithDockerClient(cli))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tgt.healthTimeout != defaultHealthTimeout {
		t.Errorf("healthTimeout = %v, want %v", tgt.healthTimeout, defaultHealthTimeout)
	}
}

//go:build integration

// The integration build tag keeps these tests out of the default `go test`
// run because they require a running Docker daemon and the `docker compose`
// CLI. Run with:
//
//	go test ./internal/targets/compose/ -tags integration
package compose

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/health"
	"accorda/internal/core/state"
	"accorda/internal/targets"
	"accorda/internal/testutil"
)

// integrationCompose is a minimal Compose file with a single service that
// carries a healthcheck so the full validate → apply → current → health
// lifecycle can be exercised against a real Docker daemon. busybox provides
// `true`, so the healthcheck reports healthy without network access.
const integrationCompose = `services:
  api:
    image: busybox:1.36
    command: ["sh", "-c", "echo accorda-log-ready; sleep 300"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 3
`

// writeIntegrationCompose writes the integration Compose file into a fresh
// temp directory and returns the file path and the derived project name.
func writeIntegrationCompose(t *testing.T) (path, project string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte(integrationCompose), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return path, composeProjectName(path)
}

// newIntegrationTarget builds a real Compose target (no injected seams) for
// the given compose file, with a short health timeout so the test does not
// wait the full default.
func newIntegrationTarget(t *testing.T, path string) *Target {
	t.Helper()
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithPullPolicy(config.PullNever),
		WithHealthTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tgt
}

// down tears down the compose project so the test leaves no containers
// behind. It is best-effort: a failure here is reported but does not fail the
// test, because the assertion under test has already run.
func down(t *testing.T, path, project string) {
	t.Helper()
	runner := cliRunner{file: path, project: project}
	if err := runner.Run(context.Background(), "down", "--remove-orphans"); err != nil {
		t.Logf("cleanup docker compose down: %v", err)
	}
}

func TestComposeTarget_ValidateAgainstRealDaemon(t *testing.T) {
	testutil.RequireDocker(t)
	path, _ := writeIntegrationCompose(t)
	tgt := newIntegrationTarget(t, path)

	if err := tgt.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestComposeTarget_ApplyCurrentHealthLifecycle(t *testing.T) {
	testutil.RequireCompose(t)
	path, project := writeIntegrationCompose(t)
	t.Cleanup(func() { down(t, path, project) })

	tgt := newIntegrationTarget(t, path)
	ctx := context.Background()

	if err := tgt.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Plan against an empty runtime: the service must be created.
	desired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "abc123",
		Services: map[string]state.Service{
			"api": {Image: "busybox:1.36"},
		},
	}
	p, err := tgt.Plan(ctx, desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !p.Changed() {
		t.Fatal("plan should be changed for an empty runtime")
	}

	// Apply the plan: the container must come up.
	if err := tgt.Apply(ctx, p); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Current must report the running service.
	runtime, err := tgt.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	api, ok := runtime.Services["api"]
	if !ok {
		t.Fatalf("runtime missing api service: %+v", runtime.Services)
	}
	if api.Status != state.RunningStatus {
		t.Errorf("api.Status = %q, want %q", api.Status, state.RunningStatus)
	}

	// Logs must fetch the service's real container output through the Docker
	// API and decode it before returning it to the caller.
	var logs bytes.Buffer
	if err := tgt.Logs(ctx, "api", targets.LogOptions{}, &logs, &logs); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(logs.String(), "accorda-log-ready") {
		t.Errorf("Logs output = %q, want startup marker", logs.String())
	}

	// Health must converge to healthy (the healthcheck reports healthy).
	h, err := tgt.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall != health.StatusHealthy {
		t.Errorf("Health.Overall = %q, want %q", h.Overall, health.StatusHealthy)
	}

	// A second Plan against the converged runtime must be a no-op.
	p2, err := tgt.Plan(ctx, desired, nil)
	if err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	if p2.Changed() {
		t.Errorf("second plan should be unchanged, got %s", p2.String())
	}
}

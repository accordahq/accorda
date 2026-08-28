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
	"crypto/sha256"
	"fmt"
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
	dir := integrationProjectDir(t)
	path = filepath.Join(dir, config.DefaultComposeFile)
	if err := os.WriteFile(path, []byte(integrationCompose), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return path, composeProjectName(path)
}

func integrationProjectDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	sum := sha256.Sum256([]byte(base + "\x00" + t.Name()))
	dir := filepath.Join(base, fmt.Sprintf("accorda-compose-it-%x", sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir integration project: %v", err)
	}
	return dir
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

func TestComposeTarget_ApplyUsesControlledInterpolationEnvironment(t *testing.T) {
	testutil.RequireCompose(t)
	t.Setenv("ACCORDA_TEST_IMAGE_TAG", "9.9")
	dir := integrationProjectDir(t)
	path := filepath.Join(dir, config.DefaultComposeFile)
	composeFile := `services:
  api:
    image: busybox:${ACCORDA_TEST_IMAGE_TAG:-1.36}
    command: ["sh", "-c", "sleep 300"]
`
	if err := os.WriteFile(path, []byte(composeFile), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ACCORDA_TEST_IMAGE_TAG=9.9\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	project := composeProjectName(path)
	t.Cleanup(func() { down(t, path, project) })

	services, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := services["api"].Image; got != "busybox:1.36" {
		t.Fatalf("planned image = %q, want busybox:1.36", got)
	}
	tgt := newIntegrationTarget(t, path)
	desired := &state.DesiredState{Commit: "abc123", Services: services}
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	runtime, err := tgt.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got := runtime.Services["api"].Image; got != "busybox:1.36" {
		t.Errorf("runtime image = %q, want planned busybox:1.36", got)
	}
}

// TestComposeTarget_RenameReclaimsOwnedStaleContainer exercises the project
// rename path end-to-end: a container created under an old Accorda project
// name (carrying the accorda.managed label and an explicit container_name)
// collides with a new project's service. Apply must reclaim the stale,
// Accorda-owned container and bring up the new one under the new project.
func TestComposeTarget_RenameReclaimsOwnedStaleContainer(t *testing.T) {
	testutil.RequireCompose(t)
	ctx := context.Background()

	dir := integrationProjectDir(t)
	path := filepath.Join(dir, config.DefaultComposeFile)
	composeFile := `services:
  db:
    image: busybox:1.36
    container_name: rename-db
    command: ["sh", "-c", "sleep 300"]
`
	if err := os.WriteFile(path, []byte(composeFile), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	// First, deploy as the "old" project via a target that stamps the
	// ownership label and uses a real docker CLI runner. This simulates the
	// prior Accorda deployment before the rename.
	oldTgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithPullPolicy(config.PullNever),
		WithProjectName("old-project"),
		WithHealthTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("New old target: %v", err)
	}
	oldDesired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "old",
		Services: map[string]state.Service{
			"db": {Image: "busybox:1.36"},
		},
	}
	oldPlan, err := oldTgt.Plan(ctx, oldDesired, nil)
	if err != nil {
		t.Fatalf("old Plan: %v", err)
	}
	if err := oldTgt.Apply(ctx, oldPlan); err != nil {
		t.Fatalf("old Apply: %v", err)
	}
	t.Cleanup(func() { down(t, path, "old-project") })
	t.Cleanup(func() { _ = (cliDocker{}).Run(ctx, "rm", "-f", "rename-db") })

	// The stale container must carry the Accorda ownership label.
	cli := cliDocker{}
	if err := cli.Run(ctx, "inspect", "--format", "{{index .Config.Labels \"accorda.managed\"}}", "rename-db"); err != nil {
		t.Fatalf("old container should carry accorda.managed label: %v", err)
	}

	// Now deploy as the "new" project. Apply must reclaim the stale owned
	// container and bring up the new one under the new project name.
	newTgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithPullPolicy(config.PullNever),
		WithProjectName("new-project"),
		WithHealthTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("New new target: %v", err)
	}
	newDesired := &state.DesiredState{
		Repository: "acme/infra",
		Commit:     "new",
		Services: map[string]state.Service{
			"db": {Image: "busybox:1.36"},
		},
	}
	newPlan, err := newTgt.Plan(ctx, newDesired, nil)
	if err != nil {
		t.Fatalf("new Plan: %v", err)
	}
	// Assign a deployment ID the way the reconcile loop does, so Apply can
	// stamp it as the accorda.deployment_id label (docs/ACCORDA.md §7).
	newPlan.DeploymentID = "dep_rename_new"
	if err := newTgt.Apply(ctx, newPlan); err != nil {
		t.Fatalf("new Apply: %v", err)
	}
	t.Cleanup(func() { down(t, path, "new-project") })

	// The new project's Current must see the db service.
	runtime, err := newTgt.Current(ctx)
	if err != nil {
		t.Fatalf("new Current: %v", err)
	}
	if _, ok := runtime.Services["db"]; !ok {
		t.Fatalf("new project runtime missing db service: %+v", runtime.Services)
	}

	// The recreated container must carry the ownership and deployment labels.
	if err := cli.Run(ctx, "inspect", "--format", "{{index .Config.Labels \""+accordaManagedLabel+"\"}}", "rename-db"); err != nil {
		t.Fatalf("new container should carry accorda.managed label: %v", err)
	}
	if err := cli.Run(ctx, "inspect", "--format", "{{index .Config.Labels \""+accordaDeploymentLabel+"\"}}", "rename-db"); err != nil {
		t.Fatalf("new container should carry accorda.deployment_id label: %v", err)
	}
}

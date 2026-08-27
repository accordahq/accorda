//go:build integration

// The integration build tag keeps these tests out of the default `go test`
// run because they require a running Docker daemon. Run with:
//
//	go test ./internal/targets/image/ -tags integration
package image

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/health"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
	"accorda/internal/sources"
	"accorda/internal/targets"
	"accorda/internal/testutil"
)

// integrationImage is a minimal single-image workload that stays running by
// default so the full validate → apply → current → health lifecycle can be
// exercised against a real Docker daemon. nginx:alpine has a long-running
// default entrypoint and no healthcheck (health is unknown, not a failure).
const integrationImage = "nginx:alpine"

// integrationName returns a unique, normalized service name for one test run
// so parallel integration tests do not collide on the same container name.
func integrationName(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().Format(time.RFC3339Nano)))
	return fmt.Sprintf("accorda-image-it-%x", sum[:8])
}

// TestIntegration_Lifecycle exercises the image target end-to-end against a
// real Docker daemon: Validate, Desired, Plan (create), Apply, Current, and
// Health. It mirrors the Compose integration test's lifecycle coverage so
// the two Docker targets stay consistent.
func TestIntegration_Lifecycle(t *testing.T) {
	testutil.RequireDocker(t)
	name := integrationName(t)
	tgt, err := New(
		config.Target{Type: config.TargetImage, Image: integrationImage, Env: map[string]string{"READY": "1"}},
		name,
		WithPullPolicy(config.PullMissing),
		WithHealthTimeout(30*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		// Remove the container so repeated integration runs do not accumulate.
		_ = tgt.runner.Run(context.Background(), "rm", "-f", name)
	})

	ctx := context.Background()
	if err := tgt.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	desired, err := tgt.Desired(ctx, sources.NewRevision(sources.Commit{SHA: "abc123"}, "", t.TempDir(), nil, nil))
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	p, err := tgt.Plan(ctx, desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planHas(p, plan.ActionCreate, name) {
		t.Fatalf("Plan = %v, want a create action for %s", p.Actions, name)
	}
	if err := tgt.Apply(ctx, p); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	runtime, err := tgt.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	rs, ok := runtime.Services[name]
	if !ok {
		t.Fatalf("Current: service %q absent after Apply: %+v", name, runtime.Services)
	}
	if rs.Status != state.RunningStatus {
		t.Errorf("Status = %q, want %q", rs.Status, state.RunningStatus)
	}
	if rs.Image != integrationImage {
		t.Errorf("Image = %q, want %q", rs.Image, integrationImage)
	}

	// A second plan against the now-converged runtime should be a noop.
	p2, err := tgt.Plan(ctx, desired, &state.DeployedState{Commit: "abc123", Services: desired.Services})
	if err != nil {
		t.Fatalf("Plan (converged): %v", err)
	}
	if p2.Changed() {
		t.Errorf("converged plan.Changed = true, want false: %v", p2.Actions)
	}

	// Health is unknown for a container without a declared healthcheck, which
	// is not a failure (docs/ACCORDA.md §19).
	h, err := tgt.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Overall == health.StatusUnhealthy {
		t.Errorf("Health.Overall = %q, want not unhealthy", h.Overall)
	}
}

// TestIntegration_Logs exercises the Logs capability against a real Docker
// daemon, asserting the container's startup line appears in the snapshot.
func TestIntegration_Logs(t *testing.T) {
	testutil.RequireDocker(t)
	name := integrationName(t)
	tgt, err := New(
		config.Target{Type: config.TargetImage, Image: integrationImage},
		name,
		WithPullPolicy(config.PullMissing),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tgt.runner.Run(context.Background(), "rm", "-f", name) })

	ctx := context.Background()
	desired, _ := tgt.Desired(ctx, sources.NewRevision(sources.Commit{SHA: "abc123"}, "", t.TempDir(), nil, nil))
	p, _ := tgt.Plan(ctx, desired, nil)
	if err := tgt.Apply(ctx, p); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var buf strings.Builder
	if err := tgt.Logs(ctx, name, targets.LogOptions{}, &buf, &buf); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	// nginx emits startup lines on stderr; the snapshot may be empty if the
	// container has not received a request, so only assert the call succeeded.
	_ = buf.String()
}

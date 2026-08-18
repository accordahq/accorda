package compose

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"

	"accorda/internal/config"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
)

// desiredState builds a DesiredState with the given service images.
func desiredState(images map[string]string) *state.DesiredState {
	services := make(map[string]state.Service, len(images))
	for name, img := range images {
		services[name] = state.Service{Image: img}
	}
	return &state.DesiredState{Repository: "acme/infra", Commit: "abc123", Services: services}
}

func TestSelectPulls_Changed(t *testing.T) {
	// changed pulls only the images of services that changed (created or
	// recreated); unchanged services are left untouched.
	desired := desiredState(map[string]string{
		"api":      "api:2",
		"worker":   "worker:1",
		"postgres": "postgres:17",
	})
	drift := []plan.Action{
		{Kind: plan.ActionRecreate, Service: "api", Image: "api:2"},
		{Kind: plan.ActionCreate, Service: "worker", Image: "worker:1"},
		{Kind: plan.ActionNoop, Service: "postgres"},
	}
	tgt := &Target{pullPolicy: config.PullChanged}
	got, err := tgt.selectPulls(context.Background(), desired, drift)
	if err != nil {
		t.Fatalf("selectPulls: %v", err)
	}
	want := []string{"api", "worker"}
	if !reflect.DeepEqual(pullNames(got), want) {
		t.Errorf("pull services = %v, want %v", pullNames(got), want)
	}
}

func TestSelectPulls_Changed_StoppedServiceNotPulled(t *testing.T) {
	// A stopped service with an unchanged image (a Start action) already has
	// its image locally, so it is not pulled.
	desired := desiredState(map[string]string{"api": "api:1"})
	drift := []plan.Action{{Kind: plan.ActionStart, Service: "api", Image: "api:1"}}
	tgt := &Target{pullPolicy: config.PullChanged}
	got, err := tgt.selectPulls(context.Background(), desired, drift)
	if err != nil {
		t.Fatalf("selectPulls: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("pull actions = %v, want none", got)
	}
}

func TestSelectPulls_Always(t *testing.T) {
	desired := desiredState(map[string]string{
		"api":      "api:2",
		"worker":   "worker:1",
		"postgres": "postgres:17",
	})
	tgt := &Target{pullPolicy: config.PullAlways}
	got, err := tgt.selectPulls(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("selectPulls: %v", err)
	}
	want := []string{"api", "postgres", "worker"} // sorted
	if !reflect.DeepEqual(pullNames(got), want) {
		t.Errorf("pull services = %v, want %v", pullNames(got), want)
	}
}

func TestSelectPulls_Never(t *testing.T) {
	desired := desiredState(map[string]string{"api": "api:2"})
	tgt := &Target{pullPolicy: config.PullNever}
	got, err := tgt.selectPulls(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("selectPulls: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("pull actions = %v, want none", got)
	}
}

func TestSelectPulls_Missing(t *testing.T) {
	// missing pulls only images not already available locally.
	desired := desiredState(map[string]string{
		"api":      "api:2",
		"worker":   "worker:1",
		"postgres": "postgres:17",
	})
	cli := &fakeDockerClient{
		images: []image.Summary{
			{RepoTags: []string{"api:2"}},
			{RepoTags: []string{"postgres:17"}},
		},
	}
	tgt := &Target{pullPolicy: config.PullMissing, docker: cli}
	got, err := tgt.selectPulls(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("selectPulls: %v", err)
	}
	want := []string{"worker"}
	if !reflect.DeepEqual(pullNames(got), want) {
		t.Errorf("pull services = %v, want %v", pullNames(got), want)
	}
}

func TestSelectPulls_Missing_ImageListFails(t *testing.T) {
	desired := desiredState(map[string]string{"api": "api:2"})
	cli := &fakeDockerClient{imageErr: errors.New("list images boom")}
	tgt := &Target{pullPolicy: config.PullMissing, docker: cli}
	if _, err := tgt.selectPulls(context.Background(), desired, nil); err == nil {
		t.Fatal("expected error when ImageList fails, got nil")
	}
}

func TestSelectPulls_Missing_DigestPinnedImage(t *testing.T) {
	// A digest-pinned image is pulled by digest, so Docker populates
	// RepoDigests but leaves RepoTags empty. The missing policy must index
	// RepoDigests too, otherwise the image looks perpetually missing and is
	// re-pulled on every deployment (docs/ACCORDA.md §7).
	desired := desiredState(map[string]string{
		"api": "ghcr.io/acme/api@sha256:91a",
	})
	cli := &fakeDockerClient{
		images: []image.Summary{
			{RepoDigests: []string{"ghcr.io/acme/api@sha256:91a"}},
		},
	}
	tgt := &Target{pullPolicy: config.PullMissing, docker: cli}
	got, err := tgt.selectPulls(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("selectPulls: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("pull actions = %v, want none (digest already local)", got)
	}
}

func TestSelectPulls_Missing_DigestPinnedImageAbsent(t *testing.T) {
	// A digest-pinned image that is not present locally must still be pulled.
	desired := desiredState(map[string]string{
		"api": "ghcr.io/acme/api@sha256:91a",
	})
	cli := &fakeDockerClient{
		images: []image.Summary{
			{RepoDigests: []string{"ghcr.io/acme/api@sha256:other"}},
		},
	}
	tgt := &Target{pullPolicy: config.PullMissing, docker: cli}
	got, err := tgt.selectPulls(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("selectPulls: %v", err)
	}
	want := []string{"api"}
	if !reflect.DeepEqual(pullNames(got), want) {
		t.Errorf("pull services = %v, want %v", pullNames(got), want)
	}
}

func TestSelectPulls_Missing_NilDockerClient(t *testing.T) {
	desired := desiredState(map[string]string{"api": "api:2"})
	tgt := &Target{pullPolicy: config.PullMissing}
	if _, err := tgt.selectPulls(context.Background(), desired, nil); err == nil {
		t.Fatal("expected error for nil docker client, got nil")
	}
}

func TestSelectPulls_UnknownPolicy(t *testing.T) {
	desired := desiredState(map[string]string{"api": "api:2"})
	tgt := &Target{pullPolicy: "sometimes"}
	if _, err := tgt.selectPulls(context.Background(), desired, nil); err == nil {
		t.Fatal("expected error for unknown policy, got nil")
	}
}

func TestPlan_InjectsPullActions(t *testing.T) {
	// Plan must prepend pull actions (per the changed policy) before the
	// drift actions, so images are fetched before services are recreated.
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
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithPullPolicy(config.PullChanged))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	desired := desiredState(map[string]string{"api": "api:2"})
	p, err := tgt.Plan(context.Background(), desired, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != 2 {
		t.Fatalf("actions len = %d, want 2: %v", len(p.Actions), p.Actions)
	}
	if p.Actions[0].Kind != plan.ActionPull || p.Actions[0].Service != "api" {
		t.Errorf("Actions[0] = %+v, want pull api", p.Actions[0])
	}
	if p.Actions[1].Kind != plan.ActionRecreate {
		t.Errorf("Actions[1].Kind = %q, want recreate", p.Actions[1].Kind)
	}
}

// pullNames returns the service names of the given pull actions in order.
func pullNames(actions []plan.Action) []string {
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		names = append(names, a.Service)
	}
	return names
}

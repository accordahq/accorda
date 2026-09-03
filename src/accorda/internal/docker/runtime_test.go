package docker

import (
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/container"

	"accorda/internal/core/state"
)

// inspect builds a container.InspectResponse with the given image, state, and
// health status, mirroring the compose test helper so the runtime-state
// mapping is exercised against realistic engine responses.
func inspect(image, status, health string) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Image: "sha256:" + image,
			State: &container.State{Status: status, Health: &container.Health{Status: health}},
		},
		Config: &container.Config{Image: image},
	}
}

func TestRuntimeService_StatusAndHealth(t *testing.T) {
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
			if got := RuntimeService(c.inspect); !reflect.DeepEqual(got, c.want) {
				t.Errorf("RuntimeService = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestImageReference_PrefersConfigImage(t *testing.T) {
	got := ImageReference(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{Image: "sha256:91a"},
		Config:            &container.Config{Image: "busybox:1.36"},
	})
	if got != "busybox:1.36" {
		t.Errorf("ImageReference = %q, want %q", got, "busybox:1.36")
	}
}

func TestImageReferenceFallsBackToImageID(t *testing.T) {
	got := ImageReference(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{Image: "sha256:91a"},
	})
	if got != "sha256:91a" {
		t.Errorf("ImageReference = %q, want %q", got, "sha256:91a")
	}
}

func TestImageReferenceEmpty(t *testing.T) {
	if got := ImageReference(container.InspectResponse{}); got != "" {
		t.Errorf("ImageReference = %q, want empty", got)
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
			if got := MergeRuntime(c.a, c.b); !reflect.DeepEqual(got, want) {
				t.Errorf("MergeRuntime = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMergeRuntime_AgreeIsShared(t *testing.T) {
	a := state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"}
	b := state.RuntimeService{Status: "running", Health: "healthy", Image: "api:1"}
	if got := MergeRuntime(a, b); !reflect.DeepEqual(got, a) {
		t.Errorf("MergeRuntime = %+v, want %+v", got, a)
	}
}

func TestRuntimeService_ExitCode(t *testing.T) {
	// A one-shot job that exited surfaces its exit code so Accorda can
	// distinguish a completed (exit 0) job from a failed (non-zero) one
	// (docs/ACCORDA.md §5.3).
	got := RuntimeService(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Image: "sha256:migrator",
			State: &container.State{Status: "exited", ExitCode: 0},
		},
		Config: &container.Config{Image: "migrator:1"},
	})
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.ExitCode)
	}
	if got.Status != "exited" {
		t.Errorf("Status = %q, want %q", got.Status, "exited")
	}
}

func TestMergeRuntime_ExitCodeDisagreementIsDegraded(t *testing.T) {
	// Replicas that exited with different codes disagree and must surface as
	// degraded rather than silently letting one win.
	a := state.RuntimeService{Status: "exited", Health: "", Image: "migrator:1", ExitCode: 0}
	b := state.RuntimeService{Status: "exited", Health: "", Image: "migrator:1", ExitCode: 1}
	want := state.RuntimeService{Status: degradedStatus, Health: "", Image: "migrator:1"}
	if got := MergeRuntime(a, b); !reflect.DeepEqual(got, want) {
		t.Errorf("MergeRuntime = %+v, want %+v", got, want)
	}
}

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

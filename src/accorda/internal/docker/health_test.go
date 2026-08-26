package docker

import (
	"testing"
	"time"

	"accorda/internal/core/health"
	"accorda/internal/core/state"
)

func TestHealthStatus_Mapping(t *testing.T) {
	cases := []struct {
		in   string
		want health.Status
	}{
		{"healthy", health.StatusHealthy},
		{"starting", health.StatusStarting},
		{"", health.StatusUnknown},
		{"none", health.StatusUnhealthy},
		{"unhealthy", health.StatusUnhealthy},
	}
	for _, c := range cases {
		if got := HealthStatus(c.in); got != c.want {
			t.Errorf("HealthStatus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHealthFromRuntime_NilRuntime(t *testing.T) {
	h := HealthFromRuntime(nil, time.Unix(0, 0))
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
	h := HealthFromRuntime(runtime, time.Unix(0, 0))
	if h.Services["api"].Status != health.StatusHealthy {
		t.Errorf("api = %q, want %q", h.Services["api"].Status, health.StatusHealthy)
	}
	if h.Services["worker"].Status != health.StatusUnknown {
		t.Errorf("worker = %q, want %q", h.Services["worker"].Status, health.StatusUnknown)
	}
}

package health

import (
	"testing"
	"time"
)

func TestNew_Defaults(t *testing.T) {
	h := New(time.Unix(1700000000, 0))
	if h.Overall != StatusUnknown {
		t.Errorf("Overall = %q, want %q", h.Overall, StatusUnknown)
	}
	if h.Services == nil {
		t.Error("Services map is nil, want initialized")
	}
}

func TestSummarize_AllHealthy(t *testing.T) {
	h := New(time.Unix(0, 0))
	h.SetService("api", StatusHealthy, "")
	h.SetService("postgres", StatusHealthy, "")
	h.Summarize()
	if h.Overall != StatusHealthy {
		t.Errorf("Overall = %q, want %q", h.Overall, StatusHealthy)
	}
	if !h.Healthy {
		t.Error("Healthy = false, want true")
	}
}

func TestSummarize_OneUnhealthy(t *testing.T) {
	h := New(time.Unix(0, 0))
	h.SetService("api", StatusHealthy, "")
	h.SetService("worker", StatusUnhealthy, "exit code 1")
	h.Summarize()
	if h.Overall != StatusUnhealthy {
		t.Errorf("Overall = %q, want %q", h.Overall, StatusUnhealthy)
	}
	if h.Healthy {
		t.Error("Healthy = true, want false")
	}
}

func TestSummarize_Starting(t *testing.T) {
	h := New(time.Unix(0, 0))
	h.SetService("api", StatusHealthy, "")
	h.SetService("worker", StatusStarting, "")
	h.Summarize()
	if h.Overall != StatusStarting {
		t.Errorf("Overall = %q, want %q", h.Overall, StatusStarting)
	}
	if h.Healthy {
		t.Error("Healthy = true, want false")
	}
}

func TestSummarize_EmptyIsUnknown(t *testing.T) {
	h := New(time.Unix(0, 0))
	h.Summarize()
	if h.Overall != StatusUnknown {
		t.Errorf("Overall = %q, want %q", h.Overall, StatusUnknown)
	}
	if h.Healthy {
		t.Error("Healthy = true, want false")
	}
}

func TestHealth_Clone_IsDeepCopy(t *testing.T) {
	h := New(time.Unix(0, 0))
	h.SetService("api", StatusHealthy, "")
	h.Deployed = true
	h.Healthy = true

	clone := h.Clone()
	clone.SetService("api", StatusUnhealthy, "mutated")
	clone.SetService("worker", StatusUnhealthy, "")

	if h.Services["api"].Status != StatusHealthy {
		t.Errorf("original mutated by clone: got %q, want %q", h.Services["api"].Status, StatusHealthy)
	}
	if _, ok := h.Services["worker"]; ok {
		t.Errorf("original gained service from clone: %v", h.Services)
	}
}

func TestHealth_String(t *testing.T) {
	h := New(time.Unix(0, 0))
	h.SetService("api", StatusHealthy, "")
	h.Deployed = true
	h.Healthy = true
	s := h.String()
	if s == "" {
		t.Error("String() returned empty string")
	}
}

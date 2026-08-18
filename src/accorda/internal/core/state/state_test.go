package state

import (
	"testing"
	"time"
)

func TestDesiredState_Clone_IsDeepCopy(t *testing.T) {
	original := DesiredState{
		Repository: "acme/infra",
		Branch:     "production",
		Commit:     "a84fd21",
		CommitTime: time.Unix(1700000000, 0),
		Services: map[string]Service{
			"api": {
				Image:    "ghcr.io/acme/api:2.4.1",
				Command:  []string{"./api", "--port", "8080"},
				Env:      map[string]string{"LOG_LEVEL": "info"},
				Ports:    []Port{{Host: "8080", Container: "8080", Protocol: "tcp"}},
				Volumes:  []Volume{{Type: "bind", Source: "/etc/api", Target: "/etc/api", ReadOnly: true}},
				Networks: []string{"frontend"},
				Labels:   map[string]string{"app": "api"},
				Healthcheck: Healthcheck{
					Test:     []string{"CMD", "curl"},
					Interval: 5 * time.Second,
					Retries:  10,
				},
				DependsOn: []string{"postgres"},
			},
		},
	}

	clone := original.Clone()

	// Map index expressions are not addressable, so reassign the whole value.
	api := clone.Services["api"]
	api.Image = "ghcr.io/acme/api:9.9.9"
	api.Env["LOG_LEVEL"] = "debug"
	api.Command[0] = "mutated"
	api.Ports[0].Host = "9999"
	api.Volumes[0].Target = "/mutated"
	api.Networks[0] = "mutated"
	api.Labels["app"] = "mutated"
	api.Healthcheck.Test[0] = "mutated"
	api.Healthcheck.Retries = 99
	api.DependsOn[0] = "mutated"
	clone.Services["api"] = api
	clone.Services["worker"] = Service{Image: "ghcr.io/acme/worker:1.0"}

	if got := original.Services["api"].Image; got != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("original image mutated by clone: got %q, want %q", got, "ghcr.io/acme/api:2.4.1")
	}
	if got := original.Services["api"].Env["LOG_LEVEL"]; got != "info" {
		t.Errorf("original env mutated by clone: got %q, want %q", got, "info")
	}
	if got := original.Services["api"].Command[0]; got != "./api" {
		t.Errorf("original command mutated by clone: got %q, want ./api", got)
	}
	if got := original.Services["api"].Ports[0].Host; got != "8080" {
		t.Errorf("original port mutated by clone: got %q, want 8080", got)
	}
	if got := original.Services["api"].Volumes[0].Target; got != "/etc/api" {
		t.Errorf("original volume mutated by clone: got %q, want /etc/api", got)
	}
	if got := original.Services["api"].Networks[0]; got != "frontend" {
		t.Errorf("original network mutated by clone: got %q, want frontend", got)
	}
	if got := original.Services["api"].Labels["app"]; got != "api" {
		t.Errorf("original label mutated by clone: got %q, want api", got)
	}
	if got := original.Services["api"].Healthcheck.Test[0]; got != "CMD" {
		t.Errorf("original healthcheck mutated by clone: got %q, want CMD", got)
	}
	if got := original.Services["api"].Healthcheck.Retries; got != 10 {
		t.Errorf("original healthcheck retries mutated by clone: got %d, want 10", got)
	}
	if got := original.Services["api"].DependsOn[0]; got != "postgres" {
		t.Errorf("original depends_on mutated by clone: got %q, want postgres", got)
	}
	if _, ok := original.Services["worker"]; ok {
		t.Errorf("original gained service from clone: %v", original.Services)
	}
}

func TestDesiredState_Clone_PreservesNilMaps(t *testing.T) {
	var zero DesiredState
	clone := zero.Clone()
	if clone.Services != nil {
		t.Errorf("Clone of zero-value Services = %v, want nil", clone.Services)
	}
}

func TestDeployedState_Clone_IsDeepCopy(t *testing.T) {
	original := DeployedState{
		DeploymentID: "dep_01Kabc",
		Commit:       "a84fd21",
		DeployedAt:   time.Unix(1700000001, 0),
		Services: map[string]Service{
			"api": {Image: "ghcr.io/acme/api:2.4.1"},
		},
	}
	clone := original.Clone()
	// Map index expressions are not addressable, so reassign the whole value.
	api := clone.Services["api"]
	api.Image = "mutated"
	clone.Services["api"] = api
	if got := original.Services["api"].Image; got != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("original image mutated by clone: got %q, want %q", got, "ghcr.io/acme/api:2.4.1")
	}
}

func TestRuntimeState_Clone_IsDeepCopy(t *testing.T) {
	original := RuntimeState{
		Services: map[string]RuntimeService{
			"api": {Status: "running", Health: "healthy", Image: "api:2.4.1"},
		},
	}
	clone := original.Clone()
	// Map index expressions are not addressable, so reassign the whole value.
	api := clone.Services["api"]
	api.Status = "stopped"
	clone.Services["api"] = api
	clone.Services["worker"] = RuntimeService{Status: "running"}
	if got := original.Services["api"].Status; got != "running" {
		t.Errorf("original status mutated by clone: got %q, want %q", got, "running")
	}
	if _, ok := original.Services["worker"]; ok {
		t.Errorf("original gained service from clone: %v", original.Services)
	}
}

func TestDesiredState_Validate(t *testing.T) {
	cases := []struct {
		name    string
		state   DesiredState
		wantErr bool
	}{
		{
			name:    "empty commit",
			state:   DesiredState{Services: map[string]Service{"api": {Image: "api:1"}}},
			wantErr: true,
		},
		{
			name: "service without image",
			state: DesiredState{
				Commit:   "abc123",
				Services: map[string]Service{"api": {}},
			},
			wantErr: true,
		},
		{
			name: "valid",
			state: DesiredState{
				Commit:   "abc123",
				Services: map[string]Service{"api": {Image: "api:1"}},
			},
		},
		{
			name:    "empty services valid",
			state:   DesiredState{Commit: "abc123"},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.state.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate: got err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestDeployedState_Validate(t *testing.T) {
	if err := (DeployedState{}).Validate(); err == nil {
		t.Fatal("empty DeployedState: expected error, got nil")
	}
	if err := (DeployedState{DeploymentID: "dep_1"}).Validate(); err == nil {
		t.Fatal("missing commit: expected error, got nil")
	}
	if err := (DeployedState{Commit: "abc"}).Validate(); err == nil {
		t.Fatal("missing deployment id: expected error, got nil")
	}
	if err := (DeployedState{DeploymentID: "dep_1", Commit: "abc"}).Validate(); err != nil {
		t.Fatalf("valid DeployedState: unexpected error: %v", err)
	}
}

func TestRuntimeState_Validate(t *testing.T) {
	if err := (RuntimeState{}).Validate(); err != nil {
		t.Fatalf("empty RuntimeState: unexpected error: %v", err)
	}
	bad := RuntimeState{Services: map[string]RuntimeService{"api": {}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("service without status: expected error, got nil")
	}
	good := RuntimeState{Services: map[string]RuntimeService{"api": {Status: "running"}}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid RuntimeState: unexpected error: %v", err)
	}
}

package state

import (
	"testing"
	"time"
)

func TestService_Hash_Deterministic(t *testing.T) {
	svc := Service{
		Image:    "ghcr.io/acme/api:2.4.1",
		Command:  []string{"./api", "--port", "8080"},
		Env:      map[string]string{"LOG_LEVEL": "info", "REGION": "us-east-1"},
		Ports:    []Port{{Host: "8080", Container: "8080", Protocol: "tcp"}},
		Volumes:  []Volume{{Type: "bind", Source: "/etc/api", Target: "/etc/api", ReadOnly: true}},
		Networks: []string{"frontend", "backend"},
		Labels:   map[string]string{"app": "api", "tier": "web"},
		Healthcheck: Healthcheck{
			Test:     []string{"CMD", "curl", "-f", "http://localhost:8080/health"},
			Interval: 5 * time.Second,
			Timeout:  3 * time.Second,
			Retries:  10,
		},
		DependsOn: []string{"postgres", "redis"},
	}

	first := svc.Hash()
	for i := 0; i < 100; i++ {
		if got := svc.Hash(); got != first {
			t.Fatalf("Hash() not deterministic: got %q, want %q", got, first)
		}
	}
}

func TestService_Hash_ReorderingEquivalent(t *testing.T) {
	// Two services that differ only in the ordering of unordered collections
	// must hash identically (docs/ACCORDA.md §10).
	a := Service{
		Image:     "api:1",
		Command:   []string{"./api", "--port", "8080"},
		Env:       map[string]string{"A": "1", "B": "2"},
		Ports:     []Port{{Host: "8080", Container: "8080", Protocol: "tcp"}, {Host: "9090", Container: "9090", Protocol: "tcp"}},
		Volumes:   []Volume{{Type: "bind", Source: "/a", Target: "/a"}, {Type: "bind", Source: "/b", Target: "/b"}},
		Networks:  []string{"frontend", "backend"},
		Labels:    map[string]string{"x": "1", "y": "2"},
		DependsOn: []string{"postgres", "redis"},
	}
	b := Service{
		Image:     "api:1",
		Command:   []string{"./api", "--port", "8080"},
		Env:       map[string]string{"B": "2", "A": "1"},
		Ports:     []Port{{Host: "9090", Container: "9090", Protocol: "tcp"}, {Host: "8080", Container: "8080", Protocol: "tcp"}},
		Volumes:   []Volume{{Type: "bind", Source: "/b", Target: "/b"}, {Type: "bind", Source: "/a", Target: "/a"}},
		Networks:  []string{"backend", "frontend"},
		Labels:    map[string]string{"y": "2", "x": "1"},
		DependsOn: []string{"redis", "postgres"},
	}

	if a.Hash() != b.Hash() {
		t.Fatalf("reordering-equivalent services hash differently:\n  a=%q\n  b=%q", a.Hash(), b.Hash())
	}
}

func TestService_Hash_CommandOrderIsSignificant(t *testing.T) {
	// Command is an ordered exec form; reordering its elements changes the
	// meaning and must change the hash.
	a := Service{Image: "api:1", Command: []string{"./api", "--port", "8080"}}
	b := Service{Image: "api:1", Command: []string{"--port", "8080", "./api"}}
	if a.Hash() == b.Hash() {
		t.Fatal("command reordering must change the hash")
	}
}

func TestService_Hash_FieldChangeChangesHash(t *testing.T) {
	base := Service{
		Image:    "api:1",
		Command:  []string{"./api"},
		Env:      map[string]string{"A": "1"},
		EnvFiles: []ExternalFile{{Path: "defaults.env", Required: true, Digest: "sha256:a"}},
		Ports:    []Port{{Host: "8080", Container: "8080", Protocol: "tcp"}},
		Volumes:  []Volume{{Type: "bind", Source: "/a", Target: "/a"}},
		Networks: []string{"frontend"},
		Labels:   map[string]string{"x": "1"},
		LabelFiles: []ExternalFile{{
			Path: "labels.env", Required: true, Digest: "sha256:b",
		}},
		Healthcheck: Healthcheck{
			Test:     []string{"CMD", "curl"},
			Interval: 5 * time.Second,
			Retries:  3,
		},
		DependsOn: []string{"postgres"},
	}

	cases := []struct {
		name   string
		mutate func(Service) Service
	}{
		{"image", func(s Service) Service { s.Image = "api:2"; return s }},
		{"command", func(s Service) Service { s.Command = []string{"./api", "--verbose"}; return s }},
		{"env value", func(s Service) Service { s.Env["A"] = "2"; return s }},
		{"env key", func(s Service) Service { s.Env["B"] = "1"; return s }},
		{"env_file path", func(s Service) Service { s.EnvFiles[0].Path = "other.env"; return s }},
		{"env_file digest", func(s Service) Service { s.EnvFiles[0].Digest = "sha256:c"; return s }},
		{"port", func(s Service) Service { s.Ports[0].Host = "9090"; return s }},
		{"volume", func(s Service) Service { s.Volumes[0].Target = "/b"; return s }},
		{"network", func(s Service) Service { s.Networks = []string{"backend"}; return s }},
		{"label", func(s Service) Service { s.Labels["x"] = "2"; return s }},
		{"label_file", func(s Service) Service { s.LabelFiles[0].Path = "other.labels"; return s }},
		{"healthcheck test", func(s Service) Service { s.Healthcheck.Test = []string{"CMD", "wget"}; return s }},
		{"healthcheck retries", func(s Service) Service { s.Healthcheck.Retries = 5; return s }},
		{"depends_on", func(s Service) Service { s.DependsOn = []string{"redis"}; return s }},
		{"one_shot", func(s Service) Service { s.OneShot = true; return s }},
	}

	baseHash := base.Hash()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.mutate(base.Clone()).Hash(); got == baseHash {
				t.Fatalf("mutating %s did not change the hash", tc.name)
			}
		})
	}
}

func TestService_Hash_ZeroValueIsStable(t *testing.T) {
	var zero Service
	if zero.Hash() == "" {
		t.Fatal("zero-value service hash must not be empty")
	}
	if zero.Hash() != (Service{}).Hash() {
		t.Fatal("zero-value service hash must be stable")
	}
}

func TestCompare_ConfigHashDiffers_IsOutOfSync(t *testing.T) {
	// Image and env match, but a non-image field (command) changed. The
	// canonical hash comparison must flag the service as out of sync so it is
	// recreated (docs/ACCORDA.md §10).
	dSvcs := map[string]Service{"api": {Image: "api:1", Command: []string{"./api", "--port", "8080"}}}
	pSvcs := map[string]Service{"api": {Image: "api:1", Command: []string{"./api", "--port", "9090"}}}
	cmp := Compare(
		desired("a84fd21", dSvcs),
		deployed("dep_1", "a84fd21", pSvcs),
		runtime(map[string]RuntimeService{"api": rsvc("api:1", "running")}),
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
	if got := cmp.Services["api"].Result; got != ResultOutOfSync {
		t.Fatalf("api: %s, want %s", got, ResultOutOfSync)
	}
}

func TestCompare_ConfigHashEqual_IsSynced(t *testing.T) {
	// Same image, env, and full config (reordering-equivalent): synced.
	dSvcs := map[string]Service{"api": {
		Image:    "api:1",
		Env:      map[string]string{"A": "1", "B": "2"},
		Networks: []string{"frontend", "backend"},
	}}
	pSvcs := map[string]Service{"api": {
		Image:    "api:1",
		Env:      map[string]string{"B": "2", "A": "1"},
		Networks: []string{"backend", "frontend"},
	}}
	cmp := Compare(
		desired("a84fd21", dSvcs),
		deployed("dep_1", "a84fd21", pSvcs),
		runtime(map[string]RuntimeService{"api": rsvc("api:1", "running")}),
	)
	if got := cmp.Services["api"].Result; got != ResultSynced {
		t.Fatalf("api: %s, want %s", got, ResultSynced)
	}
}

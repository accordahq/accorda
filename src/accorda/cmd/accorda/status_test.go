package main

import (
	"bytes"
	"testing"
	"time"

	"accorda/internal/core/health"
	"accorda/internal/core/state"
	"accorda/internal/targets/compose"
)

func TestShortSHA(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"d71b2e4", "d71b2e4"},
		{"d71b2e4abcd0123456789", "d71b2e4"},
		{"a", "a"},
	}
	for _, c := range cases {
		if got := shortSHA(c.in); got != c.want {
			t.Errorf("shortSHA(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSyncLabel(t *testing.T) {
	cases := []struct {
		name     string
		gitHead  string
		deployed string
		want     string
	}{
		{"synced", "abc1234", "abc1234", "SYNCED"},
		{"out-of-sync no deploy", "abc1234", "", "OUT_OF_SYNC"},
		{"out-of-sync different", "abc1234", "def5678", "OUT_OF_SYNC"},
		{"head unavailable", "unavailable", "", "UNKNOWN"},
		{"head unavailable deployed", "unavailable", "abc1234", "UNKNOWN"},
	}
	for _, c := range cases {
		if got := syncLabel(c.gitHead, c.deployed); got != c.want {
			t.Errorf("syncLabel(%q,%q) = %q, want %q", c.gitHead, c.deployed, got, c.want)
		}
	}
}

func TestHealthLabel(t *testing.T) {
	cases := []struct {
		name string
		hc   *health.Health
		want string
	}{
		{"nil", nil, "UNKNOWN"},
		{"empty", &health.Health{}, "UNKNOWN"},
		{"healthy", compose.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
			"api": {Health: "healthy", Status: "running"},
		}}, time.Unix(0, 0)), "HEALTHY"},
		{"unknown health", compose.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
			"api": {Health: "", Status: "running"},
		}}, time.Unix(0, 0)), "HEALTHY"},
		{"unhealthy", compose.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
			"api": {Health: "unhealthy", Status: "running"},
		}}, time.Unix(0, 0)), "UNHEALTHY"},
		{"mixed unhealthy", compose.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
			"api":    {Health: "healthy", Status: "running"},
			"worker": {Health: "unhealthy", Status: "exited"},
		}}, time.Unix(0, 0)), "UNHEALTHY"},
	}
	for _, c := range cases {
		if got := healthLabel(c.hc); got != c.want {
			t.Errorf("healthLabel(%v) = %q, want %q", c.hc, got, c.want)
		}
	}
}

func TestBuildRows_UnionAndOrdering(t *testing.T) {
	desired := &state.DesiredState{Services: map[string]state.Service{
		"api":    {Image: "ghcr.io/acme/api:2.4.1"},
		"worker": {Image: "ghcr.io/acme/worker:2.4.1"},
	}}
	runtime := &state.RuntimeState{Services: map[string]state.RuntimeService{
		"api": {Image: "ghcr.io/acme/api:2.4.1", Status: "running", Health: "healthy"},
		"db":  {Image: "postgres:17", Status: "running", Health: "healthy"},
	}}

	rows := buildRows(desired, runtime, compose.HealthFromRuntime(runtime, time.Unix(0, 0)))
	// Deterministic ordering by name regardless of map iteration order
	// (docs/DECISIONS.md #12): api, db, worker.
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.name)
	}
	want := []string{"api", "db", "worker"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
}

func TestBuildRows_Rows(t *testing.T) {
	desired := &state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "ghcr.io/acme/api:2.4.1"},
	}}
	runtime := &state.RuntimeState{Services: map[string]state.RuntimeService{
		"api": {Image: "ghcr.io/acme/api:2.4.1", Status: "running", Health: "healthy"},
	}}
	rows := buildRows(desired, runtime, compose.HealthFromRuntime(runtime, time.Unix(0, 0)))
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one", rows)
	}
	r := rows[0]
	if r.name != "api" || r.state != "running" || r.health != "healthy" || r.image != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("row = %+v, want running/healthy/api:2.4.1", r)
	}
}

func TestBuildRows_AbsentRuntimeFallsBackToDesired(t *testing.T) {
	// A desired service with no running container reports state "absent",
	// health "-", and the declared image.
	desired := &state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "ghcr.io/acme/api:2.4.1"},
	}}
	rows := buildRows(desired, &state.RuntimeState{}, &health.Health{})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want one", len(rows))
	}
	r := rows[0]
	if r.state != "absent" {
		t.Errorf("state = %q, want absent", r.state)
	}
	if r.health != "-" {
		t.Errorf("health = %q, want -", r.health)
	}
	if r.image != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("image = %q, want declared image", r.image)
	}
}

func TestWriteStatus_ContainsExpectedColumns(t *testing.T) {
	info := statusInfo{
		Environment: "production",
		Repository:  "acme/backend",
		Branch:      "main",
		GitHead:     "d71b2e4",
		Deployed:    "d71b2e4",
		Sync:        "SYNCED",
		Runtime:     "HEALTHY",
		LastDeploy:  "2026-08-15 18:42:07",
		services: []statusService{
			{name: "api", state: "running", health: "healthy", image: "api:2.4.1"},
		},
	}
	var buf bytes.Buffer
	writeStatus(&buf, info)
	out := buf.String()
	for _, want := range []string{
		"Environment   production",
		"Repository    acme/backend",
		"Branch        main",
		"Git HEAD      d71b2e4",
		"Deployed      d71b2e4",
		"Sync          SYNCED",
		"Runtime       HEALTHY",
		"Last deploy   2026-08-15 18:42:07",
		"SERVICE      STATE       HEALTH      IMAGE",
		"api          running     healthy     api:2.4.1",
	} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

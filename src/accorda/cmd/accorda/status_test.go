package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/health"
	"accorda/internal/core/state"
	shareddocker "accorda/internal/docker"
	"accorda/internal/sources"
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
		{"healthy", shareddocker.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
			"api": {Health: "healthy", Status: "running"},
		}}, time.Unix(0, 0)), "HEALTHY"},
		{"unknown health", shareddocker.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
			"api": {Health: "", Status: "running"},
		}}, time.Unix(0, 0)), "HEALTHY"},
		{"unhealthy", shareddocker.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
			"api": {Health: "unhealthy", Status: "running"},
		}}, time.Unix(0, 0)), "UNHEALTHY"},
		{"mixed unhealthy", shareddocker.HealthFromRuntime(&state.RuntimeState{Services: map[string]state.RuntimeService{
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

	rows := buildRows(desired, runtime, shareddocker.HealthFromRuntime(runtime, time.Unix(0, 0)))
	// Deterministic ordering by name regardless of map iteration order
	// (docs/DECISIONS.md #7): api, db, worker.
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
	rows := buildRows(desired, runtime, shareddocker.HealthFromRuntime(runtime, time.Unix(0, 0)))
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
		Checkout:    "/cache/accorda/checkout",
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
		"Checkout      /cache/accorda/checkout",
		"SERVICE      STATE       HEALTH      IMAGE",
		"api          running     healthy     api:2.4.1",
	} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

// statusTestSource is a controllable Source used to exercise the redaction
// and unreachable-runtime paths without a live Git repository.
type statusTestSource struct {
	fetchErr error
	commit   sources.Commit
	desired  *state.DesiredState
}

func (s *statusTestSource) Validate(context.Context) error { return nil }
func (s *statusTestSource) Fetch(context.Context) (sources.Commit, error) {
	if s.fetchErr != nil {
		return sources.Commit{}, s.fetchErr
	}
	return s.commit, nil
}
func (s *statusTestSource) Desired(_ context.Context, _ *sources.Commit) (*state.DesiredState, error) {
	return s.desired, nil
}

// TestCollectStatus_RedactsURLWhenDesiredFails verifies that a configured URL
// carrying credentials is redacted even when the desired state cannot be read
// (review finding 1): the repository line must never echo the embedded token
// (docs/ACCORDA.md §18, §56).
func TestCollectStatus_RedactsURLWhenDesiredFails(t *testing.T) {
	src := &statusTestSource{
		commit: sources.Commit{SHA: "abc1234abcd", Branch: "main"},
		// DesiredState is nil, simulating a Desired failure that leaves the
		// repository field unset, so the fallback to the configured URL is
		// what prints.
		desired: nil,
	}
	proj := &config.Project{
		Environment: "production",
		Source: config.Source{
			URL:    "https://oauth2:secret-token@git.internal/acme/repo.git",
			Branch: "main",
		},
	}
	info := collectStatus(context.Background(), proj, src, nil, nil)
	if info.Repository != "https://git.internal/acme/repo.git" {
		t.Errorf("Repository = %q, want the redacted URL without the token", info.Repository)
	}
	if bytes.Contains([]byte(info.Repository), []byte("secret-token")) {
		t.Errorf("Repository %q leaks the credential token", info.Repository)
	}
}

// TestCollectStatus_SyncLabelPopulatedWhenRuntimeUnreachable verifies the sync
// line is still computed when the runtime cannot be read (review finding 2),
// keeping the partial report self-consistent.
func TestCollectStatus_SyncLabelPopulatedWhenRuntimeUnreachable(t *testing.T) {
	src := &statusTestSource{
		commit: sources.Commit{SHA: "abc1234abcd", Branch: "main"},
	}
	proj := &config.Project{
		Environment: "production",
		Source:      config.Source{URL: "https://git.internal/acme/repo.git", Branch: "main"},
	}
	// A nil target is the simplest "runtime unavailable" case; collectStatus
	// reports Runtime unknown and must still fill Sync.
	info := collectStatus(context.Background(), proj, src, nil, nil)
	if info.Sync != "OUT_OF_SYNC" {
		t.Errorf("Sync = %q, want OUT_OF_SYNC (no deployed commit yet)", info.Sync)
	}
	if info.Runtime != "unknown" {
		t.Errorf("Runtime = %q, want unknown", info.Runtime)
	}
}

// checkoutDirStub is a statusTestSource that also exposes a checkout path,
// exercising the checkoutDirer capability path in collectStatus.
type checkoutDirStub struct {
	statusTestSource
	checkoutDir string
}

func (c *checkoutDirStub) CheckoutDir() (string, error) { return c.checkoutDir, nil }

// TestCollectStatus_PopulatesCheckoutFromSource verifies the Checkout field
// is filled when the source implements checkoutDirer.
func TestCollectStatus_PopulatesCheckoutFromSource(t *testing.T) {
	src := &checkoutDirStub{
		statusTestSource: statusTestSource{
			commit: sources.Commit{SHA: "abc1234abcd", Branch: "main"},
		},
		checkoutDir: "/cache/accorda/managed-checkout",
	}
	proj := &config.Project{
		Environment: "production",
		Source:      config.Source{URL: "https://git.internal/acme/repo.git", Branch: "main"},
	}
	info := collectStatus(context.Background(), proj, src, nil, nil)
	if info.Checkout != "/cache/accorda/managed-checkout" {
		t.Errorf("Checkout = %q, want /cache/accorda/managed-checkout", info.Checkout)
	}
}

// TestCollectStatus_CheckoutEmptyForStubSource verifies the Checkout field
// stays empty when the source does not implement checkoutDirer.
func TestCollectStatus_CheckoutEmptyForStubSource(t *testing.T) {
	src := &statusTestSource{
		commit: sources.Commit{SHA: "abc1234abcd", Branch: "main"},
	}
	proj := &config.Project{
		Environment: "production",
		Source:      config.Source{URL: "https://git.internal/acme/repo.git", Branch: "main"},
	}
	info := collectStatus(context.Background(), proj, src, nil, nil)
	if info.Checkout != "" {
		t.Errorf("Checkout = %q, want empty for non-checkoutDirer source", info.Checkout)
	}
}

//go:build integration

// The integration build tag keeps these tests out of the default `go test`
// run because they require the system `git` executable and write to temp
// directories. Run with:
//
//	go test ./internal/sources/git/ -tags integration
package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"accorda/internal/config"
	"accorda/internal/sources"
	"accorda/internal/testutil"
)

func TestGitSource_CloneFetchCheckoutAndHead(t *testing.T) {
	testutil.RequireGit(t)
	url, wantSHA, wantBranch, wantTime := testutil.MakeOriginRepo(t)

	cache := t.TempDir()
	src := config.Source{Type: "git", URL: url, Branch: wantBranch}
	g := New(src, WithCacheDir(cache), WithBaseDir(t.TempDir()))

	ctx := context.Background()
	if err := g.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	commit, err := g.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if commit.SHA != wantSHA {
		t.Errorf("Fetch SHA = %q, want %q", commit.SHA, wantSHA)
	}
	if commit.Branch != wantBranch {
		t.Errorf("Fetch Branch = %q, want %q", commit.Branch, wantBranch)
	}
	if !commit.Time.Equal(wantTime) {
		t.Errorf("Fetch Time = %v, want %v", commit.Time, wantTime)
	}

	// A subsequent Fetch must fast-forward via fetch (no re-clone): the
	// cache directory should still exist and HEAD must remain correct.
	if _, err := g.Fetch(ctx); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		t.Errorf("cache not a git repo after second Fetch: %v", err)
	}

	// Desired returns the services declared at HEAD.
	ds, err := g.Desired(ctx, nil)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if ds.Commit != wantSHA {
		t.Errorf("Desired Commit = %q, want %q", ds.Commit, wantSHA)
	}
	if ds.Branch != wantBranch {
		t.Errorf("Desired Branch = %q, want %q", ds.Branch, wantBranch)
	}
	if ds.Services["api"].Image != "ghcr.io/acme/api:1.9" {
		t.Errorf("Desired api.Image = %q, want ghcr.io/acme/api:1.9", ds.Services["api"].Image)
	}
	if ds.Services["api"].Env["LOG_LEVEL"] != "warning" {
		t.Errorf("Desired api.Env[LOG_LEVEL] = %q, want warning", ds.Services["api"].Env["LOG_LEVEL"])
	}
	if ds.Services["redis"].Image != "redis:8" {
		t.Errorf("Desired redis.Image = %q, want redis:8", ds.Services["redis"].Image)
	}
	if err := ds.Validate(); err != nil {
		t.Errorf("Desired.Validate: %v", err)
	}
}

func TestGitSource_DesiredAtExplicitRef(t *testing.T) {
	testutil.RequireGit(t)
	url, wantSHA, wantBranch, wantTime := testutil.MakeOriginRepo(t)

	src := config.Source{Type: "git", URL: url, Branch: wantBranch}
	g := New(src, WithCacheDir(t.TempDir()), WithBaseDir(t.TempDir()))

	ctx := context.Background()
	if _, err := g.Fetch(ctx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Passing the same commit explicitly must yield the same desired state.
	ref := &sources.Commit{SHA: wantSHA, Branch: wantBranch, Time: wantTime}
	ds, err := g.Desired(ctx, ref)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if ds.Commit != wantSHA {
		t.Errorf("Desired Commit = %q, want %q", ds.Commit, wantSHA)
	}
}

func TestGitSource_DesiredAtOlderCommit(t *testing.T) {
	// Regression guard for PR #46 review [HIGH]: Desired(ref) must read the
	// services file at the passed commit, not at the checked-out HEAD. The
	// repository has two commits with different services; after Fetch
	// (HEAD = v2), requesting the older commit (v1) must return v1 services.
	testutil.RequireGit(t)
	url, wantBranch, old, head := testutil.MakeOriginRepoWithHistory(t)

	src := config.Source{Type: "git", URL: url, Branch: wantBranch}
	g := New(src, WithCacheDir(t.TempDir()), WithBaseDir(t.TempDir()))

	ctx := context.Background()
	fetched, err := g.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetched.SHA != head.SHA {
		t.Fatalf("Fetch SHA = %q, want HEAD %q", fetched.SHA, head.SHA)
	}

	// Request the older commit explicitly.
	ref := &sources.Commit{SHA: old.SHA, Branch: old.Branch, Time: old.Time}
	ds, err := g.Desired(ctx, ref)
	if err != nil {
		t.Fatalf("Desired at older commit: %v", err)
	}
	if ds.Commit != old.SHA {
		t.Errorf("Desired Commit = %q, want older %q", ds.Commit, old.SHA)
	}
	// The services must come from v1, not the checked-out v2 HEAD.
	if got, want := ds.Services["api"].Image, "ghcr.io/acme/api:1.8"; got != want {
		t.Errorf("Desired api.Image at older commit = %q, want %q", got, want)
	}
	if got, want := ds.Services["api"].Env["LOG_LEVEL"], "info"; got != want {
		t.Errorf("Desired api.Env[LOG_LEVEL] at older commit = %q, want %q", got, want)
	}
	if got, want := ds.Services["redis"].Image, "redis:7"; got != want {
		t.Errorf("Desired redis.Image at older commit = %q, want %q", got, want)
	}

	// Sanity: requesting HEAD via nil ref must still return v2 services.
	dsHead, err := g.Desired(ctx, nil)
	if err != nil {
		t.Fatalf("Desired at HEAD: %v", err)
	}
	if got, want := dsHead.Services["api"].Image, "ghcr.io/acme/api:1.9"; got != want {
		t.Errorf("Desired api.Image at HEAD = %q, want %q", got, want)
	}
	if got, want := dsHead.Services["redis"].Image, "redis:8"; got != want {
		t.Errorf("Desired redis.Image at HEAD = %q, want %q", got, want)
	}
}

func TestGitSource_ComposeContextAndExternalFileDigests(t *testing.T) {
	testutil.RequireGit(t)
	origin := t.TempDir()
	runGitCommand(t, origin, "init", "--initial-branch=main")
	runGitCommand(t, origin, "config", "user.email", "accorda@example.test")
	runGitCommand(t, origin, "config", "user.name", "Accorda Test")
	deploy := filepath.Join(origin, "deploy")
	if err := os.MkdirAll(filepath.Join(deploy, "data"), 0o755); err != nil {
		t.Fatalf("mkdir deploy: %v", err)
	}
	writeIntegrationFile(t, filepath.Join(deploy, "base.yaml"), `services:
  base:
    image: api:1
    volumes:
      - ./data:/data
`)
	writeIntegrationFile(t, filepath.Join(deploy, "compose.yaml"), `services:
  api:
    extends:
      file: ./base.yaml
      service: base
    env_file:
      - service.env
    label_file:
      - labels.env
`)
	writeIntegrationFile(t, filepath.Join(deploy, "service.env"), "MODE=one\n")
	writeIntegrationFile(t, filepath.Join(deploy, "labels.env"), "tier=one\n")
	runGitCommand(t, origin, "add", ".")
	runGitCommand(t, origin, "commit", "-m", "first")
	oldSHA := runGitCommand(t, origin, "rev-parse", "HEAD")
	writeIntegrationFile(t, filepath.Join(deploy, "service.env"), "MODE=two\n")
	runGitCommand(t, origin, "add", "deploy/service.env")
	runGitCommand(t, origin, "commit", "-m", "second")

	cache := filepath.Join(t.TempDir(), "checkout")
	g := New(config.Source{
		Type: "git", URL: "file://" + origin, Branch: "main", Path: "deploy/compose.yaml",
	}, WithCacheDir(cache))
	ctx := context.Background()
	head, err := g.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	headDesired, err := g.Desired(ctx, &head)
	if err != nil {
		t.Fatalf("Desired HEAD: %v", err)
	}
	oldDesired, err := g.Desired(ctx, &sources.Commit{SHA: oldSHA, Branch: "main"})
	if err != nil {
		t.Fatalf("Desired old: %v", err)
	}
	if got := oldDesired.Services["api"].Image; got != "api:1" {
		t.Errorf("extended image = %q, want api:1", got)
	}
	if got := oldDesired.Services["api"].Volumes[0].Source; got != "data" {
		t.Errorf("relative bind source = %q, want data", got)
	}
	if headDesired.Services["api"].Hash() == oldDesired.Services["api"].Hash() {
		t.Fatal("tracked env_file content change did not change service hash")
	}
	assertIntegrationFile(t, filepath.Join(cache, "deploy", "service.env"), "MODE=two\n")
	if err := g.Materialize(ctx, &sources.Commit{SHA: oldSHA}); err != nil {
		t.Fatalf("Materialize old: %v", err)
	}
	assertIntegrationFile(t, filepath.Join(cache, "deploy", "service.env"), "MODE=one\n")
}

func runGitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeIntegrationFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertIntegrationFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}

func TestGitSource_ValidateErrors(t *testing.T) {
	testutil.RequireGit(t)
	ctx := context.Background()

	if err := New(config.Source{}).Validate(ctx); err == nil {
		t.Fatal("expected error for empty config")
	}
	if err := New(config.Source{URL: "x", Branch: ""}).Validate(ctx); err == nil {
		t.Fatal("expected error for missing branch")
	}
}

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
	"time"

	"accorda/internal/config"
	"accorda/internal/sources"
)

// requireGit skips the test if git is not on PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
}

// makeOriginRepo creates a fresh Git repository with one commit and a file at
// the given path, then returns the URL (file://...) and the expected commit
// SHA/branch/time.
func makeOriginRepo(t *testing.T) (url, sha, branch string, committedAt time.Time) {
	t.Helper()
	gitConfig := []string{"-c", "user.name=Accorda Test", "-c", "user.email=test@accorda.dev"}
	run := func(dir string, args ...string) string {
		args = append(gitConfig, args...)
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	origin := t.TempDir()
	run(origin, "init", "--initial-branch=production")
	compose := `
services:
  api:
    image: ghcr.io/acme/api:1.9
    environment:
      LOG_LEVEL: warning
  redis:
    image: redis:8
`
	if err := os.WriteFile(filepath.Join(origin, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatalf("write compose.yaml: %v", err)
	}
	run(origin, "add", "compose.yaml")
	run(origin, "commit", "-m", "initial")
	sha = run(origin, "rev-parse", "HEAD")
	committedAtStr := run(origin, "log", "-1", "--format=%aI")
	when, err := time.Parse(time.RFC3339, committedAtStr)
	if err != nil {
		t.Fatalf("parse commit time %q: %v", committedAtStr, err)
	}
	committedAt = when.UTC()
	branch = "production"
	url = "file://" + origin
	return url, sha, branch, committedAt
}

func TestGitSource_CloneFetchCheckoutAndHead(t *testing.T) {
	requireGit(t)
	url, wantSHA, wantBranch, wantTime := makeOriginRepo(t)

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
	requireGit(t)
	url, wantSHA, wantBranch, wantTime := makeOriginRepo(t)

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

func TestGitSource_ValidateErrors(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	if err := New(config.Source{}).Validate(ctx); err == nil {
		t.Fatal("expected error for empty config")
	}
	if err := New(config.Source{URL: "x", Branch: ""}).Validate(ctx); err == nil {
		t.Fatal("expected error for missing branch")
	}
}

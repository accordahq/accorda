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
	if err := os.WriteFile(filepath.Join(origin, defaultComposeFile), []byte(compose), 0o644); err != nil {
		t.Fatalf("write %s: %v", defaultComposeFile, err)
	}
	run(origin, "add", defaultComposeFile)
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

// makeOriginRepoWithHistory creates a repository with two commits whose
// services file differs, then returns the URL plus both commits' metadata:
// the older (old) and the current HEAD (head). It is used to verify that
// Desired(ref) reads content at the passed commit, not the checked-out HEAD.
func makeOriginRepoWithHistory(t *testing.T) (url, branch string, old, head commitInfo) {
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
	info := func(dir, ref string) commitInfo {
		sha := run(dir, "rev-parse", ref)
		raw := run(dir, "log", "-1", "--format=%aI", ref)
		when, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatalf("parse commit time %q: %v", raw, err)
		}
		return commitInfo{SHA: sha, Branch: "production", Time: when.UTC()}
	}

	origin := t.TempDir()
	run(origin, "init", "--initial-branch=production")

	// Commit 1: the older desired state.
	compose1 := `
services:
  api:
    image: ghcr.io/acme/api:1.8
    environment:
      LOG_LEVEL: info
  redis:
    image: redis:7
`
	if err := os.WriteFile(filepath.Join(origin, defaultComposeFile), []byte(compose1), 0o644); err != nil {
		t.Fatalf("write %s: %v", defaultComposeFile, err)
	}
	run(origin, "add", defaultComposeFile)
	run(origin, "commit", "-m", "v1")
	old = info(origin, "HEAD")

	// Commit 2: the new HEAD, with different services.
	compose2 := `
services:
  api:
    image: ghcr.io/acme/api:1.9
    environment:
      LOG_LEVEL: warning
  redis:
    image: redis:8
`
	if err := os.WriteFile(filepath.Join(origin, defaultComposeFile), []byte(compose2), 0o644); err != nil {
		t.Fatalf("write %s: %v", defaultComposeFile, err)
	}
	run(origin, "add", defaultComposeFile)
	run(origin, "commit", "-m", "v2")
	head = info(origin, "HEAD")

	branch = "production"
	url = "file://" + origin
	return url, branch, old, head
}

// commitInfo mirrors sources.Commit for test fixtures.
type commitInfo struct {
	SHA    string
	Branch string
	Time   time.Time
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

func TestGitSource_DesiredAtOlderCommit(t *testing.T) {
	// Regression guard for PR #46 review [HIGH]: Desired(ref) must read the
	// services file at the passed commit, not at the checked-out HEAD. The
	// repository has two commits with different services; after Fetch
	// (HEAD = v2), requesting the older commit (v1) must return v1 services.
	requireGit(t)
	url, wantBranch, old, head := makeOriginRepoWithHistory(t)

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

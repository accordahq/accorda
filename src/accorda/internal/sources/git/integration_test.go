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

	revision, contents := revisionContents(t, g, nil, testutil.ComposeFile)
	if revision.Commit.SHA != wantSHA || revision.Commit.Branch != wantBranch {
		t.Errorf("Revision commit = %+v, want %s on %s", revision.Commit, wantSHA, wantBranch)
	}
	for _, want := range []string{"ghcr.io/acme/api:1.9", "LOG_LEVEL: warning", "redis:8"} {
		if !strings.Contains(contents, want) {
			t.Errorf("revision artifact missing %q: %s", want, contents)
		}
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
	revision, _ := revisionContents(t, g, ref, testutil.ComposeFile)
	if revision.Commit.SHA != wantSHA {
		t.Errorf("Revision Commit = %q, want %q", revision.Commit.SHA, wantSHA)
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
	revision, oldContents := revisionContents(t, g, ref, testutil.ComposeFile)
	if revision.Commit.SHA != old.SHA {
		t.Errorf("Revision Commit = %q, want older %q", revision.Commit.SHA, old.SHA)
	}
	for _, want := range []string{"ghcr.io/acme/api:1.8", "LOG_LEVEL: info", "redis:7"} {
		if !strings.Contains(oldContents, want) {
			t.Errorf("older revision missing %q: %s", want, oldContents)
		}
	}

	_, headContents := revisionContents(t, g, nil, testutil.ComposeFile)
	for _, want := range []string{"ghcr.io/acme/api:1.9", "redis:8"} {
		if !strings.Contains(headContents, want) {
			t.Errorf("HEAD revision missing %q: %s", want, headContents)
		}
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
	headRevision, err := g.Revision(ctx, &head)
	if err != nil {
		t.Fatalf("Revision HEAD: %v", err)
	}
	defer func() { _ = headRevision.Close() }()
	oldRevision, err := g.Revision(ctx, &sources.Commit{SHA: oldSHA, Branch: "main"})
	if err != nil {
		t.Fatalf("Revision old: %v", err)
	}
	defer func() { _ = oldRevision.Close() }()
	headDigest, headOK, err := headRevision.Digest("deploy/service.env")
	if err != nil || !headOK {
		t.Fatalf("HEAD tracked digest = %q, %t, %v", headDigest, headOK, err)
	}
	oldDigest, oldOK, err := oldRevision.Digest("deploy/service.env")
	if err != nil || !oldOK {
		t.Fatalf("old tracked digest = %q, %t, %v", oldDigest, oldOK, err)
	}
	if headDigest == oldDigest {
		t.Fatal("tracked env_file content change did not change revision digest")
	}
	assertRevisionFile(t, oldRevision, "deploy/base.yaml", "services:\n  base:\n    image: api:1\n    volumes:\n      - ./data:/data\n")
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

// TestGitSource_InPlace binds the git source directly to a user-owned worktree
// (source.path, no URL) and verifies Fetch/Revision read the current HEAD in
// place without cloning. It mirrors TestGitSource_CloneFetchCheckoutAndHead
// but against a worktree that already exists on disk (issue #95).
func TestGitSource_InPlace(t *testing.T) {
	testutil.RequireGit(t)
	url, wantSHA, wantBranch, wantTime := testutil.MakeOriginRepo(t)

	// Clone the origin into a local worktree we own, then bind in place to it.
	worktree := testutil.MakeLocalWorktree(t, url, wantBranch)

	// In-place mode: path names the worktree, no URL. The compose file resolves
	// to the repo-relative default inside it. buildSource would set Source.Path
	// to the repo-relative compose path and carry the worktree root via
	// WithCacheDir; mirror that here.
	src := config.Source{Type: "git", Path: testutil.ComposeFile}
	g := New(src, WithCacheDir(worktree))

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

	revision, contents := revisionContents(t, g, nil, testutil.ComposeFile)
	if revision.Commit.SHA != wantSHA {
		t.Errorf("Revision Commit = %q, want %q", revision.Commit.SHA, wantSHA)
	}
	if !strings.Contains(contents, "ghcr.io/acme/api:1.9") {
		t.Errorf("revision artifact does not contain HEAD image: %s", contents)
	}
	if revision.Repository != worktree {
		t.Errorf("Revision Repository = %q, want worktree %q", revision.Repository, worktree)
	}

	// Materialize is unsupported in in-place mode (would rewrite the worktree).
	if err := g.Materialize(ctx, &sources.Commit{SHA: wantSHA}); err == nil {
		t.Fatal("Materialize in-place should fail, got nil")
	}

	// CheckoutPath resolves against the bound worktree root.
	got, err := g.CheckoutPath(testutil.ComposeFile)
	if err != nil {
		t.Fatalf("CheckoutPath: %v", err)
	}
	if want := filepath.Join(worktree, testutil.ComposeFile); got != want {
		t.Errorf("CheckoutPath = %q, want %q", got, want)
	}
}

// TestGitSource_InPlaceDesiredAtOlderCommit verifies that an explicit older
// SHA is read from the commit's tree without mutating the user-owned worktree
// (issue #95). The repository has two commits with different services; after
// binding at HEAD=v2, requesting the older commit must return v1 services and
// the working tree must remain at v2.
func TestGitSource_InPlaceDesiredAtOlderCommit(t *testing.T) {
	testutil.RequireGit(t)
	url, wantBranch, old, head := testutil.MakeOriginRepoWithHistory(t)

	worktree := testutil.MakeLocalWorktree(t, url, wantBranch)

	src := config.Source{Type: "git", Path: testutil.ComposeFile}
	g := New(src, WithCacheDir(worktree))

	ctx := context.Background()
	if _, err := g.Fetch(ctx); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	ref := &sources.Commit{SHA: old.SHA, Branch: old.Branch, Time: old.Time}
	revision, contents := revisionContents(t, g, ref, testutil.ComposeFile)
	if revision.Commit.SHA != old.SHA {
		t.Errorf("Revision Commit = %q, want older %q", revision.Commit.SHA, old.SHA)
	}
	if !strings.Contains(contents, "ghcr.io/acme/api:1.8") {
		t.Errorf("older revision missing old image: %s", contents)
	}

	// The working tree must still reflect HEAD (v2): the older read must not
	// have checked out the historical revision.
	data, err := os.ReadFile(filepath.Join(worktree, testutil.ComposeFile))
	if err != nil {
		t.Fatalf("read worktree compose: %v", err)
	}
	if strings.Contains(string(data), "ghcr.io/acme/api:1.8") {
		t.Fatalf("worktree was mutated to the older revision: %s", data)
	}
	if !strings.Contains(string(data), head.SHA[:7]) && !strings.Contains(string(data), "ghcr.io/acme/api:1.9") {
		t.Fatalf("worktree does not reflect HEAD v2: %s", data)
	}
}

func TestGitSource_InPlaceHistoricalComposeReferences(t *testing.T) {
	testutil.RequireGit(t)
	origin := t.TempDir()
	runGitCommand(t, origin, "init", "--initial-branch=main")
	runGitCommand(t, origin, "config", "user.email", "accorda@example.test")
	runGitCommand(t, origin, "config", "user.name", "Accorda Test")
	deploy := filepath.Join(origin, "deploy")
	if err := os.MkdirAll(deploy, 0o755); err != nil {
		t.Fatalf("mkdir deploy: %v", err)
	}
	writeIntegrationFile(t, filepath.Join(deploy, "base.yaml"), `services:
  base:
    image: api:1
`)
	writeIntegrationFile(t, filepath.Join(deploy, "included.yaml"), `services:
  worker:
    image: worker:1
`)
	writeIntegrationFile(t, filepath.Join(deploy, "compose.yaml"), `include:
  - included.yaml
services:
  api:
    extends:
      file: base.yaml
      service: base
`)
	runGitCommand(t, origin, "add", ".")
	runGitCommand(t, origin, "commit", "-m", "first")
	oldSHA := runGitCommand(t, origin, "rev-parse", "HEAD")
	writeIntegrationFile(t, filepath.Join(deploy, "base.yaml"), `services:
  base:
    image: api:2
`)
	writeIntegrationFile(t, filepath.Join(deploy, "included.yaml"), `services:
  worker:
    image: worker:2
`)
	runGitCommand(t, origin, "add", ".")
	runGitCommand(t, origin, "commit", "-m", "second")

	worktree := testutil.MakeLocalWorktree(t, "file://"+origin, "main")
	g := New(config.Source{Type: "git", Path: "deploy/compose.yaml"}, WithCacheDir(worktree))
	revision, err := g.Revision(context.Background(), &sources.Commit{SHA: oldSHA, Branch: "main"})
	if err != nil {
		t.Fatalf("Revision at older commit: %v", err)
	}
	defer func() { _ = revision.Close() }()
	assertRevisionFile(t, revision, "deploy/base.yaml", "services:\n  base:\n    image: api:1\n")
	assertRevisionFile(t, revision, "deploy/included.yaml", "services:\n  worker:\n    image: worker:1\n")
	assertIntegrationFile(t, filepath.Join(worktree, "deploy", "base.yaml"), "services:\n  base:\n    image: api:2\n")
	assertIntegrationFile(t, filepath.Join(worktree, "deploy", "included.yaml"), "services:\n  worker:\n    image: worker:2\n")
}

func revisionContents(t *testing.T, g *Git, ref *sources.Commit, repositoryPath string) (*sources.Revision, string) {
	t.Helper()
	revision, err := g.Revision(context.Background(), ref)
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	path, err := revision.Path(repositoryPath)
	if err != nil {
		t.Fatalf("revision path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read revision artifact: %v", err)
	}
	if err := revision.Close(); err != nil {
		t.Fatalf("close revision: %v", err)
	}
	return revision, string(data)
}

func assertRevisionFile(t *testing.T, revision *sources.Revision, repositoryPath, want string) {
	t.Helper()
	path, err := revision.Path(repositoryPath)
	if err != nil {
		t.Fatalf("revision path: %v", err)
	}
	assertIntegrationFile(t, path, want)
}

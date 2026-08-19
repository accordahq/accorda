package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ComposeFile is the services file name the fixtures write into the origin
// repository. It matches the git source's default services file
// (internal/sources/git defaultComposeFile).
const ComposeFile = "compose.yaml"

// CommitInfo mirrors sources.Commit for test fixtures.
type CommitInfo struct {
	SHA    string
	Branch string
	Time   time.Time
}

// git runs a git command in dir and returns its trimmed stdout, failing the
// test on error. It sets a fixed author identity so commits are reproducible.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=Accorda Test", "-c", "user.email=test@accorda.dev"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes content to path, failing the test on error. It centralizes
// the write-error message so the literal is not duplicated across fixtures.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// MakeOriginRepo creates a fresh Git repository with one commit declaring a
// two-service Compose file, then returns the file:// URL, the commit SHA, the
// branch name, and the commit's authored time.
func MakeOriginRepo(t *testing.T) (url, sha, branch string, committedAt time.Time) {
	t.Helper()
	origin := t.TempDir()
	git(t, origin, "init", "--initial-branch=production")
	compose := `services:
  api:
    image: ghcr.io/acme/api:1.9
    environment:
      LOG_LEVEL: warning
  redis:
    image: redis:8
`
	writeFile(t, filepath.Join(origin, ComposeFile), compose)
	git(t, origin, "add", ComposeFile)
	git(t, origin, "commit", "-m", "initial")
	sha = git(t, origin, "rev-parse", "HEAD")
	return "file://" + origin, sha, "production", commitTime(t, origin, "HEAD")
}

// MakeOriginRepoWithHistory creates a repository with two commits whose
// services file differs, returning the URL, branch, and both commits'
// metadata (old and head).
func MakeOriginRepoWithHistory(t *testing.T) (url, branch string, old, head CommitInfo) {
	t.Helper()
	origin := t.TempDir()
	git(t, origin, "init", "--initial-branch=production")

	compose1 := `services:
  api:
    image: ghcr.io/acme/api:1.8
    environment:
      LOG_LEVEL: info
  redis:
    image: redis:7
`
	writeFile(t, filepath.Join(origin, ComposeFile), compose1)
	git(t, origin, "add", ComposeFile)
	git(t, origin, "commit", "-m", "v1")
	old = commitInfo(t, origin, "HEAD")

	compose2 := `services:
  api:
    image: ghcr.io/acme/api:1.9
    environment:
      LOG_LEVEL: warning
  redis:
    image: redis:8
`
	writeFile(t, filepath.Join(origin, ComposeFile), compose2)
	git(t, origin, "add", ComposeFile)
	git(t, origin, "commit", "-m", "v2")
	head = commitInfo(t, origin, "HEAD")

	return "file://" + origin, "production", old, head
}

// commitInfo reads the SHA, branch, and authored time of ref.
func commitInfo(t *testing.T, dir, ref string) CommitInfo {
	t.Helper()
	return CommitInfo{SHA: git(t, dir, "rev-parse", ref), Branch: "production", Time: commitTime(t, dir, ref)}
}

// commitTime parses the authored time of ref as RFC3339 UTC.
func commitTime(t *testing.T, dir, ref string) time.Time {
	t.Helper()
	raw := git(t, dir, "log", "-1", "--format=%aI", ref)
	when, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse commit time %q: %v", raw, err)
	}
	return when.UTC()
}

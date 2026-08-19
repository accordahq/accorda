//go:build integration

// The integration build tag keeps these tests out of the default `go test`
// run because they require a running Docker daemon, the `docker compose` CLI,
// and the system `git` executable. Run with:
//
//	go test ./cmd/accorda/ -tags integration
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"accorda/internal/testutil"
)

// e2eCompose is the Compose file committed to the origin repository and
// written to the target directory. It declares a single service with a
// healthcheck so the full Git commit → detect → plan → deploy → health
// verification → receipt lifecycle converges to SYNCED against a real Docker
// daemon (docs/ACCORDA.md §55).
const e2eCompose = `services:
  api:
    image: busybox:1.36
    command: ["sh", "-c", "sleep 300"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 3
`

// writeE2EProject creates a Git origin repository declaring e2eCompose, a
// target directory holding the same Compose file, and an accorda.yaml that
// wires the git source to the origin and the compose target to the local
// file. It returns the project directory to run `accorda sync` against.
func writeE2EProject(t *testing.T) string {
	t.Helper()

	// Origin repository: the desired state in Git.
	origin := t.TempDir()
	runGit(t, origin, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(e2eCompose), 0o644); err != nil {
		t.Fatalf("write origin compose: %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "initial")

	// Project directory: the target Compose file and accorda.yaml. The
	// directory basename is fixed to "accorda" so the Compose project name
	// (derived from the file's directory basename) is deterministic and the
	// teardown can target it.
	dir := filepath.Join(t.TempDir(), "accorda")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	composePath := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(e2eCompose), 0o644); err != nil {
		t.Fatalf("write target compose: %v", err)
	}
	// target.file is resolved against the process working directory (not the
	// config's --dir), so the test must use an absolute path for the target
	// Compose file; a relative "compose.yaml" would point at the cwd and fail.
	project := `version: 1
environment: production
source:
  type: git
  url: file://` + origin + `
  branch: main
target:
  type: compose
  file: ` + composePath + `
images:
  pull: never
health:
  timeout: 30s
`
	if err := os.WriteFile(filepath.Join(dir, "accorda.yaml"), []byte(project), 0o644); err != nil {
		t.Fatalf("write accorda.yaml: %v", err)
	}
	return dir
}

// runGit runs a git command in dir with a fixed author identity, failing the
// test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=Accorda Test", "-c", "user.email=test@accorda.dev"}, args...)
	return strings.TrimSpace(execCommand(t, dir, "git", full...))
}

// execCommand runs name with args in dir and returns its combined output,
// failing the test on error.
func execCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestE2E_Sync_ConvergesToSynced drives the full reconciliation lifecycle
// end-to-end: a Git commit declares the desired state, `accorda sync` detects
// it, plans, deploys to a real Docker daemon, verifies health, and reports
// SYNCED (docs/ACCORDA.md §55 End-to-End).
func TestE2E_Sync_ConvergesToSynced(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	t.Cleanup(func() {
		// Best-effort teardown of the compose project.
		cmd := exec.Command("docker", "compose", "-f", "compose.yaml", "-p", "accorda", "down", "--remove-orphans")
		cmd.Dir = dir
		_ = cmd.Run()
	})

	var out bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !strings.Contains(out.String(), "SYNCED") {
		t.Errorf("sync output = %q, want it to contain SYNCED", out.String())
	}
}

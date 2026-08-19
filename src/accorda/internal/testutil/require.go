package testutil

import (
	"os/exec"
	"testing"
)

// RequireGit skips the test when the system `git` executable is not on PATH.
func RequireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
}

// RequireDocker skips the test when the `docker` CLI is unavailable or the
// Docker daemon is not reachable. It checks the daemon via `docker info`,
// which exercises the same engine the compose target's Docker SDK client
// talks to.
func RequireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not available: %v", err)
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
}

// RequireCompose skips the test when the `docker compose` subcommand is
// unavailable. The compose target's Apply shells out to `docker compose`, so
// the CLI must be present for apply-phase integration tests.
func RequireCompose(t *testing.T) {
	t.Helper()
	RequireDocker(t)
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skipf("docker compose not available: %v", err)
	}
}

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
	composePath := filepath.Join(dir, testutil.ComposeFile)
	if err := os.WriteFile(composePath, []byte(e2eCompose), 0o644); err != nil {
		t.Fatalf("write target compose: %v", err)
	}
	// target.file is resolved against the process working directory (not the
	// config's --dir), so the test must use an absolute path for the target
	// Compose file; a relative default filename would point at the cwd and fail.
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

// cleanupComposeProject registers a best-effort teardown for the fixed E2E
// Compose project. Keeping the command here ensures every test uses the shared
// Compose filename and the same cleanup behavior.
func cleanupComposeProject(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", testutil.ComposeFile, "-p", "accorda", "down", "--remove-orphans")
		cmd.Dir = dir
		_ = cmd.Run()
	})
}

// TestE2E_Sync_ConvergesToSynced drives the full reconciliation lifecycle
// end-to-end: a Git commit declares the desired state, `accorda sync` detects
// it, plans, deploys to a real Docker daemon, verifies health, and reports
// SYNCED (docs/ACCORDA.md §55 End-to-End).
func TestE2E_Sync_ConvergesToSynced(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	cleanupComposeProject(t, dir)

	var out bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !strings.Contains(out.String(), "SYNCED") {
		t.Errorf("sync output = %q, want it to contain SYNCED", out.String())
	}
}

// badImageCompose declares a service with a busybox tag that does not exist,
// so `docker compose up -d` fails to create the container (the e2e project
// uses images.pull=never, so no image fetch masks the failure).
const badImageCompose = `services:
  api:
    image: busybox:9.9
    command: ["sh", "-c", "sleep 300"]
`

// TestE2E_Sync_RollsBackOnFailedDeploy drives the rollback path end-to-end
// (docs/ACCORDA.md §20): a healthy busybox:1.36 deployment (commit A) is
// recorded as a receipt, then Git advances to a commit B declaring a
// nonexistent busybox:9.9; the deploy fails and `accorda sync` rolls back to
// the last known-healthy commit A, restoring the running service and printing
// an informative rollback message.
func TestE2E_Sync_RollsBackOnFailedDeploy(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	origin := gitOriginDir(t, dir)
	cleanupComposeProject(t, dir)

	// 1. First sync deploys busybox:1.36 and records a healthy receipt.
	var first bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &first, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if !strings.Contains(first.String(), "SYNCED") {
		t.Fatalf("first sync output = %q, want SYNCED", first.String())
	}

	// 2. Advance Git to a commit declaring a nonexistent image, and overwrite
	// the target Compose file to match, so the forward deploy path attempts
	// busybox:9.9.
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write origin compose (bad image): %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "bump to bad image")
	if err := os.WriteFile(filepath.Join(dir, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write target compose (bad image): %v", err)
	}

	// 3. Second sync must fail and roll back to the previous healthy commit.
	var second bytes.Buffer
	err := run([]string{"sync", "--dir", dir}, &second, nil)
	if err == nil {
		t.Fatalf("second sync succeeded, want a failure + rollback: %q", second.String())
	}
	if !strings.Contains(second.String(), "rollback: restored to commit") {
		t.Errorf("second sync output = %q, want an informative rollback message", second.String())
	}

	// 4. The on-disk Compose file must be restored to busybox:1.36.
	data, readErr := os.ReadFile(filepath.Join(dir, testutil.ComposeFile))
	if readErr != nil {
		t.Fatalf("read restored compose: %v", readErr)
	}
	if strings.Contains(string(data), "busybox:9.9") {
		t.Errorf("compose file after rollback = %q, want it restored away from busybox:9.9", data)
	}
	if !strings.Contains(string(data), "busybox:1.36") {
		t.Errorf("compose file after rollback = %q, want it to contain busybox:1.36", data)
	}
	// The full previous service model is restored from the source, not just
	// the image: the command and healthcheck from the original e2eCompose
	// must be present (docs/ACCORDA.md §20).
	if !strings.Contains(string(data), "sleep 300") {
		t.Errorf("compose file after rollback = %q, want the previous command restored", data)
	}
	if !strings.Contains(string(data), "healthcheck") {
		t.Errorf("compose file after rollback = %q, want the previous healthcheck restored", data)
	}
}

// TestE2E_Sync_FailureNoHistory_NoRollback verifies the unsafe-to-rollback
// case (docs/ACCORDA.md §20 "where safely possible"): a first sync against a
// nonexistent image with no prior healthy deployment in history must fail
// without a rollback, leaving the on-disk Compose file unchanged.
func TestE2E_Sync_FailureNoHistory_NoRollback(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	cleanupComposeProject(t, dir)

	// Overwrite Git and the target file to the nonexistent image before any
	// successful sync, so there is no healthy receipt to roll back to.
	origin := gitOriginDir(t, dir)
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write origin compose (bad image): %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "bump to bad image")
	if err := os.WriteFile(filepath.Join(dir, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write target compose (bad image): %v", err)
	}

	var out bytes.Buffer
	err := run([]string{"sync", "--dir", dir}, &out, nil)
	if err == nil {
		t.Fatalf("sync succeeded, want a failure with no rollback: %q", out.String())
	}
	if strings.Contains(out.String(), "rollback") {
		t.Errorf("sync output = %q, want no rollback message (empty history)", out.String())
	}
	// The on-disk file must be left as the failed image (no rollback wrote it
	// back).
	data, readErr := os.ReadFile(filepath.Join(dir, testutil.ComposeFile))
	if readErr != nil {
		t.Fatalf("read compose: %v", readErr)
	}
	if !strings.Contains(string(data), "busybox:9.9") {
		t.Errorf("compose file = %q, want it unchanged (busybox:9.9)", data)
	}
}

// gitOriginDir returns the path of the file:// origin repository declared in
// the project's accorda.yaml, used to add commits for the rollback scenario.
func gitOriginDir(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "accorda.yaml"))
	if err != nil {
		t.Fatalf("read accorda.yaml: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "url: file://") {
			return strings.TrimPrefix(line, "url: file://")
		}
	}
	t.Fatalf("no file:// url found in accorda.yaml:\n%s", data)
	return ""
}

// TestE2E_Status_ReportsAfterSync drives `accorda status` after a successful
// sync and verifies it prints the environment, repository, branch, Git HEAD,
// deployed commit, sync status, runtime status, and the per-service table
// (docs/ACCORDA.md §11). It runs status only after a sync has recorded a
// healthy receipt so the deployed commit and last-deploy line are populated.
func TestE2E_Status_ReportsAfterSync(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	cleanupComposeProject(t, dir)

	// First converge so a healthy receipt exists.
	var syncOut bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &syncOut, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	var out bytes.Buffer
	if err := run([]string{"status", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"Environment   production",
		"Repository",
		"Branch",
		"Git HEAD",
		"Deployed",
		"Sync          ",
		"Runtime",
		"SERVICE      STATE       HEALTH      IMAGE",
		"api",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("status output missing %q; got:\n%s", want, s)
		}
	}
}

// TestE2E_Diff_AfterSync drives `accorda diff` after a successful sync and
// verifies it reports no differences when the deployed and desired states
// agree, and that it fetches the current remote tip rather than a stale local
// cache: after the remote advances, `diff` must show the change even though
// the local cache still points at the deployed commit (docs/ACCORDA.md §11).
func TestE2E_Diff_AfterSync(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	origin := gitOriginDir(t, dir)
	cleanupComposeProject(t, dir)

	// First converge so a healthy receipt exists.
	var syncOut bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &syncOut, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// A converged deployment must produce an empty diff (no differing
	// services or fields).
	var converged bytes.Buffer
	if err := run([]string{"diff", "--dir", dir}, &converged, nil); err != nil {
		t.Fatalf("diff (converged): %v", err)
	}
	if s := converged.String(); strings.TrimSpace(s) != "" {
		t.Errorf("diff output = %q, want empty for a converged deployment", s)
	}

	// Advance the remote to a new image without touching the local cache, so
	// the old buggy path (Desired with a nil ref) would read the stale cache
	// and report no change. `diff` must fetch and show the new image.
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(changedImageCompose), 0o644); err != nil {
		t.Fatalf("write origin compose (changed image): %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "bump image")

	var out bytes.Buffer
	if err := run([]string{"diff", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("diff (after remote advance): %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "image") || !strings.Contains(s, "busybox:1.37") {
		t.Errorf("diff output = %q, want it to show the new image busybox:1.37", s)
	}
}

// changedImageCompose declares the api service with a bumped image tag, used
// to advance the origin remote after a sync so `accorda diff` must fetch the
// new tip rather than read a stale cache.
const changedImageCompose = `services:
  api:
    image: busybox:1.37
    command: ["sh", "-c", "sleep 300"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 3
`

// TestE2E_Plan_AfterSync drives `accorda plan` after a successful sync and
// verifies it prints the plan header and a per-service UNCHANGED summary for
// the converged deployment, without applying anything (docs/ACCORDA.md §11).
// The deployed baseline is the full service model re-read from the source at
// the deployed commit, so a converged service reports UNCHANGED rather than
// being over-reported as CHANGED.
func TestE2E_Plan_AfterSync(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	cleanupComposeProject(t, dir)

	// First converge so a healthy receipt exists.
	var syncOut bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &syncOut, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	var out bytes.Buffer
	if err := run([]string{"plan", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("plan: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"Deployment plan\n",
		"api",
		"UNCHANGED",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plan output missing %q; got:\n%s", want, s)
		}
	}
}

// TestE2E_History_ReportsAfterSync drives `accorda history` after a successful
// sync and verifies it prints the §11 deployment table with the header and at
// least one healthy row for the converged cycle (docs/ACCORDA.md §11).
func TestE2E_History_ReportsAfterSync(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	cleanupComposeProject(t, dir)

	// First converge so a healthy receipt is recorded.
	var syncOut bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &syncOut, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	var out bytes.Buffer
	if err := run([]string{"history", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("history: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"TIME                 COMMIT     RESULT         CHANGES\n",
		"✓ healthy",
		"api",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("history output missing %q; got:\n%s", want, s)
		}
	}
}

// TestE2E_Inspect_AfterSync drives `accorda inspect` (no commit) after a
// successful sync and verifies it prints the per-service §11 inspect view for
// the most recent deployment: the deployed digest, recreated flag, and a
// passed health result (docs/ACCORDA.md §11).
func TestE2E_Inspect_AfterSync(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	cleanupComposeProject(t, dir)

	// First converge so a healthy receipt with a resolved digest is recorded.
	var syncOut bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &syncOut, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	var out bytes.Buffer
	if err := run([]string{"inspect", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"api\n",
		"  deployed digest    ",
		"  recreated          yes\n",
		"  health             passed\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("inspect output missing %q; got:\n%s", want, s)
		}
	}
}

// TestE2E_History_RecordsFailure verifies `accorda history` records both
// healthy and failed cycles: after a healthy sync, a second sync against a
// nonexistent image fails and `history` must show both rows (the healthy
// cycle and the failed one) (docs/ACCORDA.md §11).
func TestE2E_History_RecordsFailure(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir := writeE2EProject(t)
	origin := gitOriginDir(t, dir)
	cleanupComposeProject(t, dir)

	// First converge so a healthy receipt exists (rollback has a target).
	var first bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &first, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Advance Git to a nonexistent image so the deploy fails (rollback is
	// applied, recording an OutcomeFailed receipt before the rollback).
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write origin compose (bad image): %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "bump to bad image")
	if err := os.WriteFile(filepath.Join(dir, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write target compose (bad image): %v", err)
	}

	// Second sync fails (with rollback), recording the failed cycle.
	var second bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &second, nil); err == nil {
		t.Fatalf("second sync succeeded, want failure: %q", second.String())
	}

	var out bytes.Buffer
	if err := run([]string{"history", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("history: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "✓ healthy") {
		t.Errorf("history output missing healthy row; got:\n%s", s)
	}
	if !strings.Contains(s, "✗ failed") {
		t.Errorf("history output missing failed row; got:\n%s", s)
	}
}

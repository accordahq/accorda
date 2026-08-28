//go:build integration

// The integration build tag keeps these tests out of the default `go test`
// run because they require a running Docker daemon, the `docker compose` CLI,
// and the system `git` executable. Run with:
//
//	go test ./cmd/accorda/ -tags integration
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"accorda/internal/config"
	"accorda/internal/core/history"
	"accorda/internal/targets/compose"
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

// writeE2EProject creates a Git origin repository declaring e2eCompose and an
// independent operator directory containing accorda.yaml. The target Compose
// file exists only in Git; Accorda must deploy it from its managed checkout.
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

	// Project directory: accorda.yaml only. Its test-specific basename keeps
	// Compose cleanup isolated from operator projects on the same Docker
	// daemon while remaining deterministic for this test.
	dir := e2eProjectDir(t)
	// The relative target filename resolves inside the managed Git checkout,
	// not beside accorda.yaml.
	project := `version: 1
environment: production
source:
  type: git
  url: file://` + origin + `
  branch: main
target:
  type: ` + config.TargetCompose + `
  file: ` + config.DefaultComposeFile + `
images:
  pull: ` + config.PullNever + `
health:
  timeout: 30s
`
	if err := os.WriteFile(filepath.Join(dir, config.File), []byte(project), 0o644); err != nil {
		t.Fatalf("write accorda.yaml: %v", err)
	}
	return dir
}

func e2eProjectDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	sum := sha256.Sum256([]byte(base + "\x00" + t.Name()))
	dir := filepath.Join(base, fmt.Sprintf("accorda-e2e-%x", sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
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

// cleanupComposeProject registers a best-effort teardown for this test's
// isolated Compose project. Keeping the command here ensures every test uses
// the shared Compose filename and the same cleanup behavior.
func cleanupComposeProject(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		project := compose.ProjectName(config.Target{File: filepath.Join(dir, config.DefaultComposeFile)})
		cmd := exec.Command("docker", "compose", "-f", managedComposeFile(t, dir), "-p", project, "down", "--remove-orphans")
		_ = cmd.Run()
	})
}

func managedComposeFile(t *testing.T, dir string) string {
	t.Helper()
	proj, err := config.Load(dir)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	src, err := buildSource(proj, dir)
	if err != nil {
		t.Fatalf("build source: %v", err)
	}
	tgt, err := buildTarget(proj, dir, src, proj.Name)
	if err != nil {
		t.Fatalf("build target: %v", err)
	}
	ct, ok := tgt.(*compose.Target)
	if !ok {
		t.Fatalf("build target: expected *compose.Target, got %T", tgt)
	}
	return ct.File()
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

	store := history.NewFileStore(receiptPath(dir, ""))
	before, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list receipts after first sync: %v", err)
	}

	// A second invocation constructs a fresh reconciler and recovers its
	// deployed baseline from the compact receipt journal. It must hydrate that
	// baseline from Git and leave an unchanged deployment alone.
	out.Reset()
	if err := run([]string{"sync", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	after, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list receipts after second sync: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("receipt count after unchanged sync = %d, want %d", len(after), len(before))
	}
	if !strings.Contains(out.String(), "SYNCED") {
		t.Errorf("second sync output = %q, want it to contain SYNCED", out.String())
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

	// 2. Advance Git to a commit declaring a nonexistent image. Accorda must
	// update its managed checkout so the forward deploy attempts busybox:9.9.
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write origin compose (bad image): %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "bump to bad image")

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
	data, readErr := os.ReadFile(managedComposeFile(t, dir))
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

	// Advance Git to the nonexistent image before any successful sync, so
	// there is no healthy receipt to roll back to.
	origin := gitOriginDir(t, dir)
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write origin compose (bad image): %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "bump to bad image")

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
	data, readErr := os.ReadFile(managedComposeFile(t, dir))
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
// The deployed baseline is the full service model reloaded from the source revision at
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

// ensembleComposeAPI and ensembleComposeWorker are the Compose files committed
// to two independent origin repositories. They declare differently-named
// services so a successful multi-project sync proves the two targets are
// isolated: each project deploys only its own service, and neither project's
// --remove-orphans removes the other's containers (docs/ACCORDA.md §49).
const ensembleComposeAPI = `services:
  api:
    image: busybox:1.36
    command: ["sh", "-c", "sleep 300"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 3
`

const ensembleComposeWorker = `services:
  worker:
    image: busybox:1.36
    command: ["sh", "-c", "sleep 300"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 3
`

// writeEnsembleOrigin creates a single-commit Git origin with the given
// Compose content and returns its file:// URL.
func writeEnsembleOrigin(t *testing.T, compose string) string {
	t.Helper()
	origin := t.TempDir()
	runGit(t, origin, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(compose), 0o644); err != nil {
		t.Fatalf("write origin compose: %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "initial")
	return "file://" + origin
}

// writeEnsembleProject writes a multi-project accorda.yaml that declares two
// named projects (api and worker), each with its own Git origin, so one
// `accorda sync` drives both workloads concurrently
// (docs/ACCORDA.md §49). The schema version, sync cadence, and policy
// defaults live at the document root and are inherited by both members
// (docs/DECISIONS.md #43).
func writeEnsembleProject(t *testing.T, apiURL, workerURL string) string {
	t.Helper()
	dir := e2eProjectDir(t)
	doc := `version: 1
images:
  pull: ` + config.PullNever + `
health:
  timeout: 30s
projects:
  - name: api
    environment: production
    source:
      type: git
      url: ` + apiURL + `
      branch: main
    target:
      type: ` + config.TargetCompose + `
      file: ` + config.DefaultComposeFile + `
  - name: worker
    environment: production
    source:
      type: git
      url: ` + workerURL + `
      branch: main
    target:
      type: ` + config.TargetCompose + `
      file: ` + config.DefaultComposeFile + `
`
	if err := os.WriteFile(filepath.Join(dir, config.File), []byte(doc), 0o600); err != nil {
		t.Fatalf("write accorda.yaml: %v", err)
	}
	return dir
}

// cleanupEnsembleProject tears down both members' Compose projects so one
// test's containers do not leak into the next. It derives the managed Compose
// file per member via buildSource so the cleanup targets the actual checkout.
func cleanupEnsembleProject(t *testing.T, dir string) {
	t.Helper()
	projects, err := loadProjects(dir)
	if err != nil {
		t.Fatalf("load projects for cleanup: %v", err)
	}
	t.Cleanup(func() {
		for i := range projects {
			p := &projects[i]
			src, err := buildSource(p, dir)
			if err != nil {
				continue
			}
			tgt, err := buildTarget(p, dir, src, p.Name)
			if err != nil {
				continue
			}
			ct, ok := tgt.(*compose.Target)
			if !ok {
				continue
			}
			file := ct.File()
			// The deployed Compose project name is the member name (or
			// base+"-"+target name for a named target), set on the Target
			// via WithProjectName. Recomposing it from the cache-dir path
			// would derive accorda-<hash> and target the wrong project, so
			// address the same project the reconciler deploys under.
			cmd := exec.Command("docker", "compose", "-f", file, "-p", ct.ComposeProject(), "down", "--remove-orphans")
			_ = cmd.Run()
		}
	})
}

// TestE2E_EnsembleSync_ConvergesBothProjects drives a multi-project
// `accorda sync` end-to-end: two independent Git origins each declare a
// single-service Compose file, and one sync reconciles both projects
// concurrently. Both members must report SYNCED, and each project's receipt
// journal must contain a healthy entry — proving independent targets reconcile
// concurrently under one agent (docs/ACCORDA.md §49).
func TestE2E_EnsembleSync_ConvergesBothProjects(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	apiURL := writeEnsembleOrigin(t, ensembleComposeAPI)
	workerURL := writeEnsembleOrigin(t, ensembleComposeWorker)
	dir := writeEnsembleProject(t, apiURL, workerURL)
	cleanupEnsembleProject(t, dir)

	var out bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("ensemble sync: %v\noutput: %s", err, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "api: sync: SYNCED") {
		t.Errorf("ensemble sync output missing api SYNCED; got:\n%s", s)
	}
	if !strings.Contains(s, "worker: sync: SYNCED") {
		t.Errorf("ensemble sync output missing worker SYNCED; got:\n%s", s)
	}

	// Each member must have its own receipt journal with at least one
	// healthy entry, proving the per-name state paths are isolated.
	for _, name := range []string{"api", "worker"} {
		store := history.NewFileStore(receiptPath(dir, name))
		receipts, err := store.List(context.Background())
		if err != nil {
			t.Fatalf("list %s receipts: %v", name, err)
		}
		if len(receipts) == 0 {
			t.Errorf("project %s has no receipts after ensemble sync", name)
		}
	}
}

// writeImageProject creates a Git origin repository with a placeholder file
// (the image target's desired state is config-driven, so Git only anchors the
// commit) and an independent operator directory containing an accorda.yaml
// whose target.type is image. The project's directory basename is the service
// name the single container is managed under.
func writeImageProject(t *testing.T) string {
	t.Helper()
	origin := t.TempDir()
	runGit(t, origin, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("fleet\n"), 0o644); err != nil {
		t.Fatalf("write origin placeholder: %v", err)
	}
	runGit(t, origin, "add", "README.md")
	runGit(t, origin, "commit", "-m", "initial")
	dir := e2eProjectDir(t)
	project := `version: 1
environment: production
source:
  type: git
  url: file://` + origin + `
  branch: main
target:
  type: ` + config.TargetImage + `
  image: nginx:alpine
images:
  pull: ` + config.PullNever + `
health:
  timeout: 30s
`
	if err := os.WriteFile(filepath.Join(dir, config.File), []byte(project), 0o644); err != nil {
		t.Fatalf("write accorda.yaml: %v", err)
	}
	return dir
}

// cleanupImageProject removes the single container the image target manages so
// repeated E2E runs do not accumulate containers. The service name is the
// project directory basename, matching buildImageTarget's default.
func cleanupImageProject(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		name := filepath.Base(filepath.Clean(dir))
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
}

// TestE2E_ImageSync_ConvergesToSynced drives a raw single-image target
// end-to-end: a Git origin anchors the commit, and accorda.yaml declares the
// image directly. `accorda sync` must run the container from the config-
// derived desired state and converge to SYNCED (docs/DECISIONS.md #24).
func TestE2E_ImageSync_ConvergesToSynced(t *testing.T) {
	testutil.RequireDocker(t)
	testutil.RequireGit(t)

	dir := writeImageProject(t)
	cleanupImageProject(t, dir)

	var out bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("sync: %v\noutput: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "SYNCED") {
		t.Errorf("sync output = %q, want it to contain SYNCED", out.String())
	}

	// A second sync must observe the converged container and stay SYNCED
	// without redeploying.
	out.Reset()
	if err := run([]string{"sync", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !strings.Contains(out.String(), "SYNCED") {
		t.Errorf("second sync output = %q, want it to contain SYNCED", out.String())
	}
}

// multiTargetCompose declares the Compose target of a mixed multi-target
// project (issue #103): a healthy busybox:1.36 service with a healthcheck, so
// a first sync converges it to SYNCED.
const multiTargetCompose = `services:
  api:
    image: busybox:1.36
    command: ["sh", "-c", "sleep 300"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 3
`

// writeMultiTargetProject creates a Git origin with a placeholder file and an
// operator directory whose accorda.yaml declares one named project with a
// mixed targets: list — a Compose target (compose.yaml) and an image target
// (nginx:alpine) — both reconciling from the same single source. Using two
// target types keeps them in independent namespaces (Compose project vs. a
// standalone container), so per-target isolation and rollback are meaningful.
// The project and target names are derived from the test name so Docker
// containers do not collide across tests running in the same package.
func writeMultiTargetProject(t *testing.T) (dir, origin, projectName, composeName, imageName string) {
	t.Helper()
	origin = t.TempDir()
	runGit(t, origin, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(multiTargetCompose), 0o644); err != nil {
		t.Fatalf("write origin compose: %v", err)
	}
	runGit(t, origin, "add", ".")
	runGit(t, origin, "commit", "-m", "initial")

	dir = e2eProjectDir(t)
	projectName = multiTargetProjectName(t)
	composeName = multiTargetName(t, "compose")
	imageName = multiTargetName(t, "img")
	doc := `version: 1
projects:
  - name: ` + projectName + `
    environment: production
    source:
      type: git
      url: file://` + origin + `
      branch: main
    targets:
      - name: ` + composeName + `
        type: ` + config.TargetCompose + `
        file: ` + config.DefaultComposeFile + `
      - name: ` + imageName + `
        type: ` + config.TargetImage + `
        image: nginx:alpine
images:
  pull: ` + config.PullNever + `
health:
  timeout: 30s
`
	if err := os.WriteFile(filepath.Join(dir, config.File), []byte(doc), 0o600); err != nil {
		t.Fatalf("write accorda.yaml: %v", err)
	}
	return dir, origin, projectName, composeName, imageName
}

// multiTargetProjectName derives a per-test Compose project name so the
// Docker containers one test deploys do not collide with another test's. It
// uses a hash of the test name so two top-level tests (which share no common
// prefix beyond "TestE2E_MultiTargetSync_") still get distinct, Compose-safe
// slugs.
func multiTargetProjectName(t *testing.T) string {
	sum := sha256.Sum256([]byte(t.Name()))
	return fmt.Sprintf("mt-%x", sum[:4])
}

// multiTargetName derives a per-test target name (used as the image container
// name) so two tests' image containers do not collide on the Docker daemon.
func multiTargetName(t *testing.T, kind string) string {
	return kind + "-" + multiTargetProjectName(t)
}

// cleanupMultiTargetProject tears down both workloads the multi-target sync
// deployed: the Compose project (base+"-"+composeTarget) and the image target's
// standalone container (named imageTarget), so one test's containers do not
// leak into the next. It uses the deterministic names from writeMultiTargetProject
// directly, so it does not need to re-resolve the source checkout.
func cleanupMultiTargetProject(t *testing.T, projectName, composeName, imageName string) {
	t.Helper()
	t.Cleanup(func() {
		// Compose project is base+"-"+targetName for a named target.
		_ = exec.Command("docker", "compose", "-p", projectName+"-"+composeName, "down", "--remove-orphans").Run()
		_ = exec.Command("docker", "rm", "-f", imageName).Run()
	})
}

// TestE2E_MultiTargetSync_ReconcilesAllTargets drives one `accorda sync`
// through a multi-target project: a single source revision fans out to a
// Compose and an image target, each converges to SYNCED, and each keeps its
// own receipt journal (issue #103, docs/ACCORDA.md §49).
func TestE2E_MultiTargetSync_ReconcilesAllTargets(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir, _, projectName, composeName, imageName := writeMultiTargetProject(t)
	cleanupMultiTargetProject(t, projectName, composeName, imageName)

	var out bytes.Buffer
	if err := run([]string{"sync", "--dir", dir}, &out, nil); err != nil {
		t.Fatalf("multi-target sync: %v\noutput: %s", err, out.String())
	}
	s := out.String()
	if !strings.Contains(s, composeName+": sync: SYNCED") {
		t.Errorf("sync output missing compose target SYNCED; got:\n%s", s)
	}
	if !strings.Contains(s, imageName+": sync: SYNCED") {
		t.Errorf("sync output missing image target SYNCED; got:\n%s", s)
	}

	// Each target must have its own receipt journal with a healthy entry,
	// proving per-target state isolation (issue #103).
	proj := loadMultiTargetProject(t, dir)
	targets := proj.NormalizedTargets()
	for i := range targets {
		store := history.NewFileStore(targetReceiptPath(dir, proj.Name, targets[i], len(targets) > 1))
		receipts, err := store.List(context.Background())
		if err != nil {
			t.Fatalf("list target %d receipts: %v", i, err)
		}
		if len(receipts) == 0 {
			t.Errorf("target %s has no receipts after sync", targets[i].Identity())
		}
	}
}

// loadMultiTargetProject loads the project file for a multi-target e2e test.
func loadMultiTargetProject(t *testing.T, dir string) *config.Project {
	t.Helper()
	projects, err := loadProjects(dir)
	if err != nil {
		t.Fatalf("load projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("project count = %d, want 1", len(projects))
	}
	return &projects[0]
}

// TestE2E_MultiTargetSync_RollsBackIndependently verifies per-target rollback:
// a first sync converges both targets and records healthy receipts; Git then
// advances with a broken Compose image so the Compose target's deploy fails.
// The failure must roll back the Compose target to its previous healthy commit
// while leaving the image target's healthy deployment and journal untouched
// (issue #103, docs/ACCORDA.md §20). The image target is used as the stable
// sibling precisely because its config-driven desired state does not change
// when Git advances, so it is a genuine independent workload.
func TestE2E_MultiTargetSync_RollsBackIndependently(t *testing.T) {
	testutil.RequireCompose(t)
	testutil.RequireGit(t)

	dir, origin, projectName, composeName, imageName := writeMultiTargetProject(t)
	cleanupMultiTargetProject(t, projectName, composeName, imageName)

	// 1. Converge both targets and record their healthy receipts.
	if err := run([]string{"sync", "--dir", dir}, &bytes.Buffer{}, nil); err != nil {
		t.Fatalf("first multi-target sync: %v", err)
	}
	proj := loadMultiTargetProject(t, dir)
	targets := proj.NormalizedTargets()
	composeStore := history.NewFileStore(targetReceiptPath(dir, proj.Name, targets[0], true))
	imageStore := history.NewFileStore(targetReceiptPath(dir, proj.Name, targets[1], true))

	// 2. Break only the Compose target's image in Git (nonexistent tag). The
	// image target's desired state is config-driven, so Git content does not
	// affect it.
	if err := os.WriteFile(filepath.Join(origin, testutil.ComposeFile), []byte(badImageCompose), 0o644); err != nil {
		t.Fatalf("write broken compose: %v", err)
	}
	runGit(t, origin, "add", testutil.ComposeFile)
	runGit(t, origin, "commit", "-m", "break compose image")

	// 3. Second sync: Compose target fails and rolls back; image target stays
	// healthy.
	var out bytes.Buffer
	err := run([]string{"sync", "--dir", dir}, &out, nil)
	if err == nil {
		t.Fatalf("second sync succeeded, want a compose rollback: %q", out.String())
	}
	if !strings.Contains(out.String(), "rollback: restored to commit") {
		t.Errorf("second sync output = %q, want a rollback message", out.String())
	}

	// 4. Compose target's journal has a rolled_back receipt; image target's
	// journal still has a healthy receipt (its deployment was not touched).
	composeReceipts, err := composeStore.List(context.Background())
	if err != nil {
		t.Fatalf("list compose receipts: %v", err)
	}
	if len(composeReceipts) == 0 {
		t.Fatalf("compose target has no receipts after rollback")
	}
	if composeReceipts[len(composeReceipts)-1].Result != history.OutcomeRolledBack {
		t.Errorf("compose last receipt = %s, want %s", composeReceipts[len(composeReceipts)-1].Result, history.OutcomeRolledBack)
	}
	imageReceipts, err := imageStore.List(context.Background())
	if err != nil {
		t.Fatalf("list image receipts: %v", err)
	}
	if len(imageReceipts) == 0 || imageReceipts[len(imageReceipts)-1].Result != history.OutcomeHealthy {
		t.Errorf("image target lost its healthy deployment after sibling rollback")
	}
}

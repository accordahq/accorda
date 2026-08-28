package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/history"
	"accorda/internal/core/locking"
	"accorda/internal/core/reconcile"
)

func TestBuildWebhook_Disabled_NoOp(t *testing.T) {
	unsub, err := buildWebhook(config.Notifications{}, events.NewBus())
	if err != nil {
		t.Fatalf("buildWebhook: %v", err)
	}
	if unsub != nil {
		t.Error("unsubscribe non-nil for disabled webhook, want nil")
	}
}

func TestBuildWebhook_EnabledMissingConfig_Error(t *testing.T) {
	if _, err := buildWebhook(config.Notifications{Webhook: true}, events.NewBus()); err == nil {
		t.Fatal("expected error for enabled webhook without config, got nil")
	}
}

func TestBuildWebhook_EnabledReturnsUnsubscribe(t *testing.T) {
	unsub, err := buildWebhook(config.Notifications{
		Webhook:       true,
		WebhookConfig: &config.WebhookConfig{URL: "http://127.0.0.1:1", MaxRetries: 0, Timeout: time.Millisecond},
	}, events.NewBus())
	if err != nil {
		t.Fatalf("buildWebhook: %v", err)
	}
	if unsub == nil {
		t.Fatal("unsubscribe nil, want non-nil")
	}
	unsub() // must not panic
}

func TestBuildTarget_Compose(t *testing.T) {
	p := &config.Project{
		Source: config.Source{URL: "https://example.com/acme/repo.git"},
		Target: config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile},
		Images: config.Images{Pull: config.PullAlways},
		Health: config.Health{Timeout: 0},
	}
	src, err := buildSource(p, ".")
	if err != nil {
		t.Fatalf("buildSource error = %v", err)
	}
	tgt, err := buildTarget(p, ".", src, "")
	if err != nil {
		t.Fatalf("buildTarget(compose) error = %v", err)
	}
	if tgt == nil {
		t.Fatal("buildTarget(compose) returned nil target")
	}
}

func TestBuildTarget_Unsupported(t *testing.T) {
	p := &config.Project{
		Target: config.Target{Type: config.TargetKubernetes, Path: "manifests"},
	}
	src, err := buildSource(p, ".")
	if err != nil {
		t.Fatalf("buildSource error = %v", err)
	}
	_, err = buildTarget(p, ".", src, "")
	if err == nil {
		t.Fatal("expected error for unsupported target, got nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestBuildSourcePreservesTargetIndependentRemotePath(t *testing.T) {
	cases := []struct {
		name       string
		sourcePath string
		targetFile string
	}{
		{name: "root file", targetFile: "docker-compose.yml"},
		{name: "source directory", sourcePath: "services/api", targetFile: "compose.yaml"},
		{name: "explicit source file", sourcePath: "deploy/prod.yml", targetFile: "compose.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &config.Project{
				Source: config.Source{URL: "https://example.com/acme/repo.git", Path: tc.sourcePath},
				Target: config.Target{Type: config.TargetCompose, File: tc.targetFile},
			}
			src, err := buildSource(p, ".")
			if err != nil {
				t.Fatalf("buildSource: %v", err)
			}
			if src.Source.Path != tc.sourcePath {
				t.Errorf("source path = %q, want unchanged %q", src.Source.Path, tc.sourcePath)
			}
		})
	}
}

func TestBuildSourceResolvesInPlaceWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repositoryRoot := filepath.Join(home, "repository")
	if _, err := gogit.PlainInit(repositoryRoot, false); err != nil {
		t.Fatalf("init repository: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repositoryRoot, "deploy"), 0o755); err != nil {
		t.Fatalf("mkdir deploy: %v", err)
	}
	worktreeRoot := filepath.Join(home, "worktree")
	if _, err := gogit.PlainInit(worktreeRoot, false); err != nil {
		t.Fatalf("init worktree repository: %v", err)
	}
	for _, file := range []string{filepath.Join(repositoryRoot, "docker-compose.yml"), filepath.Join(repositoryRoot, "deploy", config.DefaultComposeFile)} {
		if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatalf("write source file: %v", err)
		}
	}
	cases := []struct {
		name        string
		sourcePath  string
		targetFile  string
		wantRoot    string
		wantBinding string
	}{
		{
			name:        "directory",
			sourcePath:  filepath.Join(home, "worktree"),
			targetFile:  filepath.Join("deploy", config.DefaultComposeFile),
			wantRoot:    filepath.Join(home, "worktree"),
			wantBinding: worktreeRoot,
		},
		{
			name:        "root explicit file",
			sourcePath:  filepath.Join(repositoryRoot, "docker-compose.yml"),
			targetFile:  config.DefaultComposeFile,
			wantRoot:    repositoryRoot,
			wantBinding: filepath.Join(repositoryRoot, "docker-compose.yml"),
		},
		{
			name:        "nested explicit file",
			sourcePath:  filepath.Join(repositoryRoot, "deploy", config.DefaultComposeFile),
			targetFile:  "ignored.yaml",
			wantRoot:    repositoryRoot,
			wantBinding: filepath.Join(repositoryRoot, "deploy", config.DefaultComposeFile),
		},
		{
			name:        "home shorthand",
			sourcePath:  "~/worktree",
			targetFile:  config.DefaultComposeFile,
			wantRoot:    filepath.Join(home, "worktree"),
			wantBinding: worktreeRoot,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &config.Project{
				Source: config.Source{Type: "git", Path: tc.sourcePath},
				Target: config.Target{Type: config.TargetCompose, File: tc.targetFile},
			}
			src, err := buildSource(p, ".")
			if err != nil {
				t.Fatalf("buildSource: %v", err)
			}
			if src.CacheDir != tc.wantRoot {
				t.Errorf("worktree root = %q, want %q", src.CacheDir, tc.wantRoot)
			}
			if src.Source.Path != tc.wantBinding {
				t.Errorf("source binding = %q, want %q", src.Source.Path, tc.wantBinding)
			}
		})
	}
}

func TestBuildSourceIgnoresAbsoluteTargetPath(t *testing.T) {
	p := &config.Project{
		Source: config.Source{URL: "https://example.com/acme/repo.git"},
		Target: config.Target{Type: config.TargetCompose, File: filepath.Join(t.TempDir(), config.DefaultComposeFile)},
	}
	src, err := buildSource(p, ".")
	if err != nil {
		t.Fatalf("buildSource: %v", err)
	}
	if src.Source.Path != "" {
		t.Errorf("source path = %q, want unchanged empty path", src.Source.Path)
	}
}

func TestBuildSourceIsolatesManagedCheckoutByProject(t *testing.T) {
	p := &config.Project{
		Source: config.Source{URL: "https://example.com/acme/repo.git", Branch: "main"},
		Target: config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile},
	}
	first, err := buildSource(p, filepath.Join(t.TempDir(), "production"))
	if err != nil {
		t.Fatalf("build first source: %v", err)
	}
	second, err := buildSource(p, filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatalf("build second source: %v", err)
	}
	firstPath, err := first.CheckoutPath(config.DefaultComposeFile)
	if err != nil {
		t.Fatalf("first checkout path: %v", err)
	}
	secondPath, err := second.CheckoutPath(config.DefaultComposeFile)
	if err != nil {
		t.Fatalf("second checkout path: %v", err)
	}
	if firstPath == secondPath {
		t.Fatalf("project checkouts share path %q", firstPath)
	}
}

func TestBuildSourceIsolatesEnsembleMembersByName(t *testing.T) {
	p := &config.Project{
		Source: config.Source{URL: "https://example.com/acme/repo.git", Branch: "main"},
		Target: config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile},
	}
	// Two ensemble members in the same project directory with different names
	// must get isolated managed checkouts even though they share a repo URL,
	// otherwise two branches of one repository would race on the same worktree
	// (docs/ACCORDA.md §49, docs/DECISIONS.md #22).
	p.Name = "api"
	api, err := buildSource(p, "shared-dir")
	if err != nil {
		t.Fatalf("build api source: %v", err)
	}
	p.Name = "worker"
	worker, err := buildSource(p, "shared-dir")
	if err != nil {
		t.Fatalf("build worker source: %v", err)
	}
	apiPath, err := api.CheckoutPath(config.DefaultComposeFile)
	if err != nil {
		t.Fatalf("api checkout path: %v", err)
	}
	workerPath, err := worker.CheckoutPath(config.DefaultComposeFile)
	if err != nil {
		t.Fatalf("worker checkout path: %v", err)
	}
	if apiPath == workerPath {
		t.Fatalf("ensemble member checkouts share path %q", apiPath)
	}
}

func TestDeploymentLockPathUsesTargetIdentity(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	composeFile := filepath.Join(t.TempDir(), config.DefaultComposeFile)
	target := config.Target{Type: config.TargetCompose, File: composeFile}

	first := deploymentLockPath(t.TempDir(), target)
	second := deploymentLockPath(t.TempDir(), target)
	if first != second {
		t.Errorf("same target lock paths differ: %q != %q", first, second)
	}
	pathSpelling := deploymentLockPath(t.TempDir(), config.Target{Type: config.TargetCompose, Path: composeFile})
	if first != pathSpelling {
		t.Errorf("target.file and target.path lock paths differ: %q != %q", first, pathSpelling)
	}
	sameProject := deploymentLockPath(t.TempDir(), config.Target{
		Type: config.TargetCompose,
		File: filepath.Join(filepath.Dir(composeFile), "compose-production.yaml"),
	})
	if first != sameProject {
		t.Errorf("Compose files for the same project have different lock paths: %q != %q", first, sameProject)
	}
	other := deploymentLockPath(t.TempDir(), config.Target{
		Type: config.TargetCompose,
		File: filepath.Join(t.TempDir(), config.DefaultComposeFile),
	})
	if first == other {
		t.Errorf("different targets share lock path %q", first)
	}
	if filepath.Ext(first) != ".lock" {
		t.Errorf("lock path = %q, want .lock extension", first)
	}

	dir := t.TempDir()
	relativeFile := deploymentLockPath(dir, config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile})
	relativePath := deploymentLockPath(dir, config.Target{Type: config.TargetCompose, Path: config.DefaultComposeFile})
	if relativeFile != relativePath {
		t.Errorf("relative target.file and target.path lock paths differ: %q != %q", relativeFile, relativePath)
	}
}

func TestWithDeploymentLockSerializesAgainstHeldLock(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	target := config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile}

	// Hold the lock, then verify withDeploymentLock blocks until it is
	// released and runs the callback exactly once.
	unlock, err := locking.NewFileLocker(deploymentLockPath(dir, target)).Lock(context.Background())
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	ran := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := withDeploymentLock(context.Background(), dir, target, func() error {
			ran <- struct{}{}
			return nil
		})
		if err != nil {
			t.Errorf("withDeploymentLock: %v", err)
		}
	}()
	select {
	case <-ran:
		t.Fatal("callback ran while the lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	if err := unlock(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not run after the lock was released")
	}
	<-done
}

func TestWithDeploymentLockRunsCallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	target := config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile}
	called := false
	err := withDeploymentLock(context.Background(), dir, target, func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("withDeploymentLock: %v", err)
	}
	if !called {
		t.Fatal("callback was not invoked")
	}
}

func TestProjectStatePathUsesDefaultKeyForCurrentDirectory(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	want := filepath.Join(base, "accorda", "receipts", "default.jsonl")
	if got := projectStatePath("receipts", ".", "", ".jsonl"); got != want {
		t.Errorf("projectStatePath() = %q, want %q", got, want)
	}
}

func TestProjectStatePathScopesByProjectName(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	want := filepath.Join(base, "accorda", "receipts", filepath.Join("projects", "production", "api.jsonl"))
	if got := projectStatePath("receipts", filepath.Join("projects", "production"), "api", ".jsonl"); got != want {
		t.Errorf("projectStatePath() = %q, want %q", got, want)
	}
}

func TestStateBaseFallsBackWhenHomeIsUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	want := filepath.Join(".local", "state")
	if got := stateBase(); got != want {
		t.Errorf("stateBase() = %q, want %q", got, want)
	}
}

func TestDriftPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want reconcile.DriftPolicy
	}{
		{config.DriftRepair, reconcile.DriftRepair},
		{config.DriftDisabled, reconcile.DriftDisabled},
		{config.DriftReport, reconcile.DriftReport},
		{"bogus", reconcile.DriftReport},
		{"", reconcile.DriftReport},
	}
	for _, c := range cases {
		if got := driftPolicy(c.in); got != c.want {
			t.Errorf("driftPolicy(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPreviousFromHistory_EmptyHistory_NoPrevious verifies that an empty
// (or failed-only) history yields no rollback target, so the reconciler
// treats rollback as unsafe and lets the failure stand.
func TestPreviousFromHistory_EmptyHistory_NoPrevious(t *testing.T) {
	store := history.NewFileStore(t.TempDir() + "/receipts.jsonl")
	if prev := previousFromHistory(store, nil); prev != nil {
		t.Fatalf("previousFromHistory(empty) = %+v, want nil", prev)
	}
}

// TestPreviousFromHistory_ReturnsLastHealthy verifies that the most recent
// OutcomeHealthy receipt is reconstructed as the rollback target, skipping
// failed receipts, with per-service images from the recorded history.
func TestPreviousFromHistory_ReturnsLastHealthy(t *testing.T) {
	path := t.TempDir() + "/receipts.jsonl"
	store := history.NewFileStore(path)

	// A failed deployment, then a healthy one, then another failed one.
	now := time.Now()
	for _, rc := range []history.Receipt{
		{DeploymentID: "dep_1", Commit: "abc", Result: history.OutcomeFailed},
		{
			DeploymentID: "dep_2",
			Commit:       "def",
			Result:       history.OutcomeHealthy,
			Services:     map[string]history.ServiceReceipt{"api": {Image: "api:1", Digest: "sha256:a"}},
		},
		{DeploymentID: "dep_3", Commit: "ghi", Result: history.OutcomeFailed},
	} {
		rc.StartedAt = now
		rc.CompletedAt = now
		if err := store.Append(context.Background(), rc); err != nil {
			t.Fatalf("append receipt: %v", err)
		}
	}

	prev := previousFromHistory(store, nil)
	if prev == nil {
		t.Fatal("previousFromHistory = nil, want the last healthy deployment")
	}
	if prev.Commit != "def" {
		t.Errorf("prev.Commit = %q, want def (last healthy)", prev.Commit)
	}
	if prev.DeploymentID != "dep_2" {
		t.Errorf("prev.DeploymentID = %q, want dep_2", prev.DeploymentID)
	}
	if got := prev.Services["api"].Image; got != "api:1" {
		t.Errorf("prev api.Image = %q, want api:1", got)
	}
}

// TestPreviousFromHistory_NilStore verifies a nil store yields no rollback
// target.
func TestPreviousFromHistory_NilStore(t *testing.T) {
	if prev := previousFromHistory(nil, nil); prev != nil {
		t.Fatalf("previousFromHistory(nil) = %+v, want nil", prev)
	}
}

// TestPreviousFromHistory_StoreError_WarnsAndNoPrevious verifies that a store
// read error is reported to the warning writer and yields no rollback target,
// so an operator can distinguish "no prior healthy deployment" from "history
// could not be read".
func TestPreviousFromHistory_StoreError_WarnsAndNoPrevious(t *testing.T) {
	// A directory path is not a readable journal, so List returns an error.
	store := history.NewFileStore(t.TempDir())
	var warn bytes.Buffer
	if prev := previousFromHistory(store, &warn); prev != nil {
		t.Fatalf("previousFromHistory(error) = %+v, want nil", prev)
	}
	if !strings.Contains(warn.String(), "could not read deployment history") {
		t.Errorf("warning = %q, want it to mention the history read failure", warn.String())
	}
}

// TestTargetReceiptPath_SingleTargetPreservesLegacyPath verifies that a
// single-target project keeps the byte-identical legacy receipt path, so
// existing journals and state layout are preserved (issue #103).
func TestTargetReceiptPath_SingleTargetPreservesLegacyPath(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	dir := t.TempDir()
	single := config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile}
	if got := targetReceiptPath(dir, "", single, false); got != receiptPath(dir, "") {
		t.Errorf("single-target receipt path = %q, want legacy %q", got, receiptPath(dir, ""))
	}
}

// TestTargetReceiptPath_MultiTargetScopesByTarget verifies that two targets in
// one project get distinct receipt journals, so their history does not
// collide (issue #103).
func TestTargetReceiptPath_MultiTargetScopesByTarget(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	dir := t.TempDir()
	a := targetReceiptPath(dir, "", config.Target{Type: config.TargetCompose, File: "docker-compose.yml"}, true)
	b := targetReceiptPath(dir, "", config.Target{Type: config.TargetCompose, File: "qa/docker-compose.yml"}, true)
	if a == b {
		t.Fatalf("two targets share receipt journal %q", a)
	}
}

// TestTargetIdentity verifies the human-readable target identity used to
// prefix per-target output in a multi-target project: the operator Name when
// set, else a deterministic label from the type and configured path/image
// (issue #103).
func TestTargetIdentity(t *testing.T) {
	cases := []struct {
		name string
		tgt  config.Target
		want string
	}{
		{"compose file", config.Target{Type: config.TargetCompose, File: "docker-compose.yml"}, "compose:docker-compose.yml"},
		{"image", config.Target{Type: config.TargetImage, Image: "registry/x:1"}, "image:registry/x:1"},
		{"operator name wins", config.Target{Name: "edge", Type: config.TargetImage, Image: "registry/x:1"}, "edge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tgt.Identity(); got != tc.want {
				t.Errorf("Identity() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildEnsembleMembers_MultiTargetProject verifies a project with several
// targets produces one Ensemble member whose runner reconciles all targets,
// and that each target gets its own receipt store (issue #103).
func TestBuildEnsembleMembers_MultiTargetProject(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	dir := t.TempDir()
	projects := []config.Project{{
		Name:        "aura",
		Version:     config.SchemaVersion,
		Environment: "production",
		Source:      config.Source{Type: "git", URL: "https://example.com/repo.git", Branch: "main"},
		Targets: config.Targets{
			{Type: config.TargetCompose, File: "docker-compose.yml"},
			{Type: config.TargetCompose, File: "qa/docker-compose.yml"},
		},
	}}
	members, cleanup, err := buildEnsembleMembers(dir, projects, nil)
	if err != nil {
		t.Fatalf("buildEnsembleMembers: %v", err)
	}
	defer cleanup()
	if len(members) != 1 {
		t.Fatalf("member count = %d, want 1", len(members))
	}
	project, ok := members[0].Runner.(*reconcile.Project)
	if !ok {
		t.Fatalf("member runner = %T, want *reconcile.Project", members[0].Runner)
	}
	results := project.Reconcile(context.Background())
	// Building targets for remote compose files requires no Docker daemon, but
	// the reconciler fetch will fail on the fake remote; we only assert the
	// fan-out produced one result per target.
	if len(results) != 2 {
		t.Fatalf("project result count = %d, want 2", len(results))
	}
}

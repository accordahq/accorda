package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/history"
	"accorda/internal/core/locking"
	"accorda/internal/core/reconcile"
	gitSource "accorda/internal/sources/git"
)

type fakeSyncReconciler struct {
	result *reconcile.Result
	runErr error
}

func (f *fakeSyncReconciler) Reconcile(context.Context) *reconcile.Result {
	return f.result
}

func (f *fakeSyncReconciler) Run(_ context.Context, _ time.Duration, handle reconcile.ResultHandler) error {
	handle(f.result)
	return f.runErr
}

func TestRun_Sync_MissingProjectFile(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	e := run([]string{"sync", "--dir", dir}, &out, nil)
	if e == nil {
		t.Fatal("expected error for missing project file, got nil")
	}
	if !strings.Contains(e.Error(), "config:") {
		t.Fatalf("unexpected error %v", e)
	}
}

func TestSyncCommand_WatchFlag(t *testing.T) {
	cmd := newSyncCmd()
	flag := cmd.Flags().Lookup("watch")
	if flag == nil {
		t.Fatal("sync --watch flag is missing")
	}
	if flag.DefValue != "false" {
		t.Errorf("sync --watch default = %q, want false", flag.DefValue)
	}
}

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

func TestSyncProgressWriter_PrintsNonTerminalTransitions(t *testing.T) {
	var out bytes.Buffer
	write := syncProgressWriter(&out)
	write(context.Background(), events.Event{Type: events.EventDeploymentDetected})
	write(context.Background(), events.Event{Type: events.EventStateTransition, Payload: "invalid"})
	for _, transition := range []reconcile.StateTransition{
		{To: reconcile.PhaseFetching},
		{To: reconcile.PhaseValidating, Commit: "1234567890abcdef"},
		{To: reconcile.PhaseDeploying, Commit: "1234567890abcdef", DeploymentID: "dep_123"},
		{To: reconcile.PhaseSynced, Commit: "1234567890abcdef", DeploymentID: "dep_123"},
		{To: reconcile.PhaseFailed, Commit: "1234567890abcdef", DeploymentID: "dep_123"},
	} {
		write(context.Background(), events.Event{Type: events.EventStateTransition, Payload: transition})
	}

	want := "sync: FETCHING\n" +
		"sync: VALIDATING commit=1234567\n" +
		"sync: DEPLOYING commit=1234567 deployment=dep_123\n"
	if out.String() != want {
		t.Fatalf("progress output = %q, want %q", out.String(), want)
	}
}

func TestSyncProgressWriter_PrintsDriftEvents(t *testing.T) {
	var out bytes.Buffer
	write := syncProgressWriter(&out)
	write(context.Background(), events.Event{Type: events.EventDriftDetected})
	write(context.Background(), events.Event{Type: events.EventDriftReconciled})

	want := "sync: drift detected\n" +
		"sync: drift repaired\n"
	if out.String() != want {
		t.Fatalf("progress output = %q, want %q", out.String(), want)
	}
}

func TestWriteSyncResult_PrintsTerminalOutcome(t *testing.T) {
	cases := []struct {
		name    string
		result  *reconcile.Result
		wantOut string
		wantErr string
	}{
		{
			name:    "synced",
			result:  &reconcile.Result{Phase: reconcile.PhaseSynced},
			wantOut: "sync: SYNCED\nsync= reasons=0 services=0\n",
		},
		{
			name:    "failed",
			result:  &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("apply failed")},
			wantOut: "sync: FAILED\n",
			wantErr: "reconciliation failed: apply failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := newSyncCmd()
			cmd.SetOut(&out)
			err := writeSyncResult(cmd, tc.result)
			if out.String() != tc.wantOut {
				t.Errorf("output = %q, want %q", out.String(), tc.wantOut)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("writeSyncResult: %v", err)
			}
			if tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunReconciler(t *testing.T) {
	runErr := errors.New("watch stopped")
	cases := []struct {
		name       string
		watch      bool
		runner     *fakeSyncReconciler
		wantOut    string
		wantErrOut string
		wantErr    error
	}{
		{
			name:    "one shot",
			runner:  &fakeSyncReconciler{result: &reconcile.Result{Phase: reconcile.PhaseSynced}},
			wantOut: "sync: SYNCED\nsync= reasons=0 services=0\n",
		},
		{
			name:    "watch successful cycle",
			watch:   true,
			runner:  &fakeSyncReconciler{result: &reconcile.Result{Phase: reconcile.PhaseSynced}, runErr: runErr},
			wantOut: "sync: SYNCED\nsync= reasons=0 services=0\n",
			wantErr: runErr,
		},
		{
			name:       "watch failed cycle",
			watch:      true,
			runner:     &fakeSyncReconciler{result: &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("fetch failed")}, runErr: runErr},
			wantOut:    "sync: FAILED\n",
			wantErrOut: "sync: reconciliation failed: fetch failed\n",
			wantErr:    runErr,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			cmd := newSyncCmd()
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)

			err := runReconciler(cmd, tc.watch, time.Second, tc.runner)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("runReconciler() error = %v, want %v", err, tc.wantErr)
			}
			if out.String() != tc.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
			if errOut.String() != tc.wantErrOut {
				t.Errorf("stderr = %q, want %q", errOut.String(), tc.wantErrOut)
			}
		})
	}
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
	tgt, err := buildTarget(p, ".", src)
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
	_, err = buildTarget(p, ".", src)
	if err == nil {
		t.Fatal("expected error for unsupported target, got nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestResolveTargetPaths(t *testing.T) {
	base := t.TempDir()
	src := gitSource.New(config.Source{URL: "https://example.com/acme/repo.git", Path: config.DefaultComposeFile},
		gitSource.WithBaseDir(base))
	managed, err := src.CheckoutPath(config.DefaultComposeFile)
	if err != nil {
		t.Fatalf("CheckoutPath: %v", err)
	}
	absolute := filepath.Join(t.TempDir(), config.DefaultComposeFile)
	nested := filepath.Join("deploy", config.DefaultComposeFile)
	cases := []struct {
		name    string
		target  config.Target
		want    config.Target
		managed bool
	}{
		{
			name:    "relative file",
			target:  config.Target{File: config.DefaultComposeFile},
			want:    config.Target{File: managed},
			managed: true,
		},
		{
			name:    "relative path",
			target:  config.Target{Path: nested},
			want:    config.Target{File: managed},
			managed: true,
		},
		{
			name:   "absolute file",
			target: config.Target{File: absolute},
			want:   config.Target{File: absolute},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, managed, err := resolveTargetPaths(tc.target, src)
			if err != nil {
				t.Fatalf("resolveTargetPaths(): %v", err)
			}
			if got.Type != tc.want.Type || got.File != tc.want.File || got.Path != tc.want.Path {
				t.Fatalf("resolveTargetPaths() = %+v, want %+v", got, tc.want)
			}
			if managed != tc.managed {
				t.Fatalf("resolveTargetPaths() managed = %t, want %t", managed, tc.managed)
			}
		})
	}
}

func TestBuildSourceResolvesComposeFileInManagedCheckout(t *testing.T) {
	cases := []struct {
		name       string
		sourcePath string
		targetFile string
		want       string
	}{
		{name: "root Aura file", targetFile: "docker-compose.yml", want: "docker-compose.yml"},
		{name: "source directory", sourcePath: "services/api", targetFile: "compose.yaml", want: "services/api/compose.yaml"},
		{name: "explicit source file wins", sourcePath: "deploy/prod.yml", targetFile: "compose.yaml", want: "deploy/prod.yml"},
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
			if src.Source.Path != tc.want {
				t.Errorf("source path = %q, want %q", src.Source.Path, tc.want)
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
	if src.Source.Path != config.DefaultComposeFile {
		t.Errorf("source path = %q, want %q", src.Source.Path, config.DefaultComposeFile)
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
	if got := projectStatePath("receipts", ".", ".jsonl"); got != want {
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

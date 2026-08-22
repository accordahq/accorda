package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/history"
	"accorda/internal/core/reconcile"
)

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

func TestBuildTarget_Compose(t *testing.T) {
	p := &config.Project{
		Target: config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile},
		Images: config.Images{Pull: config.PullAlways},
		Health: config.Health{Timeout: 0},
	}
	tgt, err := buildTarget(p, ".")
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
	_, err := buildTarget(p, ".")
	if err == nil {
		t.Fatal("expected error for unsupported target, got nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestResolveTargetPaths(t *testing.T) {
	dir := t.TempDir()
	absolute := filepath.Join(t.TempDir(), config.DefaultComposeFile)
	nested := filepath.Join("deploy", config.DefaultComposeFile)
	cases := []struct {
		name   string
		target config.Target
		want   config.Target
	}{
		{
			name:   "relative file",
			target: config.Target{File: config.DefaultComposeFile},
			want:   config.Target{File: filepath.Join(dir, config.DefaultComposeFile)},
		},
		{
			name:   "relative path",
			target: config.Target{Path: nested},
			want:   config.Target{Path: filepath.Join(dir, nested)},
		},
		{
			name:   "absolute file",
			target: config.Target{File: absolute},
			want:   config.Target{File: absolute},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTargetPaths(dir, tc.target)
			if got != tc.want {
				t.Fatalf("resolveTargetPaths() = %+v, want %+v", got, tc.want)
			}
		})
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

package main

import (
	"bytes"
	"strings"
	"testing"

	"accorda/internal/config"
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
		Target: config.Target{Type: config.TargetCompose, File: "compose.yaml"},
		Images: config.Images{Pull: config.PullAlways},
		Health: config.Health{Timeout: 0},
	}
	tgt, err := buildTarget(p)
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
	_, err := buildTarget(p)
	if err == nil {
		t.Fatal("expected error for unsupported target, got nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unexpected error %v", err)
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

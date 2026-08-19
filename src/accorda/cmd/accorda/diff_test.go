package main

import (
	"bytes"
	"strings"
	"testing"

	"accorda/internal/core/state"
)

func TestBuildDiff_ImageChange(t *testing.T) {
	deployed := &state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "ghcr.io/acme/api:2.4.0"},
	}}
	desired := &state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "ghcr.io/acme/api:2.4.1"},
	}}

	roots := buildDiff(deployed, desired)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1: %v", len(roots), roots)
	}
	if roots[0].label != "api" {
		t.Fatalf("root label = %q, want api", roots[0].label)
	}
	img := findDiffNode(roots[0], "image")
	if img == nil {
		t.Fatal("expected an image field node")
	}
	if img.deployed != "ghcr.io/acme/api:2.4.0" || img.desired != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("image deployed/desired = %q/%q, want 2.4.0/2.4.1", img.deployed, img.desired)
	}
}

// TestBuildDiff_Converged_Empty verifies that a fully converged service (and
// a fully converged set) produces no diff rows, so `accorda diff` is silent
// when Git and the deployed state agree.
func TestBuildDiff_Converged_Empty(t *testing.T) {
	svc := state.Service{Image: "api:1"}
	deployed := &state.DesiredState{Services: map[string]state.Service{"api": svc}}
	desired := &state.DesiredState{Services: map[string]state.Service{"api": svc}}

	if roots := buildDiff(deployed, desired); len(roots) != 0 {
		t.Fatalf("buildDiff(converged) = %v, want empty", roots)
	}
}

// TestBuildDiff_NewService verifies a service present only in the desired
// state surfaces as a diff with every field new.
func TestBuildDiff_NewService(t *testing.T) {
	desired := &state.DesiredState{Services: map[string]state.Service{
		"worker": {Image: "worker:1"},
	}}
	roots := buildDiff(nil, desired)
	if len(roots) != 1 || roots[0].label != "worker" {
		t.Fatalf("roots = %v, want worker", roots)
	}
	if img := findDiffNode(roots[0], "image"); img == nil || img.desired != "worker:1" {
		t.Errorf("worker image node = %+v, want desired worker:1", img)
	}
}

// TestBuildDiff_RemovedService verifies that a service present only in the
// deployed state (removed from desired) surfaces as a diff with the deployed
// image only.
func TestBuildDiff_RemovedService(t *testing.T) {
	deployed := &state.DesiredState{Services: map[string]state.Service{
		"orphan": {Image: "old:1"},
	}}
	roots := buildDiff(deployed, &state.DesiredState{})
	if len(roots) != 1 || roots[0].label != "orphan" {
		t.Fatalf("roots = %v, want orphan", roots)
	}
	if img := findDiffNode(roots[0], "image"); img == nil || img.deployed != "old:1" {
		t.Errorf("image field node = %+v, want deployed old:1", img)
	}
}

// TestBuildDiff_EnvKey verifies that a differing environment variable produces
// a nested node keyed by the variable name with deployed/desired values,
// matching the §11 diff example.
func TestBuildDiff_EnvKey(t *testing.T) {
	deployed := &state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "api:1", Env: map[string]string{"LOG_LEVEL": "info"}},
	}}
	desired := &state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "api:1", Env: map[string]string{"LOG_LEVEL": "warning"}},
	}}

	roots := buildDiff(deployed, desired)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	env := findDiffNode(roots[0], "environment")
	if env == nil {
		t.Fatal("expected environment field node")
	}
	level := findDiffNode(*env, "LOG_LEVEL")
	if level == nil {
		t.Fatal("expected LOG_LEVEL child node")
	}
	if level.deployed != "info" || level.desired != "warning" {
		t.Errorf("LOG_LEVEL deployed/desired = %q/%q, want info/warning", level.deployed, level.desired)
	}
}

// TestWriteDiff_Format verifies the rendered output matches the YAML-like
// tree shape from docs/ACCORDA.md §11 (deployed/desired pairs indented under
// the service and field).
func TestWriteDiff_Format(t *testing.T) {
	roots := []diffNode{{
		label: "api",
		children: []diffNode{{
			label:    "image",
			hasValue: true,
			deployed: "ghcr.io/acme/api:2.4.0",
			desired:  "ghcr.io/acme/api:2.4.1",
		}},
	}}
	var buf bytes.Buffer
	writeDiff(&buf, roots)
	out := buf.String()
	for _, want := range []string{
		"api\n",
		"  image\n",
		"    deployed: ghcr.io/acme/api:2.4.0\n",
		"    desired:  ghcr.io/acme/api:2.4.1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

// findDiffNode returns the child node of parent with the given label, or nil.
func findDiffNode(parent diffNode, label string) *diffNode {
	for i := range parent.children {
		if parent.children[i].label == label {
			return &parent.children[i]
		}
	}
	return nil
}

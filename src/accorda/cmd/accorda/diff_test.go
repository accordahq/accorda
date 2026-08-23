package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"accorda/internal/core/history"
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
// a nested node keyed by the variable name without exposing either value.
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
	if level.deployed != "<redacted>" || level.desired != "<redacted>" {
		t.Errorf("LOG_LEVEL deployed/desired = %q/%q, want redacted values", level.deployed, level.desired)
	}
	var output bytes.Buffer
	writeDiff(&output, roots)
	if strings.Contains(output.String(), "info") || strings.Contains(output.String(), "warning") {
		t.Fatalf("diff output leaked environment values: %q", output.String())
	}
}

func TestBuildDiff_EnvAdditionShowsPresenceWithoutValue(t *testing.T) {
	desired := &state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "api:1", Env: map[string]string{"API_TOKEN": "token-super-secret"}},
	}}
	roots := buildDiff(&state.DesiredState{Services: map[string]state.Service{
		"api": {Image: "api:1"},
	}}, desired)
	var output bytes.Buffer
	writeDiff(&output, roots)
	got := output.String()
	if !strings.Contains(got, "deployed: <unset>") || !strings.Contains(got, "desired:  <redacted>") {
		t.Fatalf("diff output = %q, want unset-to-redacted transition", got)
	}
	if strings.Contains(got, "token-super-secret") {
		t.Fatalf("diff output leaked secret: %q", got)
	}
}

func TestDiffSensitiveKV_UnchangedAndRemoval(t *testing.T) {
	if got := diffSensitiveKV("environment",
		map[string]string{"TOKEN": "same"}, map[string]string{"TOKEN": "same"}); got != nil {
		t.Fatalf("diffSensitiveKV(unchanged) = %v, want nil", got)
	}
	got := diffSensitiveKV("environment", map[string]string{"TOKEN": "secret"}, nil)
	if len(got) != 1 || len(got[0].children) != 1 {
		t.Fatalf("diffSensitiveKV(removal) = %v, want one child", got)
	}
	child := got[0].children[0]
	if child.deployed != "<redacted>" || child.desired != "<unset>" {
		t.Fatalf("removal values = %q/%q, want redacted/unset", child.deployed, child.desired)
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

// TestHealthcheckString_IncludesStartPeriod verifies that a start_period-only
// change renders distinct deployed/desired values, so the diff output explains
// why the healthcheck node exists (review finding).
func TestHealthcheckString_IncludesStartPeriod(t *testing.T) {
	base := state.Healthcheck{Test: []string{"CMD", "true"}, Interval: time.Second, Timeout: time.Second, Retries: 3}
	withStart := base
	withStart.StartPeriod = 5 * time.Second

	deployed := healthcheckString(base)
	desired := healthcheckString(withStart)
	if deployed == desired {
		t.Fatalf("healthcheckString = %q for both, want start_period to differ", deployed)
	}
	if !strings.Contains(desired, "start_period=5s") {
		t.Errorf("desired healthcheckString = %q, want it to include start_period=5s", desired)
	}
}

// TestHealthcheckString_DisabledDistinct verifies a disabled healthcheck
// renders distinctly from an absent one.
func TestHealthcheckString_DisabledDistinct(t *testing.T) {
	disabled := state.Healthcheck{Disable: true}
	absent := state.Healthcheck{}
	if got := healthcheckString(disabled); got != "disabled" {
		t.Errorf("disabled healthcheckString = %q, want \"disabled\"", got)
	}
	if got := healthcheckString(absent); got != "" {
		t.Errorf("absent healthcheckString = %q, want empty", got)
	}
}

// errStore is a history.Store that fails every read, used to exercise the
// deployed-state read-error warning path in deployedAtCommit.
type errStore struct{}

func (errStore) Append(context.Context, history.Receipt) error { return nil }
func (errStore) List(context.Context) ([]history.Receipt, error) {
	return nil, errors.New("journal unreadable")
}

// TestDeployedAtCommit_ReportsStoreError verifies that a history read error is
// surfaced to the warning writer (so an operator can distinguish "no prior
// healthy deployment" from "history could not be read"), mirroring
// `accorda sync`'s previousFromHistory.
func TestDeployedAtCommit_ReportsStoreError(t *testing.T) {
	var warn bytes.Buffer
	got := deployedAtCommit(context.Background(), &statusTestSource{}, errStore{}, &warn)
	if got != nil {
		t.Fatalf("deployedAtCommit = %v, want nil on store error", got)
	}
	if !strings.Contains(warn.String(), "could not read deployment history") {
		t.Errorf("warning = %q, want it to mention the history read failure", warn.String())
	}
}

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"accorda/internal/core/history"
)

// TestInspectHealthLabel verifies the §11 inspect health column.
func TestInspectHealthLabel(t *testing.T) {
	cases := []struct {
		name string
		o    history.Outcome
		want string
	}{
		{"healthy", history.OutcomeHealthy, "passed"},
		{"failed", history.OutcomeFailed, "failed"},
		{"rolled_back", history.OutcomeRolledBack, "failed"},
		{"unknown", history.Outcome("weird"), "weird"},
	}
	for _, c := range cases {
		if got := inspectHealthLabel(c.o); got != c.want {
			t.Errorf("inspectHealthLabel(%q) = %q, want %q", c.o, got, c.want)
		}
	}
}

// TestFindReceipt_MostRecent verifies an empty commit argument selects the
// most recent receipt.
func TestFindReceipt_MostRecent(t *testing.T) {
	receipts := []history.Receipt{
		{Commit: "old"},
		{Commit: "new"},
	}
	idx, err := findReceipt(receipts, "")
	if err != nil {
		t.Fatalf("findReceipt: %v", err)
	}
	if idx != 1 {
		t.Errorf("idx = %d, want 1 (most recent)", idx)
	}
}

// TestFindReceipt_Prefix verifies a short SHA prefix matches a full SHA.
func TestFindReceipt_Prefix(t *testing.T) {
	receipts := []history.Receipt{
		{Commit: "d71b2e4abcdef0123456789"},
		{Commit: "a01fd9200000"},
	}
	idx, err := findReceipt(receipts, "d71b2e4")
	if err != nil {
		t.Fatalf("findReceipt: %v", err)
	}
	if idx != 0 {
		t.Errorf("idx = %d, want 0", idx)
	}
}

// TestFindReceipt_NotFound verifies an unknown commit is reported as an error.
func TestFindReceipt_NotFound(t *testing.T) {
	receipts := []history.Receipt{{Commit: "d71b2e4"}}
	if _, err := findReceipt(receipts, "zzzzzzz"); err == nil {
		t.Fatal("findReceipt(unknown): want error, got nil")
	}
}

// TestFindReceipt_Empty verifies an empty journal is reported as an error.
func TestFindReceipt_Empty(t *testing.T) {
	if _, err := findReceipt(nil, ""); err == nil {
		t.Fatal("findReceipt(empty): want error, got nil")
	}
}

// TestFindReceipt_PrefixMostRecent verifies that when a commit was deployed
// more than once (a rollback that restored a prior commit records a second
// receipt for the same SHA), a short prefix resolves to the most recent
// cycle, not the oldest. A user copies the 7-char prefix from the history
// table and expects the latest deployment for that commit.
func TestFindReceipt_PrefixMostRecent(t *testing.T) {
	receipts := []history.Receipt{
		{Commit: "d71b2e4abc", Result: history.OutcomeHealthy},
		{Commit: "a01fd92000", Result: history.OutcomeFailed},
		{Commit: "d71b2e4abc", Result: history.OutcomeRolledBack}, // restored prior commit
	}
	idx, err := findReceipt(receipts, "d71b2e4")
	if err != nil {
		t.Fatalf("findReceipt: %v", err)
	}
	if idx != 2 {
		t.Errorf("idx = %d, want 2 (most recent match)", idx)
	}
}

// TestPreviousHealthyBefore verifies it returns the most recent healthy
// receipt strictly before the given index.
func TestPreviousHealthyBefore(t *testing.T) {
	receipts := []history.Receipt{
		{Commit: "r1", Result: history.OutcomeHealthy, Services: map[string]history.ServiceReceipt{
			"api": {Digest: "sha256:aaa"},
		}},
		{Commit: "r2", Result: history.OutcomeFailed},
		{Commit: "r3", Result: history.OutcomeHealthy, Services: map[string]history.ServiceReceipt{
			"api": {Digest: "sha256:bbb"},
		}},
	}
	prev := previousHealthyBefore(receipts, 2)
	if prev == nil || prev.Commit != "r1" {
		t.Fatalf("previousHealthyBefore(2) = %+v, want r1", prev)
	}
	if d := previousDigest(prev, "api"); d != "sha256:aaa" {
		t.Errorf("previousDigest = %q, want sha256:aaa", d)
	}
}

// TestPreviousHealthyBefore_None verifies no prior healthy deployment yields
// nil and an empty previous digest.
func TestPreviousHealthyBefore_None(t *testing.T) {
	receipts := []history.Receipt{{Commit: "r1", Result: history.OutcomeFailed}}
	prev := previousHealthyBefore(receipts, 0)
	if prev != nil {
		t.Fatalf("previousHealthyBefore(0) = %+v, want nil", prev)
	}
	if d := previousDigest(nil, "api"); d != "" {
		t.Errorf("previousDigest(nil) = %q, want empty", d)
	}
}

// TestCollectInspect_Changed verifies a changed service shows previous and
// deployed digests, recreated=yes, and the health result.
func TestCollectInspect_Changed(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{
		{Commit: "r1", Result: history.OutcomeHealthy, Services: map[string]history.ServiceReceipt{
			"api": {Digest: "sha256:aaa"},
		}},
		{
			Commit:  "r2",
			Result:  history.OutcomeHealthy,
			Changes: []string{"api"},
			Services: map[string]history.ServiceReceipt{
				"api": {Digest: "sha256:bbb"},
			},
		},
	}}
	services, err := collectInspect(context.Background(), store, "r2")
	if err != nil {
		t.Fatalf("collectInspect: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("services = %d, want 1", len(services))
	}
	s := services[0]
	if s.name != "api" {
		t.Errorf("name = %q, want api", s.name)
	}
	if s.previousDigest != "sha256:aaa" {
		t.Errorf("previousDigest = %q, want sha256:aaa", s.previousDigest)
	}
	if s.deployedDigest != "sha256:bbb" {
		t.Errorf("deployedDigest = %q, want sha256:bbb", s.deployedDigest)
	}
	if !s.recreated {
		t.Errorf("recreated = false, want true")
	}
	if s.health != "passed" {
		t.Errorf("health = %q, want passed", s.health)
	}
	if s.unchanged {
		t.Errorf("unchanged = true, want false")
	}
}

// TestCollectInspect_Unchanged verifies a service not in Changes prints
// unchanged and is not flagged recreated.
func TestCollectInspect_Unchanged(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{
		{Commit: "r1", Result: history.OutcomeHealthy, Changes: []string{"api"}, Services: map[string]history.ServiceReceipt{
			"api":    {Digest: "sha256:aaa"},
			"worker": {Digest: "sha256:ccc"},
		}},
		{
			Commit:  "r2",
			Result:  history.OutcomeHealthy,
			Changes: []string{"api"},
			Services: map[string]history.ServiceReceipt{
				"api":    {Digest: "sha256:bbb"},
				"worker": {Digest: "sha256:ccc"},
			},
		},
	}}
	services, err := collectInspect(context.Background(), store, "r2")
	if err != nil {
		t.Fatalf("collectInspect: %v", err)
	}
	want := map[string]bool{"api": false, "worker": true}
	for _, s := range services {
		if s.unchanged != want[s.name] {
			t.Errorf("%s unchanged = %v, want %v", s.name, s.unchanged, want[s.name])
		}
	}
}

// TestCollectInspect_Failed verifies a failed receipt reports health=failed.
func TestCollectInspect_Failed(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{
		{Commit: "r1", Result: history.OutcomeFailed, Changes: []string{"api"},
			Services: map[string]history.ServiceReceipt{"api": {Digest: ""}}},
	}}
	services, err := collectInspect(context.Background(), store, "r1")
	if err != nil {
		t.Fatalf("collectInspect: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("services = %d, want 1", len(services))
	}
	if services[0].health != "failed" {
		t.Errorf("health = %q, want failed", services[0].health)
	}
}

// TestCollectInspect_MostRecent verifies an empty commit inspects the most
// recent receipt.
func TestCollectInspect_MostRecent(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{
		{Commit: "r1", Result: history.OutcomeHealthy, Services: map[string]history.ServiceReceipt{
			"api": {Digest: "sha256:aaa"},
		}},
		{Commit: "r2", Result: history.OutcomeHealthy, Changes: []string{"api"}, Services: map[string]history.ServiceReceipt{
			"api": {Digest: "sha256:bbb"},
		}},
	}}
	services, err := collectInspect(context.Background(), store, "")
	if err != nil {
		t.Fatalf("collectInspect: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("services = %d, want 1", len(services))
	}
	if services[0].deployedDigest != "sha256:bbb" {
		t.Errorf("deployedDigest = %q, want sha256:bbb (most recent)", services[0].deployedDigest)
	}
}

// TestCollectInspect_NilStore verifies a nil store is reported as an error.
func TestCollectInspect_NilStore(t *testing.T) {
	if _, err := collectInspect(context.Background(), nil, ""); err == nil {
		t.Fatal("collectInspect(nil): want error, got nil")
	}
}

// TestCollectInspect_UnknownCommit verifies an unknown commit is an error.
func TestCollectInspect_UnknownCommit(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{{Commit: "r1"}}}
	if _, err := collectInspect(context.Background(), store, "zzzz"); err == nil {
		t.Fatal("collectInspect(unknown): want error, got nil")
	}
}

// TestWriteInspect_Format verifies the rendered output matches the §11 inspect
// example: a changed service prints the four detail rows, an unchanged
// service prints a single "unchanged" line.
func TestWriteInspect_Format(t *testing.T) {
	services := []inspectService{
		{
			name:           "api",
			previousDigest: "sha256:abc",
			deployedDigest: "sha256:def",
			recreated:      true,
			health:         "passed",
		},
		{name: "postgres", unchanged: true},
	}
	var buf bytes.Buffer
	writeInspect(&buf, services)
	out := buf.String()
	for _, want := range []string{
		"api\n",
		"  previous digest    sha256:abc\n",
		"  deployed digest    sha256:def\n",
		"  recreated          yes\n",
		"  health             passed\n",
		"postgres\n",
		"  unchanged\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

// TestWriteInspect_DashDigest verifies an empty digest renders a dash.
func TestWriteInspect_DashDigest(t *testing.T) {
	services := []inspectService{{
		name:           "api",
		previousDigest: "",
		deployedDigest: "sha256:def",
		recreated:      true,
		health:         "failed",
	}}
	var buf bytes.Buffer
	writeInspect(&buf, services)
	out := buf.String()
	if !strings.Contains(out, "  previous digest    -\n") {
		t.Errorf("output missing dash previous digest; got:\n%s", out)
	}
}

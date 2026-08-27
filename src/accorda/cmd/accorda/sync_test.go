package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"accorda/internal/core/events"
	"accorda/internal/core/reconcile"
	"accorda/internal/core/state"
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

func TestSyncProgressWriter_PrintsNonTerminalTransitions(t *testing.T) {
	var out bytes.Buffer
	write := projectSyncProgressWriter(&out, "")
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
	write := projectSyncProgressWriter(&out, "")
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

// TestWriteSyncResultWithPrefix verifies the shared cycle-outcome renderer
// handles the failed, rolled-back, and healthy paths with and without a
// project-name prefix, so the single-project and ensemble output cannot drift.
func TestWriteSyncResultWithPrefix(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		result  *reconcile.Result
		wantOut string
		wantErr bool
	}{
		{
			name:    "synced no prefix",
			prefix:  "",
			result:  &reconcile.Result{Phase: reconcile.PhaseSynced, Comparison: state.Comparison{Result: state.ResultSynced}},
			wantOut: "sync: SYNCED\nsync=SYNCED reasons=0 services=0\n",
		},
		{
			name:    "synced with prefix",
			prefix:  "api: ",
			result:  &reconcile.Result{Phase: reconcile.PhaseSynced, Comparison: state.Comparison{Result: state.ResultSynced}},
			wantOut: "api: sync: SYNCED\napi: sync=SYNCED reasons=0 services=0\n",
		},
		{
			name:    "failed no prefix",
			prefix:  "",
			result:  &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("boom")},
			wantOut: "sync: FAILED\n",
			wantErr: true,
		},
		{
			name:    "failed with prefix",
			prefix:  "api: ",
			result:  &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("boom")},
			wantOut: "api: sync: FAILED\n",
			wantErr: true,
		},
		{
			name:    "rollback with prefix",
			prefix:  "api: ",
			result:  &reconcile.Result{Phase: reconcile.PhaseFailed, RolledBack: true, RolledBackTo: "abc123", Err: errors.New("boom")},
			wantOut: "api: sync: FAILED\napi: rollback: restored to commit abc123\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := writeSyncResultWithPrefix(&out, tc.prefix, tc.result)
			if out.String() != tc.wantOut {
				t.Errorf("output = %q, want %q", out.String(), tc.wantOut)
			}
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestWriteEnsembleResults_PropagatesFirstFailure verifies a one-shot
// ensemble sync aggregates the first failed member's error into the returned
// error, so the exit code is non-zero like the single-project path. It drives
// the actual writeEnsembleResults aggregation end-to-end with mixed
// successful and failed members.
func TestWriteEnsembleResults_PropagatesFirstFailure(t *testing.T) {
	cases := []struct {
		name    string
		results []reconcile.MemberResult
		wantErr string
		wantOut []string
	}{
		{
			name: "all synced",
			results: []reconcile.MemberResult{
				{Name: "api", Result: &reconcile.Result{Phase: reconcile.PhaseSynced, Comparison: state.Comparison{Result: state.ResultSynced}}},
				{Name: "worker", Result: &reconcile.Result{Phase: reconcile.PhaseSynced, Comparison: state.Comparison{Result: state.ResultSynced}}},
			},
			wantErr: "",
			wantOut: []string{"api: sync: SYNCED", "worker: sync: SYNCED"},
		},
		{
			name: "first member fails",
			results: []reconcile.MemberResult{
				{Name: "api", Result: &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("fetch boom")}},
				{Name: "worker", Result: &reconcile.Result{Phase: reconcile.PhaseSynced, Comparison: state.Comparison{Result: state.ResultSynced}}},
			},
			wantErr: "sync api: reconciliation failed: fetch boom",
			wantOut: []string{"api: sync: FAILED", "worker: sync: SYNCED"},
		},
		{
			name: "second member fails",
			results: []reconcile.MemberResult{
				{Name: "api", Result: &reconcile.Result{Phase: reconcile.PhaseSynced, Comparison: state.Comparison{Result: state.ResultSynced}}},
				{Name: "worker", Result: &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("apply boom")}},
			},
			wantErr: "sync worker: reconciliation failed: apply boom",
			wantOut: []string{"api: sync: SYNCED", "worker: sync: FAILED"},
		},
		{
			name: "both fail returns first error",
			results: []reconcile.MemberResult{
				{Name: "api", Result: &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("first")}},
				{Name: "worker", Result: &reconcile.Result{Phase: reconcile.PhaseFailed, Err: errors.New("second")}},
			},
			wantErr: "sync api: reconciliation failed: first",
			wantOut: []string{"api: sync: FAILED", "worker: sync: FAILED"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := newSyncCmd()
			cmd.SetOut(&out)
			err := writeEnsembleResults(cmd, tc.results)
			assertOutputContains(t, out.String(), tc.wantOut)
			assertErrorContains(t, err, tc.wantErr)
		})
	}
}

// assertOutputContains fails the test unless every expected substring is
// present in got.
func assertOutputContains(t *testing.T, got string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
}

// assertErrorContains fails the test unless err matches want: when want is
// empty the error must be nil, otherwise err must be non-nil and contain want.
func assertErrorContains(t *testing.T, err error, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Errorf("error = %v, want it to contain %q", err, wantErr)
	}
}

package reconcile

import (
	"context"
	"testing"
)

// TestNewProject_RequiresTargets verifies the project runner rejects an empty
// or partially-built target list at construction time (issue #103).
func TestNewProject_RequiresTargets(t *testing.T) {
	if _, err := NewProject("", nil); err == nil {
		t.Fatal("NewProject(nil) succeeded, want error")
	}
	if _, err := NewProject("", []TargetMember{}); err == nil {
		t.Fatal("NewProject(empty) succeeded, want error")
	}
	if _, err := NewProject("", []TargetMember{{Target: "compose\x00compose.yaml"}}); err == nil {
		t.Fatal("NewProject(nil reconciler) succeeded, want error")
	}
	if _, err := NewProject("", []TargetMember{{Reconciler: New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)}}); err == nil {
		t.Fatal("NewProject(empty target identity) succeeded, want error")
	}
}

// TestProject_ReconcileRunsAllTargets verifies that one cycle drives every
// target and returns one result per target, so a single source revision
// reconciles all of a project's targets in one pass (issue #103).
func TestProject_ReconcileRunsAllTargets(t *testing.T) {
	rA := New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)
	rB := New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)
	project, err := NewProject("", []TargetMember{
		{Target: "compose\x00docker-compose.yml", Reconciler: rA},
		{Target: "compose\x00qa/docker-compose.yml", Reconciler: rB},
	})
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}

	results := project.Reconcile(context.Background())
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	for _, mr := range results {
		if mr.Result == nil {
			t.Fatalf("target %q returned nil result", mr.Name)
		}
		if mr.Result.Phase != PhaseSynced {
			t.Errorf("target %q phase = %s, want %s", mr.Name, mr.Result.Phase, PhaseSynced)
		}
	}
}

// TestProject_Run_RejectsNonPositiveInterval verifies the continuous loop
// rejects a non-positive interval (issue #103).
func TestProject_Run_RejectsNonPositiveInterval(t *testing.T) {
	project, err := NewProject("", []TargetMember{
		{Target: "compose\x00docker-compose.yml", Reconciler: New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)},
	})
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	if err := project.Run(context.Background(), 0, nil); err == nil {
		t.Fatal("Run(interval=0) succeeded, want error")
	}
}

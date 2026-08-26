package plan

import (
	"strings"
	"testing"
	"time"

	"accorda/internal/core/state"
)

func TestPlan_New_AddAction_Chain(t *testing.T) {
	p := New("dep_1", "production", "abc123", time.Unix(1700000000, 0))
	p.AddAction(Action{Kind: ActionPull, Service: "api", Image: "api:2"}).
		AddAction(Action{Kind: ActionRecreate, Service: "api", Image: "api:2"})

	if len(p.Actions) != 2 {
		t.Fatalf("Actions len = %d, want 2", len(p.Actions))
	}
	if p.Actions[0].Kind != ActionPull {
		t.Errorf("Actions[0].Kind = %q, want %q", p.Actions[0].Kind, ActionPull)
	}
	if p.Actions[1].Kind != ActionRecreate {
		t.Errorf("Actions[1].Kind = %q, want %q", p.Actions[1].Kind, ActionRecreate)
	}
}

func TestPlan_Validate(t *testing.T) {
	p := New("dep_1", "production", "abc123", time.Unix(0, 0))
	if err := p.Validate(); err != nil {
		t.Fatalf("minimal valid plan: unexpected error: %v", err)
	}

	noID := New("", "production", "abc123", time.Unix(0, 0))
	if err := noID.Validate(); err == nil {
		t.Fatal("missing deployment id: expected error, got nil")
	}

	noCommit := New("dep_1", "production", "", time.Unix(0, 0))
	if err := noCommit.Validate(); err == nil {
		t.Fatal("missing commit: expected error, got nil")
	}

	badKind := New("dep_1", "production", "abc123", time.Unix(0, 0))
	badKind.AddAction(Action{Kind: ActionKind("bogus"), Service: "api"})
	if err := badKind.Validate(); err == nil {
		t.Fatal("unknown action kind: expected error, got nil")
	}

	noService := New("dep_1", "production", "abc123", time.Unix(0, 0))
	noService.AddAction(Action{Kind: ActionPull, Service: ""})
	if err := noService.Validate(); err == nil {
		t.Fatal("action without service: expected error, got nil")
	}
}

func TestPlan_Clone_IsDeepCopy(t *testing.T) {
	p := New("dep_1", "production", "abc123", time.Unix(0, 0))
	p.AddAction(Action{Kind: ActionPull, Service: "api"})
	p.Security = &Security{Vulnerabilities: VulnerabilityCounts{High: 2}}
	p.Policy = &Policy{Status: "approval_required", ApprovalsRequired: 2}

	clone := p.Clone()
	clone.Actions[0].Service = "worker"
	clone.Security.Vulnerabilities.High = 99
	clone.Policy.Status = "approved"

	if p.Actions[0].Service != "api" {
		t.Errorf("original action mutated by clone: got %q, want %q", p.Actions[0].Service, "api")
	}
	if p.Security.Vulnerabilities.High != 2 {
		t.Errorf("original security mutated by clone: got %d, want 2", p.Security.Vulnerabilities.High)
	}
	if p.Policy.Status != "approval_required" {
		t.Errorf("original policy mutated by clone: got %q, want %q", p.Policy.Status, "approval_required")
	}
}

func TestPlan_Clone_NilSafe(t *testing.T) {
	var p *Plan
	if c := p.Clone(); c != nil {
		t.Errorf("Clone(nil) = %v, want nil", c)
	}
}

func TestDriftActions_CreateNewService(t *testing.T) {
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"api": {Image: "api:2"},
		},
	}
	actions := DriftActions(desired, nil, &state.RuntimeState{})
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(actions), actions)
	}
	if actions[0].Kind != ActionCreate {
		t.Errorf("kind = %q, want %q", actions[0].Kind, ActionCreate)
	}
	if actions[0].Service != "api" {
		t.Errorf("service = %q, want %q", actions[0].Service, "api")
	}
}

func TestDriftActions_StartStoppedDeployedService(t *testing.T) {
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"api": {Image: "api:2"},
		},
	}
	deployed := &state.DeployedState{
		DeploymentID: "dep_1",
		Commit:       "abc",
		Services:     map[string]state.Service{"api": {Image: "api:2"}},
	}
	// api is deployed but not running (stopped).
	runtime := &state.RuntimeState{}
	actions := DriftActions(desired, deployed, runtime)
	found := false
	for _, a := range actions {
		if a.Service == "api" && a.Kind == ActionStart {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a Start action for stopped deployed service, got %v", actions)
	}
}

func TestDriftActions_RecreateOnConfigChange(t *testing.T) {
	// A service whose image is unchanged but whose configuration hash differs
	// (e.g. command changed) must be recreated, not treated as a noop
	// (docs/ACCORDA.md §10).
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"api": {Image: "api:1", Command: []string{"./api", "--port", "8080"}},
		},
	}
	deployed := &state.DeployedState{
		DeploymentID: "dep_1",
		Commit:       "abc",
		Services:     map[string]state.Service{"api": {Image: "api:1", Command: []string{"./api", "--port", "9090"}}},
	}
	runtime := &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"api": {Status: "running", Image: "api:1"},
		},
	}
	actions := DriftActions(desired, deployed, runtime)
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(actions), actions)
	}
	if actions[0].Kind != ActionRecreate {
		t.Errorf("kind = %q, want %q", actions[0].Kind, ActionRecreate)
	}
	if actions[0].Service != "api" {
		t.Errorf("service = %q, want %q", actions[0].Service, "api")
	}
}

func TestDriftActions_RecreateOnImageChange(t *testing.T) {
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"api": {Image: "api:2"},
		},
	}
	runtime := &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"api": {Status: "running", Image: "api:1"},
		},
	}
	actions := DriftActions(desired, nil, runtime)
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(actions), actions)
	}
	if actions[0].Kind != ActionRecreate {
		t.Errorf("kind = %q, want %q", actions[0].Kind, ActionRecreate)
	}
	if actions[0].From != "api:1" || actions[0].To != "api:2" {
		t.Errorf("from/to = %q/%q, want api:1/api:2", actions[0].From, actions[0].To)
	}
}

func TestDriftActions_StoppedService_IsStart(t *testing.T) {
	// A service present at runtime but with a non-running status (e.g.
	// "exited" after `docker compose stop api`) is drift, not convergence.
	// It must surface as a Start action, mirroring compareService's status
	// check (docs/ACCORDA.md §5.3).
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"api": {Image: "api:2"},
		},
	}
	runtime := &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"api": {Status: "exited", Image: "api:2"},
		},
	}
	actions := DriftActions(desired, nil, runtime)
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(actions), actions)
	}
	if actions[0].Kind != ActionStart {
		t.Errorf("kind = %q, want %q", actions[0].Kind, ActionStart)
	}
	if actions[0].Service != "api" {
		t.Errorf("service = %q, want %q", actions[0].Service, "api")
	}
}

func TestDriftActions_StoppedWithChangedImage_IsRecreate(t *testing.T) {
	// A service that is both stopped and has a changed image must be
	// recreated (not started), so the image change is not silently dropped.
	// The image check must precede the status check.
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"api": {Image: "api:2"},
		},
	}
	runtime := &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"api": {Status: "exited", Image: "api:1"},
		},
	}
	actions := DriftActions(desired, nil, runtime)
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(actions), actions)
	}
	if actions[0].Kind != ActionRecreate {
		t.Errorf("kind = %q, want %q", actions[0].Kind, ActionRecreate)
	}
	if actions[0].From != "api:1" || actions[0].To != "api:2" {
		t.Errorf("from/to = %q/%q, want api:1/api:2", actions[0].From, actions[0].To)
	}
}

func TestDriftActions_NoopWhenConverged(t *testing.T) {
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"api": {Image: "api:2"},
		},
	}
	runtime := &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"api": {Status: "running", Image: "api:2"},
		},
	}
	actions := DriftActions(desired, nil, runtime)
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(actions), actions)
	}
	if actions[0].Kind != ActionNoop {
		t.Errorf("kind = %q, want %q", actions[0].Kind, ActionNoop)
	}
}

func TestDriftActions_RemoveOrphan(t *testing.T) {
	desired := &state.DesiredState{
		Commit:   "abc",
		Services: map[string]state.Service{},
	}
	runtime := &state.RuntimeState{
		Services: map[string]state.RuntimeService{
			"orphan": {Status: "running", Image: "old:1"},
		},
	}
	actions := DriftActions(desired, nil, runtime)
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1: %v", len(actions), actions)
	}
	if actions[0].Kind != ActionRemove {
		t.Errorf("kind = %q, want %q", actions[0].Kind, ActionRemove)
	}
	if actions[0].Service != "orphan" {
		t.Errorf("service = %q, want %q", actions[0].Service, "orphan")
	}
}

func TestDriftActions_NilSafe(t *testing.T) {
	actions := DriftActions(nil, nil, nil)
	if len(actions) != 0 {
		t.Fatalf("actions len = %d, want 0 for nil states", len(actions))
	}
}

func TestDriftActions_DeterministicOrder(t *testing.T) {
	// Multiple services must produce actions in sorted service-name order so
	// a plan is stable regardless of Go's randomized map iteration order
	// (docs/DECISIONS.md #7). The raw slice order is asserted, not re-sorted.
	desired := &state.DesiredState{
		Commit: "abc",
		Services: map[string]state.Service{
			"zebra":  {Image: "zebra:1"},
			"alpha":  {Image: "alpha:1"},
			"middle": {Image: "middle:1"},
		},
	}
	actions := DriftActions(desired, nil, &state.RuntimeState{})
	if len(actions) != 3 {
		t.Fatalf("actions len = %d, want 3: %v", len(actions), actions)
	}
	want := []string{"alpha", "middle", "zebra"}
	for i, name := range want {
		if actions[i].Service != name {
			t.Errorf("actions[%d].Service = %q, want %q", i, actions[i].Service, name)
		}
	}
}

func TestPlan_Changed(t *testing.T) {
	noop := New("dep_1", "production", "abc", time.Unix(0, 0))
	noop.AddAction(NoopFor("api"))
	if noop.Changed() {
		t.Error("Changed() = true for a plan with only Noop actions")
	}

	empty := New("dep_1", "production", "abc", time.Unix(0, 0))
	if empty.Changed() {
		t.Error("Changed() = true for a plan with no actions")
	}

	changed := New("dep_1", "production", "abc", time.Unix(0, 0))
	changed.AddAction(Action{Kind: ActionRecreate, Service: "api"})
	if !changed.Changed() {
		t.Error("Changed() = false for a plan with a Recreate action")
	}

	var nilPlan *Plan
	if nilPlan.Changed() {
		t.Error("Changed() = true for a nil plan")
	}
}

func TestPlan_String(t *testing.T) {
	p := New("dep_1", "production", "abc", time.Unix(0, 0))
	p.AddAction(Action{Kind: ActionRecreate, Service: "api"}).
		AddAction(NoopFor("redis"))

	got := p.String()
	if !strings.Contains(got, "api") || !strings.Contains(got, "CHANGED") {
		t.Errorf("String() = %q, want it to mention api as CHANGED", got)
	}
	if !strings.Contains(got, "redis") || !strings.Contains(got, "UNCHANGED") {
		t.Errorf("String() = %q, want it to mention redis as UNCHANGED", got)
	}

	var nilPlan *Plan
	if got := nilPlan.String(); !strings.Contains(got, "<nil>") {
		t.Errorf("String() on nil plan = %q, want a nil marker", got)
	}
}

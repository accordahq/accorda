package state

import (
	"sort"
	"testing"
	"time"
)

// helper builders keep the comparison tests readable and ensure each state is
// independently constructed so a bug in one builder cannot mask a bug in
// another.
func desired(commit string, svcs map[string]Service) *DesiredState {
	return &DesiredState{
		Repository: "acme/infra",
		Branch:     "production",
		Commit:     commit,
		CommitTime: time.Unix(1700000000, 0),
		Services:   svcs,
	}
}

func deployed(id, commit string, svcs map[string]Service) *DeployedState {
	return &DeployedState{
		DeploymentID: id,
		Commit:       commit,
		DeployedAt:   time.Unix(1700000001, 0),
		Services:     svcs,
	}
}

func runtime(svcs map[string]RuntimeService) *RuntimeState {
	return &RuntimeState{Services: svcs}
}

func svc(image string) Service { return Service{Image: image} }

func rsvc(image, status string) RuntimeService {
	return RuntimeService{Status: status, Image: image}
}

func TestCompare_AllNil_IsSynced(t *testing.T) {
	cmp := Compare(nil, nil, nil)
	if cmp.Result != ResultSynced {
		t.Fatalf("Compare(nil,nil,nil) = %s, want %s", cmp.Result, ResultSynced)
	}
	if len(cmp.Reasons) != 0 {
		t.Fatalf("expected no reasons, got %v", cmp.Reasons)
	}
}

func TestCompare_AllConverged_IsSynced(t *testing.T) {
	svcs := map[string]Service{"api": svc("ghcr.io/acme/api:2.4.1")}
	cmp := Compare(
		desired("a84fd21", svcs),
		deployed("dep_1", "a84fd21", svcs),
		runtime(map[string]RuntimeService{"api": rsvc("ghcr.io/acme/api:2.4.1", "running")}),
	)
	if cmp.Result != ResultSynced {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultSynced)
	}
	if got := cmp.Services["api"].Result; got != ResultSynced {
		t.Fatalf("api: %s, want %s", got, ResultSynced)
	}
}

func TestCompare_DesiredAheadOfDeployed_IsOutOfSync(t *testing.T) {
	// Git moved to a new commit; Accorda has not deployed it yet.
	cmp := Compare(
		desired("d71b2e4", map[string]Service{"api": svc("api:2.4.2")}),
		deployed("dep_1", "a84fd21", map[string]Service{"api": svc("api:2.4.1")}),
		runtime(map[string]RuntimeService{"api": rsvc("api:2.4.1", "running")}),
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
	if got := cmp.Services["api"].Result; got != ResultOutOfSync {
		t.Fatalf("api: %s, want %s", got, ResultOutOfSync)
	}
	if len(cmp.Reasons) == 0 {
		t.Fatal("expected out-of-sync reasons, got none")
	}
}

func TestCompare_DesiredServiceNotDeployed_IsOutOfSync(t *testing.T) {
	cmp := Compare(
		desired("a84fd21", map[string]Service{"api": svc("api:1"), "worker": svc("worker:1")}),
		deployed("dep_1", "a84fd21", map[string]Service{"api": svc("api:1")}),
		runtime(map[string]RuntimeService{"api": rsvc("api:1", "running")}),
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
	if got := cmp.Services["worker"].Result; got != ResultOutOfSync {
		t.Fatalf("worker: %s, want %s", got, ResultOutOfSync)
	}
}

func TestCompare_DeployedImageDiffersFromDesired_IsOutOfSync(t *testing.T) {
	cmp := Compare(
		desired("a84fd21", map[string]Service{"api": svc("api:2.4.2")}),
		deployed("dep_1", "a84fd21", map[string]Service{"api": svc("api:2.4.1")}),
		runtime(map[string]RuntimeService{"api": rsvc("api:2.4.1", "running")}),
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
	if got := cmp.Services["api"].Result; got != ResultOutOfSync {
		t.Fatalf("api: %s, want %s", got, ResultOutOfSync)
	}
}

func TestCompare_ServiceStoppedManually_IsDrifted(t *testing.T) {
	// Git and Accorda agree; the runtime drifted because api was stopped.
	svcs := map[string]Service{"api": svc("api:1")}
	cmp := Compare(
		desired("a84fd21", svcs),
		deployed("dep_1", "a84fd21", svcs),
		runtime(map[string]RuntimeService{}),
	)
	if cmp.Result != ResultDrifted {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultDrifted)
	}
	if got := cmp.Services["api"].Result; got != ResultDrifted {
		t.Fatalf("api: %s, want %s", got, ResultDrifted)
	}
	if len(cmp.Reasons) == 0 {
		t.Fatal("expected drift reasons, got none")
	}
}

func TestCompare_ServicePresentButStopped_IsDrifted(t *testing.T) {
	// The canonical §5.3 drift case: a Compose target reports a stopped
	// container as present with an unchanged image. Compare must still
	// detect drift via Status.
	svcs := map[string]Service{"api": svc("api:1")}
	cmp := Compare(
		desired("a84fd21", svcs),
		deployed("dep_1", "a84fd21", svcs),
		runtime(map[string]RuntimeService{"api": {Status: "stopped", Image: "api:1"}}),
	)
	if cmp.Result != ResultDrifted {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultDrifted)
	}
	if got := cmp.Services["api"].Result; got != ResultDrifted {
		t.Fatalf("api: %s, want %s", got, ResultDrifted)
	}
	if len(cmp.Reasons) == 0 {
		t.Fatal("expected drift reasons, got none")
	}
}

func TestCompare_RuntimeImageDiffers_IsDrifted(t *testing.T) {
	svcs := map[string]Service{"api": svc("api:1")}
	cmp := Compare(
		desired("a84fd21", svcs),
		deployed("dep_1", "a84fd21", svcs),
		runtime(map[string]RuntimeService{"api": rsvc("api:9.9.9", "running")}),
	)
	if cmp.Result != ResultDrifted {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultDrifted)
	}
	if got := cmp.Services["api"].Result; got != ResultDrifted {
		t.Fatalf("api: %s, want %s", got, ResultDrifted)
	}
}

func TestCompare_OrphanRuntimeService_IsDrifted(t *testing.T) {
	// A service runs at runtime that is neither desired nor deployed.
	cmp := Compare(
		desired("a84fd21", map[string]Service{"api": svc("api:1")}),
		deployed("dep_1", "a84fd21", map[string]Service{"api": svc("api:1")}),
		runtime(map[string]RuntimeService{
			"api":   rsvc("api:1", "running"),
			"rogue": rsvc("rogue:1", "running"),
		}),
	)
	if cmp.Result != ResultDrifted {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultDrifted)
	}
	if got := cmp.Services["rogue"].Result; got != ResultDrifted {
		t.Fatalf("rogue: %s, want %s", got, ResultDrifted)
	}
	if got := cmp.Services["api"].Result; got != ResultSynced {
		t.Fatalf("api should remain synced: %s, want %s", got, ResultSynced)
	}
}

func TestCompare_OutOfSyncTakesPrecedenceOverDrifted(t *testing.T) {
	// One service is out of sync (not deployed), another is drifted
	// (stopped at runtime). The aggregate must be OUT_OF_SYNC because a
	// pending deploy supersedes drift repair.
	cmp := Compare(
		desired("a84fd21", map[string]Service{"api": svc("api:1"), "worker": svc("worker:1")}),
		deployed("dep_1", "a84fd21", map[string]Service{"api": svc("api:1")}),
		runtime(map[string]RuntimeService{"api": rsvc("api:1", "running")}),
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
	if got := cmp.Services["worker"].Result; got != ResultOutOfSync {
		t.Fatalf("worker: %s, want %s", got, ResultOutOfSync)
	}
}

func TestCompare_DeployedButNotDesiredAndStoppedAtRuntime_IsSynced(t *testing.T) {
	// A service was removed from Git and is no longer running: converged.
	cmp := Compare(
		desired("a84fd21", map[string]Service{}),
		deployed("dep_1", "a84fd21", map[string]Service{"api": svc("api:1")}),
		runtime(map[string]RuntimeService{}),
	)
	if got := cmp.Services["api"].Result; got != ResultSynced {
		t.Fatalf("api: %s, want %s", got, ResultSynced)
	}
	if cmp.Result != ResultSynced {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultSynced)
	}
}

func TestCompare_NilStatesTreatedAsEmpty(t *testing.T) {
	// Desired present, deployed and runtime nil: out of sync for each
	// desired service.
	cmp := Compare(
		desired("a84fd21", map[string]Service{"api": svc("api:1")}),
		nil, nil,
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
}

func TestCompare_EmptyDesiredWithOrphans_IsDrifted(t *testing.T) {
	// Nothing desired or deployed, but something runs at runtime.
	cmp := Compare(
		desired("a84fd21", map[string]Service{}),
		deployed("dep_1", "a84fd21", map[string]Service{}),
		runtime(map[string]RuntimeService{"rogue": rsvc("rogue:1", "running")}),
	)
	if cmp.Result != ResultDrifted {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultDrifted)
	}
}

func TestResult_String(t *testing.T) {
	cases := []struct {
		r    Result
		want string
	}{
		{ResultSynced, "SYNCED"},
		{ResultOutOfSync, "OUT_OF_SYNC"},
		{ResultDrifted, "DRIFTED"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("%v.String() = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestCompare_EnvDiffersBetweenDesiredAndDeployed_IsOutOfSync(t *testing.T) {
	// Git changed only env, image unchanged: Accorda must still detect a
	// needed redeploy.
	dSvcs := map[string]Service{"api": {Image: "api:1", Env: map[string]string{"LOG_LEVEL": "debug"}}}
	pSvcs := map[string]Service{"api": {Image: "api:1", Env: map[string]string{"LOG_LEVEL": "info"}}}
	cmp := Compare(
		desired("a84fd21", dSvcs),
		deployed("dep_1", "a84fd21", pSvcs),
		runtime(map[string]RuntimeService{"api": rsvc("api:1", "running")}),
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
	if got := cmp.Services["api"].Result; got != ResultOutOfSync {
		t.Fatalf("api: %s, want %s", got, ResultOutOfSync)
	}
}

func TestCompare_EnvEqualWhenBothNil_IsSynced(t *testing.T) {
	svcs := map[string]Service{"api": svc("api:1")}
	cmp := Compare(
		desired("a84fd21", svcs),
		deployed("dep_1", "a84fd21", svcs),
		runtime(map[string]RuntimeService{"api": rsvc("api:1", "running")}),
	)
	if got := cmp.Services["api"].Result; got != ResultSynced {
		t.Fatalf("api: %s, want %s", got, ResultSynced)
	}
}

func TestCompare_ReasonsAreSorted(t *testing.T) {
	// Multiple out-of-sync services produce multiple reasons; the slice
	// must be sorted so output is deterministic across runs.
	cmp := Compare(
		desired("a84fd21", map[string]Service{
			"worker": svc("worker:1"),
			"api":    svc("api:1"),
		}),
		deployed("dep_1", "a84fd21", map[string]Service{}),
		runtime(map[string]RuntimeService{}),
	)
	if cmp.Result != ResultOutOfSync {
		t.Fatalf("Result = %s, want %s", cmp.Result, ResultOutOfSync)
	}
	if len(cmp.Reasons) < 2 {
		t.Fatalf("expected at least 2 reasons, got %d", len(cmp.Reasons))
	}
	if !sort.StringsAreSorted(cmp.Reasons) {
		t.Fatalf("Reasons not sorted: %v", cmp.Reasons)
	}
}

package reconcile

import (
	"context"
	"sync"
	"testing"
	"time"

	"accorda/internal/sources"
)

// TestNewEnsemble_RequiresMembers verifies the ensemble rejects an empty or
// partially-built member list at construction time (docs/ACCORDA.md §49).
func TestNewEnsemble_RequiresMembers(t *testing.T) {
	if _, err := NewEnsemble(nil); err == nil {
		t.Fatal("NewEnsemble(nil) succeeded, want error")
	}
	if _, err := NewEnsemble([]EnsembleMember{}); err == nil {
		t.Fatal("NewEnsemble(empty) succeeded, want error")
	}
	if _, err := NewEnsemble([]EnsembleMember{{Name: "api", Reconciler: nil}}); err == nil {
		t.Fatal("NewEnsemble(nil reconciler) succeeded, want error")
	}
}

// TestNewEnsemble_DuplicateName verifies the ensemble rejects two members with
// the same name, which would make output and state attribution ambiguous.
func TestNewEnsemble_DuplicateName(t *testing.T) {
	r := New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)
	_, err := NewEnsemble([]EnsembleMember{
		{Name: "api", Reconciler: r},
		{Name: "api", Reconciler: r},
	})
	if err == nil {
		t.Fatal("NewEnsemble(duplicate names) succeeded, want error")
	}
}

// TestEnsemble_ReconcileRunsAllMembers verifies that one cycle drives every
// member and returns one result per member, so a single agent reconciles all
// its workloads in one pass (docs/ACCORDA.md §49).
func TestEnsemble_ReconcileRunsAllMembers(t *testing.T) {
	rA := New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)
	rB := New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)
	ensemble, err := NewEnsemble([]EnsembleMember{
		{Name: "api", Reconciler: rA},
		{Name: "worker", Reconciler: rB},
	})
	if err != nil {
		t.Fatalf("NewEnsemble: %v", err)
	}

	results := ensemble.Reconcile(context.Background())
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	got := map[string]*Result{}
	for _, mr := range results {
		got[mr.Name] = mr.Result
		if mr.Result == nil {
			t.Fatalf("member %q returned nil result", mr.Name)
		}
		if mr.Result.Phase != PhaseSynced {
			t.Errorf("member %q phase = %s, want %s", mr.Name, mr.Result.Phase, PhaseSynced)
		}
	}
	if _, ok := got["api"]; !ok {
		t.Error("missing result for api member")
	}
	if _, ok := got["worker"]; !ok {
		t.Error("missing result for worker member")
	}
}

// TestEnsemble_ReconcileRunsConcurrently verifies that members are reconciled
// concurrently rather than serially: a member that blocks on a channel does
// not prevent another member from starting its cycle. The ensemble still waits
// for every member before returning, so we assert the fast member's Fetch
// completes while the blocked member is still pending.
func TestEnsemble_ReconcileRunsConcurrently(t *testing.T) {
	start := make(chan struct{})
	var fastOnce sync.Once
	fastDone := make(chan struct{})
	rBlocked := New(&fakeBlockingSource{
		fakeSource: *fakeSourceWithDesired(),
		release:    start,
	}, fakeTargetWithDesired(), nil)

	fastSrc := fakeSourceWithDesired()
	fastSrc.fetchHook = func() { fastOnce.Do(func() { close(fastDone) }) }
	rFast := New(fastSrc, fakeTargetWithDesired(), nil)

	ensemble, err := NewEnsemble([]EnsembleMember{
		{Name: "blocked", Reconciler: rBlocked},
		{Name: "fast", Reconciler: rFast},
	})
	if err != nil {
		t.Fatalf("NewEnsemble: %v", err)
	}

	done := make(chan struct{})
	go func() { ensemble.Reconcile(context.Background()); close(done) }()

	select {
	case <-fastDone:
		// The fast member fetched while the blocked member was still waiting on
		// start, proving the fan-out runs members concurrently.
	case <-time.After(2 * time.Second):
		t.Fatal("fast member did not fetch while the blocked member was pending")
	}
	close(start) // release the blocked member so Reconcile can finish
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ensemble Reconcile did not finish after the blocked member was released")
	}
}

// TestEnsemble_RunStopsOnCancel verifies the continuous loop reconciles once
// immediately and then exits cleanly when the context is cancelled from the
// handler after the first cycle.
func TestEnsemble_RunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ensemble, err := NewEnsemble([]EnsembleMember{
		{Name: "api", Reconciler: New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)},
	})
	if err != nil {
		t.Fatalf("NewEnsemble: %v", err)
	}
	var mu sync.Mutex
	calls := 0
	handle := func(_ []MemberResult) {
		mu.Lock()
		calls++
		mu.Unlock()
		cancel() // stop after the first cycle
	}

	if err := ensemble.Run(ctx, time.Hour, handle); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("handler call count = %d, want 1", calls)
	}
}

func TestEnsemble_Run_RejectsNonPositiveInterval(t *testing.T) {
	ensemble, err := NewEnsemble([]EnsembleMember{
		{Name: "api", Reconciler: New(fakeSourceWithDesired(), fakeTargetWithDesired(), nil)},
	})
	if err != nil {
		t.Fatalf("NewEnsemble: %v", err)
	}
	if err := ensemble.Run(context.Background(), 0, nil); err == nil {
		t.Fatal("Run(interval=0) succeeded, want error")
	}
}

// fakeSourceWithDesired returns a source that yields a healthy desired state
// so a member reconciles to SYNCED without a live dependency.
func fakeSourceWithDesired() *fakeSource {
	return &fakeSource{
		desired: healthyDesired(),
		commit:  sources.Commit{SHA: "abc123"},
	}
}

// fakeTargetWithDesired returns a target that reports a healthy runtime.
func fakeTargetWithDesired() *fakeTarget {
	return &fakeTarget{
		health:  healthyHealth(),
		runtime: healthyRuntime(),
	}
}

// fakeBlockingSource is a Source whose Fetch blocks until release is closed,
// used to prove the ensemble fans members out concurrently.
type fakeBlockingSource struct {
	fakeSource
	release chan struct{}
}

func (f *fakeBlockingSource) Fetch(_ context.Context) (sources.Commit, error) {
	<-f.release
	return f.commit, nil
}

// Package reconcile drives the reconciliation lifecycle that turns a desired
// state into a converged runtime, for one target or for a multi-project
// ensemble of independent targets.
//
// The single-target lifecycle is described in the package doc.go. This file
// adds the Ensemble runner for docs/ACCORDA.md §49 (Phase 5 — Multi-Project /
// Multi-Target Compose): several independent Reconcilers fan out concurrently
// under one agent so one process can manage several Compose projects,
// repositories, and environments at once.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// EnsembleMember is one reconciliation unit of an Ensemble together with its
// operator name (docs/ACCORDA.md §49). The name distinguishes workloads in
// aggregated output and state paths; it is not used for reconciliation
// semantics, which the underlying Reconciler/Project owns entirely.
//
// A member is either a single-target Reconciler or a multi-target Runner
// (issue #103, docs/DECISIONS.md #53). Exactly one of the two must be set;
// the Ensemble fans each member out concurrently.
type EnsembleMember struct {
	// Name is the operator-chosen project name (api, worker, ...). It must be
	// unique within the Ensemble.
	Name string
	// Reconciler drives the lifecycle for this member's single source and
	// target. Mutually exclusive with Runner.
	Reconciler *Reconciler
	// Runner drives a multi-target unit (a Project, or a SingleTarget wrapper
	// of a Reconciler) for this member. Mutually exclusive with Reconciler.
	Runner CycleRunner
}

// unit returns the CycleRunner for this member, wrapping the Reconciler when
// only it is set. It is valid to call only after NewEnsemble validated the
// member.
func (m EnsembleMember) unit() CycleRunner {
	if m.Runner != nil {
		return m.Runner
	}
	return NewSingleTarget(m.Name, m.Reconciler)
}

// CycleRunner returns the member's CycleRunner, wrapping the single
// Reconciler when only it is set. It is the exported form of unit so callers
// that build a member directly (without NewEnsemble) can drive it uniformly.
func (m EnsembleMember) CycleRunner() CycleRunner {
	return m.unit()
}

// NewEnsemble returns an Ensemble that runs the given members concurrently.
// It requires at least one member; a member must set exactly one of
// Reconciler or Runner so a partially-built Ensemble is never silently
// half-run. Name uniqueness is checked case-insensitively to match
// config.ValidateEnsemble and Compose project-name normalization, so the two
// validators enforce the same contract rather than diverging (docs/ACCORDA.md
// §49).
func NewEnsemble(members []EnsembleMember) (*Ensemble, error) {
	if len(members) == 0 {
		return nil, errors.New("reconcile: ensemble requires at least one member")
	}
	seen := make(map[string]struct{}, len(members))
	for _, m := range members {
		if m.Name == "" {
			return nil, errors.New("reconcile: ensemble member name is required")
		}
		if (m.Reconciler == nil) == (m.Runner == nil) {
			return nil, fmt.Errorf("reconcile: ensemble member %q must set exactly one of Reconciler or Runner", m.Name)
		}
		key := strings.ToLower(m.Name)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("reconcile: ensemble member name %q collides with another member after normalization", m.Name)
		}
		seen[key] = struct{}{}
	}
	return &Ensemble{members: members}, nil
}

// Ensemble runs several Reconcilers concurrently (docs/ACCORDA.md §49). Each
// member keeps its own source, target, bus, receipt store, and lock; the
// Ensemble only fans cycles out to members and aggregates results. It is safe
// for concurrent use: Reconcile and Run acquire the ensemble's own mutex so
// two callers do not interleave member runs.
type Ensemble struct {
	mu      sync.Mutex
	members []EnsembleMember
}

// MemberResult pairs a member name with its cycle Result so callers can tell
// which workload a result belongs to when members are reconciled together.
type MemberResult struct {
	// Name is the member's operator name.
	Name string
	// Result is the member's reconciliation result for this cycle.
	Result *Result
}

// Reconcile runs one cycle for every member concurrently and returns all
// results. A member's failure does not cancel the others; each cycle runs to
// its own completion. It is the multi-project analog of Reconciler.Reconcile.
func (e *Ensemble) Reconcile(ctx context.Context) []MemberResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reconcile(ctx)
}

// reconcile performs the fan-out without acquiring the ensemble mutex; it is
// called by Reconcile and by Run's per-tick loop.
func (e *Ensemble) reconcile(ctx context.Context) []MemberResult {
	results := make([]MemberResult, 0, len(e.members))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range e.members {
		wg.Add(1)
		go func(member EnsembleMember) {
			defer wg.Done()
			unit := member.unit()
			unitResults := unit.Reconcile(ctx)
			mu.Lock()
			results = append(results, unitResults...)
			mu.Unlock()
		}(m)
	}
	wg.Wait()
	return results
}

// Run continuously reconciles all members until ctx is cancelled, running one
// cycle immediately and then every interval. Each member runs concurrently,
// so a slow or failing workload does not delay the others; failed member
// cycles are reported via handle and do not stop the loop
// (docs/DECISIONS.md #31). It is the multi-project analog of Reconciler.Run.
func (e *Ensemble) Run(ctx context.Context, interval time.Duration, handle func([]MemberResult)) error {
	if interval <= 0 {
		return fmt.Errorf("reconcile: ensemble polling interval must be positive: %s", interval)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		results := e.Reconcile(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if handle != nil {
			handle(results)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
}

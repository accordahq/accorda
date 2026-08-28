// Package reconcile — multi-target project runner (docs/ACCORDA.md §6, §25).
//
// A project may declare several targets that reconcile from a single source
// revision (issue #103, docs/DECISIONS.md #53). This file adds the Project
// runner that drives those targets: it holds one Reconciler per target, all
// sharing the project's single Source, and runs them sequentially so the
// shared source's managed checkout is never mutated concurrently. Each target
// keeps its own receipt store and deployment lock, so its history and
// serialization are isolated even though they share a Git revision.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CycleRunner is the per-project reconciliation unit that an Ensemble fans out
// concurrently. A single-target Reconciler (wrapped by SingleTarget) and a
// multi-target Project both implement it; Reconcile returns one MemberResult
// per target and Run polls continuously, so callers treat both the same way.
type CycleRunner interface {
	// Reconcile runs one cycle for every target of the unit and returns all
	// results.
	Reconcile(ctx context.Context) []MemberResult
	// Run continuously reconciles all targets until ctx is cancelled, running
	// one cycle immediately and then every interval.
	Run(ctx context.Context, interval time.Duration, handle func([]MemberResult)) error
}

// SingleTarget adapts a single Reconciler (one target) to CycleRunner so the
// Ensemble can fan out single-target projects uniformly with multi-target
// ones. label is the operator-facing name reported on each MemberResult.
type SingleTarget struct {
	label string
	r     *Reconciler
}

// NewSingleTarget wraps r, reporting each cycle result under label. label is
// empty for a standalone single-target project (preserving unprefixed
// output) or the project name for an ensemble member.
func NewSingleTarget(label string, r *Reconciler) *SingleTarget {
	return &SingleTarget{label: label, r: r}
}

// Reconcile runs one cycle and returns a single MemberResult.
func (s *SingleTarget) Reconcile(ctx context.Context) []MemberResult {
	return []MemberResult{{Name: s.label, Result: s.r.Reconcile(ctx)}}
}

// Run continuously reconciles the wrapped Reconciler.
func (s *SingleTarget) Run(ctx context.Context, interval time.Duration, handle func([]MemberResult)) error {
	return s.r.Run(ctx, interval, func(res *Result) {
		handle([]MemberResult{{Name: s.label, Result: res}})
	})
}

// TargetMember is one target of a project reconciled from the shared source.
// Its Reconciler owns the target lifecycle for this project's source; the
// Target identity labels the per-target results and state so an operator can
// attribute output and history to a specific deployment artifact.
type TargetMember struct {
	// Target identifies the deployment target (its type plus configured
	// path/image) for output and state attribution.
	Target string
	// Reconciler drives this target's lifecycle using the project's source.
	// It must be built with the shared source and its own receipt store and
	// lock.
	Reconciler *Reconciler
}

// Project reconciles several targets from one source revision
// (issue #103, docs/DECISIONS.md #53). It is the single-project analog of the
// Ensemble: where the Ensemble fans independent projects out concurrently,
// Project runs one project's targets sequentially so the shared source's
// mutable checkout is never accessed concurrently. Targets are reconciled in
// declaration order; a slow or failing target does not stop the others, but a
// later target is not started until the earlier one's cycle completes.
type Project struct {
	name    string
	members []TargetMember
}

// NewProject returns a Project that reconciles the given target members. It
// requires at least one member; a nil Reconciler or empty target identity is
// rejected so a partially-built Project is never silently half-run. name is
// the project's operator name (empty for a standalone project), used to
// prefix each target's MemberResult so a multi-project document keeps output
// attributable to its workload.
func NewProject(name string, members []TargetMember) (*Project, error) {
	if len(members) == 0 {
		return nil, errors.New("reconcile: project requires at least one target")
	}
	for _, m := range members {
		if m.Target == "" {
			return nil, errors.New("reconcile: project target identity is required")
		}
		if m.Reconciler == nil {
			return nil, errors.New("reconcile: project target has a nil reconciler")
		}
	}
	return &Project{name: name, members: members}, nil
}

// memberName returns the MemberResult label for a target: the project name
// when present, otherwise the target identity. For a standalone multi-target
// project (no name) the label is the target identity alone.
func (p *Project) memberName(target string) string {
	if p.name == "" {
		return target
	}
	return p.name + ": " + target
}

// Reconcile runs one cycle for every target sequentially and returns all
// results. A target's failure does not cancel the others; each cycle runs to
// its own completion.
func (p *Project) Reconcile(ctx context.Context) []MemberResult {
	results := make([]MemberResult, len(p.members))
	for i, m := range p.members {
		results[i] = MemberResult{Name: p.memberName(m.Target), Result: m.Reconciler.Reconcile(ctx)}
	}
	return results
}

// Run continuously reconciles all targets until ctx is cancelled, running one
// cycle immediately and then every interval. Targets run sequentially so the
// shared source is not mutated concurrently. It is the single-project analog
// of Ensemble.Run.
func (p *Project) Run(ctx context.Context, interval time.Duration, handle func([]MemberResult)) error {
	if interval <= 0 {
		return fmt.Errorf("reconcile: project polling interval must be positive: %s", interval)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		results := p.Reconcile(ctx)
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

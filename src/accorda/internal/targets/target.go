package targets

import (
	"context"

	"accorda/internal/core/health"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
)

// Target is the abstraction Accorda core uses to reconcile desired state
// against a concrete deployment target (docs/ACCORDA.md §12). Core never
// depends on a specific target implementation; Compose, Kubernetes, and any
// future drivers implement this interface.
//
// The methods follow the reconciliation lifecycle from §6:
//
//   - Validate checks that the target is configured and reachable enough to
//     reconcile, without making changes.
//   - Current returns the runtime state actually running on the target now.
//   - Plan computes the actions needed to reconcile desired state against
//     the target's current state.
//   - Apply executes a plan against the target.
//   - Health verifies that the deployed workloads are actually healthy,
//     which the spec treats as distinct from merely being deployed (§19).
//
// All methods take a context so callers can cancel long-running operations.
// Implementations must be safe for concurrent use by the reconcile loop.
type Target interface {
	// Validate checks the target configuration and connectivity. It must
	// not mutate the target.
	Validate(ctx context.Context) error
	// Current returns the runtime state currently running on the target.
	Current(ctx context.Context) (*state.RuntimeState, error)
	// Plan computes the deployment plan that reconciles desired state with
	// the target's current state, without applying it. deployed is the
	// state Accorda last successfully deployed; it supplies the deployed
	// service configuration used to decide recreation via the canonical
	// service hash (docs/ACCORDA.md §10). It may be nil when there is no
	// prior deployment.
	Plan(ctx context.Context, desired *state.DesiredState, deployed *state.DeployedState) (*plan.Plan, error)
	// Apply applies the given plan to the target. It must be idempotent
	// where possible so that retries are safe.
	Apply(ctx context.Context, p *plan.Plan) error
	// Health verifies the health of the currently deployed workloads.
	Health(ctx context.Context) (*health.Health, error)
}

// ErrNotImplemented is returned by stub targets for methods that have no
// backing implementation yet. It lets core call targets uniformly while
// drivers are still being built.
var ErrNotImplemented = errNotImplemented{}

type errNotImplemented struct{}

func (errNotImplemented) Error() string { return "target: not implemented" }

// Compile-time interface checks: every concrete target driver must satisfy
// Target. The stub type verifies the interface here so that a missing method
// is caught at build time, not at runtime.
var _ Target = (*Stub)(nil)

// Stub is a Target implementation that returns ErrNotImplemented for every
// method. It exists so that core code and tests can reference a Target
// without a real driver, and so that the Target interface has a concrete
// implementation guarding it at compile time.
type Stub struct{}

// NewStub returns a no-op Target.
func NewStub() *Stub { return &Stub{} }

func (Stub) Validate(context.Context) error                       { return ErrNotImplemented }
func (Stub) Current(context.Context) (*state.RuntimeState, error) { return nil, ErrNotImplemented }
func (Stub) Plan(context.Context, *state.DesiredState, *state.DeployedState) (*plan.Plan, error) {
	return nil, ErrNotImplemented
}
func (Stub) Apply(context.Context, *plan.Plan) error        { return ErrNotImplemented }
func (Stub) Health(context.Context) (*health.Health, error) { return nil, ErrNotImplemented }

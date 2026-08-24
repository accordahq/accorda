package sources

import (
	"context"
	"errors"
	"time"

	"accorda/internal/core/state"
)

// Source is the abstraction Accorda core uses to read desired state from a
// Git repository (docs/ACCORDA.md §13). Core never depends on a specific
// Git host; generic Git and provider integrations (GitHub, GitLab, Gitea)
// all implement this interface.
//
// The methods follow the fetch phase of the reconciliation lifecycle
// (docs/ACCORDA.md §6):
//
//   - Validate checks that the source is configured and reachable enough to
//     fetch from, without cloning.
//   - Fetch ensures the latest state is available locally and returns the
//     commit information that the desired state was read from.
//   - Desired returns the desired state declared in Git at the fetched
//     commit.
//
// All methods take a context so callers can cancel long-running operations.
// Implementations must be safe for concurrent use by the reconcile loop.
type Source interface {
	// Validate checks the source configuration and connectivity. It must
	// not clone or fetch.
	Validate(ctx context.Context) error
	// Fetch ensures the latest state of the configured branch is available
	// and returns the commit it points to.
	Fetch(ctx context.Context) (Commit, error)
	// Desired returns the desired state declared in Git at the given
	// commit. A nil commit means "use the fetched HEAD".
	Desired(ctx context.Context, ref *Commit) (*state.DesiredState, error)
}

// RevisionMaterializer is an optional source capability used when a target
// consumes repository files directly. It makes one already-fetched revision
// the active managed worktree without contacting the remote. Core uses it
// before rollback so file-backed target inputs match the restored commit.
type RevisionMaterializer interface {
	Materialize(ctx context.Context, ref *Commit) error
}

// Commit identifies a point in the Git history the source fetched.
type Commit struct {
	// SHA is the full or abbreviated commit hash.
	SHA string
	// Branch is the branch the commit was read from.
	Branch string
	// Time is the authored/committed timestamp of the commit, if known.
	Time time.Time
}

// ErrNotImplemented is returned by stub sources for methods that have no
// backing implementation yet.
var ErrNotImplemented = errNotImplemented{}

type errNotImplemented struct{}

func (errNotImplemented) Error() string { return "source: not implemented" }

// Compile-time interface check: the Stub type verifies the Source interface
// here so that a missing method is caught at build time, not at runtime.
var _ Source = (*Stub)(nil)

// Stub is a Source implementation that returns ErrNotImplemented for every
// method. It exists so that core code and tests can reference a Source
// without a real Git driver, and so that the Source interface has a concrete
// implementation guarding it at compile time.
type Stub struct{}

// NewStub returns a no-op Source.
func NewStub() *Stub { return &Stub{} }

func (Stub) Validate(context.Context) error { return ErrNotImplemented }
func (Stub) Fetch(context.Context) (Commit, error) {
	return Commit{}, ErrNotImplemented
}
func (Stub) Desired(context.Context, *Commit) (*state.DesiredState, error) {
	return nil, ErrNotImplemented
}

// ErrAbsent is returned by Fetch when the source has no commits available.
var ErrAbsent = errors.New("source: no commits available")

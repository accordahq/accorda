package sources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Source is the abstraction Accorda core uses to access revisions from a
// Git repository (docs/ACCORDA.md §13). Core never depends on a specific
// Git host; generic Git and provider integrations (GitHub, GitLab, Gitea)
// all implement this interface.
//
// The methods follow the fetch phase of the reconciliation lifecycle
// (docs/ACCORDA.md §6):
//
//   - Validate checks that the source is configured and reachable enough to
//     fetch from, without cloning.
//   - Fetch ensures the latest state is available locally and returns its
//     commit information.
//   - Revision opens a filesystem view for current or historical target
//     declarations without teaching the source how to parse them.
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
	// Revision opens a filesystem view of the given commit. A nil commit
	// means "use the fetched HEAD". The caller must close the returned view.
	Revision(ctx context.Context, ref *Commit) (*Revision, error)
}

// Worktree exposes the stable current-worktree paths needed to construct a
// file-backed target without coupling the command layer to a Git adapter.
type Worktree interface {
	CheckoutDir() (string, error)
	CheckoutPath(repositoryPath string) (string, error)
	BindingPath() string
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

// Revision is a source-owned filesystem view of one commit. Root contains a
// real directory tree so target-native loaders can resolve includes, extends,
// and relative resources. Close releases private historical materializations.
type Revision struct {
	Commit     Commit
	Repository string
	Root       string
	digest     func(string) (string, bool, error)
	release    func() error
}

// NewRevision constructs a revision view for a source adapter.
func NewRevision(commit Commit, repository, root string, digest func(string) (string, bool, error), release func() error) *Revision {
	return &Revision{Commit: commit, Repository: repository, Root: root, digest: digest, release: release}
}

// Path resolves a repository-relative path inside the revision root.
func (r *Revision) Path(repositoryPath string) (string, error) {
	if r == nil {
		return "", errors.New("source: revision is nil")
	}
	clean, err := CleanRepositoryPath(repositoryPath)
	if err != nil {
		return "", err
	}
	if r.Root == "" {
		return "", errors.New("source: revision root is empty")
	}
	root, err := filepath.Abs(r.Root)
	if err != nil {
		return "", fmt.Errorf("source: resolve revision root: %w", err)
	}
	candidate := filepath.Join(root, filepath.FromSlash(clean))
	current := root
	for _, part := range strings.Split(filepath.FromSlash(clean), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("source: inspect revision path: %w", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return "", fmt.Errorf("source: resolve revision symlink: %w", err)
		}
		if !pathWithin(root, resolved) {
			return "", fmt.Errorf("source: repository path %q resolves outside the worktree", repositoryPath)
		}
		current = resolved
	}
	return candidate, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Digest returns the SHA-256 digest of a tracked file in this revision.
// ok is false when the path is not a tracked regular file.
func (r *Revision) Digest(repositoryPath string) (digest string, ok bool, err error) {
	if r == nil || r.digest == nil {
		return "", false, nil
	}
	clean, err := CleanRepositoryPath(repositoryPath)
	if err != nil {
		return "", false, err
	}
	return r.digest(clean)
}

// Close releases resources held by the revision. It is safe to call more
// than once when the adapter's release callback is idempotent.
func (r *Revision) Close() error {
	if r == nil || r.release == nil {
		return nil
	}
	release := r.release
	r.release = nil
	if err := release(); err != nil {
		return fmt.Errorf("source: release revision: %w", err)
	}
	return nil
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
func (Stub) Revision(context.Context, *Commit) (*Revision, error) {
	return nil, ErrNotImplemented
}

// ErrAbsent is returned by Fetch when the source has no commits available.
var ErrAbsent = errors.New("source: no commits available")

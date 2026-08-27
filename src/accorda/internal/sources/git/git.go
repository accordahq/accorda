package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
	gossh "github.com/go-git/go-git/v6/plumbing/transport/ssh"

	"accorda/internal/config"
	"accorda/internal/sources"
)

// Compile-time interface check: Git satisfies sources.Source here so a
// missing method is caught at build time, not at runtime.
var _ sources.Source = (*Git)(nil)
var _ sources.RevisionMaterializer = (*Git)(nil)

// Git is the generic Git source adapter (docs/ACCORDA.md §13).
//
// It clones or fetches a repository into a local cache, checks out the
// configured branch, and returns commit metadata. It uses the go-git
// library rather than shelling out to the system `git` command, so it does
// not require `git` to be installed on the host. It never makes GitHub- or
// provider-specific calls; it works against any Git server over SSH or
// HTTPS, including on-premises servers, with zero SaaS dependency.
type Git struct {
	// Source is the validated source configuration.
	Source config.Source
	// CacheDir is the local path the repository is cloned into. If empty,
	// a directory under BaseDir named after the repo is used.
	CacheDir string
	// BaseDir is the root used to derive CacheDir when CacheDir is empty.
	// If both are empty, a private directory under the user's cache is used.
	BaseDir string
	// SSHCommand overrides the GIT_SSH_COMMAND used for SSH transports.
	// Kept for API compatibility; with go-git the SSH key is read from
	// Source.Auth.Key directly.
	SSHCommand string

	// auth holds the go-git transport auth method derived from Source.Auth
	// in New. It is applied to clone and fetch operations. Secret values
	// are never logged (docs/ACCORDA.md §18, §56).
	auth transportAuth
	// cacheRoot discovers the platform-specific private cache base. Keeping
	// this dependency explicit makes fail-closed discovery testable.
	cacheRoot func() (string, error)
	// cacheNamespace isolates the mutable checkout for one accorda.yaml
	// project. Different projects may deploy different branches of the same
	// repository concurrently and must never share a worktree.
	cacheNamespace string
}

// transportAuth carries the go-git auth method and a flag distinguishing SSH
// from HTTPS so callers can avoid importing the transport packages.
type transportAuth struct {
	method    any
	err       error
	isSSH     bool
	isHTTPS   bool
	sshUser   string
	sshKey    []byte
	httpUser  string
	httpToken string
}

// New returns a Git source for the given configuration. The returned source
// is ready to Validate but does not touch the filesystem until Validate or
// Fetch is called.
//
// New derives the auth method from src.Auth (docs/ACCORDA.md §13, §15):
//
//   - auth.type=ssh reads the private key from Source.Auth.Key and uses it
//     for go-git's SSH transport. The key material is held in memory and
//     never logged.
//   - auth.type=https uses Source.Auth.Token as the HTTP basic-auth password
//     for go-git's HTTPS transport. Source.URL remains the clean, loggable
//     identifier used in error messages and DesiredState.Repository.
//   - An empty auth.type means "use the ambient environment": go-git will
//     use SSH agent for ssh:// URLs and unauthenticated HTTPS for https://
//     URLs.
//
// Secret values are never logged. Error messages reference field names, not
// values.
func New(src config.Source, opts ...Option) *Git {
	g := &Git{Source: src, cacheRoot: defaultCacheBase}
	for _, opt := range opts {
		opt(g)
	}
	g.applyAuth()
	return g
}

// Option configures a Git source.
type Option func(*Git)

// WithCacheDir sets the directory the repository is cloned into.
func WithCacheDir(dir string) Option {
	return func(g *Git) { g.CacheDir = dir }
}

// WithBaseDir sets the root used to derive a cache directory when one is not
// set explicitly.
func WithBaseDir(dir string) Option {
	return func(g *Git) { g.BaseDir = dir }
}

// WithCacheNamespace scopes the derived cache directory to one Accorda
// project. An empty namespace preserves the URL-only adapter default used by
// direct library callers and tests.
func WithCacheNamespace(namespace string) Option {
	return func(g *Git) { g.cacheNamespace = namespace }
}

// WithSSHCommand is kept for API compatibility. With go-git the SSH key is
// read from Source.Auth.Key; the command string is no longer used.
func WithSSHCommand(_ string) Option {
	return func(*Git) {
		// Intentionally empty: go-git reads Source.Auth.Key directly. This
		// compatibility option preserves existing callers without applying the
		// obsolete GIT_SSH_COMMAND setting.
	}
}

// WithAuth sets the auth configuration applied to git operations. It
// overrides the auth derived from Source.Auth in New and is primarily useful
// in tests.
func WithAuth(a config.Auth) Option {
	return func(g *Git) {
		g.Source.Auth = a
		g.applyAuth()
	}
}

// Validate checks the source configuration. It does not clone or fetch
// (docs/ACCORDA.md §13, §6 fetch phase, §15 auth). In remote mode it
// requires a URL and branch; in in-place mode it requires the bound worktree
// to be a readable Git repository and never uses auth.
func (g *Git) Validate(_ context.Context) error {
	if g == nil {
		return errors.New("git source: nil source")
	}
	if g.isInPlace() {
		dir, err := g.cacheDir()
		if err != nil {
			return err
		}
		if _, err := git.PlainOpen(dir); err != nil {
			return fmt.Errorf("git source: open worktree %q: %w", dir, err)
		}
		return nil
	}
	if strings.TrimSpace(g.Source.URL) == "" {
		return errors.New("git source: url is required")
	}
	if g.configuredBranch() == "" {
		return errors.New("git source: branch is required")
	}
	g.applyAuth()
	return g.validateAuth()
}

// isInPlace reports whether the source reconciles in place from a user-owned
// worktree (source.path set, no URL) rather than from a remote cloned into
// the cache (source.url). A source with neither configured is invalid and is
// treated as remote so Validate reports the missing URL.
func (g *Git) isInPlace() bool {
	return g != nil && g.Source.URL == "" && g.Source.Path != ""
}

// validateAuth checks the auth configuration without touching secrets. It
// reports field-oriented errors and never includes token or key values
// (docs/ACCORDA.md §18, §56).
func (g *Git) validateAuth() error {
	switch a := g.Source.Auth; a.Type {
	case "", config.AuthSSH, config.AuthHTTPS:
		switch a.Type {
		case config.AuthSSH:
			if strings.TrimSpace(a.Key) == "" {
				return errors.New("git source: auth.key is required when auth.type is \"ssh\"")
			}
		case config.AuthHTTPS:
			if strings.TrimSpace(a.Token) == "" {
				return errors.New("git source: auth.token is required when auth.type is \"https\"")
			}
		}
	default:
		return fmt.Errorf("git source: auth.type %q is not supported (want %q or %q)",
			a.Type, config.AuthSSH, config.AuthHTTPS)
	}
	if g.auth.err != nil {
		return g.auth.err
	}
	return nil
}

// Fetch ensures the latest state of the configured branch is available
// locally and returns the commit it points to.
//
// In remote mode (source.url) it clones into the cache if absent, otherwise
// fetches and checks out the configured branch. In in-place mode (source.path)
// it never mutates the user-owned worktree and simply reads its current HEAD.
// The returned Commit carries the SHA, branch, and authored time of HEAD.
func (g *Git) Fetch(ctx context.Context) (sources.Commit, error) {
	if err := g.ensureReady(ctx); err != nil {
		return sources.Commit{}, err
	}
	dir, err := g.cacheDir()
	if err != nil {
		return sources.Commit{}, err
	}
	if !g.isInPlace() {
		if err := secureCacheDir(dir); err != nil {
			return sources.Commit{}, err
		}
		exists, err := repoExists(dir)
		if err != nil {
			return sources.Commit{}, fmt.Errorf("git source: inspect cache: %w", err)
		}
		if !exists {
			if err := g.clone(ctx, dir); err != nil {
				return sources.Commit{}, err
			}
		} else {
			if err := g.verifyOrigin(dir); err != nil {
				return sources.Commit{}, err
			}
			if err := g.fetch(ctx, dir); err != nil {
				return sources.Commit{}, err
			}
		}
		branch := g.configuredBranch()
		if err := g.checkout(ctx, dir, branch); err != nil {
			return sources.Commit{}, err
		}
	}
	return g.headCommit(ctx, dir, g.branchFor())
}

// Revision opens a real filesystem view for the requested commit. The current
// commit uses the bound or managed worktree. Historical commits are expanded
// into a private temporary tree so target loaders can resolve relative files
// without mutating either worktree mode.
func (g *Git) Revision(ctx context.Context, ref *sources.Commit) (*sources.Revision, error) {
	if err := g.ensureReady(ctx); err != nil {
		return nil, err
	}
	commit, err := g.resolveCommit(ctx, ref)
	if err != nil {
		return nil, err
	}
	dir, err := g.cacheDir()
	if err != nil {
		return nil, err
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("git source: open worktree: %w", err)
	}
	commitObject, err := repo.CommitObject(plumbing.NewHash(commit.SHA))
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("git source: read commit %s: %w", commit.SHA, err)
	}
	tree, err := commitObject.Tree()
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("git source: read tree at %s: %w", commit.SHA, err)
	}
	if commit.Branch == "" {
		commit.Branch = g.branchFor()
	}
	if commit.Time.IsZero() {
		commit.Time = commitObject.Author.When.UTC()
	}
	root := dir
	var temporaryRoot string
	if !shaIsHead(dir, commit.SHA) {
		temporaryRoot, err = os.MkdirTemp("", "accorda-git-revision-")
		if err != nil {
			_ = repo.Close()
			return nil, fmt.Errorf("git source: create temporary revision: %w", err)
		}
		if err := materializeTree(ctx, tree, temporaryRoot); err != nil {
			_ = os.RemoveAll(temporaryRoot)
			_ = repo.Close()
			return nil, fmt.Errorf("git source: materialize tree at %s: %w", commit.SHA, err)
		}
		root = temporaryRoot
	}
	repositoryID := RedactURL(g.Source.URL)
	if g.isInPlace() {
		repositoryID = dir
	}
	digest := func(repositoryPath string) (string, bool, error) {
		file, err := tree.File(repositoryPath)
		if errors.Is(err, object.ErrFileNotFound) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("git source: read tracked file %q: %w", repositoryPath, err)
		}
		contents, err := file.Contents()
		if err != nil {
			return "", false, fmt.Errorf("git source: read tracked file %q: %w", repositoryPath, err)
		}
		sum := sha256.Sum256([]byte(contents))
		return hex.EncodeToString(sum[:]), true, nil
	}
	release := func() error {
		closeErr := repo.Close()
		if temporaryRoot == "" {
			return closeErr
		}
		return errors.Join(closeErr, os.RemoveAll(temporaryRoot))
	}
	return sources.NewRevision(commit, repositoryID, root, digest, release), nil
}

// Materialize checks out an already-fetched revision into this source's
// isolated managed worktree. Rollback uses it after reading historical
// desired state so Compose sees the referenced files from the same commit.
//
// In in-place mode (source.path) Materialize is unsupported: it would rewrite
// the user-owned worktree, which the adapter never mutates
// (docs/DECISIONS.md #51). The historical desired state is still reconstructed
// from the commit's tree via Desired; only the Compose target's on-disk
// materialization for rollback is unavailable.
func (g *Git) Materialize(ctx context.Context, ref *sources.Commit) error {
	if err := g.ensureReady(ctx); err != nil {
		return err
	}
	if g.isInPlace() {
		return errors.New("git source: in-place materialization is not supported (would rewrite the user-owned worktree)")
	}
	commit, err := g.resolveCommit(ctx, ref)
	if err != nil {
		return err
	}
	dir, err := g.cacheDir()
	if err != nil {
		return err
	}
	if err := g.checkoutCommit(ctx, dir, commit.SHA); err != nil {
		return fmt.Errorf("git source: materialize %s: %w", commit.SHA, err)
	}
	return nil
}

// ensureReady runs Validate and is shared by Fetch and Desired.
func (g *Git) ensureReady(ctx context.Context) error {
	return g.Validate(ctx)
}

// cacheDir returns the configured cache directory or derives a
// collision-resistant identity under a user-private cache root.
func (g *Git) cacheDir() (string, error) {
	if g.CacheDir != "" {
		return g.CacheDir, nil
	}
	base := g.BaseDir
	if base == "" {
		root := g.cacheRoot
		if root == nil {
			root = defaultCacheBase
		}
		var err error
		base, err = root()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(base, namespacedRepoDirName(g.Source.URL, g.cacheNamespace)), nil
}

// CheckoutExists reports whether this source's isolated managed-checkout path
// already exists. It does not clone, fetch, validate, or mutate it; callers
// use the distinction between an absent checkout and an invalid existing one.
func (g *Git) CheckoutExists() (bool, error) {
	if g == nil {
		return false, errors.New("git source: nil source")
	}
	dir, err := g.cacheDir()
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.New("git source: managed checkout path must be a directory")
	}
	return true, nil
}

// CheckoutDir returns the absolute path of this source's managed Git worktree
// root. The directory does not need to exist yet: Fetch creates it before the
// reconcile loop validates or applies the target. Operators use it to stage
// gitignored deployment-time inputs (env_file, label_file) that Compose
// resolves relative to the checkout at apply time.
func (g *Git) CheckoutDir() (string, error) {
	if g == nil {
		return "", errors.New("git source: nil source")
	}
	return g.cacheDir()
}

// FindWorktreeRoot returns the root of the Git worktree containing path.
// DetectDotGit supports artifacts nested below the root as well as linked
// worktrees whose .git entry is a file.
func FindWorktreeRoot(path string) (string, error) {
	repository, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return "", fmt.Errorf("git source: find worktree containing %q: %w", path, err)
	}
	defer func() { _ = repository.Close() }()
	worktree, err := repository.Worktree()
	if err != nil {
		return "", fmt.Errorf("git source: open worktree containing %q: %w", path, err)
	}
	root, err := filepath.Abs(worktree.Filesystem().Root())
	if err != nil {
		return "", fmt.Errorf("git source: resolve worktree containing %q: %w", path, err)
	}
	return root, nil
}

// CheckoutPath returns an absolute path inside this source's managed Git
// worktree. The worktree does not need to exist yet: Fetch creates it before
// the reconcile loop validates or applies the target.
//
// Repository paths are constrained to the checkout so Git-controlled
// configuration cannot escape into arbitrary host paths.
func (g *Git) CheckoutPath(repositoryPath string) (string, error) {
	if g == nil {
		return "", errors.New("git source: nil source")
	}
	clean, err := sources.CleanRepositoryPath(repositoryPath)
	if err != nil {
		return "", err
	}
	dir, err := g.cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(clean)), nil
}

// BindingPath returns the configured source path without interpreting it as
// any target-specific artifact.
func (g *Git) BindingPath() string {
	if g == nil {
		return ""
	}
	return g.Source.Path
}

func defaultCacheBase() (string, error) {
	return cacheBase(os.UserCacheDir, os.UserConfigDir)
}

func cacheBase(userCacheDir, userConfigDir func() (string, error)) (string, error) {
	cacheDir, cacheErr := userCacheDir()
	if cacheErr == nil && strings.TrimSpace(cacheDir) != "" {
		return filepath.Join(cacheDir, "accorda", "git"), nil
	}
	configDir, configErr := userConfigDir()
	if configErr == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "accorda", "git-cache"), nil
	}
	return "", fmt.Errorf("git source: determine private cache root: user cache: %v; user config: %v",
		cacheRootError(cacheErr), cacheRootError(configErr))
}

func cacheRootError(err error) error {
	if err != nil {
		return err
	}
	return errors.New("empty path")
}

// secureCacheDir rejects symlinked cache paths and makes existing cache
// directories private before they are inspected or reused.
func secureCacheDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("git source: inspect cache path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("git source: cache path must be a private directory, not a symlink or file")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("git source: secure cache directory: %w", err)
	}
	return nil
}

func ensurePrivateCacheParent(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("git source: create cache parent: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("git source: inspect cache parent: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("git source: cache parent must be a private directory, not a symlink or file")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("git source: secure cache parent: %w", err)
	}
	return nil
}

// clone clones the configured URL into dir using go-git. Auth is applied
// via the transport method derived in applyAuth. The error message
// references the clean Source.URL so credentials are never leaked
// (docs/ACCORDA.md §18, §56).
func (g *Git) clone(ctx context.Context, dir string) error {
	if err := ensurePrivateCacheParent(filepath.Dir(dir)); err != nil {
		return err
	}
	branch := g.configuredBranch()
	cloneOpts := &git.CloneOptions{
		URL:           g.Source.URL,
		RemoteName:    "origin",
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		NoCheckout:    true,
	}
	if err := g.applyClientOptions(&cloneOpts.ClientOptions); err != nil {
		return err
	}
	if _, err := git.PlainCloneContext(ctx, dir, cloneOpts); err != nil {
		return fmt.Errorf("git source: clone %q: %w", RedactURL(g.Source.URL), err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("git source: secure cloned repository: %w", err)
	}
	return nil
}

// verifyOrigin ensures a pre-existing cache belongs to the configured
// repository before go-git follows its stored origin.
func (g *Git) verifyOrigin(dir string) error {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("git source: open cache: %w", err)
	}
	defer func() { _ = r.Close() }()
	remote, err := r.Remote("origin")
	if err != nil {
		return fmt.Errorf("git source: origin remote: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) != 1 || strings.TrimSpace(urls[0]) != strings.TrimSpace(g.Source.URL) {
		return errors.New("git source: cached origin does not match configured source")
	}
	return nil
}

// fetch fetches the configured branch from origin using go-git.
func (g *Git) fetch(ctx context.Context, dir string) error {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("git source: open cache: %w", err)
	}
	defer func() { _ = r.Close() }()
	remote, err := r.Remote("origin")
	if err != nil {
		return fmt.Errorf("git source: origin remote: %w", err)
	}
	branch := g.configuredBranch()
	fetchOpts := &git.FetchOptions{
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec("+refs/heads/" + branch + ":refs/remotes/origin/" + branch),
		},
	}
	if err := g.applyClientOptions(&fetchOpts.ClientOptions); err != nil {
		return err
	}
	if err := remote.FetchContext(ctx, fetchOpts); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("git source: fetch %q: %w", branch, err)
	}
	return nil
}

// checkout checks out the configured branch and resets the worktree to the
// fetched remote tip.
func (g *Git) checkout(_ context.Context, dir, branch string) error {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("git source: open cache: %w", err)
	}
	defer func() { _ = r.Close() }()
	wt, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("git source: worktree: %w", err)
	}
	ref := plumbing.NewRemoteReferenceName("origin", branch)
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: ref,
		Force:  true,
	}); err != nil {
		return fmt.Errorf("git source: checkout %q: %w", branch, err)
	}
	return nil
}

func (g *Git) checkoutCommit(ctx context.Context, dir, sha string) error {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("git source: open cache: %w", err)
	}
	defer func() { _ = r.Close() }()
	return checkoutRepositoryCommit(ctx, r, sha)
}

func checkoutRepositoryCommit(ctx context.Context, r *git.Repository, sha string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wt, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("git source: worktree: %w", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Hash:  plumbing.NewHash(sha),
		Force: true,
	}); err != nil {
		return fmt.Errorf("git source: checkout commit %s: %w", sha, err)
	}
	return nil
}

// headCommit reads the SHA, branch, and authored time of HEAD using go-git.
func (g *Git) headCommit(_ context.Context, dir, branch string) (sources.Commit, error) {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return sources.Commit{}, fmt.Errorf("git source: open cache: %w", err)
	}
	defer func() { _ = r.Close() }()
	head, err := r.Head()
	if err != nil {
		return sources.Commit{}, fmt.Errorf("git source: read HEAD: %w", err)
	}
	commit, err := r.CommitObject(head.Hash())
	if err != nil {
		return sources.Commit{}, fmt.Errorf("git source: read commit: %w", err)
	}
	return sources.Commit{
		SHA:    commit.Hash.String(),
		Branch: branchFor(branch, head),
		Time:   commit.Author.When.UTC(),
	}, nil
}

// branchFor returns the branch name reported for HEAD. When the configured
// branch is empty (in-place mode), it derives the branch from the HEAD
// reference's short name so the reported branch reflects the worktree's
// actual checkout.
func branchFor(configured string, head *plumbing.Reference) string {
	if configured != "" {
		return configured
	}
	if name := head.Name().Short(); name != "" && name != "HEAD" {
		return name
	}
	return ""
}

// resolveCommit returns the commit to read desired state from, defaulting to
// the current HEAD of the cache when ref is nil.
func (g *Git) resolveCommit(ctx context.Context, ref *sources.Commit) (sources.Commit, error) {
	if ref != nil && ref.SHA != "" {
		return *ref, nil
	}
	dir, err := g.cacheDir()
	if err != nil {
		return sources.Commit{}, err
	}
	if !g.isInPlace() {
		if exists, err := repoExists(dir); err != nil || !exists {
			if err != nil {
				return sources.Commit{}, fmt.Errorf("git source: inspect cache: %w", err)
			}
			// Cache is empty; fetch first so HEAD is meaningful.
			if _, err := g.Fetch(ctx); err != nil {
				return sources.Commit{}, err
			}
		}
	}
	return g.headCommit(ctx, dir, g.branchFor())
}

// configuredBranch returns the trimmed configured branch name, or the empty
// string when unset. It is the single accessor for the branch so validation,
// git operations, and commit reporting read one canonical value instead of
// reaching into Source.Branch directly.
func (g *Git) configuredBranch() string {
	if g == nil {
		return ""
	}
	return strings.TrimSpace(g.Source.Branch)
}

// branchFor returns the branch name used when reading HEAD. It is the
// configured branch in remote mode, or empty in in-place mode so headCommit
// derives the branch from the HEAD reference.
func (g *Git) branchFor() string {
	if g.isInPlace() {
		return ""
	}
	return g.configuredBranch()
}

// materializeTree writes tracked files from tree beneath root. The private
// tree is short-lived and gives target-native loaders real filesystem context.
// Regular files are written before symlinks so no tracked path can make a
// later write traverse a symlink outside the private root.
func materializeTree(ctx context.Context, tree *object.Tree, root string) error {
	if err := tree.Files().ForEach(func(file *object.File) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.Mode == filemode.Symlink {
			return nil
		}
		repositoryPath, err := sources.CleanRepositoryPath(file.Name)
		if err != nil {
			return err
		}
		destination := filepath.Join(root, filepath.FromSlash(repositoryPath))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("create directory for %q: %w", repositoryPath, err)
		}
		contents, err := file.Contents()
		if err != nil {
			return fmt.Errorf("read %q: %w", repositoryPath, err)
		}
		if err := materializeTreeFile(file.Mode, destination, contents); err != nil {
			return fmt.Errorf("write %q: %w", repositoryPath, err)
		}
		return nil
	}); err != nil {
		return err
	}
	return tree.Files().ForEach(func(file *object.File) error {
		if file.Mode != filemode.Symlink {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		repositoryPath, err := sources.CleanRepositoryPath(file.Name)
		if err != nil {
			return err
		}
		contents, err := file.Contents()
		if err != nil {
			return fmt.Errorf("read %q: %w", repositoryPath, err)
		}
		if err := validateSymlink(repositoryPath, contents); err != nil {
			return err
		}
		destination := filepath.Join(root, filepath.FromSlash(repositoryPath))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("create directory for %q: %w", repositoryPath, err)
		}
		if err := os.Symlink(contents, destination); err != nil {
			return fmt.Errorf("write %q: %w", repositoryPath, err)
		}
		return nil
	})
}

func materializeTreeFile(mode filemode.FileMode, destination, contents string) error {
	switch mode {
	case filemode.Executable:
		return os.WriteFile(destination, []byte(contents), 0o700)
	default:
		return os.WriteFile(destination, []byte(contents), 0o600)
	}
}

func validateSymlink(repositoryPath, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("git source: symlink %q points outside the revision", repositoryPath)
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(repositoryPath), target)))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("git source: symlink %q points outside the revision", repositoryPath)
	}
	return nil
}

// shaIsHead reports whether sha is the current HEAD of the repository at dir.
func shaIsHead(dir, sha string) bool {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return false
	}
	defer func() { _ = r.Close() }()
	head, err := r.Head()
	if err != nil {
		return false
	}
	return head.Hash().String() == sha
}

// applyAuth derives the go-git transport auth method from Source.Auth
// (docs/ACCORDA.md §13, §15).
//
// For auth.type=ssh, it reads the private key from Source.Auth.Key into
// memory for go-git's SSH transport. For auth.type=https, it records the
// token for go-git's HTTP basic auth. An empty auth.type means "use the
// ambient environment" (SSH agent for ssh://, unauthenticated HTTPS).
func (g *Git) applyAuth() {
	g.auth = transportAuth{}
	switch g.Source.Auth.Type {
	case config.AuthSSH:
		key := strings.TrimSpace(g.Source.Auth.Key)
		if key == "" {
			return
		}
		keyBytes, err := os.ReadFile(key)
		if err != nil {
			g.auth.err = fmt.Errorf("git source: read auth.key: %w", err)
			return
		}
		user := strings.TrimSpace(g.Source.Auth.Username)
		if user == "" {
			user = defaultSSHUser(g.Source.URL)
		}
		g.auth.isSSH = true
		g.auth.sshUser = user
		g.auth.sshKey = keyBytes
		method, err := gossh.NewPublicKeys(user, keyBytes, "")
		if err != nil {
			g.auth.err = fmt.Errorf("git source: parse auth.key: %w", err)
			return
		}
		g.auth.method = method
	case config.AuthHTTPS:
		user := strings.TrimSpace(g.Source.Auth.Username)
		if user == "" {
			user = defaultHTTPSUser(g.Source.URL)
		}
		g.auth.isHTTPS = true
		g.auth.httpUser = user
		g.auth.httpToken = g.Source.Auth.Token
	}
}

// applyClientOptions configures the go-git client options with the derived
// auth method. For SSH it creates a PublicKeys auth from the in-memory key;
// for HTTPS it creates a BasicAuth from the token.
func (g *Git) applyClientOptions(opts *[]client.Option) error {
	if g.auth.err != nil {
		return g.auth.err
	}
	switch {
	case g.auth.isSSH:
		method, ok := g.auth.method.(*gossh.PublicKeys)
		if !ok || method == nil {
			return errors.New("git source: explicit SSH authentication is not initialized")
		}
		*opts = append(*opts, client.WithSSHAuth(method))
	case g.auth.isHTTPS:
		method := &http.BasicAuth{
			Username: g.auth.httpUser,
			Password: g.auth.httpToken,
		}
		*opts = append(*opts, client.WithHTTPAuth(method))
	}
	return nil
}

// defaultSSHUser returns the username to use for SSH auth when none is
// configured. It prefers any user embedded in the URL, otherwise "git".
func defaultSSHUser(rawURL string) string {
	if u, ok := urlUser(rawURL); ok && u != "" {
		return u
	}
	return "git"
}

// defaultHTTPSUser returns the username to use for HTTPS token auth when
// none is configured. It prefers any user embedded in the URL, otherwise
// "oauth2", the conventional username for token-based Git HTTPS auth.
func defaultHTTPSUser(rawURL string) string {
	if u, ok := urlUser(rawURL); ok && u != "" {
		return u
	}
	return "oauth2"
}

// repoExists reports whether dir looks like an existing Git repository.
func repoExists(dir string) (bool, error) {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.New("cache .git path must be a directory, not a symlink or file")
	}
	return true, nil
}

// repoDirName hashes a canonical, credential-free repository identity so
// distinct URLs cannot collide through lossy filename replacement.
func repoDirName(rawURL string) string {
	return namespacedRepoDirName(rawURL, "")
}

func namespacedRepoDirName(rawURL, namespace string) string {
	identity := canonicalRepositoryURL(rawURL)
	if namespace != "" {
		identity += "\x00" + namespace
	}
	sum := sha256.Sum256([]byte(identity))
	return "accorda-" + hex.EncodeToString(sum[:])
}

func canonicalRepositoryURL(rawURL string) string {
	s := strings.TrimSpace(RedactURL(rawURL))
	if !strings.Contains(s, "://") {
		if at := strings.LastIndex(s, "@"); at >= 0 {
			s = s[at+1:]
		}
		if colon := strings.Index(s, ":"); colon >= 0 {
			s = "ssh://" + strings.ToLower(s[:colon]) + "/" + s[colon+1:]
		} else {
			return filepath.Clean(s)
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	u.User = nil
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	u.Host = host
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git")
	return u.String()
}

// urlUser extracts the userinfo username from rawURL, if present.
func urlUser(rawURL string) (string, bool) {
	if i := strings.Index(rawURL, "://"); i >= 0 {
		rest := rawURL[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			if slash := strings.Index(rest, "/"); slash < 0 || at < slash {
				userinfo := rest[:at]
				if c := strings.Index(userinfo, ":"); c >= 0 {
					userinfo = userinfo[:c]
				}
				return userinfo, userinfo != ""
			}
		}
	}
	return "", false
}

// RedactURL returns rawURL with any userinfo (credentials) removed so it is
// safe to use in error messages, DesiredState.Repository, and other
// loggable identifiers (docs/ACCORDA.md §18, §56). When there is no
// userinfo, rawURL is returned unchanged.
//
// It is exported so callers outside the git package (for example the
// `accorda status` CLI command) redact a configured URL identically to the
// source, keeping credentials out of user-facing output even when the source
// cannot be read.
func RedactURL(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return rawURL
	}
	rest := rawURL[i+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return rawURL
	}
	if slash := strings.Index(rest, "/"); slash >= 0 && at > slash {
		return rawURL
	}
	return rawURL[:i+3] + rest[at+1:]
}

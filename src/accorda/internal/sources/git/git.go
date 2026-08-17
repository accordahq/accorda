package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/state"
	"accorda/internal/sources"
)

// Compile-time interface check: Git satisfies sources.Source here so a
// missing method is caught at build time, not at runtime.
var _ sources.Source = (*Git)(nil)

// Git is the generic Git source adapter (docs/ACCORDA.md §13).
//
// It clones or fetches a repository into a local cache, checks out the
// configured branch, and returns commit metadata. It never makes GitHub- or
// provider-specific calls; it shells out to the system `git` command, which
// handles SSH and HTTPS transport using the user's environment.
type Git struct {
	// Source is the validated source configuration.
	Source config.Source
	// CacheDir is the local path the repository is cloned into. If empty,
	// a directory under BaseDir named after the repo is used.
	CacheDir string
	// BaseDir is the root used to derive CacheDir when CacheDir is empty.
	// If both are empty, the system temp directory is used.
	BaseDir string
	// SSHCommand overrides the GIT_SSH_COMMAND used for SSH transports,
	// e.g. to point at a specific key file (§15 auth.type=ssh). If empty,
	// the user's environment is used unchanged.
	SSHCommand string

	// git is the command used to run git. It defaults to "git" and is
	// overridable in tests.
	git string
}

// New returns a Git source for the given configuration. The returned source
// is ready to Validate but does not touch the filesystem until Validate or
// Fetch is called.
func New(src config.Source, opts ...Option) *Git {
	g := &Git{Source: src, git: "git"}
	for _, opt := range opts {
		opt(g)
	}
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

// WithSSHCommand sets the GIT_SSH_COMMAND used for SSH transports, e.g.
// "ssh -i /etc/Accorda/git.key -o IdentitiesOnly=yes".
func WithSSHCommand(cmd string) Option {
	return func(g *Git) { g.SSHCommand = cmd }
}

// Validate checks the source configuration and that the git CLI is
// available. It does not clone or fetch (docs/ACCORDA.md §13, §6 fetch
// phase).
func (g *Git) Validate(ctx context.Context) error {
	if g == nil {
		return errors.New("git source: nil source")
	}
	if strings.TrimSpace(g.Source.URL) == "" {
		return errors.New("git source: url is required")
	}
	if strings.TrimSpace(g.Source.Branch) == "" {
		return errors.New("git source: branch is required")
	}
	if err := g.checkGitAvailable(ctx); err != nil {
		return err
	}
	return nil
}

// Fetch ensures the latest state of the configured branch is available
// locally and returns the commit it points to.
//
// If the cache directory does not contain a repository yet, Fetch clones it.
// Otherwise it fetches and checks out the configured branch. The returned
// Commit carries the SHA, branch, and authored time of HEAD.
func (g *Git) Fetch(ctx context.Context) (sources.Commit, error) {
	if err := g.ensureReady(ctx); err != nil {
		return sources.Commit{}, err
	}
	dir := g.cacheDir()
	exists, err := repoExists(dir)
	if err != nil {
		return sources.Commit{}, fmt.Errorf("git source: inspect cache: %w", err)
	}
	if !exists {
		if err := g.clone(ctx, dir); err != nil {
			return sources.Commit{}, err
		}
	} else {
		if err := g.fetch(ctx, dir); err != nil {
			return sources.Commit{}, err
		}
	}
	if err := g.checkout(ctx, dir, g.Source.Branch); err != nil {
		return sources.Commit{}, err
	}
	return g.headCommit(ctx, dir, g.Source.Branch)
}

// Desired returns the desired state declared in Git at the given commit. A
// nil ref means "use the fetched HEAD".
//
// The desired state is read from the Compose-style services file under the
// configured source path. The minimal parser here understands the flat
// `services.<name>.image` and `services.<name>.environment` form shown in
// docs/ACCORDA.md §9. If no services file is found, the returned desired
// state still carries the repository, branch, and commit metadata so that
// core can report the fetch outcome; Services will be empty.
func (g *Git) Desired(ctx context.Context, ref *sources.Commit) (*state.DesiredState, error) {
	if err := g.ensureReady(ctx); err != nil {
		return nil, err
	}
	commit, err := g.resolveCommit(ctx, ref)
	if err != nil {
		return nil, err
	}
	services, err := g.parseServices()
	if err != nil {
		return nil, err
	}
	return &state.DesiredState{
		Repository: g.Source.URL,
		Branch:     commit.Branch,
		Commit:     commit.SHA,
		CommitTime: commit.Time,
		Services:   services,
	}, nil
}

// ensureReady runs Validate and is shared by Fetch and Desired.
func (g *Git) ensureReady(ctx context.Context) error {
	if err := g.Validate(ctx); err != nil {
		return err
	}
	return nil
}

// checkGitAvailable confirms the git executable can be found.
func (g *Git) checkGitAvailable(ctx context.Context) error {
	cmd := g.command(ctx, "--version")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git source: git CLI not available: %w", err)
	}
	return nil
}

// cacheDir returns the configured cache directory or derives one under
// BaseDir (or the temp directory) named after the repository URL.
func (g *Git) cacheDir() string {
	if g.CacheDir != "" {
		return g.CacheDir
	}
	base := g.BaseDir
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, repoDirName(g.Source.URL))
}

// clone clones the configured URL into dir without checking out a branch
// yet; checkout is performed separately so the same code path is used for
// fresh clones and existing caches.
func (g *Git) clone(ctx context.Context, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("git source: create cache parent: %w", err)
	}
	parent, name := filepath.Split(dir)
	if name == "" {
		return fmt.Errorf("git source: invalid cache dir %q", dir)
	}
	args := []string{"clone", "--no-checkout", g.Source.URL, name}
	cmd := g.command(ctx, args...)
	cmd.Dir = parent
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git source: clone %q: %w: %s", g.Source.URL, err, bytes.TrimSpace(out))
	}
	return nil
}

// fetch fetches the configured branch from the origin remote.
func (g *Git) fetch(ctx context.Context, dir string) error {
	args := []string{"fetch", "origin", g.Source.Branch}
	cmd := g.command(ctx, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git source: fetch %q: %w: %s", g.Source.Branch, err, bytes.TrimSpace(out))
	}
	return nil
}

// checkout checks out the given branch and resets it to the fetched ref so
// the working tree matches the remote tip.
func (g *Git) checkout(ctx context.Context, dir, branch string) error {
	// Try to check out the local branch; if it does not exist, create it
	// tracking the remote.
	checkout := g.command(ctx, "checkout", branch)
	checkout.Dir = dir
	if out, err := checkout.CombinedOutput(); err != nil {
		create := g.command(ctx, "checkout", "-B", branch, "origin/"+branch)
		create.Dir = dir
		if out2, err2 := create.CombinedOutput(); err2 != nil {
			return fmt.Errorf("git source: checkout %q: %w: %s | %s", branch, err,
				bytes.TrimSpace(out), bytes.TrimSpace(out2))
		}
	}
	reset := g.command(ctx, "reset", "--hard", "origin/"+branch)
	reset.Dir = dir
	if out, err := reset.CombinedOutput(); err != nil {
		return fmt.Errorf("git source: reset to origin/%s: %w: %s", branch, err, bytes.TrimSpace(out))
	}
	return nil
}

// headCommit reads the SHA, branch, and authored time of HEAD.
func (g *Git) headCommit(ctx context.Context, dir, branch string) (sources.Commit, error) {
	sha, err := g.revParse(ctx, dir, "HEAD")
	if err != nil {
		return sources.Commit{}, err
	}
	when, err := g.commitTime(ctx, dir, "HEAD")
	if err != nil {
		return sources.Commit{}, err
	}
	return sources.Commit{SHA: sha, Branch: branch, Time: when}, nil
}

// resolveCommit returns the commit to read desired state from, defaulting to
// the current HEAD of the cache when ref is nil.
func (g *Git) resolveCommit(ctx context.Context, ref *sources.Commit) (sources.Commit, error) {
	if ref != nil && ref.SHA != "" {
		return *ref, nil
	}
	dir := g.cacheDir()
	if exists, err := repoExists(dir); err != nil || !exists {
		if err != nil {
			return sources.Commit{}, fmt.Errorf("git source: inspect cache: %w", err)
		}
		// Cache is empty; fetch first so HEAD is meaningful.
		if _, err := g.Fetch(ctx); err != nil {
			return sources.Commit{}, err
		}
	}
	return g.headCommit(ctx, dir, g.Source.Branch)
}

// revParse returns the commit SHA for ref.
func (g *Git) revParse(ctx context.Context, dir, ref string) (string, error) {
	cmd := g.command(ctx, "rev-parse", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git source: rev-parse %q: %w: %s", ref, err, bytes.TrimSpace(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// commitTime returns the authored time of ref as a UTC time.Time.
func (g *Git) commitTime(ctx context.Context, dir, ref string) (time.Time, error) {
	cmd := g.command(ctx, "log", "-1", "--format=%aI", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("git source: read commit time %q: %w: %s", ref, err, bytes.TrimSpace(out))
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("git source: parse commit time %q: %w", raw, err)
	}
	return t.UTC(), nil
}

// parseServices reads the services file under the configured source path
// from the checked-out working tree.
func (g *Git) parseServices() (map[string]state.Service, error) {
	path := servicesPath(g.Source.Path)
	dir := g.cacheDir()
	full := filepath.Join(dir, path)
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("git source: read %q: %w", path, err)
	}
	services, err := parseComposeServices(data)
	if err != nil {
		return nil, fmt.Errorf("git source: parse %q: %w", path, err)
	}
	return services, nil
}

// command builds an exec.Cmd for git with the given args, inheriting the
// caller's environment and injecting GIT_SSH_COMMAND when configured.
func (g *Git) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, g.git, args...)
	cmd.Env = os.Environ()
	if g.SSHCommand != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+g.SSHCommand)
	}
	return cmd
}

// repoExists reports whether dir looks like an existing Git repository.
func repoExists(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	_ = info
	return true, nil
}

// servicesPath returns the path to the services file relative to the repo
// root, defaulting to compose.yaml when the source path is empty.
func servicesPath(sourcePath string) string {
	p := strings.TrimSpace(sourcePath)
	if p == "" {
		return "compose.yaml"
	}
	if isComposeFile(p) {
		return p
	}
	return filepath.Join(p, "compose.yaml")
}

// isComposeFile reports whether path looks like a compose file name.
func isComposeFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	default:
		return false
	}
}

// repoDirName derives a filesystem-safe directory name from a repository
// URL by stripping scheme, credentials, and trailing .git.
func repoDirName(url string) string {
	s := url
	s = strings.TrimSpace(s)
	// Strip a leading scheme.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if strings.HasPrefix(s, "git@") {
		// ssh scp-like form: git@host:path -> host/path
		s = strings.Replace(s, ":", "/", 1)
		s = strings.TrimPrefix(s, "git@")
	}
	// Strip userinfo (e.g. "git@" from ssh://git@host/...).
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	s = strings.ReplaceAll(s, string(filepath.Separator), "-")
	if s == "" {
		s = "accorda-repo"
	}
	return "accorda-" + s
}

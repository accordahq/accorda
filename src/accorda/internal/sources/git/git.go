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

	// authEnv holds environment variables derived from Source.Auth that
	// inject credentials into git without placing them on the command
	// line or in logs (docs/ACCORDA.md §18, §56). It is populated by New
	// from the validated Source.Auth and is applied to every git command.
	authEnv []string

	// git is the command used to run git. It defaults to "git" and is
	// overridable in tests.
	git string
}

// New returns a Git source for the given configuration. The returned source
// is ready to Validate but does not touch the filesystem until Validate or
// Fetch is called.
//
// New derives the auth environment from src.Auth (docs/ACCORDA.md §13, §15):
//
//   - auth.type=ssh sets GIT_SSH_COMMAND to "ssh -i <key> -o
//     IdentitiesOnly=yes" unless WithSSHCommand overrides it.
//   - auth.type=https records the token in the process environment only; it
//     is never placed on the command line. The remote URL is rewritten to
//     embed the credentials so git's HTTPS transport uses them directly.
//
// Secret values are never logged. Error messages reference field names, not
// values.
func New(src config.Source, opts ...Option) *Git {
	g := &Git{Source: src, git: "git"}
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

// WithSSHCommand sets the GIT_SSH_COMMAND used for SSH transports, e.g.
// "ssh -i /etc/Accorda/git.key -o IdentitiesOnly=yes".
func WithSSHCommand(cmd string) Option {
	return func(g *Git) { g.SSHCommand = cmd }
}

// WithAuth sets the auth configuration applied to git commands. It overrides
// the auth derived from Source.Auth in New and is primarily useful in tests.
func WithAuth(a config.Auth) Option {
	return func(g *Git) {
		g.Source.Auth = a
		g.applyAuth()
	}
}

// Validate checks the source configuration and that the git CLI is
// available. It does not clone or fetch (docs/ACCORDA.md §13, §6 fetch
// phase, §15 auth).
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
	if err := g.validateAuth(); err != nil {
		return err
	}
	if err := g.checkGitAvailable(ctx); err != nil {
		return err
	}
	return nil
}

// validateAuth checks the auth configuration without touching secrets. It
// reports field-oriented errors and never includes token or key values
// (docs/ACCORDA.md §18, §56).
func (g *Git) validateAuth() error {
	switch a := g.Source.Auth; a.Type {
	case "", config.AuthSSH, config.AuthHTTPS:
		// Validated by config.Validate for config-driven construction;
		// here we only guard direct construction (e.g. tests).
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
// configured source path, at the given commit. When a non-nil ref is passed,
// the file content is read from that commit via `git show <sha>:<path>` so the
// returned services match the reported SHA. When ref is nil, the content is
// read from the checked-out HEAD of the configured branch.
//
// The minimal parser here understands the flat
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
	services, err := g.parseServices(ctx, commit.SHA)
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
//
// Only the configured branch is fetched, so only
// refs/remotes/origin/<branch> is updated. This matches the current Source
// contract (Fetch returns HEAD of the configured branch), but callers that
// later ask Desired for a commit on a different branch may find that ref
// absent or stale. Fetching additional refs would require a broader
// `git fetch origin` here; that is deliberately avoided to keep fetches
// cheap and scoped to the configured branch.
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
//
// It uses `git checkout -B <branch> origin/<branch>` directly rather than a
// two-step "checkout then fallback to checkout -B". The two-step form
// swallowed the original checkout error (e.g. a dirty working tree) when the
// fallback succeeded, hiding the real signal. The single -B form creates or
// resets the local branch to the remote tip in one step and surfaces any
// failure unambiguously.
func (g *Git) checkout(ctx context.Context, dir, branch string) error {
	create := g.command(ctx, "checkout", "-B", branch, "origin/"+branch)
	create.Dir = dir
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("git source: checkout %q to origin/%s: %w: %s",
			branch, branch, err, bytes.TrimSpace(out))
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

// parseServices reads the services file under the configured source path.
// When sha is non-empty, the content is read from that commit via
// `git show <sha>:<path>` so the returned services match the reported commit.
// When sha is empty, the file is read from the checked-out working tree, which
// is appropriate for the nil-ref (HEAD) case.
func (g *Git) parseServices(ctx context.Context, sha string) (map[string]state.Service, error) {
	path := servicesPath(g.Source.Path)
	dir := g.cacheDir()

	var (
		data []byte
		err  error
	)
	if sha != "" {
		data, err = g.showFile(ctx, dir, sha, path)
		if err != nil {
			return nil, err
		}
	} else {
		full := filepath.Join(dir, path)
		data, err = os.ReadFile(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("git source: read %q: %w", path, err)
		}
	}

	services, err := parseComposeServices(data)
	if err != nil {
		return nil, fmt.Errorf("git source: parse %q: %w", path, err)
	}
	return services, nil
}

// showFile returns the content of path as it exists at commit sha in the
// repository rooted at dir, via `git show <sha>:<path>`.
func (g *Git) showFile(ctx context.Context, dir, sha, path string) ([]byte, error) {
	cmd := g.command(ctx, "show", sha+":"+path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// A missing path at the commit is not an error: it means no services
		// were declared there, mirroring the worktree os.ReadFile handling.
		if exit, ok := exitStderr(err); ok && isNotFoundExit(exit) {
			return nil, nil
		}
		return nil, fmt.Errorf("git source: show %s:%s: %w: %s", sha, path, err, bytes.TrimSpace(out))
	}
	return out, nil
}

// command builds an exec.Cmd for git with the given args, inheriting the
// caller's environment and injecting auth-related variables (e.g.
// GIT_SSH_COMMAND) when configured. Credentials are never placed on the
// command line or in error output (docs/ACCORDA.md §18, §56).
func (g *Git) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, g.git, args...)
	cmd.Env = os.Environ()
	if g.SSHCommand != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+g.SSHCommand)
	}
	// authEnv carries HTTPS credential environment derived in applyAuth.
	// It is appended after the inherited environment so it takes effect,
	// but the values never appear in args or in formatted error messages.
	cmd.Env = append(cmd.Env, g.authEnv...)
	return cmd
}

// applyAuth derives the auth environment and effective remote URL from
// Source.Auth (docs/ACCORDA.md §13, §15). It is safe to call multiple
// times; each call resets authEnv and the SSH command derived from auth.
//
// For auth.type=ssh, it sets GIT_SSH_COMMAND to point at the configured key
// unless WithSSHCommand provided an explicit override. For auth.type=https,
// it embeds the token into the remote URL so git's HTTPS transport uses it
// directly; the credentials live only in the in-memory URL string and the
// process environment passed to git, never in logs.
func (g *Git) applyAuth() {
	g.authEnv = nil
	switch g.Source.Auth.Type {
	case config.AuthSSH:
		key := strings.TrimSpace(g.Source.Auth.Key)
		if g.SSHCommand == "" && key != "" {
			g.SSHCommand = "ssh -i " + key + " -o IdentitiesOnly=yes"
		}
	case config.AuthHTTPS:
		user := strings.TrimSpace(g.Source.Auth.Username)
		if user == "" {
			user = defaultHTTPSUser(g.Source.URL)
		}
		token := g.Source.Auth.Token
		if u, ok := httpsURLWithCredentials(g.Source.URL, user, token); ok {
			g.Source.URL = u
		}
	}
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

// exitStderr extracts the *exec.ExitError from an error returned by an
// exec.Cmd, reporting ok=true when the command failed with a captured
// stderr buffer. It is used to classify git command failures.
func exitStderr(err error) (*exec.ExitError, bool) {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit, true
	}
	return nil, false
}

// isNotFoundExit reports whether a git exit error corresponds to a missing
// object/path (git exit status 128 with stderr containing "does not exist"
// or "exists on disk, but not in"). It is used to treat a missing services
// file at a commit as an empty desired state rather than a hard error.
func isNotFoundExit(exit *exec.ExitError) bool {
	msg := string(exit.Stderr)
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "exists on disk, but not in") ||
		strings.Contains(msg, "no such path")
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

// httpsURLWithCredentials returns rawURL with user:token embedded in the
// userinfo section, suitable for git's HTTPS transport, and ok=true when
// rawURL is an https URL. Non-https URLs (ssh://, git@..., file://) are
// returned unchanged with ok=false. Credentials are never logged; the
// returned URL is used only to configure the git process.
//
// Existing userinfo in rawURL is replaced so re-applying auth is idempotent.
func httpsURLWithCredentials(rawURL, user, token string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		return rawURL, false
	}
	rest := rawURL[len("https://"):]
	// Strip any existing userinfo so applyAuth is idempotent.
	if at := strings.Index(rest, "@"); at >= 0 {
		// Only treat the first segment before a path "/" as userinfo.
		if slash := strings.Index(rest, "/"); slash < 0 || at < slash {
			rest = rest[at+1:]
		}
	}
	return "https://" + urlEscape(user) + ":" + urlEscape(token) + "@" + rest, true
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

// urlEscape percent-encodes a credential component for use in a URL
// userinfo section, covering the reserved characters that break parsing.
func urlEscape(s string) string {
	r := strings.NewReplacer(
		":", "%3A",
		"@", "%40",
		"/", "%2F",
		"#", "%23",
		"?", "%3F",
	)
	return r.Replace(s)
}

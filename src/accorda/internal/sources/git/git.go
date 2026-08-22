package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
	gossh "github.com/go-git/go-git/v6/plumbing/transport/ssh"

	"accorda/internal/config"
	"accorda/internal/core/state"
	"accorda/internal/sources"
	"accorda/internal/targets/compose"
)

// Compile-time interface check: Git satisfies sources.Source here so a
// missing method is caught at build time, not at runtime.
var _ sources.Source = (*Git)(nil)

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
	// If both are empty, the system temp directory is used.
	BaseDir string
	// SSHCommand overrides the GIT_SSH_COMMAND used for SSH transports.
	// Kept for API compatibility; with go-git the SSH key is read from
	// Source.Auth.Key directly.
	SSHCommand string

	// auth holds the go-git transport auth method derived from Source.Auth
	// in New. It is applied to clone and fetch operations. Secret values
	// are never logged (docs/ACCORDA.md §18, §56).
	auth transportAuth
}

// transportAuth carries the go-git auth method and a flag distinguishing SSH
// from HTTPS so callers can avoid importing the transport packages.
type transportAuth struct {
	method    any
	isSSH     bool
	isHTTPS   bool
	sshUser   string
	sshKey    []byte
	sshPass   string
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
	g := &Git{Source: src}
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

// WithSSHCommand is kept for API compatibility. With go-git the SSH key is
// read from Source.Auth.Key; the command string is no longer used.
func WithSSHCommand(_ string) Option {
	// No-op: go-git reads the SSH key from Source.Auth.Key directly, so the
	// GIT_SSH_COMMAND string that the previous CLI-based implementation used
	// is no longer needed. The option is retained so existing callers compile.
	return func(*Git) {}
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
// (docs/ACCORDA.md §13, §6 fetch phase, §15 auth).
func (g *Git) Validate(_ context.Context) error {
	if g == nil {
		return errors.New("git source: nil source")
	}
	if strings.TrimSpace(g.Source.URL) == "" {
		return errors.New("git source: url is required")
	}
	if strings.TrimSpace(g.Source.Branch) == "" {
		return errors.New("git source: branch is required")
	}
	return g.validateAuth()
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
// The desired state is read from the Compose services file under the
// configured source path, at the given commit. When a non-nil ref is passed,
// the file content is read from that commit's tree so the returned services
// match the reported SHA. When ref is nil, the content is read from the
// checked-out HEAD of the configured branch.
//
// The services file is parsed using the compose-go loader
// (github.com/compose-spec/compose-go/v2), which handles the full Compose
// schema including interpolation, extends, and profiles. If no services
// file is found, the returned desired state still carries the repository,
// branch, and commit metadata so core can report the fetch outcome;
// Services will be empty.
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
		Repository: RedactURL(g.Source.URL),
		Branch:     commit.Branch,
		Commit:     commit.SHA,
		CommitTime: commit.Time,
		Services:   services,
	}, nil
}

// ensureReady runs Validate and is shared by Fetch and Desired.
func (g *Git) ensureReady(ctx context.Context) error {
	return g.Validate(ctx)
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

// clone clones the configured URL into dir using go-git. Auth is applied
// via the transport method derived in applyAuth. The error message
// references the clean Source.URL so credentials are never leaked
// (docs/ACCORDA.md §18, §56).
func (g *Git) clone(ctx context.Context, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("git source: create cache parent: %w", err)
	}
	cloneOpts := &git.CloneOptions{
		URL:           g.Source.URL,
		RemoteName:    "origin",
		ReferenceName: plumbing.NewBranchReferenceName(g.Source.Branch),
		SingleBranch:  true,
		NoCheckout:    true,
	}
	g.applyClientOptions(&cloneOpts.ClientOptions)
	if _, err := git.PlainCloneContext(ctx, dir, cloneOpts); err != nil {
		return fmt.Errorf("git source: clone %q: %w", RedactURL(g.Source.URL), err)
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
	fetchOpts := &git.FetchOptions{
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec("+refs/heads/" + g.Source.Branch + ":refs/remotes/origin/" + g.Source.Branch),
		},
	}
	g.applyClientOptions(&fetchOpts.ClientOptions)
	if err := remote.FetchContext(ctx, fetchOpts); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("git source: fetch %q: %w", g.Source.Branch, err)
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
		Branch: branch,
		Time:   commit.Author.When.UTC(),
	}, nil
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

// parseServices reads the services file under the configured source path.
// When sha is non-empty, the content is read from that commit's tree so the
// returned services match the reported commit. When sha is empty, the file
// is read from the checked-out working tree, which is appropriate for the
// nil-ref (HEAD) case.
//
// The file is parsed using the compose-go loader, which handles the full
// Compose schema. The loader requires a working directory for path
// resolution; for a file read at a specific commit, the content is extracted
// from the commit tree and loaded from an in-memory file set.
func (g *Git) parseServices(ctx context.Context, sha string) (map[string]state.Service, error) {
	path := servicesPath(g.Source.Path)
	dir := g.cacheDir()

	data, err := g.readServicesFile(ctx, dir, sha, path)
	if err != nil {
		return nil, err
	}
	if data == nil {
		// No services file found; return an empty set.
		return map[string]state.Service{}, nil
	}
	services, err := compose.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("git source: parse %q: %w", path, err)
	}
	return services, nil
}

// readServicesFile returns the raw content of the services file. When sha is
// non-empty, the content is read from that commit's tree via go-git; when sha
// is empty, the file is read from the checked-out worktree.
func (g *Git) readServicesFile(ctx context.Context, dir, sha, path string) ([]byte, error) {
	if sha == "" {
		full := filepath.Join(dir, path)
		data, err := os.ReadFile(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("git source: read %q: %w", path, err)
		}
		return data, nil
	}
	return g.readFileAtCommit(ctx, dir, sha, path)
}

// readFileAtCommit reads a file's content from a specific commit's tree
// using go-git, replacing the previous `git show <sha>:<path>` approach.
func (g *Git) readFileAtCommit(_ context.Context, dir, sha, path string) ([]byte, error) {
	r, err := git.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("git source: open cache: %w", err)
	}
	defer func() { _ = r.Close() }()
	hash := plumbing.NewHash(sha)
	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("git source: read commit %s: %w", sha, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("git source: read tree: %w", err)
	}
	file, err := tree.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("git source: find %q: %w", path, err)
	}
	reader, err := file.Blob.Reader()
	if err != nil {
		return nil, fmt.Errorf("git source: open %q: %w", path, err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("git source: read %q: %w", path, err)
	}
	return data, nil
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
			// Defer the error to clone/fetch time; don't fail construction.
			return
		}
		user := strings.TrimSpace(g.Source.Auth.Username)
		if user == "" {
			user = defaultSSHUser(g.Source.URL)
		}
		g.auth.isSSH = true
		g.auth.sshUser = user
		g.auth.sshKey = keyBytes
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
func (g *Git) applyClientOptions(opts *[]client.Option) {
	switch {
	case g.auth.isSSH:
		method, err := gossh.NewPublicKeys(g.auth.sshUser, g.auth.sshKey, g.auth.sshPass)
		if err != nil {
			return
		}
		*opts = append(*opts, client.WithSSHAuth(method))
	case g.auth.isHTTPS:
		method := &http.BasicAuth{
			Username: g.auth.httpUser,
			Password: g.auth.httpToken,
		}
		*opts = append(*opts, client.WithHTTPAuth(method))
	}
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
// root, defaulting to config.DefaultComposeFile when the source path is empty.
func servicesPath(sourcePath string) string {
	p := strings.TrimSpace(sourcePath)
	if p == "" {
		return config.DefaultComposeFile
	}
	if isComposeFile(p) {
		return p
	}
	return filepath.Join(p, config.DefaultComposeFile)
}

// isComposeFile reports whether path looks like a compose file name.
func isComposeFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case config.DefaultComposeFile, "compose.yml", "docker-compose.yaml", "docker-compose.yml":
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
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if strings.HasPrefix(s, "git@") {
		s = strings.Replace(s, ":", "/", 1)
		s = strings.TrimPrefix(s, "git@")
	}
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

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitlib "github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"

	"accorda/internal/config"
)

func TestFetchAndDesiredRejectInvalidSource(t *testing.T) {
	g := New(config.Source{})
	if _, err := g.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("Fetch() error = %v, want URL validation error", err)
	}
	if _, err := g.Desired(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("Desired() error = %v, want URL validation error", err)
	}
}

func TestGitOperationOpenErrors(t *testing.T) {
	g := New(config.Source{URL: "https://example.com/repo.git", Branch: "main"})
	missing := filepath.Join(t.TempDir(), "missing")
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "fetch", run: func() error { return g.fetch(context.Background(), missing) }, want: "open cache"},
		{name: "checkout", run: func() error { return g.checkout(context.Background(), missing, "main") }, want: "open cache"},
		{name: "head", run: func() error { _, err := g.headCommit(context.Background(), missing, "main"); return err }, want: "open cache"},
		{name: "commit file", run: func() error {
			_, err := g.readFileAtCommit(context.Background(), missing, "abc", "compose.yaml")
			return err
		}, want: "open cache"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("operation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCloneReportsCacheParentError(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write parent: %v", err)
	}
	g := New(config.Source{URL: "https://example.com/repo.git", Branch: "main"})
	if err := g.clone(context.Background(), filepath.Join(parent, "repo")); err == nil || !strings.Contains(err.Error(), "create cache parent") {
		t.Errorf("clone() error = %v, want cache-parent error", err)
	}
}

func TestFetchRejectsPreExistingNonRepositoryCache(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "user-owned")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	g := New(config.Source{URL: "https://example.com/repo.git", Branch: "main"}, WithCacheDir(dir))
	if _, err := g.Fetch(t.Context()); err == nil {
		t.Fatal("Fetch() error = nil, want pre-existing cache rejection")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("pre-existing cache content = %q, %v; want preserved", data, err)
	}
}

func TestFetchReportsCacheAndRepositoryErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (string, string)
		want  string
	}{
		{
			name: "unsafe cache path",
			setup: func(t *testing.T) (string, string) {
				path := filepath.Join(t.TempDir(), "cache")
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write cache file: %v", err)
				}
				return path, "https://example.com/repo.git"
			},
			want: "cache path",
		},
		{
			name: "unsafe git metadata",
			setup: func(t *testing.T) (string, string) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write .git file: %v", err)
				}
				return dir, "https://example.com/repo.git"
			},
			want: "inspect cache",
		},
		{
			name: "missing origin",
			setup: func(t *testing.T) (string, string) {
				dir := initCacheRepository(t)
				return dir, "https://example.com/repo.git"
			},
			want: "origin remote",
		},
		{
			name: "fetch failure",
			setup: func(t *testing.T) (string, string) {
				dir := initCacheRepository(t)
				url := filepath.Join(t.TempDir(), "missing-origin")
				addOrigin(t, dir, url)
				return dir, url
			},
			want: "fetch \"main\"",
		},
		{
			name: "checkout failure",
			setup: func(t *testing.T) (string, string) {
				dir := initCacheRepository(t)
				commitCacheBranch(t, dir, "main")
				addOrigin(t, dir, dir)
				corruptRepositoryIndex(t, dir)
				return dir, dir
			},
			want: "index",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, url := tc.setup(t)
			g := New(config.Source{URL: url, Branch: "main"}, WithCacheDir(dir))
			if _, err := g.Fetch(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Fetch() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func initCacheRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gitlib.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("close repository: %v", err)
	}
	return dir
}

func addOrigin(t *testing.T, dir, url string) {
	t.Helper()
	repo, err := gitlib.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	defer func() { _ = repo.Close() }()
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
}

func commitCacheBranch(t *testing.T, dir, branch string) {
	t.Helper()
	repo, err := gitlib.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	defer func() { _ = repo.Close() }()
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("cache fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	hash, err := worktree.Commit("fixture", &gitlib.CommitOptions{Author: &object.Signature{
		Name: "Accorda Test", Email: "test@accorda.dev", When: time.Unix(1, 0).UTC(),
	}})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName(branch), hash)); err != nil {
		t.Fatalf("set branch: %v", err)
	}
}

func corruptRepositoryIndex(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, ".git", "index")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("create invalid index: %v", err)
	}
}

func TestResolveCommitReportsCacheInspectionError(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write parent: %v", err)
	}
	g := New(
		config.Source{URL: "https://example.com/repo.git", Branch: "main"},
		WithCacheDir(filepath.Join(parent, "repo")),
	)
	if _, err := g.resolveCommit(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "inspect cache") {
		t.Errorf("resolveCommit() error = %v, want cache inspection error", err)
	}
}

func TestReadServicesFileFromWorktree(t *testing.T) {
	dir := t.TempDir()
	g := New(config.Source{URL: "https://example.com/repo.git", Branch: "main"}, WithCacheDir(dir))
	if data, err := g.readServicesFile(context.Background(), dir, "", "missing.yaml"); err != nil || data != nil {
		t.Errorf("read missing file = %q, %v; want nil, nil", data, err)
	}
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	if data, err := g.readServicesFile(context.Background(), dir, "", "compose.yaml"); err != nil || string(data) != "services: {}\n" {
		t.Errorf("read compose = %q, %v", data, err)
	}
	if _, err := g.readServicesFile(context.Background(), dir, "", "."); err == nil || !strings.Contains(err.Error(), "git source: read") {
		t.Errorf("read directory error = %v", err)
	}
}

func TestParseServicesMissingAndInvalid(t *testing.T) {
	dir := t.TempDir()
	g := New(config.Source{URL: "https://example.com/repo.git", Branch: "main"}, WithCacheDir(dir))
	services, err := g.parseServices(context.Background(), "")
	if err != nil || len(services) != 0 {
		t.Errorf("parse missing services = %v, %v; want empty", services, err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.DefaultComposeFile), []byte("services: ["), 0o600); err != nil {
		t.Fatalf("write invalid compose: %v", err)
	}
	if _, err := g.parseServices(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "git source: parse") {
		t.Errorf("parseServices() error = %v, want parse error", err)
	}
}

func TestApplyClientOptions(t *testing.T) {
	tests := []struct {
		name string
		git  *Git
		want int
	}{
		{name: "ambient", git: New(config.Source{}), want: 0},
		{name: "HTTPS", git: New(config.Source{Auth: config.Auth{Type: config.AuthHTTPS, Token: "token"}}), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []client.Option
			if err := tt.git.applyClientOptions(&opts); err != nil {
				t.Fatalf("applyClientOptions: %v", err)
			}
			if len(opts) != tt.want {
				t.Errorf("options = %d, want %d", len(opts), tt.want)
			}
		})
	}
}

func TestApplyClientOptionsRejectsInvalidSSHState(t *testing.T) {
	tests := []struct {
		name string
		auth transportAuth
		want string
	}{
		{name: "stored auth error", auth: transportAuth{err: errors.New("key failed")}, want: "key failed"},
		{name: "missing SSH method", auth: transportAuth{isSSH: true}, want: "not initialized"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &Git{auth: tc.auth}
			var opts []client.Option
			if err := g.applyClientOptions(&opts); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("applyClientOptions() error = %v, want %q", err, tc.want)
			}
			if len(opts) != 0 {
				t.Fatalf("options = %d, want none", len(opts))
			}
		})
	}
}

func TestCacheDirDefaultsToUserCache(t *testing.T) {
	g := New(config.Source{URL: "https://example.com/repo.git", Branch: "main"})
	if got := g.cacheDir(); filepath.Dir(got) != filepath.Clean(defaultCacheBase()) {
		t.Errorf("cacheDir() = %q, want path under %q", got, defaultCacheBase())
	}
}

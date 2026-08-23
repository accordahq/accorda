package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"

	"accorda/internal/config"
)

func TestOptions(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	baseDir := t.TempDir()
	g := New(
		config.Source{URL: "https://example.com/acme/repo.git", Branch: "main"},
		WithCacheDir(cacheDir),
		WithBaseDir(baseDir),
		WithSSHCommand("ignored"),
		WithAuth(config.Auth{Type: config.AuthHTTPS, Token: "secret"}),
	)
	if g.CacheDir != cacheDir || g.BaseDir != baseDir {
		t.Errorf("New options = CacheDir %q, BaseDir %q", g.CacheDir, g.BaseDir)
	}
	if !g.auth.isHTTPS || g.auth.httpToken != "secret" {
		t.Errorf("WithAuth() did not apply HTTPS auth: %#v", g.auth)
	}
}

func TestCacheDir(t *testing.T) {
	baseDir := t.TempDir()
	g := New(config.Source{URL: "https://example.com/acme/repo.git", Branch: "main"}, WithBaseDir(baseDir))
	want := filepath.Join(baseDir, repoDirName("https://example.com/acme/repo.git"))
	if got := g.cacheDir(); got != want {
		t.Errorf("cacheDir() = %q, want %q", got, want)
	}
}

func TestCacheDir_CollidingLegacyURLsAreDistinct(t *testing.T) {
	baseDir := t.TempDir()
	first := New(config.Source{URL: "https://git.internal/acme/prod", Branch: "main"}, WithBaseDir(baseDir))
	second := New(config.Source{URL: "https://git.internal/acme-prod", Branch: "main"}, WithBaseDir(baseDir))
	if first.cacheDir() == second.cacheDir() {
		t.Fatalf("cache paths collide: %q", first.cacheDir())
	}
}

func TestVerifyOrigin_RejectsMismatchedCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	defer func() { _ = repo.Close() }()
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: "origin", URLs: []string{"https://git.internal/acme/other.git"},
	}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	g := New(config.Source{URL: "https://git.internal/acme/prod.git", Branch: "main"}, WithCacheDir(dir))
	if err := g.verifyOrigin(dir); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("verifyOrigin() error = %v, want mismatch", err)
	}
}

func TestVerifyOrigin_AcceptsExactMatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	defer func() { _ = repo.Close() }()
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: "origin", URLs: []string{"https://git.internal/acme/prod.git"},
	}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	g := New(config.Source{URL: "https://git.internal/acme/prod.git", Branch: "main"}, WithCacheDir(dir))
	if err := g.verifyOrigin(dir); err != nil {
		t.Fatalf("verifyOrigin() error = %v, want nil", err)
	}
}

func TestVerifyOrigin_RejectsDifferentSSHUser(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	defer func() { _ = repo.Close() }()
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: "origin", URLs: []string{"ssh://git@git.internal/acme/prod.git"},
	}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	g := New(config.Source{URL: "ssh://deploy@git.internal/acme/prod.git", Branch: "main"}, WithCacheDir(dir))
	if err := g.verifyOrigin(dir); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("verifyOrigin() error = %v, want exact-origin mismatch", err)
	}
}

func TestSecureCacheDir_RejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(root, "cache")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := secureCacheDir(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("secureCacheDir() error = %v, want symlink rejection", err)
	}
}

func TestSecureCacheDir_RestrictsPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := secureCacheDir(dir); err != nil {
		t.Fatalf("secureCacheDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("cache permissions = %o, want 700", got)
	}
}

func TestEnsurePrivateCacheParent_RejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(root, "parent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := ensurePrivateCacheParent(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ensurePrivateCacheParent() error = %v, want symlink rejection", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		git  *Git
		want string
	}{
		{name: "nil", git: nil, want: "nil source"},
		{name: "URL", git: &Git{Source: config.Source{Branch: "main"}}, want: "url is required"},
		{name: "branch", git: &Git{Source: config.Source{URL: "https://example.com/repo"}}, want: "branch is required"},
		{name: "valid", git: New(config.Source{URL: "https://example.com/repo", Branch: "main"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.git.Validate(context.Background())
			if tt.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Errorf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRepoExists(t *testing.T) {
	dir := t.TempDir()
	if exists, err := repoExists(dir); err != nil || exists {
		t.Errorf("repoExists(empty) = %v, %v; want false, nil", exists, err)
	}
	gitPath := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitPath, 0o700); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if exists, err := repoExists(dir); err != nil || !exists {
		t.Errorf("repoExists(.git) = %v, %v; want true, nil", exists, err)
	}
}

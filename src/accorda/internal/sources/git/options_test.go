package git

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
	want := filepath.Join(baseDir, "accorda-example.com-acme-repo")
	if got := g.cacheDir(); got != want {
		t.Errorf("cacheDir() = %q, want %q", got, want)
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
	if err := writeFile(gitPath, "gitdir"); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	if exists, err := repoExists(dir); err != nil || !exists {
		t.Errorf("repoExists(.git) = %v, %v; want true, nil", exists, err)
	}
}

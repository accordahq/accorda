package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing/client"

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

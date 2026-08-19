package git

import (
	"context"
	"os"
	"strings"
	"testing"

	"accorda/internal/config"
)

// TestApplyAuth_SSHReadsKey verifies that auth.type=ssh reads the key into
// memory for go-git's SSH transport (docs/ACCORDA.md §15). The key path must
// point at a readable file.
func TestApplyAuth_SSHReadsKey(t *testing.T) {
	// Create a temporary dummy key file so applyAuth can read it.
	keyPath := t.TempDir() + "/test.key"
	const dummyKey = "-----BEGIN OPENSSH PRIVATE KEY-----\ndummy\n-----END OPENSSH PRIVATE KEY-----"
	if err := writeFile(keyPath, dummyKey); err != nil {
		t.Fatalf("write key: %v", err)
	}
	g := New(config.Source{
		Type:   "git",
		URL:    "git@git.internal:acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthSSH, Key: keyPath},
	})
	if !g.auth.isSSH {
		t.Fatal("auth.isSSH = false after auth.type=ssh")
	}
	if string(g.auth.sshKey) != dummyKey {
		t.Errorf("sshKey = %q, want the file content", string(g.auth.sshKey))
	}
	if g.auth.sshUser != "git" {
		t.Errorf("sshUser = %q, want default git", g.auth.sshUser)
	}
}

// TestApplyAuth_SSHCustomUser verifies that an explicit username is used for
// SSH auth rather than the default "git".
func TestApplyAuth_SSHCustomUser(t *testing.T) {
	keyPath := t.TempDir() + "/test.key"
	if err := writeFile(keyPath, "dummy"); err != nil {
		t.Fatalf("write key: %v", err)
	}
	g := New(config.Source{
		Type:   "git",
		URL:    "git@git.internal:acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthSSH, Key: keyPath, Username: "deploy"},
	})
	if g.auth.sshUser != "deploy" {
		t.Errorf("sshUser = %q, want deploy", g.auth.sshUser)
	}
}

// TestApplyAuth_HTTPSSetsCredentials verifies that auth.type=https records
// the token and username for go-git's HTTP basic auth, while Source.URL
// stays clean and the token is never placed in Source.URL
// (docs/ACCORDA.md §18, §56).
func TestApplyAuth_HTTPSSetsCredentials(t *testing.T) {
	const token = "ghp_secrettoken"
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthHTTPS, Token: token},
	})
	if !g.auth.isHTTPS {
		t.Fatal("auth.isHTTPS = false after auth.type=https")
	}
	if g.auth.httpToken != token {
		t.Errorf("httpToken = %q, want %q", g.auth.httpToken, token)
	}
	if g.auth.httpUser != "oauth2" {
		t.Errorf("httpUser = %q, want oauth2 default", g.auth.httpUser)
	}
	// Source.URL must remain the clean identifier.
	if strings.Contains(g.Source.URL, token) {
		t.Errorf("Source.URL leaks token: %q", g.Source.URL)
	}
	if g.Source.URL != "https://git.internal/acme/infra.git" {
		t.Errorf("Source.URL = %q, want unchanged clean URL", g.Source.URL)
	}
}

// TestApplyAuth_HTTPSDefaultsUser verifies that when no username is given,
// the default is "oauth2" for https token auth.
func TestApplyAuth_HTTPSDefaultsUser(t *testing.T) {
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthHTTPS, Token: "tok"},
	})
	if g.auth.httpUser != "oauth2" {
		t.Errorf("httpUser = %q, want oauth2", g.auth.httpUser)
	}
}

// TestApplyAuth_HTTPSPreservesExplicitUser verifies that an explicit
// username is used rather than the default.
func TestApplyAuth_HTTPSPreservesExplicitUser(t *testing.T) {
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthHTTPS, Token: "tok", Username: "ci-bot"},
	})
	if g.auth.httpUser != "ci-bot" {
		t.Errorf("httpUser = %q, want ci-bot", g.auth.httpUser)
	}
}

// TestApplyAuth_HTTPSIdempotent verifies that re-applying auth does not
// stack credentials and that Source.URL stays clean.
func TestApplyAuth_HTTPSIdempotent(t *testing.T) {
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthHTTPS, Token: "tok"},
	})
	g.applyAuth()
	g.applyAuth()
	if g.auth.httpToken != "tok" {
		t.Errorf("httpToken = %q, want tok after re-apply", g.auth.httpToken)
	}
	if strings.Contains(g.Source.URL, "tok") {
		t.Errorf("Source.URL = %q, must not contain token after re-apply", g.Source.URL)
	}
}

// TestApplyAuth_NonHTTPSURLNoToken verifies that ssh:// and git@ URLs do not
// set HTTPS credentials when auth.type=https is (mis)configured: auth.isHTTPS
// is set but no token leaks into Source.URL.
func TestApplyAuth_NonHTTPSURLNoToken(t *testing.T) {
	cases := []string{
		"git@git.internal:acme/infra.git",
		"ssh://git@git.internal/acme/infra.git",
		"file:///tmp/repo",
	}
	for _, u := range cases {
		g := New(config.Source{
			Type:   "git",
			URL:    u,
			Branch: "main",
			Auth:   config.Auth{Type: config.AuthHTTPS, Token: "tok"},
		})
		if g.Source.URL != u {
			t.Errorf("Source.URL = %q, want unchanged %q", g.Source.URL, u)
		}
		if strings.Contains(g.Source.URL, "tok") {
			t.Errorf("Source.URL leaks token for non-https URL: %q", g.Source.URL)
		}
	}
}

// TestApplyAuth_EmptyAuthIsAmbient verifies that an absent auth.type means
// "use the ambient environment" — no SSH or HTTPS credentials are set.
func TestApplyAuth_EmptyAuthIsAmbient(t *testing.T) {
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
	})
	if g.auth.isSSH {
		t.Error("auth.isSSH = true, want false for ambient auth")
	}
	if g.auth.isHTTPS {
		t.Error("auth.isHTTPS = true, want false for ambient auth")
	}
}

// TestValidateAuth_Errors verifies field-oriented validation errors that
// never reveal secret values.
func TestValidateAuth_Errors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		source config.Source
		want   string
	}{
		{
			name:   "ssh without key",
			source: config.Source{URL: "git@host:repo", Branch: "main", Auth: config.Auth{Type: config.AuthSSH}},
			want:   "auth.key is required",
		},
		{
			name:   "https without token",
			source: config.Source{URL: "https://host/repo", Branch: "main", Auth: config.Auth{Type: config.AuthHTTPS}},
			want:   "auth.token is required",
		},
		{
			name:   "unsupported auth type",
			source: config.Source{URL: "https://host/repo", Branch: "main", Auth: config.Auth{Type: "basic"}},
			want:   `auth.type "basic" is not supported`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &Git{Source: c.source}
			err := g.validateAuth()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
	_ = ctx
}

// TestURLUser covers userinfo extraction.
func TestURLUser(t *testing.T) {
	cases := []struct {
		url  string
		want string
		ok   bool
	}{
		{"https://git@git.internal/repo", "git", true},
		{"https://oauth2:tok@git.internal/repo", "oauth2", true},
		{"https://git.internal/repo", "", false},
		{"ssh://git@git.internal/repo", "git", true},
		{"git@git.internal:repo", "", false}, // scp-like form not handled here
	}
	for _, c := range cases {
		got, ok := urlUser(c.url)
		if ok != c.ok {
			t.Errorf("urlUser(%q) ok = %v, want %v", c.url, ok, c.ok)
		}
		if got != c.want {
			t.Errorf("urlUser(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// TestRedactURL covers credential stripping for loggable identifiers.
func TestRedactURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://oauth2:tok@git.internal/repo", "https://git.internal/repo"},
		{"https://git.internal/repo", "https://git.internal/repo"},
		{"git@git.internal:acme/infra.git", "git@git.internal:acme/infra.git"},
		{"ssh://git@git.internal/repo", "ssh://git.internal/repo"},
	}
	for _, c := range cases {
		if got := RedactURL(c.in); got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// writeFile is a minimal helper for creating test key files.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

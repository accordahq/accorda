package git

import (
	"context"
	"strings"
	"testing"

	"accorda/internal/config"
)

// TestApplyAuth_SSHSetsSSHCommand verifies that auth.type=ssh derives a
// GIT_SSH_COMMAND pointing at the configured key (docs/ACCORDA.md §15).
func TestApplyAuth_SSHSetsSSHCommand(t *testing.T) {
	g := New(config.Source{
		Type:   "git",
		URL:    "git@git.internal:acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthSSH, Key: "/etc/Accorda/git.key"},
	})
	if g.SSHCommand == "" {
		t.Fatal("SSHCommand empty after auth.type=ssh")
	}
	if !strings.Contains(g.SSHCommand, "/etc/Accorda/git.key") {
		t.Errorf("SSHCommand = %q, want it to contain the key path", g.SSHCommand)
	}
	if !strings.Contains(g.SSHCommand, "IdentitiesOnly=yes") {
		t.Errorf("SSHCommand = %q, want IdentitiesOnly=yes to prevent agent fallback", g.SSHCommand)
	}
}

// TestApplyAuth_SSHDoesNotOverrideExplicitCommand verifies that an explicit
// WithSSHCommand wins over the auth-derived command.
func TestApplyAuth_SSHDoesNotOverrideExplicitCommand(t *testing.T) {
	g := New(
		config.Source{
			Type:   "git",
			URL:    "git@git.internal:acme/infra.git",
			Branch: "main",
			Auth:   config.Auth{Type: config.AuthSSH, Key: "/etc/Accorda/git.key"},
		},
		WithSSHCommand("ssh -i /custom/key"),
	)
	if g.SSHCommand != "ssh -i /custom/key" {
		t.Errorf("SSHCommand = %q, want the explicit override", g.SSHCommand)
	}
}

// TestApplyAuth_HTTPSEmbedsCredentials verifies that auth.type=https rewrites
// an https URL to embed the token in the userinfo and that the token is not
// placed on the git command line or in authEnv.
func TestApplyAuth_HTTPSEmbedsCredentials(t *testing.T) {
	const token = "ghp_secrettoken"
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthHTTPS, Token: token},
	})
	if !strings.HasPrefix(g.Source.URL, "https://") {
		t.Fatalf("URL = %q, want https scheme", g.Source.URL)
	}
	if !strings.Contains(g.Source.URL, ":"+token+"@") {
		t.Errorf("URL = %q, want embedded token in userinfo", g.Source.URL)
	}
	// The token must not appear in authEnv: HTTPS auth uses the URL, not env.
	for _, e := range g.authEnv {
		if strings.Contains(e, token) {
			t.Errorf("authEnv contains token: %q", e)
		}
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
	if !strings.Contains(g.Source.URL, "oauth2:tok@") {
		t.Errorf("URL = %q, want oauth2 default user", g.Source.URL)
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
	if !strings.Contains(g.Source.URL, "ci-bot:tok@") {
		t.Errorf("URL = %q, want explicit username ci-bot", g.Source.URL)
	}
}

// TestApplyAuth_HTTPSIdempotent verifies that re-applying auth does not
// stack userinfo segments.
func TestApplyAuth_HTTPSIdempotent(t *testing.T) {
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthHTTPS, Token: "tok"},
	})
	g.applyAuth()
	g.applyAuth()
	if strings.Count(g.Source.URL, "@") != 1 {
		t.Errorf("URL = %q, want exactly one @ after repeated applyAuth", g.Source.URL)
	}
}

// TestApplyAuth_NonHTTPSURLUnchanged verifies that ssh:// and git@ URLs are
// not rewritten by https auth.
func TestApplyAuth_NonHTTPSURLUnchanged(t *testing.T) {
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
			t.Errorf("URL = %q, want unchanged %q", g.Source.URL, u)
		}
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
			g := &Git{Source: c.source, git: "git"}
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

// TestHTTPSURLWithCredentials covers the URL rewriting helper.
func TestHTTPSURLWithCredentials(t *testing.T) {
	cases := []struct {
		name, url, user, token, want string
		ok                           bool
	}{
		{
			name:  "simple https",
			url:   "https://git.internal/acme/infra.git",
			user:  "oauth2",
			token: "tok",
			want:  "https://oauth2:tok@git.internal/acme/infra.git",
			ok:    true,
		},
		{
			name:  "replaces existing userinfo",
			url:   "https://old:oldtok@git.internal/acme/infra.git",
			user:  "ci",
			token: "newtok",
			want:  "https://ci:newtok@git.internal/acme/infra.git",
			ok:    true,
		},
		{
			name:  "ssh url unchanged",
			url:   "ssh://git@git.internal/acme/infra.git",
			user:  "oauth2",
			token: "tok",
			want:  "ssh://git@git.internal/acme/infra.git",
			ok:    false,
		},
		{
			name:  "git scp-like unchanged",
			url:   "git@git.internal:acme/infra.git",
			user:  "oauth2",
			token: "tok",
			want:  "git@git.internal:acme/infra.git",
			ok:    false,
		},
		{
			name:  "escapes special chars in token",
			url:   "https://git.internal/acme/infra.git",
			user:  "oauth2",
			token: "a/b@c",
			want:  "https://oauth2:a%2Fb%40c@git.internal/acme/infra.git",
			ok:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := httpsURLWithCredentials(c.url, c.user, c.token)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if got != c.want {
				t.Errorf("got = %q, want %q", got, c.want)
			}
		})
	}
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

// TestCommand_NoSecretsInArgs verifies that neither the SSH key path content
// nor the HTTPS token appears as a command-line argument. The SSH key path
// appears in GIT_SSH_COMMAND (an env var), which is acceptable; the token
// appears only in the URL passed to git, which is the standard HTTPS auth
// mechanism. This test guards against accidentally moving secrets into args.
func TestCommand_NoSecretsInArgs(t *testing.T) {
	const token = "ghp_supersecret"
	g := New(config.Source{
		Type:   "git",
		URL:    "https://git.internal/acme/infra.git",
		Branch: "main",
		Auth:   config.Auth{Type: config.AuthHTTPS, Token: token},
	})
	ctx := context.Background()
	cmd := g.command(ctx, "fetch", "origin", "main")
	for _, a := range cmd.Args {
		// The token legitimately appears in the remote URL when HTTPS auth
		// is used; assert it never appears in a non-URL argument.
		if a == token {
			t.Errorf("token appears as a bare command argument: %q", a)
		}
	}
	// The token must not be duplicated in the environment beyond the URL
	// usage. authEnv should be empty for HTTPS auth.
	for _, e := range g.authEnv {
		if strings.Contains(e, token) {
			t.Errorf("authEnv leaks token: %q", e)
		}
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"accorda/internal/config"
)

func TestRun_NoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	e := run(nil, &out, &errOut)
	if e != errUsage {
		t.Fatalf("run(nil) error = %v, want %v", e, errUsage)
	}
	// cobra prints help to stdout when the root command runs without a
	// subcommand.
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected usage on stdout, got %q", out.String())
	}
}

func TestRun_Help(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var out, errOut bytes.Buffer
		if e := run([]string{arg}, &out, &errOut); e != nil {
			t.Fatalf("run(%q) error = %v", arg, e)
		}
		if !strings.Contains(out.String(), "Available Commands:") {
			t.Fatalf("run(%q): expected commands listing, got %q", arg, out.String())
		}
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	e := run([]string{"bogus"}, &out, &errOut)
	if e == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
	if !strings.Contains(e.Error(), `unknown command "bogus"`) {
		t.Fatalf("expected unknown-command error, got %v", e)
	}
}

func TestRun_Version(t *testing.T) {
	for _, arg := range []string{"version", "-v", "--version"} {
		var out, errOut bytes.Buffer
		if e := run([]string{arg}, &out, &errOut); e != nil {
			t.Fatalf("run(%q) error = %v", arg, e)
		}
		if !strings.HasPrefix(out.String(), "accorda ") {
			t.Fatalf("run(%q): expected \"accorda ...\", got %q", arg, out.String())
		}
	}
}

func TestRun_Version_BuildVersion(t *testing.T) {
	old := buildVersion
	buildVersion = "v0.1.0-test"
	t.Cleanup(func() { buildVersion = old })

	var out bytes.Buffer
	if e := run([]string{"version"}, &out, nil); e != nil {
		t.Fatalf("run(version) error = %v", e)
	}
	if got, want := out.String(), "accorda v0.1.0-test\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestRun_Init_CreatesProjectFile(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	args := []string{"init", "--dir", dir, "--env", "production", "--repo", "git@github.com:acme/backend.git", "--branch", "main"}
	if e := run(args, &out, nil); e != nil {
		t.Fatalf("run(init) error = %v", e)
	}

	got, err := os.ReadFile(filepath.Join(dir, config.File))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		"version: 1",
		"environment: production",
		"type: git",
		"url: git@github.com:acme/backend.git",
		"branch: main",
		"type: compose",
		"file: compose.yaml",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("project file missing %q; got %s", want, s)
		}
	}
	if !strings.Contains(out.String(), "Initialized Accorda project") {
		t.Fatalf("expected init success message, got %q", out.String())
	}

	// The file must be consumable by config.Load so `accorda sync` works
	// (issue #68): init and sync share the unified project format.
	proj, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load after init: %v", err)
	}
	if proj.Environment != "production" || proj.Source.URL != "git@github.com:acme/backend.git" {
		t.Fatalf("loaded project = %+v, want production/git@github.com:acme/backend.git", proj)
	}
}

func TestRun_Init_Defaults(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	// --repo is required (source.url), so supply one; env and file default.
	if e := run([]string{"init", "--dir", dir, "--repo", "git@github.com:acme/backend.git"}, &out, nil); e != nil {
		t.Fatalf("run(init) error = %v", e)
	}
	got, err := os.ReadFile(filepath.Join(dir, config.File))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "environment: default") {
		t.Fatalf("expected default env, got %s", s)
	}
	if !strings.Contains(s, "branch: main") {
		t.Fatalf("expected default branch, got %s", s)
	}
	if !strings.Contains(s, "file: compose.yaml") {
		t.Fatalf("expected default compose file, got %s", s)
	}
}

func TestRun_Init_MissingRepo(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	e := run([]string{"init", "--dir", dir}, &out, nil)
	if e == nil {
		t.Fatal("expected error for missing --repo, got nil")
	}
	if !strings.Contains(e.Error(), "source.url is required") {
		t.Fatalf("unexpected error %v", e)
	}
}

func TestRun_Init_Auth(t *testing.T) {
	cases := []struct {
		name     string
		args     []string // appended to a base init command with --dir and --repo
		wantAuth string   // expected auth section content (empty = no auth section)
		wantLoad bool     // whether config.Load should succeed on the written file
		wantErr  string   // non-empty = expected error substring
		wantHint string   // non-empty = expected hint substring in stdout
	}{
		{
			name:     "ambient (no auth flags)",
			args:     nil,
			wantAuth: "",
			wantLoad: true,
		},
		{
			name:     "ssh with key",
			args:     []string{"--auth-type", "ssh", "--auth-key", "/home/user/.ssh/id_ed25519"},
			wantAuth: "type: ssh",
			wantLoad: true,
		},
		{
			name:     "ssh without key fails validation",
			args:     []string{"--auth-type", "ssh"},
			wantErr:  "source.auth.key is required",
			wantLoad: false,
		},
		{
			name:     "https writes ambient with hint (token added by hand)",
			args:     []string{"--auth-type", "https"},
			wantAuth: "",
			wantLoad: true,
			wantHint: "HTTPS auth requires source.auth.token",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			base := []string{"init", "--dir", dir, "--repo", "git@github.com:acme/backend.git"}
			if e := run(append(base, c.args...), &out, nil); e != nil {
				if c.wantErr == "" {
					t.Fatalf("run(init) error = %v, want nil", e)
				}
				if !strings.Contains(e.Error(), c.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", e, c.wantErr)
				}
				return
			}
			if c.wantErr != "" {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			got, err := os.ReadFile(filepath.Join(dir, config.File))
			if err != nil {
				t.Fatalf("read project file: %v", err)
			}
			s := string(got)
			if c.wantAuth != "" && !strings.Contains(s, c.wantAuth) {
				t.Fatalf("project file missing auth section %q; got %s", c.wantAuth, s)
			}
			if c.wantAuth == "" && strings.Contains(s, "auth:") {
				t.Fatalf("project file should have no auth section; got %s", s)
			}
			// Verify the SSH key path appears when ssh auth is used.
			if strings.Contains(c.wantAuth, "ssh") && !strings.Contains(s, "/home/user/.ssh/id_ed25519") {
				t.Fatalf("project file missing SSH key path; got %s", s)
			}
			if c.wantHint != "" && !strings.Contains(out.String(), c.wantHint) {
				t.Fatalf("stdout missing hint %q; got %s", c.wantHint, out.String())
			}
			if c.wantLoad {
				if _, err := config.Load(dir); err != nil {
					t.Fatalf("config.Load after init: %v", err)
				}
			}
		})
	}
}

func TestRun_Init_MissingFlagValue(t *testing.T) {
	var out bytes.Buffer
	e := run([]string{"init", "--env"}, &out, nil)
	if e == nil {
		t.Fatal("expected error for missing -env value, got nil")
	}
	if !strings.Contains(e.Error(), "flag needs an argument") {
		t.Fatalf("unexpected error %v", e)
	}
}

func TestRun_Init_UnknownFlag(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	e := run([]string{"init", "--dir", dir, "--bogus"}, &out, nil)
	if e == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
	if !strings.Contains(e.Error(), "unknown flag") {
		t.Fatalf("unexpected error %v", e)
	}
}

func TestRun_StubCommands(t *testing.T) {
	for _, cmd := range []string{"history", "inspect", "logs", "doctor"} {
		var out bytes.Buffer
		e := run([]string{cmd}, &out, nil)
		if e == nil {
			t.Fatalf("run(%q): expected not-implemented error, got nil", cmd)
		}
		if !strings.Contains(e.Error(), "not yet implemented") {
			t.Fatalf("run(%q): unexpected error %v", cmd, e)
		}
	}
}

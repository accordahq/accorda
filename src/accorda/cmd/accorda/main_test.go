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
		"type: " + config.TargetCompose,
		"file: " + config.DefaultComposeFile,
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
	if !strings.Contains(s, "file: "+config.DefaultComposeFile) {
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
			args:     []string{"--auth-type", config.AuthSSH, "--auth-key", "/home/user/.ssh/id_ed25519"},
			wantAuth: "type: " + config.AuthSSH,
			wantLoad: true,
		},
		{
			name:     "ssh without key fails validation",
			args:     []string{"--auth-type", config.AuthSSH},
			wantErr:  "source.auth.key is required",
			wantLoad: false,
		},
		{
			name:     "https writes ambient with hint (token added by hand)",
			args:     []string{"--auth-type", config.AuthHTTPS},
			wantAuth: "",
			wantLoad: true,
			wantHint: "HTTPS auth requires source.auth.token",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertInitAuth(t, c.args, c.wantAuth, c.wantLoad, c.wantErr, c.wantHint)
		})
	}
}

// assertInitAuth runs `accorda init` with the given auth flags and checks the
// written file, stdout hint, and config.Load outcome. It is extracted from
// TestRun_Init_Auth to keep the test function below the cognitive complexity
// limit (go:S3776).
func assertInitAuth(t *testing.T, args []string, wantAuth string, wantLoad bool, wantErr, wantHint string) {
	t.Helper()
	dir := t.TempDir()
	var out bytes.Buffer
	base := []string{"init", "--dir", dir, "--repo", "git@github.com:acme/backend.git"}
	if e := run(append(base, args...), &out, nil); e != nil {
		if wantErr == "" {
			t.Fatalf("run(init) error = %v, want nil", e)
		}
		if !strings.Contains(e.Error(), wantErr) {
			t.Fatalf("error = %v, want it to contain %q", e, wantErr)
		}
		return
	}
	if wantErr != "" {
		t.Fatalf("expected error containing %q, got nil", wantErr)
	}
	s := readInitFile(t, dir)
	assertAuthContent(t, s, wantAuth)
	if wantHint != "" && !strings.Contains(out.String(), wantHint) {
		t.Fatalf("stdout missing hint %q; got %s", wantHint, out.String())
	}
	if wantLoad {
		if _, err := config.Load(dir); err != nil {
			t.Fatalf("config.Load after init: %v", err)
		}
	}
}

// assertAuthContent checks the written project file for the expected auth
// section. Extracted from assertInitAuth to reduce cognitive complexity
// (go:S3776).
func assertAuthContent(t *testing.T, s, wantAuth string) {
	t.Helper()
	if wantAuth != "" && !strings.Contains(s, wantAuth) {
		t.Fatalf("project file missing auth section %q; got %s", wantAuth, s)
	}
	if wantAuth == "" && strings.Contains(s, "auth:") {
		t.Fatalf("project file should have no auth section; got %s", s)
	}
	// Verify the SSH key path appears when ssh auth is used.
	if strings.Contains(wantAuth, config.AuthSSH) && !strings.Contains(s, "/home/user/.ssh/id_ed25519") {
		t.Fatalf("project file missing SSH key path; got %s", s)
	}
}

// readInitFile reads the accorda.yaml written by init in dir, failing the
// test on error. Extracted to reduce cognitive complexity (go:S3776).
func readInitFile(t *testing.T, dir string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, config.File))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}
	return string(got)
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

// TestRun_HistoryInspect_RequireConfig verifies the implemented history and
// inspect commands require a project file and fail cleanly when none exists,
// rather than reporting "not yet implemented".
func TestRun_HistoryInspect_RequireConfig(t *testing.T) {
	for _, cmd := range []string{"history", "inspect"} {
		var out bytes.Buffer
		e := run([]string{cmd, "--dir", t.TempDir()}, &out, nil)
		if e == nil {
			t.Fatalf("run(%q): expected config error, got nil", cmd)
		}
		if strings.Contains(e.Error(), "not yet implemented") {
			t.Fatalf("run(%q): unexpected not-implemented error %v", cmd, e)
		}
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	got, err := os.ReadFile(filepath.Join(dir, projectFile))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		"ACCORDA_ENV=production",
		"ACCORDA_REPO=git@github.com:acme/backend.git",
		"ACCORDA_BRANCH=main",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("project file missing %q; got %s", want, s)
		}
	}
	if !strings.Contains(out.String(), "Initialized Accorda project") {
		t.Fatalf("expected init success message, got %q", out.String())
	}
}

func TestRun_Init_Defaults(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if e := run([]string{"init", "--dir", dir}, &out, nil); e != nil {
		t.Fatalf("run(init) error = %v", e)
	}
	got, err := os.ReadFile(filepath.Join(dir, projectFile))
	if err != nil {
		t.Fatalf("read project file: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "ACCORDA_ENV=default") {
		t.Fatalf("expected default env, got %s", s)
	}
	if strings.Contains(s, "ACCORDA_REPO") {
		t.Fatalf("repo should be omitted by default, got %s", s)
	}
	if !strings.Contains(s, "ACCORDA_BRANCH=main") {
		t.Fatalf("expected default branch, got %s", s)
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

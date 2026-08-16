// Package main implements the accorda command-line interface.
//
// The CLI is the primary OSS user surface described in docs/ACCORDA.md §11
// (CLI) and §45 (Phase 1 — Docker Compose OSS MVP). The minimum command set
// from §79 Step 6 is wired up here:
//
//	accorda init     create an Accorda project/target
//	accorda status   show environment, repo, branch, Git HEAD, deployed SHA, ...
//	accorda diff     show deployed vs desired changes
//	accorda plan     show intended actions without deploying
//	accorda sync     run reconciliation
//	accorda history  show deployment history
//
// Additional §11 commands (inspect, logs, doctor, version) are registered;
// version is fully implemented, the rest are recognized but report that they
// are not yet implemented until the backing core packages land.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "accorda:", err)
		os.Exit(1)
	}
}

// run dispatches a subcommand. It is separated from main so tests can drive
// it with controlled arguments and capture output.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version", "-v", "--version":
		return runVersion(stdout)
	case "init":
		return runInit(rest, stdout)
	case "status", "diff", "plan", "sync", "history", "inspect", "logs", "doctor":
		return runStub(cmd, stdout)
	default:
		fmt.Fprintf(stderr, "accorda: unknown command %q\n\n", cmd)
		printUsage(stderr)
		return errUsage
	}
}

var errUsage = errors.New("usage: accorda <command> [flags]")

// commands lists the commands shown in help output, grouped to match the
// §79 Step 6 minimum set and the wider §11 surface.
var commands = []struct {
	name string
	desc string
}{
	{"init", "create an Accorda project/target"},
	{"status", "show environment, repo, branch, Git HEAD, deployed SHA, sync, runtime, services"},
	{"diff", "show deployed vs desired changes"},
	{"plan", "show intended actions without deploying"},
	{"sync", "run reconciliation"},
	{"history", "show deployment history"},
	{"inspect", "show details for a specific deployment"},
	{"logs", "show logs for a deployment or service"},
	{"doctor", "check the local Accorda installation and configuration"},
	{"version", "print the Accorda version"},
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: accorda <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.desc)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run \"accorda <command> -h\" for command-specific help.")
}

// runVersion prints the build version. It prefers the version baked in at
// build time via -ldflags, then the VCS information embedded by the Go
// toolchain in the build info, and finally a dev placeholder.
func runVersion(w io.Writer) error {
	if buildVersion != "" {
		fmt.Fprintf(w, "accorda %s\n", buildVersion)
		return nil
	}
	if v := vcsVersion(); v != "" {
		fmt.Fprintf(w, "accorda %s\n", v)
		return nil
	}
	fmt.Fprintln(w, "accorda dev (no version info)")
	return nil
}

// buildVersion is set via -ldflags "-X main.buildVersion=<version>". It stays
// empty for local builds, in which case runVersion falls back to VCS info.
var buildVersion = ""

// vcsVersion extracts a version string from the VCS data embedded in the
// Go build info (available for modules built from a VCS-tracked directory).
func vcsVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return "dev-" + rev + dirty
}

// runInit creates an Accorda project in the current directory (or the path
// given with -dir). The MVP project file records the environment name and the
// Git source to reconcile from, matching the §11 `accorda init` purpose of
// "create a Accorda project/target".
func runInit(args []string, w io.Writer) error {
	dir, env, repo, branch, err := parseInitFlags(args)
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if err := initProject(dir, env, repo, branch); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	fmt.Fprintf(w, "Initialized Accorda project in %s\n", filepath.Join(dir, projectFile))
	return nil
}

func parseInitFlags(args []string) (dir, env, repo, branch string, err error) {
	dir = "."
	env = "default"
	branch = "main"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-dir":
			if i+1 >= len(args) {
				return "", "", "", "", errors.New("-dir requires a value")
			}
			i++
			dir = args[i]
		case "-env":
			if i+1 >= len(args) {
				return "", "", "", "", errors.New("-env requires a value")
			}
			i++
			env = args[i]
		case "-repo":
			if i+1 >= len(args) {
				return "", "", "", "", errors.New("-repo requires a value")
			}
			i++
			repo = args[i]
		case "-branch":
			if i+1 >= len(args) {
				return "", "", "", "", errors.New("-branch requires a value")
			}
			i++
			branch = args[i]
		case "-h", "--help":
			err = errInitHelp
			return
		default:
			if strings.HasPrefix(a, "-") {
				err = fmt.Errorf("unknown flag %q", a)
				return
			}
			err = fmt.Errorf("unexpected argument %q", a)
			return
		}
	}
	return dir, env, repo, branch, nil
}

var errInitHelp = errors.New("usage: accorda init [-dir <path>] [-env <name>] [-repo <url>] [-branch <name>]")

const projectFile = "accorda.env"

// initProject writes a minimal Accorda project file. The format is dotenv so
// it is trivially consumable by later phases without a parser dependency.
func initProject(dir, env, repo, branch string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Accorda project configuration\n")
	fmt.Fprintf(&b, "ACCORDA_ENV=%s\n", env)
	if repo != "" {
		fmt.Fprintf(&b, "ACCORDA_REPO=%s\n", repo)
	}
	fmt.Fprintf(&b, "ACCORDA_BRANCH=%s\n", branch)
	return os.WriteFile(filepath.Join(dir, projectFile), []byte(b.String()), 0o644)
}

// runStub handles commands whose backing core packages are not yet
// implemented. They are recognized so the CLI surface matches the spec and
// so users get a clear, actionable message instead of "unknown command".
func runStub(cmd string, w io.Writer) error {
	return fmt.Errorf("%s: not yet implemented (see docs/ACCORDA.md §11, §45)", cmd)
}

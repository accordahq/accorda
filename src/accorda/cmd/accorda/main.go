// Package main implements the accorda command-line interface.
//
// The CLI is the primary OSS user surface described in docs/ACCORDA.md §11
// (CLI) and §45 (Phase 1 — Docker Compose OSS MVP). It is built on the cobra
// library with a root command and the subcommands from §11 / §79 Step 6:
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

	"github.com/spf13/cobra"
)

var errUsage = errors.New("usage: accorda <command> [flags]")

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// cobra already prints errors to stderr; keep the exit code nonzero.
		os.Exit(1)
	}
}

// newRootCmd builds the cobra root command tree. It is exposed via the
// package-level newRootCmd so tests can construct a fresh tree, inject
// arguments, and capture output.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "accorda",
		Short: "Accorda — GitOps reconciliation for Docker Compose",
		Long: "Accorda is a GitOps reconciliation tool for Docker Compose\n" +
			"deployments. See docs/ACCORDA.md for the full product specification.",
		// Setting Version enables cobra's built-in --version/-v flag. The
		// value is resolved lazily so it reflects build-time -ldflags and
		// VCS fallback at runtime, mirroring the `version` subcommand.
		Version:      versionString(),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return errUsage
		},
	}
	// Print "accorda <version>\n" to match the `version` subcommand output
	// instead of cobra's default "<name> version <version>" template.
	root.SetVersionTemplate("accorda {{.Version}}\n")

	root.AddCommand(
		newInitCmd(),
		newStatusCmd(),
		newDiffCmd(),
		newPlanCmd(),
		newSyncCmd(),
		newHistoryCmd(),
		newInspectCmd(),
		newLogsCmd(),
		newDoctorCmd(),
		newVersionCmd(),
	)
	return root
}

// run dispatches a subcommand via cobra. It is separated from main so tests
// can drive it with controlled arguments and capture output.
func run(args []string, stdout, stderr io.Writer) error {
	root := newRootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	return root.Execute()
}

// --- version --------------------------------------------------------------

// buildVersion is set via -ldflags "-X main.buildVersion=<version>". It stays
// empty for local builds, in which case runVersion falls back to VCS info.
var buildVersion = ""

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Accorda version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVersion(cmd.OutOrStdout())
		},
	}
}

// runVersion prints the build version. It prefers the version baked in at
// build time via -ldflags, then the VCS information embedded by the Go
// toolchain in the build info, and finally a dev placeholder.
func runVersion(w io.Writer) error {
	fmt.Fprintf(w, "accorda %s\n", versionString())
	return nil
}

// versionString resolves the version reported by both the `version`
// subcommand and the root command's --version/-v flag. It prefers the
// version baked in at build time via -ldflags, then the VCS information
// embedded by the Go toolchain in the build info, and finally a dev
// placeholder.
func versionString() string {
	if buildVersion != "" {
		return buildVersion
	}
	if v := vcsVersion(); v != "" {
		return v
	}
	return "dev (no version info)"
}

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

// --- init -----------------------------------------------------------------

const projectFile = "accorda.env"

func newInitCmd() *cobra.Command {
	var (
		dir    string
		env    string
		repo   string
		branch string
	)
	c := &cobra.Command{
		Use:   "init",
		Short: "Create an Accorda project/target",
		Long: "Create a minimal Accorda project file in the target directory.\n" +
			"The file records the environment name and the Git source to\n" +
			"reconcile from, matching the §11 `accorda init` purpose.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := initProject(dir, env, repo, branch); err != nil {
				return fmt.Errorf("init: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Initialized Accorda project in %s\n", filepath.Join(dir, projectFile))
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	c.Flags().StringVar(&env, "env", "default", "environment name")
	c.Flags().StringVar(&repo, "repo", "", "Git source repository URL")
	c.Flags().StringVar(&branch, "branch", "main", "Git branch to reconcile")
	return c
}

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

// --- stub commands --------------------------------------------------------

// stubCmd builds a command that reports it is not yet implemented. These
// commands are recognized so the CLI surface matches the spec and users get a
// clear, actionable message instead of "unknown command".
func stubCmd(name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s: not yet implemented (see docs/ACCORDA.md §11, §45)", name)
		},
	}
}

func newStatusCmd() *cobra.Command {
	return stubCmd("status", "show environment, repo, branch, Git HEAD, deployed SHA, sync, runtime, services")
}

func newDiffCmd() *cobra.Command {
	return stubCmd("diff", "show deployed vs desired changes")
}

func newPlanCmd() *cobra.Command {
	return stubCmd("plan", "show intended actions without deploying")
}

func newHistoryCmd() *cobra.Command {
	return stubCmd("history", "show deployment history")
}

func newInspectCmd() *cobra.Command {
	return stubCmd("inspect", "show details for a specific deployment")
}

func newLogsCmd() *cobra.Command {
	return stubCmd("logs", "show logs for a deployment or service")
}

func newDoctorCmd() *cobra.Command {
	return stubCmd("doctor", "check the local Accorda installation and configuration")
}

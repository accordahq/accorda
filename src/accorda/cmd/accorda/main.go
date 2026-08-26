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
//	accorda inspect  show per-service detail for a specific deployment
//	accorda logs     fetch or follow logs for a service
//
// version is fully implemented, and status, diff, plan, history, inspect, logs,
// and doctor are implemented in their eponymous files.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

	"accorda/internal/config"
)

var errUsage = errors.New("usage: accorda <command> [flags]")

var createProjectFile = func(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
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

func newInitCmd() *cobra.Command {
	var (
		dir      string
		env      string
		repo     string
		branch   string
		file     string
		authType string
		authKey  string
	)
	c := &cobra.Command{
		Use:   "init",
		Short: "Create an Accorda project/target",
		Long: "Create an Accorda project file (accorda.yaml) in the target\n" +
			"directory. The file records the environment name, the Git source\n" +
			"to reconcile from, and the Compose target, matching the §11\n" +
			"`accorda init` purpose and the unified project format in §25.",
		RunE: func(cmd *cobra.Command, args []string) error {
			hint, err := initProject(dir, env, repo, branch, file, authType, authKey)
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Initialized Accorda project in %s\n", filepath.Join(dir, config.File))
			if hint != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", hint)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	c.Flags().StringVar(&env, "env", "default", "environment name")
	c.Flags().StringVar(&repo, "repo", "", "Git source repository URL")
	c.Flags().StringVar(&branch, "branch", "main", "Git branch to reconcile")
	c.Flags().StringVar(&file, "file", config.DefaultComposeFile, "Compose file path in Git (absolute path selects a local override)")
	c.Flags().StringVar(&authType, "auth-type", "", "Git auth type: ssh or https (https writes ambient; add source.auth.token by hand)")
	c.Flags().StringVar(&authKey, "auth-key", "", "SSH private key path (for --auth-type=ssh)")
	return c
}

// initProject writes a minimal Accorda project file in the canonical
// accorda.yaml format (docs/ACCORDA.md §25) so that `accorda sync` can load
// it. The file is the unified project format consumed by the config loader
// (internal/config), aligning `init` with the rest of the CLI
// (docs/DECISIONS.md #10).
//
// It returns a non-empty hint string when the user requested HTTPS auth
// (which requires a token that cannot be supplied via a flag without
// recording it in shell history); the hint tells the user to edit the file
// and add source.auth.token. In that case the file is written with ambient
// auth (no auth section) so it remains valid and loadable.
func initProject(dir, env, repo, branch, file, authType, authKey string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	auth, hint := initAuth(authType, authKey)
	proj := &config.Project{
		Version:     config.SchemaVersion,
		Environment: env,
		Source: config.Source{
			Type:   "git",
			URL:    repo,
			Branch: branch,
			Auth:   auth,
		},
		Target: config.Target{
			Type: config.TargetCompose,
			File: file,
		},
	}
	if !filepath.IsAbs(file) {
		proj.Source.Path = file
	}
	config.ApplyDefaults(proj)
	if err := config.Validate(proj); err != nil {
		return "", err
	}
	data, err := config.MarshalProject(proj)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, config.File)
	fileHandle, err := createProjectFile(path)
	if err != nil {
		return "", err
	}
	if _, err := fileHandle.Write(data); err != nil {
		_ = fileHandle.Close()
		return "", err
	}
	if err := fileHandle.Close(); err != nil {
		return "", err
	}
	return hint, nil
}

// initAuth builds the source.auth configuration from the init flags. An empty
// authType means "use the ambient Git environment" and produces a zero Auth,
// which the loader and the git source both accept (docs/ACCORDA.md §15).
//
// For --auth-type=ssh, the key path from --auth-key is recorded so the file is
// ready to use. For --auth-type=https, the token cannot be supplied via a
// flag without recording it in shell history, so init writes ambient auth
// (no auth section) and returns a hint telling the user to edit the file and
// add source.auth.token by hand. This keeps the generated file valid and
// loadable rather than failing validation with no file written.
func initAuth(authType, authKey string) (config.Auth, string) {
	switch authType {
	case config.AuthSSH:
		return config.Auth{Type: config.AuthSSH, Key: authKey}, ""
	case config.AuthHTTPS:
		return config.Auth{}, "Note: HTTPS auth requires source.auth.token; edit " + config.File + " to add it."
	default:
		return config.Auth{}, ""
	}
}

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/history"
	"accorda/internal/core/locking"
	"accorda/internal/core/reconcile"
	"accorda/internal/core/state"
	"accorda/internal/sources/git"
	"accorda/internal/targets/compose"
)

// newSyncCmd builds the `accorda sync` command (docs/ACCORDA.md §11). It loads
// the project file, constructs the Git source and Compose target, and drives
// either one reconciliation pass or the continuous --watch loop.
//
// The command is the production wiring point for the core packages that were
// previously only exercised by tests: the Reconciler (internal/core/reconcile),
// the Git source (internal/sources/git), and the Compose target
// (internal/targets/compose). It threads the project's drift policy, image
// pull policy, and health timeout into the reconciler and target
// (docs/DECISIONS.md #18, #21, #22).
func newSyncCmd() *cobra.Command {
	var (
		dir   string
		watch bool
	)
	c := &cobra.Command{
		Use:   "sync",
		Short: "run reconciliation",
		Long: "Run one reconciliation pass: fetch the desired state from Git,\n" +
			"plan the changes, apply them to the target, and verify health.\n" +
			"With --watch, run immediately and then continuously at sync.interval.\n" +
			"The project file (accorda.yaml) is read from the project directory.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runSync(cmd, dir, watch); err != nil {
				return fmt.Errorf("sync: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	c.Flags().BoolVar(&watch, "watch", false, "continuously reconcile at sync.interval")
	return c
}

// runSync loads the project configuration, constructs the source and target,
// and runs one reconciliation cycle or the continuous polling loop, printing
// each terminal phase and desired/deployed/runtime comparison.
func runSync(cmd *cobra.Command, dir string, watch bool) error {
	proj, err := config.Load(dir)
	if err != nil {
		return err
	}

	src := git.New(proj.Source)
	tgt, err := buildTarget(proj, dir)
	if err != nil {
		return err
	}

	store := history.NewFileStore(receiptPath(dir))
	r := reconcile.New(src, tgt, events.NewBus()).
		WithDriftPolicy(driftPolicy(proj.Reconcile.Drift)).
		WithEnvironment(proj.Environment).
		WithReceiptStore(store).
		WithLocker(locking.NewFileLocker(deploymentLockPath(dir, proj.Target)))
	if watch {
		return r.Run(cmd.Context(), proj.Sync.Interval, func(res *reconcile.Result) {
			if resultErr := writeSyncResult(cmd, res); resultErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "sync: %v\n", resultErr)
			}
		})
	}
	return writeSyncResult(cmd, r.Reconcile(cmd.Context()))
}

// writeSyncResult prints one reconciliation cycle. A failed cycle is returned
// as an error for one-shot sync; watch mode logs it and keeps polling so a
// transient source or target failure can recover without restarting Accorda.
func writeSyncResult(cmd *cobra.Command, res *reconcile.Result) error {
	if res.Phase == reconcile.PhaseFailed {
		if res.RolledBack {
			// A failed deployment was rolled back to a known previous commit.
			// Report the rollback clearly so a user sees what was restored and
			// why the active state is healthy (docs/ACCORDA.md §20).
			fmt.Fprintf(cmd.OutOrStdout(), "sync: %s\n", res.Phase)
			fmt.Fprintf(cmd.OutOrStdout(), "rollback: restored to commit %s\n", res.RolledBackTo)
			return fmt.Errorf("reconciliation failed and was rolled back: %w", res.Err)
		}
		return fmt.Errorf("reconciliation failed: %w", res.Err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "sync: %s\n", res.Phase)
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", res.Comparison.String())
	return nil
}

// previousFromHistory returns the last known-healthy deployment state read
// from the receipt journal, used as the rollback target
// (docs/ACCORDA.md §20). It scans the receipts for the most recent
// OutcomeHealthy row and reconstructs its deployed state from the recorded
// commit and branch. When history is empty (or has no healthy deployment), it
// returns nil so the reconciler treats rollback as unsafe and lets the
// failure stand.
//
// The previous services carry only the image reference recorded in the
// receipt (the history model records image + digest per service); the
// reconciler restores the full previous service model by reading the desired
// state at the previous commit from the source, so the on-disk Compose file
// reflects the complete previous deployment rather than just the image.
//
// A store read error is reported to warn (so an operator can distinguish "no
// prior healthy deployment" from "history could not be read") and treated as
// no rollback target, honoring the last safely possible qualifier in §20.
func previousFromHistory(store history.Store, warn io.Writer) *state.DeployedState {
	rc, err := lastHealthyReceipt(store)
	if err != nil && warn != nil {
		fmt.Fprintf(warn, "sync: warning: could not read deployment history for rollback: %v\n", err)
	}
	if rc == nil {
		return nil
	}
	services := make(map[string]state.Service, len(rc.Services))
	for name, svc := range rc.Services {
		services[name] = state.Service{Image: svc.Image}
	}
	return &state.DeployedState{
		DeploymentID: rc.DeploymentID,
		Commit:       rc.Commit,
		Services:     services,
	}
}

// lastHealthyReceipt returns the most recent OutcomeHealthy receipt from the
// journal, or nil when the store has no healthy deployment. It returns a
// non-nil error only when the store cannot be read. It is shared by the sync
// command (rollback target) and the status command (last-deploy / deployed
// commit), so both surfaces agree on what the last healthy deployment was
// (docs/ACCORDA.md §7, §20).
func lastHealthyReceipt(store history.Store) (*history.Receipt, error) {
	if store == nil {
		return nil, nil
	}
	receipts, err := store.List(context.Background())
	if err != nil {
		return nil, err
	}
	for _, rc := range slices.Backward(receipts) {
		if rc.Result == history.OutcomeHealthy {
			cp := rc
			return &cp, nil
		}
	}
	return nil, nil
}

// buildTarget constructs the deployment target from the project's target
// configuration, resolving relative target paths from the directory containing
// accorda.yaml. Only the Compose target is implemented; other target types are
// recognized by the config loader but have no driver yet.
func buildTarget(p *config.Project, dir string) (*compose.Target, error) {
	if p.Target.Type != config.TargetCompose {
		return nil, fmt.Errorf("target type %q is not implemented", p.Target.Type)
	}
	target := resolveTargetPaths(dir, p.Target)
	return compose.New(target,
		compose.WithPullPolicy(p.Images.Pull),
		compose.WithHealthTimeout(p.Health.Timeout),
		compose.WithEnvironment(p.Environment),
	)
}

// resolveTargetPaths interprets relative target paths from the project root.
// It returns a copy so loading a target never mutates the parsed Project.
func resolveTargetPaths(dir string, target config.Target) config.Target {
	if target.File != "" && !filepath.IsAbs(target.File) {
		target.File = filepath.Join(dir, target.File)
	}
	if target.Path != "" && !filepath.IsAbs(target.Path) {
		target.Path = filepath.Join(dir, target.Path)
	}
	return target
}

// receiptPath returns the path of the deployment receipt journal for the
// project directory. Receipts are stored under a global state directory
// (docs/ACCORDA.md §28 "local filesystem", §42 "local history"), keyed by the
// project directory so multiple projects do not share a journal. The state
// directory honors XDG_STATE_HOME when set, falling back to ~/.local/state,
// and finally ~/.accorda for environments without XDG.
func receiptPath(dir string) string {
	return projectStatePath("receipts", dir, ".jsonl")
}

// deploymentLockPath returns the target-scoped lock file used to serialize
// reconciliation across CLI processes. Hashing the effective Compose project
// identity means different Compose files that mutate the same project share a
// lock without exposing the project name in the state directory.
func deploymentLockPath(dir string, target config.Target) string {
	resolved := resolveTargetPaths(dir, target)
	identity := resolved.Type + "\x00" + compose.ProjectName(resolved)
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(stateBase(), "accorda", "locks", fmt.Sprintf("%x.lock", digest))
}

func projectStatePath(kind, dir, extension string) string {
	base := stateBase()
	key := filepath.Clean(dir)
	if key == "." {
		key = "default"
	}
	return filepath.Join(base, "accorda", kind, key+extension)
}

func stateBase() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return base
}

// driftPolicy maps the project's reconcile.drift setting to the reconciler's
// DriftPolicy (docs/ACCORDA.md §5.3, docs/DECISIONS.md #22). The config loader
// validates the value upstream, so an unknown value degrades to report-only.
func driftPolicy(p string) reconcile.DriftPolicy {
	switch p {
	case config.DriftRepair:
		return reconcile.DriftRepair
	case config.DriftDisabled:
		return reconcile.DriftDisabled
	default:
		return reconcile.DriftReport
	}
}

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/reconcile"
	"accorda/internal/sources/git"
	"accorda/internal/targets/compose"
)

// newSyncCmd builds the `accorda sync` command (docs/ACCORDA.md §11). It
// triggers one reconciliation pass on demand: it loads the project file,
// constructs the Git source and Compose target from it, and drives the
// reconciliation lifecycle state machine (docs/ACCORDA.md §6) to completion.
//
// The command is the production wiring point for the core packages that were
// previously only exercised by tests: the Reconciler (internal/core/reconcile),
// the Git source (internal/sources/git), and the Compose target
// (internal/targets/compose). It threads the project's drift policy, image
// pull policy, and health timeout into the reconciler and target
// (docs/DECISIONS.md #18, #21, #22).
func newSyncCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "sync",
		Short: "run reconciliation",
		Long: "Run one reconciliation pass: fetch the desired state from Git,\n" +
			"plan the changes, apply them to the target, and verify health.\n" +
			"The project file (accorda.yaml) is read from the project directory.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runSync(cmd, dir); err != nil {
				return fmt.Errorf("sync: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	return c
}

// runSync loads the project configuration, constructs the source and target,
// and runs a single reconciliation cycle, printing the terminal phase and the
// desired/deployed/runtime comparison to the command's output.
func runSync(cmd *cobra.Command, dir string) error {
	proj, err := config.Load(dir)
	if err != nil {
		return err
	}

	src := git.New(proj.Source)
	tgt, err := buildTarget(proj)
	if err != nil {
		return err
	}

	r := reconcile.New(src, tgt, events.NewBus()).
		WithDriftPolicy(driftPolicy(proj.Reconcile.Drift))
	res := r.Reconcile(context.Background())

	if res.Phase == reconcile.PhaseFailed {
		return fmt.Errorf("reconciliation failed: %w", res.Err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "sync: %s\n", res.Phase)
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", res.Comparison.String())
	return nil
}

// buildTarget constructs the deployment target from the project's target
// configuration. Only the Compose target is implemented; other target types
// are recognized by the config loader but have no driver yet.
func buildTarget(p *config.Project) (*compose.Target, error) {
	if p.Target.Type != config.TargetCompose {
		return nil, fmt.Errorf("target type %q is not implemented", p.Target.Type)
	}
	return compose.New(p.Target,
		compose.WithPullPolicy(p.Images.Pull),
		compose.WithHealthTimeout(p.Health.Timeout),
	)
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

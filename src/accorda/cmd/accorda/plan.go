// Package main — `accorda plan` (docs/ACCORDA.md §11).
//
// plan shows exactly what Accorda intends to do without performing the
// deployment. It is read-only with respect to the target and source: it
// fetches the desired state from Git and computes the deployment plan, but
// never applies it. The output is the per-service action summary produced by
// plan.Plan.String, prefixed with the intended plan header from §11, so an
// operator can review the intended actions before running `accorda sync`.
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/history"
	"accorda/internal/core/plan"
)

// newPlanCmd builds the `accorda plan` command (docs/ACCORDA.md §11). It
// shows the intended actions Accorda would take to reconcile the desired
// state from Git with the target's current state, without applying them. The
// command is read-only: it never mutates the target or source.
func newPlanCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "plan",
		Short: "show intended actions without deploying",
		Long: "Show the intended deployment plan without performing it\n" +
			"(docs/ACCORDA.md §11): fetch the desired state from Git, compare it\n" +
			"with the target's current state, and print the per-service actions\n" +
			"Accorda would take to reconcile the desired state. plan is read-only\n" +
			"and does not change the target or source.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runPlan(cmd, dir); err != nil {
				return fmt.Errorf("plan: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	return c
}

// runPlan loads the project, constructs the source and target, and computes
// the deployment plan from the current desired and runtime states, printing
// the intended actions to the command's output. The plan is never applied.
func runPlan(cmd *cobra.Command, dir string) error {
	proj, err := config.Load(dir)
	if err != nil {
		return err
	}
	src, err := buildSource(proj, dir)
	if err != nil {
		return err
	}
	tgt, err := buildTarget(proj, dir, src)
	if err != nil {
		return err
	}
	ctx := context.Background()

	commit, err := src.Fetch(ctx)
	if err != nil {
		return fmt.Errorf("fetch desired state: %w", err)
	}
	desired, err := src.Desired(ctx, &commit)
	if err != nil {
		return fmt.Errorf("read desired state: %w", err)
	}

	// The plan is computed against the last known-healthy deployment as the
	// deployed baseline (docs/ACCORDA.md §20). The baseline is the full
	// service model re-read from the source at the deployed commit (the
	// receipt journal stores only image/digest), so `accorda plan` and
	// `accorda diff` agree on the deployed side and a converged service is
	// not over-reported as CHANGED. The reconcile loop independently hydrates
	// its receipt baseline from the same deployed Git commit before planning,
	// so plan and sync compare equivalent full models. When history has no
	// healthy deployment, the baseline is nil and the plan treats every
	// desired service as new.
	store := history.NewFileStore(receiptPath(dir))
	deployed := deployedStateFromDesired(deployedAtCommit(ctx, src, store, cmd.ErrOrStderr()))
	p, err := tgt.Plan(ctx, desired, deployed)
	if err != nil {
		return err
	}

	writePlan(cmd.OutOrStdout(), p)
	return nil
}

// writePlan prints the plan in the format shown in docs/ACCORDA.md §11. It
// delegates to plan.Plan.String, which renders the identifying fields
// (deployment, environment, commit) followed by the per-service
// CHANGED/UNCHANGED summary, so `accorda plan` shows the intended actions
// without performing them.
func writePlan(w io.Writer, p *plan.Plan) {
	fmt.Fprintf(w, "Deployment plan\n")
	fmt.Fprintf(w, "%s", p.String())
}

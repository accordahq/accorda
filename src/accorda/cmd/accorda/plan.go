// Package main — `accorda plan` (docs/ACCORDA.md §11).
//
// plan shows exactly what Accorda intends to do without performing the
// deployment. It is read-only with respect to the target and source: it
// fetches a Git revision, asks the target to load desired state, and computes the deployment plan, but
// never applies it. The output is the per-service action summary produced by
// plan.Plan.String, prefixed with the intended plan header from §11, so an
// operator can review the intended actions before running `accorda sync`.
package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/history"
	"accorda/internal/core/plan"
)

// newPlanCmd builds the `accorda plan` command (docs/ACCORDA.md §11). It
// shows the intended actions Accorda would take to reconcile the desired
// state from the target revision with the target's current state, without applying them. The
// command is read-only: it never mutates the target or source.
func newPlanCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "plan",
		Short: "show intended actions without deploying",
		Long: "Show the intended deployment plan without performing it\n" +
			"(docs/ACCORDA.md §11): fetch the Git revision, load target state, and compare it\n" +
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
	projects, err := loadProjects(dir)
	if err != nil {
		return err
	}
	for i := range projects {
		p := &projects[i]
		if err := runPlanOne(cmd, dir, p); err != nil {
			return fmt.Errorf("plan %s: %w", p.Name, err)
		}
	}
	return nil
}

// runPlanOne computes and prints the plan for a single project's targets. name
// is the project's operator-chosen name (empty for a single-project document),
// used to scope the source, target, and receipt journal.
func runPlanOne(cmd *cobra.Command, dir string, p *config.Project) error {
	src, err := buildSource(p, dir)
	if err != nil {
		return err
	}
	ctx := context.Background()

	targets := p.NormalizedTargets()
	multiTarget := len(targets) > 1
	for i := range targets {
		tgtCfg := targets[i]
		tgt, err := buildTargetConfig(p, tgtCfg, dir, src, p.Name)
		if err != nil {
			return err
		}

		// Re-reading the deployed commit from the managed worktree temporarily
		// checks out a historical revision, so plan takes the same deployment
		// lock as sync to avoid racing a concurrent deployment
		// (docs/DECISIONS.md #40).
		if err := withDeploymentLock(ctx, dir, tgtCfg, func() error {
			commit, err := src.Fetch(ctx)
			if err != nil {
				return fmt.Errorf("fetch desired state: %w", err)
			}
			desired, derr := desiredAt(ctx, src, tgt, &commit)
			if derr != nil && desired == nil {
				return fmt.Errorf("read desired state: %w", derr)
			}
			if derr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: revision cleanup: %v\n", derr)
			}

			// The plan is computed against the last known-healthy deployment as the
			// deployed baseline (docs/ACCORDA.md §20). The baseline is the full
			// service model reloaded by the target at the deployed commit (the
			// receipt journal stores only image/digest), so `accorda plan` and
			// `accorda diff` agree on the deployed side and a converged service is
			// not over-reported as CHANGED. The reconcile loop independently hydrates
			// its receipt baseline from the same deployed Git commit before planning,
			// so plan and sync compare equivalent full models. When history has no
			// healthy deployment, the baseline is nil and the plan treats every
			// desired service as new.
			store := history.NewFileStore(targetReceiptPath(dir, p.Name, tgtCfg, multiTarget))
			deployed := deployedStateFromDesired(deployedAtCommit(ctx, src, tgt, store, cmd.ErrOrStderr()))
			pn, err := tgt.Plan(ctx, desired, deployed)
			if err != nil {
				return err
			}

			writeTargetHeader(cmd.OutOrStdout(), p.Name, tgtCfg, multiTarget)
			writePlan(cmd.OutOrStdout(), pn)
			writeEnvOverrides(cmd.OutOrStdout(), tgtCfg.Services)
			return nil
		}); err != nil {
			return err
		}
	}
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

// writeEnvOverrides prints a summary of per-service env overrides configured
// in accorda.yaml (docs/DECISIONS.md #23), so the operator knows which
// services have deploy-time env inputs and from where (inline values, local
// files, or both). Nothing is printed when no overrides are configured.
func writeEnvOverrides(w io.Writer, services map[string]config.ServiceOverride) {
	if len(services) == 0 {
		return
	}
	fmt.Fprintln(w, "Env overrides")
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := services[name]
		parts := make([]string, 0, 2)
		if len(svc.EnvFiles) > 0 {
			paths := make([]string, 0, len(svc.EnvFiles))
			for _, f := range svc.EnvFiles {
				paths = append(paths, f.Path)
			}
			parts = append(parts, fmt.Sprintf("%d file(s): %s", len(svc.EnvFiles), strings.Join(paths, ", ")))
		}
		if len(svc.Env) > 0 {
			parts = append(parts, fmt.Sprintf("%d inline value(s)", len(svc.Env)))
		}
		fmt.Fprintf(w, "  %-12s %s\n", name, strings.Join(parts, "; "))
	}
}

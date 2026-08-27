package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/history"
	"accorda/internal/core/locking"
	"accorda/internal/core/reconcile"
)

// newSyncCmd builds the `accorda sync` command (docs/ACCORDA.md §11). It loads
// the project file, constructs the Git source and deployment target, and
// drives either one reconciliation pass or the continuous --watch loop.
//
// The command is the production wiring point for the core packages that were
// previously only exercised by tests: the Reconciler (internal/core/reconcile),
// the Git source (internal/sources/git), and the target driver selected by
// the project's target.type (internal/targets). It threads the project's
// drift policy, image pull policy, and health timeout into the reconciler
// and target (docs/DECISIONS.md #17, #19, #26).
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
	projects, err := loadProjects(dir)
	if err != nil {
		return err
	}
	return runProjectsSync(cmd, dir, watch, projects)
}

// loadProjects normalizes an accorda.yaml document into a uniform project
// list (docs/ACCORDA.md §25, §49). A single-project document becomes a
// one-element list whose member name is empty; a multi-project document
// yields its named projects. The CLI drives every shape through the same
// reconciliation path, so single- and multi-project sync differ only in the
// number and naming of the members, not in how they are reconciled.
func loadProjects(dir string) ([]config.Project, error) {
	doc, err := config.LoadDocument(dir)
	if err != nil {
		return nil, err
	}
	if doc.Ensemble != nil {
		return doc.Ensemble.Projects, nil
	}
	return []config.Project{*doc.Project}, nil
}

// runProjectsSync reconciles every project in an accorda.yaml document
// concurrently (docs/ACCORDA.md §49). Each project builds its own source,
// target, receipt store, lock, and event bus, so workloads reconcile
// independently; results are aggregated and printed per project so an
// operator can tell which workload a cycle outcome belongs to. A single
// unnamed project behaves exactly like the legacy one-project sync: no name
// prefix in output and no name subdirectory in state paths.
func runProjectsSync(cmd *cobra.Command, dir string, watch bool, projects []config.Project) error {
	if len(projects) == 0 {
		return errors.New("sync: no projects configured")
	}
	members, cleanup, err := buildEnsembleMembers(cmd, dir, projects)
	if err != nil {
		cleanup() // unwind any members already built before the failure
		return err
	}
	defer cleanup()
	if len(members) == 1 {
		// A single-project document runs through the same single-reconciler
		// path as before, preserving its output and state layout.
		m := members[0]
		return runReconciler(cmd, watch, projects[0].Sync.Interval, m.Reconciler)
	}
	ensemble, err := reconcile.NewEnsemble(members)
	if err != nil {
		return err
	}
	if watch {
		return ensemble.Run(cmd.Context(), projects[0].Sync.Interval, func(results []reconcile.MemberResult) {
			for _, mr := range results {
				if resultErr := writeMemberResult(cmd, mr); resultErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "sync %s: %v\n", mr.Name, resultErr)
				}
			}
		})
	}
	// One-shot: a failed member cycle must propagate as a non-zero exit so
	// automation keyed on the exit code treats a failed reconcile as failure,
	// matching the single-project path (docs/ACCORDA.md §11). All members run
	// (a failure in one does not block the others); the first failed result is
	// returned after every member's output is printed.
	results := ensemble.Reconcile(cmd.Context())
	return writeEnsembleResults(cmd, results)
}

// writeEnsembleResults prints every member's cycle outcome and returns the
// first failed member's error, so a one-shot ensemble sync propagates a
// non-zero exit when any member fails (docs/ACCORDA.md §11). It is extracted
// from runProjectsSync so the aggregation is testable without a live source
// or target.
func writeEnsembleResults(cmd *cobra.Command, results []reconcile.MemberResult) error {
	var firstErr error
	for _, mr := range results {
		if err := writeMemberResult(cmd, mr); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("sync %s: %w", mr.Name, err)
		}
	}
	return firstErr
}

// buildEnsembleMembers constructs the reconciler members for each project,
// wiring per-member source, target, receipt store, lock, and event bus. It
// returns the members and a cleanup function that unsubscribes every member's
// bus progress writer and webhook consumer; the caller must defer cleanup() so
// the subscriptions do not outlive the reconciliation. On error, cleanup
// unwinds any members already built so a partial build leaks nothing.
func buildEnsembleMembers(cmd *cobra.Command, dir string, projects []config.Project) ([]reconcile.EnsembleMember, func(), error) {
	members := make([]reconcile.EnsembleMember, 0, len(projects))
	var unsubscribers []func()
	cleanup := func() {
		for _, unsub := range unsubscribers {
			if unsub != nil {
				unsub()
			}
		}
	}
	for i := range projects {
		p := &projects[i]
		src, err := buildSource(p, dir, p.Name)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("sync %s: %w", p.Name, err)
		}
		tgt, err := buildTarget(p, dir, src, p.Name)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("sync %s: %w", p.Name, err)
		}
		store := history.NewFileStore(receiptPath(dir, p.Name))
		bus := events.NewBus()
		unsubscribers = append(unsubscribers, bus.Subscribe(projectSyncProgressWriter(cmd.OutOrStdout(), p.Name)))
		if wh, err := buildWebhook(p.Notifications, bus); err != nil {
			cleanup()
			return nil, nil, err
		} else if wh != nil {
			unsubscribers = append(unsubscribers, wh)
		}
		r := reconcile.New(src, tgt, bus).
			WithDriftPolicy(driftPolicy(p.Reconcile.Drift)).
			WithEnvironment(p.Environment).
			WithReceiptStore(store).
			WithLocker(locking.NewFileLocker(deploymentLockPath(dir, p.Target)))
		members = append(members, reconcile.EnsembleMember{Name: p.Name, Reconciler: r})
	}
	return members, cleanup, nil
}

// writeMemberResult prints one ensemble member's cycle outcome, prefixed with
// the project name so an operator can attribute the result to a workload.
func writeMemberResult(cmd *cobra.Command, mr reconcile.MemberResult) error {
	return writeSyncResultWithPrefix(cmd.OutOrStdout(), syncPrefix(mr.Name), mr.Result)
}

// writeSyncResult prints one reconciliation cycle. A failed cycle is returned
// as an error for one-shot sync; watch mode logs it and keeps polling so a
// transient source or target failure can recover without restarting Accorda.
func writeSyncResult(cmd *cobra.Command, res *reconcile.Result) error {
	return writeSyncResultWithPrefix(cmd.OutOrStdout(), "", res)
}

// writeSyncResultWithPrefix prints one reconciliation cycle with the given
// prefix (empty for a single-project document, "name: " for an ensemble
// member). It is the single renderer for a cycle outcome so the failure,
// rollback, and healthy rendering is shared and cannot drift between the
// single-project and ensemble paths.
func writeSyncResultWithPrefix(w io.Writer, prefix string, res *reconcile.Result) error {
	if res.Phase == reconcile.PhaseFailed {
		fmt.Fprintf(w, "%ssync: %s\n", prefix, res.Phase)
		if res.RolledBack {
			// A failed deployment was rolled back to a known previous commit.
			// Report the rollback clearly so a user sees what was restored and
			// why the active state is healthy (docs/ACCORDA.md §20).
			fmt.Fprintf(w, "%srollback: restored to commit %s\n", prefix, res.RolledBackTo)
			return fmt.Errorf("reconciliation failed and was rolled back: %w", res.Err)
		}
		return fmt.Errorf("reconciliation failed: %w", res.Err)
	}
	fmt.Fprintf(w, "%ssync: %s\n", prefix, res.Phase)
	fmt.Fprintf(w, "%s%s\n", prefix, res.Comparison.String())
	return nil
}

// projectSyncProgressWriter returns an event handler that prints lifecycle
// progress lines for one project. Lines are prefixed with the project name so
// concurrent ensemble output stays attributable to its workload; an empty name
// (single-project document) prints with no prefix.
func projectSyncProgressWriter(w io.Writer, name string) events.Handler {
	prefix := syncPrefix(name)
	return func(_ context.Context, event events.Event) {
		switch event.Type {
		case events.EventDriftDetected:
			fmt.Fprintf(w, "%ssync: drift detected\n", prefix)
		case events.EventDriftReconciled:
			fmt.Fprintf(w, "%ssync: drift repaired\n", prefix)
		case events.EventStateTransition:
			writeProjectTransition(w, prefix, event.Payload)
		}
	}
}

// writeProjectTransition prints a non-terminal state transition line for one
// project. prefix is the project-name prefix (including any trailing ": ") or
// empty for a single-project document.
func writeProjectTransition(w io.Writer, prefix string, payload any) {
	transition, ok := payload.(reconcile.StateTransition)
	if !ok || transition.To == reconcile.PhaseSynced || transition.To == reconcile.PhaseFailed {
		return
	}
	fmt.Fprintf(w, "%ssync: %s", prefix, transition.To)
	if transition.Commit != "" {
		fmt.Fprintf(w, " commit=%s", shortSHA(transition.Commit))
	}
	if transition.DeploymentID != "" {
		fmt.Fprintf(w, " deployment=%s", transition.DeploymentID)
	}
	fmt.Fprintln(w)
}

// syncPrefix returns the "name: " prefix for a project name, or "" when the
// name is empty so single-project output has no prefix.
func syncPrefix(name string) string {
	if name == "" {
		return ""
	}
	return name + ": "
}

type syncReconciler interface {
	Reconcile(context.Context) *reconcile.Result
	Run(context.Context, time.Duration, reconcile.ResultHandler) error
}

func runReconciler(cmd *cobra.Command, watch bool, interval time.Duration, r syncReconciler) error {
	ctx := cmd.Context()
	if watch {
		return r.Run(ctx, interval, func(res *reconcile.Result) {
			if resultErr := writeSyncResult(cmd, res); resultErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "sync: %v\n", resultErr)
			}
		})
	}
	return writeSyncResult(cmd, r.Reconcile(ctx))
}

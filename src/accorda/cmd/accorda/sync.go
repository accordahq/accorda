package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/history"
	"accorda/internal/core/locking"
	"accorda/internal/core/reconcile"
	"accorda/internal/core/state"
	"accorda/internal/notifications/webhook"
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

// buildWebhook constructs and subscribes the generic outbound webhook
// notification target when the project enables it (docs/ACCORDA.md §21). It
// returns the unsubscribe function and a nil error when the channel is
// disabled. Delivery errors are reported to the command's stderr so a
// misconfigured webhook is observable without blocking reconciliation.
func buildWebhook(n config.Notifications, bus events.Bus) (func(), error) {
	if !n.Webhook {
		return nil, nil
	}
	if n.WebhookConfig == nil {
		return nil, errors.New("sync: notifications.webhook is enabled but webhooks is not configured")
	}
	con, err := webhook.New(*n.WebhookConfig, webhook.WithErrorSink(func(err error) {
		fmt.Fprintf(os.Stderr, "sync: webhook: %v\n", err)
	}))
	if err != nil {
		return nil, fmt.Errorf("sync: %w", err)
	}
	return con.Subscribe(bus), nil
}

// buildSource resolves the repository-relative Compose artifact shared by the
// Git source and Compose target. A source path naming a directory is combined
// with the target filename; an explicit source YAML path wins.
//
// name is the operator project name in a multi-project document, or empty for
// a single-project document. It is appended to the git cache namespace so two
// ensemble members that share a repository URL (e.g. two branches of one
// repo) get isolated checkouts instead of racing on the same worktree
// (docs/ACCORDA.md §49; docs/DECISIONS.md #43).
func buildSource(p *config.Project, dir, name string) (*git.Git, error) {
	source := p.Source
	targetPath := configuredTargetPath(p.Target)
	if filepath.IsAbs(targetPath) {
		targetPath = ""
	}
	composePath, err := git.ComposePath(source.Path, targetPath)
	if err != nil {
		return nil, err
	}
	source.Path = composePath
	projectDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve project directory: %w", err)
	}
	namespace := filepath.Clean(projectDir)
	if name != "" {
		namespace = filepath.Join(namespace, name)
	}
	return git.New(source, git.WithCacheNamespace(namespace)), nil
}

// buildTarget constructs the deployment target against the Git source's
// managed checkout. Only the Compose target is implemented; other target
// types are recognized by the config loader but have no driver yet.
//
// name is the operator project name in an ensemble document, or empty for a
// single project. When non-empty, it overrides the Compose project name so two
// ensemble members whose Compose files live in same-named directories do not
// derive the same project name — which would make `--remove-orphans`
// destructive across projects (docs/ACCORDA.md §49).
func buildTarget(p *config.Project, dir string, src *git.Git, name string) (*compose.Target, error) {
	if p.Target.Type != config.TargetCompose {
		return nil, fmt.Errorf("target type %q is not implemented", p.Target.Type)
	}
	target, managed, err := resolveTargetPaths(p.Target, src)
	if err != nil {
		return nil, err
	}
	options := []compose.Option{
		compose.WithPullPolicy(p.Images.Pull),
		compose.WithHealthTimeout(p.Health.Timeout),
		compose.WithEnvironment(p.Environment),
		compose.WithServiceOverrides(p.Target.Services),
	}
	if name != "" {
		options = append(options, compose.WithProjectName(name))
	} else if managed {
		projectDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve project directory: %w", err)
		}
		options = append(options, compose.WithProjectName(filepath.Base(filepath.Clean(projectDir))))
	}
	return compose.New(target, options...)
}

// resolveTargetPaths points repository-relative Compose targets at the Git
// source's managed checkout. Absolute target paths remain explicit local
// overrides for backwards compatibility.
func resolveTargetPaths(target config.Target, src *git.Git) (config.Target, bool, error) {
	configured := configuredTargetPath(target)
	if filepath.IsAbs(configured) {
		return target, false, nil
	}
	if src == nil {
		return config.Target{}, false, errors.New("build target: Git source is nil")
	}
	file, err := src.CheckoutPath(src.Source.Path)
	if err != nil {
		return config.Target{}, false, err
	}
	target.File = file
	target.Path = ""
	return target, true, nil
}

func configuredTargetPath(target config.Target) string {
	if target.File != "" {
		return target.File
	}
	return target.Path
}

// receiptPath returns the path of the deployment receipt journal for the
// project directory. Receipts are stored under a global state directory
// (docs/ACCORDA.md §28 "local filesystem", §42 "local history"), keyed by the
// project directory so multiple projects do not share a journal. In a
// multi-project document, name disambiguates each member's journal so one
// agent keeps per-workload history (docs/ACCORDA.md §49). The state directory
// honors XDG_STATE_HOME when set, falling back to ~/.local/state, and finally
// ~/.accorda for environments without XDG.
func receiptPath(dir, name string) string {
	return projectStatePath("receipts", dir, name, ".jsonl")
}

// withDeploymentLock acquires the target-scoped deployment lock for the
// duration of fn and releases it on return. Read-only commands that re-read
// historical desired state from the managed Git worktree (plan, diff) take
// the same lock as sync so their temporary worktree checkout cannot race a
// concurrent deployment that reads the on-disk Compose file
// (docs/DECISIONS.md #43).
func withDeploymentLock(ctx context.Context, dir string, target config.Target, fn func() error) error {
	unlock, err := locking.NewFileLocker(deploymentLockPath(dir, target)).Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return fn()
}

// deploymentLockPath returns the target-scoped lock file used to serialize
// reconciliation across CLI processes. Hashing the effective Compose project
// identity means different Compose files that mutate the same project share a
// lock without exposing the project name in the state directory.
func deploymentLockPath(dir string, target config.Target) string {
	resolved := target
	if configured := configuredTargetPath(target); !filepath.IsAbs(configured) {
		if target.File != "" {
			resolved.File = filepath.Join(dir, target.File)
		} else {
			resolved.Path = filepath.Join(dir, target.Path)
		}
	}
	identity := resolved.Type + "\x00" + compose.ProjectName(resolved)
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(stateBase(), "accorda", "locks", fmt.Sprintf("%x.lock", digest))
}

func projectStatePath(kind, dir, name, extension string) string {
	base := stateBase()
	key := filepath.Clean(dir)
	if key == "." {
		key = "default"
	}
	if name != "" {
		key = filepath.Join(key, name)
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

// Package main — shared project wiring (docs/ACCORDA.md §6, §12, §13).
//
// This file holds the business logic shared across CLI commands for
// constructing sources, targets, locks, receipt stores, and deployment
// state from a parsed accorda.yaml project. It deliberately contains no
// cobra command definitions and no output formatting so the orchestrator
// (sync.go) and the read-only commands (diff, plan, status, doctor, logs,
// inspect, history) can call it without pulling in CLI concerns.

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
	"strings"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/history"
	"accorda/internal/core/locking"
	"accorda/internal/core/reconcile"
	"accorda/internal/core/state"
	"accorda/internal/notifications/webhook"
	"accorda/internal/sources"
	"accorda/internal/sources/git"
	"accorda/internal/targets"
)

// buildSource constructs the source for one project without interpreting any
// target artifact path (docs/ACCORDA.md §13).
//
// The source has two modes (issue #95, docs/DECISIONS.md #51):
//
//   - url reconciles from a remote repository. The adapter clones it into a
//     private cache keyed by project and member name, so two ensemble members
//     that share a repository URL get isolated checkouts (docs/ACCORDA.md §49;
//     docs/DECISIONS.md #22).
//   - path reconciles in place from a user-owned local worktree without
//     cloning. The worktree root is the configured path (or its parent when
//     the path names a Compose file); the adapter binds to it and never
//     mutates it.
func buildSource(p *config.Project, dir string) (*git.Git, error) {
	source := p.Source

	if source.URL == "" {
		localPath, err := expandHomePath(source.Path)
		if err != nil {
			return nil, err
		}
		absolutePath, err := filepath.Abs(localPath)
		if err != nil {
			return nil, fmt.Errorf("resolve local source path: %w", err)
		}
		info, err := os.Stat(absolutePath)
		if err != nil {
			return nil, fmt.Errorf("inspect local source path: %w", err)
		}
		root := absolutePath
		if !info.IsDir() {
			root, err = git.FindWorktreeRoot(filepath.Dir(absolutePath))
			if err != nil {
				return nil, err
			}
		}
		source.Path = absolutePath
		return git.New(source, git.WithCacheDir(root)), nil
	}
	projectDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve project directory: %w", err)
	}
	namespace := filepath.Clean(projectDir)
	if p.Name != "" {
		namespace = filepath.Join(namespace, p.Name)
	}
	return git.New(source, git.WithCacheNamespace(namespace)), nil
}

// expandHomePath resolves the shell-style home shorthand accepted by the
// documented in-place source configuration. Environment variables and
// ~other-user forms are intentionally left untouched.
func expandHomePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// buildTarget constructs one deployment target by dispatching to the
// registered target builder (docs/ACCORDA.md §12). The command layer does not
// switch on target type or import concrete drivers: each driver package
// registers a TargetBuilder via init, and BuildTarget selects it from the
// registry. An unimplemented target type (kubernetes, helm) surfaces as a
// clear "not implemented" error.
//
// name is the operator project name in an ensemble document, or empty for a
// single project. It doubles as the Compose project name override and the
// image target's service name.
func buildTarget(p *config.Project, dir string, worktree sources.Worktree, name string) (targets.Target, error) {
	return buildTargetConfig(p, p.Target, dir, worktree, name)
}

// buildTargetConfig constructs the target for a specific config.Target within
// a project (issue #103, docs/DECISIONS.md #53). It is the per-target builder
// used by multi-target projects; buildTarget wraps it for the common
// single-target case.
func buildTargetConfig(p *config.Project, tgt config.Target, dir string, worktree sources.Worktree, name string) (targets.Target, error) {
	ctx := targets.TargetContext{Project: *p, Target: tgt, Dir: dir, Name: name, Worktree: worktree}
	return targets.BuildTarget(ctx)
}

// writeTargetHeader prints the attribution header before a per-target view
// (diff, plan, status, history, inspect) when the output needs to distinguish
// which workload and/or target it belongs to: the project name and/or the
// target identity. For a single-target project with no name it prints
// nothing, so single-project output stays unchanged. projectName is the
// operator-chosen project name (empty for a standalone project); tgt is the
// target being reported; multiTarget reports whether the project declares
// more than one target.
func writeTargetHeader(w io.Writer, projectName string, tgt config.Target, multiTarget bool) {
	parts := []string{}
	if projectName != "" {
		parts = append(parts, projectName)
	}
	if multiTarget {
		parts = append(parts, tgt.Identity())
	}
	if len(parts) > 0 {
		fmt.Fprintf(w, "%s\n", strings.Join(parts, ": "))
	}
}

// desiredAt opens a source revision, delegates target-specific artifact
// loading to the target, and releases any private historical materialization.
// A cleanup failure (e.g. removing a private historical tree) is returned as
// a non-fatal warning joined to the error only when the read itself failed;
// when the read succeeds, a cleanup error is logged but does not discard the
// successfully loaded desired state.
func desiredAt(ctx context.Context, src sources.Source, target targets.Target, ref *sources.Commit) (_ *state.DesiredState, err error) {
	revision, err := src.Revision(ctx, ref)
	if err != nil {
		return nil, err
	}
	desired, derr := target.Desired(ctx, revision)
	cerr := revision.Close()
	if derr != nil {
		return nil, errors.Join(derr, cerr)
	}
	return desired, cerr
}

// buildEnsembleMembers constructs the per-project reconciliation wiring. For
// each project it builds one source and either a single target (legacy) or
// one target per entry in the project's targets: list (issue #103,
// docs/DECISIONS.md #53). Each target gets its own reconciler, receipt store,
// and target-scoped lock, all sharing the project's single source.
//
// CLI orchestration supplies only the progress renderer factory.
func buildEnsembleMembers(dir string, projects []config.Project, progress func(string) events.Handler) ([]reconcile.EnsembleMember, func(), error) {
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
		src, err := buildSource(p, dir)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("sync %s: %w", p.Name, err)
		}
		bus := events.NewBus()
		if progress != nil {
			unsubscribers = append(unsubscribers, bus.Subscribe(progress(p.Name)))
		}
		if wh, err := buildWebhook(p.Notifications, bus); err != nil {
			cleanup()
			return nil, nil, err
		} else if wh != nil {
			unsubscribers = append(unsubscribers, wh)
		}

		member, err := buildProjectMember(p, dir, src, bus)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("sync %s: %w", p.Name, err)
		}
		members = append(members, member)
	}
	return members, cleanup, nil
}

// sourceAndWorktree is the combined capability the wire layer requires of the
// project's source: it must both reconcile (sources.Source) and expose
// worktree paths to file-backed targets (sources.Worktree). The git adapter
// implements both; the narrowed interface keeps the wiring target-agnostic.
type sourceAndWorktree interface {
	sources.Source
	sources.Worktree
}

// buildProjectMember constructs the reconciliation unit for one project: a
// single Reconciler for a single-target project, or a Project runner that fans
// one source out to several targets (issue #103, docs/DECISIONS.md #53). Each
// target gets its own receipt store and target-scoped lock, all sharing the
// project's single source.
func buildProjectMember(p *config.Project, dir string, src sourceAndWorktree, bus events.Bus) (reconcile.EnsembleMember, error) {
	targets := p.NormalizedTargets()
	if len(targets) == 1 {
		r, err := buildTargetReconciler(p, dir, src, bus, targets[0], receiptPath(dir, p.Name))
		if err != nil {
			return reconcile.EnsembleMember{}, err
		}
		return reconcile.EnsembleMember{Name: p.Name, Reconciler: r}, nil
	}

	targetMembers := make([]reconcile.TargetMember, 0, len(targets))
	for j := range targets {
		tgtCfg := targets[j]
		// This is the multi-target branch (the single-target case returned
		// above), so per-target journal scoping is always needed.
		r, err := buildTargetReconciler(p, dir, src, bus, tgtCfg, targetReceiptPath(dir, p.Name, tgtCfg, true))
		if err != nil {
			return reconcile.EnsembleMember{}, err
		}
		// Use the human-readable target identity (name when set, else the
		// type + configured path/image) for event and result attribution, not
		// the internal lock key (which contains an embedded NUL and is meant
		// only as a state-dir key). The lock itself still uses targetIdentity.
		label := tgtCfg.Identity()
		r.WithTarget(label)
		targetMembers = append(targetMembers, reconcile.TargetMember{
			Target:     label,
			Reconciler: r,
		})
	}
	project, err := reconcile.NewProject(p.Name, targetMembers)
	if err != nil {
		return reconcile.EnsembleMember{}, err
	}
	return reconcile.EnsembleMember{Name: p.Name, Runner: project}, nil
}

// buildTargetReconciler builds one target's Reconciler with its own receipt
// store and target-scoped lock, sharing the project's single source and event
// bus.
func buildTargetReconciler(p *config.Project, dir string, src sourceAndWorktree, bus events.Bus, tgtCfg config.Target, receipt string) (*reconcile.Reconciler, error) {
	tgt, err := buildTargetConfig(p, tgtCfg, dir, src, p.Name)
	if err != nil {
		return nil, err
	}
	return reconcile.New(src, tgt, bus).
		WithDriftPolicy(driftPolicy(p.Reconcile.Drift)).
		WithEnvironment(p.Environment).
		WithReceiptStore(history.NewFileStore(receipt)).
		WithLocker(locking.NewFileLocker(deploymentLockPath(dir, tgtCfg))), nil
}

// withDeploymentLock acquires the target-scoped deployment lock for the
// duration of fn and releases it on return. Read-only commands that re-read
// historical desired state from the managed Git worktree (plan, diff) take
// the same lock as sync so their temporary worktree checkout cannot race a
// concurrent deployment that reads the on-disk Compose file
// (docs/DECISIONS.md #40).
func withDeploymentLock(ctx context.Context, dir string, target config.Target, fn func() error) error {
	unlock, err := locking.NewFileLocker(deploymentLockPath(dir, target)).Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return fn()
}

// deploymentLockPath returns the target-scoped lock file used to serialize
// reconciliation across CLI processes. Hashing the effective target identity
// means different targets that mutate the same workload share a lock without
// exposing the project/service name in the state directory. The identity is
// target-type-specific: the Compose target uses the Compose project name,
// and the image target uses the service name.
func deploymentLockPath(dir string, target config.Target) string {
	identity := targetIdentity(dir, target)
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(stateBase(), "accorda", "locks", fmt.Sprintf("%x.lock", digest))
}

// targetIdentity returns the stable, target-scoped identity used to key the
// deployment lock, derived from the raw config.Target via the registered
// builder so the command layer does not switch on target type
// (docs/ACCORDA.md §47, docs/DECISIONS.md #40).
func targetIdentity(dir string, target config.Target) string {
	return targets.LockIdentityFromConfig(dir, target)
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

// targetReceiptPath returns the deployment receipt journal for one target of
// a project. tgt is the specific target whose journal is being resolved and
// multiTarget reports whether the project declares more than one target
// (issue #103, docs/DECISIONS.md #53). In a multi-target project each target
// keys its journal by its identity so two targets in the same project do not
// collide; with a single target the path is byte-identical to receiptPath so
// existing journals and state layout are preserved.
func targetReceiptPath(dir, name string, tgt config.Target, multiTarget bool) string {
	if !multiTarget {
		return receiptPath(dir, name)
	}
	key := filepath.Clean(dir)
	if key == "." {
		key = "default"
	}
	if name != "" {
		key = filepath.Join(key, name)
	}
	key = filepath.Join(key, "targets", safeSegment(tgt.Identity()))
	return filepath.Join(stateBase(), "accorda", "receipts", key+".jsonl")
}

// safeSegment returns a filesystem-safe suffix for a target's receipt journal
// key. It is a digest of the target's identity so the journal is deterministic
// and collision-resistant without exposing the raw path in the state
// directory.
func safeSegment(identity string) string {
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", digest[:8])
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
// DriftPolicy (docs/ACCORDA.md §5.3, docs/DECISIONS.md #26). The config loader
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
		return nil, fmt.Errorf("sync: notifications.webhook is enabled but webhooks is not configured")
	}
	con, err := webhook.New(*n.WebhookConfig, webhook.WithErrorSink(func(err error) {
		fmt.Fprintf(os.Stderr, "sync: webhook: %v\n", err)
	}))
	if err != nil {
		return nil, fmt.Errorf("sync: %w", err)
	}
	return con.Subscribe(bus), nil
}

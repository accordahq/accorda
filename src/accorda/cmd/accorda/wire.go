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
func buildSource(p *config.Project, dir, name string) (*git.Git, error) {
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
	if name != "" {
		namespace = filepath.Join(namespace, name)
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

// buildTarget constructs the deployment target for one project by
// dispatching to the registered target builder (docs/ACCORDA.md §12). The
// command layer does not switch on target type or import concrete drivers:
// each driver package registers a TargetBuilder via init, and BuildTarget
// selects it from the registry. An unimplemented target type (kubernetes,
// helm) surfaces as a clear "not implemented" error.
//
// name is the operator project name in an ensemble document, or empty for a
// single project. It doubles as the Compose project name override and the
// image target's service name.
func buildTarget(p *config.Project, dir string, worktree sources.Worktree, name string) (targets.Target, error) {
	ctx := targets.TargetContext{Project: *p, Dir: dir, Name: name, Worktree: worktree}
	return targets.BuildTarget(ctx)
}

// desiredAt opens a source revision, delegates target-specific artifact
// loading to the target, and releases any private historical materialization.
func desiredAt(ctx context.Context, src sources.Source, target targets.Target, ref *sources.Commit) (_ *state.DesiredState, err error) {
	revision, err := src.Revision(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, revision.Close()) }()
	return target.Desired(ctx, revision)
}

// buildEnsembleMembers constructs the per-project source, target, receipts,
// lock, bus, and reconciler wiring. CLI orchestration supplies only the
// progress renderer factory.
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
		if progress != nil {
			unsubscribers = append(unsubscribers, bus.Subscribe(progress(p.Name)))
		}
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

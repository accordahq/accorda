// Package main — `accorda inspect` (docs/ACCORDA.md §11).
//
// inspect shows the per-service detail of a specific deployment: the previous
// and deployed image digests, whether the service was recreated, and the
// health result. It is read-only: it reads the local receipt journal and
// never mutates the target or the source. It needs no running Docker daemon
// and no Git fetch — the journal records the digests and the changed-service
// names, so the §11 inspect view is reconstructible from receipts alone
// (docs/ACCORDA.md §7).
package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/history"
)

// inspectService is one service's inspect view. It is a plain struct so
// tests can exercise the formatting and field logic without a live journal.
type inspectService struct {
	name           string
	previousDigest string
	deployedDigest string
	recreated      bool
	health         string
	// unchanged is true when the service was not changed by this cycle, so
	// the §11 example prints "unchanged" instead of the digest/health rows.
	unchanged bool
}

// newInspectCmd builds the `accorda inspect` command (docs/ACCORDA.md §11).
// It prints the per-service previous/deployed digests, recreated flag, and
// health result for a given commit. The command is read-only and works from
// the local receipt journal, so it does not require a running Docker daemon
// or a Git fetch.
func newInspectCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "inspect [commit]",
		Short: "show details for a specific deployment",
		Long: "Show per-service deployment detail for a given commit\n" +
			"(docs/ACCORDA.md §11): previous and deployed image digests,\n" +
			"whether the service was recreated, and the health result. inspect\n" +
			"is read-only and reads the local receipt journal, so it does not\n" +
			"require a running Docker daemon or a Git fetch. With no commit\n" +
			"argument it inspects the most recent deployment.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commit := ""
			if len(args) == 1 {
				commit = args[0]
			}
			if err := runInspect(cmd, dir, commit); err != nil {
				return fmt.Errorf("inspect: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	return c
}

// runInspect loads the project, reads the receipt journal, and prints the
// inspect view for the given commit (or the most recent deployment when the
// commit is empty). A project-level error (config load) is fatal; an unknown
// commit or empty journal is reported as an error so the operator knows the
// deployment was never recorded.
func runInspect(cmd *cobra.Command, dir, commit string) error {
	projects, err := loadProjects(dir)
	if err != nil {
		return err
	}
	for i := range projects {
		p := &projects[i]
		if err := runInspectOne(cmd, dir, commit, p); err != nil {
			return fmt.Errorf("inspect %s: %w", p.Name, err)
		}
	}
	return nil
}

// runInspectOne inspects the deployment history for a single project.
func runInspectOne(cmd *cobra.Command, dir, commit string, p *config.Project) error {
	store := history.NewFileStore(receiptPath(dir, p.Name))
	services, err := collectInspect(context.Background(), store, commit)
	if err != nil {
		return err
	}
	if p.Name != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", p.Name)
	}
	writeInspect(cmd.OutOrStdout(), services)
	return nil
}

// collectInspect reads the receipt journal and builds the per-service inspect
// view for the deployment at commit. When commit is empty it inspects the
// most recent receipt. The commit may be a full SHA or a short prefix; it is
// matched against the receipt Commit field (which stores the full SHA).
//
// For each service the previous digest is read from the most recent healthy
// receipt *before* the inspected one that declares the service, so the
// "previous digest" reflects what was running before this cycle. The deployed
// digest is the inspected receipt's digest. "recreated" is true when the
// service is in the receipt's Changes list. "health" is passed/failed from the
// receipt outcome. A service not in Changes (and present in Services) is
// unchanged and printed as such, matching the §11 example.
func collectInspect(ctx context.Context, store history.Store, commit string) ([]inspectService, error) {
	if store == nil {
		return nil, fmt.Errorf("no deployment journal")
	}
	receipts, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	idx, err := findReceipt(receipts, commit)
	if err != nil {
		return nil, err
	}
	rc := receipts[idx]
	previous := previousHealthyBefore(receipts, idx)

	// Build the per-service view for every service the inspected receipt
	// declares. Sorted by name for deterministic output
	// (docs/DECISIONS.md #12).
	names := rc.SortedServiceNames()
	services := make([]inspectService, 0, len(names))
	changed := toSet(rc.Changes)
	for _, name := range names {
		svc := rc.Services[name]
		_, isChanged := changed[name]
		services = append(services, inspectService{
			name:           name,
			previousDigest: previousDigest(previous, name),
			deployedDigest: svc.Digest,
			recreated:      isChanged,
			health:         inspectHealthLabel(rc.Result),
			unchanged:      !isChanged,
		})
	}
	return services, nil
}

// findReceipt returns the index of the receipt matching commit. When commit
// is empty it returns the most recent receipt (last in append order). The
// commit may be a full SHA or a short prefix of the receipt's full SHA. When
// multiple receipts match a prefix (a commit can be deployed more than once,
// for example a rollback that restored a prior commit), the most recent match
// is returned so `inspect <short>` reflects the latest cycle for that commit.
func findReceipt(receipts []history.Receipt, commit string) (int, error) {
	if len(receipts) == 0 {
		return -1, fmt.Errorf("no deployments recorded")
	}
	if commit == "" {
		return len(receipts) - 1, nil
	}
	// Match either the full SHA or a short prefix, so a user can pass the
	// 7-character SHA the history/inspect tables display. Iterate newest
	// first so a commit deployed more than once resolves to its latest cycle.
	for i := len(receipts) - 1; i >= 0; i-- {
		if receipts[i].Commit == commit || strings.HasPrefix(receipts[i].Commit, commit) {
			return i, nil
		}
	}
	return -1, fmt.Errorf("no deployment found for commit %q", commit)
}

// previousHealthyBefore returns the most recent OutcomeHealthy receipt before
// the receipt at idx, or nil when there is none. It is the source of the
// "previous digest" column: the digests of the deployment that was running
// before the inspected cycle.
func previousHealthyBefore(receipts []history.Receipt, idx int) *history.Receipt {
	for j := idx - 1; j >= 0; j-- {
		if receipts[j].Result == history.OutcomeHealthy {
			cp := receipts[j]
			return &cp
		}
	}
	return nil
}

// previousDigest returns the digest recorded for name in the previous healthy
// receipt, or an empty string when there is no previous deployment or the
// service was not present in it.
func previousDigest(previous *history.Receipt, name string) string {
	if previous == nil {
		return ""
	}
	if svc, ok := previous.Services[name]; ok {
		return svc.Digest
	}
	return ""
}

// inspectHealthLabel maps a receipt outcome to the §11 inspect health column.
// A healthy deployment passed; a failed or rolled-back deployment did not. An
// unknown outcome degrades to the raw value so the column is never blank.
func inspectHealthLabel(o history.Outcome) string {
	switch o {
	case history.OutcomeHealthy:
		return "passed"
	case history.OutcomeFailed, history.OutcomeRolledBack, history.OutcomeInterrupted:
		return "failed"
	case history.OutcomeInProgress:
		return "in_progress"
	default:
		return string(o)
	}
}

// toSet builds a set membership lookup from a string slice.
func toSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}

// writeInspect prints the per-service inspect view in the format shown in
// docs/ACCORDA.md §11. A changed service prints its previous/deployed
// digests, recreated flag, and health; an unchanged service prints a single
// "unchanged" line.
func writeInspect(w io.Writer, services []inspectService) {
	for _, s := range services {
		fmt.Fprintf(w, "%s\n", s.name)
		if s.unchanged {
			fmt.Fprintf(w, "  unchanged\n")
			continue
		}
		fmt.Fprintf(w, "  previous digest    %s\n", digestOrDash(s.previousDigest))
		fmt.Fprintf(w, "  deployed digest    %s\n", digestOrDash(s.deployedDigest))
		fmt.Fprintf(w, "  recreated          %s\n", yesNo(s.recreated))
		fmt.Fprintf(w, "  health             %s\n", s.health)
	}
}

// digestOrDash prints a digest, or a dash when it is empty (the receipt could
// not resolve it, or there was no previous deployment).
func digestOrDash(digest string) string {
	if digest == "" {
		return "-"
	}
	return digest
}

// yesNo renders a boolean as the yes/no the §11 example uses.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

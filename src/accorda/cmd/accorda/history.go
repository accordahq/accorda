// Package main — `accorda history` (docs/ACCORDA.md §11).
//
// history prints the deployment journal as the table the spec's §11 example
// describes: one row per deployment cycle with the time, commit, result, and
// the services that changed. It is read-only: it reads the local receipt
// journal and never mutates the target or the source. It needs no running
// Docker daemon and no Git fetch — the journal is the source of truth for
// "what was deployed, when, and what was the outcome" (docs/ACCORDA.md §7).
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

// historyRow is one row of the deployment history table. It is a plain
// struct so tests can exercise the formatting and field logic without a
// live receipt journal.
type historyRow struct {
	time    string
	commit  string
	result  string
	changes string
}

// newHistoryCmd builds the `accorda history` command (docs/ACCORDA.md §11).
// It prints the deployment journal as a table of time, commit, result, and
// changed services. The command is read-only and works from the local
// receipt journal, so it does not require a running Docker daemon or a Git
// fetch.
func newHistoryCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "history",
		Short: "show deployment history",
		Long: "Show the deployment history from the local receipt journal\n" +
			"(docs/ACCORDA.md §11). Prints one row per deployment cycle with\n" +
			"the time, commit, result (✓ healthy / ✗ failed / ↺ rolled_back),\n" +
			"and the services that changed. history is read-only and does not\n" +
			"require a running Docker daemon or a Git fetch.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runHistory(cmd, dir); err != nil {
				return fmt.Errorf("history: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	return c
}

// runHistory loads the project, reads the receipt journal, and prints the
// deployment history table to the command's output. A project-level error
// (config load) is fatal; an empty journal prints the header with no rows.
func runHistory(cmd *cobra.Command, dir string) error {
	proj, err := config.Load(dir)
	if err != nil {
		return err
	}
	// The environment is read so the command validates the project, but it
	// is not printed: the §11 history table is per-deployment, not per-
	// environment. Loading the project also resolves the receipt journal
	// path keyed by the project directory (receiptPath).
	_ = proj

	store := history.NewFileStore(receiptPath(dir))
	rows, err := collectHistory(context.Background(), store)
	if err != nil {
		return err
	}
	writeHistory(cmd.OutOrStdout(), rows)
	return nil
}

// collectHistory reads the receipt journal and builds the history rows. The
// journal is appended in chronological order (oldest first; history.Store.List
// returns append order, which is the order reconcile recorded the receipts),
// so history is printed newest first simply by reversing the receipts rather
// than sorting. Sorting by the truncated HH:MM time column would reorder
// same-minute deployments non-deterministically and misorder cross-midnight
// deployments; reversing preserves the true chronological order for both
// (docs/ACCORDA.md §11 most-recent-at-top). A nil store yields no rows.
func collectHistory(ctx context.Context, store history.Store) ([]historyRow, error) {
	if store == nil {
		return nil, nil
	}
	receipts, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	rows := make([]historyRow, 0, len(receipts))
	for i := len(receipts) - 1; i >= 0; i-- {
		rc := receipts[i]
		rows = append(rows, historyRow{
			time:    rc.CompletedAt.UTC().Format(historyTimeFormat),
			commit:  shortSHA(rc.Commit),
			result:  resultLabel(rc.Result),
			changes: joinChanges(rc.Changes),
		})
	}
	return rows, nil
}

// historyTimeFormat is the time format for the history table, matching the
// §11 example (HH:MM). It is UTC so the column is stable across timezones.
const historyTimeFormat = "15:04"

// resultLabel maps a receipt outcome to the §11 table glyph: ✓ healthy,
// ✗ failed, ↺ rolled_back. An unknown outcome degrades to the raw value so
// the table is never empty.
func resultLabel(o history.Outcome) string {
	switch o {
	case history.OutcomeHealthy:
		return "✓ healthy"
	case history.OutcomeFailed:
		return "✗ failed"
	case history.OutcomeRolledBack:
		return "↺ rolled_back"
	default:
		return string(o)
	}
}

// joinChanges joins the changed service names with a space. The receipt's
// Changes field is already sorted (docs/DECISIONS.md #12), so the column is
// deterministic. An empty list prints a dash so the column is never blank.
func joinChanges(changes []string) string {
	if len(changes) == 0 {
		return "-"
	}
	return strings.Join(changes, " ")
}

// writeHistory prints the deployment history table in the format shown in
// docs/ACCORDA.md §11. The header is always printed; rows follow in the
// order given (newest first).
func writeHistory(w io.Writer, rows []historyRow) {
	fmt.Fprintf(w, "%-20s %-10s %-14s %s\n", "TIME", "COMMIT", "RESULT", "CHANGES")
	for _, r := range rows {
		fmt.Fprintf(w, "%-20s %-10s %-14s %s\n", r.time, r.commit, r.result, r.changes)
	}
}

// Package main — `accorda logs` (docs/ACCORDA.md §11).
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/targets"
)

// newLogsCmd builds the operational, read-only log command. A service is
// required so the target driver can select the corresponding workload; -f
// follows new output and --tail limits the initial snapshot.
func newLogsCmd() *cobra.Command {
	var (
		dir    string
		follow bool
		tail   string
	)
	c := &cobra.Command{
		Use:   "logs SERVICE",
		Short: "fetch or follow logs for a service",
		Long: "Fetch logs for a service through the configured target driver.\n" +
			"Use --follow to keep streaming new output (docs/ACCORDA.md §11).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := targets.LogOptions{Follow: follow, Tail: tail}
			if err := runLogs(cmd, dir, args[0], opts); err != nil {
				return fmt.Errorf("logs: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow new log output")
	c.Flags().StringVar(&tail, "tail", "all", "number of lines to show from the end, or all")
	return c
}

func runLogs(cmd *cobra.Command, dir, service string, opts targets.LogOptions) error {
	proj, err := config.Load(dir)
	if err != nil {
		return err
	}
	tgt, err := buildTarget(proj, dir)
	if err != nil {
		return err
	}
	return tgt.Logs(cmd.Context(), service, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

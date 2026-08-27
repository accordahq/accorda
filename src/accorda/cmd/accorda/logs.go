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
	projects, err := loadProjects(dir)
	if err != nil {
		return err
	}
	for i := range projects {
		p := &projects[i]
		if err := runLogsOne(cmd, dir, service, opts, p); err != nil {
			return fmt.Errorf("logs %s: %w", p.Name, err)
		}
	}
	return nil
}

// runLogsOne fetches logs for a single project's service across its targets.
func runLogsOne(cmd *cobra.Command, dir, service string, opts targets.LogOptions, p *config.Project) error {
	src, err := buildSource(p, dir)
	if err != nil {
		return err
	}
	tgts := p.NormalizedTargets()
	for i := range tgts {
		tgtCfg := tgts[i]
		tgt, err := buildTargetConfig(p, tgtCfg, dir, src, p.Name)
		if err != nil {
			return err
		}
		logTarget, ok := tgt.(targets.LogTarget)
		if !ok {
			return fmt.Errorf("logs %s: target type %q does not support logs", p.Name, tgtCfg.Type)
		}
		if err := logTarget.Logs(cmd.Context(), service, opts, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			return err
		}
	}
	return nil
}

// Package main — `accorda doctor` (docs/ACCORDA.md §11, §45).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/sources/git"
)

const (
	doctorPass    = "PASS"
	doctorFail    = "FAIL"
	doctorProject = "Project configuration"
	doctorSource  = "Git source configuration"
	doctorCompose = "Compose target and Docker"
)

var errDoctorFailed = errors.New("one or more checks failed")

// doctorResult is one local installation or configuration check. Keeping the
// report as data lets the command print every completed check before returning
// a non-zero status when operator action is required.
type doctorResult struct {
	name   string
	status string
	detail string
}

// newDoctorCmd builds the read-only installation and configuration diagnostic
// command. It validates the project, Git source settings, Compose file, and
// Docker engine connectivity without fetching Git or changing the target.
func newDoctorCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "doctor",
		Short: "check the local Accorda installation and configuration",
		Long: "Check the Accorda project configuration, Git source settings,\n" +
			"Compose target, and Docker engine connectivity without making changes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			results := diagnose(cmd.Context(), dir)
			writeDoctorReport(cmd.OutOrStdout(), results)
			if doctorFailed(results) {
				return fmt.Errorf("doctor: %w", errDoctorFailed)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	return c
}

// diagnose runs checks in dependency order. A malformed or missing project
// prevents construction of the source and target, so that failure is returned
// alone instead of producing misleading follow-on failures.
func diagnose(ctx context.Context, dir string) []doctorResult {
	proj, err := config.Load(dir)
	results := []doctorResult{doctorCheck(doctorProject, err)}
	if err != nil {
		return results
	}

	src := git.New(proj.Source)
	results = append(results, doctorCheck(doctorSource, src.Validate(ctx)))

	proj.Target = resolveDoctorTargetPaths(dir, proj.Target)
	tgt, err := buildTarget(proj)
	if err == nil {
		err = tgt.Validate(ctx)
	}
	return append(results, doctorCheck(doctorCompose, err))
}

// resolveDoctorTargetPaths interprets relative target paths from the directory
// containing accorda.yaml. This makes --dir behave like a project root instead
// of accidentally resolving the default compose.yaml against the caller's
// working directory.
func resolveDoctorTargetPaths(dir string, target config.Target) config.Target {
	if target.File != "" && !filepath.IsAbs(target.File) {
		target.File = filepath.Join(dir, target.File)
	}
	if target.Path != "" && !filepath.IsAbs(target.Path) {
		target.Path = filepath.Join(dir, target.Path)
	}
	return target
}

func doctorCheck(name string, err error) doctorResult {
	if err == nil {
		return doctorResult{name: name, status: doctorPass}
	}
	return doctorResult{name: name, status: doctorFail, detail: err.Error()}
}

func doctorFailed(results []doctorResult) bool {
	for _, result := range results {
		if result.status == doctorFail {
			return true
		}
	}
	return false
}

func writeDoctorReport(w io.Writer, results []doctorResult) {
	for _, result := range results {
		if result.detail == "" {
			fmt.Fprintf(w, "%s  %s\n", result.status, result.name)
			continue
		}
		fmt.Fprintf(w, "%s  %s: %s\n", result.status, result.name, result.detail)
	}
	if !doctorFailed(results) {
		fmt.Fprintln(w, "Accorda is ready.")
	}
}

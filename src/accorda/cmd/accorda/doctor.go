// Package main — `accorda doctor` (docs/ACCORDA.md §11, §45).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	gitSource "accorda/internal/sources/git"
)

const (
	doctorPass     = "PASS"
	doctorFail     = "FAIL"
	doctorInfo     = "INFO"
	doctorProject  = "Project configuration"
	doctorSource   = "Git source configuration"
	doctorTarget   = "Deployment target and Docker"
	doctorCheckout = "Managed checkout"
)

var errDoctorFailed = errors.New("one or more checks failed")

// validateEnvironmentTarget is the optional capability a target exposes so
// doctor can validate the deployment environment (Docker engine, CLI) before
// a managed Git checkout exists. The Compose target implements it; the image
// target validates the engine in Validate and does not need the fallback.
type validateEnvironmentTarget interface {
	ValidateEnvironment(ctx context.Context) error
}

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
			"deployment target, and Docker engine connectivity without making changes.",
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
// alone instead of producing misleading follow-on failures. In a multi-project
// document, each member is diagnosed independently and its results are
// prefixed with the member name (docs/ACCORDA.md §49).
func diagnose(ctx context.Context, dir string) []doctorResult {
	projects, err := loadProjects(dir)
	if err != nil {
		return []doctorResult{doctorCheck(doctorProject, err)}
	}
	var results []doctorResult
	for i := range projects {
		p := &projects[i]
		results = append(results, diagnoseProject(ctx, dir, p)...)
	}
	return results
}

// diagnoseProject runs the dependency-order checks for one project. The
// results are prefixed with the project name so a multi-project report stays
// attributable to its workload; a single unnamed project prints with no
// prefix. In a multi-target project each target is checked independently.
func diagnoseProject(ctx context.Context, dir string, p *config.Project) []doctorResult {
	results := []doctorResult{doctorCheck(doctorProject, nil)}

	src, err := buildSource(p, dir)
	if err != nil {
		return append(results, doctorCheck(doctorSource, err))
	}
	results = append(results, doctorCheck(doctorSource, src.Validate(ctx)))

	targets := p.NormalizedTargets()
	for i := range targets {
		tgtCfg := targets[i]
		tgt, err := buildTargetConfig(p, tgtCfg, dir, src, p.Name)
		if err == nil {
			err = tgt.Validate(ctx)
			if err != nil && managedTargetPending(src, tgtCfg, err) {
				// The Compose target exposes ValidateEnvironment so doctor can
				// check the Docker engine and Compose CLI before the managed Git
				// checkout exists. The image target validates the engine in
				// Validate, so it does not need this fallback.
				if ve, ok := tgt.(validateEnvironmentTarget); ok {
					err = ve.ValidateEnvironment(ctx)
				}
			}
		}
		results = append(results, doctorCheck(doctorTarget, err))
	}

	// Surface the managed checkout path so operators know where to place
	// gitignored deployment-time inputs (env_file, label_file) that Compose
	// resolves relative to the checkout. Only shown when the source is valid.
	if checkoutDir, dirErr := src.CheckoutDir(); dirErr == nil {
		results = append(results, doctorResult{
			name:   doctorCheckout,
			status: doctorInfo,
			detail: checkoutDir,
		})
	}

	if p.Name != "" {
		for i := range results {
			results[i].name = p.Name + ": " + results[i].name
		}
	}
	return results
}

func managedTargetPending(src *gitSource.Git, target config.Target, err error) bool {
	if filepath.IsAbs(target.ConfiguredPath()) || !errors.Is(err, os.ErrNotExist) || src == nil {
		return false
	}
	exists, checkoutErr := src.CheckoutExists()
	return checkoutErr == nil && !exists
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

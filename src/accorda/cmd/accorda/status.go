// Package main — `accorda status` (docs/ACCORDA.md §11).
//
// status is the production wiring point that reads the project's runtime
// picture and prints it. It is read-only: it never mutates the target or the
// source. It reports the environment, repository, branch, Git HEAD, deployed
// commit, sync status, runtime status, last deploy time, and a per-service
// table of state/health/image, matching the spec's §11 example.
package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/health"
	"accorda/internal/core/history"
	"accorda/internal/core/state"
	"accorda/internal/sources"
	"accorda/internal/sources/git"
	"accorda/internal/targets/compose"
)

// newStatusCmd builds the `accorda status` command (docs/ACCORDA.md §11). It
// prints the project's runtime posture: environment, repository, branch, Git
// HEAD, deployed commit, sync status, runtime status, last deploy time, and a
// per-service table of state/health/image. The command is read-only and makes
// no changes to the target or source.
func newStatusCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "status",
		Short: "show environment, repo, branch, Git HEAD, deployed SHA, sync, runtime, services",
		Long: "Print the current status of the project: environment, repository,\n" +
			"branch, Git HEAD, deployed commit, sync/runtime status, last deploy\n" +
			"time, and a per-service table of state/health/image (docs/ACCORDA.md §11).\n" +
			"status is read-only and does not change the target or source.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runStatus(cmd, dir); err != nil {
				return fmt.Errorf("status: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	return c
}

// statusInfo is the aggregated, formatted view the status command prints. It
// is a plain struct so tests can exercise the formatting and field logic
// without a live source/target.
type statusInfo struct {
	Environment string
	Repository  string
	Branch      string
	GitHead     string
	Deployed    string
	Sync        string
	Runtime     string
	LastDeploy  string
	// services is the per-service table, sorted by name for deterministic
	// output (docs/DECISIONS.md #12).
	services []statusService
}

// statusService is one row of the per-service table.
type statusService struct {
	name   string
	state  string
	health string
	image  string
}

// runStatus loads the project, constructs the source and target, and prints
// the status report to the command's output. It reports a partial status
// (best-effort) when a non-critical read fails so an operator can still see
// the fields that were resolved; a project-level error (config load, target
// construction) is fatal.
func runStatus(cmd *cobra.Command, dir string) error {
	proj, err := config.Load(dir)
	if err != nil {
		return err
	}
	src, err := buildSource(proj)
	if err != nil {
		return err
	}
	tgt, err := buildTarget(proj, dir, src)
	if err != nil {
		return err
	}

	ctx := context.Background()
	info := collectStatus(ctx, proj, src, tgt, history.NewFileStore(receiptPath(dir)))
	writeStatus(cmd.OutOrStdout(), info)
	return nil
}

// collectStatus gathers every field the status table needs, tolerating
// failures of the non-critical source/runtime/history reads. The target must
// be reachable (it is needed for the runtime state); a nil target yields the
// source-only part of the report.
func collectStatus(ctx context.Context, proj *config.Project, src sources.Source, tgt *compose.Target, store history.Store) statusInfo {
	info := statusInfo{
		Environment: proj.Environment,
		// Redact the configured URL up front so a raw embedded token is never
		// surfaced, even when the source cannot be read and Desired never
		// overrides it (docs/ACCORDA.md §18, §56). This mirrors the same
		// redaction the git source applies to DesiredState.Repository.
		Repository: git.RedactURL(proj.Source.URL),
		Branch:     proj.Source.Branch,
	}

	// Fetch the Git HEAD so the report shows the commit Git declares. This
	// also populates the branch/repository from the source on success.
	commit, err := src.Fetch(ctx)
	if err != nil {
		info.GitHead = "unavailable"
	} else {
		info.GitHead = shortSHA(commit.SHA)
		if commit.Branch != "" {
			info.Branch = commit.Branch
		}
	}

	// The last healthy deployment from history supplies the deployed commit
	// and the last-deploy time. When history has none, both stay empty.
	if rc, err := lastHealthyReceipt(store); err == nil && rc != nil {
		info.Deployed = shortSHA(rc.Commit)
		info.LastDeploy = rc.CompletedAt.UTC().Format("2006-01-02 15:04:05")
	}

	// The Sync label is derivable purely from the Git HEAD and the deployed
	// commit, so it is computed before any target read. This keeps the line
	// populated even when the runtime is unreachable, consistent with the
	// best-effort partial-report design.
	info.Sync = syncLabel(info.GitHead, info.Deployed)

	// The runtime and its health are read from the target. If the target is
	// unreachable, report the runtime as unavailable and skip the service
	// table so the command still prints the configuration-level status.
	if tgt == nil {
		info.Runtime = "unknown"
		return info
	}
	runtime, err := tgt.Current(ctx)
	if err != nil {
		info.Runtime = "unreachable"
		return info
	}
	// The aggregate runtime label and the per-service health column both come
	// from the same health mapping the reconcile loop's Health phase uses
	// (compose.HealthFromRuntime), so `status` and a live sync agree on what
	// "healthy" means (docs/ACCORDA.md §19).
	hc := compose.HealthFromRuntime(runtime, time.Now())
	info.Runtime = healthLabel(hc)

	// The desired state from Git supplies the redacted repository and the
	// service table's declared images. It is best-effort: on failure the
	// runtime table is still printed and the repository stays redacted from
	// the configured URL.
	desired, desiredErr := src.Desired(ctx, nil)
	if desiredErr == nil && desired != nil {
		if desired.Repository != "" {
			info.Repository = desired.Repository
		}
		if desired.Branch != "" {
			info.Branch = desired.Branch
		}
	} else {
		desired = nil
	}
	info.services = buildRows(desired, runtime, hc)
	return info
}

// shortSHA abbreviates a full commit SHA to 7 characters, matching the §11
// example (d71b2e4). It leaves already-short SHAs unchanged.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// syncLabel classifies the desired-vs-deployed relationship as the spec's
// SYNCED / OUT_OF_SYNC labels (docs/ACCORDA.md §5). It compares the Git HEAD
// and the last healthy deployed commit.
func syncLabel(gitHead, deployed string) string {
	if gitHead == "unavailable" {
		return "UNKNOWN"
	}
	if deployed == "" || deployed != gitHead {
		return "OUT_OF_SYNC"
	}
	return "SYNCED"
}

// healthLabel summarizes the aggregate health from the same mapping the
// reconcile loop uses (docs/ACCORDA.md §11, §19). UNHEALTHY when the
// deployment's overall health is unhealthy; otherwise HEALTHY — including
// StatusUnknown (a target without health checks) and StatusStarting, neither
// of which the reconcile loop treats as a failed deployment. UNKNOWN when no
// services are running.
func healthLabel(hc *health.Health) string {
	if hc == nil || len(hc.Services) == 0 {
		return "UNKNOWN"
	}
	if hc.Overall == health.StatusUnhealthy {
		return "UNHEALTHY"
	}
	return "HEALTHY"
}

// buildRows derives the per-service table from the desired (Git) and runtime
// states plus the health mapping. Each service's state/health/image come from
// the running container when present; otherwise the state reflects the
// desired-but-not-running service. Rows are sorted by service name for
// deterministic output (docs/DECISIONS.md #12).
func buildRows(desired *state.DesiredState, runtime *state.RuntimeState, hc *health.Health) []statusService {
	names := unionServiceNames(desired, runtime)
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	rows := make([]statusService, 0, len(sorted))
	for _, n := range sorted {
		rows = append(rows, buildRow(n, desired, runtime, hc))
	}
	return rows
}

// unionServiceNames returns the set of service names across the desired and
// runtime states.
func unionServiceNames(desired *state.DesiredState, runtime *state.RuntimeState) map[string]struct{} {
	names := map[string]struct{}{}
	if desired != nil {
		for n := range desired.Services {
			names[n] = struct{}{}
		}
	}
	if runtime != nil {
		for n := range runtime.Services {
			names[n] = struct{}{}
		}
	}
	return names
}

// buildRow derives a single service row. State and image come from the
// running container when present; the declared image from Git fills in when
// the service is not running. Missing fields get a stable placeholder.
func buildRow(n string, desired *state.DesiredState, runtime *state.RuntimeState, hc *health.Health) statusService {
	row := statusService{name: n}
	if svc, ok := runtimeService(runtime, n); ok {
		row.state = svc.Status
		row.image = svc.Image
		if sh, ok := healthService(hc, n); ok {
			row.health = string(sh.Status)
		}
	}
	if row.image == "" {
		if svc, ok := desiredService(desired, n); ok {
			row.image = svc.Image
		}
	}
	if row.state == "" {
		row.state = "absent"
	}
	if row.health == "" {
		row.health = "-"
	}
	if row.image == "" {
		row.image = "-"
	}
	return row
}

// runtimeService returns the running service for name, if present.
func runtimeService(runtime *state.RuntimeState, name string) (state.RuntimeService, bool) {
	if runtime == nil {
		return state.RuntimeService{}, false
	}
	svc, ok := runtime.Services[name]
	return svc, ok
}

// healthService returns the health for name, if present.
func healthService(hc *health.Health, name string) (health.ServiceHealth, bool) {
	if hc == nil {
		return health.ServiceHealth{}, false
	}
	sh, ok := hc.Services[name]
	return sh, ok
}

// desiredService returns the declared service for name, if present.
func desiredService(desired *state.DesiredState, name string) (state.Service, bool) {
	if desired == nil {
		return state.Service{}, false
	}
	svc, ok := desired.Services[name]
	return svc, ok
}

// writeStatus prints the status report in the tabular format shown in
// docs/ACCORDA.md §11.
func writeStatus(w io.Writer, info statusInfo) {
	fmt.Fprintf(w, "Environment   %s\n", info.Environment)
	fmt.Fprintf(w, "Repository    %s\n", info.Repository)
	fmt.Fprintf(w, "Branch        %s\n", info.Branch)
	fmt.Fprintf(w, "Git HEAD      %s\n", info.GitHead)
	fmt.Fprintf(w, "Deployed      %s\n", info.Deployed)
	fmt.Fprintf(w, "Sync          %s\n", info.Sync)
	fmt.Fprintf(w, "Runtime       %s\n", info.Runtime)
	fmt.Fprintf(w, "Last deploy   %s\n", info.LastDeploy)
	fmt.Fprintf(w, "SERVICE      STATE       HEALTH      IMAGE\n")
	for _, r := range info.services {
		fmt.Fprintf(w, "%-12s %-11s %-11s %s\n", r.name, r.state, r.health, r.image)
	}
}

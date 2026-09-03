// Package main — `accorda diff` (docs/ACCORDA.md §11).
//
// diff shows the per-field deployed vs desired comparison the spec's §11
// example describes. It is read-only: it never mutates the target or the
// source. The "deployed" side is the last known-healthy deployment, read as
// target state at the last healthy Git revision (the receipt journal
// records only the image/digest per service, so the full per-field definition
// must be reloaded through the target). The "desired" side is the current Git
// HEAD. Only services and fields that differ are printed, in a YAML-like
// tree, with per-field deployed/desired values.
package main

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"accorda/internal/config"
	"accorda/internal/core/history"
	"accorda/internal/core/state"
	"accorda/internal/secrets"
	"accorda/internal/sources"
	"accorda/internal/targets"
)

// newDiffCmd builds the `accorda diff` command (docs/ACCORDA.md §11). It
// prints the per-field deployed-vs-desired comparison without changing the
// target or source. The command is read-only and works from Git plus the
// deployment history, so it does not require a running Docker daemon.
func newDiffCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "diff",
		Short: "show deployed vs desired changes",
		Long: "Show the per-field deployed vs desired changes (docs/ACCORDA.md §11).\n" +
			"The deployed side is the last healthy deployment from history, re-read\n" +
			"from Git; the desired side is the current Git HEAD. diff is read-only\n" +
			"and does not change the target or source.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runDiff(cmd, dir); err != nil {
				return fmt.Errorf("diff: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", ".", "project directory")
	return c
}

// diffNode is one row in the diff tree. It is either a leaf carrying a
// per-field deployed/desired value pair, or a branch whose children hold the
// nested details (for example a service's environment variables). Rendering
// prints the label, the leaf values when present, and then the children,
// indenting one level per nesting.
type diffNode struct {
	label    string
	deployed string
	desired  string
	hasValue bool
	children []diffNode
}

// runDiff loads the project, reads target desired state at Git HEAD and the
// deployed state from the last healthy deployment, and prints the per-field
// diff to the command's output. The deployed side is reloaded by the target
// at the deployed commit so the full service definition is available for
// per-field comparison (the receipt journal stores only image/digest).
func runDiff(cmd *cobra.Command, dir string) error {
	projects, err := loadProjects(dir)
	if err != nil {
		return err
	}
	for i := range projects {
		p := &projects[i]
		if err := runDiffOne(cmd, dir, p); err != nil {
			return fmt.Errorf("diff %s: %w", p.Name, err)
		}
	}
	return nil
}

// runDiffOne computes and prints the diff for a single project's targets.
// name is the project's operator-chosen name (empty for a single-project
// document), used to scope the source and receipt journal.
func runDiffOne(cmd *cobra.Command, dir string, p *config.Project) error {
	src, err := buildSource(p, dir)
	if err != nil {
		return err
	}
	ctx := context.Background()

	// Re-reading the deployed commit from the managed worktree temporarily
	// checks out a historical revision, so diff takes the same deployment lock
	// as sync to avoid racing a concurrent deployment (docs/DECISIONS.md #40).
	targets := p.NormalizedTargets()
	multiTarget := len(targets) > 1
	for i := range targets {
		tgtCfg := targets[i]
		tgt, err := buildTargetConfig(p, tgtCfg, dir, src, p.Name)
		if err != nil {
			return err
		}
		if err := withDeploymentLock(ctx, dir, tgtCfg, func() error {
			// Fetch first so the desired side reflects the current remote tip, not a
			// stale local cache (the git source's Desired only fetches when the cache
			// is empty). This matches `accorda plan` and `accorda status`.
			commit, err := src.Fetch(ctx)
			if err != nil {
				return fmt.Errorf("fetch desired state: %w", err)
			}
			desired, derr := desiredAt(ctx, src, tgt, &commit)
			if derr != nil && desired == nil {
				return fmt.Errorf("read desired state: %w", derr)
			}
			if derr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: revision cleanup: %v\n", derr)
			}

			store := history.NewFileStore(targetReceiptPath(dir, p.Name, tgtCfg, multiTarget))
			deployed := deployedAtCommit(ctx, src, tgt, store, cmd.ErrOrStderr())

			writeTargetHeader(cmd.OutOrStdout(), p.Name, tgtCfg, multiTarget)
			writeDiff(cmd.OutOrStdout(), buildDiff(deployed, desired))
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// deployedAtCommit returns the full desired state at the last healthy
// deployment's commit, reconstructed from a source revision by the target. It returns nil when
// history has no healthy deployment or the source cannot be read at that
// commit, so the caller degrades to "all desired is new". A store read error
// is reported to warn so an operator can distinguish "no prior healthy
// deployment" from "history could not be read", mirroring `accorda sync`'s
// previousFromHistory. It is shared by `accorda diff` and `accorda plan` so
// both commands use the same full-model deployed baseline.
func deployedAtCommit(ctx context.Context, src sources.Source, target targets.Target, store history.Store, warn io.Writer) *state.DesiredState {
	rc, err := lastHealthyReceipt(store)
	if err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "warning: could not read deployment history: %v\n", err)
		}
		return nil
	}
	if rc == nil {
		return nil
	}
	if d, _ := desiredAt(ctx, src, target, &sources.Commit{SHA: rc.Commit}); d != nil {
		return d
	}
	return nil
}

// deployedStateFromDesired converts a full desired state (re-read from the
// target at the deployed commit) into the DeployedState baseline a target's
// Plan expects. It returns nil when the desired state is nil, so a caller
// with no prior healthy deployment passes a nil baseline and the plan treats
// every desired service as new.
func deployedStateFromDesired(d *state.DesiredState) *state.DeployedState {
	if d == nil {
		return nil
	}
	return &state.DeployedState{
		Commit:   d.Commit,
		Services: d.Services,
	}
}

// buildDiff produces the per-field diff tree from the deployed and desired
// states. Only services with at least one differing field are included, in
// sorted service-name order for deterministic output (docs/DECISIONS.md #7).
func buildDiff(deployed, desired *state.DesiredState) []diffNode {
	names := unionDesiredNames(deployed, desired)
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	roots := make([]diffNode, 0, len(sorted))
	for _, n := range sorted {
		dSvc, _ := desiredService(deployed, n)
		rSvc, _ := desiredService(desired, n)
		children := diffService(dSvc, rSvc)
		if len(children) == 0 {
			continue
		}
		roots = append(roots, diffNode{label: n, children: children})
	}
	return roots
}

// unionDesiredNames returns the set of service names across two desired
// states. A service present in either side is considered, so a service that
// was removed from the desired state still appears (as deployed-only) and a
// newly added one appears (as desired-only).
func unionDesiredNames(a, b *state.DesiredState) map[string]struct{} {
	names := map[string]struct{}{}
	if a != nil {
		for n := range a.Services {
			names[n] = struct{}{}
		}
	}
	if b != nil {
		for n := range b.Services {
			names[n] = struct{}{}
		}
	}
	return names
}

// writeDiff renders the per-service diff tree to w in the YAML-like format
// shown in docs/ACCORDA.md §11.
func writeDiff(w io.Writer, roots []diffNode) {
	for _, r := range roots {
		writeDiffNode(w, r, 0)
	}
}

// writeDiffNode renders one diff tree node and its descendants, indenting two
// spaces per nesting level so a service's fields and nested values line up.
func writeDiffNode(w io.Writer, n diffNode, indent int) {
	pad := strings.Repeat("  ", indent)
	fmt.Fprintf(w, "%s%s\n", pad, n.label)
	if n.hasValue {
		fmt.Fprintf(w, "%s  deployed: %s\n", pad, n.deployed)
		fmt.Fprintf(w, "%s  desired:  %s\n", pad, n.desired)
	}
	for _, c := range n.children {
		writeDiffNode(w, c, indent+1)
	}
}

// diffService compares a deployed and a desired service definition field by
// field, returning only the fields that differ.
func diffService(d, s state.Service) []diffNode {
	var fields []diffNode
	fields = append(fields, diffScalar("image", d.Image, s.Image)...)
	fields = append(fields, diffJoined("command", d.Command, s.Command)...)
	fields = append(fields, diffSensitiveKV("environment", d.Env, s.Env)...)
	fields = append(fields, diffJoined("env_file", externalFileIdentities(d.EnvFiles), externalFileIdentities(s.EnvFiles))...)
	fields = append(fields, diffJoined("ports", state.StringPorts(d.Ports), state.StringPorts(s.Ports))...)
	fields = append(fields, diffJoined("volumes", state.StringVolumes(d.Volumes), state.StringVolumes(s.Volumes))...)
	fields = append(fields, diffJoined("networks", d.Networks, s.Networks)...)
	fields = append(fields, diffKV("labels", d.Labels, s.Labels)...)
	fields = append(fields, diffJoined("label_file", externalFileIdentities(d.LabelFiles), externalFileIdentities(s.LabelFiles))...)
	fields = append(fields, diffHealthcheck(d.Healthcheck, s.Healthcheck)...)
	fields = append(fields, diffJoined("depends_on", d.DependsOn, s.DependsOn)...)
	fields = append(fields, diffScalar("one_shot", strconv.FormatBool(d.OneShot), strconv.FormatBool(s.OneShot))...)
	return fields
}

func externalFileIdentities(files []state.ExternalFile) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		identity := file.Path
		if !file.Required {
			identity += " (optional)"
		}
		if file.Format != "" {
			identity += " format=" + file.Format
		}
		if file.Digest != "" {
			identity += " sha256=" + file.Digest[:min(12, len(file.Digest))]
		}
		out = append(out, identity)
	}
	return out
}

// diffScalar returns a single-value field node when deployed and desired
// differ, or nil when they are equal.
func diffScalar(name, deployed, desired string) []diffNode {
	if deployed == desired {
		return nil
	}
	return []diffNode{{label: name, hasValue: true, deployed: deployed, desired: desired}}
}

// diffJoined returns a single leaf node joining the deployed and desired
// slices when they differ. Slices are assumed to be in a stable order
// (networks, depends_on, and command are preserved in definition order;
// ports/volumes are sorted by formatPorts/formatVolumes).
func diffJoined(name string, deployed, desired []string) []diffNode {
	if slices.Equal(deployed, desired) {
		return nil
	}
	return []diffNode{{label: name, hasValue: true, deployed: strings.Join(deployed, ", "), desired: strings.Join(desired, ", ")}}
}

// diffKV returns a nested node of key→deployed/desired leaves for non-sensitive
// map-typed fields when any key differs. The keys are sorted for
// deterministic output (docs/DECISIONS.md #7).
func diffKV(name string, deployed, desired map[string]string) []diffNode {
	keys := sortedMapKeys(deployed, desired)
	children := make([]diffNode, 0, len(keys))
	for _, k := range keys {
		if deployed[k] == desired[k] {
			continue
		}
		children = append(children, diffNode{label: k, hasValue: true, deployed: deployed[k], desired: desired[k]})
	}
	if len(children) == 0 {
		return nil
	}
	return []diffNode{{label: name, children: children}}
}

// diffSensitiveKV reports changed keys without exposing either plaintext
// value. Presence is retained so additions and removals remain clear.
func diffSensitiveKV(name string, deployed, desired map[string]string) []diffNode {
	keys := sortedMapKeys(deployed, desired)
	children := make([]diffNode, 0, len(keys))
	for _, key := range keys {
		deployedValue, deployedPresent := deployed[key]
		desiredValue, desiredPresent := desired[key]
		if deployedPresent == desiredPresent && deployedValue == desiredValue {
			continue
		}
		children = append(children, diffNode{
			label:    key,
			hasValue: true,
			deployed: secrets.DisplayValue(deployedValue, deployedPresent),
			desired:  secrets.DisplayValue(desiredValue, desiredPresent),
		})
	}
	if len(children) == 0 {
		return nil
	}
	return []diffNode{{label: name, children: children}}
}

func sortedMapKeys(first, second map[string]string) []string {
	keys := make(map[string]struct{}, len(first)+len(second))
	for key := range first {
		keys[key] = struct{}{}
	}
	for key := range second {
		keys[key] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)

	return sorted
}

// diffHealthcheck returns a leaf node when the two healthchecks differ. A
// Healthcheck has a slice field (Test), so equality is structural.
func diffHealthcheck(d, s state.Healthcheck) []diffNode {
	if reflect.DeepEqual(d, s) {
		return nil
	}
	return []diffNode{{label: "healthcheck", hasValue: true, deployed: healthcheckString(d), desired: healthcheckString(s)}}
}

// healthcheckString renders a healthcheck concisely for diff output. It
// includes every reconciliation-relevant field (test, interval, timeout,
// retries, start_period) so a change to any of them is visible in the
// rendered deployed/desired values, and renders a disabled healthcheck
// distinctly from an absent one.
func healthcheckString(h state.Healthcheck) string {
	if h.Disable {
		return "disabled"
	}
	if len(h.Test) == 0 {
		return ""
	}
	return fmt.Sprintf("%s (interval=%v timeout=%v retries=%d start_period=%v)",
		strings.Join(h.Test, " "), h.Interval, h.Timeout, h.Retries, h.StartPeriod)
}

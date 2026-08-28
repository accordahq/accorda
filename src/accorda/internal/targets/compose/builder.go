package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"accorda/internal/config"
	"accorda/internal/sources"
	"accorda/internal/targets"
)

func init() {
	targets.RegisterBuilder(config.TargetCompose, targets.BuilderFunc{
		BuildFn:          BuildFromContext,
		LockIdentityFunc: lockIdentityFromConfig,
	})
}

// lockIdentityFromConfig returns the Compose project name derived from the
// raw config.Target, so the deployment lock is scoped to the project name
// without constructing the driver (docs/ACCORDA.md §47, docs/DECISIONS.md
// #44). When the target carries a name, it is appended to the Compose project
// name so two Compose targets in one project (for example qa and prod
// deploying the same compose file) get isolated Compose namespaces and do
// not collide on Docker labels or --remove-orphans (issue #103,
// docs/DECISIONS.md #53).
func lockIdentityFromConfig(dir string, target config.Target) string {
	resolved := target
	if configured := target.ConfiguredPath(); !filepath.IsAbs(configured) {
		if target.File != "" {
			resolved.File = filepath.Join(dir, target.File)
		} else {
			resolved.Path = filepath.Join(dir, target.Path)
		}
	}
	return config.TargetCompose + "\x00" + disambiguateProject(target.Name, ProjectName(resolved))
}

// disambiguateProject returns the Compose project name for a target: the base
// project name when the target has no name (preserving the legacy
// single-target behavior), or base+"-"+name when the target is named, so two
// named Compose targets in one project deploy into isolated Compose projects.
func disambiguateProject(targetName, baseProject string) string {
	if targetName == "" {
		return baseProject
	}
	return baseProject + "-" + targetName
}

// BuildFromContext is the compose target's TargetBuilder. It resolves the
// Compose file against the Git source's managed checkout (unless the target
// path is absolute), threads the project's pull policy, health timeout,
// environment, and per-service overrides, and derives the Compose project
// name. When the target carries a name, the Compose project name is
// disambiguated as base+"-"+targetName so multiple Compose targets in one
// project do not collide (issue #103, docs/DECISIONS.md #53). A single
// unnamed target preserves the legacy project-name derivation.
func BuildFromContext(ctx targets.TargetContext) (targets.Target, error) {
	target, artifact, managed, err := resolveComposePaths(ctx)
	if err != nil {
		return nil, err
	}
	options := []Option{
		WithPullPolicy(ctx.Project.Images.Pull),
		WithHealthTimeout(ctx.Project.Health.Timeout),
		WithEnvironment(ctx.Project.Environment),
		WithServiceOverrides(ctx.Target.Services),
		WithArtifact(artifact),
	}
	projectName := projectForContext(ctx, managed)
	if projectName != "" {
		options = append(options, WithProjectName(projectName))
	}
	return New(target, options...)
}

// projectForContext derives the Compose project name for the target being
// built. When the target carries a name, the project name is disambiguated as
// base+"-"+targetName so multiple Compose targets in one project get isolated
// Compose projects. Otherwise the legacy derivation applies: the ensemble
// member name, or the project-directory basename for a managed checkout.
func projectForContext(ctx targets.TargetContext, managed bool) string {
	base := ""
	if ctx.Name != "" {
		base = ctx.Name
	} else if managed {
		projectDir, err := filepath.Abs(ctx.Dir)
		if err != nil {
			return ""
		}
		base = filepath.Base(filepath.Clean(projectDir))
	}
	if base == "" {
		return ""
	}
	return disambiguateProject(ctx.Target.Name, base)
}

// resolveComposePaths points repository-relative Compose targets at the Git
// source's managed checkout. Absolute target paths remain explicit local
// overrides for backwards compatibility.
func resolveComposePaths(ctx targets.TargetContext) (config.Target, string, bool, error) {
	configured := ctx.Target.ConfiguredPath()
	if filepath.IsAbs(configured) {
		return ctx.Target, "", false, nil
	}
	if ctx.Worktree == nil {
		return config.Target{}, "", false, fmt.Errorf("compose target: source worktree is nil")
	}
	artifact, err := composeArtifact(ctx, configured)
	if err != nil {
		return config.Target{}, "", false, err
	}
	file, err := ctx.Worktree.CheckoutPath(artifact)
	if err != nil {
		return config.Target{}, "", false, err
	}
	target := ctx.Target
	target.File = file
	target.Path = ""
	return target, artifact, true, nil
}

func composeArtifact(ctx targets.TargetContext, configured string) (string, error) {
	binding := ctx.Worktree.BindingPath()
	if ctx.Project.Source.URL != "" {
		return sources.ComposePath(binding, configured)
	}
	info, err := os.Stat(binding)
	if err != nil {
		return "", fmt.Errorf("compose target: inspect source path: %w", err)
	}
	if info.IsDir() {
		return sources.CleanRepositoryPath(configured)
	}
	root, err := ctx.Worktree.CheckoutDir()
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, binding)
	if err != nil {
		return "", fmt.Errorf("compose target: resolve source file: %w", err)
	}
	return sources.CleanRepositoryPath(relative)
}

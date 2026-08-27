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
// #44).
func lockIdentityFromConfig(dir string, target config.Target) string {
	resolved := target
	if configured := target.ConfiguredPath(); !filepath.IsAbs(configured) {
		if target.File != "" {
			resolved.File = filepath.Join(dir, target.File)
		} else {
			resolved.Path = filepath.Join(dir, target.Path)
		}
	}
	return config.TargetCompose + "\x00" + ProjectName(resolved)
}

// BuildFromContext is the compose target's TargetBuilder. It resolves the
// Compose file against the Git source's managed checkout (unless the target
// path is absolute), threads the project's pull policy, health timeout,
// environment, and per-service overrides, and derives the Compose project
// name from the ensemble member name or the project-directory basename.
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
	if ctx.Name != "" {
		options = append(options, WithProjectName(ctx.Name))
	} else if managed {
		projectDir, err := filepath.Abs(ctx.Dir)
		if err != nil {
			return nil, fmt.Errorf("resolve project directory: %w", err)
		}
		options = append(options, WithProjectName(filepath.Base(filepath.Clean(projectDir))))
	}
	return New(target, options...)
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

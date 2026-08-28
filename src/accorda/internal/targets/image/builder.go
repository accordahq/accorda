package image

import (
	"fmt"
	"path/filepath"

	"accorda/internal/config"
	"accorda/internal/targets"
)

func init() {
	targets.RegisterBuilder(config.TargetImage, targets.BuilderFunc{
		BuildFn: BuildFromContext,
		LockIdentityFunc: func(_ string, target config.Target) string {
			return config.TargetImage + "\x00" + target.Image
		},
	})
}

// BuildFromContext is the image target's TargetBuilder. The image target's
// desired state is config-driven (docs/DECISIONS.md #24), so it does not
// resolve a file from the Git checkout. The service name is the target's own
// name when set (so in a multi-target project each container is named after
// its target, not the project), otherwise the ensemble member name, otherwise
// the project-directory basename.
func BuildFromContext(ctx targets.TargetContext) (targets.Target, error) {
	serviceName := ctx.Target.Name
	if serviceName == "" {
		serviceName = ctx.Name
	}
	if serviceName == "" {
		projectDir, err := filepath.Abs(ctx.Dir)
		if err != nil {
			return nil, fmt.Errorf("resolve project directory: %w", err)
		}
		serviceName = filepath.Base(filepath.Clean(projectDir))
	}
	options := []Option{
		WithPullPolicy(ctx.Project.Images.Pull),
		WithHealthTimeout(ctx.Project.Health.Timeout),
		WithEnvironment(ctx.Project.Environment),
		WithProject(ctx.Name),
	}
	return New(ctx.Target, serviceName, options...)
}

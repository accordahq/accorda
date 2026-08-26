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
// desired state is config-driven (docs/DECISIONS.md #49), so it does not
// resolve a file from the Git checkout. The service name is the ensemble
// member name or the project-directory basename.
func BuildFromContext(ctx targets.TargetContext) (targets.Target, error) {
	serviceName := ctx.Name
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
	}
	return New(ctx.Project.Target, serviceName, options...)
}

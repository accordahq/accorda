package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	shareddocker "accorda/internal/docker"
)

// Runner runs `docker` subcommands against the managed container. It is a
// seam so tests can substitute a fake without a `docker` binary or a running
// daemon, mirroring the Compose driver's composeRunner seam
// (docs/ACCORDA.md §12, docs/DECISIONS.md #3).
type Runner interface {
	// Run executes `docker <args...>`. It returns a non-nil error when the
	// command fails, wrapping the CLI's stderr for diagnosis.
	Run(ctx context.Context, args ...string) error
}

// cliRunner is the production Runner. It shells out to the `docker` CLI,
// scoping container operations with the service name so Apply targets exactly
// the container Current enumerates via the accorda.image.service label.
type cliRunner struct {
	name  string
	image string
}

// Run implements Runner.
func (r cliRunner) Run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	// Filter the host environment to the Docker operational allowlist so the
	// `docker` CLI gets connectivity, credentials, proxies, and certificate
	// discovery without inheriting arbitrary host variables (least privilege,
	// consistent with the Compose target; docs/ACCORDA.md §18, §56,
	// docs/DECISIONS.md #21).
	cmd.Env = shareddocker.ControlledEnvironment(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

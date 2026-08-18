package compose

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// composeRunner runs `docker compose` subcommands against the project's
// Compose file. It is a seam so tests can substitute a fake without a
// `docker compose` binary or a running daemon, mirroring the dockerClient
// seam used by Current (docs/ACCORDA.md §12, docs/DECISIONS.md #3).
//
// Apply shells out to the `docker compose` CLI rather than driving the
// Docker engine SDK directly: recreating a service through the SDK would
// mean reimplementing Compose's container, network, and volume creation
// logic, which the spec explicitly frames as "the equivalent of
// `docker compose up -d`" (docs/ACCORDA.md §9). Delegating to the CLI keeps
// Accorda focused on reconciliation and inherits Compose's semantics.
type composeRunner interface {
	// Run executes `docker compose <args...>` scoped to the project's
	// Compose file and project name. It returns a non-nil error when the
	// command fails, wrapping the CLI's stderr for diagnosis.
	Run(ctx context.Context, args ...string) error
}

// cliRunner is the production composeRunner. It shells out to the `docker
// compose` CLI, scoping every invocation with -f (the Compose file) and -p
// (the project name) so Apply targets exactly the containers Current
// enumerates via the com.docker.compose.project label.
type cliRunner struct {
	file    string
	project string
}

// Run implements composeRunner.
func (r cliRunner) Run(ctx context.Context, args ...string) error {
	full := make([]string, 0, len(args)+5)
	full = append(full, "compose", "-f", r.file, "-p", r.project)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

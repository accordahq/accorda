package compose

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// dockerClient is the seam the Compose target uses to talk to the Docker
// engine. It is a subset of the Docker SDK's APIClient surface, narrowed to
// the calls the runtime-state reader needs (Ping, ContainerList). Defining
// the seam as a local interface keeps the Docker SDK dependency inside this
// adapter (core never sees it) and lets tests substitute a fake client
// without a running daemon (docs/ACCORDA.md §12, docs/DECISIONS.md #3).
//
// The real Docker client (client.Client) satisfies this interface; see the
// compile-time assertion below.
type dockerClient interface {
	Ping(ctx context.Context) (types.Ping, error)
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
}

// Compile-time check: the Docker SDK client satisfies dockerClient.
var _ dockerClient = (*client.Client)(nil)

// newDockerClient returns a real Docker engine client configured from the
// environment with automatic API version negotiation. It is used by the
// Compose target when no client is injected (production path).
func newDockerClient() (dockerClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return cli, nil
}

// composeProjectLabel is the Docker label Compose v2 sets on every container
// it manages, carrying the (normalized) project name. Filtering on it lets
// Accorda enumerate exactly the containers belonging to a Compose project
// without relying on naming conventions.
const composeProjectLabel = "com.docker.compose.project"

// composeServiceLabel is the Docker label carrying the Compose service name
// (the key in the Compose file's `services:` map). Accorda maps it back to
// the desired-state service name so runtime state aligns with the desired
// state keyed in Git.
const composeServiceLabel = "com.docker.compose.service"

// projectFilters returns the Docker filter args that select all containers
// belonging to the given Compose project, including stopped ones so that
// drift (a manually stopped service) is observable rather than hidden
// (docs/ACCORDA.md §5.3).
func projectFilters(project string) filters.Args {
	args := filters.NewArgs()
	args.Add("label", composeProjectLabel+"="+project)
	return args
}

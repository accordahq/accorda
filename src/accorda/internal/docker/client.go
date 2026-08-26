package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// Client is the seam target drivers use to talk to the Docker engine. It is
// a subset of the Docker SDK's APIClient surface, narrowed to the calls the
// runtime-state reader and image-pull policy need (Ping, ContainerList,
// ContainerInspect, ImageList, ImageInspect). Defining the seam as a local
// interface keeps the Docker SDK dependency inside this package (core never
// sees it) and lets tests substitute a fake client without a running daemon
// (docs/ACCORDA.md §12, docs/DECISIONS.md #3).
//
// The real Docker client (client.Client) satisfies this interface; see the
// compile-time assertion below.
type Client interface {
	Ping(ctx context.Context) (types.Ping, error)
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (image.InspectResponse, error)
}

// LogClient is the additional Docker capability used only by the logs
// command. Keeping it separate means runtime-state test doubles and future
// read-only clients do not need to implement an operation outside the core
// reconciliation path.
type LogClient interface {
	ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
}

// Compile-time check: the Docker SDK client satisfies Client.
var _ Client = (*client.Client)(nil)
var _ LogClient = (*client.Client)(nil)

// NewClient returns a real Docker engine client configured from the
// environment with automatic API version negotiation. It is used by target
// drivers when no client is injected (production path).
func NewClient() (Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return cli, nil
}

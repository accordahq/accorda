package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	shareddocker "accorda/internal/docker"
	"accorda/internal/targets"
)

// Logs fetches or follows logs for every container belonging to service in
// this Compose project (docs/ACCORDA.md §11). Docker's multiplexed stream is
// decoded so stdout and stderr reach the corresponding CLI writers. Scaled
// replicas are read in stable container-ID order for snapshots and
// concurrently when following so one live replica cannot block the others.
func (t *Target) Logs(ctx context.Context, service string, opts targets.LogOptions, stdout, stderr io.Writer) error {
	if t == nil {
		return errors.New("compose target: nil target")
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return errors.New("compose target: service is required")
	}
	logClient, ok := t.docker.(dockerLogClient)
	if !ok {
		return errors.New("compose target: docker client does not support container logs")
	}
	containers, err := t.docker.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: serviceFilters(t.project, service),
	})
	if err != nil {
		return fmt.Errorf("compose target: list containers for service %q: %w", service, err)
	}
	if len(containers) == 0 {
		return fmt.Errorf("compose target: service %q has no containers", service)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].ID < containers[j].ID })
	if opts.Tail == "" {
		opts.Tail = shareddocker.AllLogLines
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if !opts.Follow {
		for _, ctr := range containers {
			if err := t.streamContainerLogs(ctx, logClient, ctr.ID, opts, stdout, stderr); err != nil {
				return err
			}
		}
		return nil
	}
	return t.followContainerLogs(ctx, logClient, containers, opts, stdout, stderr)
}

// streamContainerLogs copies one container's log stream. TTY containers emit
// a raw stream; non-TTY containers use Docker's stdout/stderr framing.
func (t *Target) streamContainerLogs(ctx context.Context, client dockerLogClient, id string, opts targets.LogOptions, stdout, stderr io.Writer) error {
	inspected, err := t.docker.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("compose target: inspect container %q for logs: %w", id, err)
	}
	stream, err := client.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
		Tail:       opts.Tail,
	})
	if err != nil {
		return fmt.Errorf("compose target: read container %q logs: %w", id, err)
	}
	defer stream.Close()

	if inspected.Config != nil && inspected.Config.Tty {
		_, err = io.Copy(stdout, stream)
	} else {
		_, err = stdcopy.StdCopy(stdout, stderr, stream)
	}
	if err != nil {
		return fmt.Errorf("compose target: stream container %q logs: %w", id, err)
	}
	return nil
}

// followContainerLogs streams scaled replicas concurrently. Both locked
// writers share one mutex so stdout/stderr frame writes cannot overlap when
// callers route them to the same underlying destination.
func (t *Target) followContainerLogs(ctx context.Context, client dockerLogClient, containers []container.Summary, opts targets.LogOptions, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		errOnce  sync.Once
		firstErr error
	)
	lockedStdout := lockedWriter{mu: &mu, writer: stdout}
	lockedStderr := lockedWriter{mu: &mu, writer: stderr}
	for _, ctr := range containers {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := t.streamContainerLogs(ctx, client, id, opts, lockedStdout, lockedStderr); err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}(ctr.ID)
	}
	wg.Wait()
	return firstErr
}

type lockedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

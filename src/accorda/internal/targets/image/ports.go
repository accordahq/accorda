package image

import (
	"io"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"

	"accorda/internal/core/state"
)

// containerListOptions returns the Docker list options that select the
// container(s) the image target manages, including stopped ones so drift (a
// manually stopped container) is observable rather than hidden
// (docs/ACCORDA.md §5.3).
func containerListOptions(name string) container.ListOptions {
	args := filters.NewArgs()
	args.Add("label", containerNameLabel+"="+name)
	return container.ListOptions{All: true, Filters: args}
}

// dockerLogsOptions converts the target-agnostic LogOptions into the Docker
// SDK's LogsOptions.
func dockerLogsOptions(follow bool, tail string) container.LogsOptions {
	return container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	}
}

// parsePorts converts Docker short-form port strings ("host:container",
// "container", or "host:container/protocol") into the normalized state.Port
// model used by desired state and hashing.
func parsePorts(ports []string) []state.Port {
	out := make([]state.Port, 0, len(ports))
	for _, p := range ports {
		out = append(out, parsePort(p))
	}
	return out
}

// parsePort parses a single Docker short-form port mapping. It accepts the
// Docker forms "container", "host:container", and "ip:host:container", with
// an optional "/protocol" suffix.
func parsePort(p string) state.Port {
	port := state.Port{Protocol: "tcp"}
	rest := p
	if idx := strings.Index(rest, "/"); idx >= 0 {
		port.Protocol = strings.ToLower(rest[idx+1:])
		rest = rest[:idx]
	}
	parts := strings.Split(rest, ":")
	switch len(parts) {
	case 1:
		port.Container = parts[0]
	case 2:
		port.Host = parts[0]
		port.Container = parts[1]
	case 3:
		port.HostIP = parts[0]
		port.Host = parts[1]
		port.Container = parts[2]
	}
	return port
}

// sortedEnvKeys returns the keys of env in sorted order so the `docker run`
// argument list is deterministic (docs/DECISIONS.md #12).
func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stdcopyLogs decodes a Docker log stream into stdout/stderr, handling the
// TTY raw-stream case. It mirrors the Compose driver's log framing.
func stdcopyLogs(stdout, stderr io.Writer, stream io.ReadCloser, tty bool) error {
	defer stream.Close()
	if tty {
		_, err := io.Copy(stdout, stream)
		return err
	}
	_, err := stdcopy.StdCopy(stdout, stderr, stream)
	return err
}

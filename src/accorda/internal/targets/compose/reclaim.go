package compose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/docker/docker/api/types/container"

	shareddocker "accorda/internal/docker"
)

// dockerCli runs plain `docker` subcommands (not `docker compose`). It is used
// for reclaim and volume-migration operations that the Compose CLI does not
// expose through a project-scoped invocation: removing a stale container by
// daemon-wide name, and copying a named volume. It is a seam so tests can
// substitute a fake without a `docker` binary or a running daemon, mirroring
// the composeRunner seam (docs/ACCORDA.md §12, docs/DECISIONS.md #3).
type dockerCli interface {
	// Run executes `docker <args...>`. It returns a non-nil error when the
	// command fails, wrapping the CLI's stderr for diagnosis.
	Run(ctx context.Context, args ...string) error
}

// cliDocker is the production dockerCli. It shells out to the `docker` CLI
// with the shared operational environment so connectivity, credentials, and
// proxies are honored without inheriting arbitrary host variables.
type cliDocker struct{}

// Run implements dockerCli.
func (cliDocker) Run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = shareddocker.ControlledEnvironment(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// reclaimStaleContainers removes containers that claim an explicit
// container_name a service is about to create, but that belong to a
// different Compose project. Before removing each such container, its named
// volumes are migrated to this project's volume namespace so the recreated
// service keeps its data (docs/DECISIONS.md #54).
//
// Safety (docs/DECISIONS.md #54): a container is reclaimed ONLY when it
// carries the Accorda ownership label (accordaManagedLabel). A container
// without that label is treated as not owned by Accorda and is never
// removed — even if it collides by name. This guarantees Accorda never
// deletes a container it did not create.
func (t *Target) reclaimStaleContainers(ctx context.Context, deployFile string, services []string) error {
	if t.dockerCli == nil || t.docker == nil {
		return nil
	}
	names, err := serviceContainerNamesFromFile(deployFile)
	if err != nil {
		return fmt.Errorf("compose target: read container names for reclaim: %w", err)
	}
	// Only services Accorda is about to create matter; a service whose name
	// collides by container_name must be reclaimed before `up` runs.
	targets := make([]string, 0)
	for _, svc := range services {
		if name, ok := names[svc]; ok {
			targets = append(targets, name)
		}
	}
	if len(targets) == 0 {
		return nil
	}

	containers, err := t.docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("compose target: list containers for reclaim: %w", err)
	}
	byName := make(map[string]container.Summary, len(containers))
	for _, c := range containers {
		for _, n := range c.Names {
			byName[strings.TrimPrefix(n, "/")] = c
		}
	}

	for _, want := range targets {
		c, ok := byName[want]
		if !ok {
			continue
		}
		// A container managed by this same project already owns the name; the
		// normal `up -d` path will recreate it. Only a container from a
		// different project is a stale conflict.
		if c.Labels[composeProjectLabel] == t.project {
			continue
		}
		// Ownership gate: never remove a container Accorda did not create.
		if c.Labels[accordaManagedLabel] != "true" {
			continue
		}
		if err := t.reclaimOne(ctx, want, c); err != nil {
			return fmt.Errorf("compose target: reclaim stale %q: %w", want, err)
		}
	}
	return nil
}

// reclaimOne force-removes a single stale, Accorda-owned container after
// migrating its named volumes to the current project namespace.
func (t *Target) reclaimOne(ctx context.Context, name string, c container.Summary) error {
	inspected, err := t.docker.ContainerInspect(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", name, err)
	}
	if err := t.migrateVolumes(ctx, inspected); err != nil {
		return err
	}
	return t.dockerCli.Run(ctx, "rm", "-f", name)
}

// migrateVolumes copies this project's named volumes referenced by the stale
// container from the container's (old) volume names to the current project's
// volume namespace, preserving data across a project rename. Bind mounts and
// non-project volumes are left untouched. A failed copy aborts reclaim so
// data is never silently dropped before the container is removed.
func (t *Target) migrateVolumes(ctx context.Context, inspected container.InspectResponse) error {
	oldProject := inspected.Config.Labels[composeProjectLabel]
	if oldProject == "" {
		return nil
	}
	for _, m := range inspected.Mounts {
		if m.Type != "volume" || m.Name == "" {
			continue
		}
		base, ok := strings.CutPrefix(m.Name, oldProject+"_")
		if !ok {
			continue
		}
		targetVol := t.project + "_" + base
		// docker run --rm -v <old>:/from -v <new>:/to busybox cp -a /from/. /to/
		args := []string{
			"run", "--rm",
			"-v", m.Name + ":/from",
			"-v", targetVol + ":/to",
			"busybox:1.36", "cp", "-a", "/from/.", "/to/",
		}
		if err := t.dockerCli.Run(ctx, args...); err != nil {
			return fmt.Errorf("migrate volume %q -> %q: %w", m.Name, targetVol, err)
		}
	}
	return nil
}

// serviceContainerNamesFromFile reads a Compose file and returns each
// service's explicit container_name.
func serviceContainerNamesFromFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return serviceContainerNames(data)
}

var _ dockerCli = cliDocker{}

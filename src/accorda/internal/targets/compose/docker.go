package compose

import (
	"github.com/docker/docker/api/types/filters"

	shareddocker "accorda/internal/docker"
)

// dockerClient is the shared Docker engine client seam, re-exported from
// internal/docker. The Compose target talks to the Docker engine through the
// same narrow interface as the image target; both live in internal/docker so
// the Docker SDK dependency stays out of core (docs/ACCORDA.md §12,
// docs/DECISIONS.md #3).
type dockerClient = shareddocker.Client

// dockerLogClient is the additional Docker capability used only by the logs
// command. Keeping it separate means runtime-state test doubles and future
// read-only clients do not need to implement an operation outside the core
// reconciliation path.
type dockerLogClient = shareddocker.LogClient

// newDockerClient returns a real Docker engine client configured from the
// environment with automatic API version negotiation. It is used by the
// Compose target when no client is injected (production path).
func newDockerClient() (dockerClient, error) {
	return shareddocker.NewClient()
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

// accordaManagedLabel is the Docker label Accorda stamps on every container
// it deploys, marking it as owned by Accorda. It is the durable ownership
// proof used when reclaiming stale containers: a container that collides by
// explicit container_name with a service Accorda is about to deploy is only
// force-removed when it carries this label (so Accorda never deletes a
// container it did not create, docs/DECISIONS.md #54). The label is set via
// the rendered deploy Compose file so it travels with the container regardless
// of which Compose project Accorda later manages it under.
const accordaManagedLabel = "accorda.managed"

// accordaDeploymentLabel is the Docker label Accorda stamps on every container
// it deploys, carrying the deployment identifier (e.g. "dep_xxx") that links
// the live container back to its deployment receipt journal entry
// (docs/ACCORDA.md §7). It is informational and never participates in desired
// state, hashing, or drift comparison. Only set when the plan carries a
// DeploymentID, so direct-construction paths that assign none are unaffected.
const accordaDeploymentLabel = "accorda.deployment_id"

// projectFilters returns the Docker filter args that select all containers
// belonging to the given Compose project, including stopped ones so that
// drift (a manually stopped service) is observable rather than hidden
// (docs/ACCORDA.md §5.3).
func projectFilters(project string) filters.Args {
	args := filters.NewArgs()
	args.Add("label", composeProjectLabel+"="+project)
	return args
}

// serviceFilters narrows the project container set to one Compose service.
func serviceFilters(project, service string) filters.Args {
	args := projectFilters(project)
	args.Add("label", composeServiceLabel+"="+service)
	return args
}

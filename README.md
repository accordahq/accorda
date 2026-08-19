# Accorda OSS

Accorda OSS is the open-source GitOps reconciliation project described in `docs/ACCORDA.md`.

This repository intentionally stays focused on the OSS product and does not include hosted control-plane features. Architecture and design decisions are recorded in [`docs/DECISIONS.md`](docs/DECISIONS.md). Accorda is licensed under the Apache License, Version 2.0 (see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE)); third-party dependency licenses are listed in [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md); see [`docs/licensing.md`](docs/licensing.md) for the compliance workflow.

## Project status

This repository is being bootstrapped as a Go-based foundation for the Accorda OSS runtime. The CLI (`cmd/accorda`) implements the command surface from `docs/ACCORDA.md` §11 and §45; `accorda version`, `accorda init`, and `accorda sync` are functional, while the remaining reconciliation commands (`status`, `diff`, `plan`, `history`) are wired up and report that they are not yet implemented until the backing core packages land. The unified project format and its loader (`internal/config`) are implemented; see "Project file" below. The core abstractions from `docs/ACCORDA.md` §12 are defined: the `Target` interface (`internal/targets`), the `Source` interface (`internal/sources`), and the typed `state`, `plan`, and `health` structs (`internal/core`), with compile-time interface checks and unit tests for value semantics. The generic Git source adapter (`internal/sources/git`) is implemented: it clones, fetches, checks out, and returns HEAD commit metadata against any Git server over SSH or HTTPS, with no GitHub-specific calls; see "Git source" below. The Docker Compose target driver (`internal/targets/compose`) is implemented through its validate, runtime-state, plan, and apply phases: it parses a Compose file into Accorda's normalized service model, validates required fields, reads the runtime state of the project's containers via the Docker engine SDK, maps them back to Accorda service names and health states, computes a per-service `CHANGED`/`UNCHANGED` plan, and applies it via scoped `docker compose up -d`; see "Compose target" below.

## Quick start

```bash
cd src/accorda
go build ./cmd/accorda
./accorda version
./accorda init --env production --repo git@github.com:acme/backend.git --branch main
```

`accorda init` writes a minimal `accorda.env` project file in the current directory (override with `--dir <path>`). `accorda version` prints the build version, falling back to VCS revision info from the Go build. The CLI is built on [cobra](https://github.com/spf13/cobra); run `accorda --help` or `accorda <command> --help` for details.

## Project file

The unified Accorda project format is defined in `docs/ACCORDA.md` §8 (Docker Compose Target) and §25 (Unified Project Format) and implemented by the `internal/config` package. A project is described in an `accorda.yaml` file:

```yaml
version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
  branch: production
  path: services/api
target:
  type: compose
  file: compose.yaml
sync:
  interval: 30s
images:
  pull: changed
reconcile:
  drift: repair
  remove_orphans: true
health:
  timeout: 120s
```

`config.Load(dir)` reads and validates `accorda.yaml` from a directory; `config.Parse(data)` does the same for raw YAML bytes. The loader is target-agnostic (Compose, Kubernetes, and Helm target types are recognized), applies defaults for omitted optional fields, rejects unknown fields, and returns field-oriented errors for invalid configuration. See the package documentation in `src/accorda/internal/config` for the accepted fields and validation rules.

## Core interfaces

The core abstractions defined in `docs/ACCORDA.md` §12 are implemented so that Accorda core never depends on a specific Git host or deployment target:

- `internal/core/state` — the three states Accorda reasons about: `DesiredState` (what Git declares), `DeployedState` (what Accorda successfully deployed), and `RuntimeState` (what is actually running), plus `Service` and `RuntimeService` value types. `Service` carries the normalized Compose definition (image, command, env, ports, volumes, networks, labels, healthcheck, dependencies); each state has a `Clone` deep-copy method and a `Validate` method.
- `internal/core/plan` — the `Plan` and `Action` value types that describe the concrete actions needed to reconcile desired state with a target's current state, including the target-agnostic `DriftActions` diffing helper (create, recreate, start, stop, remove, noop) and a `Changed`/`String` summary for per-service `CHANGED`/`UNCHANGED` output.
- `internal/core/health` — the `Health` and `ServiceHealth` value types that distinguish `DEPLOYED`, `HEALTHY`, and `SYNCED` as separate outcomes, with a `Summarize` helper that derives the aggregate status from per-service results.
- `internal/core/events` — the generic event `Bus` and event type names (`deployment.detected`, `deployment.succeeded`, `state.transition`, etc.) that core publishes to; concrete delivery adapters live under `internal/notifications`.
- `internal/core/reconcile` — the `Reconciler` that drives the reconciliation lifecycle state machine (`DETECTED → FETCHING → VALIDATING → PLANNING → PULLING → DEPLOYING → VERIFYING → HEALTHY → SYNCED`) with failure paths to `FAILED` and rollback to a known previous deployment, emitting state transitions and deployment events on a `Bus`. When the runtime has drifted, it reacts according to its drift policy (`WithDriftPolicy`): `report` emits `DriftDetected`, `repair` additionally re-plans and re-applies to restore the desired runtime and emits `DriftReconciled`, and `disabled` ignores drift.
- `internal/targets` — the `Target` interface (`Validate`, `Current`, `Plan`, `Apply`, `Health`) with a compile-time `Stub` implementation guarding the interface, and the `internal/targets/compose` driver implementing Compose file load/validate.
- `internal/sources` — the `Source` interface (`Validate`, `Fetch`, `Desired`) with a compile-time `Stub` implementation guarding the interface.

## Git source

The generic Git source adapter (`internal/sources/git`, `docs/ACCORDA.md` §13) implements `sources.Source` and works against any Git server over SSH or HTTPS, including on-premises servers, with zero SaaS dependency and no GitHub-specific calls. It uses the [go-git](https://github.com/go-git/go-git) library for Git operations (clone, fetch, checkout, reading files at commits), so the system `git` CLI is not required at runtime. Auth is handled via go-git transport methods: SSH key auth, HTTPS token auth, or ambient (SSH agent / unauthenticated HTTPS).

`git.New(config.Source, opts...)` constructs a source configured from `accorda.yaml`. `Validate` checks the source configuration without cloning. `Fetch` clones the repository into a local cache directory on first use (via go-git), then fetches and checks out the configured branch on subsequent calls, returning the `Commit` (SHA, branch, authored time) that `HEAD` points to. `Desired` reads the Compose-style services file under the configured `source.path` and returns a `state.DesiredState` carrying the repository, branch, commit, and declared services.

Authentication follows §15 and is configured explicitly via `source.auth` in `accorda.yaml` (or `git.WithAuth` in code):

```yaml
source:
  type: git
  url: ssh://git@git.internal/acme/prod.git
  branch: production
  auth:
    type: ssh
    key: /etc/Accorda/git.key
```

```yaml
source:
  type: git
  url: https://git.internal/acme/infra.git
  branch: main
  auth:
    type: https
    token: ghp_personal_or_installation_token
    username: x-access-token   # optional; defaults to "oauth2"
```

- `auth.type: ssh` sets `GIT_SSH_COMMAND` to `ssh -i <key> -o IdentitiesOnly=yes` (override with `git.WithSSHCommand`). The key material is never read or logged by Accorda.
- `auth.type: https` embeds the token in the remote URL so Git's HTTPS transport uses it directly. Credentials are never placed on the command line or in logs.
- An absent `auth` section means "use the ambient Git environment" (SSH agent, Git credential helpers), which remains the default for local development.

The cache directory is configurable with `git.WithCacheDir` or derived under `git.WithBaseDir`.

Unit tests cover the services-file parser and path/URL helpers. Integration tests (build tag `integration`, requiring the `git` executable) create a local repository, clone and check it out, and assert that `Fetch` returns the correct HEAD commit info and that `Desired` returns the declared services:

```bash
cd src/accorda
go test ./internal/sources/git/ -tags integration
```

Provider integrations (`internal/providers`) and the remaining target drivers will build on these interfaces in later milestones.

## Compose target

The Docker Compose target driver (`internal/targets/compose`, `docs/ACCORDA.md §8`) implements the `targets.Target` interface. The load/validate phase is implemented: `compose.LoadFile(path)` (or `compose.Parse(data)` for raw bytes) uses the [compose-go](https://github.com/compose-spec/compose-go) loader to parse the Compose file (handling the full Compose schema: interpolation, extends, short and long forms), then normalizes each service into a `state.Service` with image, command, environment, ports, volumes, networks, labels, healthcheck, and dependencies. Required fields are validated: a service must declare an image.

The runtime-state reader is implemented: `compose.New(config.Target, opts...)` constructs a `Target` from the Accorda project's target configuration. `Target.Validate` loads the Compose file and pings the Docker engine. `Target.Current` reads the runtime state of the project's containers via the [Docker engine SDK](https://github.com/docker/docker) and returns a `state.RuntimeState` (docs/ACCORDA.md §5.3): it lists all containers carrying the `com.docker.compose.project` label matching the project name (including stopped ones, so drift is observable), inspects each for container state and health, and maps them back to Accorda service names via the `com.docker.compose.service` label. The Docker SDK is confined to the adapter through a `dockerClient` seam so core never imports it.

The plan phase is implemented: `Target.Plan` reads the runtime state and delegates the desired-vs-deployed diff to the target-agnostic `plan.DriftActions` helper, producing a per-service `CHANGED`/`UNCHANGED` plan (docs/ACCORDA.md §9) without applying anything. It also prepends pull actions according to the image pull policy (`images.pull` in `accorda.yaml`, or `compose.WithPullPolicy`): `changed` pulls only changed services' images, `missing` pulls only images not already local, `always` pulls every image, and `never` pulls nothing. The apply phase is implemented: `Target.Apply` runs the equivalent of `docker compose up -d` scoped to only the changed services (docs/ACCORDA.md §9), mapping each plan action to a `docker compose` subcommand (`up -d`, `up -d --remove-orphans`, `pull`, `stop`) and skipping noop services, with partial failures reported per service. The health phase is implemented: `Target.Health` reads the runtime state and maps each service's Docker healthcheck status to a `health.Status` (healthy, starting, unhealthy, or unknown when no healthcheck is declared), waiting up to the health timeout (`health.timeout` in `accorda.yaml`, or `compose.WithHealthTimeout`) for services to become healthy (docs/ACCORDA.md §19).

The reconciliation lifecycle state machine (`internal/core/reconcile`) orchestrates a `Source` and a `Target` through the §6 phases, emitting state transitions and deployment events on an `events.Bus`, and rolling back to the previous deployment when apply or health verification fails (§20).

The project name is derived from the Compose file's directory basename (matching the Compose v2 heuristic) and normalized to match the label; `compose.WithProjectName` overrides it when the Compose file declares a top-level `name:` or `COMPOSE_PROJECT_NAME` is set. Tests inject a fake Docker client so no running daemon is required.

```bash
cd src/accorda
go test ./internal/targets/compose/
```

## Commands

The CLI implements the minimum command set from `docs/ACCORDA.md` §79 Step 6 plus the wider §11 surface:

| Command    | Status                | Description                                              |
| ---------- | --------------------- | -------------------------------------------------------- |
| `init`     | implemented           | create an Accorda project/target                         |
| `version`  | implemented           | print the Accorda version                                |
| `status`   | not yet implemented   | show environment, repo, branch, Git HEAD, deployed SHA... |
| `diff`     | not yet implemented   | show deployed vs desired changes                         |
| `plan`     | not yet implemented   | show intended actions without deploying                  |
| `sync`     | implemented           | run reconciliation                                       |
| `history`  | not yet implemented   | show deployment history                                  |
| `inspect`  | not yet implemented   | show details for a specific deployment                   |
| `logs`     | not yet implemented   | show logs for a deployment or service                    |
| `doctor`   | not yet implemented   | check the local Accorda installation and configuration   |

## Repository layout

```text
.
├── docs/
│   └── ACCORDA.md
├── src/
│   └── accorda/
│       ├── cmd/
│       │   └── accorda/
│       │       └── main.go
│       ├── internal/
│       │   ├── config/
│       │   ├── core/
│       │   │   ├── state/
│       │   │   ├── plan/
│       │   │   ├── reconcile/
│       │   │   ├── health/
│       │   │   ├── history/
│       │   │   └── events/
│       │   ├── sources/
│       │   │   └── git/
│       │   ├── providers/
│       │   ├── targets/
│       │   ├── secrets/
│       │   └── notifications/
│       ├── go.mod
│       └── README.md
├── AGENTS.md
├── README.md
├── LICENSE
└── .github/
```

Each package under `internal/` contains a `doc.go` describing its
responsibility, matching the core and adapter boundaries defined in
`docs/ACCORDA.md`. `internal/config` implements the `accorda.yaml` loader and
validator. The core interface packages (`internal/core/state`, `plan`, and
`health`, and `internal/targets` and `internal/sources`) define the typed
abstractions from §12 with compile-time interface checks and unit tests. The
remaining adapter and core packages hold only their package documentation
until their backing implementations land. `internal/sources/git` implements
the generic Git source adapter (clone, fetch, checkout, commit metadata)
described in `docs/ACCORDA.md` §13.

## Verification

```bash
cd src/accorda
go test ./...
go build ./...
```

## Integration & end-to-end tests

Beyond the hermetic unit tests, the repository ships an integration and end-to-end suite (build tag `integration`) that exercises Accorda against real external dependencies — a Git repository, a Docker daemon, and the Docker Compose CLI — per the testing strategy in `docs/ACCORDA.md` §55. Each test skips gracefully when its prerequisite is unavailable, so the default `go test ./...` run stays hermetic.

- `internal/sources/git` — clones, fetches, checks out, and reads desired state from a real local Git repository.
- `internal/targets/compose` — validates, plans, applies, reads runtime state, and verifies health against a real Docker daemon and `docker compose`.
- `cmd/accorda` — drives the full lifecycle end-to-end: a Git commit declares the desired state, `accorda sync` detects it, plans, deploys, verifies health, and reports `SYNCED`.

Shared fixtures and prerequisite checks live in `internal/testutil` so the three suites do not duplicate repository setup or skip logic.

```bash
cd src/accorda
go test -tags integration ./internal/sources/git/ ./internal/targets/compose/ ./cmd/accorda/
```

## Notes

- `docs/ACCORDA.md` is the source of truth and must not be modified.
- This repository is OSS-only and keeps the Go starter focused on the local reconciliation workflow.
- The Go starter app is a minimal foundation for the upcoming Accorda reconciliation workflow.

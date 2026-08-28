# Accorda OSS

Accorda OSS is the open-source GitOps reconciliation project described in `docs/ACCORDA.md`.

This repository intentionally stays focused on the OSS product and does not include hosted control-plane features. Architecture and design decisions are recorded in [`docs/DECISIONS.md`](docs/DECISIONS.md). Accorda is licensed under the Apache License, Version 2.0 (see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE)); third-party dependency licenses are listed in [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md); see [`docs/licensing.md`](docs/licensing.md) for the compliance workflow.

Operator documentation:

- [Installation](docs/INSTALLATION.md) — prerequisites and the status of installation methods.
- [Usage](docs/USAGE.md) — initialize, plan, reconcile, and operate a Compose project.

## Project status

This repository is being bootstrapped as a Go-based foundation for the Accorda OSS runtime. The CLI (`cmd/accorda`) implements the command surface from `docs/ACCORDA.md` §11 and §45; `accorda version`, `accorda init`, `accorda sync`, `accorda status`, `accorda diff`, `accorda plan`, `accorda history`, `accorda inspect`, `accorda logs`, and `accorda doctor` are functional. The unified project format and its loader (`internal/config`) are implemented; see "Project file" below. The core abstractions from `docs/ACCORDA.md` §12 are defined: the `Target` interface (`internal/targets`), the `Source` interface (`internal/sources`), and the typed `state`, `plan`, and `health` structs (`internal/core`), with compile-time interface checks and unit tests for value semantics. The generic Git source adapter (`internal/sources/git`) is implemented: it clones, fetches, checks out, and returns HEAD commit metadata against any Git server over SSH or HTTPS, with no GitHub-specific calls; see "Git source" below. The Docker Compose target driver (`internal/targets/compose`) is implemented through its reconciliation and logs operations: it parses a Compose file into Accorda's normalized service model, validates required fields, reads runtime state through the Docker engine SDK, computes and applies deployment plans, verifies health, and fetches or follows service logs; see "Compose target" below. Deployment history is recorded as an append-only local journal of deployment receipts (`internal/core/history`), capturing each cycle's commit, result (`healthy`/`failed`), and changed services, stored under `$XDG_STATE_HOME/accorda/receipts/<project>.jsonl` by `accorda sync`.

`accorda sync` runs one reconciliation pass for interactive use and automation, printing lifecycle progress while it fetches, validates, plans, pulls, deploys, and verifies the target before reporting the terminal outcome. `accorda sync --watch` runs the same reconciler continuously: one immediate pass followed by Git polling at `sync.interval`. An unchanged branch HEAD does not plan or mutate the target, but the cycle still evaluates workload health, checks runtime drift, and applies the configured drift policy. Failed cycles are reported and polling continues; SIGINT or SIGTERM cancels an in-flight source/target operation and shuts the loop down cleanly. There is no separate daemon binary—the watch command is the daemon process and should be supervised by systemd, Docker, or the platform's service manager in production.

The `accorda status` command (`cmd/accorda/status.go`, `docs/ACCORDA.md` §11) prints the project's posture: environment, repository, branch, Git HEAD, deployed commit, sync/runtime status, last deploy time, and a per-service table of state/health/image. It is read-only: it fetches the Git source for the HEAD commit, reads the last healthy deployment receipt from the journal, and reads the target's runtime state, without mutating either.

The `accorda diff` command (`cmd/accorda/diff.go`, `docs/ACCORDA.md` §11) shows the per-field deployed vs desired changes. The deployed side is the last healthy deployment, re-read from Git at the deployed commit; the desired side is the current Git HEAD. Environment keys and change state are shown, but their values are always redacted. It is read-only and works from Git plus the deployment history, so it needs no running Docker daemon.

The `accorda plan` command (`cmd/accorda/plan.go`, `docs/ACCORDA.md` §11) shows the intended per-service actions Accorda would take to reconcile the desired state with the target's current state, without deploying. It computes the same plan a `sync` would apply (including image pulls per the project's pull policy) and prints a `CHANGED`/`UNCHANGED` summary.

The `accorda history` command (`cmd/accorda/history.go`, `docs/ACCORDA.md` §11) prints the deployment journal as a table of time, commit, result (✓ healthy / ✗ failed / ↺ rolled_back), and the services that changed, newest first. It is read-only and reads the local receipt journal, so it needs no running Docker daemon and no Git fetch.

The `accorda inspect` command (`cmd/accorda/inspect.go`, `docs/ACCORDA.md` §11) shows the per-service detail for a specific deployment: the previous and deployed image digests, whether the service was recreated, and the health result. With no commit argument it inspects the most recent deployment. It is read-only and reads the local receipt journal, so it needs no running Docker daemon and no Git fetch.

The `accorda logs SERVICE` command (`cmd/accorda/logs.go`, `docs/ACCORDA.md` §11) fetches logs for every container of a Compose service through the target driver. `--tail N` limits the initial output and `--follow`/`-f` streams new output. Docker's stdout/stderr streams are decoded and written to the corresponding terminal streams.

The `accorda doctor` command (`cmd/accorda/doctor.go`, `docs/ACCORDA.md` §11) checks the project configuration, Git source settings, Compose target file when the managed checkout already exists, Docker engine connectivity, and Docker Compose CLI availability. It prints each check result and exits nonzero when a check fails. The command is read-only: it does not fetch Git or change the deployment target; validation of a not-yet-fetched Compose file is deferred to `plan` or `sync` after the first fetch.

## Quick start

```bash
cd src/accorda
go build -o accorda ./cmd/accorda
./accorda version
./accorda init --env production --repo git@github.com:acme/backend.git --branch main
./accorda sync --watch
```

`accorda init` writes a minimal `accorda.yaml` project file in the current directory (override with `--dir <path>`) using the unified project format (§25), so `accorda sync` can load it directly. The `--file` value is recorded as both the Git source's Compose path and the Compose target file. On the first `plan` or `sync`, Accorda clones the repository into its private cache and runs Compose directly from that managed checkout; the operator does not clone the application repository beside `accorda.yaml`. New project files use mode `0600`, and `init` refuses to overwrite an existing file. Run `accorda init --help` for the available flags (source auth type, Compose file path). `accorda version` prints the build version, falling back to VCS revision info from the Go build. The CLI is built on [cobra](https://github.com/spf13/cobra); run `accorda --help` or `accorda <command> --help` for details.

## Building

The Go module lives under `src/accorda`. From that directory, build the `accorda`
binary into the current directory:

```bash
cd src/accorda
go build -o accorda ./cmd/accorda
```

The resulting `./accorda` binary is self-contained and can be run from anywhere
(see the Quick start above). To install it on your `PATH` instead, use `go
install`:

```bash
cd src/accorda
go install ./cmd/accorda
```

`go install` places the binary in `$(go env GOPATH)/bin`. The build embeds VCS
revision info, so `accorda version` reports the commit it was built from.


## System requirements

`accorda` is a single self-contained Go binary with a small runtime footprint.
The numbers below are observed on a local development machine running
`accorda sync --watch` against a Compose target while idle between
reconciliation cycles; they are indicative, not a guarantee, and scale with the
number of projects, targets, and services being reconciled.

| Resource     | Idle (observed) | Notes                                                                                                                                 |
| ------------ | --------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| CPU          | ~0%             | The watch loop sleeps between `sync.interval` polls; CPU rises only during a reconciliation pass (fetch, plan, pull, deploy, verify). |
| Memory (RSS) | ~15 MB          | Go runtime baseline; grows with the number of projects/targets and the size of the fetched repository.                                |
| Threads      | ~18             | Go runtime worker threads; all sleeping while idle.                                                                                   |

Runtime prerequisites (see [Installation](docs/INSTALLATION.md)):

- a reachable Docker Engine and Docker Compose v2 (`docker compose`) for the Compose target;
- network access to the configured Git repository and container registries;
- Git credentials for the repository (SSH key or HTTPS token).

The system `git` executable is not required at runtime — Accorda uses its
built-in Git adapter. There is no separate daemon binary: `accorda sync
--watch` is the daemon process and should be supervised by systemd, Docker, or
the platform's service manager in production.

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

`config.Load(dir)` reads and validates `accorda.yaml` from a directory; `config.Parse(data)` does the same for raw YAML bytes. A file containing `source.auth.token` or an inline URL password must have mode `0600` or stricter. The loader is target-agnostic (Compose, image, Kubernetes, and Helm target types are recognized), applies defaults for omitted optional fields, rejects unknown fields, and returns field-oriented errors for invalid configuration. See the package documentation in `src/accorda/internal/config` for the accepted fields and validation rules.

For Compose, relative `target.file` and `target.path` values are repository-relative and resolve inside Accorda's managed Git checkout. When `source.path` names a directory, the target filename is appended (for example `services/api` plus `compose.yaml`); when `source.path` names a YAML file, that exact file is used. An absolute target path remains an explicit local-file override for compatibility.

For a raw single-image target (`target.type: image`), the desired state is declared directly in `accorda.yaml` — no Compose file is parsed. The image, env, and ports fields build a single-service desired state while the source revision still anchors receipts and history to the Git commit (`docs/DECISIONS.md` #24, #52):

```yaml
target:
  type: image
  image: registry.example.com/edge-agent:1.2.3
  env:
    REGION: eu-west-1
  ports:
    - "8080:8080"
```

### Multi-project (ensemble)

A single agent can manage several workloads concurrently by listing named
projects under a top-level `projects:` key
(docs/ACCORDA.md §49, Phase 5 — Multi-Project / Multi-Target Compose). The
schema version, sync cadence, and policy defaults live at the document root
and are shared by every member; members may override the image pull, drift,
and health defaults (docs/DECISIONS.md #43):

```yaml
version: 1
sync:
  interval: 30s
images:
  pull: changed
reconcile:
  drift: repair
health:
  timeout: 120s
projects:
  - name: api
    environment: production
    source:
      type: git
      url: git@github.com:acme/api.git
      branch: main
    target:
      type: compose
      file: compose.yaml
  - name: worker
    environment: production
    source:
      type: git
      url: git@github.com:acme/worker.git
      branch: main
    target:
      type: compose
      file: compose.yaml
```

The presence of `projects:` selects the multi-project shape; its absence
selects the single-project shape shown above — the two cannot be mixed. Each
project is reconciled independently with its own source, target, receipt
journal, and target-scoped lock, so `accorda sync` reconciles all members
concurrently and a failure in one workload does not block the others. Every
command (`status`, `plan`, `diff`, `history`, `inspect`, `logs`, `doctor`)
iterates over all members and prefixes its output with the project name.
Project names must be unique (compared case-insensitively, matching Compose
project-name normalization) so `--remove-orphans` cannot remove a sibling
project's containers; each member's Compose project name is set to its
`name`, and its git checkout is namespaced by name so two members sharing a
repository URL get isolated worktrees.

The `version` and `sync` settings are global and not overridable per project —
one agent runs on one schema and one polling cadence — while `images`,
`reconcile`, and `health` act as defaults that any member may override.

### Multiple targets per project

A single project can reconcile several deployment targets from its one source
by declaring a `targets:` list (issue #103, docs/DECISIONS.md #53). Each target
locates and parses its own artifact from the same Git revision, and keeps its
own receipt journal and deployment lock, so two Compose files (or future
Compose + Kubernetes combinations) deploy together from one fetch. The legacy
single `target:` scalar remains valid and is equivalent to a one-element
`targets:` list:

```yaml
version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
  branch: main
targets:
  - type: compose
    file: docker-compose.yml
  - type: compose
    file: qa/docker-compose.yml
```

One `accorda sync` cycle fetches one source revision and reconciles every
declared target. Targets within a project run sequentially (they share the
source's managed checkout, so concurrent mutation is avoided); independent
projects still run concurrently. Target identities must be unique within a
project so their journals and locks do not collide. Every command (`diff`,
`plan`, `status`, `history`, `inspect`, `logs`, `doctor`) addresses each target
and prefixes its output with the target identity when a project has more than
one. Rollback operates per target, so a failed target restores independently
without affecting its siblings.

## Core interfaces

The core abstractions defined in `docs/ACCORDA.md` §12 are implemented so that Accorda core never depends on a specific Git host or deployment target:

- `internal/core/state` — the three states Accorda reasons about: `DesiredState` (what Git declares), `DeployedState` (what Accorda successfully deployed), and `RuntimeState` (what is actually running), plus `Service` and `RuntimeService` value types. `Service` carries the normalized Compose definition (image, command, env, ports, volumes, networks, labels, healthcheck, dependencies); each state has a `Clone` deep-copy method and a `Validate` method.
- `internal/core/plan` — the `Plan` and `Action` value types that describe the concrete actions needed to reconcile desired state with a target's current state, including the target-agnostic `DriftActions` diffing helper (create, recreate, start, stop, remove, noop) and a `Changed`/`String` summary for per-service `CHANGED`/`UNCHANGED` output.
- `internal/core/health` — the `Health` and `ServiceHealth` value types that distinguish `DEPLOYED`, `HEALTHY`, and `SYNCED` as separate outcomes, with a `Summarize` helper that derives the aggregate status from per-service results.
- `internal/core/events` — the generic event `Bus` and event type names (`deployment.detected`, `deployment.succeeded`, `state.transition`, etc.) that core publishes to; concrete delivery adapters live under `internal/notifications`.
- `internal/notifications/webhook` — the generic outbound webhook notification target (`docs/ACCORDA.md` §21): it subscribes to the event `Bus` and POSTs each event as a JSON payload to a configurable URL, with bounded retry on transient failures (dispatched asynchronously so it never blocks reconciliation), redirect-rejection hardening, an optional shared-secret HMAC signature header, and redaction of secret environment values before serialization. `accorda sync` subscribes it when `notifications.webhook: true` and `notifications.webhooks.url` are set in `accorda.yaml`.
- `internal/core/reconcile` — the `Reconciler` that drives the reconciliation lifecycle state machine (`DETECTED → FETCHING → VALIDATING → PLANNING → PULLING → DEPLOYING → VERIFYING → HEALTHY → SYNCED`) with failure paths to `FAILED` and rollback to a known previous deployment, emitting state transitions and deployment events on a `Bus`. On a deploy or health-verification failure with a known previous deployment, it restores the previous state (for a Compose target this re-materializes the services file before re-applying) and records an `OutcomeRolledBack` receipt; with no prior healthy deployment in history it leaves the failure standing (the "where safely possible" qualifier in `docs/ACCORDA.md` §20). When the runtime has drifted, it reacts according to its drift policy (`WithDriftPolicy`): `report` emits `DriftDetected`, `repair` additionally re-plans and re-applies to restore the desired runtime and emits `DriftReconciled`, and `disabled` ignores drift.
- `internal/targets` — the `Target` interface (`Desired`, `Validate`, `Current`, `Plan`, `Apply`, `Health`) with a compile-time `Stub` implementation guarding the interface and the `internal/targets/compose` and `internal/targets/image` drivers. Each target owns artifact discovery and normalization. The shared Docker operations layer lives in `internal/docker` and is consumed by both Docker targets so the Docker SDK dependency stays confined to the adapters.
- `internal/sources` — the `Source` interface (`Validate`, `Fetch`, `Revision`) with a compile-time `Stub` implementation guarding the interface. A revision carries commit metadata and a releasable filesystem view; sources do not parse target declarations.

## Git source

The generic Git source adapter (`internal/sources/git`, `docs/ACCORDA.md` §13) implements `sources.Source` and works against any Git server over SSH or HTTPS, including on-premises servers, with zero SaaS dependency and no GitHub-specific calls. It uses the [go-git](https://github.com/go-git/go-git) library for Git operations (clone, fetch, checkout, reading files at commits), so the system `git` CLI is not required at runtime. Auth is handled via go-git transport methods: SSH key auth, HTTPS token auth, or ambient (SSH agent / unauthenticated HTTPS).

The adapter has two modes, selected by which field is configured:

- **Remote mode** (`source.url`) clones or fetches the repository into a private per-user cache keyed by the credential-free repository identity and operator project directory. This is the default.
- **In-place mode** (`source.path`, no `url`) binds directly to a user-owned local git worktree without cloning. `Fetch` only reads its current `HEAD`; the adapter never mutates the worktree. This yields a real, stable `HEAD` SHA at zero cost, so the reconcile loop's commit-anchored fast path, receipts, and `diff`/`plan`/`history`/`inspect` work unchanged. Historical desired state, including rollback baselines, is reconstructed from the commit's tree without rewriting the operator's checkout. Applying an automatic in-place rollback is unsupported because `Materialize` would have to rewrite that checkout.

`git.New(config.Source, opts...)` constructs a source configured from `accorda.yaml`. `Validate` checks the source configuration without cloning, including reading and parsing an explicitly configured SSH key. In remote mode `Fetch` clones into the private cache on first use, verifies a cached repository's `origin` before every reuse, then fetches and checks out the configured branch. The project namespace keeps production and staging checkouts independent even when they use different branches of the same repository. In in-place mode `Fetch` reads `HEAD` from the bound worktree. `Revision` exposes commit metadata plus a safe real-filesystem view: HEAD uses the active worktree, while historical commits use a private temporary materialization that is released after the target loads it. Compose discovers and parses its configured file from that view; the Git adapter remains target-agnostic.

Authentication follows §15 and is configured explicitly via `source.auth` in `accorda.yaml` (or `git.WithAuth` in code); in-place mode never uses auth:

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

In-place mode points at a local worktree:

```yaml
source:
  type: git
  path: ~/Work/Docker        # a git worktree; reconciled in place, no clone
```

- `auth.type: ssh` reads and parses the configured key for go-git's SSH transport. An unreadable, invalid, encrypted, or unsupported key fails validation rather than falling back to the ambient SSH agent. Key material is never logged.
- `auth.type: https` supplies the token directly to go-git's HTTP transport without changing the repository URL. Credentials are never placed on the command line or in logs.
- An absent `auth` section means "use the ambient Git environment" (SSH agent, Git credential helpers), which remains the default for local development.

The cache directory is configurable with `git.WithCacheDir` or derived under
`git.WithBaseDir`; the default is a private per-user cache. Git operations fail
closed when the platform cannot discover either a user cache or user config
directory—there is no shared temporary-directory fallback.

Unit tests cover the services-file parser and path/URL helpers. Integration tests (build tag `integration`, requiring the `git` executable) create a local repository, clone and check it out, and assert that `Fetch` returns the correct HEAD commit info and that `Desired` returns the declared services:

```bash
cd src/accorda
go test ./internal/sources/git/ -tags integration
```

Provider integrations (`internal/providers`) and the remaining target drivers will build on these interfaces in later milestones.

## Secret handling

Plaintext secrets stay in memory whenever possible. When a target renderer
requires a file path, `internal/secrets.WithPlaintextFile` materializes the
plaintext only under the memory-backed `/run/accorda` runtime directory, using
a private `0700` directory and a `0600` file. The file exists only while the
consumer callback runs and is removed immediately afterward, including when
the callback returns an error or panics. Cleanup failures are surfaced to the
caller rather than silently leaving plaintext behind; if cleanup fails during
panic unwinding, the re-panic preserves both the callback panic and cleanup
error. SOPS decryption remains separate from this lifecycle policy and is
delegated to SOPS rather than implemented by Accorda.

## Compose target

For repository-relative targets, CLI wiring points the Compose driver at the Git source's project-isolated managed checkout. Compose therefore receives the complete fetched repository context—including `extends`, relative build/bind contexts, and `env_file` paths—without a user-managed clone. Planning and `docker compose` use the same controlled Docker-operational environment and disable implicit `.env` interpolation, so declaration defaults such as `${PORT:-8080}` cannot be silently overridden by arbitrary host variables. `env_file` and `label_file` declarations participate in service identity; changes to files tracked in Git are represented by SHA-256 digests without retaining their values, while untracked local secret files remain deployment-time references.

The Docker Compose target driver (`internal/targets/compose`, `docs/ACCORDA.md` §8) implements the `targets.Target` interface. The load/validate phase is implemented: `compose.LoadFile(path)` (or `compose.Parse(data)` for raw bytes) uses the [compose-go](https://github.com/compose-spec/compose-go) loader to parse the Compose file with its real project-directory context (handling interpolation, extends, and short and long forms), then normalizes each service into a `state.Service` with image, command, environment, external file references, ports, volumes, networks, labels, healthcheck, and dependencies. Required fields are validated: a service must declare an image.

The runtime-state reader is implemented: `compose.New(config.Target, opts...)` constructs a `Target` from the Accorda project's target configuration. `Target.Validate` loads the Compose file and pings the Docker engine. `Target.Current` reads the runtime state of the project's containers via the [Docker engine SDK](https://github.com/docker/docker) and returns a `state.RuntimeState` (docs/ACCORDA.md §5.3): it lists all containers carrying the `com.docker.compose.project` label matching the project name (including stopped ones, so drift is observable), inspects each for container state and health, and maps them back to Accorda service names via the `com.docker.compose.service` label. The Docker SDK is confined to the adapter through a `dockerClient` seam so core never imports it.

The optional logs capability is separate from the five-method reconciliation interface. `Target.Logs` selects containers by Compose project and service labels, fetches snapshot logs or follows live output through the Docker engine API, and decodes Docker's multiplexed stdout/stderr framing. Scaled replicas are read deterministically for snapshots and concurrently while following.

The plan phase is implemented: `Target.Plan` reads the runtime state and delegates the desired-vs-deployed diff to the target-agnostic `plan.DriftActions` helper, producing a per-service `CHANGED`/`UNCHANGED` plan (docs/ACCORDA.md §9) without applying anything. It also prepends pull actions according to the image pull policy (`images.pull` in `accorda.yaml`, or `compose.WithPullPolicy`): `changed` pulls only changed services' images, `missing` pulls only images not already local, `always` pulls every image, and `never` pulls nothing. The apply phase is implemented: `Target.Apply` runs the equivalent of `docker compose up -d` scoped to only the changed services (docs/ACCORDA.md §9), mapping each plan action to a `docker compose` subcommand (`up -d`, `up -d --remove-orphans`, `pull`, `stop`) and skipping noop services, with partial failures reported per service. Service names are validated before planning/applying and passed after an end-of-options delimiter so Git-controlled names cannot become Compose flags. The health phase is implemented: `Target.Health` reads the runtime state and maps each service's Docker healthcheck status to a `health.Status` (healthy, starting, unhealthy, or unknown when no healthcheck is declared), waiting up to the health timeout (`health.timeout` in `accorda.yaml`, or `compose.WithHealthTimeout`) for services to become healthy (docs/ACCORDA.md §19).

The reconciliation lifecycle state machine (`internal/core/reconcile`) orchestrates a `Source` and a `Target` through the §6 phases, emitting state transitions and deployment events on an `events.Bus`, and rolling back to the previous deployment when apply or health verification fails (§20). Reconciliation is hardened for §47: each changed deployment is checkpointed before target mutation, unmatched checkpoints resume idempotently after restart, target-scoped locks prevent concurrent `sync` processes from racing, and a commit that lands during deployment is reconciled before the lock is released. Partial Compose apply failures report both completed actions and the failed service/action.

The project name is derived from the Compose file's directory basename (matching the Compose v2 heuristic) and normalized to match the label; `compose.WithProjectName` overrides it when the Compose file declares a top-level `name:` or `COMPOSE_PROJECT_NAME` is set. Tests inject a fake Docker client so no running daemon is required.

```bash
cd src/accorda
go test ./internal/targets/compose/
```

## Commands

The CLI implements the minimum command set from `docs/ACCORDA.md` §79 Step 6 plus the wider §11 surface:

| Command   | Status      | Description                                               |
| --------- | ----------- | --------------------------------------------------------- |
| `init`    | implemented | create an Accorda project/target                          |
| `version` | implemented | print the Accorda version                                 |
| `status`  | implemented | show environment, repo, branch, Git HEAD, deployed SHA... |
| `diff`    | implemented | show deployed vs desired changes                          |
| `plan`    | implemented | show intended actions without deploying                   |
| `sync`    | implemented | run once, or continuously with `--watch`                  |
| `history` | implemented | show deployment history                                   |
| `inspect` | implemented | show details for a specific deployment                    |
| `logs`    | implemented | fetch or follow logs for a service                        |
| `doctor`  | implemented | check the local Accorda installation and configuration    |

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
│       │   ├── format/
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
- `internal/targets/image` — validates, plans, applies, reads runtime state, verifies health, and streams logs for a raw single-image target against a real Docker daemon (`docker run`).
- `cmd/accorda` — drives the full lifecycle end-to-end: a Git commit declares the desired state, `accorda sync` detects it, plans, deploys, verifies health, and reports `SYNCED`. Includes a raw-image target E2E covering the config-driven desired-state path.

Shared fixtures and prerequisite checks live in `internal/testutil` so the three suites do not duplicate repository setup or skip logic.

For full validation — gofmt check, build, unit suite, and the integration/E2E suite — run the single `scripts/test.sh` command instead of assembling the long `go test` invocations by hand:

```bash
scripts/test.sh
```

The script also fails when aggregate statement coverage is below 85%. Set
`ACCORDA_MIN_COVERAGE` to exercise a different threshold locally; CI uses the
repository default.

You can also run the integration suite directly from the module directory:

```bash
cd src/accorda
go test -tags integration ./internal/sources/git/ ./internal/targets/compose/ ./internal/targets/image/ ./cmd/accorda/
```

## Notes

- `docs/ACCORDA.md` is the source of truth and must not be modified.
- This repository is OSS-only and keeps the Go starter focused on the local reconciliation workflow.
- The Go starter app is a minimal foundation for the upcoming Accorda reconciliation workflow.

# Accorda OSS

Accorda OSS is the open-source GitOps reconciliation project described in `docs/ACCORDA.md`.

This repository intentionally stays focused on the OSS product and does not include hosted control-plane features.

## Project status

This repository is being bootstrapped as a Go-based foundation for the Accorda OSS runtime. The CLI (`cmd/accorda`) implements the command surface from `docs/ACCORDA.md` §11 and §45; `accorda version` and `accorda init` are functional, while the reconciliation commands (`status`, `diff`, `plan`, `sync`, `history`) are wired up and report that they are not yet implemented until the backing core packages land. The unified project format and its loader (`internal/config`) are implemented; see "Project file" below. The core abstractions from `docs/ACCORDA.md` §12 are defined: the `Target` interface (`internal/targets`), the `Source` interface (`internal/sources`), and the typed `state`, `plan`, and `health` structs (`internal/core`), with compile-time interface checks and unit tests for value semantics. The generic Git source adapter (`internal/sources/git`) is implemented: it clones, fetches, checks out, and returns HEAD commit metadata against any Git server over SSH or HTTPS, with no GitHub-specific calls; see "Git source" below.

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

- `internal/core/state` — the three states Accorda reasons about: `DesiredState` (what Git declares), `DeployedState` (what Accorda successfully deployed), and `RuntimeState` (what is actually running), plus `Service` and `RuntimeService` value types. Each state has a `Clone` deep-copy method and a `Validate` method.
- `internal/core/plan` — the `Plan` and `Action` value types that describe the concrete actions needed to reconcile desired state with a target's current state, including the target-agnostic `DriftActions` diffing helper (create, recreate, start, stop, remove, noop).
- `internal/core/health` — the `Health` and `ServiceHealth` value types that distinguish `DEPLOYED`, `HEALTHY`, and `SYNCED` as separate outcomes, with a `Summarize` helper that derives the aggregate status from per-service results.
- `internal/targets` — the `Target` interface (`Validate`, `Current`, `Plan`, `Apply`, `Health`) with a compile-time `Stub` implementation guarding the interface.
- `internal/sources` — the `Source` interface (`Validate`, `Fetch`, `Desired`) with a compile-time `Stub` implementation guarding the interface.

## Git source

The generic Git source adapter (`internal/sources/git`, `docs/ACCORDA.md` §13) implements `sources.Source` and works against any Git server over SSH or HTTPS, including on-premises servers, with zero SaaS dependency and no GitHub-specific calls. It shells out to the system `git` command, which handles SSH agent and HTTPS credential transport via the user's environment.

`git.New(config.Source, opts...)` constructs a source configured from `accorda.yaml`. `Validate` checks the configuration and that the `git` CLI is available without cloning. `Fetch` clones the repository into a local cache directory on first use, then fetches and checks out the configured branch on subsequent calls, returning the `Commit` (SHA, branch, authored time) that `HEAD` points to. `Desired` reads the Compose-style services file under the configured `source.path` and returns a `state.DesiredState` carrying the repository, branch, commit, and declared services.

Authentication follows §15: SSH keys via `git.WithSSHCommand("ssh -i /etc/Accorda/git.key -o IdentitiesOnly=yes")` (sets `GIT_SSH_COMMAND`) and HTTPS credentials/tokens via the user's Git environment. The cache directory is configurable with `git.WithCacheDir` or derived under `git.WithBaseDir`.

Unit tests cover the services-file parser and path/URL helpers. Integration tests (build tag `integration`, requiring the `git` executable) create a local repository, clone and check it out, and assert that `Fetch` returns the correct HEAD commit info and that `Desired` returns the declared services:

```bash
cd src/accorda
go test ./internal/sources/git/ -tags integration
```

Provider integrations (`internal/providers`) and the remaining target drivers will build on these interfaces in later milestones.

## Commands

The CLI implements the minimum command set from `docs/ACCORDA.md` §79 Step 6 plus the wider §11 surface:

| Command    | Status                | Description                                              |
| ---------- | --------------------- | -------------------------------------------------------- |
| `init`     | implemented           | create an Accorda project/target                         |
| `version`  | implemented           | print the Accorda version                                |
| `status`   | not yet implemented   | show environment, repo, branch, Git HEAD, deployed SHA... |
| `diff`     | not yet implemented   | show deployed vs desired changes                         |
| `plan`     | not yet implemented   | show intended actions without deploying                  |
| `sync`     | not yet implemented   | run reconciliation                                       |
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

## Notes

- `docs/ACCORDA.md` is the source of truth and must not be modified.
- This repository is OSS-only and keeps the Go starter focused on the local reconciliation workflow.
- The Go starter app is a minimal foundation for the upcoming Accorda reconciliation workflow.

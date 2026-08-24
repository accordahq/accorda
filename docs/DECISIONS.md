# Accorda OSS — Architecture Decisions

This file records the architecture and design decisions baked into the
Accorda OSS codebase. It is the place agents and contributors record durable
decisions and their rationale, so future work stays aligned.

The authoritative product specification is [`ACCORDA.md`](ACCORDA.md). This
file describes decisions **already made in the code**, with rationale and
consequences. It does not replace the spec.

When adding a decision:
1. Append a new numbered entry under [Decision log](#decision-log).
2. Keep it short: one paragraph of context, the decision, and the
   consequence. Cite the files it lives in.
3. Do not edit or contradict `ACCORDA.md`. If a decision diverges from an
   earlier assumption, state the divergence explicitly.

---

## Decision log

### 1. Module layout and minimal dependencies

**Context.** Accorda OSS is intentionally scoped to a single Go module under
`src/accorda/`. The product spec (`ACCORDA.md` §3, §4, §45) demands a
provider-independent, SaaS-free OSS runtime: Core must not fundamentally know
about GitHub, Docker Compose, AWS, or any particular provider (§12), and the
OSS agent must remain functional indefinitely without Accorda Cloud (§4).
The spec is silent on *how* adapters are implemented (CLI vs library).

**Decision.** Keep application code under `src/accorda/`; keep root-level
documentation (`README.md`, `docs/`, `AGENTS.md`) outside the implementation
tree. Dependencies: `github.com/spf13/cobra` (CLI), `gopkg.in/yaml.v3`
(YAML), `github.com/go-git/go-git/v6` (Git operations),
`github.com/compose-spec/compose-go/v2` (Compose parsing), and
`github.com/docker/docker` (Docker engine API for the Compose target's
runtime-state reader). Accorda delegates to these libraries rather than
maintaining its own Git transport or Compose parser, so it stays focused on
its own mission (reconciliation) and avoids hand-rolled code that would have
to track upstream specs.

**Consequence.** `go.mod` stays tiny and the adapters inherit the user's
environment. Embedding `go-git` or the Docker SDK in an adapter later would
not violate the spec, as long as Core stays provider-agnostic; any new
dependency requires justification and must not leak provider-specific
assumptions into core.

### 1a. Direct dependencies and their usage

Each direct dependency is confined to a single adapter or subsystem and
does not leak into core:

| Library | Version | Used by | Purpose |
| --- | --- | --- | --- |
| `github.com/spf13/cobra` | v1.10.2 | `cmd/accorda` | CLI command tree, flag parsing, help generation for the `accorda` binary (`init`, `status`, `diff`, `plan`, `sync`, `history`, `inspect`, `logs`, `doctor`, `version`). |
| `gopkg.in/yaml.v3` | v3.0.1 | `internal/config` | YAML decoding of `accorda.yaml` with strict field validation (`KnownFields(true)`) and a custom `UnmarshalYAML` on `Secrets` for two-shape acceptance. |
| `github.com/go-git/go-git/v6` | v6.0.0-alpha.5 | `internal/sources/git` | Pure-Go Git operations: clone (`PlainCloneContext`), fetch (`Remote.FetchContext`), checkout (`Worktree.Checkout`), commit metadata (`CommitObject`), file-at-commit reads (`Tree().File()`). Auth via `ssh.PublicKeys` / `http.BasicAuth`. Replaces the system `git` CLI. |
| `github.com/compose-spec/compose-go/v2` | v2.14.0 | `internal/targets/compose` | Compose file parsing via `loader.LoadWithContext` into `types.Project`, then normalized into `state.Service`. Handles the full Compose schema (interpolation, extends, profiles, short/long forms). Replaces the hand-rolled parser. |
| `github.com/docker/docker` | v28.5.2+incompatible | `internal/targets/compose` | Docker engine API client used by `Target.Current` to list and inspect the project's containers and map them to `state.RuntimeState` (container state + health). Reached through a local `dockerClient` seam so the SDK does not leak into core. |

All other entries in `go.mod` are indirect (transitive) dependencies of these
five, pulled in automatically by `go mod tidy`. They are not imported by
Accorda directly.

### 2. `docs/ACCORDA.md` is authoritative and immutable

**Context.** The spec is the source of truth for the product.

**Decision.** `docs/ACCORDA.md` must not be modified by agents or
contributors. When the code diverges from an earlier spec assumption, record
the divergence here, in `README.md`, or in a package `doc.go` — never in the
spec.

**Consequence.** Doc comments cite spec sections (`// (docs/ACCORDA.md §8)`)
so code and spec stay traceable.

### 3. Target- and source-agnostic core

**Context.** §12 requires that core never fundamentally knows about GitHub,
Docker Compose, AWS, or Kubernetes.

**Decision.** `internal/core/*` imports only `state`, `plan`, `health`;
concrete drivers live under `internal/targets/*` and `internal/sources/*`.
Core interacts with targets via the `Target` interface
(`Validate`, `Current`, `Plan`, `Apply`, `Health`) and with sources via the
`Source` interface (`Validate`, `Fetch`, `Desired`). Neither core nor any
adapter imports a concrete provider package.

**Consequence.** New targets/sources implement the interface; core never
changes when a new provider is added.

### 4. Compile-time interface checks with stubs

**Context.** Interfaces must be guarded so a missing method is caught at
build time, not at runtime, while drivers are still being built.

**Decision.** Every interface package ships a `Stub` (no-op returning
`ErrNotImplemented`) and a compile-time assertion
`var _ Interface = (*Stub)(nil)`. Concrete drivers carry the same assertion
(e.g. `var _ sources.Source = (*Git)(nil)`).

**Consequence.** Core code and tests can reference a `Target`/`Source` before
a real driver exists; a renamed or removed method fails the build.

### 5. Value-type semantics and deep-copy `Clone`

**Context.** The reconcile loop passes state between phases and must not
suffer aliasing bugs.

**Decision.** `state.*`, `plan.Plan`, and `health.Health` are value types.
Each has a `Clone()` deep copy. Clone helpers preserve nil-vs-empty so a
zero value round-trips (`cloneServices`, `cloneStringMap`, `clonePorts`,
`cloneVolumes`).

**Consequence.** Callers can snapshot and mutate copies safely. New
reference-type fields added to these structs must be covered by `Clone`.

### 6. Image-centric service model

**Context.** §7, §8, §9 revolve around container image references and digests.

**Decision.** `state.Service` is image-centric: a service without an image
fails `Validate`. The Compose parser requires an `image` per service
(build-only services fail load-time validation). The git source's minimal
parser populates `Image` and `Env`; the Compose driver populates the full
normalized set (`Command`, `Ports`, `Volumes`, `Networks`, `Labels`,
`Healthcheck`, `DependsOn`).

**Consequence.** Desired state is concrete and deployable. Build-only
services are a known gap until a build-to-image resolution step exists.

### 7. Compose parsing via compose-go

**Context.** The full Compose spec is large and evolving (interpolation,
extends, profiles, short/long forms). Accorda only needs the subset it
reasons about for reconciliation, but hand-rolling a parser means tracking
the spec ourselves.

**Decision.** `internal/targets/compose` uses the compose-go loader
(`github.com/compose-spec/compose-go/v2/loader`) to parse Compose files into
`types.Project`, then normalizes the subset of `types.ServiceConfig` Accorda
models (image, command, environment, ports, volumes, networks, labels,
healthcheck, depends_on) into `state.Service`. The loader handles YAML
parsing, interpolation, extends, and normalization so Accorda does not
maintain its own parser. Validation enforces that every service has an image.

**Consequence.** Accorda gets full Compose spec compliance for free; adding
a new field is a localized normalization in `parse.go`. The dependency is
confined to the `targets/compose` adapter and does not leak into core.

### 8. Git source uses go-git

**Context.** §13 requires generic Git over SSH or HTTPS with zero SaaS
dependency. The spec does not mandate a Git library; this is an
implementation choice.

**Decision.** `internal/sources/git` uses `github.com/go-git/go-git/v6`
instead of shelling out to the system `git` CLI. It clones, fetches, checks
out, and reads files at commits via the go-git API. Auth is handled via
go-git transport methods: `ssh.PublicKeys` for SSH key auth, `http.BasicAuth`
for HTTPS token auth, and ambient (SSH agent / unauthenticated HTTPS) when
no auth is configured. The `git` CLI is no longer a runtime dependency.

**Consequence.** No system `git` dependency; typed commit/tree objects
replace hand-parsed CLI output. Transport and credential handling come from
go-git. The dependency is confined to the `sources/git` adapter and does
not leak into core.

### 9. Secrets are never logged

**Context.** §18/§56 require that tokens, keys, and plaintext secrets never
appear in logs or errors.

**Decision.** `Auth.Token` and `Auth.Key` are treated as secrets. For the Git
source, `Source.URL` stays clean and is used in errors and
`DesiredState.Repository`; the credential-bearing `remoteURL` is used only by
clone/fetch and is never logged. `redactURL` strips userinfo before any URL
surfaces. Error messages reference field names, never values. Tests assert
no token leak in errors, args, and state (`auth_test.go`).

**Consequence.** Adding a new auth path or error message must preserve this
guarantee; tests guard it.

### 10. Strict config loader

**Context.** Typos in `accorda.yaml` should surface immediately.

**Decision.** `internal/config` decodes with `KnownFields(true)` (reject
unknown fields), applies defaults for zero-value optional fields, and
returns field-oriented errors. `Secrets` accepts either a list of file paths
or `{provider: sops}` via a custom `UnmarshalYAML`.

**Consequence.** The loader is strict and user-friendly; new config fields
require an explicit validator update.

### 11. `accorda init` writes the unified `accorda.yaml` format

**Context.** The CLI `init` command originally wrote a minimal `accorda.env`
dotenv file while `accorda sync` read the canonical `accorda.yaml`
(docs/ACCORDA.md §25), so the primary onboarding path was broken (issue #68).

**Decision.** `cmd/accorda` `init` now writes `accorda.yaml`
(const `config.File`) using `config.MarshalProject`, the inverse of
`config.Parse`. The written file carries the schema version, environment,
git source, and Compose target, with optional sections omitted via yaml
`omitempty` so the output is minimal and round-trips through the strict
loader. `init` applies `config.ApplyDefaults` and `config.Validate` before
marshaling so the file is immediately consumable by `accorda sync`. The
dotenv format is no longer produced; `.gitignore` ignores `accorda.yaml`
instead of `accorda.env`.

**Consequence.** `init` and `sync` share the unified project format
(docs/ACCORDA.md §25), and the README Quick Start works end-to-end. The
`config` package exports `MarshalProject` and `ApplyDefaults` so the CLI can
produce a valid project file without re-implementing defaults or encoding.

### 12. Deterministic comparison and plan output

**Context.** CLI, event, and history output must be stable.

**Decision.** `state.Compare` sorts `Reasons` (`sort.Strings`) so output is
deterministic. `plan.Plan` includes explicit `Noop` actions so a full plan
reflects every service, making future hashing/auditing deterministic
(§31). `Plan` carries optional `Security` and `Policy` for the same reason.

**Consequence.** Plan hashing and event ordering are reproducible.

### 13. DRY, SOLID, and cognitive complexity

**Context.** The codebase must stay reviewable and maintainable as it grows.

**Decision.** All new and changed code must follow:
- **DRY**: do not duplicate literals, logic, or helpers. Extract named
  constants for repeated string/numeric literals (e.g.
  `defaultComposeFile`, `httpsScheme`). Extract shared helpers instead of
  copy-pasting.
- **SOLID**: keep package boundaries aligned to single responsibilities
  (core vs adapters vs CLI); depend on interfaces, not concrete providers;
  keep types open to extension via new implementations, closed to modification
  of existing contracts.
- **Cognitive complexity**: keep functions and test functions below 15
  cognitive complexity. Split large test functions into table-driven cases or
  focused sub-tests; extract helpers for repeated assertion blocks. If a
  function exceeds 15, refactor before committing.

**Consequence.** Agents and contributors apply these rules during
implementation and review; linters (SonarQube, gopls `unusedparams`) surface
violations. See `AGENTS.md` for the enforcement workflow.

### 14. Dependency licenses

**Context.** Accorda OSS is Apache-2.0 (see `LICENSE`). The dependency tree
must remain permissively licensed so the OSS binary can be distributed without
copyleft obligations on the compiled binary.

**Decision.** All direct and indirect dependencies are permissively
licensed (Apache-2.0, MIT, BSD, ISC). The dependency tree does not include
GPL, LGPL, or other copyleft licenses. A summary of direct dependencies:

| Library | License |
| --- | --- |
| `github.com/spf13/cobra` | Apache-2.0 |
| `gopkg.in/yaml.v3` | MIT + Apache-2.0 |
| `github.com/go-git/go-git/v6` | Apache-2.0 |
| `github.com/compose-spec/compose-go/v2` | Apache-2.0 |
| `github.com/docker/docker` | Apache-2.0 |

**`github.com/opencontainers/go-digest`** (indirect, pulled by compose-go)
ships two license files: `LICENSE` (Apache-2.0) for the Go code, and
`LICENSE.docs` (CC-BY-SA-4.0) for documentation material. The Creative
Commons license applies only to non-code documentation in the module, not
to the Go implementation or the compiled binary. The compiled binary is
governed by the Apache-2.0 code license.

Before adding a new dependency, verify its license is permissive and does not
introduce copyleft obligations. If a dependency ships separate
code/documentation licenses, confirm the code license governs the compiled
binary. When preparing a release, inspect each dependency's license file
and preserve applicable notices in a compliance file.

**Repository layout for distribution:**

```
LICENSE                       # Apache License 2.0 for Accorda
NOTICE                        # Accorda's NOTICE (Apache-2.0 §4(d))
THIRD_PARTY_LICENSES.md        # Dependency licenses + copyright notices, grouped by license
docs/licensing.md              # Developer/CI workflow for generating the inventory
```

`THIRD_PARTY_LICENSES.md` groups dependencies by license and includes the
verbatim upstream license texts and component-specific copyright/NOTICE
attributions. It is generated from the actual Go dependency graph (see
`docs/licensing.md` for the commands), so a future dependency update cannot
quietly introduce GPL/AGPL/MPL. A CI license allowlist (e.g. `go-licenses`
or equivalent) should fail the build if a disallowed license appears.

For Apache-2.0 dependencies, upstream `NOTICE` file content is propagated
where present (Apache-2.0 §4(d) requires retaining relevant NOTICE
attribution in derivative works). For MIT/BSD/ISC dependencies, component-
specific copyright notices are preserved in `THIRD_PARTY_LICENSES.md`; their
licenses do not apply to Accorda's own code.

**Consequence.** Accorda OSS stays distributable under Apache-2.0 without
copyleft concerns. The `NOTICE` and `THIRD_PARTY_LICENSES.md` files, plus a
CI license allowlist, keep the project compliant as dependencies change.

### 15. Integration and end-to-end tests are gated behind the `integration` build tag

**Context.** The testing strategy (docs/ACCORDA.md §55) calls for an
integration and E2E suite against a real Git repository, Docker daemon, and
Docker Compose. Those dependencies are not available in the default CI
environment, and the default `go test ./...` run must stay hermetic.

**Decision.** Integration and E2E tests live in `*_test.go` files guarded by
the `integration` build tag and skip gracefully (via `t.Skipf`) when a
prerequisite is unavailable. Shared repository fixtures and prerequisite
checks are extracted into `internal/testutil` so the git source, compose
target, and `cmd/accorda` suites do not duplicate setup or skip logic
(DRY, docs/DECISIONS.md #13). The compose target's integration tests exercise
the real `dockerClient` and `cliRunner` seams (no injected fakes), and the
`cmd/accorda` E2E test drives the full lifecycle through `accorda sync`.

**Consequence.** `go test ./...` remains hermetic; the integration suite is
run explicitly with `go test -tags integration ./...`. New integration tests
must use the `integration` tag and the `testutil` helpers rather than
re-implementing fixtures.

---

## Decision log (continued)

New entries are appended below. Use the next available number.

### 15. Compose target reads runtime state via the Docker engine SDK

**Context.** Issue #9 (§5.3) requires the Compose target to read per-service
runtime state (container state + health) from the Docker engine/Compose API
with no mutation. The driver needs to enumerate the project's containers and
map them back to Accorda service names and health states.

**Decision.** `internal/targets/compose` adds the Docker engine SDK
(`github.com/docker/docker`, `+incompatible`) as a direct dependency, confined
to the adapter. The driver talks to the engine through a local `dockerClient`
seam (a subset of the SDK APIClient: Ping, ContainerList, ContainerInspect)
so core never imports the Docker SDK (docs/DECISIONS.md #3). `Target.Current`
lists all containers carrying the `com.docker.compose.project` label matching
the project name (including stopped ones, so drift is observable), inspects
each for state and health, and maps them via the `com.docker.compose.service`
label into a `state.RuntimeState`. Health is part of runtime state
(docs/ACCORDA.md §5.3), so the per-container inspect is accepted for the MVP;
`ContainerList`'s `Summary` does not carry health, and moving health out of
runtime state would require a spec change that `docs/ACCORDA.md` does not
currently authorize. The project name is derived from the Compose file's
directory basename (matching Compose v2's heuristic) and normalized to match
the label; `WithProjectName` overrides it for explicit config. `Apply` and
`Health` remain `ErrNotImplemented` until later milestones.

**Consequence.** Runtime state is read back without shelling out to
`docker compose ps`; the Docker SDK is the second adapter-specific runtime
dependency (after compose-go for parsing). Tests use a fake `dockerClient` so
no running daemon is required. The dependency is Apache-2.0 (permissive) and
is recorded in the #14 license table.

### 16. Compose plan generation delegates to plan.DriftActions

**Context.** Issue #10 (§9, §12) requires the Compose target's `Plan` method
to produce a per-service `CHANGED`/`UNCHANGED` desired-vs-deployed diff that
is safe and idempotent. The diffing logic already exists as the
target-agnostic `plan.DriftActions` helper (create, recreate, start, stop,
remove, noop), which the spec's §12 abstraction expects every target to
reuse rather than reimplement.

**Decision.** `Target.Plan` reads the runtime state via `Current` and
delegates the diff to `plan.DriftActions(desired, nil, runtime)`, wrapping
the resulting actions in a `plan.Plan` whose identifying fields are populated
from the desired state (`Environment` = repository, `Commit` = commit;
`DeploymentID` is left empty because deployment identifiers are assigned by
the reconcile loop, docs/ACCORDA.md §7). `DriftActions` now iterates service
names in sorted order so the action slice is deterministic regardless of Go's
randomized map iteration order, honoring the determinism contract
(docs/DECISIONS.md #12) that plan hashing and signing depend on. `Plan`
gains `Changed()` (true when any action is not a noop) and `String()` (a
per-service `CHANGED`/`UNCHANGED` summary) for CLI output.

**Consequence.** The Compose target's plan phase is implemented without
duplicating diff logic; the same `DriftActions` helper will serve future
targets. Plan output is deterministic and human-readable, ready for the
`accorda plan` CLI command (issue #26) and eventual hashing/signing (§31).

<!-- Add new decisions here. Format:
### N. Short title
**Context.** ...
**Decision.** ...
**Consequence.** ...
-->

### 17. Compose Apply shells out to `docker compose` via a runner seam

**Context.** Issue #11 (§9) requires `Target.Apply` to run the equivalent of
`docker compose up -d` scoped to only the changed services, handling errors
and partial failures. Driving recreation through the Docker engine SDK would
mean reimplementing Compose's container, network, and volume creation logic,
which the spec explicitly frames as "the equivalent of `docker compose up
-d`" rather than a mandate to reimplement Compose.

**Decision.** `Target.Apply` maps each plan action to a `docker compose`
subcommand and delegates execution to a local `composeRunner` seam
(`Run(ctx, args...)`), whose production implementation (`cliRunner`) shells
out to the `docker compose` CLI scoped with `-f <file> -p <project>`. The
The mapping is: create/recreate/start → `up -d <service>`; remove → `up -d
--remove-orphans`; pull → `pull <service>`; stop → `stop <service>`; noop →
skipped. Orphans are removed via `--remove-orphans` rather than `rm
<service>` because the orphan's service name is no longer defined in the
Compose file, so `rm` would fail with "no such service".
The runner is injected via `WithRunner` so tests substitute a fake without a
`docker compose` binary or daemon, mirroring the `dockerClient` seam used by
`Current`. Partial failures return an error naming the failing service and
action.

**Consequence.** Apply inherits Compose's recreation semantics without
reimplementing them, and the `docker compose` CLI dependency stays confined
to the adapter (docs/DECISIONS.md #3). `Health` remains `ErrNotImplemented`
until issue #15.

### 18. Image pull policies select pull actions in Plan

**Context.** Issue #12 (§9) requires the Compose target to support the four
image pull policies — `changed`, `missing`, `always`, `never` — and to pull
only the images each policy selects. The `changed` policy depends on knowing
which services changed, which the plan phase already computes via
`plan.DriftActions` (docs/DECISIONS.md #16).

**Decision.** `Target` carries a `pullPolicy` field (default
`config.PullChanged`), settable via `WithPullPolicy`; the reconcile loop
supplies it from the project's `images.pull` setting. `Target.Plan` computes
the drift actions, then prepends pull actions selected by `selectPulls`
(`internal/targets/compose/pull.go`) before the drift actions so images are
fetched before the services that depend on them are created or recreated.
The policy semantics are: `changed` pulls only services whose drift action is
create or recreate (a stopped service with an unchanged image already has its
image locally); `missing` pulls only images not present in the engine's local
image list (read via a new `ImageList` method on the `dockerClient` seam);
`always` pulls every desired service's image; `never` pulls nothing. Pull
actions are ordered by service name so the plan stays deterministic
(docs/DECISIONS.md #12).

**Consequence.** The pull policy is enforced at plan time, so `Apply` needs no
policy awareness and simply executes the `pull` actions already in the plan.
The `dockerClient` seam grows `ImageList` (still a subset of the Docker SDK
APIClient, confined to the adapter). The `missing` policy is the only one
that reads the engine's image list; the others are pure functions of the
desired state and drift actions.

### 19. Service hashing for recreate decisions

**Context.** Issue #13 (§10) requires Accorda to compare normalized service
configuration rather than relying exclusively on textual Git diffs, so it can
decide whether a service actually requires recreation. The normalized service
config (image, command, env, ports, volumes, networks, labels, healthcheck,
depends_on) is already produced by the Compose parser (docs/DECISIONS.md #7)
and its unordered collections are already sorted for determinism
(docs/DECISIONS.md #12).

**Decision.** `state.Service` gains a `Hash()` method that canonicalizes the
reconciliation-relevant fields into a deterministic string and returns its
SHA-256 hex digest. Unordered collections (env, labels, ports, volumes,
networks, depends_on) are sorted at the canonicalization boundary so
reordering-equivalent configs hash identically; ordered fields (command,
healthcheck test) are preserved verbatim because their order is significant.
`state.Compare` adds a final desired-vs-deployed check that compares the two
hashes, so a service whose image and env match but whose command, ports,
volumes, networks, labels, healthcheck, or depends_on changed is flagged
OUT_OF_SYNC. The hash lives in `internal/core/state/hash.go` and uses only
the standard library (`crypto/sha256`, `encoding/hex`).

The recreation decision itself is wired into the plan path:
`plan.DriftActions` compares the deployed service's hash against the desired
hash and emits `ActionRecreate` when they differ even though the image is
unchanged. To supply the deployed configuration, the `Target.Plan` interface
method gains a `deployed *state.DeployedState` parameter (previously the
Compose target passed `nil`, so `DriftActions` could not see the deployed
hash and also could not distinguish `ActionCreate` from `ActionStart`). The
Compose target forwards its `deployed` argument to `DriftActions`.

**Consequence.** Recreate decisions now cover the full normalized service
definition, not just image and env, and are driven in both the status path
(`Compare`) and the plan path (`DriftActions`). The hash is a pure function
of the service value, so it is deterministic and testable without a target.
Any new reconciliation-relevant field added to `state.Service` must be
included in `canonical()` or it will be silently excluded from recreate
decisions. The `Target.Plan` signature change is a breaking interface change
that all target drivers and the reconcile loop must adopt.

### 20. Reconciliation lifecycle state machine and event bus

**Context.** Issue #14 (§6) requires the reconciliation lifecycle state
machine that orchestrates `Source.Fetch`, `Target.Plan`, `Target.Apply`, and
health verification, emitting state transitions as events (§21). The issue
lists the event bus (#20) as a dependency, but the bus is self-contained
infrastructure with no blockers, so it is implemented here rather than
blocking the lifecycle on a separate milestone.

**Decision.** `internal/core/events` gains an in-memory `Bus` (interface +
`NewBus`) with `Publish` and `Subscribe`; subscribers receive events
synchronously in subscription order, and `Subscribe` returns an idempotent
unsubscribe function. `internal/core/reconcile` gains a `Reconciler` that
walks the lifecycle phases (`DETECTED → FETCHING → VALIDATING → PLANNING →
PULLING → DEPLOYING → VERIFYING → HEALTHY → SYNCED`), emitting an
`EventStateTransition` (payload `StateTransition{From, To, Commit,
DeploymentID, Err}`) at each phase change plus the §21 deployment events
(`deployment.detected`, `deployment.started`, `deployment.succeeded`,
`deployment.failed`, `deployment.rolled_back`, `health.changed`). Failure
paths transition to `FAILED`; when apply or health verification fails and a
previous deployment is known (`WithPrevious`), the reconciler re-plans and
re-applies the previous services and emits `deployment.rolled_back` (§20).
Because `Target.Health` is still `ErrNotImplemented` (issue #15), the
reconciler treats that sentinel as "healthy" so the lifecycle can reach
`SYNCED`; the health gate becomes active once #15 lands. The reconciler
depends only on the `Source` and `Target` interfaces (docs/DECISIONS.md #3).

**Consequence.** The lifecycle is driven end-to-end in core without a
concrete provider, and consumers observe progress through the bus rather
than provider callbacks. The `ErrNotImplemented` health bypass is a
temporary, documented divergence that must be removed when #15 is
implemented. The `Bus` is synchronous and in-memory; a future async or
durable bus can implement the same interface without changing the reconciler.

### 21. Compose health verification maps Docker healthcheck status

**Context.** Issue #15 (§19) requires the Compose target to wait for Compose
healthchecks before declaring a deployment successful, distinguishing
DEPLOYED, HEALTHY, and SYNCED, and honoring `health.timeout`. The runtime
state already carries each container's healthcheck status (docs/DECISIONS.md
#15), and `health.Health` already models the aggregate and per-service
outcomes.

**Decision.** `Target.Health` reads the runtime state via `Current` and maps
each service's Docker healthcheck status to a `health.Status`: `healthy` →
`StatusHealthy`, `starting` → `StatusStarting`, empty (no healthcheck) →
`StatusUnknown`, and anything else → `StatusUnhealthy`. It polls `Current`
every `healthPollInterval` (2s) until no service is still starting, or until
the health timeout elapses, then summarizes the per-service results. The
timeout is carried on the `Target` as `healthTimeout` (default
`defaultHealthTimeout` = 120s, mirroring the config default), settable via
`WithHealthTimeout`; the reconcile loop will supply it from the project's
`health.timeout` setting once it constructs targets from a `config.Project`
(the `sync` command is still a stub, so no production caller wires it yet).
When the timeout elapses while a service is still
starting, that service is reported unhealthy with a message naming the
timeout, so a deployment that never becomes healthy is not silently declared
successful. A service with no healthcheck is immediately unknown and does not
block the wait. `Health` returns an error only when reading runtime state
fails; an unhealthy deployment is reported through the returned `Health`
value, not an error.

**Consequence.** The `ErrNotImplemented` health bypass in the reconciler
(docs/DECISIONS.md #20) is now dead for the Compose target: `Health` returns
a real assessment, so the reconcile loop's VERIFYING phase gates on actual
health. The gate is on `Overall == StatusUnhealthy` rather than `!Healthy`,
so a no-healthcheck deployment (Overall == `StatusUnknown`) proceeds to
SYNCED instead of being rolled back — DEPLOYED, HEALTHY, and SYNCED are
distinct outcomes (docs/ACCORDA.md §19). The Docker SDK dependency is
unchanged (health is read from the existing `ContainerInspect` path). The
health timeout is a target-level concern, consistent with the pull policy
(docs/DECISIONS.md #18); the reconcile loop must thread `health.timeout` into
the target when it constructs the driver.

### 22. Drift repair is a reconciler-level policy

**Context.** Issue #16 (§5.3) requires drift detection and repair: when the
runtime diverges from the desired state while Git is unchanged, Accorda must
emit `DriftDetected` and, when `reconcile.drift: repair` is configured,
restore the desired runtime and emit `DriftReconciled`. The config loader
already models the three drift policies (`repair`, `report`, `disabled`) and
defaults to `report` (docs/DECISIONS.md #10), and the reconciler already
detects drift in its `sync` phase via `state.Compare` and emits
`DriftDetected` (docs/DECISIONS.md #20).

**Decision.** `internal/core/reconcile` gains a `DriftPolicy` type
(`DriftReport`, `DriftRepair`, `DriftDisabled`) carried on the `Reconciler`
and settable via `WithDriftPolicy`, defaulting to `DriftReport`. The `sync`
phase's drift branch delegates to `handleDrift`, which emits `DriftDetected`
unless the policy is `disabled`, and, when the policy is `repair`, re-plans
(`Target.Plan`) and re-applies (`Target.Apply`) the desired state against the
synthesized deployed state, then emits `DriftReconciled` on success. Repair
reuses the existing `Plan`/`Apply` path rather than a bespoke drift path, so
the same `plan.DriftActions` diff (docs/DECISIONS.md #16) drives both initial
deployment and drift repair. A failed repair is silent (no event) and leaves
the result as `DRIFTED`, matching the report-only behavior.

**Consequence.** Drift repair is implemented in core without a concrete
provider, honoring docs/DECISIONS.md #3. The `reconcile.drift` config value
is not yet threaded into the reconciler because the `sync` command is still a
stub and no production caller constructs a `Reconciler` from a
`config.Project`; the `WithDriftPolicy` seam is the wiring point for that
future work, mirroring the `WithHealthTimeout`/`WithPullPolicy` seams on the
Compose target.

### 23. `accorda sync` wires the reconciler to the Git source and Compose target

**Context.** Issue #27 (§11) requires the `accorda sync` command to trigger
one reconciliation pass on demand. The reconciler (docs/DECISIONS.md #20),
the Git source (docs/DECISIONS.md #8), and the Compose target
(docs/DECISIONS.md #15–#19, #21) were all implemented and unit-tested but had
no production caller: the `sync` command was a stub, so the drift policy
(#22), pull policy (#18), and health timeout (#21) seams were never threaded
from a `config.Project`.

**Decision.** `cmd/accorda` gains a real `newSyncCmd` (in `sync.go`) that
loads the project file via `config.Load(dir)`, constructs the Git source
(`git.New(proj.Source)`) and the Compose target
(`compose.New(proj.Target, compose.WithPullPolicy(proj.Images.Pull),
compose.WithHealthTimeout(proj.Health.Timeout))`), and drives a single
`reconcile.New(...).WithDriftPolicy(driftPolicy(proj.Reconcile.Drift))`
cycle, printing the terminal phase and the `state.Comparison` summary. A
`driftPolicy` helper maps the config's `reconcile.drift` string to the
reconciler's `DriftPolicy` (unknown values degrade to `DriftReport`, matching
the reconciler's defensive default). Only the Compose target is wired;
`buildTarget` returns a "not implemented" error for other target types, which
the config loader still recognizes. The `sync` command is removed from the
`stubCmd` set.

**Consequence.** The reconciliation lifecycle is now reachable end-to-end
from the CLI, and the previously test-only seams (`WithDriftPolicy`,
`WithPullPolicy`, `WithHealthTimeout`) are exercised by a production caller.
The `sync` command performs a single pass, not the §11 loop; the loop and
deployment-history persistence remain future work (issue #14's follow-up).
`init` now writes `accorda.yaml` (docs/DECISIONS.md #11, issue #68), so the
Quick Start produces a project file `sync` can load directly.

### 24. Integration tests run in CI and surfaced real bugs

**Context.** After wiring the `integration` build-tag suite (docs/DECISIONS.md
#15) into a CI job with a live Docker daemon and the Go toolchain that
`go-version-file` selects, the run failed in three distinct ways that were
invisible to the hermetic unit tests and to a newer local Go toolchain.

**Decision.** The failures were fixed as follows:
- The compose/E2E suite surfaced a real production bug: `Target.Current` read
  the runtime image from `ContainerJSONBase.Image`, which Docker populates
  with the resolved image ID (`sha256:...`), not the image reference the
  operator passed (`busybox:1.36`). Desired state models references
  (docs/ACCORDA.md §8), so the desired-vs-runtime comparison always reported
  drift. The reader now prefers `Config.Image` (the reference) and falls back
  to `ContainerJSONBase.Image` only when `Config` is absent.
- The git source integration tests failed under Go 1.25.0 because go-git's
  `file://` transport returns "repository not found" on that toolchain but
  works on Go 1.25.6+. The CI workflow now pins the toolchain explicitly to
  Go 1.25.6 (`go-version: '1.25.6'`) rather than selecting it via
  `go-version-file`, leaving the module's `go` directive at the true minimum
  (1.25.0) so downstream builders on older 1.25.x patches are not forced up.
- The E2E fixture used a relative `target.file`, which `sync` resolves against
  the process working directory rather than the project `--dir`; it now uses
  an absolute path.

**Consequence.** The integration suite now passes in CI against a real Docker
daemon, and it proved its value by catching the image-reference bug that unit
tests could not. The module's `go` directive stays at 1.25.0; only the CI
toolchain is pinned to 1.25.6 to work around go-git's file-transport quirk.

### 25. Docker engine SDK vulnerabilities are accepted risk with no fixed version

**Context.** `govulncheck` reports two advisories on the Docker engine SDK
direct dependency (`github.com/docker/docker` v28.5.2+incompatible, the
latest release): `GO-2026-4883` (off-by-one in plugin privilege validation
during `docker plugin install`) and `GO-2026-4887` (AuthZ plugin bypass with
oversized request bodies). Both advisories state that systems not using
Docker plugins / authorization plugins are unaffected.

**Decision.** Accept the findings as risk rather than attempt a version bump,
because no fixed `github.com/docker/docker` release exists (the fixes landed
only in the restructured `github.com/moby/moby/v2` module, ≥ v2.0.0-beta.8).
Accorda uses the SDK only as a client (Ping, ContainerList, ContainerInspect,
ImageList, ImageInspect) and never installs plugins or drives the daemon's
plugin/AuthZ flow, so the reported attack surface is not reachable from
Accorda's usage. Migrating the dependency to `moby/moby/v2` is tracked as
future work rather than a current change.

**Consequence.** The `dockerClient` seam (docs/DECISIONS.md #15) stays on
`github.com/docker/docker`; the advisories are documented as accepted,
unreachable risk. A dependency migration to `github.com/moby/moby/v2` would be
the way to clear them and should be revisited when that module stabilizes.

### 26. Deployment receipts are recorded in an append-only JSON-lines journal

**Context.** Issue #18 (§7) requires every successful deployment to create a
deployment receipt recording the deployment ID, repository, environment,
commit, start/completion timestamps, and per-service image reference and
resolved manifest digest. The spec's storage mechanism is underspecified; it
consistently describes a "local journal" (§21), "local history" (§42), and
the agent owning the "local filesystem" (§28), with the agent remaining
functional without Accorda Cloud (§4). The runtime state already carries each
running service's image reference (docs/DECISIONS.md #24); it does not yet
carry the resolved digest.

**Decision.** Add a `Digest` field to `state.RuntimeService`, populated by
the Compose target's `Current` via a new `ImageInspect` method on the
`dockerClient` seam (reading the image's `RepoDigests[0]`, best-effort —
unresolvable images keep an empty digest). Record receipts through a new
`history.Store` interface, whose default `history.FileStore` writes an
append-only JSON-lines journal (one receipt per line, flushed on append) on
the local filesystem, adding no dependency beyond the standard library
(docs/DECISIONS.md #1). Receipts are written by the reconcile loop at the end
of a changed, SYNCED deployment (`recordReceipt`), gated on the plan actually
changing the target so a no-op cycle produces no receipt. The loop assigns
the deployment ID (`dep_<hex>`, docs/ACCORDA.md §7) when the target's plan
leaves it empty. The `sync` command wires the store and environment, storing
receipts under `$XDG_STATE_HOME/accorda/receipts/<project>.jsonl` (falling
back to `~/.local/state`), keyed by project directory.

**Consequence.** Accorda can now answer "exactly which commit and image
digest was running on target X at time Y?" (docs/ACCORDA.md §7). The JSON-lines
journal is crash-safe (append + fsync) and preserves the audit-trail property
that a receipt is never mutated once written. The `dockerClient` seam grows
`ImageInspect` (still a subset of the Docker SDK, confined to the adapter).
Receipt recording is best-effort — a store failure is not a deployment
failure. The `history.Store` seam leaves room for a future durable/durable
bus or SQL backend without changing core. A follow-up will surface receipts
via `accorda history` (issue #28) and rollback recording (§20).

### 27. Deployment history records result and changes for healthy and failed cycles

**Context.** Issue #19 (§11) requires a local append-only journal of
deployments carrying `commit`, `result`, and `changes` — the `accorda history`
table shows a `RESULT` column (`✓ healthy` / `✗ failed`) and a `CHANGES`
column (the affected services). The receipt journal introduced in ADR #26
records only successful, SYNCED deployments and carries neither a result nor
a changed-services list, so the history could not show failed cycles or which
services each deployment touched.

**Decision.** Extend `history.Receipt` with two fields: `Result` (`Outcome`
= `healthy` or `failed`, docs/ACCORDA.md §11) and `Changes` (the sorted,
unique service names the deployment changed, from the plan's non-noop
actions, deterministic per docs/DECISIONS.md #12). The reconcile loop records
an `OutcomeHealthy` receipt at the end of a changed, SYNCED deployment
(carrying the runtime digests) and an `OutcomeFailed` receipt on a deploy or
health-verification failure (carrying no services, since the deployment never
converged). Recording remains best-effort and preserves the ADR #26 gate for
healthy receipts (a no-op cycle records nothing); a failed receipt is always
recorded, because a no-op plan never reaches a failure path.

**Consequence.** The deployment history now reflects both healthy and failed
cycles with their changed services, matching the §11 table. Failed receipts
omit digest data because the runtime was never read. A store failure still
never changes the reported cycle outcome. `accorda history` (issue #28) can
now render the §11 columns directly from the journal.

### 28. Rollback restores the last known-healthy deployment

**Context.** Issue #17 (§20) requires that when a deployment fails (apply or
health verification), Accorda restore the last known-healthy commit "where
safely possible" and record the rollback in deployment history. Deployment
receipts (#26, #27) already record per-service image + digest for healthy
cycles and carry a `Commit`, so the previous deployment is reconstructible
from the receipt journal. The Compose target's `Plan`/`Apply` resolve services
against the on-disk Compose file, so a rollback that merely re-planned the
previous services would recreate the image currently in the file — the failed
one.

**Decision.** Rollback is wired as follows:
- `internal/core/history` gains `OutcomeRolledBack`, recorded as a receipt
  carrying the restored commit when a rollback succeeds (§20).
- `reconcile.Result` gains `RolledBackTo` (the restored commit) alongside
  `RolledBack`; the `sync` command prints an informative
  `rollback: restored to commit <sha>` message so a user sees what happened.
- The reconcile loop gains a `desiredApplier` capability interface
  (`ApplyDesired(ctx, desired) (*plan.Plan, error)`); a target that
  implements it (the Compose target) is rolled back by applying the previous
  desired state directly, so the on-disk artifact reflects the restored
  services before `docker compose up -d` runs. A target that only implements
  `Target` is rolled back via the existing `Plan`+`Apply` path.
- The rollback restores the **full** previous service model by reading the
  desired state at the previous commit from the source (`Source.Desired`),
  not just the image reference recorded in the receipt, so the restored
  Compose file carries the previous command, env, ports, volumes, healthcheck,
  and dependencies. If the source cannot be read, it falls back to the
  image-only services from the receipt (the "where safely possible" qualifier
  in §20).
- `accorda sync` reconstructs the previous deployment from the receipt
  journal via `previousFromHistory(store)` (the most recent `OutcomeHealthy`
  receipt, image-only per the image-centric model of #6) and supplies it via
  `WithPrevious`. When history is empty, `previousFromHistory` returns nil and
  the reconciler's existing nil-previous guard makes rollback a no-op — the
  failure stands (the "where safely possible" qualifier in §20). A store read
  error is reported to the command's stderr so an operator can distinguish
  "no prior healthy deployment" from "history could not be read".

**Consequence.** A failed deployment rolls back automatically to the last
healthy commit with an informative CLI message and an `OutcomeRolledBack`
history record. With no prior healthy deployment, the failure stands and no
rollback is attempted. The Compose target implements `ApplyDesired`; other
targets keep the `Plan`+`Apply` fallback, and core stays target-agnostic
(#3). `accorda history` (#28 CLI) can later render the rolled-back rows from
the journal; only recording is implemented here.

### 29. Full validation is run through `scripts/test.sh`

**Context.** Agents and contributors were expected to run both the unit suite
(`go test ./...`) and the integration/E2E suite (`go test -tags integration
./...`) before publishing a pull request, but the long commands were easy to
get wrong, and running only one of them allowed a change that broke a module
outside the one under edit to slip through unnoticed until CI failed.

**Decision.** `scripts/test.sh` runs the full validation in one invocation:
a gofmt check, the build, and one additive integration-tagged test pass
(`go test -count=1 -tags integration ./...`), stopping on the first failure.
The tagged pass includes all regular unit tests, so running an untagged pass
separately would duplicate them. The script resolves the module directory
relative to its own location, uses `-count=1` to defeat caching, and relies on
the existing `internal/testutil` prerequisite checks to skip integration tests
gracefully when a prerequisite is unavailable. Agents and contributors are
instructed to use it (see `AGENTS.md` "Common commands" and
`docs/IMPLEMENTER.md` §5) for full validation rather than assembling the long
`go test` commands by hand.

**Consequence.** A single `scripts/test.sh` gives a complete validation pass,
so a change that breaks a module outside the one under edit is never missed.
The integration suites still skip gracefully without Docker, preserving the
hermetic default run from ADR #15.

### 30. `accorda status` is a read-only snapshot of the project posture

**Context.** Issue #25 (§11) requires an `accorda status` command that prints
the environment, repository, branch, Git HEAD, deployed commit, sync status,
runtime status, last-deploy time, and a per-service table of state/health/
image. The underlying data already exists: the Git source exposes HEAD via
`Fetch` and the redacted repository via `Desired`; deployment receipts
(`history.Store`) record the last healthy commit and completion time; and the
Compose target exposes the runtime via `Current`. None of this is persisted
as a single "status" snapshot, and `status` must not mutate anything.

**Decision.** `cmd/accorda/status.go` implements `accorda status` as a
read-only projection that composes the existing seams:
- It calls `src.Fetch` for the Git HEAD and `src.Desired` for the redacted
  repository/branch and declared services (best-effort; a source failure
  degrades to "unavailable" rather than aborting the whole report).
- It redacts the configured URL up front via the exported
  `git.RedactURL`, so the repository line never echoes an embedded
  credential even when the source cannot be read (docs/ACCORDA.md §18, §56).
- It reads the last healthy receipt via a shared `lastHealthyReceipt(store)`
  helper (extracted from `previousFromHistory` in `sync.go`) for the deployed
  commit and last-deploy time.
- It calls `Target.Current` for the runtime state and derives the aggregate
  runtime and per-service health via the exported `compose.HealthFromRuntime`
  (the existing health mapping, exported for reuse).
- The sync label (`SYNCED`/`OUT_OF_SYNC`/`UNKNOWN`) is derived from the Git
  HEAD vs the last healthy deployed commit and computed before any target
  read, so the line stays populated when the runtime is unreachable; the
  runtime label (`HEALTHY`/`UNHEALTHY`/`UNKNOWN`) is derived from the
  per-service health.
- Output is a tabular report matching the §11 example, with service rows
  sorted by name for deterministic output (#12). The `status` command no
  longer uses the shared stub; it is fully implemented.

**Consequence.** `accorda status` reports the project posture without changing
anything, reusing the Git source, receipt journal, and Compose runtime seams.
The runtime→health mapping is now exported (`compose.HealthFromRuntime`) so
`status` and the reconcile loop's `Health` phase agree; `lastHealthyReceipt`
is shared with `sync` so both surfaces agree on the last healthy deployment.
The git source's `redactURL` helper is exported (`git.RedactURL`) so `status`
redacts a configured URL identically to the source, keeping credentials out of
user-facing output. A future task may reconcile `status` with the
deployed-commit semantics of the persisted receipts (#7) as the CLI grows.

### 31. `accorda diff` and `accorda plan` are read-only CLI projections

**Context.** Issue #26 (§11) requires `accorda diff` (per-field deployed vs
desired) and `accorda plan` (intended actions without deploying). The plan
phase (#10, decision #16) and the `plan.Plan` `String()`/`Changed()` summary
already exist, and the receipt journal records the last healthy deployment
(#7, #27), so both commands can be built entirely from existing seams without
new core abstractions.

**Decision.** `cmd/accorda/diff.go` implements `accorda diff` as a read-only,
daemon-free projection: the "deployed" side is the desired state re-read from
Git at the last healthy deployment's commit (`lastHealthyReceipt` + source
`Desired`), because the receipt journal stores only per-service image/digest
and the full per-field definition must come from the source; the "desired"
side is the current Git HEAD. Only services and fields that differ are
printed, in a YAML-like tree with `deployed:`/`desired:` pairs matching the
§11 example, in sorted order (#12). `cmd/accorda/plan.go` implements
`accorda plan` by fetching the desired state, constructing the target, and
calling `Target.Plan` with the same full-model deployed baseline as `diff`
(re-read from the source at the deployed commit via a shared
`deployedAtCommit` helper, converted to a `DeployedState`), so the two
commands agree on the deployed side and a converged service is not
over-reported as `CHANGED`; it then prints the plan header and
`plan.Plan.String()`. Neither command mutates the target or source.

**Consequence.** `accorda diff` and `accorda plan` become fully implemented
CLI surface. `diff` needs no Docker daemon because it compares Git commits;
`plan` requires the target's runtime (via `Target.Plan`) but never applies
the plan. Both share the `deployedAtCommit` helper (and `lastHealthyReceipt`)
so the deployed baseline is consistent with each other and with `status`'s
deployed-commit read; `plan` uses the full model rather than the image-only
`previousFromHistory` rollback baseline, so its output reflects the actual
deployed configuration. The remaining stub commands are `history`, `inspect`,
`logs`, and `doctor`.

### 32. Plan environment is threaded from the project, not the desired state

**Context.** Issue #52 (§25, §31) requires `Plan.Environment` to reflect the
project's environment from `accorda.yaml`, not the Git-declared desired-state
repository. `Target.Plan` populated `Plan.Environment` with
`desired.Repository` as a stand-in, guarded by a TODO whose premise (that
`DesiredState` should carry an environment field) was incorrect: §5.1 lists
only `Repository`, `Branch`, `Commit` for desired state, while §25 declares
`environment` as a top-level project field (`config.Project.Environment`) and
§31 carries it as a plan identifying field. The reconciler already owned an
`r.environment` field (set via `WithEnvironment`, decision #23) used in
deployment receipts (§7), but the plan's `Environment` was still the
repository stand-in.

**Decision.** The environment is threaded from `config.Project.Environment`
into the Compose target via a new `WithEnvironment` option, mirroring the
`WithPullPolicy` (decision #18) and `WithHealthTimeout` (decision #21) seams.
`buildTarget` (shared by `accorda sync` and `accorda plan`) supplies
`compose.WithEnvironment(proj.Environment)`, and `Target.Plan` uses the
stored `t.environment` instead of `desired.Repository`, removing the TODO and
the stand-in. The reconciler does not overwrite `p.Environment` after
`Target.Plan`: the plan is a value type whose `environment` is part of its
identity (§31: "deterministic enough to be hashed and signed"), so the target
is the single source of authority for that field, just as it is for `Commit`
and `CreatedAt`. The reconciler retains its own `r.environment` for the
separate concern of receipt recording (§7). Config validation already
requires `config.Project.Environment` to be non-empty (`config.validateVersion`
returns "environment is required"), so the `config.Load` → `buildTarget`
path always carries a real environment into `WithEnvironment`. An empty
`Plan.Environment` is therefore only reachable via direct target
construction (for example tests that bypass `config.Load`); it is empty
rather than a repository stand-in, which is the behavior the direct-
construction tests exercise.

**Consequence.** `Plan.Environment` now matches the spec's project-level
environment concept, and `accorda plan`/`accorda sync` output and any future
plan hashing reflect the real environment. The change stays within the
established `With*` threading pattern, so no `Target` interface change is
needed and future target drivers receive the environment the same way. The
reconciler's plan/receipt separation is preserved: the plan owns its
`environment`, the receipt owns its `environment`, and they agree because
both derive from the same `config.Project.Environment`.

### 30. `accorda history` and `accorda inspect` read the receipt journal

**Context.** Issue #28 (§11) requires two read-only CLI surfaces that turn
the deployment journal into the spec's history table and per-service inspect
view. The receipt journal (`internal/core/history`, ADR #27) already records
every cycle's commit, outcome (`OutcomeHealthy`/`OutcomeFailed`/
`OutcomeRolledBack`), changed service names, and per-service image + digest,
so both views are reconstructible from receipts alone — no Docker daemon and
no Git fetch is needed.

**Decision.** The two commands are wired as follows:
- `accorda history` (`cmd/accorda/history.go`) reads the local receipt
  journal via `history.NewFileStore(receiptPath(dir))` and prints the §11
  table: one row per cycle with time (UTC `HH:MM`), 7-char commit, result
  glyph (`✓ healthy` / `✗ failed` / `↺ rolled_back`), and the sorted changed
  services. Rows are printed newest first to match the §11 example (most
  recent cycle at the top). The header is always printed, even for an empty
  journal.
- `accorda inspect [commit]` (`cmd/accorda/inspect.go`) reads the same
  journal and prints the per-service §11 view for the deployment at the given
  commit (full SHA or a short prefix; empty means the most recent cycle). For
  each changed service it prints the previous digest (from the most recent
  healthy receipt *before* the inspected one), the deployed digest, the
  recreated flag (true when the service is in the receipt's `Changes`), and
  the health result (`passed` for a healthy receipt, `failed` otherwise). An
  unchanged service (present in `Services` but not in `Changes`) prints a
  single `unchanged` line, matching the §11 example.
- Both commands are read-only and load the project (`config.Load`) only to
  validate it and resolve the journal path keyed by the project directory
  (`receiptPath`). They never construct the target or the source, so they
  work without a running Docker daemon and without Git credentials.
- The existing `shortSHA` helper (status.go) is reused for the commit
  column, and the existing `receiptPath`/`lastHealthyReceipt`-style helpers
  in sync.go are reused for journal access, so no read path is duplicated
  (#DRY).
- `main.go`'s `newHistoryCmd`/`newInspectCmd` stubs are replaced by the
  implemented builders; `logs` and `doctor` remain stubs.

**Consequence.** The §11 history and inspect surfaces are fully delivered
from the receipt journal, with no new core package and no target/source
dependency. The commands share the journal read path with `status`/`diff`/
`sync`, so all CLI surfaces agree on what was deployed. The "previous
digest" column reflects the prior healthy deployment's recorded digest
(the receipt stores the resolved digest per service), and "recreated"
reflects the plan's `Changes` list, so the inspect view is accurate without
re-reading Git or the runtime. An unknown commit or an empty journal is
reported as an error so an operator can distinguish "no such deployment"
from "nothing deployed yet".

### 33. Service logs are an optional target capability

**Context.** Issue #29 (§11) requires `accorda logs` to fetch or stream
service logs through the target driver. The five-method `Target` interface is
the reconciliation lifecycle contract from §12; adding an operational method
to it would force logging onto targets and core test doubles that do not use
it. Docker also returns non-TTY stdout/stderr in a multiplexed stream that a
generic CLI should not need to understand.

**Decision.** `internal/targets` defines the focused `LogTarget` capability
with `Logs(ctx, service, LogOptions, stdout, stderr)`. The Compose target
implements it through the Docker engine API: containers are selected by both
Compose project and service labels, stopped containers are included, snapshot
reads are ordered by container ID, and followed replicas are streamed
concurrently. The adapter detects TTY streams and otherwise decodes Docker's
multiplexed framing with `stdcopy`, keeping Docker-specific transport details
inside `internal/targets/compose`. The CLI requires one service argument and
exposes `--tail` plus `--follow`/`-f`.

**Consequence.** `accorda logs SERVICE` can fetch or follow Compose service
logs without changing the reconciliation interface. Future target drivers can
opt into the same capability without changing core, while drivers that cannot
provide logs continue to implement only `Target`.

### 34. `accorda doctor` reuses lifecycle validation without mutation

**Context.** The §11 and §45 CLI surface includes `accorda doctor` to check
the local installation and configuration. Configuration, Git source, Compose
file, and Docker connectivity validation already exist at their owning package
boundaries; duplicating those rules in the CLI would let diagnostics drift from
the reconciliation lifecycle.

**Decision.** Every project command resolves relative target paths from the
project directory supplied by `--dir` through the shared `buildTarget` helper.
`accorda doctor` loads and validates `accorda.yaml`, validates the configured
Git source without fetching it, constructs the target through that shared path,
and calls its `Validate` method. For the Compose target, that final check parses
the Compose file, pings the Docker engine, and invokes `docker compose version`
to confirm the deployment CLI is available. Results are printed in dependency
order with `PASS` or `FAIL`; a failed check makes the command exit nonzero. A
project-load failure stops dependent checks because no trustworthy source or
target configuration exists. The command never fetches Git or mutates the
target.

**Consequence.** Operators get a single read-only readiness diagnostic whose
rules remain aligned with the production config, source, and target validators.
The command distinguishes successful readiness from actionable failures via
both human-readable output and its process exit status.

### 35. Reconciliation uses durable checkpoints and target-scoped locks

**Context.** Issue #56 and `docs/ACCORDA.md` §47 require restart recovery,
deployment locking, concurrent-commit handling, and retry-safe partial-failure
semantics. A receipt written only after success cannot distinguish a crash
after target mutation from a cycle that never started, and independent
`accorda sync` processes could otherwise race on the same Compose project.

**Decision.** A changed cycle appends an `in_progress` receipt before calling
`Target.Apply`; checkpoint persistence is required before mutation. On restart,
`history.Unfinished` finds an unmatched checkpoint and the reconciler re-plans
against live runtime state, reusing its deployment ID so target operations can
be retried idempotently from observed state. If Git has advanced, the checkpoint is
closed as `interrupted` and the latest commit wins. `WithLocker` guards the
complete fetch-through-verification cycle; the CLI supplies a target-scoped
OS advisory file lock under the Accorda state directory, keyed by the effective
Compose project name rather than its Compose filename so every file that can
mutate the same project is serialized. The kernel releases the lock handle when
an owner exits, avoiding stale-lock/PID-reuse ambiguity.
Before releasing it, the reconciler fetches again and immediately repeats when
a commit arrived in flight. Compose returns a structured `targets.ApplyError`
with completed actions (including all orphans covered by one batched removal)
and the failed action.

**Consequence.** Process crashes leave durable, recoverable intent; retries
converge from observed runtime rather than replaying assumed progress; two CLI
reconciles cannot mutate one target concurrently; and in-flight Git updates are
not lost. The append-only journal retains immutable checkpoints while
`accorda history` collapses a closed checkpoint into its terminal row and shows
only genuinely unfinished checkpoints as `in_progress`.

### 36. MVP command trust boundaries fail closed

**Context.** Issue #83 found that repository caches, explicit credentials,
Git-controlled Compose names, CLI diffs, local config permissions, and the
build toolchain crossed trust boundaries without sufficient validation. A
predictable cache root under the shared temporary directory could also be
pre-seeded by another local user; changing its mode would not change ownership.

**Decision.** Git cache names are the SHA-256 digest of a canonical,
credential-free repository URL under a private user cache; cache paths reject
symlinks, use mode `0700`, and verify the cached `origin` before reuse.
Automatic cache discovery uses the user cache directory, falls back only to
the user config directory, and fails closed if neither provides a non-empty
root; no shared temporary-directory fallback is used. Explicit SSH keys are
read and parsed during source validation, and any failure is fatal rather than
falling back to ambient credentials. Environment values in
`accorda diff` are represented only as `<redacted>`/`<unset>`. Compose service
names must start with an alphanumeric character, contain only Compose-safe
identifier characters, and are passed after `--` when used as CLI operands.
`accorda init` creates `accorda.yaml` exclusively with mode `0600`, while the
loader rejects group/world-readable files containing an HTTPS token or inline
URL password. The
module and CI require Go 1.25.13, and CI runs symbol-level `govulncheck`, failing
on every reachable finding except the two Docker SDK advisories whose
unreachable plugin/AuthZ paths are accepted in decision #25.

**Consequence.** Cached state and ambient credentials can no longer silently
change deployment identity, and an attacker cannot regain cache control by
pre-seeding a shared temporary path. Service accounts without a discoverable
user cache or config directory must provide a valid user environment. Git
content cannot inject Compose flags or expose environment plaintext through
default output, credential files are private, and release builds use a patched
standard library with continuous vulnerability checking. This supersedes
decision #24's Go 1.25.6 CI pin and minimum-toolchain choice.

### 37. Full validation enforces aggregate statement coverage

**Context.** Sonar reports coverage on changed lines, but contributors had no
local coverage floor and CI duplicated only part of `scripts/test.sh`. A test
suite could pass while aggregate coverage regressed, and local validation could
diverge from the pull-request workflow.

**Decision.** `scripts/test.sh` derives aggregate statement coverage from its
Go coverage profile and fails below 85%. `ACCORDA_MIN_COVERAGE` may override
the threshold for local diagnostics, while CI uses the repository default and
invokes the same script instead of maintaining separate format, test, and build
commands. Sonar remains responsible for its distinct new-code coverage gate.

**Consequence.** Contributors and CI receive the same validation and an
immediate aggregate coverage regression signal. The local gate complements,
rather than attempts to reproduce, Sonar's baseline-aware changed-line metric.

### 38. Plaintext files have a callback-scoped runtime lifecycle

**Context.** Issue #23 and `docs/ACCORDA.md` §18 require plaintext secrets to
remain in memory whenever possible and, when a file is unavoidable, to use a
memory-backed runtime location with restrictive permissions and immediate,
verified cleanup. SOPS decryption (issue #22) needs this policy before it can
safely hand plaintext to target renderers.

**Decision.** `internal/secrets.WithPlaintextFile` is the single file-backed
plaintext boundary. It materializes plaintext only under `/run/accorda`,
requires the runtime directory to be private (`0700` or stricter) and not a
symbolic link, creates a cryptographically unpredictable file with mode `0600`,
and exposes its path only for the duration of a callback. A deferred removal
runs after callback success, error, or panic; removal failures are joined with
the callback error and returned instead of being hidden. During panic unwinding,
a `PanicCleanupError` re-panic preserves the exact original panic value and the
cleanup failure. The directory remains for reuse but never contains plaintext
outside the callback lifetime. SOPS continues to own cryptography; this package
owns only plaintext lifecycle and shared redaction.

**Consequence.** Future decryptors and target renderers cannot choose ad hoc
persistent temporary locations. Cleanup behavior and permissions are enforced
centrally and covered by tests before SOPS integration begins.

### 39. Continuous reconciliation is a mode of the existing CLI process

**Context.** Issue #55 and `docs/ACCORDA.md` §45 require Git polling at
`sync.interval`, while operators and existing automation still need a bounded
one-shot sync. A second daemon binary would duplicate configuration and
reconciler wiring without adding a distinct responsibility.

**Decision.** `Reconciler.Run` wraps the existing lifecycle with one immediate
cycle followed by timer-driven cycles until context cancellation. Each cycle
fetches the tracked branch through `Source.Fetch`; an unchanged HEAD produces
no target mutation but still evaluates workload health, emits `health.changed`,
and continues through runtime comparison for drift handling. An unhealthy
unchanged deployment cannot be reported as `SYNCED`.
Failed cycles are reported without stopping the loop so transient dependencies
can recover. `accorda sync` remains one-shot and `accorda sync --watch` invokes
the loop using the project's `sync.interval`. The CLI process installs SIGINT
and SIGTERM cancellation, which is passed through `cmd.Context()` to both modes.

**Consequence.** Accorda ships one binary and one reconciliation implementation.
Interactive users and jobs keep a terminating `sync`, while deployments run
`sync --watch` under an external process supervisor such as systemd or Docker.
The supervisor owns restart and startup policy; Accorda owns polling,
reconciliation, and graceful in-flight cancellation.

### 40. Compose deploys from the Git source's managed checkout

**Context.** The Git adapter already cloned and updated the declared source in
a private cache, but CLI wiring resolved a relative Compose target beside
`accorda.yaml`. Operators therefore had to maintain a second application
checkout, and a newly fetched commit could be planned while `docker compose`
applied a stale local file. This contradicted the Git-driven flow in
`docs/ACCORDA.md` §6, §8, §13, and §25.

**Decision.** `cmd/accorda` resolves repository-relative Compose targets
through `git.Git.CheckoutPath` and runs the target against the same managed
worktree updated by `Source.Fetch`. `source.path` may select a Compose file or
a directory combined with `target.file`; an omitted source path uses the
target path. Checkout path resolution rejects absolute paths and traversal.
The Compose project name remains derived from the operator project directory
so moving the internal cache does not change target identity. Absolute target
paths remain explicit local overrides. `accorda init` records a relative
`--file` in both source and target configuration, while `doctor` checks Docker
without fetching when the managed checkout does not exist yet.

**Consequence.** `accorda.yaml` can live independently from the application
repository, and the first `plan` or `sync` creates the only checkout Accorda
needs. Fetch, desired-state parsing, relative Compose resources, deployment,
and watch updates all use one revision; Core interfaces remain unchanged and
provider-independent.

### 41. Compose interpolation uses declared defaults, not host values

**Context.** Leaving interpolation disabled preserved `${VAR:-default}` as a
literal, which compose-go could not normalize in typed fields such as short
port mappings. Using the process environment instead would make desired state
host-dependent and could leak secrets into comparisons or output.

**Decision.** `internal/targets/compose.ParseWithContext` enables compose-go
interpolation with an explicit empty environment. Compose declaration defaults
therefore resolve (`${PORT:-80}` becomes `80`), while ambient environment values
are never consulted. `env_file` and `label_file` resolution is skipped while
parsing desired state so host-local files and secret values are not required or
ingested; Docker Compose resolves those deployment-time inputs from the managed
checkout when it applies the target. Variables without declared defaults resolve
according to Compose's empty-environment semantics.

**Consequence.** Git-authored defaulted ports, URLs, and other typed Compose
fields parse deterministically on every host without incorporating local
secrets into desired state.

### 42. Receipt baselines are hydrated from deployed Git revisions

**Context.** Deployment receipts intentionally record image references and
digests rather than duplicating the complete desired service model. After an
agent restart, reconstructing `DeployedState` from only those image fields made
the Compose target compare a partial service against the complete Git model.
An unchanged healthy commit was therefore planned as a full recreation.

**Decision.** A reconciler that recovers its previous deployment from receipts
hydrates that image-only baseline before target planning. If the receipt commit
matches the current validated desired state, it reuses that in-memory model. If
the commits differ, it reads and validates desired state at the receipt commit
through the target-agnostic `Source` interface. Failure to recover the complete
historical model fails reconciliation before target mutation instead of
planning from partial data. Explicit `WithPrevious` values remain unchanged;
only receipt-derived baselines require hydration.

**Consequence.** Restarted and one-shot agents treat an unchanged deployment as
a no-op, while changed commits still receive an accurate full-model comparison.
Receipts remain compact and image/digest-focused, and Core gains no Compose- or
provider-specific dependency.

### 43. Managed Compose inputs are isolated and planned with deployment semantics

**Context.** A repository-URL-only cache allowed independent Accorda projects
to mutate the same checkout, so different branches could race between desired
state parsing and Compose apply. Compose parsing also lacked the file's project
directory, discarded `env_file` and `label_file` declarations, and used
different interpolation inputs than the Compose CLI. Those gaps could apply a
different revision or configuration than the plan. `doctor` additionally
treated any missing managed file as an unfetched checkout.

**Decision.** CLI-created Git sources namespace the credential-free repository
cache identity by the absolute operator project directory. Compose loads with
the managed file's filename and project directory, retains ordered external
file declarations, and includes SHA-256 digests for referenced files present
in the Git revision without retaining their contents. Planning and Compose
execution share a controlled Docker-operational environment; arbitrary host
application variables are excluded and implicit `.env` interpolation is
disabled. Before Compose rollback, sources that implement the optional
`RevisionMaterializer` capability activate the previous Git revision so its
file-backed inputs are restored. `doctor` defers missing-file validation only
when the managed Git checkout itself does not exist.

**Consequence.** Separate project files can safely deploy different branches
of one repository, file-relative Compose features resolve against the reviewed
snapshot, and file-backed or interpolated changes cannot bypass plan identity.
Rollback restores the Compose document and tracked referenced files from the
same commit, while local untracked secret values remain outside desired state
and history.

### 44. Read-only commands serialize historical worktree reads with the deployment lock

**Context.** Reading desired state at a historical commit (used by `accorda
plan` and `accorda diff` to reconstruct the deployed baseline) temporarily
checks out that revision into the shared managed worktree so Compose's
`extends`/`includes` and relative resources resolve against the reviewed
snapshot. A concurrent `sync` that reads the on-disk Compose file during that
window could apply the wrong revision. `plan` and `diff` are otherwise
read-only and previously took no lock.

**Decision.** `accorda plan` and `accorda diff` acquire the same target-scoped
deployment lock that `sync` holds for the duration of their source reads and
plan computation. The lock is released before the command returns, so the
read-only commands serialize only their worktree-mutating section against a
concurrent deployment, not the whole process.

**Consequence.** A `plan` or `diff` running while `sync` deploys waits for the
deployment to finish (or the lock to be released) before reading historical
desired state, eliminating the wrong-revision window. The lock is advisory and
per-target, so independent projects remain unaffected.

### 45. Per-service env overrides from accorda.yaml

**Context.** Docker Compose resolves `env_file` paths relative to the Compose
file's directory, which for Accorda is the managed Git checkout. Gitignored
`.env` files (e.g. `apps/*/.env`) are absent from that checkout, so
`docker compose up -d` fails with "env file not found". The operator can stage
them manually (the managed checkout path is surfaced by `doctor`/`status`), but
that is fragile and not declarative. The existing `secrets:` field is the
spec-intended home for SOPS-encrypted files committed to Git (§17), but the
decryption/staging pipeline is not yet implemented, and it does not support
plaintext key/value overrides.

**Decision.** Add a per-service `services` section under `target` in
`accorda.yaml` so operators can declare environment inputs for specific
services without relying on gitignored `env_file` files:

```yaml
target:
  type: compose
  file: docker-compose.yml
  services:
    aura-api:
      env:
        AURA_API_ROOT_PATH: /api/ai
        LLM_SERVICE_URL: http://llm-service:8001
      env_files:
        - type: file
          path: /Users/akurilo/.config/accorda/aura-api.env
    llm-service:
      env_files:
        - /Users/akurilo/.config/accorda/llm-service.env
```

Each service entry accepts two input kinds, combinable:

- **Inline key/value pairs** under `env:` (`KEY: value`) — merged into the
  service's `environment:` at deploy time. These override any value declared
  in the Compose file's `environment:` for the same key and add keys not
  present.
- **`env_files:`** — a list of local `.env` files. Each entry is either a
  plain string path (short form) or a mapping with `type: file` and `path:`
  (long form). Entries are read at deploy time and merged into the service's
  `environment:` with the same override-and-add semantics. Paths are
  operator-local (absolute or relative to the `accorda.yaml` directory); they
  are never committed to Git and their contents are never stored in desired
  state, receipts, or history.

The merge applies **at deploy time only**, by rendering a deploy Compose file
that carries the resolved `environment:` for each overridden service before
`docker compose up -d` runs. Desired-state parsing and hashing remain Git-only:
the overrides do not enter `state.Service.Env` used by `plan`/`diff`/hashing,
so they cannot cause spurious drift or leak secret values into comparison
output. Precedence on key collision, from lowest to highest: Compose
`environment:`, `env_files` entries (in list order), inline `env:` values.

The existing `env_file:` mechanism in Compose is preserved as-is: if the
Compose file declares `env_file:` and the files exist in the checkout (e.g.
committed to Git), Accorda does not touch them. If the files are absent
(gitignored), `docker compose up -d` fails — the developer is responsible for
providing them in GitOps. The `target.services` overrides are purely additive
environment inputs that expand on top of whatever the Compose file already
declares; they do not stage or replace `env_file:` files. SOPS support (§17)
remains a future enhancement and is not replaced by this decision.

**Consequence.** Operators can remove gitignored `env_file:` declarations from
the Compose file and declare per-service env inputs declaratively in
`accorda.yaml`, with either inline values or local file references. The Compose
file stays clean and portable for non-Accorda use; Accorda layers the
operator-specific environment on top at deploy time. Secret values in
`from_file` stay on the operator's machine and are never persisted by Accorda.
The override is deployment-scoped, so it does not pollute Git-authored desired
state, receipts, or drift comparisons.


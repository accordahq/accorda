# Accorda OSS — Architecture Decisions

This file records the durable architecture and design decisions baked into the
Accorda OSS codebase, with rationale and consequences, so future work stays
aligned. It does **not** replace the product spec.

- `docs/ACCORDA.md` is the authoritative, immutable product specification.
- Each entry is terse: context, decision, consequence, and the files it lives in.
- Shared principles (interface dependence, determinism, DRY) are stated once and
  referenced by number, not restated. Append new entries with the next number.

---

## Foundations

### 1. Module layout and minimal dependencies

`src/accorda/` is a single Go module; docs and agent files live at the repo root.
Five direct dependencies, each confined to one subsystem and never imported by
core:

| Library | Version | Used by | Purpose |
| --- | --- | --- | --- |
| `spf13/cobra` | v1.10.2 | `cmd/accorda` | CLI command tree, flags, help. |
| `gopkg.in/yaml.v3` | v3.0.1 | `internal/config` | Strict `accorda.yaml` decoding. |
| `go-git/go-git/v6` | v6.0.0-alpha.5 | `internal/sources/git` | Pure-Go Git clone/fetch/checkout/reads. |
| `compose-spec/compose-go/v2` | v2.14.0 | `internal/targets/compose` | Compose parsing → `state.Service`. |
| `docker/docker` | v28.5.2+incompatible | `internal/targets/compose` | Engine API for runtime-state reads. |

Everything else in `go.mod` is transitive. Accorda delegates Git transport and
Compose parsing to these libraries to stay focused on reconciliation. A new
dependency needs justification and must not leak provider assumptions into core.

### 2. `docs/ACCORDA.md` is authoritative and immutable

Do not modify the spec. Record divergence in this file, `README.md`, or a
package `doc.go`. Doc comments cite spec sections (`// (docs/ACCORDA.md §8)`).

### 3. Target- and source-agnostic core

`internal/core/*` imports only `state`, `plan`, `health`; drivers live under
`internal/targets/*` and `internal/sources/*`. Core depends on the `Target`
(`Validate`, `Current`, `Plan`, `Apply`, `Health`) and `Source` (`Validate`,
`Fetch`, `Desired`) interfaces only, so adding a provider never changes core.

### 4. Compile-time interface checks

Every interface package ships a `Stub` (no-op `ErrNotImplemented`) and
`var _ Interface = (*Stub)(nil)`; drivers assert the same. Missing or renamed
methods fail the build, so core and tests can reference a `Target`/`Source`
before a real driver exists.

### 5. Value types with deep-copy `Clone`

`state.*`, `plan.Plan`, and `health.Health` are value types with `Clone()` that
preserve nil-vs-empty (`cloneServices`, `cloneStringMap`, `clonePorts`,
`cloneVolumes`) so callers can snapshot safely. New reference-type fields must be
covered by `Clone`.

### 6. Image-centric service model

`state.Service` is image-centric; a service without an image fails `Validate`.
The Compose parser requires an `image` per service; the git source populates
`Image`+`Env`; the Compose driver populates the full normalized set. Desired
state is concrete and deployable; build-only services are a known gap.

### 7. Deterministic comparison and plan output

`state.Compare` sorts `Reasons`; `plan.Plan` includes explicit `Noop` actions so
the full plan is deterministic and hashable (§31), carrying optional
`Security`/`Policy`. Any slice field derived from a map must be sorted at the
normalization boundary.

### 8. DRY, SOLID, and cognitive complexity

New code must avoid duplicated literals/logic (named constants, shared helpers,
table-driven tests), keep single-responsibility package boundaries with interface
dependence, and keep functions and test functions below 15 cognitive complexity.
Linters (SonarQube, gopls) surface violations; see `AGENTS.md` for enforcement.

---

## Configuration

### 9. Strict config loader

`internal/config` decodes with `KnownFields(true)`, applies defaults for zero
fields, and returns field-oriented errors. `Secrets` accepts a file-path list or
`{provider: sops}` via a custom `UnmarshalYAML`. New config fields require an
explicit validator update.

### 10. `accorda init` writes the unified `accorda.yaml`

`init` writes `accorda.yaml` via `config.MarshalProject` (the inverse of `Parse`),
applying defaults and validation first so the file round-trips through the strict
loader and is immediately consumable by `sync`. The old dotenv format is gone;
`.gitignore` ignores `accorda.yaml` (issue #68).

---

## Adapters — Git source

### 11. Git source uses go-git

`internal/sources/git` uses go-git instead of the system `git` CLI. It clones,
fetches, checks out, and reads files at commits via the API; auth is
`ssh.PublicKeys`, `http.BasicAuth`, or ambient (agent/unauthenticated). No runtime
`git` dependency; typed commit/tree objects replace parsed CLI output.

### 12. Secrets are never logged

`Auth.Token`/`Auth.Key` are secrets. `Source.URL` stays clean (used in errors and
`DesiredState.Repository`); the credential-bearing `remoteURL` is used only by
clone/fetch and never logged. `redactURL` strips userinfo before any URL surfaces;
errors name fields, never values. Tests assert no token leak.

### 51. Git source has remote and in-place modes selected by `url` vs `path`

Issue #95: the git source (`internal/sources/git`) has two modes rather than two
adapters. Remote mode (`source.url`) clones/fetches into the private cache and
checks out the configured branch, as before. In-place mode (`source.path`, no
`url`) binds directly to a user-owned local git worktree without cloning; `Fetch`
reads the worktree's current `HEAD` and never mutates it. Both modes share one
worktree-read core, so the commit-anchored fast path, receipts, and `diff`/`plan`/
`history`/`inspect` work unchanged. Historical desired state (used by `diff`,
`plan`, and rollback-baseline reads) is privately materialized from the commit's
tree via go-git so Compose resolves tracked `extends`/`include` files without
rewriting the operator's checkout. Applying an automatic in-place rollback is
unsupported because `Materialize` would rewrite the user-owned worktree. Config:
exactly one of `url` or `path` selects the mode; with
`url`, `path` is an optional repo-relative compose path. Files: `internal/sources/git/git.go`,
`internal/config/config.go` (`validateSource`), `cmd/accorda/wire.go` (`buildSource`).

### 52. Targets load desired state from source-owned revision views

Issue #101: `sources.Source` exposes `Validate`, `Fetch`, and `Revision`; a
revision carries commit metadata, a constrained filesystem root, tracked-file
digests, and callback-scoped cleanup. Current reads use the active worktree;
historical reads materialize the generic Git tree privately, including in-place
mode. `targets.Target.Desired` is mandatory: Compose resolves/parses its own
artifact and image builds services from config. This supersedes #24/#32's
`Source.Desired`/`DesiredProvider` flow and removes Git's Compose dependency.
Remote rollback may still activate a revision through `RevisionMaterializer`;
in-place rollback remains unsupported. Files: `internal/sources`,
`internal/sources/git`, `internal/targets`, `internal/core/reconcile`,
`cmd/accorda/wire.go`.

### 53. A project reconciles multiple targets from one source

Issue #103: one project can declare several targets (`targets:` list) that
reconcile from a single `source`. `config.Project.Targets` (plural) replaces the
singular `Target`; the legacy `target:` scalar is promoted into a one-element
list by `ApplyDefaults` (`NormalizedTargets` is the single read path). `ValidateTargets`
requires at least one target and unique target identity per project. `Targets` is a
plain `[]Target` slice decoded by the strict loader, so unknown fields in a
`targets:` entry are rejected like every other section.

Core: `internal/core/reconcile` adds `Project`, a runner that fans one source out
to several targets sequentially (so the shared managed checkout is never mutated
concurrently, unlike the concurrent `Ensemble`), each target keeping its own
receipt store and target-scoped lock. `EnsembleMember` now accepts a `CycleRunner`
(a single target or a `Project`) so single- and multi-target members fan out
uniformly. `Reconciler.WithTarget` labels its `StateTransition` events so output
stays attributable. The `targets.TargetContext` carries the specific `Target`
being built; `BuildTarget` falls back to `Project.Target` when it is empty.

Wire/CLI: `buildEnsembleMembers`/`buildTargetReconciler` build one target per
declared entry. `targetReceiptPath` keeps the single-target path byte-identical
and scopes each target's journal by its identity in a multi-target project.
`diff`, `plan`, `status`, `history`, `inspect`, `logs`, and `doctor` iterate the
project's targets and prefix per-target output with the target identity when the
project has more than one. Rollback operates per target independently. This
builds on #52's prerequisite boundary (sources return revisions, targets own
desired-state loading) and #42/#49's ensemble (independent projects) without
sharing state between targets. Files: `internal/config`, `internal/targets`,
`internal/core/reconcile` (`project.go`, `ensemble.go`), `cmd/accorda` (`wire.go`,
`sync.go`, `diff.go`, `plan.go`, `status.go`, `history.go`, `inspect.go`,
`logs.go`, `doctor.go`).

Target naming: a named target drives both the Compose project name and the
image container name. For a named Compose target the Compose project name is
`base+"-"+targetName` (for example `aura-qa`), so several Compose targets in one
project deploy into isolated Compose namespaces and do not collide on Docker
labels or `--remove-orphans`; the deployment lock keys off the same
disambiguated identity. For an image target the container is named after the
target (for example `edge-agent`) and carries an `accorda.image.project`
label with the project (group) name, so the project is the logical group and
the target name identifies the specific container instance. A single unnamed
target preserves the legacy derivation (project name for Compose, project
directory basename for image).

---

## Adapters — Compose target

### 13. Compose parsing via compose-go

`internal/targets/compose` uses the compose-go loader into `types.Project`, then
normalizes the modeled subset (`image`, `command`, `env`, `ports`, `volumes`,
`networks`, `labels`, `healthcheck`, `depends_on`) into `state.Service`. The
loader handles YAML, interpolation, extends, and normalization; validation
requires an image per service. New fields are localized to `parse.go`.

### 14. Compose reads runtime state via the Docker engine SDK

Issue #9 (§5.3): `Target.Current` lists containers tagged with the
`com.docker.compose.project` label (including stopped, so drift is observable)
and inspects each, mapping via `com.docker.compose.service` into
`state.RuntimeState`. The SDK is reached through a local `dockerClient` seam
(Ping, ContainerList, ContainerInspect) so core never imports it. The project
name is the Compose directory basename (overridable via `WithProjectName`).
Health is part of runtime state, so per-container inspect is accepted for the
MVP; `ContainerList.Summary` lacks health and moving health out of runtime state
would need a spec change. `Apply`/`Health` were `ErrNotImplemented` until #16/#19.

### 15. Compose plan delegates to `plan.DriftActions`

`Target.Plan` reads runtime via `Current` and delegates the diff to the
target-agnostic `plan.DriftActions(desired, nil, runtime)`, wrapping actions in a
`plan.Plan` with `Environment`=repository, `Commit`=commit (DeploymentID left for
the loop, §7). `DriftActions` iterates services in sorted order for determinism.
`Plan` gains `Changed()` and `String()` for CLI output.

### 16. Compose Apply shells out to `docker compose`

Issue #11 (§9): `Target.Apply` maps plan actions to `docker compose` subcommands
through a `composeRunner` seam (`Run(ctx, args...)`): create/recreate/start →
`up -d <svc>`; remove → `up -d --remove-orphans`; pull → `pull <svc>`; stop →
`stop <svc>`; noop → skipped. Orphans use `--remove-orphans` rather than `rm`
because the orphan name is no longer in the file. `WithRunner` injects a fake for
tests. Partial failures name the failing service and action.

### 17. Image pull policies select pull actions in Plan

Issue #12 (§9): `Target` carries a `pullPolicy` (default `config.PullChanged`,
set via `WithPullPolicy`, supplied by `images.pull`). `Plan` prepends pull actions
selected by `selectPulls` before the drift actions. Semantics: `changed` pulls
only create/recreate services; `missing` pulls only images absent locally (via a
new `ImageList` method on the seam); `always` pulls all; `never` none. Actions are
ordered by service name for determinism, so `Apply` needs no policy awareness.

### 18. Service hashing for recreate decisions

Issue #13 (§10): `state.Service.Hash()` canonicalizes reconciliation-relevant
fields (unordered collections sorted at the boundary; command/healthcheck test
kept verbatim) into a SHA-256 hex digest. `state.Compare` adds a desired-vs-
deployed hash check so a changed command/ports/volumes/networks/labels/
healthcheck/depends_on flags OUT_OF_SYNC. `plan.DriftActions` emits
`ActionRecreate` when hashes differ despite the same image, which required
`Target.Plan` to gain a `deployed *state.DeployedState` parameter (previously
`nil`, which also conflated `ActionCreate`/`ActionStart`). New service fields
must be added to `canonical()`.

### 19. Compose health verification maps Docker healthcheck status

Issue #15 (§19): `Target.Health` maps each service's healthcheck status
(healthy/starting/empty→Unknown/else Unhealthy), polling `Current` every 2s until
no service is starting or the `healthTimeout` (default 120s, `WithHealthTimeout`)
elapses. A still-starting service at timeout is reported unhealthy naming the
timeout. No-healthcheck services are immediately unknown and don't block. Errors
only on runtime read failure; unhealthy is reported in the value, not an error.
This makes the reconciler's #25 health bypass dead for Compose.

### 20. Compose deploys from the Git source's managed checkout

§6, §8, §13, §25: the CLI resolved relative Compose targets beside `accorda.yaml`,
so operators maintained a second checkout and a freshly fetched commit could be
planned while `docker compose` applied a stale file. `cmd/accorda` resolves
repository-relative targets through `git.Git.CheckoutPath` and runs the target
against the worktree updated by `Source.Fetch`. `source.path` may select a file or
a directory combined with `target.file`; an omitted source path uses the target
path. Resolution rejects absolute paths and traversal. The project name stays the
operator project directory so moving the cache doesn't change identity. Absolute
target paths remain explicit overrides. `init` records a relative `--file`;
`doctor` checks Docker without fetching when no checkout exists.

### 21. Compose interpolation uses declared defaults, not host values

`internal/targets/compose.ParseWithContext` enables compose-go interpolation with
an explicit **empty** environment, so `${PORT:-80}` resolves to `80` while ambient
host values are never consulted (which would make desired state host-dependent and
could leak secrets). `env_file`/`label_file` resolution is skipped at parse time;
Compose resolves those deployment-time inputs from the managed checkout on apply.

### 22. Managed Compose inputs are isolated and planned with deployment semantics

CLI-created Git sources namespace the credential-free cache identity by the
absolute operator project directory, so independent projects don't race on one
checkout. Compose loads with the file's name/project dir, retains ordered external
file declarations, and stores SHA-256 digests of referenced files in the revision
(not their contents). Planning and Compose execution share a controlled
Docker-operational environment (host vars excluded, implicit `.env` disabled).
Before rollback, sources implementing the `RevisionMaterializer` capability
activate the previous revision so its file-backed inputs are restored. `doctor`
defers missing-file validation only when the checkout itself doesn't exist.

### 23. Per-service env overrides from `accorda.yaml`

Compose resolves `env_file` relative to the managed checkout, so gitignored `.env`
files break `up`. Add `target.services.<name>` with two combinable inputs, merged
**at deploy time only** into a deploy Compose file (mode 0600, removed after
`Apply`): inline `env:` pairs and `env_files:` (short path string or
`{type: file, path:}`). Precedence (low→high): Compose `environment:`, then
`env_files` in order, then inline `env:`. A missing/unreadable file is a hard
error. Overrides never enter `state.Service.Env` (plan/diff/hash stay Git-only, so
no spurious drift or secret leakage), are never committed or persisted. Existing
committed `env_file:` is untouched; SOPS (§17) remains future work.

### 24. Raw single-image target (`target.type: image`)

Issue #94: add `image` as a sibling of `compose` (not a `docker` rename), in
`internal/targets/image`, implementing `Target` plus optional `DesiredProvider`
and `LogTarget`. Desired state is **config-driven** from
`target.image`/`target.env`/`target.ports`; the Git source still supplies commit
metadata so receipts/history stay anchored. The reconcile loop consults
`targets.DesiredProvider` after `source.Desired` and replaces services with the
config-derived model (preserving source identifying fields); non-implementing
targets keep source-driven behavior. Shared Docker operations moved to
`internal/docker` (client/log seams, runtime mapping, digest resolution, health
mapping, pull policy) consumed by both Docker targets; Compose-label logic stays
in `targets/compose`. `buildTarget` (in `cmd/accorda/wire.go`) dispatches by type;
`status`/`doctor` use
`docker.HealthFromRuntime` and a `validateEnvironmentTarget` capability. Apply
runs `docker run -d` via a `Runner` seam, removing an existing container first.
`target.pull` is not added (the global `images.pull` covers it). `env` enters
desired state because it is Git-authored config, unlike the Compose
`ServiceOverride`. Service name = project name or the project-directory basename.

### 54. Accorda-owned containers and safe stale reclaim

Issue context: a project rename collides when Compose services declare explicit
`container_name`, because those names are daemon-global — the old project's
containers keep the names and the new project's `docker compose up` fails with
"container name already in use". Accorda must resolve this itself, but must
**never delete a container it does not own** and must preserve the renamed
service's data.

Decision:

- Every container Accorda deploys is stamped with `accorda.managed=true` via the
  rendered deploy Compose file (the always-rendered `.accorda-deploy.yml`), so
  the label travels with the container regardless of the Compose project Accorda
  later manages it under. This is the durable ownership proof
  (`internal/targets/compose/deploy.go`, `docker.go`).
- Each deployed container also carries `accorda.deployment_id=<dep>` (when a
  deployment ID is assigned), linking the live container back to its receipt
  journal entry for ops traceability. The label is informational only: it never
  enters desired state, hashing, or drift comparison.
- Before `docker compose up`, `Apply` scans the daemon for containers that claim
  an explicit `container_name` a service is about to create but that belong to a
  **different** project, and force-removes them **only** when they carry the
  `accorda.managed` label. A container without the label is never touched, even
  on a name collision (`internal/targets/compose/reclaim.go`).
- Before removing a stale owned container, its named volumes are migrated to the
  current project's volume namespace (via a throwaway busybox
  `cp -a /from/. /to/`), so a renamed service keeps its data. Bind mounts and
  non-project volumes are left untouched. A failed volume migration aborts the
  reclaim so data is not silently dropped.
- Reclaim uses a plain `docker` CLI runner seam (`dockerCli`), mirroring the
  image target's `Runner`, so the SDK `Client` interface is unchanged.

Consequence: Accorda autonomously heals name collisions from its own earlier
deployments and preserves volume data across renames, while the ownership label
is the hard guarantee that it never deletes a container it did not create.
Containers deployed before this change (no label) require a one-time manual
teardown; the label protects all future deployments. Files:
`internal/targets/compose/{reclaim.go,reclaim_test.go,deploy.go,docker.go}`.

---

## Core — reconciliation

### 25. Reconciliation lifecycle state machine and event bus

Issue #14 (§6): `internal/core/events` has an in-memory `Bus` (Publish/Subscribe,
synchronous, idempotent unsubscribe). `internal/core/reconcile` has a `Reconciler`
walking `DETECTED → FETCHING → VALIDATING → PLANNING → PULLING → DEPLOYING →
VERIFYING → HEALTHY → SYNCED`, emitting `EventStateTransition` plus the §21
deployment events. Failures go to `FAILED`; with `WithPrevious`, a failed
apply/verify re-plans and re-applies the previous services and emits
`deployment.rolled_back`. `Target.Health` was `ErrNotImplemented`; the reconciler
treated that sentinel as healthy (a documented, temporary bypass removed by #19).
Core depends only on the `Source`/`Target` interfaces.

### 26. Drift repair is a reconciler-level policy

Issue #16 (§5.3): the `Reconciler` carries a `DriftPolicy` (`report` default,
`repair`, `disabled`, via `WithDriftPolicy`). The sync-phase drift branch emits
`DriftDetected` unless `disabled`; under `repair` it re-plans and re-applies the
desired state (reusing the `Plan`/`Apply` path, not a bespoke drift path) and
emits `DriftReconciled`. A failed repair is silent and leaves `DRIFTED`. The
`reconcile.drift` config value isn't yet threaded because `sync` was a stub; the
seam mirrors `WithHealthTimeout`/`WithPullPolicy`.

### 27. Deployment receipts are an append-only JSON-lines journal

Issue #18 (§7): add `Digest` to `state.RuntimeService` (from `ImageInspect`
`RepoDigests[0]`, best-effort). Record receipts via the `history.Store` interface;
the default `history.FileStore` writes an append-only, fsynced JSON-lines journal
(std-lib only). The loop records a receipt at the end of a changed, SYNCED
deployment (`recordReceipt`), gated on the plan actually changing the target; it
assigns `dep_<hex>` when the plan leaves it empty. `sync` stores under
`$XDG_STATE_HOME/accorda/receipts/<project>.jsonl`, keyed by project dir. Receipt
recording is best-effort — a store failure is not a deployment failure.
`accorda history` (#38) and rollback (#29) build on it.

### 28. History records result and changes for healthy and failed cycles

Issue #19 (§11): extend `history.Receipt` with `Result` (`OutcomeHealthy`/
`OutcomeFailed`) and `Changes` (sorted unique changed services). The loop records
a healthy receipt on changed/SYNCED (with runtime digests) and a failed receipt on
deploy or health-verification failure (no services). Recording stays best-effort;
the healthy gate (a no-op records nothing) is preserved, and a failed receipt is
always recorded.

### 29. Rollback restores the last known-healthy deployment

Issue #17 (§20): `internal/core/history` gains `OutcomeRolledBack`; `reconcile.Result`
gains `RolledBackTo`; `sync` prints `rollback: restored to commit <sha>`. The loop
gains a `desiredApplier` capability (`ApplyDesired(ctx, desired)`); a target that
implements it (Compose) rolls back by applying the previous desired state directly
so the on-disk artifact reflects the restored services before `up`. Rollback
restores the **full** previous model by reading desired state at the previous
commit from the source (falling back to image-only receipt services if unreadable —
the §20 "where safely possible" qualifier). `sync` supplies the previous deployment
via `previousFromHistory(store)` (in `cmd/accorda/wire.go`, most recent
`OutcomeHealthy`); with no prior
healthy deployment the failure stands.

### 30. Reconciliation uses durable checkpoints and target-scoped locks

Issue #56 (§47): a changed cycle appends an `in_progress` receipt **before**
`Target.Apply` (persistence is required before mutation). On restart,
`history.Unfinished` finds an unmatched checkpoint; the reconciler re-plans against
live runtime, reusing its deployment ID for idempotent retry. If Git has advanced,
the checkpoint closes as `interrupted` and the latest commit wins. `WithLocker`
guards the full cycle; the CLI supplies a target-scoped OS advisory file lock under
the state dir, keyed by effective Compose project name. Before releasing the lock
the reconciler re-fetches and repeats if a commit arrived in flight. Compose
returns a structured `targets.ApplyError` (completed actions, one batched orphan
removal, failed action).

### 31. Continuous reconciliation is a mode of the existing CLI process

Issue #55 (§45): `Reconciler.Run` wraps the lifecycle with one immediate cycle then
timer-driven cycles until context cancellation. Each cycle fetches the tracked
branch; an unchanged HEAD produces no target mutation but still evaluates workload
health, emits `health.changed`, and continues through runtime comparison for drift —
an unhealthy unchanged deployment can't be reported `SYNCED`. Failed cycles are
reported without stopping. `sync` stays one-shot; `sync --watch` runs the loop at
`sync.interval`. The CLI installs SIGINT/SIGTERM cancellation passed through
`cmd.Context()`.

### 32. Receipt baselines are hydrated from deployed Git revisions

Reconstructing `DeployedState` from image-only receipts made the target compare a
partial service against the full Git model, so an unchanged healthy commit was
planned as a full recreation. A reconciler that recovers its previous deployment
from receipts hydrates the image-only baseline: if the receipt commit matches
current desired state, reuse it; else read and validate desired state at the
receipt commit via the `Source`. Failure to recover the full model fails
reconciliation before mutation. Explicit `WithPrevious` values are unchanged; only
receipt-derived baselines hydrate.

### 33. Service logs are an optional target capability

Issue #29 (§11): `internal/targets` defines `LogTarget` with
`Logs(ctx, service, LogOptions, stdout, stderr)`. Compose implements it via the
engine API (project+service labels, stopped containers included, snapshot reads
ordered by container ID, replicas streamed concurrently), decoding Docker's
multiplexed framing with `stdcopy` (TTY-aware) inside the adapter. The CLI requires
one service and exposes `--tail` and `--follow`/`-f`.

---

## CLI

### 34. `accorda sync` wires the reconciler to the Git source and Compose target

Issue #27 (§11): `newSyncCmd` loads the project, builds the Git source and Compose
target (threading `WithPullPolicy`, `WithHealthTimeout`), and drives one
`reconcile.New(...).WithDriftPolicy(...)` cycle, printing the terminal phase and
the `state.Comparison`. A `driftPolicy` helper (in `cmd/accorda/wire.go`) maps config to `DriftPolicy`
(unknown → report). Only Compose is wired; other target types error. `sync` is
removed from the stubs. One pass, not the §11 loop (loop + history persistence are
future work).

### 35. `accorda status` is a read-only snapshot

Issue #25 (§11): `status.go` composes existing seams without mutation: `Fetch`+
`Desired` for HEAD/branch/repo (degrading to "unavailable"), `git.RedactURL` so
credentials never echo, `lastHealthyReceipt(store)` (in `cmd/accorda/wire.go`) for deployed commit/time,
`Target.Current` + `compose.HealthFromRuntime` for runtime/health. The sync label
(`SYNCED`/`OUT_OF_SYNC`/`UNKNOWN`) is derived from HEAD vs deployed commit before
any target read; the runtime label from per-service health. Output is a sorted
tabular report matching §11.

### 36. `accorda diff` and `accorda plan` are read-only CLI projections

Issue #26 (§11): both build the "deployed" side as the desired state re-read from
Git at the last healthy deployment's commit (`lastHealthyReceipt` (in `cmd/accorda/wire.go`) +
`deployedAtCommit`), since receipts store only image/digest. `diff` prints only
differing services/fields as `deployed:`/`desired:` pairs (YAML-like, sorted,
daemon-free). `plan` fetches desired, constructs the target, and calls `Target.Plan`
with the same full-model baseline, printing `plan.Plan.String()`. Neither mutates.
`diff` needs no daemon; `plan` reads runtime but never applies.

### 37. Plan environment is threaded from the project

Issue #52 (§25, §31): `Plan.Environment` must be the project environment, not the
Git-desired repository stand-in (§5.1 lists no env for desired state). The Compose
target gains `WithEnvironment`, `buildTarget` (in `cmd/accorda/wire.go`) supplies
`proj.Environment`, and
`Target.Plan` uses the stored value. The reconciler doesn't overwrite it (the plan
owns its environment identity). Config validation already requires a non-empty
environment. Empty `Plan.Environment` is reachable only via direct target
construction (tests).

### 38. `accorda history` and `accorda inspect` read the receipt journal

Issue #28 (§11): both are read-only, daemon-free, and Git-free. `history` prints
the §11 table (UTC `HH:MM`, 7-char commit, `✓ healthy`/`✗ failed`/`↺ rolled_back`,
sorted changed services), newest first, header always printed. `inspect [commit]`
prints per-service previous digest (from the most recent healthy receipt before
the inspected one), deployed digest, recreated flag, and health result; unchanged
services print `unchanged`. Both load the project only to validate and resolve
`receiptPath` (in `cmd/accorda/wire.go`). An unknown commit or an empty journal is an error.

### 39. `accorda doctor` reuses lifecycle validation without mutation

`doctor` loads and validates `accorda.yaml`, validates the Git source without
fetching, constructs the target via the shared `buildTarget` (in `cmd/accorda/wire.go`), and calls `Validate`
(for Compose: parses the file, pings the engine, runs `docker compose version`).
Results print in dependency order with `PASS`/`FAIL`; any failure exits nonzero. A
project-load failure stops dependent checks. It never fetches Git or mutates the
target.

### 40. Read-only commands serialize historical reads with the deployment lock

`accorda plan` and `diff` temporarily check out a historical revision into the
shared managed worktree (so `extends`/`includes`/relative resources resolve), which
a concurrent `sync` reading the on-disk file could race. Both acquire the same
target-scoped deployment lock for their source-read + plan section, releasing
before return. Advisory and per-target, so independent projects are unaffected.

---

## Notifications

### 41. Webhook notifications are a best-effort event bus consumer

Issue #21 (§21): `internal/notifications/webhook` implements a `Consumer` that
subscribes and POSTs each event as a JSON envelope `{type, timestamp, payload}`.
Delivery is **asynchronous** (one goroutine per event) so retries never block the
synchronous bus; bounded to `maxConcurrentDeliveries` (16) — beyond that, events
are dropped and reported, not queued. Bounded exponential backoff (500ms → 10s
cap, `max_retries` default 3) retries only transport errors and 5xx/429, not 4xx.
A dedicated `http.Client` rejects redirects (no pivot to an internal address) and
enforces the per-request `timeout`. An optional shared `secret` produces an
`X-Accorda-Signature` HMAC-SHA256 header (receiver compares with
`crypto/hmac.Equal`). Payloads are redacted: env maps → `<redacted>`, error strings
URL-redacted (no inline `user:password@`). Config is `notifications.webhooks`
(`url`, `max_retries`, `timeout`, `secret`) gated by `notifications.webhook: true`;
a mismatched flag/URL is a config error. `sync` subscribes it when enabled. Events
may deliver out of order; other channels remain future work.

---

## Ensemble (multi-project)

### 42. One agent reconciles multiple projects concurrently

Issue #57 (§49): `accorda.yaml` accepts a top-level `projects:` list of named
entries; its absence selects the single-project shape (output and state paths
byte-identical). `config.ValidateEnsemble` rejects empty, unnamed, and
case-insensitively colliding names. `loadProjects` normalizes either shape and
loops members with `p.Name` (the `""` sentinel only for a genuine single-project
doc). `reconcile.Ensemble` fans cycles to member `Reconciler`s concurrently (one
goroutine each), a slow member not blocking others. Each member owns its
source/target/bus/receipts/lock; `buildSource` (in `cmd/accorda/wire.go`) appends
the member name to the cache namespace; `buildTarget` sets the Compose project name
to the member name (so same-named dirs don't collide on `--remove-orphans`);
`receiptPath` scopes by member name. All CLI commands iterate members and prefix
output with the name.

### 43. Ensemble globals are shared at the document root

The ensemble root owns `version`, `sync`, `images`, `reconcile`, `health` beside
`projects:`. `version` and `sync.interval` are global and **not overridable** (a
per-member block is rejected); `images`/`reconcile`/`health` are overridable
defaults (`parseEnsemble` decodes pointer fields so "unset" is distinguishable,
then resolves each member inheriting the root unless overridden). The single-
project shape is unchanged. The `interval` divergence check is removed (the
interval can't diverge). Hoisting these fields is a deliberate, breaking schema
change.

---

## Quality, security, and tooling

### 44. Dependency licenses

Accorda is Apache-2.0; the dependency tree must stay permissively licensed (no
copyleft). `THIRD_PARTY_LICENSES.md` groups licenses and verbatim texts with
component copyright/NOTICE, generated from the real Go dependency graph (see
`docs/licensing.md`); a CI license allowlist fails the build on disallowed
licenses. `go-digest` (indirect) ships a CC-BY-SA-4.0 `LICENSE.docs` that applies
only to non-code docs, not the compiled binary. Apache-2.0 `NOTICE` content is
propagated (§4(d)); MIT/BSD/ISC notices are preserved in the inventory.

### 45. Integration and E2E tests are gated behind the `integration` build tag

Integration/E2E suites run against a real Git repo, Docker daemon, and Compose;
they live under the `integration` tag and skip via `t.Skipf` when a prerequisite is
missing. Shared fixtures/checks live in `internal/testutil` (DRY). The Compose
suite uses the real `dockerClient`/`cliRunner` seams; the E2E test drives the full
lifecycle through `accorda sync`. Run explicitly with `go test -tags integration
./...`; the default run stays hermetic.

### 46. Integration tests in CI surfaced real bugs

CI runs the tagged suite against a live Docker daemon. Fixes: (1) `Target.Current`
read the runtime image from `ContainerJSONBase.Image` (a resolved `sha256:...`),
not the reference the operator passed, so desired-vs-runtime always reported drift;
it now prefers `Config.Image`, falling back only when `Config` is absent. (2)
go-git's `file://` transport failed on Go 1.25.0 but works on 1.25.6+, so CI pins
the toolchain to 1.25.6 while the module's `go` directive stays at the true minimum
1.25.0. (3) The E2E fixture used a relative `target.file`, resolved against cwd not
`--dir`; now absolute.

### 47. Docker engine SDK vulnerabilities are accepted risk

`govulncheck` reports two Docker SDK advisories (`GO-2026-4883` plugin privilege
off-by-one; `GO-2026-4887` AuthZ plugin bypass), both affecting only systems using
Docker plugins/AuthZ. Accorda uses the SDK only as a client and never installs
plugins, so the surface is unreachable. No fixed `docker/docker` release exists
(the fix is in `moby/moby/v2`); migration is tracked future work. The seam stays on
`docker/docker`.

### 48. Full validation is run through `scripts/test.sh`

`scripts/test.sh` runs a gofmt check, the build, and one additive
integration-tagged pass (`go test -count=1 -tags integration ./...`), stopping on
the first failure — the tagged pass includes the unit tests, so a separate untagged
pass would duplicate them. It resolves the module dir relative to itself, uses
`-count=1` to defeat caching, and relies on `internal/testutil` prereq skips. Use
it for full validation (`AGENTS.md`, `docs/IMPLEMENTER.md §5`). It also derives
aggregate statement coverage from the Go profile and fails below 85%
(`ACCORDA_MIN_COVERAGE` may override locally; CI uses the default and runs the same
script). Sonar keeps its distinct new-code gate.

### 49. MVP command trust boundaries fail closed

Issue #83: Git cache names are the SHA-256 of a canonical, credential-free URL
under a private user cache; cache paths reject symlinks, use mode `0700`, and
verify the cached `origin`. Automatic cache discovery uses the user cache dir,
falls back only to the user config dir, and fails closed if neither gives a
non-empty root (no shared temp fallback). Explicit SSH keys are read/parsed at
source validation; failure is fatal (no ambient fallback). `diff` env values show
only `<redacted>`/`<unset>`. Compose service names must start alphanumeric and use
Compose-safe identifiers, passed after `--` as CLI operands. `init` writes
`accorda.yaml` mode `0600`; the loader rejects group/world-readable files
containing an HTTPS token or inline URL password. Module and CI require Go 1.25.13,
and CI runs symbol-level `govulncheck`, failing on every reachable finding except
the two unreachable Docker SDK advisories (#47). This supersedes #46's Go 1.25.6
pin and minimum-toolchain choice.

### 50. Plaintext files have a callback-scoped runtime lifecycle

Issue #23 (§18): `internal/secrets.WithPlaintextFile` is the single file-backed
plaintext boundary. It materializes plaintext only under `/run/accorda`, requires
the runtime dir to be private (`0700` or stricter) and not a symlink, creates an
unpredictable file with mode `0600`, and exposes its path only for a callback's
duration. Removal runs after callback success, error, or panic; failures join the
returned error; during unwinding a `PanicCleanupError` re-panic preserves the exact
original panic value. SOPS owns cryptography; this package owns plaintext lifecycle
and shared redaction.

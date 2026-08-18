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
tree. Keep dependencies minimal: currently `github.com/spf13/cobra` (CLI) and
`gopkg.in/yaml.v3` (YAML). The git source shells out to the system `git` CLI
and the Compose parser is hand-rolled over `yaml.v3` rather than embedding a
Git library or the Docker SDK — an implementation choice, not a spec mandate.

**Consequence.** `go.mod` stays tiny and the adapters inherit the user's
environment. Embedding `go-git` or the Docker SDK in an adapter later would
not violate the spec, as long as Core stays provider-agnostic; any new
dependency requires justification and must not leak provider-specific
assumptions into core.

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

### 7. Dependency-free Compose parser

**Context.** The full Compose spec is large; Accorda only needs the subset
reasons about for reconciliation. The spec does not mandate a parser
implementation; this is an implementation choice.

**Decision.** `internal/targets/compose` parses the `services:` map and the
per-service fields Accorda needs, using only `yaml.v3` and
`internal/core/state`. No Docker SDK, no Compose spec library. Unknown
**top-level** keys (e.g. `volumes:`, `networks:`) are tolerated; unknown
**service-level** keys are rejected so typos surface. Short and long forms
are normalized for ports, volumes, environment, command, networks, labels,
depends_on, and healthcheck.

**Consequence.** The parser is small and reviewable; adding a Compose field
is a localized change in `decode.go`. Unsupported fields fail loudly rather
than silently. Adopting a Compose spec library later would not violate the
spec, provided it stays confined to the `targets/compose` adapter.

### 8. Git source shells out to system `git`

**Context.** §13 requires generic Git over SSH or HTTPS with zero SaaS
dependency. The spec does not mandate shelling out vs embedding a Git
library; this is an implementation choice.

**Decision.** `internal/sources/git` shells out to the system `git` command
rather than embedding a Go Git library. It inherits the user's SSH agent and
credential helpers. Fetch is scoped to the configured branch only.
`Desired(ref)` reads the services file at a commit via
`git show <sha>:<path>`; a missing file is an empty desired state, not an
error.

**Consequence.** No Git library dependency; transport and credential handling
come from the user's environment. Reading a commit on an unconfigured branch
requires that ref to have been fetched separately. Embedding `go-git` later
would not violate the spec, provided it stays confined to the `sources/git`
adapter.

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

### 11. Current state: `accorda init` writes `accorda.env`, not `accorda.yaml`

**Context.** The CLI `init` command predates the full `accorda.yaml` loader.

**Decision.** `cmd/accorda` `init` writes a minimal `accorda.env` dotenv file
(const `projectFile = "accorda.env"`). The `accorda.yaml` loader
(`internal/config`) is the canonical project format for the loader path.
This divergence is a current-state decision, not a final design.

**Consequence.** A future task should reconcile `init` to write
`accorda.yaml`; until then, both paths coexist and the divergence is
documented here and in the README.

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

---

## Decision log (continued)

New entries are appended below. Use the next available number.

<!-- Add new decisions here. Format:
### N. Short title
**Context.** ...
**Decision.** ...
**Consequence.** ...
-->
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
(YAML), `github.com/go-git/go-git/v6` (Git operations), and
`github.com/compose-spec/compose-go/v2` (Compose parsing). Accorda delegates
to these libraries rather than maintaining its own Git transport or Compose
parser, so it stays focused on its own mission (reconciliation) and avoids
hand-rolled code that would have to track upstream specs.

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

All other entries in `go.mod` are indirect (transitive) dependencies of these
four, pulled in automatically by `go mod tidy`. They are not imported by
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

---

## Decision log (continued)

New entries are appended below. Use the next available number.

<!-- Add new decisions here. Format:
### N. Short title
**Context.** ...
**Decision.** ...
**Consequence.** ...
-->
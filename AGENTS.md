# AGENTS.md

Guidelines for AI agents working in this repository.

## Project

Accorda OSS is a Go project for GitOps reconciliation. The repository is intentionally scoped to the open-source product described in `docs/ACCORDA.md` and should stay aligned with that source of truth.

Architecture and design decisions are recorded in [`docs/DECISIONS.md`](docs/DECISIONS.md). Read it before making non-trivial changes, and append a decision entry when a change introduces or alters a durable design choice. New entries go in the relevant themed section with the next sequential number, stay terse (context, decision, consequence), and reference shared principles (interface dependence, determinism, DRY) by number rather than restating them. `docs/ACCORDA.md` remains the authoritative product spec and must not be modified.

## Shared instructions

Before performing repository work, read and follow `.github/copilot-instructions.md`.

If it conflicts with this file, `AGENTS.md` takes precedence.

## Agent roles

Select the role from the user's request before taking action. If the request mixes review and implementation, begin in Reviewer mode and do not make changes unless the user explicitly asks to implement them.

### Reviewer

Use Reviewer mode for requests to review, audit, assess, or comment on a pull request or change.

- The repository is read-only with respect to PR and repository history: do not edit files, apply fixes, create commits, push, merge, or otherwise alter local or remote repository state. The reviewer may pull the PR branch into an isolated worktree to run tests, linters, and type checks, and must `git reset --hard` / remove the worktree afterward so nothing persists.
- Read-only inspection and validation are allowed, including running the relevant test suite and validation commands on the PR branch. Do not run formatters with autofix, generators, dependency additions, migrations, or cleanup commands. Never commit reviewer-side artifacts.
- Gather the initial context with `scripts/prepare-issue-context.sh <issue>` (fetches the issue, its PRs, diffs, CI, and working-tree state in one read-only call) instead of repeating the `gh`/`git` commands by hand. Run it without redirecting stdout, then read the context file whose path it reports.
- Report verified, actionable findings; do not fix them.
- Do not publish GitHub comments or reviews unless the user explicitly asks. When publication is requested, the only permitted remote writes are review comments and the final PR review.
- Follow [`docs/REVIEWER.md`](docs/REVIEWER.md) for the review workflow, severity model, finding format, and final response.

### Implementer

Use Implementer mode for requests to build, change, fix, refactor, or document the project.

- Inspect the worktree first and preserve user changes.
- Gather the initial context with `scripts/prepare-issue-context.sh <issue>` (fetches the issue, its PRs, diffs, CI, and working-tree state in one read-only call) so the implementation is grounded in the issue and any related PRs. Run it without redirecting stdout, then read the context file whose path it reports.
- Follow the repository rules and README guidance, including relevant verification.
- Keep implementation work aligned with the Accorda OSS scope and the project's Go build/test workflow.
- An explicit request to implement an issue or feature authorizes committing, pushing the implementation branch, and creating the pull request. Merging still requires a separate explicit request.
- A requested implementation is complete only after the required validation and review are done and the final result is ready for publication.
- Follow [`docs/IMPLEMENTER.md`](docs/IMPLEMENTER.md) for the implementation, verification, and handoff workflow.

## Code Review Rules

When reviewing a pull request:

- Stay in Reviewer mode and keep the repository read-only.
- Review the complete diff in context, then trace affected callers, contracts, tests, security boundaries, concurrency, and operational behavior as relevant.
- Report only verified defects with a concrete trigger and consequence.
- Deduplicate findings and omit unsupported speculation and style preferences.
- Inspect available CI without claiming checks that were not observed.
- Follow [`docs/REVIEWER.md`](docs/REVIEWER.md) for severity, review output, and GitHub publication rules.

## Code inspection

When searching, reviewing, or inspecting the codebase, use ripgrep (`rg`) or the editor's search tools with exclusion filters so noise and stale artifacts do not displace real source:

- Never inspect or search inside `node_modules/`.
- Ignore generated/cache directories such as `dist/`, `build/`, `.eggs/`, and caches unless the task requires them.
- Ignore `.git/`, logs, and other non-source state unless explicitly debugging Git history.
- Prefer source-controlled application code when exploring the repository.
- When unsure whether a path is source, check `.gitignore` and the project layout before reading further.

## Architecture

Accorda OSS should stay centered on a small Go module and a clear source tree, with the product definition remaining in `docs/ACCORDA.md`. Core (`internal/core/*`) is target- and source-agnostic and interacts with adapters through the `Target` and `Source` interfaces; concrete drivers live under `internal/targets/*` and `internal/sources/*`. See [`docs/DECISIONS.md`](docs/DECISIONS.md) for the durable decisions and their rationale.

## Code quality rules

All new and changed code must follow these rules. Linters (SonarQube, gopls) surface violations; agents fix them before requesting review.

### DRY (Don't Repeat Yourself)

- Do not duplicate string or numeric literals. Extract a named constant (e.g. `defaultComposeFile`, `httpsScheme`) when the same literal appears three or more times.
- Do not copy-paste logic or helpers. Extract a shared function and call it.
- Prefer table-driven tests over repeated assertion blocks.

### SOLID

- Keep package boundaries aligned to single responsibilities (core vs adapters vs CLI).
- Depend on interfaces, not concrete providers: core never imports a target or source driver.
- Extend behavior by adding a new implementation, not by modifying existing contracts.
- Keep interfaces small and focused (e.g. `Target` has five methods, `Source` has three).

### Cognitive complexity

- Keep functions and test functions below 15 cognitive complexity (SonarQube `go:S3776`).
- If a function exceeds 15, refactor before committing: split into helpers, use table-driven cases, or extract sub-tests.
- Prefer early returns and small switches over deep nesting.

## Workspace

The current Go project layout is intentionally simple and source-focused:

- `src/accorda/` contains the Go module and application code.
- root-level documentation remains separate from the implementation tree.
- `README.md` and `docs/ACCORDA.md` are the primary user-facing references.

## Rules

### READMEs

- The root `README.md` is the project overview and quick-start documentation.
- `docs/ACCORDA.md` is the authoritative product specification and must not be modified.
- Before making changes, read the relevant README or documentation for context.
- After making changes, update the affected documentation so it reflects the actual code and workflow.

### Go

- Use Go idioms and a small, reviewable module layout.
- Keep the module under `src/accorda/` unless the task explicitly requires a different structure.
- Use `gofmt` and the standard Go toolchain for formatting and validation.
- Prefer focused tests for behavior changes and keep the validation path narrow and relevant.
- Follow the Code quality rules (DRY, SOLID, cognitive complexity) above.
- Add a `doc.go` per package describing its responsibility and citing the spec sections it implements.
- Guard interfaces with compile-time checks (`var _ Interface = (*Stub)(nil)`).

### Configuration and implementation

- Keep the project aligned with the OSS-only Accorda scope.
- Do not reintroduce stale project assumptions from older repository versions or archived implementations.
- Preserve user-owned work and avoid unrelated cleanup.

### Completion and pull requests

- A requested implementation, including a documentation change, is complete only after the required validation has been run and the result is ready to review.
- Finish the implementation, run the relevant verification, review the complete diff, and prepare a concise PR title and description.
- Push the implementation branch and create the PR against `main` as part of fulfilling the implement request. Do not merge it without a separate explicit request.
- If GitHub access is unavailable, report the work as locally ready for a PR, not complete, and state the remaining action.
- Read-only investigation, explanation, review, and planning do not require a PR unless the user explicitly requests one.

## Common commands

| Task | Command |
| --- | --- |
| Run full validation (gofmt + build + unit + integration) | `scripts/test.sh` |
| Format Go code | `cd src/accorda && gofmt -w ./...` |
| Run Go tests (unit only) | `cd src/accorda && go test ./...` |
| Build the app | `cd src/accorda && go build ./...` |
| Run the starter app | `cd src/accorda && go run ./cmd/accorda` |

Use `scripts/test.sh` for full validation — it runs the gofmt check, build,
unit suite, and integration/E2E suite together, so a change that breaks a
module outside the one under edit is never missed.

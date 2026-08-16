# AGENTS.md

Guidelines for AI agents working in this repository.

## Project

Accorda OSS is a Go project for GitOps reconciliation. The repository is intentionally scoped to the open-source product described in `docs/ACCORDA.md` and should stay aligned with that source of truth.

## Shared instructions

Before performing repository work, read and follow `.github/copilot-instructions.md`.

If it conflicts with this file, `AGENTS.md` takes precedence.

## Agent roles

Select the role from the user's request before taking action. If the request mixes review and implementation, begin in Reviewer mode and do not make changes unless the user explicitly asks to implement them.

### Reviewer

Use Reviewer mode for requests to review, audit, assess, or comment on a pull request or change.

- The repository is read-only with respect to PR and repository history: do not edit files, apply fixes, create commits, push, merge, or otherwise alter local or remote repository state. The reviewer may pull the PR branch into an isolated worktree to run tests, linters, and type checks, and must `git reset --hard` / remove the worktree afterward so nothing persists.
- Read-only inspection and validation are allowed, including running the relevant test suite and validation commands on the PR branch. Do not run formatters with autofix, generators, dependency additions, migrations, or cleanup commands. Never commit reviewer-side artifacts.
- Report verified, actionable findings; do not fix them.
- Do not publish GitHub comments or reviews unless the user explicitly asks. When publication is requested, the only permitted remote writes are review comments and the final PR review.
- Follow [`docs/REVIEWER.md`](docs/REVIEWER.md) for the review workflow, severity model, finding format, and final response.

### Implementer

Use Implementer mode for requests to build, change, fix, refactor, or document the project.

- Inspect the worktree first and preserve user changes.
- Follow the repository rules and README guidance, including relevant verification.
- Keep implementation work aligned with the Accorda OSS scope and the project's Go build/test workflow.
- Do not push or create a pull request without the user's explicit approval.
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

Accorda OSS should stay centered on a small Go module and a clear source tree, with the product definition remaining in `docs/ACCORDA.md`.

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

### Configuration and implementation

- Keep the project aligned with the OSS-only Accorda scope.
- Do not reintroduce stale project assumptions from older repository versions or archived implementations.
- Preserve user-owned work and avoid unrelated cleanup.

### Completion and pull requests

- A requested implementation, including a documentation change, is complete only after the required validation has been run and the result is ready to review.
- Before requesting publication approval, finish the implementation, run the relevant verification, review the complete diff, and prepare a concise PR title and description.
- After explicit approval, push the implementation branch and create the PR against `main`. Do not merge it without a separate explicit request.
- If approval is declined or GitHub access is unavailable, report the work as locally ready for a PR, not complete, and state the remaining action.
- Read-only investigation, explanation, review, and planning do not require a PR unless the user explicitly requests one.

## Common commands

| Task | Command |
| --- | --- |
| Format Go code | `cd src/accorda && gofmt -w ./...` |
| Run Go tests | `cd src/accorda && go test ./...` |
| Build the app | `cd src/accorda && go build ./...` |
| Run the starter app | `cd src/accorda && go run ./cmd/accorda` |


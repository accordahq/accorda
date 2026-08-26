# Accorda — Instructions

This file gives GitHub Copilot context about the Accorda project so it can work within the existing conventions. Role selection, including the read-only pull request Reviewer role, is defined in `AGENTS.md` and takes precedence.

## Project overview

This repository is the open-source Accorda OSS project described in `docs/ACCORDA.md`. The active implementation is Go-based and should stay aligned with that product definition rather than older archived implementation details. Architecture and design decisions are recorded in `docs/DECISIONS.md`; read it before non-trivial changes and append a decision when a change alters a durable design choice. Add new decisions in the relevant themed section with the next sequential number, keep them terse (context, decision, consequence), and reference shared principles (interface dependence, determinism, DRY) by number rather than restating them.

## README maintenance

- Keep the root `README.md` concise and current.
- Update documentation whenever the project scope, startup flow, or implementation status changes.
- Keep docs aligned with the actual Go code and validation workflow.
- Treat `docs/ACCORDA.md` as the product specification and do not modify it.

## Implementation expectations

- Prefer Go idioms and a small, reviewable module layout.
- Keep the Go source under `src/accorda/` unless a task clearly requires a different structure.
- Add tests for behavior changes and keep them focused.
- Validate with `gofmt`, `go test ./...`, and `go build ./...` in the module directory.
- Avoid adding unused dependencies or speculative architecture.
- Keep the project ready for OSS iteration without reintroducing stale earlier assumptions.
- Follow the code-quality rules in `AGENTS.md` (DRY, SOLID, cognitive complexity). Do not duplicate literals or logic; extract named constants and shared helpers; keep functions and test functions below 15 cognitive complexity (SonarQube `go:S3776`); prefer table-driven tests over repeated assertion blocks.
- Add a `doc.go` per package describing its responsibility and citing the spec sections it implements.
- Guard interfaces with compile-time checks (`var _ Interface = (*Stub)(nil)`).

## Publication expectations

- An explicit request to implement an issue or feature authorizes committing,
  pushing the implementation branch, and creating the pull request. Merging
  still requires a separate explicit request.

## Review expectations

- Keep review work read-only unless the user explicitly asks for an implementation change.
- Verify the relevant Go checks when available.
- Report only concrete findings with a clear trigger and consequence.
- Keep the review aligned with the Accorda OSS scope and the source-of-truth document.

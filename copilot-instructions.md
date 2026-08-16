# Accorda — Instructions

This file gives GitHub Copilot context about the Accorda project so it can work within the existing conventions. Role selection, including the read-only pull request Reviewer role, is defined in `AGENTS.md` and takes precedence.

## Project overview

This repository is the open-source Accorda OSS project described in `docs/ACCORDA.md`. The active implementation is Go-based and should stay aligned with that product definition rather than older archived implementation details.

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

## Review expectations

- Keep review work read-only unless the user explicitly asks for an implementation change.
- Verify the relevant Go checks when available.
- Report only concrete findings with a clear trigger and consequence.
- Keep the review aligned with the Accorda OSS scope and the source-of-truth document.

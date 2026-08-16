# Accorda OSS

Accorda OSS is the open-source GitOps reconciliation project described in `docs/ACCORDA.md`.

This repository intentionally stays focused on the OSS product and does not include hosted control-plane features.

## Project status

This repository is being bootstrapped as a Go-based foundation for the Accorda OSS runtime. The current starter app is intentionally minimal and meant to provide a clean, testable base for future reconciliation features.

## Quick start

```bash
cd src/accorda
go build ./cmd/accorda
./accorda "friend"
```

Expected output:

```text
Hello, friend! Accorda OSS is ready.
```

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
│       │   ├── core/
│       │   │   ├── state/
│       │   │   ├── plan/
│       │   │   ├── reconcile/
│       │   │   ├── health/
│       │   │   ├── history/
│       │   │   └── events/
│       │   ├── sources/
│       │   ├── providers/
│       │   ├── targets/
│       │   ├── secrets/
│       │   ├── notifications/
│       │   └── hello/
│       │       ├── greet.go
│       │       └── greet_test.go
│       ├── go.mod
│       └── README.md
├── AGENTS.md
├── README.md
├── LICENSE
└── .github/
```

Each package under `internal/` currently contains a `doc.go` describing its
responsibility, matching the core and adapter boundaries defined in
`docs/ACCORDA.md`. No provider or target implementation code is included yet.

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

# Accorda OSS

Accorda OSS is the open-source GitOps reconciliation project described in `docs/ACCORDA.md`.

This repository intentionally stays focused on the OSS product and does not include hosted control-plane features.

## Project status

This repository is being bootstrapped as a Go-based foundation for the Accorda OSS runtime. The CLI (`cmd/accorda`) implements the command surface from `docs/ACCORDA.md` §11 and §45; `accorda version` and `accorda init` are functional, while the reconciliation commands (`status`, `diff`, `plan`, `sync`, `history`) are wired up and report that they are not yet implemented until the backing core packages land.

## Quick start

```bash
cd src/accorda
go build ./cmd/accorda
./accorda version
./accorda init -env production -repo git@github.com:acme/backend.git -branch main
```

`accorda init` writes a minimal `accorda.env` project file in the current directory (override with `-dir <path>`). `accorda version` prints the build version, falling back to VCS revision info from the Go build.

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
│       │   └── notifications/
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

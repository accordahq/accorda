# Usage

This guide describes the currently implemented Docker Compose workflow. It
assumes Accorda has already been installed as described in
[Installation](INSTALLATION.md) and that `accorda` is available on `PATH`.

## Create a project

An Accorda project is an operator-owned directory containing `accorda.yaml`.
It does not need to be inside the Git repository being reconciled. Accorda
maintains its own private checkout of that repository.

Create a directory for one deployment target:

```bash
mkdir -p "$HOME/accorda/projects/backend-production"
cd "$HOME/accorda/projects/backend-production"
```

Initialize it using SSH authentication:

```bash
accorda init \
  --env production \
  --repo git@github.com:example/platform-deployments.git \
  --branch main \
  --file deploy/compose.yaml \
  --auth-type ssh \
  --auth-key "$HOME/.ssh/id_ed25519"
```

`init` creates `accorda.yaml` with mode `0600` and refuses to overwrite an
existing file. It records configuration only; the first `plan` or `sync`
fetches the repository into Accorda's managed cache.

For an HTTPS repository, initialize with `--auth-type https`, then add the
credential to the generated file as directed by the command. Keep any project
file containing a token at mode `0600` and never commit it to Git.

## Project configuration

A generated project can be adjusted directly:

```yaml
version: 1
environment: production
source:
  type: git
  url: git@github.com:example/platform-deployments.git
  branch: main
  path: deploy/compose.yaml
  auth:
    type: ssh
    key: /home/accorda/.ssh/id_ed25519
target:
  type: compose
  file: deploy/compose.yaml
sync:
  interval: 30s
images:
  pull: changed
reconcile:
  drift: report
  remove_orphans: true
health:
  timeout: 120s
```

The Git repository is a desired-state source. It may be a dedicated GitOps
repository or an application repository that also contains the Compose file
and related resources. Relative Compose paths, `env_file` entries, configs,
build contexts, and other referenced files resolve from the managed checkout.
Each operator project receives an isolated checkout, so production and staging
may safely track different branches of the same repository.

Compose interpolation uses Git-authored values and defaults plus only the host
settings needed to reach Docker. Arbitrary application variables and an
implicit project `.env` file do not override the reviewed plan. Declare
deployment inputs explicitly with `environment` or `env_file`; `env_file` and
`label_file` declarations are tracked by the plan, and changes to referenced
files committed in Git recreate the affected service without storing their
contents in Accorda history.

Every Compose service must declare an `image`. A service may also declare
`build`; Docker Compose can then use the checked-out build context. For images
that exist only locally and cannot be pulled from a registry, set:

```yaml
images:
  pull: never
```

Supported image pull policies are `changed`, `missing`, `always`, and `never`.
Supported drift policies are `report`, `repair`, and `disabled`.

## Check prerequisites

Run diagnostics before the first deployment:

```bash
accorda doctor
```

`doctor` validates project and Git authentication configuration, Docker Engine
connectivity, and Docker Compose availability. For a new project it does not
fetch Git; validation of the repository Compose file occurs during `plan` or
`sync`.

## Review the deployment

Fetch the configured branch and preview the intended actions without changing
the target:

```bash
accorda plan
```

After at least one successful deployment, inspect field-level changes between
the last healthy revision and current Git HEAD:

```bash
accorda diff
```

Environment values are redacted from diff output.

## Reconcile once

Apply the current desired state, wait for health verification, and record a
deployment receipt:

```bash
accorda sync
```

The command prints each reconciliation phase as it progresses and finishes
with `sync: SYNCED` or `sync: FAILED`, so long fetch, pull, deployment, and
health-verification operations remain visible.

Accorda attempts rollback to the previous healthy deployment when apply or
health verification fails and a previous healthy deployment is available.

## Reconcile continuously

Run one immediate reconciliation and then poll Git using `sync.interval`:

```bash
accorda sync --watch
```

Use a process supervisor such as systemd or Docker to start this process at
boot and restart it after failure. Send SIGINT or SIGTERM for graceful
shutdown.

## Inspect operations

Show current desired, deployed, and runtime posture:

```bash
accorda status
```

Show the local deployment journal:

```bash
accorda history
```

Inspect the latest deployment or a specific commit:

```bash
accorda inspect
accorda inspect a84fd21
```

Read or follow logs for a Compose service:

```bash
accorda logs api --tail 200
accorda logs api --follow
```

## Operate multiple projects

### One project per directory

Keep a separate operator directory and `accorda.yaml` for each independent
deployment target or environment:

```text
~/accorda/projects/
├── backend-production/accorda.yaml
├── backend-staging/accorda.yaml
└── monitoring-production/accorda.yaml
```

Change into the corresponding project directory before running commands. Use
`--dir <path>` when operating a project from another directory.

### One agent, several workloads (ensemble)

A single `accorda.yaml` can list several named projects under a top-level
`projects:` key so one agent reconciles them concurrently
(docs/ACCORDA.md §49):

```yaml
projects:
  - name: api
    version: 1
    environment: production
    source:
      type: git
      url: git@github.com:acme/api.git
      branch: main
    target:
      type: compose
      file: compose.yaml
  - name: worker
    version: 1
    environment: production
    source:
      type: git
      url: git@github.com:acme/worker.git
      branch: main
    target:
      type: compose
      file: compose.yaml
```

Run `accorda sync` (or `accorda sync --watch`) once; both projects reconcile
concurrently and each line is prefixed with its project name. Every command
(`status`, `plan`, `diff`, `history`, `inspect`, `logs`, `doctor`) iterates
over all members. Project names must be unique (case-insensitive, matching
Compose project-name normalization); each member's Compose project name and
git checkout are scoped by its name so `--remove-orphans` cannot remove a
sibling's containers and two members sharing a repository get isolated
worktrees.

## Command help

The installed CLI is the authoritative reference for available flags:

```bash
accorda --help
accorda init --help
accorda sync --help
```

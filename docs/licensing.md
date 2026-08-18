# Licensing compliance

This document describes how to generate and maintain Accorda's third-party
license inventory. The release artifact is `THIRD_PARTY_LICENSES.md` at the
repository root; this file contains the developer and CI workflow for keeping
it accurate.

## Generating the dependency inventory

The dependency inventory for a release must be derived from the packages
used to build the Accorda executable, not from `go.sum`. Modules appearing
only in `go.sum` (test-only dependencies, historical versions, module
metadata) are not part of an Accorda binary.

Before generating the inventory, normalize the module graph:

```bash
cd src/accorda
go mod tidy
```

To inspect modules used by the Accorda executable for a single target:

```bash
go list -deps \
  -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' \
  ./cmd/accorda | sort -u
```

Because Go build constraints select different dependencies on different
operating systems and architectures, release builds should check each
supported target. The union of dependencies across all targets is the basis
for the third-party license bundle:

```bash
for target in \
  "linux amd64" \
  "darwin amd64" \
  "darwin arm64" \
  "windows amd64"
do
  set -- $target
  GOOS=$1 GOARCH=$2 go list -deps \
    -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' \
    ./cmd/accorda
done | grep -v '^ $' | sort -u
```

When the reconcile commands are wired up to import the git source and
compose target packages, the full dependency set (go-git, compose-go, etc.)
will appear in the `./cmd/accorda` output. Until then, use `./...` to capture
library dependencies that are not yet reachable from the CLI stubs.

## Updating THIRD_PARTY_LICENSES.md

1. Generate the dependency set as described above.
2. For each dependency, locate its `LICENSE` file in the Go module cache
   (`$(go env GOMODCACHE)/<module>@<version>/`).
3. Identify the license type and group the dependency under the matching
   license section in `THIRD_PARTY_LICENSES.md`.
4. For Apache-2.0 dependencies, check for an upstream `NOTICE` file. If it
   contains attribution that must be propagated under Apache-2.0 §4(d),
   include it in the component-specific notices section.
5. For MIT/BSD/ISC dependencies, preserve the component-specific copyright
   notice.

## CI license allowlist

A CI step should verify that no disallowed license (GPL, AGPL, LGPL, MPL,
or other copyleft licenses) appears in the dependency tree. Use
`go-licenses` or an equivalent tool to classify licenses and fail the build
if a disallowed license is detected.

## Apache-2.0 §4(d) NOTICE propagation

Apache-2.0 §4(d) requires that derivative works retain attribution notices
from upstream `NOTICE` files of Apache-2.0-licensed dependencies. The
following dependencies ship `NOTICE` files that have been incorporated into
`THIRD_PARTY_LICENSES.md`:

- `github.com/compose-spec/compose-go/v2` — "The Compose Specification,
  Copyright 2020 The Compose Specification Authors"
- `go.yaml.in/yaml/v4` — libyaml MIT attribution + Apache-2.0 copyright
- `gopkg.in/yaml.v3` — Apache-2.0 copyright (Canonical Ltd)

When a new Apache-2.0 dependency is added, check its `NOTICE` file and
propagate any required attribution.
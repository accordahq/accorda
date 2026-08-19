#!/usr/bin/env bash
# Run the full Accorda Go validation: unit tests, integration/E2E tests, and
# the build, from a single invocation.
#
# Agents and contributors should call this script (`scripts/test.sh`) instead
# of running the long `go test ./... && go test -tags integration ./...`
# commands by hand, so a full validation pass is always run and a change that
# breaks a module outside the one under edit is never missed.
#
# The integration tests require the system `git` executable and, for the
# Docker Compose suites, a running Docker daemon with `docker compose`. They
# skip gracefully (via internal/testutil) when a prerequisite is unavailable,
# so the script is safe to run anywhere; the full suite is exercised in CI
# where Docker is present.
#
# Exit status is non-zero if formatting, build, unit tests, or integration
# tests fail.
set -euo pipefail

# Resolve the script's directory so the script works regardless of the
# caller's working directory.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
module_dir="$(cd "$here/../src/accorda" && pwd)"

cd "$module_dir"

echo "==> gofmt check"
unformatted="$(gofmt -l $(go list -f '{{.Dir}}' ./...))"
if [[ -n "$unformatted" ]]; then
  echo "error: gofmt needed on:" >&2
  echo "$unformatted" >&2
  exit 1
fi

echo "==> go build"
go build ./...

echo "==> unit tests (go test ./...)"
go test -count=1 ./...

echo "==> integration/E2E tests (go test -tags integration ./...)"
go test -count=1 -tags integration ./...

echo "==> all validation passed"

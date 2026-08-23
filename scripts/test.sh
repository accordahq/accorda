#!/usr/bin/env bash
# Run the full Accorda Go validation: unit + integration/E2E tests, and the
# build, from a single invocation.
#
# Agents and contributors should call this script (`scripts/test.sh`) instead
# of running the long `go test ./... && go test -tags integration ./...`
# commands by hand, so a full validation pass is always run and a change that
# breaks a module outside the one under edit is never missed.
#
# The single `go test -tags integration ./...` below runs the *full* suite:
# the integration build tag is additive, so it compiles and runs the
# integration-tagged tests together with all regular unit tests in one pass.
# A separate `go test ./...` would re-run the same unit tests, so there is no
# separate unit-test command. `-coverpkg=./...` instruments imported project
# packages too, so shared integration helpers receive coverage credit when
# they execute from another package's tests.
#
# The integration tests require the system `git` executable and, for the
# Docker Compose suites, a running Docker daemon with `docker compose`. They
# skip gracefully (via internal/testutil) when a prerequisite is unavailable,
# so the script is safe to run anywhere; the full suite is exercised in CI
# where Docker is present.
#
# Exit status is non-zero if formatting, build, tests, or the aggregate
# statement-coverage threshold fail.
set -euo pipefail

minimum_coverage="${ACCORDA_MIN_COVERAGE:-85.0}"
if [[ ! "$minimum_coverage" =~ ^[0-9]+([.][0-9]+)?$ ]] ||
  ! awk -v minimum="$minimum_coverage" 'BEGIN { exit !(minimum + 0 <= 100) }'; then
  echo "error: ACCORDA_MIN_COVERAGE must be a number from 0 to 100" >&2
  exit 1
fi

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

echo "==> full suite: unit + integration/E2E (go test -tags integration ./...)"
go test -v -count=1 -tags integration -coverpkg=./... ./... -coverprofile=coverage.out

echo "==> aggregate statement coverage (minimum ${minimum_coverage}%)"
coverage="$(go tool cover -func=coverage.out | awk '$1 == "total:" { gsub(/%/, "", $3); print $3 }')"
if [[ -z "$coverage" ]]; then
  echo "error: aggregate coverage was not reported" >&2
  exit 1
fi
if ! awk -v actual="$coverage" -v minimum="$minimum_coverage" \
  'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
  echo "error: aggregate statement coverage ${coverage}% is below ${minimum_coverage}%" >&2
  exit 1
fi
echo "aggregate statement coverage: ${coverage}%"

echo "==> all validation passed"

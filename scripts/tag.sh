#!/usr/bin/env bash
# Create a semantic-version tag on the current commit and push it, which
# triggers the Release workflow (docs/ACCORDA.md §23).
#
# Usage:
#   scripts/tag.sh [major|minor|patch]   # autoincrement from the latest tag
#   scripts/tag.sh v1.2.3                # explicit version
#
# With no argument the bump defaults to `patch`. The script validates the
# working tree (clean, on main, up to date with origin), derives the next
# version from the latest reachable `v*` tag, and refuses to overwrite an
# existing tag. The tag is pushed so the Release workflow builds and publishes
# the binaries.
#
# Exit status is non-zero on any validation failure or if the push fails.
set -euo pipefail

remote=origin

usage() {
  echo "usage: $0 [major|minor|patch|vX.Y.Z]" >&2
  exit 2
}

# latest_tag prints the highest reachable `v*` tag, or nothing when none exists.
latest_tag() {
  git tag --sort=-v:refname --merged HEAD | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1 || true
}

# bump computes the next version from a base version and a bump kind.
bump() {
  local base=$1 kind=$2
  local major minor patch
  major="${base#v}"
  major="${major%%.*}"
  minor="${base#*.}"
  minor="${minor%%.*}"
  patch="${base##*.}"
  case "$kind" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
  esac
  echo "v${major}.${minor}.${patch}"
}

# --- validation -----------------------------------------------------------

if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  echo "error: working tree is not clean; commit or stash changes before tagging" >&2
  exit 1
fi

branch="$(git branch --show-current)"
if [[ "$branch" != "main" ]]; then
  echo "error: tags must be created on main (current branch: $branch)" >&2
  exit 1
fi

git fetch "$remote" --tags
if ! git diff --quiet "$remote/main" HEAD; then
  echo "error: local main is not up to date with $remote/main; pull first" >&2
  exit 1
fi

# --- version resolution ---------------------------------------------------

arg="${1:-patch}"
case "$arg" in
  major|minor|patch)
    base="$(latest_tag)"
    if [[ -z "$base" ]]; then
      echo "error: no existing v* tag to autoincrement from; pass an explicit version (e.g. v0.1.0)" >&2
      exit 1
    fi
    version="$(bump "$base" "$arg")"
    ;;
  v[0-9]*.[0-9]*.[0-9]*)
    version="$arg"
    ;;
  *)
    usage
    ;;
esac

if git rev-parse -q --verify "refs/tags/$version" >/dev/null; then
  echo "error: tag $version already exists" >&2
  exit 1
fi

# --- create and push ------------------------------------------------------

echo "Creating tag $version on $(git rev-parse --short HEAD)"
git tag -a "$version" -m "Accorda $version"
git push "$remote" "$version"

echo "Tag $version pushed; the Release workflow will build and publish it."

#!/usr/bin/env bash
# Post inline review comments and a final review on a GitHub pull request.
#
# Usage:
#   scripts/pr-review.sh <pr> inline <path> <line> <body>
#   scripts/pr-review.sh <pr> review <approve|request-changes|comment> <body>
#
# `inline` posts a single line-anchored review comment on the PR head commit.
# `review` submits the final review with the given verdict.
#
# The script resolves the PR head SHA automatically, forces the integer `line`
# type the REST API requires, and applies the authorship rule: GitHub rejects
# `--approve`/`--request-changes` from the PR author, so when the authenticated
# account owns the PR the script falls back to `--comment` and records the
# intended verdict in the review body.
#
# Requires the GitHub CLI (gh) authenticated against the repository. The body
# is a single shell argument; quote it (or use $'...') for multi-line text.
#
# Exit status is non-zero if gh is missing, the PR cannot be read, or a write
# fails.
set -euo pipefail

owner=accordahq
repo=accorda

usage() {
  echo "usage: $0 <pr> inline <path> <line> <body>" >&2
  echo "       $0 <pr> review <approve|request-changes|comment> <body>" >&2
  exit 2
}

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh (GitHub CLI) is required" >&2
  exit 1
fi

[[ $# -ge 2 ]] || usage
pr=$1
mode=$2
shift 2

head_sha() {
  gh pr view "$pr" --json headRefOid --jq .headRefOid
}

author_login() {
  gh pr view "$pr" --json author --jq .author.login
}

me() {
  gh api user --jq .login
}

case "$mode" in
  inline)
    [[ $# -eq 3 ]] || usage
    path=$1
    line=$2
    body=$3
    sha=$(head_sha)
    # -F line=N forces an integer; -f line=N sends a string and the API
    # rejects it with a 422 ("line" is not an integer).
    gh api "repos/$owner/$repo/pulls/$pr/comments" \
      -f commit_id="$sha" \
      -f path="$path" \
      -F line="$line" \
      -f body="$body" >/dev/null
    echo "posted inline comment on $path:$line"
    ;;
  review)
    [[ $# -eq 2 ]] || usage
    verdict=$1
    body=$2
    case "$verdict" in
      approve) flag=--approve ;;
      request-changes) flag=--request-changes ;;
      comment) flag=--comment ;;
      *) usage ;;
    esac
    if [[ "$verdict" != "comment" && "$(me)" == "$(author_login)" ]]; then
      echo "note: authenticated account is the PR author; GitHub rejects $flag, falling back to --comment" >&2
      upper=$(printf '%s' "$verdict" | tr '[:lower:]' '[:upper:]')
      body="**Intended verdict: $upper** (submitted as COMMENT because the authenticated account is the PR author)."$'\n\n'"$body"
      flag=--comment
    fi
    tmp=$(mktemp)
    trap 'rm -f "$tmp"' EXIT
    printf '%s\n' "$body" >"$tmp"
    gh pr review "$pr" "$flag" --body-file "$tmp"
    ;;
  *)
    usage
    ;;
esac

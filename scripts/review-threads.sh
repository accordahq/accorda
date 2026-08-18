#!/usr/bin/env bash
# Inspect and resolve review threads on a GitHub pull request.
#
# Usage:
#   scripts/review-threads.sh <pr-number>            # list unresolved threads
#   scripts/review-threads.sh resolve <pr-number> <thread-id>...
#
# The default (list) mode prints each unresolved thread's ID and a short
# body snippet so you can decide which threads are actually addressed before
# resolving them. The `resolve` mode resolves only the thread IDs you pass;
# it never resolves threads you did not explicitly list.
#
# Requires the GitHub CLI (gh) authenticated against the repository. Output
# is trimmed with --jq to keep it small.
#
# Exit status is non-zero if gh is missing, the query fails, or a resolve
# mutation fails.
set -euo pipefail

owner=accordahq
repo=accorda

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh (GitHub CLI) is required" >&2
  exit 1
fi

# list_threads prints "id<TAB>snippet" for each unresolved thread.
list_threads() {
  local pr=$1
  gh api graphql \
    -f query='query($owner: String!, $repo: String!, $pr: Int!) { repository(owner: $owner, name: $repo) { pullRequest(number: $pr) { reviewThreads(first: 50) { nodes { id isResolved comments(first: 1) { nodes { body } } } } } } }' \
    -f owner="$owner" -f repo="$repo" -F pr="$pr" \
    --jq '.data.repository.pullRequest.reviewThreads.nodes[]
      | select(.isResolved == false)
      | [.id, (.comments.nodes[0].body // "" | split("\n")[0] | .[0:80])]
      | @tsv'
}

resolve_thread() {
  local id=$1
  gh api graphql \
    -f query='mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }' \
    -f id="$id" \
    --jq '.data.resolveReviewThread.thread.isResolved' >/dev/null
  echo "resolved $id"
}

if [[ $# -ge 1 && $1 == "resolve" ]]; then
  shift
  if [[ $# -lt 2 ]]; then
    echo "usage: $0 resolve <pr-number> <thread-id>..." >&2
    exit 2
  fi
  pr=$1
  shift
  for id in "$@"; do
    resolve_thread "$id"
  done
  exit 0
fi

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <pr-number>" >&2
  echo "       $0 resolve <pr-number> <thread-id>..." >&2
  exit 2
fi

pr=$1
list_threads "$pr"

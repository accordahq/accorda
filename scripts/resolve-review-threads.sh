#!/usr/bin/env bash
# Resolve all unresolved review threads on a GitHub pull request.
#
# Usage:
#   scripts/resolve-review-threads.sh <owner> <repo> <pr-number>
#
# Requires the GitHub CLI (gh) authenticated against the repository. The
# script lists unresolved review threads (id + isResolved only, via --jq to
# keep output small) and resolves each with the resolveReviewThread mutation.
#
# Exit status is non-zero if gh is missing, the query fails, or any thread
# fails to resolve.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <owner> <repo> <pr-number>" >&2
  exit 2
fi

owner=$1
repo=$2
pr=$3

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh (GitHub CLI) is required" >&2
  exit 1
fi

# List unresolved thread IDs only (no comment bodies) to keep output small.
ids=$(gh api graphql \
  -f query='query($owner: String!, $repo: String!, $pr: Int!) { repository(owner: $owner, name: $repo) { pullRequest(number: $pr) { reviewThreads(first: 50) { nodes { id isResolved } } } } }' \
  -f owner="$owner" -f repo="$repo" -F pr="$pr" \
  --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | .id')

if [[ -z "$ids" ]]; then
  echo "no unresolved review threads"
  exit 0
fi

for id in $ids; do
  gh api graphql \
    -f query='mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }' \
    -f id="$id" \
    --jq '.data.resolveReviewThread.thread.isResolved' >/dev/null
  echo "resolved $id"
done

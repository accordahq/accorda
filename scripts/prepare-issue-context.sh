#!/usr/bin/env bash
# Gather the initial context for an issue and its related pull requests in one
# read-only call, so an agent does not repeat the same gh/git commands for
# every issue. It is used by both the Reviewer (to establish review scope) and
# the Implementer (to understand the issue before working on it).
#
# Usage:
#   scripts/prepare-issue-context.sh <issue> [pr...]
#
# <issue> the issue number to work on or review. It may be given as a bare
#         number (17), a GitHub issue URL
#         (https://github.com/accordahq/accorda/issues/17), or a #-prefixed
#         number (#17); the script normalizes all three to the bare number.
# [pr...] optional explicit PR numbers. When omitted, the script finds the
#         pull requests that reference the issue (via the issue timeline) and
#         gathers context for each.
#
# The script prints, for the issue and each related PR, in order:
#   1. The issue body, state, labels, and milestone.
#   2. The PRs that reference the issue.
#   3. For each PR: metadata (title, body, author, base/head refs, URL,
#      commits, files, CI status rollup), the PR diff, the PR CI checks, the
#      local diff of the head branch against its base, and whether the current
#      working tree is on the PR head commit.
#
# The script is read-only with respect to the PR and repository history: it
# only fetches the head branch and prints diffs. It never checks out, commits,
# pushes, or alters PR metadata. The agent decides whether to check out the
# head branch (e.g. into an isolated worktree) after seeing the state.
#
# The script writes its full output to a deterministic file and prints that
# path, so an agent can read the complete context in one call even when the
# terminal truncates the inline output. The file is written to the OS temp
# directory and named with the issue and PR numbers, e.g.
#   /tmp/accorda-issue-17-pr-73-context.txt
# The agent should read that file (not the truncated terminal output) to get
# the full issue body, PR metadata, and diffs.
#
# Requires the GitHub CLI (gh) authenticated against the repository and a git
# remote named `origin`.
#
# Exit status is non-zero if gh is missing, the issue cannot be read, or a
# head branch cannot be fetched.
set -euo pipefail

owner=accordahq
repo=accorda

if ! command -v gh >/dev/null 2>&1; then
  echo "error: gh (GitHub CLI) is required" >&2
  exit 1
fi

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <issue> [pr...]" >&2
  exit 2
fi
issue=$1
shift

# Normalize the issue argument: accept a bare number (17), a GitHub issue URL
# (https://github.com/accordahq/accorda/issues/17), or a #-prefixed number
# (#17), and reduce all three to the bare number.
issue="${issue#https://github.com/$owner/$repo/issues/}"
issue="${issue#\#}"
if [[ ! "$issue" =~ ^[0-9]+$ ]]; then
  echo "error: invalid issue argument '$issue' (expected a number, #number, or a GitHub issue URL)" >&2
  exit 2
fi

# Resolve the PRs that reference the issue when none were passed explicitly.
prs=("$@")
if [[ ${#prs[@]} -eq 0 ]]; then
  prs=()
  while IFS= read -r pr; do
    prs+=("$pr")
  done < <(gh api "repos/$owner/$repo/issues/$issue/timeline" \
    --jq '.[] | select(.event=="cross-referenced" and .source.issue.pull_request) | .source.issue.number')
fi

# Resolve the PRs that reference the issue when none were passed explicitly.
prs=("$@")
if [[ ${#prs[@]} -eq 0 ]]; then
  prs=()
  while IFS= read -r pr; do
    prs+=("$pr")
  done < <(gh api "repos/$owner/$repo/issues/$issue/timeline" \
    --jq '.[] | select(.event=="cross-referenced" and .source.issue.pull_request) | .source.issue.number')
fi

# Write the full output to a deterministic file so an agent can read the
# complete context in one call even when the terminal truncates inline output.
# The file is created in the OS temp directory and named with the issue and
# PR numbers. The script's own stdout/stderr still go to the terminal so the
# user sees progress; the file holds the full gathered context.
outfile="/tmp/accorda-issue-${issue}-context.txt"
if [[ ${#prs[@]} -gt 0 ]]; then
  prs_slug=$(IFS=-; echo "${prs[*]}")
  outfile="/tmp/accorda-issue-${issue}-pr-${prs_slug}-context.txt"
fi
: > "$outfile"

# Run the rest of the script with stdout redirected to the context file, then
# print the path prominently so the agent knows where to read the full output.
{
echo "======================================================================"
echo "Issue #$issue"
echo "======================================================================"
gh issue view "$issue" --json number,title,body,state,labels,milestone,url \
  --jq '{number, title, state, url, labels: [.labels[].name], milestone: .milestone.title, body}'

echo
echo "======================================================================"
echo "PRs referencing issue #$issue"
echo "======================================================================"
if [[ ${#prs[@]} -eq 0 ]]; then
  echo "(none found)"
else
  for pr in "${prs[@]}"; do
    gh pr view "$pr" --json number,title,state,url,author,headRefName,baseRefName \
      --jq '{number, title, state, url, author: .author.login, baseRefName, headRefName}'
  done
fi

for pr in "${prs[@]+"${prs[@]}"}"; do
  echo
  echo "======================================================================"
  echo "PR #$pr metadata"
  echo "======================================================================"
  gh pr view "$pr" --json number,title,body,author,baseRefName,headRefName,url,commits,files,statusCheckRollup \
    --jq '{number, title, author: .author.login, baseRefName, headRefName, url, commits: [.commits[].oid], files: [.files[].path], statusCheckRollup: [.statusCheckRollup[] | {name, conclusion, status}]}'

  echo
  echo "======================================================================"
  echo "PR #$pr diff"
  echo "======================================================================"
  gh pr diff "$pr"

  echo
  echo "======================================================================"
  echo "PR #$pr CI checks"
  echo "======================================================================"
  gh pr checks "$pr" || echo "(no checks reported)"

  # Resolve the PR head SHA and branch once; used for the diff and the
  # working-tree check.
  head_sha=$(gh pr view "$pr" --json headRefOid --jq .headRefOid)
  head_branch=$(gh pr view "$pr" --json headRefName --jq .headRefName)
  base_branch=$(gh pr view "$pr" --json baseRefName --jq .baseRefName)

  # Fetch the head branch so the local diff reflects the PR head. A merged
  # PR's branch is often deleted from the remote, so fall back to fetching
  # the head SHA directly; if neither is available, skip the local diff.
  echo
  echo "======================================================================"
  echo "Fetching head branch $head_branch"
  echo "======================================================================"
  if ! git fetch origin "$head_branch" 2>&1; then
    echo "(branch $head_branch not on remote; trying head SHA $head_sha)"
    if ! git fetch origin "$head_sha" 2>&1; then
      echo "(head SHA $head_sha not fetchable; skipping local diff)"
      continue
    fi
  fi

  echo
  echo "======================================================================"
  echo "Diff $base_branch...$head_branch (stat)"
  echo "======================================================================"
  git diff --stat "origin/$base_branch...origin/$head_branch" 2>/dev/null \
    || git diff --stat "origin/$base_branch...$head_sha"

  echo
  echo "======================================================================"
  echo "Diff $base_branch...$head_branch (full)"
  echo "======================================================================"
  git diff "origin/$base_branch...origin/$head_branch" 2>/dev/null \
    || git diff "origin/$base_branch...$head_sha"

  echo
  echo "======================================================================"
  echo "Working-tree state (PR #$pr)"
  echo "======================================================================"
  current=$(git rev-parse HEAD 2>/dev/null || true)
  current_branch=$(git branch --show-current 2>/dev/null || true)
  if [[ "$current" == "$head_sha" ]]; then
    echo "On PR head: current HEAD ($current) == PR head ($head_sha)."
    echo "Branch: $current_branch"
  elif [[ -n "$current_branch" && "$current_branch" == "$head_branch" ]]; then
    echo "On branch $head_branch but HEAD ($current) != PR head ($head_sha)."
    echo "Run: git fetch origin $head_branch && git reset --hard origin/$head_branch"
    echo "(or check out into an isolated worktree) before reviewing the working tree."
  else
    echo "Not on the PR branch. Current branch: ${current_branch:-<detached>}, HEAD: $current."
    echo "PR head branch: $head_branch ($head_sha)."
    echo "To review the working tree, check out the head branch into an isolated worktree."
  fi
done
} > "$outfile" 2>&1

echo
echo "======================================================================"
echo "Context written to: $outfile"
echo "Read this file for the full issue body, PR metadata, and diffs."
echo "======================================================================"

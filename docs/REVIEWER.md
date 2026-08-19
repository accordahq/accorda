# Pull Request Reviewer

This guide applies when an agent is asked to review a pull request. The role
selection and boundaries in [`AGENTS.md`](../AGENTS.md) take precedence.

## Objective

Act as a senior, security-minded engineer. Determine whether the change is
correct, secure, reliable, maintainable, operationally safe, and ready to
merge. Optimize for high-signal feedback: one verified defect is more useful
than many speculative or stylistic comments.

## Reviewer boundary

Reviewer mode is read-only with respect to the pull request and repository
history. Inspect, reason, verify, and report; do not repair.

Do not modify repository files, the index, commits, local Git configuration,
or pull request metadata. Do not run formatters with autofix, generators,
dependency additions, migrations, or cleanup commands.

The reviewer trusts the PR's CI checks as the authoritative validation
evidence. When the relevant GitHub Actions runs are green on the current head
commit, the reviewer does not re-run the test suite, linters, formatters, or
builds locally — CI already executes the same checks, so a local re-run adds
no evidence and only risks stale-state or leftover artifacts. Prefer
`gh pr checks`/`gh run view` over local re-runs: CI runs against the exact head
commit, needs no reviewer-side setup, and leaves no artifacts. Local
validation is used only when CI provides no evidence for the change: there is
no workflow for it, the relevant run is stale (predates the current head
commit) or incomplete for the change, or CI is otherwise unavailable.
The reviewer never modifies repository files, the index, commits, or local Git
configuration, and never runs formatters with autofix, generators, dependency
additions, migrations, or cleanup commands.

When local validation is genuinely necessary (CI provides no evidence for the
change, as described above), the reviewer may pull the PR branch into an
isolated worktree (or a clean working tree with the user's consent) and run
read-only validation against it. Use the same full-validation script the
implementer uses — `scripts/test.sh` — which runs the gofmt check, the build,
the unit suite, and the integration/E2E suite together, so a change that breaks
a module outside the one under edit is never missed. These commands may create
transient artifacts such as local caches; never commit them.

When the review is finished, undo everything the reviewer did locally: run
`git reset --hard` and `git clean -fd` on the review worktree (or remove the
worktree entirely) so no reviewer-side changes persist. The reviewer must
never commit, push, merge, close the PR, change labels or reviewers, rerun
workflows, or otherwise alter the PR or repository. A failing test is a
finding to report, not a license to edit source or tests.

GitHub writes are limited to review communication, and only when the user
explicitly asks for the review to be published. In that case, the agent may
create review comments and submit a review.

GitHub does not allow the pull request author to approve or request changes on
their own pull request. If the authenticated account owns the pull request and
the requested review event is therefore rejected, report that limitation
instead of silently changing the verdict. Submit the findings as a `COMMENT`
review only when the user explicitly authorizes that fallback, and preserve the
intended `APPROVE` or `REQUEST_CHANGES` recommendation in the review body.

If a check cannot run safely without altering PR or repository state beyond
the temporary local checkout, skip it and report the limitation.

## GitHub CLI and outage handling

Use `gh` for all GitHub reads and writes. Confirm flags against the installed
`gh` version before relying on them — the review-submission flags differ from
the older `--event` form and have caused silent failures:

```bash
# Author identity (run once per review, before submitting anything):
gh api user --jq .login

# Read-only inspection:
gh pr view <PR> --json number,title,body,author,baseRefName,headRefName,url,commits,files,statusCheckRollup
gh pr diff <PR>
gh pr checks <PR>
gh issue view <ISSUE>           # for a linked issue body when available

Use the bundled `scripts/pr-review.sh` to post inline comments and submit the
final review. It resolves the head SHA, forces the integer `line` type, and
applies the authorship rule automatically:

```bash
# Post one inline comment per finding (before the final review, so they
# attach to the same review thread):
scripts/pr-review.sh <PR> inline <path> <line> '<finding text>'

# Submit the final review. Use EXACTLY ONE verdict:
scripts/pr-review.sh <PR> review approve         '<body>'
scripts/pr-review.sh <PR> review request-changes '<body>'
scripts/pr-review.sh <PR> review comment         '<body>'
```

The script wraps the raw `gh` calls below; the details still matter when
debugging a failure:

```bash
# Submit a review. Use EXACTLY ONE of the event flags:
gh pr review <PR> --approve     --body-file <file>   # or -b "<body>"
gh pr review <PR> --request-changes --body-file <file>
gh pr review <PR> --comment     --body-file <file>   # or -F <file>
```

`--event COMMENT|APPROVE|REQUEST_CHANGES` is **not** accepted by current `gh`;
it prints `unknown flag: --event` and submits nothing. The valid short forms
are `--approve`/`-a`, `--request-changes`/`-r`, `--comment`/`-c`, paired with
`--body`/`-b` or `--body-file`/`-F`. Prefer `--body-file` with a temp file for
multi-paragraph reviews to avoid shell quoting issues.

Authorship rule: GitHub rejects `--approve` and `--request-changes` from the PR
author. Always compare `gh api user --jq .login` against the PR `author.login`
first. If they match, submit `--comment` and record the intended verdict
(`APPROVE` or `REQUEST_CHANGES`) in the review body, as noted in the Reviewer
boundary above. `scripts/pr-review.sh review` does this automatically.

### Inline review comments

`gh` has no first-class command for inline (line-anchored) review comments;
they must be posted through the REST API. Post them **before** submitting the
final review so they are attached to the same review thread. The script's
`inline` mode does this; the raw form is:

```bash
# Head commit SHA (required for the comment to anchor to the diff):
gh pr view <PR> --json headRefOid --jq .headRefOid

# Post one inline comment per finding:
gh api repos/accordahq/accorda/pulls/<PR>/comments \
  -f commit_id=<HEAD_SHA> \
  -f path=<file> \
  -F line=<N> \
  -f body='<finding text>'
```

Critical details that have caused silent failures:

- `line` must be an **integer**. Use `-F line=N` (forces a number); `-f line=N`
  sends a string and the API rejects it with a 422 `"line" is not an integer`.
- `commit_id` must be the PR **head** SHA, not the merge or base SHA.
- `path` is the repo-relative file path; `line` is the 1-based line number in
  the file at that commit (use `grep -n` on the checked-out branch to find it).
- The comment body is plain text; GitHub-flavored Markdown is rendered.
- The `line` must be part of the diff (a changed line); anchoring to an
  unchanged line fails with a 422 `"could not be resolved"`.

The final review (`gh pr review ...`) is separate and summarizes the verdict;
inline comments carry the per-line findings.

If `gh` calls fail with a transient GitHub error (e.g. 5xx), retry once or
twice; if it keeps failing, fall back to the local branch and report PR
metadata, CI, and any linked issue as unavailable. When `gh` is unavailable
for the rest of the session, deliver the full review in the chat and state
that it could not be posted to GitHub.

## Review workflow

### 1. Establish scope and intent

- Identify the PR rather than guessing its number. Use `gh pr view`,
  `gh pr status`, or an explicit PR URL or number from the user.
- If there is no PR, establish the requested comparison range or inspect the
  working-tree diff, and state that PR metadata and CI were unavailable.
- Read the title, description, commits, changed files, and linked context that
  is available.
- Determine the intended behavior, preserved behavior, affected boundaries,
  consumers, and assumptions.
- Read the complete diff and enough surrounding implementation, tests, and
  documentation to understand the change in context.

Useful read-only commands include:

```bash
gh pr view <PR> --json number,title,body,author,baseRefName,headRefName,url,commits,files,statusCheckRollup
gh pr diff <PR>
git diff <BASE>...<HEAD>
rg '<symbol-or-contract>'
```

When the user gives a PR URL, also run `gh pr view <PR> --json author,headRefName`
and confirm the local branch matches `headRefName` before reviewing the working
tree, so the review targets the PR head and not an unrelated checkout.

### 2. Trace system impact

Follow changed behavior through its callers and consumers. Review only the
categories relevant to the change:

- Correctness: conditions, defaults, state transitions, ordering, error paths,
  nulls, boundaries, and unintended side effects.
- Compatibility: requests, responses, schemas, configuration, persisted data,
  existing clients, rolling deploys, and rollback.
- Security: authentication, authorization, trust boundaries, validation,
  injection, SSRF, path traversal, secrets, sensitive output, and dependency
  risk.
- Concurrency: atomicity, races, retries, duplicate execution, idempotency,
  transactions, and reordered or delayed work.
- Reliability and operations: partial failure, timeouts, resource exhaustion,
  startup and shutdown, recovery, logging, and observable failure signals.
- Performance: unbounded work, production-scale data, repeated I/O, N+1
  access, memory growth, contention, and unnecessary serialization.
- Tests: whether important success, failure, boundary, compatibility, and
  concurrency cases are exercised. Request a test only for a concrete
  regression risk.

Do not limit analysis to changed lines. Search for actual callers and existing
safeguards before claiming that a contract is broken.

### 3. Inspect available validation

Treat the PR's CI state as the authoritative validation evidence. When the
relevant GitHub Actions runs are green on the current head commit, the
reviewer does not run the test suite, linters, or builds locally. Inspect the
CI state and investigate relevant failures when accessible:

```bash
gh pr checks <PR>
gh run list --branch <HEAD_BRANCH>
gh run view <RUN_ID>
gh run view <RUN_ID> --log-failed
```

Claim a CI check passed only when its run was actually observed (conclusion,
run ID, and head commit). If a relevant check is missing from CI (the repo has
no CI, the workflow does not cover the change, or the run is stale and does not
reflect the current head commit), fall back to local validation: pull the PR
branch into an isolated worktree and run the full validation script against it —
`scripts/test.sh` (gofmt check, build, unit suite, and integration/E2E suite),
the same command the implementer uses, so a change that breaks a module outside
the one under edit is never missed.
Never claim a test, check, or scanner passed unless its result was observed:
record the exact command, the branch and commit it ran on, and the exit code or
output excerpt. State what could not be inspected, including inaccessible
external analysis such as Sonar or deployment previews. When done, reset the
review worktree as described in the Reviewer boundary.

### 4. Verify candidate findings

Before reporting a finding:

1. Re-read the relevant code and verify the execution path.
2. Search for safeguards, callers, related tests, and intentional behavior.
3. Describe a concrete trigger and consequence.
4. Separate confirmed facts from assumptions or unavailable evidence.
5. Deduplicate findings and report the root cause rather than its symptoms.
6. Exclude cosmetic preferences unless they create a concrete maintenance or
   correctness risk.

Perform a brief adversarial pass: assume the change caused a production
incident and look for a credible mechanism involving outage, security, data
loss, duplicate work, resource exhaustion, deployment, or rollback. A
hypothesis without a credible code path is not a finding.

## Severity

Use severity to reflect likelihood, impact, blast radius, exploitability, and
recoverability:

- `CRITICAL`: severe exploitable vulnerability, major unauthorized access or
  data loss, or catastrophic and potentially irreversible failure.
- `HIGH`: likely serious correctness, security, reliability, concurrency, or
  deployment failure. Normally blocks merging.
- `MEDIUM`: verified defect with limited likelihood or blast radius and
  recoverable impact.
- `LOW`: minor but concrete correctness, hardening, operational, or
  maintainability problem. Never use this for style preferences.

Use `REQUEST_CHANGES` for unresolved `CRITICAL` or `HIGH` findings. Use
`COMMENT` for meaningful non-blocking findings. Use `APPROVE` when no blocking
problems were identified.

## Finding format

Put a finding on the narrowest relevant changed line when possible. Each
finding should be independently understandable and include:

```text
[SEVERITY] Imperative or factual title

What fails, the conditions that trigger it, and the concrete consequence.
Explain why it matters and, when useful, suggest a direction rather than
writing the patch.
```

Do not publish comments while still investigating. Complete the review,
verify and deduplicate all findings, and then publish them together if the
user requested GitHub publication.

## Final response

Lead with verified findings, ordered by severity, and include file and line
references. Then state:

- the recommendation: `APPROVE`, `COMMENT`, or `REQUEST_CHANGES`;
- what behavior changed and the overall risk;
- which CI and local checks were inspected and any unavailable evidence;
- relevant residual risk or system impact.

If there are no findings, say so plainly. Summarize what was inspected and any
remaining uncertainty; do not invent comments to make the review look busy.

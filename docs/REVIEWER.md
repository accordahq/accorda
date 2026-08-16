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

The reviewer may pull the PR branch into an isolated worktree (or a clean
working tree with the user's consent) and run read-only validation against it:
the relevant Go test suite, formatting check, build validation, and any repo-
approved tooling required for the change. These commands may create transient
artifacts such as local caches; never commit them.

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

Inspect the PR's CI state and investigate relevant failures when accessible:

```bash
gh pr checks <PR>
gh run list --branch <HEAD_BRANCH>
gh run view <RUN_ID>
gh run view <RUN_ID> --log-failed
```

When CI is unavailable or insufficient, pull the PR branch into an isolated
worktree and run the relevant local checks against it — the test suite,
linters, and type checks listed in the Reviewer boundary. Never claim a test,
check, or scanner passed unless its result was observed: record the exact
command, the branch and commit it ran on, and the exit code or output excerpt.
State what could not be inspected, including inaccessible external analysis
such as Sonar or deployment previews. When done, reset the review worktree as
described in the Reviewer boundary.

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

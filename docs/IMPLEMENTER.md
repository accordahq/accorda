# Implementer

This guide applies when an agent is asked to build, change, fix, refactor, or
document the project. The role selection and non-negotiable rules in
[`AGENTS.md`](../AGENTS.md) take precedence.

## Objective

Deliver the requested change completely and safely. Preserve user work, follow
the repository's architecture and conventions, verify the result in proportion
to its risk, and leave the work in a reviewable pull request.

Implementation authority covers local changes required by the request. It does
not authorize unrelated cleanup, deployment, or remote GitHub mutations.

## Workflow

### 1. Establish scope and repository state

- Read `AGENTS.md`, `.github/copilot-instructions.md`, and the relevant
  component README files before changing the repository.
- Read `docs/DECISIONS.md` before non-trivial changes; append a decision
  entry when the change introduces or alters a durable design choice.
- Inspect the worktree and current branch. Treat existing modifications and
  untracked files as user-owned unless the task clearly says otherwise.
- If existing work prevents a safe branch switch or overlaps the requested
  change, stop and ask the user how to proceed. Never discard or overwrite it.
- Identify the intended behavior, affected components, interfaces, callers,
  tests, documentation, and important compatibility constraints.

### 2. Prepare the implementation branch

Start new implementation work from the latest remote `main`:

```bash
git switch main
git pull --ff-only origin main
git switch -c <descriptive-branch-name>
```

Do not implement directly on `main`. Keep all related implementation commits
on the dedicated branch. If the task is already continuing on its dedicated
branch, preserve that branch and verify its relationship to `origin/main`
instead of restarting the work.

### 3. Implement the smallest complete change

- Follow the architecture, package boundaries, and component-specific rules in
  `AGENTS.md` and the relevant READMEs.
- Make changes that are necessary for the requested outcome. Avoid unrelated
  refactors, dependency upgrades, formatting churn, or speculative features.
- Preserve existing public behavior unless the request intentionally changes
  it. Trace changes across producers, consumers, schemas, configuration, and
  deployment boundaries.
- Handle relevant failure paths, validation, security, concurrency,
  compatibility, and operational behavior as part of the implementation.
- Add or update tests when they protect the changed behavior against a concrete
  regression.
- Use the project’s approved tooling for edits and dependency changes. Do not
  edit manifests by hand when the repo expects a package-manager workflow.

### 4. Keep documentation accurate

Read the README for every affected component before changing it. After the
implementation, update the appropriate existing README when an endpoint,
module, setting, interface, responsibility, workflow, or user-visible behavior
changed.

Do not create a new README unless the change adds a major component. Keep
documentation concise and describe the current behavior rather than the
history of the change.

### 5. Verify proportionately

Run the narrowest relevant checks first, then broader checks when warranted by
the change's scope and risk. Typical evidence includes:

- focused unit or integration tests;
- affected test suites;
- type checking, linting, or formatting checks;
- build or packaging validation;
- manual inspection of generated or user-facing output.

Do not claim a check passed unless its result was observed. If a relevant check
cannot run because of environment, dependency, credential, or infrastructure
limitations, report what was attempted and what remains unverified.

Documentation-only changes normally require link, formatting, and diff checks
rather than application test suites.

### 6. Review the completed change

Before committing or requesting permission to publish:

1. Inspect the complete diff, including newly created files.
2. Confirm the diff matches the user's request and contains no unrelated work.
3. Check for accidental secrets, debug output, generated artifacts, and stale
   documentation.
4. Revisit important callers, interfaces, and failure paths.
5. Run `git diff --check` and the relevant validation.
6. Confirm the worktree contains only understood changes.

Create focused commits with clear messages on the implementation branch.

### 7. Prepare and publish the pull request

Before any push or pull request creation:

- finish the implementation and verification;
- prepare a concise PR title and description;
- summarize the changed behavior and validation;
- obtain the user's explicit approval to push and create the PR.

Approval to implement is not approval to publish. Do not push, force-push, or
call `gh pr create` until the user approves those remote mutations.

After approval:

```bash
git push -u origin <branch-name>
gh pr create --base main --head <branch-name>
```

Do not merge the PR unless the user separately and explicitly requests it.

### 8. Resolve review feedback

When a reviewer leaves comments on the pull request, resolve them:

- Read every review comment (including inline comments) and address each
  finding — fix blocking issues, and either fix or explicitly respond to
  non-blocking ones.
- Prefer fixing over arguing; when a finding is intentionally not fixed,
  reply with a concrete rationale rather than leaving it unanswered.
- After making changes, re-run the relevant validation and push the follow-up
  commits to the same branch.
- Reply to the review thread summarizing what changed so the reviewer can
  re-review without re-reading the whole diff.
- Mark each addressed thread as resolved. Replying with a comment does not
  resolve a thread; resolution is a separate action, and leaving threads
  unresolved blocks the reviewer from seeing the PR as addressed.

To keep API output small (and save tokens), fetch only the fields you need
with `--jq`, and resolve threads in one loop:

```bash
# List thread IDs + a short body snippet (not the full body).
gh api graphql -f query='query { repository(owner:"O", name:"R") { pullRequest(number:N) { reviewThreads(first:50) { nodes { id isResolved } } } } }' --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | .id'

# Resolve each unresolved thread.
for id in <thread-ids>; do
  gh api graphql -f query='mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }' -f id="$id" --jq '.data.resolveReviewThread.thread.isResolved'
done
```

Prefer `--jq` over dumping full JSON; it trims the response to the fields you
actually need and avoids re-reading large comment bodies.

## Completion and handoff

An implementation request is complete only when the pull request exists and
its URL has been provided to the user. Until then, describe the result as
locally ready for a pull request and state the remaining action.

The final handoff should include:

- the outcome and important behavior changes;
- affected files or components;
- validation performed and any remaining limitations;
- the commit or branch when useful;
- the pull request URL, or a clear request for publication approval when the
  work is only local.

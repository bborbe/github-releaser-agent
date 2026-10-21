---
status: prompted
approved: "2026-10-21T16:39:03Z"
generating: "2026-10-21T16:40:10Z"
prompted: "2026-10-21T16:51:35Z"
branch: dark-factory/bug-releaser-publish-blocked-by-branch-protection
---

## Summary

- The releaser pushes the release commit and tag straight to `master`; on a repo whose default branch is ruleset-protected, GitHub rejects the push and no release ships.
- The rejection is `GH013: Repository rule violations found … Changes must be made through a pull request`. The recorded sample also carries `3 of 3 required status checks are expected`, which `pkg/git/error_classifier.go` already maps to `protected_branch_rejected` — a bare `GH013` line without those tokens would fall to `unknown`.
- Each rejection burns one of the task's three triggers; after three the executor parks the task at its trigger cap and it is never re-driven.
- Fix: on a branch-protection rejection, publish through a pull request — push the commit to a release branch, open the PR, push the tag — and park the task at `human_review` with the PR URL instead of failing.
- A human merges the PR; the release then completes visibly instead of silently disappearing.

## Problem

22 of 30 open `github-releaser-agent` tasks on nuke prod sit at the executor's trigger cap. Twelve of them failed on a push rejected by branch protection (`GH013` ×8, `GH006` ×4). The agent's contract is to cut the next release, and the documented design is that the push succeeds because the releaser's GitHub App is a **bypass actor** on the repo's `master-protection` ruleset (`README.md` § How it works). That design is not holding: the `bypass_actors` on `bborbe/go-skeleton`, `bborbe/nuke` and `bborbe/sun` are identical — all three list `Integration 3907226` (the releaser app) with `bypass_mode: always` — yet `nuke` and `sun` released successfully on 2026-10-21 while `go-skeleton` was rejected. The one observed ruleset difference is that `go-skeleton` alone sets `strict_required_status_checks_policy: true` with three required checks. Why the bypass did not apply is unresolved; it is recorded as a follow-up, not a gate on this fix.

## Assumptions

- The releaser app (`3907226`) is installed on `bborbe/go-skeleton` with contents write; a missing installation would surface as an auth/404 error, not as a ruleset violation.
- The `bypass_actors` are identical across `go-skeleton`, `nuke` and `sun`. The one observed ruleset difference is `go-skeleton` alone setting `strict_required_status_checks_policy: true` with three required checks, which does not by itself explain a bypass denial.
- The push identity is the app installation token — `cmdEnv()` passes only `HOME` and `PATH`, and the clone URL carries `x-access-token:<IAT>`.
- `human_review` is outside the executor's trigger phase set (`trigger.phases: [planning, execution, ai_review]`), so parking there stops the trigger burn.
- A pull request opened by the app against a protected branch becomes mergeable once the required checks pass.

## Goal

Record the bypass evidence and the leading hypothesis in `# Progress`, and ship the pull-request fallback regardless: on a push rejected by branch protection, the release lands as a reviewable PR with its tag pushed and the task parked at `human_review` naming the PR URL, so it stops consuming triggers and the operator gets a one-click merge instead of a silently parked task. Restoring the bypass fast path is a recorded follow-up, not part of this fix.

## Reproduction

Observed 2026-10-21 on nuke prod, executor `agent-task-executor:v0.23.1`, agent image `github-releaser-agent:v0.4.13`.

Task `064df133-71a5-5151-8376-c7a0e063c760` (`Release bborbe-go-skeleton 8c6e133`, `trigger_count: 3`) records, verbatim:

```
push failed: git push: remote: error: GH013: Repository rule violations found for refs/heads/master.
remote: Review all repository rules at https://github.com/bborbe/go-skeleton/rules?ref=refs%2Fheads%2Fmaster
remote:
remote: - Changes must be made through a pull request.
remote:
remote: - 3 of 3 required status checks are expected.
remote:
To https://github.com/bborbe/go-skeleton.git
 ! [remote rejected] HEAD -> master (push declined due to repository rule violations)
 ! [remote rejected] v0.6.3 -> v0.6.3 (atomic transaction failed)
error: failed to push some refs to 'https://github.com/bborbe/go-skeleton.git'
```

The same signature appears in eight further tasks; four more record `GH006: Protected branch update failed`. All twelve are at `trigger_count` 3 or 4 with `status: in_progress`, `phase: planning`.

Job evidence: `github-releaser-agent-6a74c9cf-20261021154850` logs `ai_review: push failed: … GH013 …` and the executor increments `skipped_trigger_cap`.

Replay: `/github-release-repo-trigger` for a ruleset-protected repo emits a fresh `github-release` task; any releaser Job whose `## Failure` carries the block above reproduces it. Agent image under test: `github-releaser-agent:v0.4.13` (the prod Config pin) while master was at `v0.4.19`; post-push reconciliation from spec 003 is merged, so `## Result` already records `error_category` — the routing, not the classification, is what fails.

## Expected vs Actual

| | |
|---|---|
| **Expected** | The agent's documented contract (`README.md` § How it works, `docs/dod.md` § Git-Write Safety) is to cut the next release. Where the bypass fast path does not land, it publishes through a PR: branch push, PR create, tag push, then parks the task at `human_review` with the PR URL. |
| **Actual** | It pushes `HEAD` and the tag directly to `master` (`pkg/steps_ai_review.go` `finishApproved` → `pkg/git/os_exec_git_ops.go` `Push`). The push is rejected, `output.Approved=false` with `FailedChecks=[Push]`, the task goes to `human_review` as a generic failure, and the next spawn repeats it until the trigger cap parks the task permanently. |

## Why this is a bug

The documented design (`README.md` § How it works) is that the releaser's GitHub App bypasses `master-protection`, so a direct push is the intended path. The live state contradicts that design for `bborbe/go-skeleton` while the identical ruleset on `bborbe/nuke` and `bborbe/sun` let the same app push — so either the bypass is not reaching this repo/app pair, or the push identity differs from what the design assumes. Separately, `pkg/git/error_classifier.go` has no `GH013` fragment: the recorded sample still classifies correctly only because it also carries `required status checks`, and a GH013 message without that token would be `unknown`.

This makes the fix order load-bearing: establish why the bypass did not apply **before** choosing between restoring the documented fast path and adding a PR fallback.

## Acceptance Criteria

- [ ] The bypass question is recorded in `# Progress` with the evidence gathered — identical `bypass_actors` on `go-skeleton`/`nuke`/`sun`, and `go-skeleton` alone carrying `strict_required_status_checks_policy: true` with three required checks — and the leading hypothesis named. The fix does not depend on resolving it. Evidence: `grep -n 'bypass'` on `25 Tasks/Releaser Tasks Park at the Trigger Cap After Failing Inside the Agent.md` in the `private-personal` vault returns ≥1 line naming the hypothesis and the reads that produced it.
- [ ] `ClassifyError` maps a stderr containing `GH013` and no other classifier token to `ErrorCategoryProtectedBranchRejected`. Evidence: `grep -n 'GH013' pkg/git/error_classifier.go` returns ≥1 line, and a `DescribeTable` row in `pkg/git/error_classifier_test.go` asserts the category for a verbatim GH013 sample.
- [ ] On a push error classified `ErrorCategoryProtectedBranchRejected`, the agent pushes the release commit to a `release/<tag>` branch instead of `master`, opens a pull request against the default branch, and pushes the annotated tag. Evidence: a Ginkgo test with a fake git-ops and a fake GitHub client asserts the branch ref push, exactly one PR create call carrying title `release <tag>` and a body containing the CHANGELOG section, and the `refs/tags/<tag>` push.
- [ ] The resulting task result is `human_review` with `previous_assignee: github-releaser-agent` and a new `pull_request_url` field on the `## Result` section (the reserved `path: "pr-merge"` value per `pkg/result_output.go`). Evidence: the same test asserts the returned `agentlib.Result` status/next-phase, the `path` value, and that the rendered `## Result` contains the PR URL.
- [ ] A non-protection push error (`non-fast-forward`, `unknown`) keeps today's behaviour: `Approved=false`, `FailedChecks` contains `Push`, task goes to `human_review` with no PR created. Evidence: a table test asserting zero PR-create calls for those categories.
- [ ] **Post-Deploy (Rung-2):** re-driving one capped releaser task against a ruleset-protected repo yields an open PR and the task at `phase: human_review` with the PR URL. Evidence: `gh pr list --repo <owner>/<repo> --state open` returns ≥1 row, and `grep -n 'phase: human_review' <task file>` returns 1 line.
  - `deploy_check:` `kubectlnukedev -n dev get config github-releaser-agent -o jsonpath='{.spec.image}'`
  - `deploy_target:` `github-releaser-agent:$(git describe --tags --abbrev=0)`
- [ ] `ROOTDIR=/workspace make precommit` exits 0.
- [ ] `CHANGELOG.md` carries a `## Unreleased` bullet describing the PR fallback. Evidence: `grep -n '^## Unreleased' CHANGELOG.md` returns exactly one line and it precedes the first `^## v` heading; the bullet beneath it mentions the pull-request fallback. The section does not exist on master today (`## v0.4.19` is first), so the prompt creates it below the preamble.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `ROOTDIR=/workspace make precommit` — exits 0
- `grep -n 'GH013' pkg/git/error_classifier.go` — returns ≥1 line
- `go test ./pkg/git/... ./pkg/...` — classifier table and ai-review fallback tests pass

### Operator-executable (runs on the host after PR merge)

- Append the runbook's `### Branch-protection bypass` evidence and hypothesis to `# Progress` on `25 Tasks/Releaser Tasks Park at the Trigger Cap After Failing Inside the Agent.md` in the `private-personal` vault; `grep -n 'bypass'` on that task returns ≥1 line.
- Deploy the released image to nuke dev and re-drive one capped releaser task; confirm a PR exists (`gh pr list --repo <owner>/<repo> --state open`) and the task file shows `phase: human_review` with the PR URL.
- Confirm the task's `trigger_count` stops incrementing after the `human_review` park.
- Deploy to prod only with the operator's in-the-moment approval.

## Desired Behavior

1. A push rejection whose stderr classifies as `protected_branch_rejected` (GH013, GH006, `protected branch`, `Required reviews`, `required status checks`) takes the PR path, not the failure path.
2. The PR path pushes `HEAD` to `release/<tag>` and creates a pull request against the repo's default branch, titled `release <tag>`, with the CHANGELOG section as its body.
3. The annotated tag is pushed to the remote in the same run.
4. The task returns `human_review` with `previous_assignee: github-releaser-agent`, and `## Result` carries the PR URL and the rejection reason.
5. `human_review` is outside the agent's trigger phase set, so the task stops consuming triggers.
6. Every other push failure keeps the current failure shape and never opens a PR.
7. The remote remains the authority: the run never reports `released` unless the tag is observed on the remote.

## Constraints

- The git writes stay deterministic Go: the LLM step still only classifies the bump.
- The release commit touches `CHANGELOG.md` only, via an explicit pathspec.
- The escalation contract is preserved: a step that cannot proceed returns `NeedsInput` / `human_review` with `previous_assignee: github-releaser-agent`.
- No new write path beyond: push to `release/<tag>`, push `refs/tags/<tag>`, create one pull request.
- Do not force-push; do not merge the PR automatically — a human merges.
- On the PR path the result is `outcome: failed` with `error_category: protected_branch_rejected` and `pull_request_url` set. Post-push reconciliation must not upgrade it to `released` even though the tag is present, because the default branch does not carry the commit — `human_review` wins over reconciliation on this path.
- `README.md` § How it works and `docs/dod.md` § Git-Write Safety are updated to describe the PR fallback alongside the direct push.
- Errors use `github.com/bborbe/errors`; logging is `glog` `V(n)`.
- Existing tests must still pass.

## Failure Modes

| Trigger | Expected behavior | Detection / Recovery |
|---|---|---|
| Branch protection rejects the push (GH013 / GH006) | Push `release/<tag>`, open PR, push tag, park `human_review` with PR URL | `gh pr list` shows the PR; task `## Result` names it |
| PR create fails (auth, 422, rate limit) | Fall back to today's failure shape with the PR error in `## Result`; do not report `released` | `## Result` carries the API error; task at `human_review` |
| Tag push fails while the branch push succeeded | Report the tag error; do not report `released` | `## Result` names the tag failure; remote has no tag |
| Non-protection push error | Unchanged failure path, no PR | `FailedChecks` contains `Push`; zero PR-create calls |
| Repo already has an open PR whose head branch is `release/<tag>` | Reuse it: do not open a second PR | Test asserts one PR-create call at most; match key is the head branch |
| Remote tag already exists at a different commit | Existing `superseded` handling is unchanged | `## Result` names the observed SHA |
| Tag pushed, PR never merged | Tag exists on the remote but the default branch does not carry the commit; the next release derives its version from the latest tag | Operator merges the PR (`gh pr list --state open`) or deletes the tag |
| Crash between branch push, PR create and tag push | Re-drive reuses the existing branch and PR and pushes the tag | `## Result` names the step reached; the duplicate-PR row prevents a second PR |

Pushing a public tag is the one irreversible write in this spec. Order it after the PR is created, and record the PR URL before the tag push so a crash leaves a recoverable pointer.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Root-cause the bypass failure — installation scope, token identity, ruleset evaluation — and record the mechanism | — | 1 | — |
| 2 | Add `GH013` to `classifierTable` + table-test row | 1 | 2 | — |
| 3 | PR fallback in the ai-review push path: branch push, PR create, tag push, `human_review` result + tests | 2–7 | 3, 4, 5 | 1, 2 |
| 4 | `## Unreleased` CHANGELOG bullet + `README.md` / `docs/dod.md` update + `make precommit` green | — | 7, 8 | 3 |

AC6 is the verification-phase Post-Deploy AC — no prompt covers it; it runs after the dev deploy.

## Do-Nothing Option

Not fixing it keeps every ruleset-protected repo unreleasable: 12 of 22 capped releaser tasks today, and the count grows with each new protected repo. Each occurrence costs a manual re-drive and a silent gap in the release history.

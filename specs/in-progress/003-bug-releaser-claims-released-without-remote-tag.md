---
status: prompted
approved: "2026-09-13T17:23:19Z"
generating: "2026-09-13T17:57:40Z"
prompted: "2026-09-13T17:57:40Z"
branch: dark-factory/bug-releaser-claims-released-without-remote-tag
---

## Summary

- `github-releaser-agent` writes `"outcome": "released"` into the task's `## Result` block during execution, from the tag it created in its own workdir — before any push is attempted and without asking GitHub. A release whose push never landed still reads as success.
- Three live release tasks are wedged in exactly that state: `## Result` says `released`, GitHub has no such tag, the repo never received its version.
- A single run can contradict itself: the wedged `c7eb0f3` task carries both `"outcome": "released"` and `## Failure — failed checks: UnexpectedFileChange, Faithfulness`. Two components disagree about the same run and the optimistic one is what the task page shows.
- Fix: the success claim moves to the step that owns the push — after pushing, the agent consults the remote tag (dereferenced commit, prefix-compared against the expected commit) and rewrites `## Result` to a non-success outcome naming the failure whenever the remote does not carry that tag.
- A second, mechanically coupled defect is fixed in the same change: the existing post-check compares a short 7-char expected SHA against a full 40-char remote SHA for exact equality, so its `released` verdict is unreachable and a landed release is mis-verdicted `superseded`.

## Problem

The release pipeline's own record of what happened cannot be trusted, and it cannot be promoted to autonomous operation while that is true. `github-releaser-agent` records `outcome: released` from workdir-local state before the only push is attempted and never re-checks it afterwards, so a release whose push never landed is indistinguishable — on the task page, for every consumer, and for any promotion metric — from one that shipped. Three release tasks are wedged in that state today, each reading success while its repository sits a version behind.

## Reproduction

Observed during the `github-release-close-obsolete-tasks` sweep run in dry-run on 2026-09-13. The sweep declined to close all three tasks because it compares *remote* tags — the opposite of what the agent's own result step does:

```bash
bash ~/.claude/skills/github-release-close-obsolete-tasks/scripts/check.sh   # dry-run, no --apply
```

Sweep verdicts (verbatim):

- `Release bborbe-go-skeleton c7eb0f3` — "v0.6.1 not on remote, repo still at v0.6.0 (real failure)"
- `Release bborbe-go-skeleton ba735cf` — same, wedged since 2026-09-12
- `Release bborbe-notification-controller 855b280` — "v0.5.0 not on remote, repo still at v0.4.0"

Live verification for `c7eb0f3`, gathered 2026-09-13:

- Commit `a05d4d29`, message `release v0.6.1`, authored `2026-09-13T10:06:26Z` — exists on GitHub (`gh api repos/bborbe/go-skeleton/commits/a05d4d2`).
- `git ls-remote https://github.com/bborbe/go-skeleton.git refs/heads/master` → `c7eb0f382d78` — the release commit is NOT on master.
- `git ls-remote --tags https://github.com/bborbe/go-skeleton.git` → highest tag is `v0.6.0` — `v0.6.1` was never pushed.

The offending `## Result` block, quoted verbatim from the task file (note `local_tag` beside `tag` — the report is assembled from the tag the agent created in its own workdir, never from the remote):

```
{ "outcome": "released", "path": "direct-push", "commit_sha": "a05d4d2", "tag": "v0.6.1", "local_tag": "v0.6.1", "workdir": "/tmp/github-releaser-755cb88a-…" }
```

The same task file carries `## Failure — failed checks: UnexpectedFileChange, Faithfulness` — the agent's own review caught the discrepancy while `## Result` claimed success.

**The trigger differs across the three wedged tasks; only the misreport is shared.** `ba735cf` and `855b280` each record an explicitly rejected `Push` (GH013 ruleset and GH006 classic protection respectively). `c7eb0f3` records **no `Push` failure at all** — only the failed review checks, because its review verdict was never approved and the push was never attempted. Sibling defects around ruleset bypass must not be absorbed into this spec: the shared defect is the unconditional success record, not the rejection cause.

Environment: production `github-releaser-agent` (job-style agent, one pod per release task; image live on dev + prod on 2026-09-13 — read the deployed tag from the `github-releaser-agent` Config CR before replaying, this table goes stale), consuming `task_type: github-release` tasks written to the OpenClaw vault under `tasks/`.

## Expected vs Actual

| | |
|---|---|
| **Expected** | `## Result` reads `"outcome": "released"` only after the remote confirms the planned tag exists at the expected commit. A rejected push, or a review rejection that never attempted a push, yields a non-success outcome naming the failure. |
| **Actual** | `## Result` is written `"outcome": "released"` during execution (`pkg/steps_execution.go:154-166`), from workdir-local state (`tagName` / `local_tag`), before the only push is attempted. The push runs later in the review step (`pkg/steps_ai_review.go:300-311`); on push failure it appends `CheckPush`, sets `Approved=false` and routes to `human_review`, but never rewrites `## Result` — the optimistic value stands. A review that returns `!approved` never reaches the push at all, leaving the same optimistic value. |

## Why this is a bug

`pkg/result_output.go:13-15` states the contract for the block: `outcome: released` means "direct-push succeeded". Execution writes that value before the push is even attempted — the field's documented meaning is violated by construction, not by an edge case.

The defect is the *absence* of verification, not the optimistic write. At execution time an empty remote tag is the expected state (the push has not happened yet), so the execution-step post-check's no-op on an empty remote (`pkg/steps_execution.go:598-607`) is right. What is missing is any post-push verification anywhere downstream: the push step never consults the remote after pushing, and the review step's `!approved` branch consults it only to *upgrade* the verdict, never to downgrade the optimistic record.

Downstream consumers trust the field: `pkg/steps_ai_review.go:192` gates the whole review on `result.Outcome != ResultOutcomeReleased`. The failure is silent by construction — the pipeline believes it released, the task page carries a success record, the task parks looking like it needs a decision, and the repo simply never gets its version. Anything waiting on that tag waits forever, and nothing in the agent's output says so.

Mechanically coupled second defect: `pkg/steps_execution.go:611` compares the observed remote SHA against `expectedSHA` for exact equality, but `expectedSHA` is `Result.commit_sha`, produced by `git rev-parse --short HEAD` (`pkg/git/os_exec_git_ops.go:118-125`), while `LsRemote` returns the full 40-char SHA (`pkg/git/os_exec_git_ops.go:192-234`). Exact equality can never hold, so the `released` post-check verdict is unreachable and a landed release is mis-verdicted `superseded`.

## Goal

Every release task page states what actually happened on the remote. `outcome: released` appears only when the remote carries the planned tag at the expected commit; a push that was rejected, a review that never pushed, or a remote answer that could not be obtained all leave a non-success outcome naming the failure — so `## Result` and `## Review` can no longer disagree about the same run. Both SHA comparisons in the pipeline (the existing execution-step post-check and the new post-push verification) compare short expected against full observed by prefix, so a landed release is verdicted `released` and only a different commit is verdicted `superseded`.

## Acceptance Criteria

- [ ] **(a) No `released` survives an absent remote tag.** For a release run whose tag is absent from the remote after the push step has run, the task markdown carries a non-success `## Result` outcome — never `"outcome": "released"`. Evidence: file content (negative + positive) — the regression test asserts, on the remote-absent fixture, that the extracted `## Result` outcome equals `ResultOutcomeFailed` and that the emitted markdown contains no `"outcome": "released"` (`grep -c '"outcome": "released"' <emitted markdown>` returns 0); `grep -n 'ResultOutcomeFailed' pkg/steps_ai_review_test.go` returns ≥1 and `make test` exits 0.
- [ ] **(b) The push step performs the post-push verification, and the outcome is derived from the observed SHA.** After the push call, the step consults the remote for `refs/tags/<planned tag>`, prefers the dereferenced `^{}` commit line and falls back to the plain tag line when the tag is lightweight, and derives the outcome from that observed SHA. Evidence: file content (grep, discriminating) — `grep -n 'LsRemote' pkg/steps_ai_review.go` returns MORE matches than the pre-fix count of 4 (a call-graph stub that adds none does not pass), and the two derivation fixtures named in (c)/(d) assert opposite outcomes from the same push result, which only a result-driven branch can produce; `pkg/git` tests cover both `^{}`-present and lightweight-only output shapes (`grep -c 'LsRemote\|ParseLsRemoteOutput' pkg/git/os_exec_git_ops_test.go` returns at least the pre-fix count of 14).
- [ ] **(c) Rejected push OR failed review without a push yields a non-success outcome naming the failure.** Both paths write a `## Result` whose `outcome` is `ResultOutcomeFailed` (the two-shape contract makes this the only alternative to `released`) with a non-empty `error` naming the failure — the push error text, or the failed check names — and `error_category` populated. Evidence: file content (grep) — two Ginkgo specs whose fixture names state the intent (`PushRejectedRemoteAbsent` and `ReviewNotApprovedNoPushRemoteAbsent`) each assert `Outcome == ResultOutcomeFailed` and `Error != ""`; both fixture names are absent from the tree today, so neither grep can pass pre-fix.
- [ ] **(c2) An unverifiable remote is fail-closed.** When the post-push consult returns an `LsRemote` error (or the 30s deadline expires), the outcome is non-success and `error` names the failed remote verification — never `released`. Evidence: file content (grep) — a Ginkgo spec named `RemoteCheckErroredFailsClosed` asserts `Outcome == ResultOutcomeFailed` and that `Error` names remote verification; `grep -c 'RemoteCheckErroredFailsClosed' pkg/steps_ai_review_test.go` returns ≥1 and the name is absent from the tree today.
- [ ] **(c3) An error-with-landed-release is not mis-downgraded.** A push that returns an error while the remote already carries the tag at the expected commit keeps `outcome: released` — the remote is the authority, and `git push` can exit non-zero on a partially-applied atomic push. Evidence: file content (grep) — a Ginkgo spec named `PushRejectedButRemoteConfirms` asserts `Outcome == ResultOutcomeReleased` with the push error recorded in the `## Review` note; the name is absent from the tree today. This is the control that stops the laziest satisfying implementation — "unconditionally write `failed` whenever `Push` errs" — from passing.
- [ ] **(d) Prefix comparison, and a matching tag still yields `released`.** The execution-step post-check compares the observed full SHA against the short expected SHA by prefix — a match upgrades the verdict to `released` with frontmatter `status: completed` / `phase: done` (existing upgrade path), a different commit yields `superseded`, and an empty expected SHA keeps the existing superseded-only behavior. Evidence: file content (grep, discriminating) — `pkg/steps_execution_test.go` contains one spec asserting verdict `released` for a full-SHA fixture whose prefix equals the recorded short SHA and one asserting `superseded` for a different commit; both specs take a fixture name stating the intent (`ShortExpectedMatchesFullObserved` / `DifferentCommitSuperseded`) that is absent from the tree today, so the grep cannot pass pre-fix — the bare symbol `ResolutionVerdictReleased` already appears there at `steps_execution_test.go:1599` and is therefore NOT sufficient evidence; `grep -n 'HasPrefix' pkg/steps_execution.go` returns ≥1 (0 today); `make test` exits 0.
- [ ] **(e) A test asserts the push-failure path specifically.** A rejected push produces a non-success outcome AND the result block names the failure — asserted on the extracted `## Result`, not merely that a `Push` call happened. Evidence: file content (grep) — `pkg/steps_ai_review_test.go` contains the `PushRejectedRemoteAbsent` fixture with assertions on the extracted `ResultOutput.Outcome` and `ResultOutput.Error`; the assertion must not depend on the local variable's name (a correctly-behaving test that binds the extracted value to a different identifier still passes), so the grep anchors on the fixture name plus the two field assertions rather than on `Expect(result.Error)`; `make test` exits 0.
- [ ] **(f) `CHANGELOG.md` gains a `## Unreleased` entry, and the `## Result` contract doc comment is corrected.** The file has no `## Unreleased` section today (its first section header is `## v0.4.10`), so the section is created below the preamble and above the newest `## vX.Y.Z` header, per `docs/dod.md`, with one bullet naming the remote-verified result reconciliation. Evidence: file content (grep) — `grep -n '^## Unreleased' CHANGELOG.md` returns exactly one line, `grep -n '^## ' CHANGELOG.md | head -1` prints that same line number, and the bullet names the fix: `grep -n 'Unreleased' -A 3 CHANGELOG.md | grep -ci 'remote'` returns ≥1. The doc comment at `pkg/result_output.go:13-15` states `outcome: released` means "direct-push succeeded"; the fix redefines it to "the remote confirms the tag", so that comment must say so — `grep -n 'remote' pkg/result_output.go` returns ≥1 (0 today). Leaving it stale would re-create the documented-meaning drift this spec exists to remove.
- [ ] **(g) `make precommit` exits 0.** Evidence: exit code.

Scenario coverage: none added. The behavior is reached by unit and integration tests at the step boundary — both affected steps emit the markdown that is asserted in-process, with the git ops faked — and the runtime proof is the operator replay in the Verification section below. No existing scenario covers this path, and the regression risk it would add is already covered by the push-failure spec required in (e).

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```bash
make precommit                      # exit 0 — fmt, generate, test, lint, vet, vuln, license
make test                           # exit 0

grep -n '^## Unreleased' CHANGELOG.md
grep -n '^## ' CHANGELOG.md | head -1          # must print the `## Unreleased` line number

grep -n 'HasPrefix' pkg/steps_execution.go     # prefix comparison in the post-check
grep -n 'ResultOutcomeFailed' pkg/steps_ai_review.go   # the downgrade write site in the push step
grep -n 'LsRemote' pkg/steps_ai_review.go      # the push path consults the remote
grep -n 'PushReturns' pkg/steps_ai_review_test.go      # the push-failure regression spec
grep -n 'ResolutionVerdictReleased' pkg/steps_execution_test.go  # happy-path verdict control
```

### Operator-executable (runs on the host after PR merge — the bug-verification rung)

Per `bug-workflow.md`: tests passing is NOT acceptable verification evidence for a bug fix. Passing tests prove what the author thought; the reproduction must be replayed against the deployed artifact.

1. **Release and deploy the fixed image.** Four steps, in order — verified against the overlay and both live clusters on 2026-09-13:
   1. Merge to master; the bot cuts `vX.Y.Z` (git tag only).
   2. **Build and push the image by hand** — CI runs `make precommit` only, so the tag builds no image: `VERSION=vX.Y.Z make build upload` from the repo at the release commit. Confirm with `docker manifest inspect docker.io/bborbe/github-releaser-agent:vX.Y.Z`.
   3. **Bump THREE pins in `~/Documents/workspaces/nuke/github-releaser/`** — `Makefile` `AGENT_TAG` (line 29), `values-dev.yaml` `agent.tag`, `values-prod.yaml` `agent.tag`. Confirm the old version is gone: `grep -rn 'v<old>' Makefile values-*.yaml` returns nothing for the stages being shipped.
   4. **Deploy per stage** — `cd ~/Documents/workspaces/nuke/github-releaser && BRANCH=dev make apply` (prod: `BRANCH=master make apply`; `BRANCH=prod` is rejected by `Makefile.env`). The Makefile sets its own `KUBECONFIG=~/.kube/nuke-<stage>`, so it need not be passed.

   ⚠️ **`VERSION=vX.Y.Z make buca` is the WRONG path for this service** (the repo's own `docs/releasing-github-releaser-agent.md` still names it): it pushes to `docker.io` rather than the quant registry the cluster pulls from, applies no manifest, and rolls nothing. Recorded because it would otherwise be discovered at deploy time — the one point in the pipeline where an unverified mechanism costs a live misroute.
2. **Confirm the running artifact is fresh.** The agent is job-style: already-`Succeeded` pods keep the old tag forever. Read the `github-releaser-agent` Config CR for the deployed image reference and confirm it carries the new tag; the next spawned release Job pod is the runtime proof.
3. **Replay the reproduction.** Trigger release tasks for repos whose pushes are rejected by branch protection (GH013 ruleset, GH006 classic protection) and for a repo whose review fails with no push attempted (e.g. an unexpected file in the release commit).
   - `grep -n '"outcome"' "<task file>"` — the value must not be `released`; `"error"` must name the failure (the push rejection text, or the failed check names).
   - Happy-path control: a normal release whose tag does land must show `"outcome": "released"` with the tag present on the remote — `git ls-remote --tags https://github.com/<owner>/<repo>.git refs/tags/<tag>` returns the tag, and the returned commit SHA starts with the recorded `commit_sha`.
4. The three wedged tasks (`Release bborbe-go-skeleton c7eb0f3`, `Release bborbe-go-skeleton ba735cf`, `Release bborbe-notification-controller 855b280`) predate the fix. Disposing of them is operator work via the `github-release-close-obsolete-tasks` sweep, not evidence for or against this fix.

## Desired Behavior

1. **The push step owns the success claim.** After `Push` returns, the step consults the remote for `refs/tags/<planned tag>` and derives the `## Result` outcome from what the remote reports: a tag present at the expected commit (prefix-compared) keeps/confirms `outcome: released`; a tag absent yields a non-success outcome naming the failure; a tag present at a **different** commit (superseded) also yields a non-success outcome naming the observed SHA — the outcome never claims this task's commit released a version it did not. Per the "downgrade writes only `## Result`" constraint, the superseded case leaves frontmatter `status` / `phase` and the `## Review` section to the routing path, so the task still closes `completed` with the existing `## Resolution` / `## Review Warning` record standing. One `glog.V(2)` line records task id, tag, observed remote SHA, and the derived outcome — log wording is agent-decides-at-impl-time; the four recorded facts are not.
2. **A rejected push downgrades the record.** On push error the step still consults the remote before writing: if the remote confirms the tag at the expected commit, the push landed despite the error and `released` stands; if the remote does not carry the tag, `## Result` is rewritten to the non-success shape with `error` naming the push failure and `error_category` from the existing classifier (`git.ClassifyError` — e.g. `protected_branch_rejected` for GH013/GH006). The `## Review` block keeps its `push failed` note and the route to `human_review` is unchanged.
3. **A review rejection that never pushed gets the same treatment, reusing the observation already made.** On the `!approved` branch the existing remote consult runs first (the `checkReviewOverride` check). Its observation — observed SHA, or the consult error — is what the reconciliation consumes; the remote is not asked a second time for the same decision. Absent → `## Result` rewritten to a non-success outcome naming the failed checks; present at the expected commit → the existing `## Review Warning` close-as-completed path stands and `released` is confirmed; present at a different commit → the superseded rewrite of Desired Behavior 1, with the existing record naming the observed SHA also standing. The existing no-op behaviour on a consult error inside that pre-existing check is unchanged and tested — only the post-push consult is fail-closed.
4. **Unverifiable is never success.** An `LsRemote` error or timeout on the post-push consult is fail-closed: the outcome is non-success, with `error` naming that remote verification failed. Silence must not read as a shipped release.
5. **Prefix comparison in the existing post-check.** The execution-step post-check compares the observed SHA against the expected SHA by prefix (observed starts with expected); an empty expected SHA keeps the existing superseded-only behavior. A landed release is verdicted `released` and upgrades the frontmatter to `status: completed` / `phase: done`; only a different commit is verdicted `superseded`.
6. **The two sections can no longer disagree.** Every path that parks a task in `human_review` after a rejected push or a rejected review writes the non-success `## Result` first, so a task page whose remote has no tag never reads `released`.
7. **The changelog records the fix** under a newly created `## Unreleased` section.

## Constraints

- **Prefix comparison is mandatory.** `Result.commit_sha` is a short SHA; `LsRemote` returns a full 40-char SHA. Compare with `strings.HasPrefix(observedSHA, expectedSHA)`. Never restore exact equality.
- **Rejected alternative — no new outcome value.** Adding `pending` / `unverified` to `ResultOutput.Outcome` is rejected: the `## Result` contract has exactly two shapes (doc comment in `pkg/result_output.go`), and a third value changes the contract for existing consumers (`pkg/steps_ai_review.go:192` gates on `result.Outcome != ResultOutcomeReleased`) without adding safety. The fix uses the existing `outcome`, `error_category`, `error` fields — the JSON field set is not extended (the doc comment requires a spec amendment for new fields).
- **No new error-category enum values.** Reuse `git.ClassifyError` and its existing categories.
- **The execution-step post-check keeps its empty-remote no-op.** At execution time the push has not run, so an absent remote tag is the expected state. Do not downgrade there; the post-push verification is the deciding check.
- **The downgrade writes only the `## Result` section** on the `human_review` path — it does not set frontmatter `status` / `phase` (those remain the routing/escalation path's decision), and it does not alter the existing `checkReviewOverride` / `finishReviewOverride` close-as-completed contract for a remote-confirmed tag. The `writeShortCircuit` behavior for pre-existing `failed` outcomes is unchanged.
- **No new retry loop and no new timeout.** One remote consult per decision, bounded by the existing `lsRemoteTimeout` (30s, `pkg/steps_execution.go:28`). One consult **per decision** — not one per run: a decision that already has an observation from a consult made earlier in the same run (the `checkReviewOverride` check on the `!approved` path) reconciles from that observation and does not query the remote again, while a decision whose push just happened consults. The dereference preference (`^{}` line, plain-line fallback for lightweight tags) is already implemented in `LsRemote` / `parseLsRemoteOutput` — reuse it, do not re-implement it.
- **Remote state is read-only here.** No tag deletion, no force-push; `Push` keeps `--atomic` and stays force-free.
- **Git-write safety invariant untouched.** CHANGELOG-only commit via explicit pathspec, deterministic Go tag/push, LLM only classifies (`.dark-factory.yaml` `workflow: direct`, `docs/dod.md` § Git-Write Safety).
- **Existing tests keep passing** (Ginkgo v2 / Gomega, Counterfeiter fakes, no live Claude or network calls in tests).
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)` — no `fmt.Print*`.

## Assumptions

- The push step in the review phase is the only place the agent writes to the remote; execution's local commit and tag never reach GitHub on their own.
- `LsRemote` already returns the dereferenced commit SHA for annotated tags and the plain line for lightweight tags — the verification reuses that behavior instead of adding a new remote helper.
- The task file is the durable record operators read; no second result channel carries a different verdict.
- `Result.commit_sha` stays a short SHA from `git rev-parse --short HEAD`; the fix adapts the comparison, not the recorded value.
- The operator-side `github-release-close-obsolete-tasks` sweep stays available as an independent remote-tag cross-check for tasks that park.

## Failure Modes

| Trigger | Expected behavior | Detection / Recovery |
|---|---|---|
| Push rejected by GH013 ruleset (repro: `Release bborbe-go-skeleton ba735cf`) or GH006 classic protection (repro: `Release bborbe-notification-controller 855b280`) | `## Result` downgraded to a non-success outcome; `error_category` classifies the rejection; `## Review` keeps the `push failed` note; task parks `human_review` | Detection: `grep -c '"outcome": "released"'` on the task file returns 0 and `error` names the rejection. Recovery: operator restores the ruleset bypass for the releaser app, then re-triggers the release check |
| Review returns `!approved` and no push is attempted (repro: `Release bborbe-go-skeleton c7eb0f3`, failed checks `UnexpectedFileChange` + `Faithfulness`) | Remote consult → absent → non-success outcome naming the failed checks; the `## Review` verdict is preserved | Detection: `## Result` and `## Review` no longer contradict — `grep -c '"outcome": "released"'` returns 0. Recovery: operator triage on the failed check |
| `LsRemote` error during the post-push consult (auth broken, DNS, TCP) | Fail-closed: non-success outcome recording that remote verification failed; never `released` | Detection: error text on `## Result` plus the `V(2)` log line. Recovery: re-run the task; a genuinely shipped release is closed by the operator sweep, which compares remote tags independently |
| Remote consult stalls past the 30s `lsRemoteTimeout` | Same fail-closed branch as an error (`context` deadline exceeded) | Detection: log line carrying the timeout error. Recovery: re-run the task |
| GitHub rate-limits or throttles the consult | Same fail-closed branch as an error | Detection: error text names the throttle. Recovery: re-run after the window |
| Tag present at a DIFFERENT commit (superseded) | Not `released` — `## Result` is rewritten to the non-success shape naming the observed SHA, so the page never claims this task's commit shipped the version; the existing superseded record (`## Resolution` / `## Review Warning`) stands and frontmatter `status` / `phase` stay the routing path's decision, so the task closes `completed` | Detection: observed SHA differs from the recorded `commit_sha`, and `grep -c '"outcome": "released"'` on the task file returns 0. Recovery: none needed — the task is closed with the observed SHA recorded |
| Happy-path control: tag present at the expected commit (short expected vs full observed) | `released` confirmed; frontmatter `status: completed` / `phase: done` on the post-check upgrade | Detection: `## Resolution` verdict `released` plus `git ls-remote --tags` showing the tag. Recovery: n/a |
| Execution-step post-check runs pre-push; remote tag legitimately absent | Existing no-op stands — no downgrade, no `released` verdict written | Detection: `V(2)` log verdict `no-op-remote-empty`. Recovery: n/a — the post-push verification is the deciding check |
| A pre-fix task whose `## Result` already reads the optimistic `released` is re-fired | The push step re-derives the outcome from the remote, so the stale optimistic record cannot survive a re-run | Detection: outcome changes on the re-fired page, or the release lands. Recovery: re-fire the task, or close it with the operator sweep |
| Crash between the successful push and the `## Result` rewrite | The record stays optimistic for that one step; a re-fire re-derives from the remote | Detection: task page optimistic while `git ls-remote --tags` shows the tag. Recovery: re-fire the task; the window is bounded to a single step, not to the whole pipeline |
| Review override path: review rejected but the remote already carries the tag | Existing close-as-completed path with `## Review Warning` stands; outcome is confirmed `released` | Detection: `## Review Warning` names the observed SHA. Recovery: n/a |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Prefix comparison in the existing execution-step post-check (short expected vs full observed) + tests asserting the match and the different-commit cases under distinctive fixture names | 5 | (d) | — |
| 2 | Post-push remote verification in the push/review step + `## Result` reconciliation on the push-error, error-with-landed-release, `!approved`-without-push and fail-closed consult-error paths + tests named so each grep is discriminating pre/post fix | 1, 2, 3, 4, 6 | (a), (b), (c), (c2), (c3), (e) | prompt 1 (shares the prefix comparison) |
| 3 | `CHANGELOG.md` `## Unreleased` entry naming the remote-verified reconciliation + the `pkg/result_output.go` contract doc comment + `make precommit` green | 7 | (f), (g) | prompts 1, 2 |

Rationale: prompt 1 and prompt 2 both need the prefix comparison, so it lands first and stays a self-contained, independently verifiable change. Prompt 2 is the behavioral core — it must not be split from its tests, because the reconciliation is only observable through the markdown the step emits; its four fixture names each carry one branch, so a partial implementation fails a named AC rather than slipping through. Prompt 3 is the trivial tail (changelog, contract comment) and can only be verified once the code prompts are in.

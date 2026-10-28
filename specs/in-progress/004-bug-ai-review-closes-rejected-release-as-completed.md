---
status: prompted
approved: "2026-10-28T10:55:17Z"
generating: "2026-10-28T11:18:05Z"
prompted: "2026-10-28T11:18:05Z"
branch: dark-factory/bug-ai-review-closes-rejected-release-as-completed
---

## Summary

- The review step's "nothing to verify" short-circuit fires whenever `## Result` is anything other than `released`, and it unconditionally returns the terminal phase `done`. The task record then reads `status: completed` / `phase: done` without ever asking the remote whether the release exists.
- A rejected release is re-triggered because the failure path leaves the task in `phase: ai_review`. The re-run takes the short-circuit, so the *rejected* release closes as *completed* on the second pass — the first pass's rejection is what creates the second pass's false success.
- Seventeen release records for `bborbe/go-skeleton` read `status: completed` with `"outcome": "failed"`. Thirteen of them claim a version that is not on the remote at all (twelve `v0.7.0`, one `v0.6.3`; the remote's newest tag is `v0.6.2`).
- Fix: the failed-outcome branch consults the remote once for the planned tag before it may reach any terminal phase. Tag absent → the task parks at `human_review` (`status: in_progress`). Tag present → `done` is allowed. The emitted review block reports the checks that were never evaluated as `false` instead of `true`.
- The planned tag is read from `## Plan`, because the `## Result` failure shape carries neither tag nor commit by contract — the short-circuit has to derive its target from somewhere that still exists on a failed page.

## Problem

The agent's review step is the last gate between a rejected release and the task record, and it currently waves rejected releases through. A `## Result` of `failed` means the release did not happen: the push was rejected, the review rejected it, or the remote could not confirm it. The step's response is to write a review block claiming every check passed, notes saying "nothing to verify", and a terminal result. Nothing asks the remote. The task page then states the release completed, and every consumer of that page — the operator, the release sweep, any promotion metric — is told a version shipped that does not exist. Because the failure path also leaves the task in the review phase, the controller re-runs it, and the re-run's only job is to produce that same false success. The pipeline has a mechanism whose entire observable effect is to convert a rejection into a completion.

## Reproduction

Read-only host probes, gathered 2026-10-28. The vault holds the release records; the GitHub API holds the truth about tags.

```bash
gh api "repos/bborbe/go-skeleton/tags?per_page=100" --jq '.[].name'   # newest: v0.6.2
```

Seventeen files under `~/Documents/Obsidian/private-agent/tasks/` named `Release bborbe-go-skeleton*.md` carry BOTH `status: completed` in frontmatter AND `"outcome": "failed"` in their result block. Grouped by the version each one planned:

| Planned version (`next_version_header`) | Records | Present on the remote? |
|---|---|---|
| `## v0.7.0` | 12 | no |
| `## v0.6.3` | 1 | no |
| `## v0.6.2` | 1 | yes |
| `## v0.4.4` | 1 | yes |
| `## v0.4.3` | 1 | yes |
| `## v0.4.0` | 1 | yes |

Thirteen records assert a completed release for a version the repository never received. The other four planned a version that does exist — they are completed records whose outcome text says the opposite, which is the same defect seen from the harmless side.

Verbatim in content, reflowed for width, from `Release bborbe-go-skeleton 032260d.md` (one of the twelve) — section name then body:

```
Result:  {"outcome": "failed", "path": "direct-push", "error_category": "unknown", "error": "failed checks: Faithfulness"}

Review:  {"approved": true, "checks": {"tag_exists": true, "tag_at_expected_sha": true,
          "changelog_header_rewritten": true, "faithfulness_ok": true, "unexpected_file_change": false},
          "notes": "execution step recorded failure; nothing to verify", "overall": "pass"}
```

The same page's frontmatter reads `status: completed` / `phase: done`. `v0.7.0` is not on the remote.

Note what the failed result block does **not** carry: no `tag`, no `local_tag`, no `commit_sha`, no `workdir`. This is not an accident of one run — it is the documented contract (`pkg/result_output.go:18-22`: "Outcome='failed' … CommitSHA + Tag empty"), and none of the seventeen files carries a `commit_sha`. Any fix that assumes the failed page still names its tag or its release commit will not fire in production.

## Expected vs Actual

| | |
|---|---|
| **Expected** | A `failed` outcome may reach the terminal phase only after the remote confirms the planned tag exists. Without that confirmation the task parks for a human, and the review block reports the checks that were never evaluated as not-run. |
| **Actual** | `pkg/steps_ai_review.go:203-205` routes every non-`released` outcome into `writeShortCircuit` (`:699-723`), which writes `approved: true`, all four check booleans `true`, `overall: pass`, notes `"execution step recorded failure; nothing to verify"`, and returns `AgentStatusDone` / `NextPhase: "done"` without any remote call. The deliverer maps `Done` + `done` to `status: completed` (`github.com/bborbe/agent@v0.112.0` `delivery/result-deliverer.go:263-273`). |

## Why this is a bug

The review block's own vocabulary says the checks passed. `tag_exists: true` on a page whose release was rejected is a false statement about the remote, written by the component that exists to check it. That is not a missing feature; it is a report that contradicts the same page's result block.

The loop is what makes it durable. On a rejection the step returns `AgentStatusFailed` / `NextPhase: human_review` (`:582-599`), but the deliverer preserves the incoming phase for `Failed` (`result-deliverer.go:294-307`), so the task stays `phase: ai_review` / `status: in_progress` and the executor re-triggers that phase. The re-run reads the `failed` result the previous run wrote and takes the short-circuit — so the pipeline's retry mechanism is what turns a rejection into a completion. Closing the task on the first pass would have been merely wrong; re-running it makes it reliably wrong.

The false completion also destroys the signal an operator needs. A parked task says "this needs a decision". A completed task says "done, move on". Thirteen of those records are the second message applied to a version that was never cut.

## Goal

A `failed` release outcome can no longer become a completed task record without the remote confirming the planned tag. When the remote lacks it, the record parks at `human_review` with `status: in_progress`, the review block reports the unrun checks as not-run and names the short-circuit, and the record is no longer re-triggered into a terminal phase. When the remote carries the planned tag, the record still closes — a version that shipped is a version that shipped. No task page anywhere in the pipeline reports a check as passing that the step never performed.

## Non-goals

- Do NOT change the shared agent library's `Failed` / `NextPhase` mapping. The fix adapts this repo's returned result, not the framework.
- Do NOT repair the seventeen already-wedged records. They are operator work (re-run, or close via the release sweep); this spec stops new ones.
- Do NOT re-enable the structural checks (`TagExists`, `TagAtExpectedSHA`, `ChangelogHeaderRewritten`) on the pre-push path — they were disabled deliberately (`pkg/steps_ai_review.go:229-237`) and that decision is untouched.
- Do NOT deploy or replay a release. The dev deploy and the replay of these records belong to the parent task's Definition of Done, exactly as in spec 003.
- Do NOT add an opt-out flag, environment variable, or config key that disables the consult — invariant; if a future consumer demands variation, that is a separate spec.
- Do NOT add a retry, a second consult, or a new timeout.

## Acceptance Criteria

- [ ] **(a) A `failed`-result run whose remote lacks the planned tag never reaches the terminal phase.** A Ginkgo spec named `ShortCircuitRemoteAbsentParksHumanReview` runs the review step on a `failed`-result fixture whose remote answers "no such tag", and asserts `result.NextPhase == "human_review"` and `result.Status == agentlib.AgentStatusDone` (the assertion is on `NextPhase`, not on `Status`, because `Done` is also the parking status). Evidence: file content plus a recorded red run — all three parts required. (i) `grep -c 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go` returns ≥1 and the name is absent from the tree today (0 matches), so the grep cannot pass pre-fix. (ii) The named block itself carries the assertion: `grep -A 12 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'NextPhase'` returns ≥1 **and** those same 20 lines contain the `human_review` literal — so an empty `It` bearing the name does not satisfy this AC. (iii) **Revert probe**: restoring `writeShortCircuit`'s early `AgentStatusDone` / `NextPhase: "done"` return makes exactly that named spec fail; record the failing run's output in the prompt's final summary — this pipeline opens no PR (`.dark-factory.yaml` sets `pr: false`), so the summary is the channel — and carry it into the parent task's `# Progress`. `make test` exits 0 on the fixed tree.
- [ ] **(b) Control — a present planned tag still closes the task.** A Ginkgo spec named `ShortCircuitTagPresentCloses` runs the same fixture with the remote returning a commit SHA for the planned tag and asserts `result.NextPhase == "done"` with `review.Checks.TagExists == true`. Evidence: file content plus a recorded red run — all three parts required. (i) The fixture name is absent from the tree today. (ii) The named block carries the assertion: `grep -A 12 'ShortCircuitTagPresentCloses' pkg/steps_ai_review_test.go | grep -c '"done"'` returns ≥1 **and** those same 20 lines contain `TagExists`, so a named-but-empty `It` fails this AC. (iii) **Revert probe**: an unconditional-park implementation (always `NextPhase: "human_review"`) makes exactly that named spec fail; record the failing run's output in the prompt's final summary and carry it into the parent task's `# Progress`. This is the control that stops the laziest satisfying implementation — "park every failed outcome" — from passing. `make test` exits 0.
- [ ] **(c) The tag-at-commit check is never reported as evaluated on this path.** Both short-circuit fixtures assert `review.Checks.TagAtExpectedSHA` is `false`, and the hardcoded `true` disappears from the branch. Evidence: file content (grep + assertion, discriminating) — `sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -c 'TagAtExpectedSHA:[[:space:]]*true'` returns 0 (returns 1 today); `make test` exits 0. A `failed` page records no release commit, so this check has nothing to compare against and must never read as a pass.
- [ ] **(d) The review block reports what was observed, and the remote was actually asked.** The absent fixture asserts `review.Checks.TagExists == false`, `review.Notes` contains the literal `short-circuit`, and `fakeOps.LsRemoteCallCount() == 1` — exactly one consult, and more than zero. Evidence: file content (grep + assertion, discriminating) — `grep -A 12 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'LsRemoteCallCount'` returns ≥1 and those same 20 lines contain the `short-circuit` notes token, so a named-but-empty spec fails this AC; the call count is exactly `0` on this path today, so the assertion discriminates; and deleting the `## Review` block fails the spec on the extraction error — `extractReview` asserts `Expect(err).NotTo(HaveOccurred())` at `pkg/steps_ai_review_test.go:227-235` — which is the intended signal. The notes assertion is kept because it pins the mandated `short-circuit` token, not because it is what catches a deleted block. `make test` exits 0.
- [ ] **(e) Two runs, deliverer layer: the record resolves to `in_progress` / `human_review`, not `completed` / `done`.** A spec named `TwoRunChainParksHumanReview` runs the review step twice over the same markdown and publishes the second result through `factory.CreateKafkaResultDeliverer` with a fake sync producer, then unwraps the published `cdb.CommandObject` (`base.ParseEvent`) into the task object and asserts `Frontmatter["status"] == "in_progress"` and `Frontmatter["phase"] == "human_review"`, and that neither value is `completed` / `done`. Evidence: kafka message payload (the published task object's frontmatter) plus a content probe — the fixture name is absent from the tree today; `grep -A 30 'TwoRunChainParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'Frontmatter'` returns ≥2 (one per asserted key), so a named-but-empty spec fails this AC; `make test` exits 0. This is the AC that exercises the actual status resolution rather than the step's return value.
- [ ] **(f) `CHANGELOG.md` gains a `## Unreleased` entry.** The file has no `## Unreleased` section today (its first top-level section header is `## v0.6.1` at line 8), so the section is created below the preamble and above the newest version header, per `docs/dod.md`, with one bullet naming the short-circuit fix. Evidence: file content (grep) — `grep -n '^## Unreleased' CHANGELOG.md` returns exactly one line, and `grep -n '^## ' CHANGELOG.md | head -1` prints that same line number; `grep -n 'Unreleased' -A 3 CHANGELOG.md | grep -ci 'remote'` returns ≥1.
- [ ] **(g) `make precommit` exits 0.** Evidence: exit code.

Scenario coverage: none added. The behaviour is reached at the step boundary with the git ops faked, and the status resolution is reached through the real deliverer in AC (e) — both in-process, no cluster, no network. No existing scenario covers this path, and the runtime proof is the operator replay named in Verification, which is the parent task's DoD.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```bash
make precommit                                  # exit 0 — fmt, generate, test, lint, vet, vuln, license
make test                                       # exit 0

grep -n 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go   # ≥1
grep -n 'ShortCircuitTagPresentCloses' pkg/steps_ai_review_test.go               # ≥1
grep -n 'TwoRunChainParksHumanReview' pkg/steps_ai_review_test.go                # ≥1
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go \
  | grep -c 'TagAtExpectedSHA:[[:space:]]*true'                                  # 0
grep -n '^## Unreleased' CHANGELOG.md                                            # exactly one line
grep -n '^## ' CHANGELOG.md | head -1                                            # the same line
```

### Operator-executable

Deployment and replay are the parent task's Definition of Done, not this spec's — matching spec 003's split. The replay fixture is already on disk and needs no new data: after the fixed image runs, re-triggering the thirteen records whose planned version is absent (`v0.7.0` ×12, `v0.6.3` ×1) must leave each at `phase: human_review` / `status: in_progress` with `"tag_exists": false`, and re-triggering the four whose planned version is present (`v0.4.0`, `v0.4.3`, `v0.4.4`, `v0.6.2`) must leave them `status: completed` / `phase: done`. Read each record with `grep -n '"tag_exists"' "<task file>"` and `grep -nE '^(status|phase):' "<task file>"`.

## Desired Behavior

1. **The failed-outcome branch consults the remote before any terminal decision.** When the result outcome is not `released`, the step resolves the planned tag, asks the remote once for that tag, and only then decides between closing and parking. A run that cannot consult never reaches a terminal phase — an unasked remote is not evidence of a release.
2. **The planned tag comes from the plan section, through the derivation that already exists.** The result failure shape carries neither tag nor commit by contract, so the tag is taken from the plan's next-version header via the same derivation execution already uses (`strings.TrimPrefix(plan.NextVersionHeader, "## ")`). A second derivation, a new field, or a fallback to a re-derived version is out.
3. **Tag absent parks the task.** The step returns `AgentStatusDone` with `NextPhase: "human_review"`, which the deliverer resolves to `status: in_progress` / `phase: human_review`. `AgentStatusFailed` is explicitly not used here: it preserves the incoming `phase: ai_review`, which is what re-triggers the record forever. The step does not write frontmatter itself on this path.
4. **Tag present closes the task.** `AgentStatusDone` / `NextPhase: "done"` is allowed when the remote reports a commit for the planned tag, and only then. A version that is on the remote is released; the task's purpose is the version.
5. **An unverifiable remote parks the task.** A consult error, a deadline expiry, or a skipped consult (missing clone URL, ref, or planned tag) parks exactly like an absent tag, and the review notes name the failed verification. Silence must not read as a shipped release.
6. **The review block reports only what was observed.** On the short-circuit path the emitted review carries `tag_exists` reflecting the observation, `tag_at_expected_sha: false` always (the check is never evaluated on a failed page), and notes containing `short-circuit` plus the planned tag and the observed state. `approved`, `overall`, and the remaining check booleans are unchanged from today — the parking is expressed by the returned `NextPhase`, not by a failed review verdict, and this spec does not redesign the block's verdict semantics; the exact notes wording beyond the mandated `short-circuit` token is agent decides at impl time.
7. **One consult, one log line.** Exactly one remote consult per run on this path, bounded by the existing `lsRemoteTimeout`, with one `glog.V(2)` line recording task id, planned tag, observed SHA, and the decision. Log wording is agent decides at impl time; the four recorded facts are not.
8. **The changelog records the fix** under a newly created `## Unreleased` section.

## Constraints

- **The fix lands in this repo only.** Changing the shared agent library's `Failed` handling (which preserves the phase and therefore causes the re-trigger) is out of scope. This repo parks the task at `human_review` instead, which the executor does not re-trigger.
- **No new result outcome value, no new result field, no new error-category value.** The result contract has exactly two shapes and a doc comment stating that new fields require a spec amendment. Reuse `outcome`, `error_category`, `error` only.
- **Reuse the existing consult, do not re-implement it.** `consultRemoteTag` already owns auth injection, the `lsRemoteTimeout` bound, and the skip guard; `LsRemote` already owns the dereferenced-commit preference (`^{}` line, plain-line fallback for lightweight tags) and returns `("", nil)` for a missing tag. One consult per decision, no retry loop, no new timeout.
- **Reuse the existing planned-tag derivation.** Execution derives the tag three times as `strings.TrimPrefix(plan.NextVersionHeader, "## ")`; the short-circuit uses the same expression or a shared helper extracted from it. No second derivation.
- **Remote state is read-only here.** No tag creation, no deletion, no force-push. The fix only asks a question.
- **The derived tag reaches `git ls-remote` as a separate argv element, never a shell string.** `refs/tags/<tag>*` is built in Go (`pkg/git/os_exec_git_ops.go:237`, already `#nosec G204`) and matched by exact suffix in `parseLsRemoteOutput`, so a malformed `next_version_header` can at worst ask about a tag that does not exist — it cannot inject a command. The token is injected into the URL, never into the log line (`git.RedactToken`).
- **The step writes no frontmatter on this path.** Status and phase are the deliverer's decision on both the park and the close branch; writing them in the step would duplicate the mapping that AC (e) exercises.
- **Two existing tests encode the pre-fix behaviour and are rewritten, not preserved.** `pkg/steps_ai_review_test.go:446` (`"no HTTP calls, approved:true, status:done, next_phase:done"`) asserts `NextPhase == "done"`, `TagExists == true`, and the `"nothing to verify"` notes; `pkg/steps_ai_review_test.go:1243-1244` (`"Result.Outcome=failed short-circuits without LsRemote (no ## Review Warning)"`) asserts `LsRemoteCallCount() == 0` and `NextPhase == "done"`. Both assert exactly the behaviour this spec removes. Their replacement is AC (a)/(d); leaving them untouched makes `make test` fail, which is the intended signal.
- **This spec supersedes spec 003's constraint for this path, deliberately.** Spec 003 stated "the `writeShortCircuit` behavior for pre-existing `failed` outcomes is unchanged" — that was scoped to spec 003's own change, which shipped as v0.6.1 and left the short-circuit as it was. Spec 003 promised not to touch it; this spec exists to touch it.
- **The short-circuit fixtures must carry `clone_url` and `ref` frontmatter *and* a `## Plan` carrying `next_version_header`.** Without the frontmatter the consult's skip guard fires and `LsRemoteCallCount()` stays 0; without the plan header the derived tag is empty, so the same guard fires a second way. Either omission fails AC (a)/(d) — deliberate, so a fixture that never asks the remote, or never names a tag to ask about, cannot satisfy the ACs. The existing `taskWithFailedResult()` fixture (`pkg/steps_ai_review_test.go:100-118`) carries none of the three and must be extended, not reused as-is.
- **Existing test conventions hold.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network, no live Claude calls.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)`; tokens are redacted before they reach a log line (`git.RedactToken`), as the existing consult already does.

## Assumptions

- **Tag presence is the only attributable evidence available on a failed page.** The contract forbids `commit_sha` and `tag` on the failure shape, so no comparison against a release commit is possible. A present planned tag therefore means the version shipped; an absent one means it did not. This is the same test the operator's release sweep applies.
- `LsRemote` returns `("", nil)` for a missing tag and a 40-char commit for a present one — verified in `pkg/git/os_exec_git_ops.go` (doc comment and `parseLsRemoteOutput`), not assumed.
- Parking at `human_review` is this repo's established "needs an operator" state — `finishHumanReview` is the pre-existing path that uses it — and the loop in the reproduction comes from `Failed` preserving `phase: ai_review`, not from `human_review`. The executor's trigger-phase allowlist lives in the deploy-side agent service, not this repo, so it is not asserted here.
- The task file is the durable record operators read; no second channel carries a different verdict.
- `next_version_header` is populated on every task that reached the review phase — planning writes it and the review step already treats a missing plan as a fatal error.

## Failure Modes

| Trigger | Expected behavior | Detection / Recovery |
|---|---|---|
| Rejected release re-triggered (the reproduction: 12 records planning `v0.7.0`) | Consult finds no tag → park `human_review` / `in_progress`; record never reaches `completed` | Detection: `grep -nE '^(status\|phase):' <task file>` shows `in_progress` / `human_review`, and `"tag_exists": false` in the review block. Recovery: operator triages the original failure; the release sweep can close a task whose version does land later |
| Release genuinely landed but the page says `failed` (push error that still applied) | Consult finds the tag → close `completed` / `done` | Detection: `"tag_exists": true` with the phase resolving to `done`. Recovery: none needed |
| `LsRemote` error, auth failure, or DNS/TCP problem | Fail-closed: park `human_review`, notes name the failed verification; never `done` | Detection: notes text plus the `V(2)` log line carrying the redacted error. Recovery: re-run the task; a re-run re-consults |
| Consult stalls past `lsRemoteTimeout` | Same fail-closed branch (context deadline exceeded) | Detection: log line carrying the timeout error. Recovery: re-run |
| GitHub rate-limits the consult | Same fail-closed branch | Detection: notes and log name the throttle. Recovery: re-run after the window |
| Frontmatter lacks `clone_url`, `ref`, or the plan carries no next-version header | Skip guard fires, consult skipped, park `human_review` with notes naming the missing input; never `done` | Detection: notes name the skip. Recovery: operator inspects the task page; a malformed page cannot close itself |
| Crash between the consult and the result return | No terminal write happened, so the record keeps its `failed` result and non-terminal phase | Detection: page unchanged after a crash. Recovery: re-run; the consult repeats — it is read-only, so repeating is free |
| Two workers run the same task concurrently | Both consult; both park or both close on the same observation — the operation is idempotent and writes nothing to the remote | Detection: duplicate review writes, last one wins. Recovery: none needed |
| An already-wedged record (`status: completed`) is re-triggered | The consult runs before the decision, so the stale completion cannot survive a re-run: the page is rewritten to park when the tag is absent | Detection: the record's frontmatter changes on re-trigger. Recovery: none — this is the repair path for pre-fix records |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The consult + planned-tag derivation + the park/close/fail-closed decision in the failed-outcome branch, with the `ShortCircuitRemoteAbsentParksHumanReview` and `ShortCircuitTagPresentCloses` fixtures | 1, 2, 3, 4, 5 | (a), (b) | — |
| 2 | Review-block reporting (observed `tag_exists`, `tag_at_expected_sha` never true, `short-circuit` notes, one `V(2)` line) + rewriting the two pre-fix tests | 6, 7 | (c), (d) | prompt 1 (same fixtures) |
| 3 | Deliverer-layer two-run chain, `CHANGELOG.md` `## Unreleased` entry, `make precommit` green | 8 | (e), (f), (g) | prompts 1, 2 |

Rationale: prompt 1 is the behavioural core and carries the decision table — splitting it from its own fixtures would make the decision untestable. Prompt 2 layers the reporting on top of the same fixtures, which is why it follows rather than parallelises: the fixtures do not exist until prompt 1 writes them. Prompt 3 is the tail: the deliverer test needs the parked result to exist, and the changelog entry can only be honest once the code is in.

## Do-Nothing Option

The defect costs a version. Every rejected release silently converts to a completed record, the repository never receives the version its task page claims, and the operator's only signal is a version mismatch discovered by accident — as the thirteen `v0.7.0` records were. Doing nothing also means the retry mechanism keeps doing this: every re-trigger of a failed release is another chance to write a false completion, so the record count grows with each attempt rather than staying put. The alternative fix — teaching the shared agent library to honour `NextPhase` on `Failed` — would repair every agent, but it changes framework semantics for all consumers and is out of this repo's scope; parking at `human_review` achieves the same stop locally.

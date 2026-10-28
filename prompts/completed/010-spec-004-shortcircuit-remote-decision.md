---
status: completed
spec: [004-bug-ai-review-closes-rejected-release-as-completed]
summary: 'The failed-outcome short-circuit now derives the planned tag from ## Plan, consults the remote once via consultRemoteTag, parks at Done/human_review on an absent, errored, or skipped consult, and closes Done/done only on a positive observed SHA; two specs rewritten and three new fixtures added.'
execution_id: github-releaser-agent-ai-review-shortcircuit-exec-010-spec-004-shortcircuit-remote-decision
dark-factory-version: v0.198.7
created: "2026-10-28T11:02:40Z"
queued: "2026-10-28T11:18:12Z"
started: "2026-10-28T12:52:07Z"
completed: "2026-10-28T12:58:09Z"
---

# Make the failed-outcome short-circuit consult the remote before any terminal decision

<summary>
- A release whose `## Result` says `failed` can no longer close the task by default. The review step asks the remote once about the planned tag before it decides anything.
- When the remote does not carry the planned tag, the task parks for a human instead of claiming the release completed.
- When the remote does carry the planned tag, the task still closes as done — a version that shipped is a version that shipped.
- An unasked or unreachable remote parks the task too: silence is never read as a shipped release.
- The planned tag is taken from the plan section, because the failed result shape carries neither a tag nor a commit by contract.
- The retry loop stops: parking uses the status the controller does not re-trigger, so a rejected release cannot be re-run into a false completion.
- Two existing specs that encode the old "close on failure" behaviour are rewritten, and two new specs pin the park branch and its control.
- Nothing is written to the remote, no new result field or outcome value is introduced, and no new timeout or config key appears.
- This is the first of three prompts; it changes the routing decision only. What the review block reports is the next prompt.
</summary>

<objective>
Stop the AI-review step from converting a rejected release into a completed task record. On the `Result.Outcome != released` branch, derive the planned tag from `## Plan`, consult the remote exactly once through the existing `consultRemoteTag`, and only then decide: tag absent (or the consult skipped/errored) parks the task at `human_review`; tag present closes it at `done`. The step must never reach a terminal phase on an unasked remote, and must never use `AgentStatusFailed` on this path — that status preserves `phase: ai_review` and re-triggers the record forever.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done this prompt is validated against.

Read these files BEFORE changing anything:
- `pkg/steps_ai_review.go` — `Run` (the `if result.Outcome != ResultOutcomeReleased` branch that currently calls `s.writeShortCircuit(ctx, md)`; note `plan` is already extracted above it), `writeShortCircuit` (the change site: it writes a `ReviewOutput` and returns `AgentStatusDone` / `NextPhase: "done"` with no remote call), `consultRemoteTag` (the existing consult: it owns the skip guard for missing `clone_url` / `ref` / `tag`, injects auth via `injectToken(normalizeCloneURLToHTTPS(cloneURL), s.ghToken)`, bounds the call with `lsRemoteTimeout`, and returns `remoteObservation{Consulted, SHA, Err}`), `finishHumanReview` (the established parking path: writes `## Review`, returns `AgentStatusFailed` / `human_review` — do NOT copy its status here), `finishApproved` (the `Done` / `"done"` close shape), and `checkFaithfulness` (the existing `strings.TrimPrefix(plan.NextVersionHeader, "## ")` derivation at the `headingText :=` line — the same expression this prompt reuses).
- `pkg/plan_output.go` — `PlanOutput.NextVersionHeader` (`json:"next_version_header,omitempty"`), the field the planned tag comes from.
- `pkg/result_output.go` — `ResultOutput` and its two-shape doc comment: `Outcome="failed"` carries `ErrorCategory` + `Error` and leaves `CommitSHA` / `Tag` empty. That is why the tag must come from the plan, not from the result.
- `pkg/git/os_exec_git_ops.go` — `LsRemote(ctx, cloneURL, ref, tag)`: it prepends `refs/tags/` itself and appends the `*` glob, so the caller passes the BARE tag, and a missing tag returns `("", nil)`.
- `pkg/steps_ai_review_test.go` — `taskWithFailedResult()` (the fixture this prompt extends: it currently has no `clone_url`, no `ref`, and a `## Plan` with only `{"outcome":"ready"}`), `runStep`, `extractReview`, and the two specs that encode the pre-fix behaviour: `Context("7g. Short-circuit: Result.outcome != released")` → `It("no HTTP calls, approved:true, status:done, next_phase:done")`, and inside `Context("review-warning override (spec 064)")` the last spec `It("Result.Outcome=failed short-circuits without LsRemote (no ## Review Warning)")`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-linting-guide.md`
</context>

<requirements>

## 1. Pass the plan into the short-circuit

In `Run`, the failed-outcome branch now hands `plan` over:

```go
if result.Outcome != ResultOutcomeReleased {
	return s.writeShortCircuit(ctx, md, plan)
}
```

New signature (unexported, one call site):

```go
func (s *aiReviewStep) writeShortCircuit(
	ctx context.Context,
	md *agentlib.Markdown,
	plan *PlanOutput,
) (*agentlib.Result, error)
```

Do not add a `*ResultOutput` parameter, a tag parameter, or a frontmatter read of `## Result` — the tag is derived from the plan (requirement 2).

## 2. Derive the planned tag with the existing derivation

Inside `writeShortCircuit`:

```go
tag := strings.TrimPrefix(plan.NextVersionHeader, "## ")
```

This is the same expression execution and the faithfulness check already use. Do not add a second derivation, do not fall back to `plan.NextVersion`, and do not re-read `## Unreleased`. An empty `tag` is a valid input — it trips the consult's skip guard and parks (requirement 4).

## 3. One consult, through the existing helper

```go
obs := s.consultRemoteTag(ctx, md, tag)
```

Exactly one consult per run on this path. Do not call `s.ops.LsRemote` directly, do not re-implement auth injection, do not add a retry, do not add a new timeout, and do not call `git.ClassifyError` here. `consultRemoteTag` already owns the skip guard and the `lsRemoteTimeout` bound; reuse it unchanged.

## 4. The decision table

Derive the return value from the observation only:

| Observation | Return |
|---|---|
| `obs.Consulted == false` (skip guard: missing `clone_url`, `ref`, or planned tag) | `&agentlib.Result{Status: agentlib.AgentStatusDone, NextPhase: "human_review"}` |
| `obs.Err != nil` (consult error or `lsRemoteTimeout` deadline) | same park: `AgentStatusDone` / `"human_review"` |
| `obs.Consulted && obs.Err == nil && obs.SHA == ""` (remote does not carry the tag) | same park: `AgentStatusDone` / `"human_review"` |
| `obs.Consulted && obs.Err == nil && obs.SHA != ""` (remote carries the tag) | `&agentlib.Result{Status: agentlib.AgentStatusDone, NextPhase: "done"}` |

Hard rules for this branch:
- **Never `agentlib.AgentStatusFailed`.** It preserves the incoming `phase: ai_review` in the deliverer, which re-triggers the record forever. `AgentStatusDone` + `NextPhase: "human_review"` resolves to `status: in_progress` / `phase: human_review`, which the executor does not re-trigger.
- **Never write frontmatter.** Do not set `md.Frontmatter["status"]` or `["phase"]`; status and phase are the deliverer's decision on both branches (that mapping is exercised in prompt 3).
- **Never rewrite `## Result`.** The page already carries `outcome: failed`; this branch only writes `## Review` and returns.
- **Never reach `"done"` without a positive observation.** A consult error, a deadline, or a skipped consult is not evidence of a release.
- Use `"human_review"` and `"done"` as the literal `NextPhase` strings in the tests, and `string(domain.TaskPhaseHumanReview)` for the park value in the function — that is what `finishHumanReview` already returns (`pkg/steps_ai_review.go:596`), so the two parking paths match; `finishApproved` and `finishReviewOverride` return `NextPhase: "done"` as a literal, so keep that one literal. Keep the test assertions on the `"human_review"` literal so the verification greps stay meaningful.
- **Evaluate `obs.Err != nil` before the `obs.SHA` checks.** An error outranks a non-empty SHA — a consult that errored has no trustworthy observation, so a partial result must never reach `"done"`.
- `ctx` must be threaded into the consult (it already is) — no `context.Background()`.

Leave the `ReviewOutput` literal inside `writeShortCircuit` exactly as it is today in this prompt (`Approved: true`, `Overall: OverallPass`, all four check booleans `true`, notes `"execution step recorded failure; nothing to verify"`). Prompt 2 replaces those values with the observed ones; changing them here would put the reporting half in the wrong prompt and would make the two rewritten specs below assert a state this prompt does not yet produce.

## 5. Update the comments that describe the old behaviour

- `Run`'s sequence comment, step 2 (currently "If Result.Outcome != `released` → short-circuit approved=true") must state the new contract: the failed-outcome branch derives the planned tag from `## Plan`, consults the remote once, and parks at `human_review` unless the remote carries the tag.
- `writeShortCircuit`'s doc comment (currently "no checks to run, the spec says approved=true with overall=pass") must describe the consult, the decision table, and the fact that the park uses `Done` / `human_review` deliberately — with the one-clause reason (a `Failed` status preserves `phase: ai_review` and re-triggers).
- No comment may continue to assert that a non-`released` outcome needs no verification.

## 6. Extend the failed-result fixture

`taskWithFailedResult()` in `pkg/steps_ai_review_test.go` must carry all three inputs the consult and the derivation need, or the skip guard fires and every new assertion below is vacuous:

- frontmatter gains `clone_url: https://github.com/bborbe/example.git` and `ref: main` (same values as `taskWithResultFull`);
- the `## Plan` JSON gains `"next_version":"0.7.0"` and `"next_version_header":"## v0.7.0"`;
- the `## Result` block stays exactly as today: `{"outcome":"failed","error_category":"unknown","error":"clone failed"}` — no `tag`, no `commit_sha`, no `workdir`. That absence is the contract this fix exists to work with; do not add them to make the fixture easier.

Both rewritten specs and both new specs below use this extended fixture.

## 7. Rewrite the two specs that encode the pre-fix behaviour

These two assert exactly what this spec removes. Leaving them untouched makes `make test` fail — the intended signal is that they must be rewritten, not deleted silently.

**7a. `Context("7g. Short-circuit: Result.outcome != released")`** — keep the context, rewrite the `It`:
- Keep the three HTTP-client assertions (`fakeClient.TagExistsCallCount()`, `ResolveTagCommitCallCount()`, `FetchChangelogCallCount()` all `0`) — this path still makes no GitHub REST call.
- Add `fakeOps.LsRemoteReturns("", nil)` at the top of the spec (an explicit absent-remote stub; do not rely on the fake's zero value).
- Assert `result.Status == agentlib.AgentStatusDone` and `result.NextPhase == "human_review"`.
- Assert `fakeOps.LsRemoteCallCount() == 1`.
- Assert `extractReview(md).Approved` is true and `extractReview(md).Checks.ChangelogHeaderRewritten` is true (both unchanged by this prompt).
- Remove the `TagExists` / `TagAtExpectedSHA` / `"nothing to verify"` assertions from this spec — prompt 2 owns those observed values and re-asserts them in the new fixtures.
- Rename the `It` description so it no longer claims `status:done, next_phase:done`.

**7b. The last spec of `Context("review-warning override (spec 064)")`** (`It("Result.Outcome=failed short-circuits without LsRemote (no ## Review Warning)")`) — rewrite it in place:
- Change its stub from `fakeOps.LsRemoteReturns("abc123", nil)` to `fakeOps.LsRemoteReturns("", nil)`. The old stub would make the tag present and the task close — the opposite of what this spec now proves.
- Assert `result.Status == agentlib.AgentStatusDone`, `result.NextPhase == "human_review"`, `fakeOps.LsRemoteCallCount() == 1`.
- Keep `Expect(extractReview(md).Approved).To(BeTrue())` and `Expect(extractReviewWarning(md)).To(BeNil())`.
- Rename the `It` description so it no longer claims "without LsRemote".
- Rewrite the comment above that `It` too — it currently reads `// (f) short-circuit path (Result.Outcome == "failed") is unchanged. The new branch is unreachable. LsRemote must NOT be called.` and must now say the short-circuit consults the remote once for the planned tag, that an absent tag parks at `human_review`, and that no `## Review Warning` is written.

Do not touch any other spec in the file.

## 8. Add the two named fixtures

Add `Context("short-circuit remote decision (spec 004)", func() { ... })` inside `Describe("Run")`, after the `Context("post-push remote reconciliation (spec 003)")` block and before the closing of `Describe("Run")`. Both fixtures inherit the `Describe("Run")` `BeforeEach` (tmpdir + faithful LLM stub); neither needs a workdir, because the failed result carries none and the faithfulness / diff checks never run on this branch.

Put each fixture name verbatim in its `It(...)` description so it is greppable, and keep that spec's assertions within 12 lines of the name (setup included) — the verification greps below window 12 lines, and the three fixtures sit adjacent, so a wider window would let a neighbour's assertion satisfy the grep.

**8a. `ShortCircuitRemoteAbsentParksHumanReview`** (AC (a))
- Stub `fakeOps.LsRemoteReturns("", nil)`.
- `result, md := runStep(taskWithFailedResult())`.
- Assert `result.Status == agentlib.AgentStatusDone` and `result.NextPhase == "human_review"`.
- Assert `fakeOps.LsRemoteCallCount() == 1`.
- Assert the consult asked for the BARE derived tag: `_, _, _, tagArg := fakeOps.LsRemoteArgsForCall(0)` then `Expect(tagArg).To(Equal("v0.7.0"))`. This is what pins requirement 2 — a fix that passes `""` or `"refs/tags/v0.7.0"` fails here.
- Do not assert on the check booleans or the notes in this prompt.

**8b. `ShortCircuitTagPresentCloses`** (AC (b) — the control that stops "park every failed outcome" from passing)
- Stub `fakeOps.LsRemoteReturns("abc1234e3cca37862f4e612a7b14c4e00af6b935", nil)`.
- `result, md := runStep(taskWithFailedResult())`.
- Assert `result.Status == agentlib.AgentStatusDone` and `result.NextPhase == "done"`.
- Assert `fakeOps.LsRemoteCallCount() == 1`.
- Assert `extractReview(md).Checks.TagExists` is true and `extractReview(md).Approved` is true.
- Assert the marshaled page still carries the fixture's result body verbatim: `body, err := md.Marshal(context.Background())`, `Expect(err).NotTo(HaveOccurred())`, `Expect(body).To(ContainSubstring(`"outcome":"failed"`))`. The fixture writes compact JSON and `Markdown.Marshal` renders section bodies untouched, so the spaced form never appears — do not add a space to the assertion, and do not edit the fixture's `## Result` to make it match (requirement 6 forbids that).

**8c. `ShortCircuitConsultErrorParksHumanReview`** (DB 5 — fail-closed on an unverifiable remote)
- Stub `fakeOps.LsRemoteReturns("", errors.New("ls-remote boom"))` (`errors` is already imported in this file).
- `result, md := runStep(taskWithFailedResult())`.
- Assert `result.Status == agentlib.AgentStatusDone` and `result.NextPhase == "human_review"`.
- Assert `fakeOps.LsRemoteCallCount() == 1`.
- Do not assert on the check booleans or the notes in this prompt.

## 9. Revert probes (both required; the spec's ACs name them as evidence)

Execute both probes in the container and report the captured failing output in your final summary:

1. **Park branch.** Temporarily restore the pre-fix return in `writeShortCircuit` (skip the consult and return `AgentStatusDone` / `NextPhase: "done"`), run only the named spec, and confirm `ShortCircuitRemoteAbsentParksHumanReview` fails:
   ```bash
   go test ./pkg/ -run TestSuite -args -ginkgo.focus='ShortCircuitRemoteAbsentParksHumanReview'
   ```
   Restore the fixed code afterwards.
2. **Close branch.** Temporarily make the function park unconditionally (`AgentStatusDone` / `NextPhase: "human_review"` for every observation), run the control, and confirm `ShortCircuitTagPresentCloses` fails:
   ```bash
   go test ./pkg/ -run TestSuite -args -ginkgo.focus='ShortCircuitTagPresentCloses'
   ```
   Restore the fixed code afterwards.

The tree you leave behind is the fixed one, with both probes reverted, `make test` green and `make precommit` green.

A focus that matches zero specs still exits 0 in Ginkgo, so each probe must also prove its spec actually ran: pipe the run to a log and require a line naming one spec, e.g. `… -ginkgo.focus='ShortCircuitRemoteAbsentParksHumanReview' 2>&1 | tee /tmp/probe1.log` followed by `grep -q 'Ran 1 of' /tmp/probe1.log`. A probe whose log does not show exactly one spec run is inconclusive, not a pass.

## 10. Self-check before finishing

Re-run `<verification>` and confirm it passes. Then walk each row of requirement 4 against the code and confirm the function can return `"done"` only on a non-empty observed SHA with no error; confirm `AgentStatusFailed` does not appear inside `writeShortCircuit`; confirm the step writes no frontmatter and no `## Result` on this branch; and confirm the file has the two new specs, the two rewritten specs, and no other spec touched.
</requirements>

<constraints>
- **The failed result shape carries no `tag` / `commit_sha` by contract** (`pkg/result_output.go`): the planned tag comes from `## Plan.next_version_header` through the existing `strings.TrimPrefix(plan.NextVersionHeader, "## ")` derivation. No second derivation, no new field, no fallback to `plan.NextVersion`.
- **Parking shape is `AgentStatusDone` + `NextPhase: "human_review"`.** `agentlib.AgentStatusFailed` is forbidden on this path: it preserves the incoming `phase: ai_review`, which re-triggers the record forever. `AgentStatusDone` + `"done"` is allowed only on a positive observation.
- **One consult, no retry, no new timeout.** Reuse `consultRemoteTag` (which owns auth injection, the `lsRemoteTimeout` bound, and the skip guard). Do not call `s.ops.LsRemote` from this file a second time and do not modify `pkg/git`.
- **No new result field, no new outcome value, no new error-category value, no new config key, no opt-out flag, no environment variable.** The `## Result` contract keeps exactly two shapes; this prompt reads it and never extends it.
- **The step writes no frontmatter and no `## Result` on this path.** Status and phase are the deliverer's decision on both branches; writing them in the step duplicates the mapping that prompt 3 exercises.
- **Remote state is read-only here.** No tag creation, deletion, or force-push. The fix only asks a question.
- **`TagAtExpectedSHA` must never be reported `true` on this path** (a `failed` page records no release commit, so the check has nothing to compare against). This prompt does not change the booleans — prompt 2 does — but must not introduce a `TagAtExpectedSHA: true` either.
- **The notes must contain the literal `short-circuit`** — prompt 2 implements that; do not remove the notes field or restructure the block in a way that makes it unreachable.
- **Do not change the shared agent library's `Failed` / `NextPhase` mapping.** The fix adapts this repo's returned result, not the framework.
- **Do not repair the seventeen already-wedged release records** and do not replay a release. Operator work, out of scope.
- **Do not re-enable the structural checks on the pre-push path** (`TagExists`, `TagAtExpectedSHA`, `ChangelogHeaderRewritten` stay disabled for the `released` path as they are today).
- **Two existing tests encode the pre-fix behaviour and are rewritten, not preserved** (`Context("7g. ...")` and the last spec of `Context("review-warning override (spec 064)")`).
- **`CHANGELOG.md` is prompt 3's job.** If `docs/dod.md`'s changelog criterion reads as unmet when you self-review, report it as deferred to prompt 3 — do not add the entry here, or prompt 3 produces a duplicate bullet.
- **Existing test conventions hold.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network, no live Claude calls.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)`; tokens are redacted via `git.RedactToken` before reaching a log line.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).
Run `make test` — exit 0.

```bash
grep -n 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go   # >= 1 (0 before this change)
grep -n 'ShortCircuitTagPresentCloses' pkg/steps_ai_review_test.go               # >= 1 (0 before this change)

grep -A 30 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'NextPhase'     # >= 1
grep -A 30 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'human_review'  # >= 1
grep -A 30 'ShortCircuitTagPresentCloses' pkg/steps_ai_review_test.go | grep -c '"done"'                    # >= 1
grep -A 30 'ShortCircuitTagPresentCloses' pkg/steps_ai_review_test.go | grep -c 'TagExists'                 # >= 1

# The planned tag is derived from the plan and reaches the consult bare (0 occurrences before this change).
grep -c 'v0.7.0' pkg/steps_ai_review_test.go   # >= 2: the fixture's next_version_header and the asserted bare tag argument

grep -c 's.consultRemoteTag(' pkg/steps_ai_review.go    # 3 (2 before this change: checkReviewOverride + finishApproved)
grep -c 's.ops.LsRemote(' pkg/steps_ai_review.go        # 1 — unchanged; the consult stays the only LsRemote call site

! sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'AgentStatusFailed'
! sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'Frontmatter\['
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'consultRemoteTag'
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'TrimPrefix'

grep -c 'taskWithFailedResult := func' pkg/steps_ai_review_test.go   # 1 — one builder, extended in place
```
</verification>

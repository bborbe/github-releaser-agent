---
status: approved
spec: [004-bug-ai-review-closes-rejected-release-as-completed]
created: "2026-10-28T11:02:40Z"
queued: "2026-10-28T11:18:18Z"
---

# Report only what the short-circuit observed: real `tag_exists`, never `tag_at_expected_sha`, and a named short-circuit

<summary>
- The review block written on a failed release stops claiming checks passed that were never performed.
- Whether the planned tag exists is reported from the actual observation, so a parked task says the tag was not found instead of pretending it was.
- The tag-at-commit check is never reported as evaluated on this path, because a failed page records no release commit to compare against.
- The review notes name the short-circuit, the planned tag and the observed state, so an operator reading the task page sees why it parked.
- A skipped consult or a failed one says so in the notes: an unverifiable remote is visible, not silent.
- Exactly one diagnostic log line records the task, the planned tag, the observed SHA and the decision, with any token redacted.
- Three specs pin the reporting: absent remote, present remote, and the skipped consult, each asserting the booleans and the notes token.
- The approved/overall verdicts are untouched — parking is expressed by the returned phase, not by a failed review verdict.
- This is the second of three prompts; it layers reporting on the decision the first prompt shipped.
</summary>

<objective>
Make the failed-outcome short-circuit's `## Review` block honest. The block must report `tag_exists` from the observation, must never report `tag_at_expected_sha: true` (the check is not evaluated on a failed page), and its notes must contain the literal `short-circuit` plus the planned tag and the observed state — including the skipped and errored cases. Add one `glog.V(2)` line per run carrying the task id, the planned tag, the observed SHA and the decision. The verdict semantics (`approved`, `overall`, the remaining check booleans) stay as they are.
</objective>

<context>
Read `docs/dod.md` for the Definition of Done and `CLAUDE.md` for project conventions.

Read these files BEFORE changing anything:
- `pkg/steps_ai_review.go` — `writeShortCircuit` as prompt 1 left it (it now takes `plan *PlanOutput`, derives `tag := strings.TrimPrefix(plan.NextVersionHeader, "## ")`, calls `obs := s.consultRemoteTag(ctx, md, tag)` once and returns `AgentStatusDone` / `human_review` or / `done`), the `remoteObservation` struct (`Consulted`, `SHA`, `Err`), `consultRemoteTag`, `reconcilePostPush` (the existing pattern for a `glog.V(2)` decision line carrying `task_identifier`, tag, observed SHA and outcome, with the error passed through `git.RedactToken`), and the `ReviewOutput` / `ReviewChecks` types (field names and JSON tags: `tag_exists`, `tag_at_expected_sha`, `changelog_header_rewritten`, `faithfulness_ok`, `unexpected_file_change`).
- `pkg/result_output.go` — the two-shape contract, so the comments you write stay true: a `failed` page carries no `CommitSHA` and no `Tag`.
- `pkg/steps_ai_review_test.go` — `extractReview` (it fails the spec when `## Review` is missing or malformed), `extractResult`, `taskWithFailedResult()` as prompt 1 extended it, and the two fixtures prompt 1 added: `ShortCircuitRemoteAbsentParksHumanReview` and `ShortCircuitTagPresentCloses`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-linting-guide.md`
</context>

<requirements>

## 1. Build the review block from the observation

Inside `writeShortCircuit`, replace the hardcoded check booleans with the observed values. The block stays a single `ReviewOutput` literal built in that function (the verification greps its body):

| Field | Value |
|---|---|
| `Approved` | `true` — unchanged |
| `Overall` | `OverallPass` — unchanged |
| `Checks.TagExists` | `obs.Consulted && obs.Err == nil && obs.SHA != ""` — the tag exists only when the remote was asked, answered, and named a commit |
| `Checks.TagAtExpectedSHA` | `false` — always; a `failed` page records no release commit, so this check is never evaluated on this path |
| `Checks.ChangelogHeaderRewritten` | `true` — unchanged |
| `Checks.FaithfulnessOK` | `true` — unchanged |
| `Notes` | `shortCircuitNotes(obs, tag)` (requirement 2) |

No other field is set. Do not populate `PerEntry`, `UnexpectedFiles`, or `FailedChecks` on this path.

## 2. The notes name the short-circuit and the observation

Add an unexported helper next to `writeShortCircuit`:

```go
// shortCircuitNotes describes the failed-outcome short-circuit for the
// ## Review notes: the literal "short-circuit", the planned tag, and the
// observed state (tag present / tag absent / consult skipped / consult
// failed). Error text is redacted before it reaches the durable page.
func shortCircuitNotes(obs remoteObservation, tag string) string
```

Contract:
- The returned string contains the literal `short-circuit` in every branch — this token is mandated and asserted by the specs.
- It names the planned tag (or, when `tag == ""`, says the plan carried no next-version header).
- It distinguishes the four states: tag present (include the observed SHA), tag absent, consult skipped, and consult failed (name the failure, redacted with `git.RedactToken(err.Error())`). `remoteObservation` does not record *which* input the skip guard rejected, so the skipped branch must name all three candidates — `clone_url`, `ref`, and the planned tag — rather than try to derive the reason.
- When the run parks, the notes say the task is parked for a human review; when it closes, they say the release is confirmed.
- Wording beyond those facts is yours. Keep it one sentence where possible — this string lands in a JSON block an operator reads.

Do not put the tag or the error text into the notes unredacted when the error could carry a URL: pass any error text through `git.RedactToken`.

## 3. One log line per run

Add exactly one `glog.V(2)` line in `writeShortCircuit`, after the decision is derived, recording four facts (wording is yours, the facts are not):
- the task identifier — `md.Frontmatter.String("task_identifier")`, as `reconcilePostPush` reads it;
- the planned tag;
- the observed SHA (`obs.SHA`, empty when absent or skipped);
- the decision (`done` / `human_review`, and the reason: present / absent / skipped / failed).

Any error text in the line goes through `git.RedactToken`. Do not add a second log line, a metric, or a warning-level line on this path.

## 4. Update the comments that are now false

- `ReviewOutput.PerEntry`'s comment (it currently says `PerEntry` is empty "when the execution step recorded failure (nothing to verify)") must describe the current behaviour without the phrase `nothing to verify`, which no longer appears in any notes string.
- `writeShortCircuit`'s doc comment must state what the block reports: `tag_exists` observed, `tag_at_expected_sha` never evaluated here, notes carrying the `short-circuit` token.

Do not change the `ReviewChecks` or `ReviewOutput` struct fields, their JSON tags, or the constants.

## 5. Extend the three fixtures' assertions

All three specs were added by prompt 1 with routing-only assertions. Add the reporting assertions; keep every routing assertion they already have. Keep each fixture's assertions within 12 lines of its name — the three fixtures sit adjacent, so a wider window would let a neighbour's assertion satisfy the verification grep.

**5a. `ShortCircuitRemoteAbsentParksHumanReview`** — add, within 30 lines of the spec name:
- Keep the `Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))` assertion prompt 1 added — exactly one consult, and more than zero; it reads `0` on this path before the fix, so it discriminates. Do not duplicate the line.
- `Expect(review.Checks.TagExists).To(BeFalse())`
- `Expect(review.Checks.TagAtExpectedSHA).To(BeFalse())`
- `Expect(review.Notes).To(ContainSubstring("short-circuit"))`
- `Expect(review.Notes).To(ContainSubstring("v0.7.0"))` — the notes name the planned tag.

**5b. `ShortCircuitTagPresentCloses`** — add, within 30 lines of the spec name:
- `Expect(review.Checks.TagExists).To(BeTrue())` (prompt 1 already asserts this; keep it as the observed-value assertion it now is)
- `Expect(review.Checks.TagAtExpectedSHA).To(BeFalse())`
- `Expect(review.Notes).To(ContainSubstring("short-circuit"))`

A `failed` page can never legitimately report `tag_at_expected_sha: true`; both fixtures assert it is `false` so an implementation that leaves the hardcoded `true` in place fails on the control as well as on the park.

**5c. `ShortCircuitConsultErrorParksHumanReview`** (prompt 1's fail-closed fixture) — add, within 12 lines of the spec name:
- `Expect(review.Checks.TagExists).To(BeFalse())`
- `Expect(review.Checks.TagAtExpectedSHA).To(BeFalse())`
- `Expect(review.Notes).To(ContainSubstring("short-circuit"))`

Prompt 1 deliberately left this fixture's booleans and notes unasserted ("Do not assert on the check booleans or the notes in this prompt"). This is the only place the `obs.Err != nil` branch of `shortCircuitNotes` can be pinned, and spec 004's Failure Modes row "`LsRemote` error, auth failure, or DNS/TCP problem" names the notes text as its detection signal — leave it unasserted and that row ships with no test.

## 6. Add the skipped-consult fixture

Add `It("ShortCircuitSkipGuardParksHumanReview — missing clone_url/ref skips the consult → parked, notes name the skip", ...)` to the same `Context("short-circuit remote decision (spec 004)")` block, after the existing fixtures (prompt 1 adds three: absent, present, consult-error).

- Fixture: a new builder `taskWithFailedResultNoRemote()` next to `taskWithFailedResult()`, identical except it omits the `clone_url` and `ref` frontmatter keys. Its `## Plan` keeps `"next_version_header":"## v0.7.0"` so the derivation still runs and only the frontmatter skip guard is under test.
- Assert `result.Status == agentlib.AgentStatusDone`, `result.NextPhase == "human_review"`.
- Assert `fakeOps.LsRemoteCallCount() == 0` — the remote was never asked.
- Assert `extractReview(md).Checks.TagExists` is `false` and `extractReview(md).Checks.TagAtExpectedSHA` is `false`.
- Assert `extractReview(md).Notes` contains `short-circuit` and names all three candidate inputs, using one `Or(...)` matcher over the substrings `clone_url`, `ref`, and `next-version header` rather than three separate matchers. The `Or` is required because the helper cannot know which one the guard rejected — do not make the test assert a single specific reason.

This covers the failure-mode row "frontmatter lacks `clone_url`, `ref`, or the plan carries no next-version header": the consult is skipped and the task parks rather than closing.

## 7. Self-check before finishing

Re-run `<verification>` and confirm it passes. Then confirm, by reading the function: `tag_exists` is derived from the observation and not a literal; `TagAtExpectedSHA` is `false` in every path; the notes string contains `short-circuit` in all four branches; exactly one `glog.V(2)` line exists on this path and it carries all four facts; no frontmatter and no `## Result` write was added; and the three fixtures plus the skip fixture carry the assertions listed above.
</requirements>

<constraints>
- **`TagAtExpectedSHA` must never be reported `true` on this path.** A `failed` page records no release commit (`pkg/result_output.go` two-shape contract), so the check has nothing to compare against and must read as not-run.
- **`tag_exists` reflects the observation, not a literal.** Present only when the remote was asked, answered without error, and returned a non-empty SHA.
- **The notes must contain the literal `short-circuit`** — mandated token, asserted by three specs.
- **`approved`, `overall` and the remaining check booleans are unchanged from today.** Parking is expressed by the returned `NextPhase`, not by a failed review verdict; this prompt does not redesign the block's verdict semantics.
- **Parking shape stays `AgentStatusDone` + `NextPhase: "human_review"`.** `agentlib.AgentStatusFailed` remains forbidden on this path (it preserves `phase: ai_review` and re-triggers forever). This prompt changes no return value.
- **One consult, no retry, no new timeout.** `consultRemoteTag` owns the auth injection, the `lsRemoteTimeout` bound and the skip guard. Do not call `s.ops.LsRemote` directly and do not modify `pkg/git`.
- **No new result field, no new outcome value, no new error-category value, no new config key, no opt-out flag, no environment variable.** Do not add a field to `ReviewOutput` or `ReviewChecks`.
- **The step writes no frontmatter and no `## Result` on this path.** Status and phase are the deliverer's decision (prompt 3 exercises that mapping).
- **Remote state is read-only here.** No tag creation, deletion, or force-push.
- **Tokens never reach a durable page or a log line unredacted.** Pass error text through `git.RedactToken` in both the notes and the log line.
- **Do not change the shared agent library's `Failed` / `NextPhase` mapping.** Out of scope; this repo's returned result is what adapts.
- **Do not repair the seventeen already-wedged release records** and do not replay a release.
- **Do not re-enable the structural checks on the pre-push path.**
- **Existing test conventions hold.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network, no live Claude calls.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)` — no `fmt.Print*`.
- **Do not touch `CHANGELOG.md` or `README.md`; the changelog entry is prompt 3's.** `docs/dod.md` requires a `## Unreleased` entry, so if that criterion reads as unmet when you self-review, report it as deferred to prompt 3 — adding it here makes prompt 3 produce a duplicate bullet.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).
Run `make test` — exit 0.

```bash
# The hardcoded true is gone from the short-circuit body (prints 1 before this change).
! sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'TagAtExpectedSHA:[[:space:]]*true'

# tag_exists is derived, not literal.
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'TagExists:'
! sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'TagExists:[[:space:]]*true'

# The mandated token lives in the notes builder (the function does not exist before this change,
# so this cannot pass on the pre-fix tree even though "short-circuit" appears twice in other comments).
sed -n '/func shortCircuitNotes/,/^}/p' pkg/steps_ai_review.go | grep -q 'short-circuit'

# The specs pin the reporting. Window 12 lines, not 30: the fixtures sit adjacent, so a wider
# window would let a neighbour's assertion satisfy the grep.
grep -A 12 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'LsRemoteCallCount'   # >= 1
grep -A 12 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'short-circuit'       # >= 1
grep -A 12 'ShortCircuitRemoteAbsentParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'TagExists'           # >= 1
grep -A 12 'ShortCircuitTagPresentCloses' pkg/steps_ai_review_test.go | grep -c 'TagAtExpectedSHA'                # >= 1
grep -A 12 'ShortCircuitConsultErrorParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'short-circuit'       # >= 1
grep -n 'ShortCircuitSkipGuardParksHumanReview' pkg/steps_ai_review_test.go                                       # >= 1 (0 before)

# One log line on the short-circuit path, carrying the four recorded facts (0 before this change).
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -c 'glog.V(2)'   # exactly 1
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'RedactToken'
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'task_identifier'
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'obs.SHA'
sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'tag'

# No frontmatter write and no result rewrite were introduced.
! sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'Frontmatter\['
! sed -n '/func (s \*aiReviewStep) writeShortCircuit/,/^}/p' pkg/steps_ai_review.go | grep -q 'writeFailureResult'
! grep -q 'nothing to verify' pkg/steps_ai_review.go   # 2 matches before this change: the notes literal and the PerEntry comment
```
</verification>

---
status: completed
spec: [003-bug-releaser-claims-released-without-remote-tag]
summary: 'ai-review now verifies the pushed tag against the remote once and derives ## Result from the observed SHA, keeping `released` only on a confirmed prefix match and parking unconfirmed releases for a human'
execution_id: github-releaser-agent-exec-008-spec-003-post-push-remote-verification
dark-factory-version: v0.193.0
created: "2026-09-13T19:50:00Z"
queued: "2026-09-13T17:55:23Z"
started: "2026-09-13T18:01:01Z"
completed: "2026-09-13T18:05:29Z"
---

# Verify the pushed tag on the remote and reconcile the task's Result block

<summary>
- The step that owns the push now asks GitHub what the remote actually carries before the task page is allowed to read "released".
- A pushed-but-unverified release no longer reads as success: when the remote does not carry the planned tag, the task page records a non-success outcome naming the failure instead of the optimistic "released".
- A rejected push keeps its existing routing, but the page no longer claims a release: the outcome is downgraded and the failure category names the rejection.
- The remote is the authority when a push command errors but the tag did land — an error-with-landed-release keeps "released" and records the push error on the review, so a blanket "always downgrade on push error" cannot pass.
- A tag that exists at a different commit never reads as this task's release: the outcome is downgraded and names the commit the remote actually shows, while the existing routing still closes the task.
- A review that never pushed is reconciled from the remote observation the review already made — the remote is never asked twice for the same decision — and an unconfirmed release is downgraded.
- An unreachable remote is never success: a failed or timed-out verification records that the remote could not be verified and parks the task for a human instead of claiming a release.
- Five new regression specs cover one branch each, and the existing review-override spec that asserted "no remote call on the happy path" is updated to the new single verification call.
- This is the second of three prompts for spec 003; it builds on the prefix comparison that landed in the first.
</summary>

<objective>
Move the success claim to the step that owns the push: after `Push` returns, the AI-review step consults the remote for `refs/tags/<planned tag>`, derives the `## Result` outcome from what the remote reports (prefix-compared against the recorded short SHA), and rewrites `## Result` to the documented non-success shape whenever the remote cannot confirm the tag at the expected commit. The task page and the review can then no longer disagree about the same run, and the `released` outcome means what `pkg/result_output.go` says it means: the remote carries the planned tag at the expected commit.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done this prompt is validated against.

Read these files BEFORE changing anything:
- `pkg/steps_ai_review.go` — `Run` (its step 7 / step 8 sequence comment and the `!approved` branch), `finishApproved` (where `Push` runs — the change site), `finishHumanReview` (the parking path: writes `## Review`, returns Failed / human_review, sets the cleanup sentinel), `checkReviewOverride` (the existing consult on the `!approved` path), `finishReviewOverride` (writes `## Review` + `## Review Warning`, sets frontmatter `completed` / `done`, returns Done), `writeShortCircuit` (the pre-existing-failure path — unchanged).
- `pkg/steps_execution.go` — `fail` and `postCheck` show the established `## Result` writing and verdict-logging patterns (marshal the typed struct, `md.ReplaceSection`, one `glog.V(2)` line). `lsRemoteTimeout` (30s) is the only timeout.
- `pkg/result_output.go` — `ResultOutput`, `ResultOutcomeReleased`, `ResultOutcomeFailed`, `ResultPathDirectPush`, and the two-shape doc comment (the failure shape is: ErrorCategory + Error populated, CommitSHA + Tag empty). Do not extend this struct.
- `pkg/git/error_classifier.go` — `ClassifyError`. Note it returns the EMPTY sentinel `ErrorCategory("")` for a nil error, and `omitempty` drops an empty category from the JSON.
- `pkg/git/os_exec_git_ops.go` — `LsRemote(ctx, cloneURL, ref, tag)`: it prepends `refs/tags/` itself, so the caller passes the BARE tag (`result.LocalTag`, e.g. `v1.0.0`) and NEVER `"refs/tags/"+tag`. It already prefers the dereferenced `^{}` line and falls back to the plain line for lightweight tags.
- `pkg/steps_ai_review_test.go` — read the helper block at the top of `Describe("AIReviewStep")` (`taskWithResult`, `runStep`, `setupReleasedWorkdir`, `stubFaithfulLLM`, `extractReview`, `extractReviewWarning`), the `Context("push fails after approval")` and `Context("concurrent push ...")` blocks, and the `Context("review-warning override (spec 064)")` block in full.
- `pkg/url_helpers.go` — `injectToken` / `normalizeCloneURLToHTTPS`, the auth model `checkReviewOverride` already uses.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-linting-guide.md`
</context>

<requirements>

## 1. One shared remote consult helper

Add an unexported observation value and one consult method to `pkg/steps_ai_review.go`. The observation must distinguish "the remote was never asked" from "the remote was asked and returned nothing":

```go
// remoteObservation is the outcome of one remote consult. Consulted=false
// means the skip guard fired (missing clone_url / ref / tag) and the remote
// was never asked — no reconciliation may run on that observation.
type remoteObservation struct {
	Consulted bool
	SHA       string
	Err       error
}
```

```go
// consultRemoteTag asks the remote which commit, if any, sits at
// refs/tags/<tag>. It mirrors checkReviewOverride's auth + timeout model:
// authed URL from the task frontmatter, bounded by the existing
// lsRemoteTimeout. It NEVER calls ClassifyError and never retries.
func (s *aiReviewStep) consultRemoteTag(
	ctx context.Context,
	md *agentlib.Markdown,
	tag string,
) remoteObservation
```

Contract:
- Skip guard first: when `md.Frontmatter.String("clone_url")`, `md.Frontmatter.String("ref")`, or the `tag` argument is empty, return `remoteObservation{Consulted: false}` WITHOUT calling the ops seam.
- Otherwise build the authed URL exactly as `checkReviewOverride` does (`injectToken(normalizeCloneURLToHTTPS(cloneURL), s.ghToken)`), bound the call with `context.WithTimeout(ctx, lsRemoteTimeout)` and `defer cancel()`, and call `s.ops.LsRemote(lsCtx, authedURL, ref, tag)` passing the BARE tag.
- Return `remoteObservation{Consulted: true, SHA: sha, Err: err}`.

Refactor `checkReviewOverride` to use this helper, and extend it to hand its observation back to `Run`:

```go
func (s *aiReviewStep) checkReviewOverride(
	ctx context.Context,
	md *agentlib.Markdown,
	output *ReviewOutput,
	result *ResultOutput,
) (*ReviewWarningOutput, remoteObservation)
```

Its behaviour is otherwise unchanged: it returns a nil warning (and no override) when the consult was skipped, when the observed SHA is empty, or when the consult errored — logging the error through `git.RedactToken` as it does today. Update its single call site in `Run`. `checkReviewOverride` is unexported and called only from `Run`; no test calls it directly.

## 2. One failure-writing helper

Add an unexported helper that writes the documented non-success shape, mirroring `executionStep.fail` (`pkg/steps_execution.go`):

```go
// writeFailureResult rewrites the ## Result section to the documented
// non-success shape: Outcome=failed, Path=direct-push, ErrorCategory +
// Error populated, CommitSHA / Tag / Workdir / LocalTag empty (the
// two-shape contract in pkg/result_output.go).
func (s *aiReviewStep) writeFailureResult(
	ctx context.Context,
	md *agentlib.Markdown,
	category git.ErrorCategory,
	message string,
) error
```

Implementation: build `ResultOutput{Outcome: ResultOutcomeFailed, Path: ResultPathDirectPush, ErrorCategory: category, Error: message}`, marshal with `agentlib.MarshalSectionTyped(ctx, "## Result", output)`, `md.ReplaceSection(section)`, and return an error wrapped with `errors.Wrapf(ctx, err, ...)` if marshalling fails. Do NOT touch `## Review`, `## Review Warning`, or the frontmatter from this helper.

**Every call site must pass a non-empty category.** `git.ClassifyError(nil)` returns `ErrorCategory("")`, which `omitempty` silently drops from the emitted JSON — a failure shape without a category. The sites with no git error to classify (tag absent, tag at a different commit, review rejected without a push) pass `git.ErrorCategoryUnknown` explicitly.

## 3. Post-push verification in `finishApproved`

After `pushErr := s.ops.Push(ctx, result.Workdir, "HEAD", "refs/tags/"+result.LocalTag)` returns, and after the existing push-error bookkeeping (note `"push failed: " + pushErr.Error()`, `output.Approved = false`, append `CheckPush`), call `obs := s.consultRemoteTag(ctx, md, result.LocalTag)` once and derive the outcome from it. Exactly one consult per decision — never re-ask the remote inside the reconciliation.

| Push result | Observation | `## Result` after the step | `error` | `error_category` | Routing |
|---|---|---|---|---|---|
| error | not consulted (skip guard) | untouched | — | — | existing push-error routing (Failed / `human_review`) |
| error | tag present at the expected commit (prefix match) | untouched — `released` KEPT | — | — | existing push-error routing (unchanged) |
| error | tag present at a different commit | rewritten failed | names the observed SHA | `git.ErrorCategoryUnknown` | existing push-error routing (unchanged) |
| error | tag absent | rewritten failed | the push error text | `git.ClassifyError(pushErr)` | existing push-error routing (unchanged) |
| error | consult returned an error | rewritten failed | `"remote verification failed: " + obs.Err.Error()` | `git.ClassifyError(obs.Err)` | existing push-error routing (unchanged) |
| ok | not consulted (skip guard) | untouched | — | — | existing Done path (unchanged) |
| ok | tag present at the expected commit (prefix match) | untouched — `released` KEPT | — | — | existing Done path (unchanged) |
| ok | tag present at a different commit | rewritten failed | names the observed SHA | `git.ErrorCategoryUnknown` | park: Failed / `human_review` |
| ok | tag absent | rewritten failed | names the tag the remote does not carry (include the tag string) | `git.ErrorCategoryUnknown` | park: Failed / `human_review` |
| ok | consult returned an error | rewritten failed | `"remote verification failed: " + obs.Err.Error()` | `git.ClassifyError(obs.Err)` | park: Failed / `human_review` |

Rules that fall out of the table:
- **`released` survives only on a confirmed prefix match** (or when the consult was skipped). Every other observation rewrites `## Result` to the non-success shape, so a task page whose remote does not carry the tag never reads `released`.
- The prefix comparison is `strings.HasPrefix(obs.SHA, result.CommitSHA)` — the recorded `commit_sha` is a short SHA; the remote returns 40 characters. Never use exact equality.
- **The push-error routing is unchanged in every row**: the existing `finishHumanReview` call, the `"push failed"` note, the `CheckPush` failed check, and the Failed / `human_review` result. Do NOT write a `## Review Warning` block on the push path.
- The matching rows keep today's `finishApproved` tail exactly as it is (marshal `## Review`, set the cleanup sentinel, return Done / `done`).
- The park rows write the downgraded `## Result` FIRST, then call `finishHumanReview` with `output.Notes` set to that row's `## Result` error message (so the review section no longer reads "all checks passed" while the result says failed). Leave `output.Approved` and `output.FailedChecks` as they are on those rows — the push itself did not fail, so do not append `CheckPush`.
- One `glog.V(2)` line per reconciliation decision carrying the task identifier (`md.Frontmatter.String("task_identifier")`), the tag, the observed remote SHA, and the derived outcome; pass any error text through `git.RedactToken(err.Error())` like the existing review-override log line does. Wording is yours; those four recorded facts are not.

## 4. Reconcile the `!approved` path from the observation it already made

In `Run`'s `!approved` branch, take the observation returned by `checkReviewOverride` and reconcile from it. Do NOT consult the remote a second time on this path — the decision already has its observation.

| Observation | `## Result` | `error` | `error_category` | Routing |
|---|---|---|---|---|
| not consulted (skip guard) | untouched (existing no-op) | — | — | `finishHumanReview` (unchanged) |
| tag present at the expected commit | untouched — `released` confirmed; no rewrite needed | — | — | `finishReviewOverride` (completed, unchanged) |
| tag present at a different commit | rewritten failed | names the observed SHA | `git.ErrorCategoryUnknown` | `finishReviewOverride` (completed, unchanged; the `## Review Warning` block stands) |
| tag absent | rewritten failed | names the failed checks (`output.Notes` or the joined `output.FailedChecks`) | `git.ErrorCategoryUnknown` | `finishHumanReview` (unchanged) |
| consult returned an error | rewritten failed | names the failed checks | `git.ErrorCategoryUnknown` | `finishHumanReview` (unchanged; no `## Review Warning`) |

Notes:
- Write the downgraded `## Result` before the terminal routing call on every row that rewrites.
- The last two rows keep `checkReviewOverride`'s pre-existing no-op: it still returns a nil warning, and the routing stays `human_review`. Only the new reconciliation layer writes `## Result`; the sub-decision itself is unchanged. Only a post-push consult is fail-closed (it parks the task); an unconfirmed `!approved` task is parked either way.
- The superseded row must not touch the frontmatter: `finishReviewOverride` keeps setting `completed` / `done`, so the task still closes `completed` while the page no longer claims this task's commit shipped the version.
- `writeShortCircuit` (a pre-existing non-`released` outcome) is unchanged: no consult, no rewrite.

## 5. Update the comments that describe the old contract

Rewrite, in `pkg/steps_ai_review.go`: `Run`'s step 7 / step 8 sequence comment (it currently says the `!approved` branch only *upgrades* the verdict), `finishApproved`'s doc comment (it currently describes push failure as the only non-Done exit), and `checkReviewOverride`'s doc comment (extend it for the returned observation; keep its "returns nil on empty result or `LsRemote` error" statement true). No comment may continue to assert that a push error is the only route to a non-success page, or that the reconciliation asks the remote again.

## 6. Tests — `pkg/steps_ai_review_test.go`

### 6a. Hoist the two Context-scoped helpers

`taskWithResultFull` and `driveRejectingFaithfulnessLLM` are currently closure-scoped inside `Context("review-warning override (spec 064)")`. Move both up to the `Describe("AIReviewStep")` scope, next to the existing helper block that starts at `taskWithResult` (so both the override Context and the new Context can use them). Delete the originals from inside the Context — the override Context's specs keep using them unchanged.

`taskWithResultFull` is the ONLY task builder whose frontmatter carries `clone_url` and `ref`; `taskWithResult` does not. Every new fixture below must be built with `taskWithResultFull` — a fixture built with `taskWithResult` trips the consult's skip guard, which would make the confirmation fixture pass vacuously and the downgrade fixtures impossible to satisfy.

### 6b. Add an extractor next to `extractReview`

```go
extractResult := func(md *agentlib.Markdown) *pkg.ResultOutput {
	res, err := agentlib.ExtractSection[pkg.ResultOutput](
		context.Background(),
		md,
		"## Result",
	)
	Expect(err).NotTo(HaveOccurred())
	return res
}
```

Add the import `"github.com/bborbe/github-releaser-agent/pkg/git"` (unaliased, as `pkg/steps_execution_test.go` already does) so the specs can assert on `git.ErrorCategoryUnknown` and `git.ErrorCategoryProtectedBranchRejected`.

### 6c. New Context with five specs

Add `Context("post-push remote reconciliation (spec 003)", func() { ... })` after the `Context("review-warning override (spec 064)")` block, immediately before the closing of `Describe("Run")`. Its specs inherit the `Describe("Run")` BeforeEach (`tmpDir` with a real `CHANGELOG.md` via `setupReleasedWorkdir`, `CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)`, a faithful LLM stub), which is what makes the review approved and the push reached. Put each fixture name verbatim in its `It(...)` description so the name is greppable and discriminating.

**1. `PushRejectedRemoteAbsent`** — rejected push, remote absent.
- Fixture: `taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir)`.
- Stubs: `fakeOps.PushReturns(errors.New("remote: error: GH006: Protected branch update failed for refs/heads/master"))` and `fakeOps.LsRemoteReturns("", nil)`.
- Assert: `fakeOps.PushCallCount()` == 1; `fakeOps.LsRemoteCallCount()` == 1; `result.Status` == `agentlib.AgentStatusFailed`; `result.NextPhase` == `"human_review"`; extracted `## Result`: `Outcome` == `pkg.ResultOutcomeFailed`, `Error` contains `"GH006"`, `ErrorCategory` == `git.ErrorCategoryProtectedBranchRejected`; `extractReview(md).Notes` contains `"push failed"`; `extractReview(md).FailedChecks` contains `pkg.CheckPush`; `extractReviewWarning(md)` is nil; the recorded consult asked for the BARE tag — `_, _, _, tagArg := fakeOps.LsRemoteArgsForCall(0)` then `Expect(tagArg).To(Equal("v1.0.0"))` (never `"refs/tags/v1.0.0"`, which would query `refs/tags/refs/tags/...`); and `md.Marshal(context.Background())` contains neither `"outcome": "released"` nor `"outcome":"released"`.

**2. `PushRejectedButRemoteConfirms`** — the control: the remote is the authority on an error-but-landed push.
- Fixture: `taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir)`.
- Stubs: the same GH006 push error, and `fakeOps.LsRemoteReturns("abc1234e3cca37862f4e612a7b14c4e00af6b935", nil)` (full 40-char SHA whose prefix is the recorded short SHA).
- Assert: `## Result`.Outcome == `pkg.ResultOutcomeReleased` (KEPT); `## Result`.Error is empty; `fakeOps.PushCallCount()` == 1; `fakeOps.LsRemoteCallCount()` == 1; `result.Status` == `agentlib.AgentStatusFailed`; `result.NextPhase` == `"human_review"`; `extractReview(md).Notes` contains `"push failed"` and the push error text (`ContainSubstring("GH006")`); `extractReviewWarning(md)` is nil.

**3. `RemoteCheckErroredFailsClosed`** — post-push verification error.
- Fixture: `taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir)`.
- Stubs: no `PushReturns` stub (the fake returns nil — the push succeeded) and `fakeOps.LsRemoteReturns("", errors.New("ls-remote boom"))`.
- Assert: `fakeOps.PushCallCount()` == 1; `fakeOps.LsRemoteCallCount()` == 1; extracted `## Result`: `Outcome` == `pkg.ResultOutcomeFailed`, `Error` contains `"remote verification"` and `ErrorCategory` == `git.ErrorCategoryUnknown`; `result.Status` == `agentlib.AgentStatusFailed`; `result.NextPhase` == `"human_review"`; `md.Marshal(context.Background())` contains neither `"outcome": "released"` nor `"outcome":"released"`.

**4. `ReviewNotApprovedNoPushRemoteAbsent`** — review rejected, no push attempted, remote absent.
- Setup: `driveRejectingFaithfulnessLLM()` and `fakeOps.LsRemoteReturns("", nil)`; fixture `taskWithResultFull("abc123", "v1.2.8", "released", tmpDir)`.
- Assert: `fakeOps.PushCallCount()` == 0; `fakeOps.LsRemoteCallCount()` == 1 (the reconciliation consumed the pre-existing observation — a second consult fails this spec); `result.Status` == `agentlib.AgentStatusFailed`; `result.NextPhase` == `"human_review"`; `extractReview(md).Approved` is false and `.FailedChecks` contains `pkg.CheckFaithfulness`; `extractReviewWarning(md)` is nil; extracted `## Result`: `Outcome` == `pkg.ResultOutcomeFailed`, `Error` contains `"Faithfulness"`, `ErrorCategory` == `git.ErrorCategoryUnknown`; and `md.Marshal(context.Background())` contains neither `"outcome": "released"` nor `"outcome":"released"`.

**5. Push succeeded but the remote does not carry the tag.** This branch has no minted fixture name; give the spec a description that states the intent, e.g. `It("push succeeded but the remote does not carry the tag → ## Result downgraded to failed, task parked", ...)`.
- Fixture: `taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir)`; no push stub; `fakeOps.LsRemoteReturns("", nil)`.
- Assert: `fakeOps.PushCallCount()` == 1; `fakeOps.LsRemoteCallCount()` == 1; extracted `## Result`: `Outcome` == `pkg.ResultOutcomeFailed`, `Error` is non-empty and contains `"v1.0.0"`, `ErrorCategory` == `git.ErrorCategoryUnknown`; `result.Status` == `agentlib.AgentStatusFailed`; `result.NextPhase` == `"human_review"`; `md.Marshal(context.Background())` contains neither `"outcome": "released"` nor `"outcome":"released"`.

### 6d. Adjust exactly three existing specs in the override Context

- **`remote SHA differs from expected → completed, ## Review Warning, ObservedRemoteSHA = remote SHA`** (~line 1120): keep every existing assertion (`result.Status` Done, `NextPhase` done, `LsRemoteCallCount` 1, frontmatter `completed` / `done`, the `## Review Warning` with `ObservedRemoteSHA` `"deadbee"`) and ADD the superseded rewrite: extracted `## Result`.`Outcome` == `pkg.ResultOutcomeFailed` and `## Result`.`Error` contains `"deadbee"`.
- **`LsRemote errors → human_review, no ## Review Warning block`** (~line 1173): keep its routing/status assertions and its `LsRemoteCallCount` 1, and ADD that the extracted `## Result`.`Outcome` == `pkg.ResultOutcomeFailed` with a non-empty `Error`. Update its comment — it currently says the verdict downgrade does not happen, which is no longer true.
- **`Approved=true happy path is unchanged (no LsRemote, no ## Review Warning)`** (~line 1193): its fixture is `taskWithResultFull("abc123", "v1.0.0", "released", tmpDir)` and it already stubs `fakeOps.LsRemoteReturns("abc123", nil)`, so the post-push consult now runs once and the observed SHA prefix-matches the recorded `abc123` → `released` is kept and the task still closes Done / done. Change its `LsRemoteCallCount` expectation from `0` to `1` and rename it (plus its comment) so it no longer claims no remote call happens.

Do NOT touch: the `!approved` + expected-commit spec (~line 1079), the `!approved` + remote-empty spec (~line 1151), the short-circuit spec (~line 1213), or the two push-failure specs in `Context("push fails after approval")` and `Context("concurrent push (tag already exists on upstream)")`. Those two build their fixture with `taskWithResult`, which has no `clone_url` / `ref`, so the consult's skip guard fires and their existing assertions stay true unchanged.

After this work, the `LsRemoteCallCount` expectations in the file read: `1`, `1`, `1`, `1` for the four `!approved` specs (unchanged), `1` for the approved happy path (raised from `0`), and `0` for the short-circuit spec (unchanged).

### 6e. Lint budget

`funlen` (80 lines / 50 statements), `gocognit` (20) and `nestif` (4) are enabled. Keep the reconciliation in the helpers described above and extract further helpers as needed so `Run` and `finishApproved` do not grow past those limits. Only add a `//nolint:` comment with a rationale if extraction genuinely cannot get under the limit — the existing `//nolint:gocognit,funlen` markers on `Run` / `postCheck` are the precedent for how that is written.

## 7. Self-check before finishing

Re-run `<verification>` and confirm it passes. Walk each row of both tables and confirm the implementation handles it, then confirm the test file has all five new specs — the four named fixtures built with `taskWithResultFull` and their assertions — and the new Context, the three adjusted specs, no other spec touched, and that no new consult happens on the `!approved` path beyond the pre-existing one.
</requirements>

<constraints>
- **The push step owns the success claim, and the outcome is derived from the observed SHA.** `released` survives only when the remote shows the planned tag at the expected commit.
- **Prefix comparison is mandatory.** Compare with `strings.HasPrefix(observedSHA, expectedSHA)`. Never use exact equality.
- **One consult per decision.** A decision that already has an observation from a consult made earlier in the same run (the `checkReviewOverride` check on the `!approved` path) reconciles from that observation and does not query the remote again; a decision whose push just happened consults once.
- **Unverifiable is never success.** An `LsRemote` error or timeout on the post-push consult is fail-closed: non-success outcome, `error` naming that remote verification failed, task parked for a human.
- **The fingerprint of a landed release is preserved.** A push error whose tag the remote confirms at the expected commit keeps `released`; the remote is the authority.
- **No new retry loop and no new timeout.** Reuse `lsRemoteTimeout` (30s). The dereference preference (`^{}` line, plain-line fallback for lightweight tags) already lives in `LsRemote` / `parseLsRemoteOutput` — reuse it, do NOT re-implement it and do NOT modify `pkg/git`.
- **Rejected alternative — no new outcome value.** The `## Result` contract has exactly two shapes; do not add `pending` / `unverified` / anything else, and do not extend the JSON field set.
- **No new error-category enum values.** Reuse `git.ClassifyError` and its existing categories.
- **The downgrade writes only the `## Result` section** on the `human_review` path — it does not set frontmatter `status` / `phase` (those remain the routing/escalation path's decision) and it does not alter the existing `checkReviewOverride` / `finishReviewOverride` close-as-completed contract for a remote-confirmed tag. `writeShortCircuit` for pre-existing `failed` outcomes is unchanged.
- **The execution-step post-check keeps its empty-remote no-op.** At execution time the push has not run; the post-push verification is the deciding check. Do not edit `pkg/steps_execution.go` in this prompt.
- **Remote state is read-only here.** No tag deletion, no force-push; `Push` keeps `--atomic` and stays force-free.
- **Git-write safety invariant untouched.** CHANGELOG-only commit via explicit pathspec; deterministic Go writes; the LLM only classifies.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)` — no `fmt.Print*`.
- **Existing tests keep passing** (Ginkgo v2 / Gomega, Counterfeiter fakes, no live Claude or network calls in tests).
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make test` — exit 0, with the five new specs present and the rest of the file green.
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).

```bash
grep -n 'LsRemote' pkg/steps_ai_review.go      # MORE matches than the pre-fix count of 4
grep -n 'HasPrefix' pkg/steps_ai_review.go     # >= 1
grep -n 'ResultOutcomeFailed' pkg/steps_ai_review.go   # >= 1 (the downgrade write site)
grep -c 'PushRejectedRemoteAbsent' pkg/steps_ai_review_test.go          # >= 1
grep -c 'PushRejectedButRemoteConfirms' pkg/steps_ai_review_test.go     # >= 1
grep -c 'RemoteCheckErroredFailsClosed' pkg/steps_ai_review_test.go     # >= 1
grep -c 'ReviewNotApprovedNoPushRemoteAbsent' pkg/steps_ai_review_test.go  # >= 1
grep -n 'LsRemoteCallCount' pkg/steps_ai_review_test.go   # the six pre-existing expectations read 1,1,1,1,1,0; the five new specs add five more ==1 assertions
grep -n 's.ops.LsRemote(' pkg/steps_ai_review.go      # exactly ONE call site — the shared consult helper
grep -n 'LsRemoteArgsForCall' pkg/steps_ai_review_test.go   # >= 1 — the recorded tag argument is asserted to be the BARE tag
```
</verification>

---
status: approved
spec: [006-planning-containment-guard]
created: "2026-11-10T13:45:00Z"
queued: "2026-11-10T13:30:57Z"
---

# Close a release task whose ref is already released, before it re-plans

<summary>
- A release task that already shipped its version stops re-planning the next one and closes itself.
- Today the "is this already released?" question is asked only when the changelog looks malformed, so a healthy re-trigger re-plans forever.
- The check now runs on every planning pass, before the changelog is classified or fetched.
- It asks GitHub whether the task's ref is already contained in the released tag — not whether it equals it.
- Equality was the old test and it cannot fire here: the release commit is a child of the task's ref, so the ref is an ancestor, never equal.
- A ref with new commits ahead of the tag still plans a release exactly as today.
- Any lookup failure falls through to today's behaviour — an outage can never close a task.
- The task file records which tag contained the ref, so the closure is auditable.
- Costs one extra tag lookup plus one GitHub compare call per planning run; that is the price of asking on the happy path.
- Existing nothing-to-release behaviour for the malformed-changelog case is preserved unchanged.
</summary>

<objective>
Make the planning step close a release task as `nothing_to_release` when the task's `ref` is already contained in the effective current version's tag, before it classifies a bump and plans the next version. This stops the re-plan loop at its source: a record that shipped version X currently re-enters planning, finds a valid `## Unreleased` at its pre-release `ref`, plans X+1, and returns `NextPhase: execution` — so it never writes a terminal state for X and reads as permanently live.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions.

**This prompt implements `specs/006-planning-containment-guard.md`, which SUPERSEDES nine normative statements of spec 002** (`specs/completed/002-planning-close-nothing-to-release.md`): its Constraints line "The check runs **only** after a `P1` validation failure. The happy path … makes no extra API call", and its acceptance criteria 8 and 9 ("other preconditions untouched … zero invocations" / "happy path makes no extra request"). Completed specs are immutable, so the reversal is recorded in spec 006 rather than by editing 002 — read spec 006 before starting, it carries the failure-mode table and the security note. The containment check deliberately moves to every planning pass; the two tests that pinned the old invariant are updated in requirement 7.

Read these files fully BEFORE writing code:
- `/workspace/pkg/steps_planning.go` — the file you are changing. Note `Run`, `resolveCurrentVersion`, `releaseTagCommit`, `handleValidationFailure`, `releaseTagVerdict`, `sameCommit`, and `publishNothingToRelease`. Read the `publishNothingToRelease` doc comment: `Status: Done` + explicit `NextPhase: "done"` is the ONLY terminal disposition — never the empty-`NextPhase` auto-advance (bug 048).
- `/workspace/pkg/githubtags/tags.go` — the `TagsFetcher` interface and `httpTagsFetcher`. `CommitSHAForTag` is the exemplar for a new method on this client: same auth, same 15-second client, same wrapped-error style, same `errors.Errorf(ctx, …)` argument validation. `fetchPage` is the exemplar for request-header construction.
- `/workspace/pkg/plan_output.go` — the `PlanOutput` struct and the `PlanOutcome*` / `Precondition*` constants.
- `/workspace/pkg/steps_planning_test.go` — the test style you must extend: `withNoRemoteTags()`, `pkg.NewPlanningStep(...)`, `agentlib.ParseMarkdown`, `agentlib.ExtractSection[pkg.PlanOutput](ctx, md, "## Plan")`, and the bug-048 deliverer regression.
- `/workspace/pkg/githubtags/tags_test.go` and `pkg/githubtags/export_test.go` — the `NewHTTPTagsFetcherForTest` + `httptest.NewServer` harness you will extend in requirement 7b.
- `/workspace/pkg/export_test.go` — the `XxxForTest` seam-export idiom.
- `/workspace/docs/dod.md` — the `## Git-Write Safety` section.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`

The defect being closed, for fixture realism: record `37145af6` shipped `v0.23.3` while its `ref` was the commit one behind the tag commit — GitHub compare reported `ahead 1, behind 0`. Short SHAs: ref `e341c42`, tag commit `600a8f66`. At `ref` the changelog still carried `## Unreleased`, so every re-trigger planned another version and the record never reached `completed`. Measured across 24 records on 2026-11-08, each with 3–6 succeeded Jobs.
</context>

<requirements>

## 1. New client method — `/workspace/pkg/githubtags/tags.go`

Add a compare method to `TagsFetcher` and its `httpTagsFetcher` implementation. It answers one question: is `base` already contained in `head`?

```go
// CompareResult is the subset of GitHub's compare payload the planning step
// needs. BehindBy is the number of commits HEAD lacks that BASE has, and is
// the only field that decides containment — AheadBy is decoded so the
// decision can be logged with both directions visible, never for control flow.
//
// BehindBy is a POINTER so a payload that omits or renames the key is
// distinguishable from a genuine 0. An absent key must never read as
// "contained": that would close a record with unreleased work on a shape
// change, which is the exact class of defect this guard exists to remove.
type CompareResult struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy *int   `json:"behind_by"`
}
```

Interface addition:

```go
	// CompareCommits returns GitHub's comparison of base...head for
	// owner/repo. Costs one compare call — no pagination, no per-commit
	// request. Returns a wrapped error on empty args, transport failure,
	// non-2xx (including 404 for an unknown ref), and decode failure.
	// BehindBy is nil when the payload does not carry the key.
	CompareCommits(ctx context.Context, owner, repo, base, head string) (CompareResult, error)
```

Implementation notes:
- Validate `owner`, `repo`, `base` and `head` as non-empty with `errors.Errorf(ctx, …)` BEFORE building the request — an empty argument must fail without an HTTP call.
- **Direction:** `base` is the task's `ref`, `head` is the tag's commit. GitHub's `base...head` therefore reports `behind_by` = commits the tag commit lacks that `ref` has; `0` means the tag already contains the ref.
- Endpoint: `%s/repos/%s/%s/compare/%s...%s` with `url.PathEscape` on owner, repo, base and head. A `ref` containing `/` (a branch name) is escaped per component — encoded, never split.
- Headers exactly as `fetchPage` builds them: `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, and `Authorization: Bearer <token>` only when the token is non-empty.
- Same `http.Client` (15s) and the same error wrapping as `CommitSHAForTag`.
- Return the decoded `CompareResult`; no pagination loop.
- **If `dupl` flags the new method against `fetchPage`** (it is enabled at `.golangci.yml:18` with the default 150-token threshold, and the request/response skeleton is ~35 near-identical lines): extract the shared plumbing — build request + headers, `Do`, `ReadAll`, non-2xx check — into an unexported `doGet(ctx, pageURL string) ([]byte, http.Header, error)` helper and have both `fetchPage` and `CompareCommits` call it. Do NOT add `//nolint:dupl`.

Then regenerate the Counterfeiter fake for the widened interface (`go generate ./...`, or `make generate`) — `mocks/tags_fetcher.go` must gain `CompareCommits` and its `Returns`/`ArgsForCall`/`CallCount` helpers, and the repo must compile.

## 2. Pure containment predicate — `/workspace/pkg/steps_planning.go`

Add a package-level function, no receiver, no I/O:

```go
// refContainedInTag reports whether the compare payload proves base is an
// ancestor of (or equal to) head. GitHub's compare(base...head) reports
// behind_by = commits head lacks that base has, so behind_by == 0 means
// head already contains everything base contains — the release shipped.
// A nil BehindBy (key absent or renamed) is NOT proof: it fails open to
// planning, as does any status other than "ahead" / "identical".
func refContainedInTag(r githubtags.CompareResult) bool {
	if r.BehindBy == nil || *r.BehindBy != 0 {
		return false
	}
	return r.Status == "ahead" || r.Status == "identical"
}
```

## 3. Guard helper — `/workspace/pkg/steps_planning.go`

Add to `*planningStep`:

```go
// refAlreadyReleased reports whether ref is already contained in the named
// tag's commit, and returns the tag commit for the audit record. Fail-open
// by construction: EVERY error path returns (false, ""), so a transport
// failure, a rate limit, an absent tag or a decode error falls through to
// today's planning exactly as if the guard did not exist. An outage can
// never close a task.
func (s *planningStep) refAlreadyReleased(
	ctx context.Context,
	owner, name, ref, tag string,
) (bool, string) {
	tagCommit := s.releaseTagCommit(ctx, owner, name, tag)
	if tagCommit == "" {
		return false, ""
	}
	cmp, err := s.tagsFetcher.CompareCommits(ctx, owner, name, ref, tagCommit)
	if err != nil {
		glog.Warningf(
			"planning: compare failed for %s/%s ref=%s tag=%s (continuing as not released): %v",
			owner, name, ref, tag, err,
		)
		return false, ""
	}
	released := refContainedInTag(cmp)
	behind := -1 // -1 means "key absent" — distinct from a real 0
	if cmp.BehindBy != nil {
		behind = *cmp.BehindBy
	}
	glog.V(2).Infof(
		"planning: containment_check ref=%s tag=%s tag_commit=%s status=%s ahead=%d behind=%d released=%t",
		ref, tag, tagCommit, cmp.Status, cmp.AheadBy, behind, released,
	)
	return released, tagCommit
}
```

**Deliberate widening, state it in the commit message:** spec 002's ref bound (`isHexString`, `minRefMatchLen`) applied to a client-side SHA comparison. Here the comparison is server-side, so a short or branch-name `ref` is resolved by GitHub and simply returns a non-containable result unless GitHub says it is contained. The bound is not re-implemented; a non-hex ref cannot manufacture containment.

## 4. Guard placement in `Run`

In `Run`, immediately AFTER the `resolveCurrentVersion` call and BEFORE `s.fetcher.Fetch(...)`, insert:

```go
	// A record whose ref is already contained in the released tag has
	// nothing left to release. Without this check the happy path re-plans
	// the NEXT version from the pre-release ref, where ## Unreleased is
	// still present, and returns NextPhase=execution — so the record never
	// writes a terminal state for the version it actually shipped.
	if released, tagCommit := s.refAlreadyReleased(ctx, owner, name, ref, effectiveVersion); released {
		return s.publishNothingToRelease(ctx, md, effectiveVersion, tagCommit, fmt.Sprintf(
			"nothing to release: task ref %s is already contained in tag %s (%s)",
			ref, effectiveVersion, tagCommit,
		))
	}
```

The early return also skips the changelog fetch, which is correct — there is nothing to read.

## 5. Parameterise the reason, and correct the three now-false doc comments

`publishNothingToRelease` currently builds its own message. Change its signature to take the reason:

```go
func (s *planningStep) publishNothingToRelease(
	ctx context.Context,
	md *agentlib.Markdown,
	tag, tagCommit, reason string,
) (*agentlib.Result, error) {
```

Delete the local `reason := fmt.Sprintf(...)` line; the body is otherwise unchanged (`PlanOutput{Outcome, Reason: reason, CurrentVersion: tag}`, `md.Frontmatter["status"] = "completed"`, `md.Frontmatter["phase"] = "done"`, `NextPhase: "done"`).

Update the existing P1 call site in `handleValidationFailure` to pass the equality wording it has today, so the malformed-changelog path's message does not change:

```go
			return s.publishNothingToRelease(ctx, md, effectiveVersion, tagCommit, fmt.Sprintf(
				"nothing to release: task ref is the commit tag %s already points at (%s)",
				effectiveVersion, tagCommit,
			))
```

**Five doc comments are now false — correct all five in this change:**
- The `PlanOutcomeNothingToRelease` constant's comment in `pkg/plan_output.go` says the outcome is written only when the tag "points at the task's own ref … on a positive, observed SHA match". Rewrite it to name both cases: the equality case (P1 failure, tag points at ref) and the containment case (any planning pass, GitHub compare reports `behind_by: 0` with status `ahead` or `identical`), and state that any uncertainty falls back to planning.
- `handleValidationFailure`'s doc comment says "The release-tag check runs ONLY on a P1_unreleased_not_first failure". Scope the claim to this function: its tag check runs only on a P1 failure; the containment guard in `Run` runs on every pass.
- `Run`'s own doc comment lists "read frontmatter, fetch CHANGELOG, validate, classify bump" and omits the new first step. Add the containment check to that list.
- `sameCommit`'s doc comment ends "a nothing_to_release verdict may only be written on a positive match here" — false once the containment guard can write that verdict from a compare payload. Reword the tail to: "This equality bound now decides only the P1 malformed-changelog path; the containment guard in Run writes nothing_to_release from GitHub's compare payload instead (spec 006 § Constraints)."
- `publishNothingToRelease`'s doc comment says "block naming the tag and **the shared commit SHA**" — true only for the P1 equality case. Reword to "the tag and the commit compared against the ref".

## 6. Test seam export — `/workspace/pkg/export_test.go`

```go
// RefContainedInTagForTest exposes the unexported refContainedInTag helper
// so the external _test package can pin the compare-payload contract.
var RefContainedInTagForTest = refContainedInTag
```

## 7. Tests — append to `/workspace/pkg/steps_planning_test.go` (package `pkg_test`)

Add one new top-level `Describe("steps_planning containment guard", …)` block. The fixtures `taskMD`, `releasedChangelog` and `incidentSHA` in the existing `Describe("steps_planning nothing_to_release (spec 002)", …)` block are **closure-local** — declare your own inside the new block (full 40-character SHAs; do not copy a truncated or ellipsised value). Because `BehindBy` is now a pointer, add a tiny local helper in the new block and use it in every fixture:

```go
	intPtr := func(i int) *int { return &i }
```

**Prime the effective version in every case.** Either call `withNoRemoteTags()` (which falls back to the fixture's `current_version`) or `tagsFetcher.LatestSemverTagReturns("v0.3.1", nil)`. Do NOT leave it unset: the mock then returns `("", nil)`, `effectiveVersion` becomes empty, and every `PlanOutcomeReady` case escalates instead of planning.

**First, update the two tests this change invalidates.** Both assert `tagsFetcher.CommitSHAForTagCallCount() == 0`, which the guard now makes 1. Rename each so its name stops asserting the opposite of what it checks:
- `It("non-hex ref cannot match", …)` → `It("non-hex ref cannot match by commit equality", …)` — it still passes unchanged (the zero-value `CompareResult` is not containment); only the name needs to stop claiming the containment path cannot fire.
- `It("other preconditions never consult the tag seam", …)` → `It("other preconditions consult the tag seam once but still escalate", …)` — change the assertion to `Equal(1)`, add `tagsFetcher.CompareCommitsReturns(githubtags.CompareResult{Status: "behind", BehindBy: intPtr(2)}, nil)` (spec 006 AC12 requires an explicit non-contained payload, not the mock's zero value), and rewrite the inline comment: the containment guard consults the tag seam once on every pass, GitHub reports the ref is NOT contained, so planning still reaches the unchanged P2 escalation.
- `It("happy path makes no extra request", …)` → `It("happy path consults the tag seam once and still plans", …)` — change to `Equal(1)`, add the same explicit non-contained `CompareCommitsReturns` stub, and note planning still reaches `PlanOutcomeReady`.

Every OTHER existing test must still pass unmodified.

Then add the new coverage. Items 3–9 are the *planning* cases — every one of them must use a VALID `## Unreleased` changelog (the same shape as item 1), not the P1 `releasedChangelog` fixture; with the P1 fixture they return `NeedsInput` instead of `Ready` and the assertion fails for the wrong reason.

1. **The regression this prompt exists for** — `It("a task whose ref is already released closes terminal instead of re-planning", …)`. Changelog fixture carries a VALID `## Unreleased` with bullets (the pre-fix shape: nothing looks malformed). Task `ref: "1111111111111111111111111111111111111111"`, `CommitSHAForTagReturns("2222222222222222222222222222222222222222", nil)`, `CompareCommitsReturns(githubtags.CompareResult{Status: "ahead", AheadBy: 1, BehindBy: intPtr(0)}, nil)`. Assert:
   - `plan.Outcome == pkg.PlanOutcomeNothingToRelease`
   - `result.Status == agentlib.AgentStatusDone` AND `result.NextPhase == "done"`
   - `md.Frontmatter["status"] == "completed"` AND `md.Frontmatter["phase"] == "done"`
   - `md.Frontmatter` does NOT have key `"previous_assignee"`
   - `fakeRunner.RunCallCount() == 0` — the bump LLM is never reached
   - `fakeFetcher.FetchCallCount() == 0` — the changelog is never fetched
   - `maintainerConfig.FetchCallCount() == 0` — the guard returns before `resolveMaintainerConfig`, so a malformed `.maintainer.yaml` cannot divert the closure to `human_review`
   - `tagsFetcher.CommitSHAForTagCallCount() == 1` and `tagsFetcher.CompareCommitsCallCount() == 1` — exactly one lookup and one compare per pass
   - `plan.Reason` contains the tag and the tag commit, and does NOT contain `"is the commit tag"` (the P1 equality wording)
   - the compare was called with its arguments in the right order: counterfeiter returns **all five** parameters, so destructure `_, _, _, base, head := tagsFetcher.CompareCommitsArgsForCall(0)` — `base` equals the task ref and `head` equals the tag commit
2. **Equal ref still closes** — `{Status: "identical", AheadBy: 0, BehindBy: intPtr(0)}` → `nothing_to_release`.
3. **New work still plans** — `It("a ref ahead of the tag still plans a release", …)`. `CompareCommitsReturns({Status: "behind", AheadBy: 0, BehindBy: intPtr(2)}, nil)` → `plan.Outcome == pkg.PlanOutcomeReady`. This is the anti-overreach test: the guard must not close a record that has unreleased work.
4. **Diverged ref still plans** — `{Status: "diverged", AheadBy: 3, BehindBy: intPtr(2)}` → `PlanOutcomeReady`.
5. **Fail-open — compare error** — `CompareCommitsReturns({}, stderrors.New("compare: status 503"))` → `PlanOutcomeReady`. An outage never closes a task.
6. **Fail-open — tag absent** — `CommitSHAForTagReturns("", githubtags.ErrTagNotFound)` → `PlanOutcomeReady`, and `CompareCommitsCallCount() == 0` (no compare is attempted without a tag commit).
7. **Unknown status fails open** — `{Status: "", AheadBy: 0, BehindBy: intPtr(0)}` → `PlanOutcomeReady`.
8. **Absent `behind_by` fails open — the safety case.** `It("a payload without behind_by never closes a task", …)`. `CompareCommitsReturns({Status: "ahead", AheadBy: 1, BehindBy: nil}, nil)` → `PlanOutcomeReady`. This is the regression that proves a renamed or missing field cannot silently close a record with unreleased work.
9. **Non-hex ref is resolved server-side, both ways.** `ref: "main"` with `CommitSHAForTagReturns(tagSHA, nil)`:
   - `CompareCommitsReturns({Status: "behind", BehindBy: intPtr(1)}, nil)` → `PlanOutcomeReady`, and the compare IS called (the dropped client-side bound is a recorded decision, not an oversight);
   - `CompareCommitsReturns({Status: "ahead", AheadBy: 1, BehindBy: intPtr(0)}, nil)` → `PlanOutcomeNothingToRelease` (a branch ref GitHub resolves is treated exactly like a SHA).
10. **`refContainedInTag` table — `DescribeTable` over `pkg.RefContainedInTagForTest`:**
   - `{Status: "ahead", BehindBy: intPtr(0)}` → true
   - `{Status: "identical", BehindBy: intPtr(0)}` → true
   - `{Status: "behind", BehindBy: intPtr(2)}` → false
   - `{Status: "diverged", BehindBy: intPtr(2)}` → false
   - `{Status: "", BehindBy: intPtr(0)}` → false (unknown status is not proof)
   - `{Status: "ahead", BehindBy: nil}` → false (absent key is not zero)
11. **Deliverer boundary** — mirror the bug-048 regression's shape: build `agentlib.NewAgent(agentlib.NewPhase(domain.TaskPhasePlanning, step))`, deliver through `delivery.NewFileResultDeliverer(delivery.NewPassthroughContentGenerator(), taskFile)`, call `agent.Run`, then assert the file the deliverer wrote CONTAINS `status: completed`, `phase: done` and `assignee: github-releaser-agent`, and does NOT contain `previous_assignee`. This is the only test that proves the closure survives the framework.

## 7b. `CompareCommits` contract test — append to `/workspace/pkg/githubtags/tags_test.go`

The tests above mock at the interface level, so the new endpoint string, the header set and the JSON tags are otherwise never traversed — a typo in `behind_by` or in the `compare/%s...%s` path would pass every one of them and fail at runtime. Add one `It` block using the existing `NewHTTPTagsFetcherForTest` + `httptest.NewServer` harness. This is a different package — do NOT reuse the `intPtr` helper from `pkg_test`; assert the pointer directly:

- Serve `{"status":"ahead","ahead_by":1,"behind_by":0}`; assert the returned `Status` is `"ahead"`, `AheadBy` is `1`, and `BehindBy` is non-nil with value `0`; assert the recorded request path is exactly `/repos/owner/repo/compare/base...head` (pins both the endpoint shape and the base/head order) and that the request carried `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28` and the bearer `Authorization` header.
- Serve `{"status":"ahead","ahead_by":1}` with **no** `behind_by` key; assert `BehindBy` is nil — this is the payload-shape guard that keeps a renamed field from reading as containment.
- Serve a 404 body; assert a non-nil wrapped error (unknown ref → fail-open at the caller).
- Serve malformed JSON; assert a non-nil wrapped error.
- Empty owner, empty repo, empty base, empty head each return a non-nil error **without a request** (assert the handler was never reached).

## 8. `docs/dod.md` — rewrite the tail of the escalation bullet

The existing bullet at `docs/dod.md:19` currently ends:

```
That exception requires a positive, observed match (hex-only, at least 7 characters, case-insensitive prefix); a mismatch, an absent tag, or any lookup error falls back to the escalation above
```

Replace **that tail only** with:

```
It is joined by a second exception, both requiring a positive, observed GitHub answer and both falling back to the escalation above on any uncertainty: (a) that SHA-verified plan (hex-only, at least 7 characters, case-insensitive prefix match); and (b) a containment-verified nothing_to_release plan — any planning pass where GitHub's compare of `ref...tag-commit` reports `behind_by: 0` with status `ahead` or `identical`, proving the ref is already contained in the released tag. A ref ahead of or diverged from the tag, an absent tag, or any compare error falls back to the escalation above
```

Do NOT append a new clause after "falls back to the escalation above" — that binds the new text to "falls back" and inverts its meaning. Do not restructure the rest of the file.

## 9. `CLAUDE.md` and `README.md`

- `CLAUDE.md` § Key Design Decisions: the existing "Escalation over guessing" bullet already reads "never auto-advances to `done`" with no carve-out, which is false since spec 002 and doubly false now. Append ", except the nothing_to_release carve-outs below" after that phrase (no number — spec 002's SHA-verified carve-out is not currently documented in this file, so only the new bullet appears below), then add one bullet —
  `- **Containment closure** — planning closes as `nothing_to_release` when the task ref is already contained in the released tag (GitHub compare, `behind_by: 0`, status `ahead` or `identical`). Fail-open on any lookup or compare error.`
- `README.md` has no planning section — its `## How it works` list is execution-only. Add one sentence as a new first step in that list — renumber the existing 1–5 to 2–6 so the list stays sequential: "Skip a task whose ref is already contained in the released tag — planning closes it as `completed` instead of re-planning the next version."

## 10. CHANGELOG

`## Unreleased` does not exist; create it below the preamble block (after the "adheres to Semantic Versioning" line) and above `## v0.10.0`, per `docs/dod.md` § Documentation. Exactly one bullet:

```
- fix(planning): a release task whose ref is already contained in the effective current version's tag now closes as `completed` instead of re-planning the next version. The "already released?" check previously ran only after a `P1_unreleased_not_first` validation failure, so a healthy re-trigger re-planned forever and never wrote a terminal state — measured across 24 records on 2026-11-08. The check now runs on every planning pass and uses GitHub's compare payload (ref contained in the tag commit) rather than commit equality, which could never fire because the release commit is a child of the task ref. A ref ahead of or diverged from the tag still plans; any lookup or compare error falls through to planning unchanged
```
</requirements>

<constraints>
- Fail-open is mandatory: every error path from `releaseTagCommit` or `CompareCommits` must fall through to today's planning. Never a silent completion on uncertainty.
- The guard runs BEFORE the changelog fetch, so a closing record costs one tag lookup plus one compare and makes no content request.
- The guard also runs before `resolveMaintainerConfig`, so a malformed `.maintainer.yaml` is bypassed when the ref is already contained. That is intended — there is nothing to configure for a task that will not release — and must be mentioned in the commit message.
- The P1 malformed-changelog path keeps its existing wording and behaviour. Only the shared `publishNothingToRelease` signature changes.
- `changelog.ValidateUnreleased`, `classifyValidationFailure`, `escalate`, and the `escalation` struct are NOT modified.
- `PlanOutput` fields keep their JSON names; the change adds no new outcome constant.
- Do NOT touch `pkg/steps_execution.go` or `pkg/steps_ai_review.go`.
- Do NOT add a config knob, a Prometheus metric, or a feature flag.
- Errors use `github.com/bborbe/errors` with context wrapping — never `fmt.Errorf`, never `context.Background()`. Logging is `glog` with `V(n)`-gated `Info`; the compare-failure path uses `glog.Warningf`, mirroring `releaseTagCommit`.
- Keep `Run` within the funlen budget (80 lines / 50 statements). If it trips, extract the guard into `s.closeIfAlreadyReleased(ctx, md, owner, name, ref, effectiveVersion) (*agentlib.Result, bool, error)` and leave `Run`'s body otherwise unchanged.
- Repo-relative paths only.
- Do NOT commit — dark-factory handles git.
- Two existing tests assert the tag seam is never consulted and MUST be updated (requirement 7); every other existing test must still pass, unmodified.
</constraints>

<verification>
This run uses `hideGit=true`, which masks `.git` in the container. That empties `ROOTDIR` (`Makefile.variables` derives it from `git rev-parse`), so EVERY `make` call must pass `ROOTDIR=/workspace` explicitly or it dies at `include $(ROOTDIR)/default.env`. Do not run bare `make`, and do not run any `git` command.

Run from `/workspace`:

```
cd /workspace && go generate ./...
cd /workspace && ROOTDIR=/workspace make test
cd /workspace && grep -n 'CompareCommits' pkg/githubtags/tags.go mocks/tags_fetcher.go
cd /workspace && grep -n 'containment_check ref=' pkg/steps_planning.go
cd /workspace && grep -q 'containment-verified nothing_to_release plan' docs/dod.md && echo 'DOD OK' || { echo 'DOD FAILED: carve-out missing'; exit 1; }
cd /workspace && test "$(awk '/^## /{s=$0} /already contained in the effective current version/{print s}' CHANGELOG.md | head -1)" = '## Unreleased' && echo 'CHANGELOG OK' || { echo 'CHANGELOG FAILED: bullet is not under ## Unreleased'; exit 1; }
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/... && go tool cover -func=/tmp/cover.out | grep -E 'refContainedInTag|refAlreadyReleased|CompareCommits|total'
```

The coverage line must show `refContainedInTag` at 100.0% (it is table-tested), and `refAlreadyReleased` and `CompareCommits` at or above 80.0%. Report the printed percentages; do not claim coverage you did not read.

Then `ROOTDIR=/workspace make precommit` — must exit 0 (fmt, generate, test, lint, vet, vuln, license).

Before you finish, re-run `<verification>` and confirm it passes; walk each acceptance criterion against the change.
</verification>

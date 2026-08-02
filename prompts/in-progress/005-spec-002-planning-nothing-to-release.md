---
status: approved
spec: [002-planning-close-nothing-to-release]
created: "2026-08-02T15:20:00Z"
queued: "2026-08-02T13:42:58Z"
branch: dark-factory/planning-close-nothing-to-release
---

<summary>
- A release task that fires against a commit which was already released now closes itself instead of parking forever waiting for an operator.
- Before rejecting a task for "the changelog has no Unreleased section on top", planning asks GitHub which commit the current version's tag points at.
- If that is the exact commit the task targets, there is genuinely nothing to release and the task ends as completed.
- The task file records the deciding facts — the tag consulted and the shared commit SHA — so the outcome is auditable.
- No operator hand-off is written on that path: the task is not put back in anyone's queue.
- Everything else still escalates exactly as today: a different commit, a missing tag, a lookup failure, a short or non-hex ref, or any other precondition.
- A GitHub outage can never turn an escalation into a silent completion — uncertainty always goes to a human.
- Normal releases pay no extra API call; the check only runs on the failing path.
- One greppable log line records the decision on both branches for operator triage.
- Documents the single carve-out to the "never auto-advance to done" escalation rule so the rule and its exception cannot drift apart.
</summary>

<objective>
Teach the planning step to complete a release task as `nothing_to_release` — instead of escalating with `P1_unreleased_not_first` — when, and only when, GitHub confirms the task's `ref` is the exact commit the effective current version's tag already points at. Every other situation escalates byte-identically to today. Ships the behavior, its unit tests, the CHANGELOG entry, and the `docs/dod.md` escalation-contract carve-out.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions.

**Prerequisite check — run FIRST, before writing any code:**

```
cd /workspace && grep -n "CommitSHAForTag" pkg/githubtags/tags.go && grep -n "ErrTagNotFound" pkg/githubtags/tags.go && grep -n "func (fake \*TagsFetcher) CommitSHAForTag" mocks/tags_fetcher.go
```

If any of the three greps returns nothing, STOP immediately: do not implement, do not stub the seam, and report `"status":"failed"` with the message `tag-commit seam not yet deployed (prompt 1-spec-002-tag-commit-seam)`. This prompt cannot compile without it.

Read these files fully BEFORE writing code:
- `/workspace/pkg/steps_planning.go` — the file you are changing. Note `Run` (the `changelog.ValidateUnreleased` failure block that calls `s.escalate`), `resolveCurrentVersion`, `escalate` + the `escalation` value type, `failInvalidConfig`, `classifyValidationFailure`, `readRequired`, and the `AgentLogin` constant.
- `/workspace/pkg/plan_output.go` — the `PlanOutput` struct, the `PlanOutcome*` constants, and the `Precondition*` constants.
- `/workspace/pkg/steps_ai_review.go` — read `finishReviewOverride` (around the `md.Frontmatter["status"] = "completed"` / `md.Frontmatter["phase"] = "done"` writes and the `Status: agentlib.AgentStatusDone, NextPhase: "done"` return). That is the EXACT terminal disposition shape you must mirror — the explicit `NextPhase: "done"` form, never the empty-`NextPhase` auto-advance described in the `escalate` doc comment (bug 048).
- `/workspace/pkg/changelog/changelog.go` — `ValidateUnreleased`. A fully-released CHANGELOG (no `## Unreleased` heading at all, first `## ` heading is a version header) yields the reason `"Unreleased is not the first ## section; found '<v>' at line N. …"`, which `classifyValidationFailure` maps to `P1_unreleased_not_first`. That is the incident shape your happy-path fixture must reproduce. A changelog with ZERO `## ` headings yields `"Unreleased section not found."` → `P2_unreleased_empty`.
- `/workspace/pkg/githubtags/tags.go` — the seam: `CommitSHAForTag(ctx, owner, repo, tag string) (string, error)` and the `ErrTagNotFound` sentinel added by prompt 1.
- `/workspace/pkg/steps_planning_test.go` — the test style you must extend: `withNoRemoteTags()` helper, `pkg.NewPlanningStep(runner, fetcher, maintainerConfig, tagsFetcher, allowMajor)`, `agentlib.ParseMarkdown`, `agentlib.ExtractSection[pkg.PlanOutput](ctx, md, "## Plan")`.
- `/workspace/pkg/export_test.go` — the `XxxForTest` seam-export idiom you will extend.
- `/workspace/docs/dod.md` — the `## Git-Write Safety` section; the escalation-contract bullet you will amend is the one ending `never auto-advances to `done``.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Incident being closed (spec 002 § Problem), for fixture realism: `bborbe/tts-mcp`, task `ref: 7657fc0af1a115b2518ac4c4d332722d8fc3d35c`, tag `v0.3.1` points at that same commit, agent recorded `outcome: needs_input, precondition_failed: P1_unreleased_not_first`.
</context>

<requirements>

## 1. New outcome constant — `/workspace/pkg/plan_output.go`

Add to the `PlanOutcome*` const block:

```go
	// PlanOutcomeNothingToRelease signals the task targets a commit that
	// is ALREADY released: the effective current version's tag points at
	// the task's own ref. Terminal success — the step returns
	// Status=AgentStatusDone, NextPhase="done" and writes NO escalation
	// frontmatter. Only ever written on a positive, observed SHA match
	// (spec 002 § Desired Behavior 2/3).
	PlanOutcomeNothingToRelease = "nothing_to_release"
```

Update the `PlanOutput` doc comment's shape list from three to four valid shapes, adding:

```
//   - Outcome="nothing_to_release" — ref is already released; Reason + CurrentVersion populated
```

Do NOT add, rename, or re-tag any `PlanOutput` field — the change is constant-only and additive.

## 2. Pure match helpers — `/workspace/pkg/steps_planning.go`

Add these package-level declarations (no receiver, no I/O):

```go
// minRefMatchLen is the shortest task ref that may participate in a commit
// match. Below this a truncated or hand-edited ref could prefix-match an
// unrelated commit, so anything shorter is a non-match (spec 002
// § Constraints "Match bound").
const minRefMatchLen = 7

// releaseTagVerdictEscalate is the log token for the non-matching branch of
// the release-tag check. The matching branch logs PlanOutcomeNothingToRelease.
const releaseTagVerdictEscalate = "escalate"

// isHexString reports whether s is non-empty and consists only of hex
// digits. Non-hex refs (e.g. a branch name like "main") can never match a
// commit SHA.
func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// sameCommit reports whether ref and tagCommit denote the same commit.
// All four bounds must hold: both hex-only, both at least minRefMatchLen
// characters, and — after lowercasing — the shorter is a prefix of the
// longer. Empty, short, or non-hex input is always a non-match; a
// nothing_to_release verdict may only be written on a positive match here.
func sameCommit(ref, tagCommit string) bool {
	a := strings.ToLower(strings.TrimSpace(ref))
	b := strings.ToLower(strings.TrimSpace(tagCommit))
	if len(a) < minRefMatchLen || len(b) < minRefMatchLen {
		return false
	}
	if !isHexString(a) || !isHexString(b) {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

// releaseTagVerdict returns the log/decision token for the release-tag
// check: PlanOutcomeNothingToRelease on a positive commit match,
// releaseTagVerdictEscalate otherwise. Pure function of its inputs — the
// same token is written to the log line and drives the branch, so the two
// can never disagree.
func releaseTagVerdict(ref, tagCommit string) string {
	if sameCommit(ref, tagCommit) {
		return PlanOutcomeNothingToRelease
	}
	return releaseTagVerdictEscalate
}
```

`strings` is already imported.

## 3. Fail-open tag-commit lookup

Add to `*planningStep`:

```go
// releaseTagCommit returns the commit SHA the named tag points at, or ""
// when the tag is absent or the lookup failed. Fail-open by construction:
// EVERY error path returns "", which yields a non-match and therefore the
// unchanged escalation. A GitHub outage can never convert an escalation
// into a silent completion (spec 002 § Failure Modes rows 1, 2, 7).
func (s *planningStep) releaseTagCommit(
	ctx context.Context,
	owner, name, tag string,
) string {
	sha, err := s.tagsFetcher.CommitSHAForTag(ctx, owner, name, tag)
	if err != nil {
		if stderrors.Is(err, githubtags.ErrTagNotFound) {
			glog.V(2).Infof("planning: tag %s absent on %s/%s", tag, owner, name)
			return ""
		}
		glog.Warningf(
			"planning: tag-commit lookup failed for %s/%s tag=%s (escalating): %v",
			owner, name, tag, err,
		)
		return ""
	}
	return sha
}
```

`stderrors`, `githubtags`, and `glog` are already imported by this file.

## 4. Route the validation failure through a new decision helper

In `Run`, replace the current validation-failure block:

```go
	valid, reason, _ := changelog.ValidateUnreleased(changelogBytes)
	if !valid {
		precondition := classifyValidationFailure(reason)
		glog.V(2).
			Infof("planning: validate Unreleased failed precondition=%s reason=%q", precondition, reason)
		return s.escalate(ctx, md, escalation{
			reason:             reason,
			preconditionFailed: precondition,
			currentVersion:     currentVersion,
		})
	}
```

with:

```go
	valid, reason, _ := changelog.ValidateUnreleased(changelogBytes)
	if !valid {
		return s.handleValidationFailure(
			ctx, md, owner, name, ref, effectiveVersion, reason, currentVersion,
		)
	}
```

and add the helper:

```go
// handleValidationFailure decides between completing the task as
// nothing_to_release and the unchanged escalation.
//
// The release-tag check runs ONLY on a P1_unreleased_not_first failure —
// the one case where a healthy post-release changelog is misread as
// malformed. Every other precondition (P2_unreleased_empty,
// bad_current_version, missing_frontmatter_*) escalates without touching
// the tag seam, so the happy path and the other failure paths make no
// extra GitHub request (spec 002 § Constraints).
//
// snapshotVersion is the frontmatter current_version, preserved verbatim on
// the escalation path; effectiveVersion is the tag resolved by
// resolveCurrentVersion and is the tag consulted for the commit lookup.
func (s *planningStep) handleValidationFailure(
	ctx context.Context,
	md *agentlib.Markdown,
	owner, name, ref, effectiveVersion, reason, snapshotVersion string,
) (*agentlib.Result, error) {
	precondition := classifyValidationFailure(reason)
	glog.V(2).
		Infof("planning: validate Unreleased failed precondition=%s reason=%q", precondition, reason)
	if precondition == PreconditionP1UnreleasedNotFirst {
		tagCommit := s.releaseTagCommit(ctx, owner, name, effectiveVersion)
		verdict := releaseTagVerdict(ref, tagCommit)
		glog.V(2).Infof(
			"planning: release_tag_check ref=%s tag=%s tag_commit=%s verdict=%s",
			ref, effectiveVersion, tagCommit, verdict,
		)
		if verdict == PlanOutcomeNothingToRelease {
			return s.publishNothingToRelease(ctx, md, effectiveVersion, tagCommit)
		}
	}
	return s.escalate(ctx, md, escalation{
		reason:             reason,
		preconditionFailed: precondition,
		currentVersion:     snapshotVersion,
	})
}
```

The `glog.V(2).Infof("planning: release_tag_check ref=%s tag=%s tag_commit=%s verdict=%s", …)` format string MUST stay on ONE source line and MUST keep all four tokens `ref=`, `tag=`, `tag_commit=`, `verdict=` (spec AC #12 greps for them on one line). It is emitted exactly once whenever the P1 check runs, on BOTH branches, with the neutral `release_tag_check` prefix.

Do NOT change `escalate`, the `escalation` struct, the P1 reason string, or the `PreconditionP1UnreleasedNotFirst` value.

## 5. Terminal completion path

```go
// publishNothingToRelease writes a ## Plan(outcome=nothing_to_release)
// block naming the tag and the shared commit SHA, marks the task
// completed/done, and returns the EXPLICIT terminal disposition
// (Status=Done, NextPhase="done") used by ai_review's finishReviewOverride
// — never the empty-NextPhase auto-advance (bug 048).
//
// It deliberately writes NO escalation frontmatter: `assignee` is left
// byte-identical to its input value and `previous_assignee` is never set.
// There is nothing for an operator to pick up (spec 002 § Desired Behavior 2).
func (s *planningStep) publishNothingToRelease(
	ctx context.Context,
	md *agentlib.Markdown,
	tag, tagCommit string,
) (*agentlib.Result, error) {
	reason := fmt.Sprintf(
		"nothing to release: task ref is the commit tag %s already points at (%s)",
		tag, tagCommit,
	)
	output := PlanOutput{
		Outcome:        PlanOutcomeNothingToRelease,
		Reason:         reason,
		CurrentVersion: tag,
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Plan", output)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "marshal ## Plan section (nothing_to_release)")
	}
	md.ReplaceSection(section)
	md.Frontmatter["status"] = "completed"
	md.Frontmatter["phase"] = "done"
	glog.V(2).Infof("planning: nothing to release — tag=%s commit=%s", tag, tagCommit)
	return &agentlib.Result{
		Status:    agentlib.AgentStatusDone,
		NextPhase: "done",
		Message:   reason,
	}, nil
}
```

`PreconditionFailed` MUST stay empty on this path — it is a completion, not an escalation. `fmt` and `errors` are already imported.

## 6. Test seam exports — `/workspace/pkg/export_test.go`

Append:

```go
// SameCommitForTest exposes the unexported sameCommit helper so the
// external _test package can exercise the four match bounds directly.
var SameCommitForTest = sameCommit

// ReleaseTagVerdictForTest exposes the unexported releaseTagVerdict helper
// so the external _test package can assert the exact verdict token emitted
// on each branch of the release-tag check.
var ReleaseTagVerdictForTest = releaseTagVerdict
```

## 7. Tests — append to `/workspace/pkg/steps_planning_test.go` (package `pkg_test`)

Add one new top-level `Describe("steps_planning nothing_to_release (spec 002)", …)` block. Shared fixtures inside it:

```go
	const incidentSHA = "7657fc0af1a115b2518ac4c4d332722d8fc3d35c"

	// A HEALTHY post-release changelog: no ## Unreleased at all, first ##
	// heading is a version header → ValidateUnreleased reports
	// "Unreleased is not the first ## section" → P1_unreleased_not_first.
	// This is the exact shape of the bborbe/tts-mcp incident.
	releasedChangelog := []byte(
		"# Changelog\n\nIntro.\n\n## v0.3.1\n\n- fix: something\n\n## v0.3.0\n\n- old\n",
	)

	taskMD := func(ref string) string {
		return "---\nstatus: in_progress\nphase: planning\n" +
			"assignee: github-releaser-agent\ntask_type: github-release\n" +
			"repo: bborbe/tts-mcp\nclone_url: https://github.com/bborbe/tts-mcp.git\n" +
			"ref: " + ref + "\ncurrent_version: v0.3.1\n" +
			"task_identifier: gh-release-bborbe-tts-mcp-spec002\n---\n\n# release task\n"
	}
```

Every test builds the step with `pkg.NewPlanningStep(fakeRunner, fakeFetcher, &mocks.MaintainerConfigFetcher{}, tagsFetcher, false)` where `tagsFetcher.LatestSemverTagReturns("v0.3.1", nil)` unless stated otherwise, and asserts `fakeRunner.RunCallCount() == 0` on every escalation/completion path (the LLM is never reached).

Cover ALL of:

1. **`It("nothing to release: ref equals the tag's commit → outcome=nothing_to_release, task completed", …)`** — the It name MUST contain the literal phrase `nothing to release` (spec AC #1 greps test names). `CommitSHAForTagReturns(incidentSHA, nil)`, task ref `incidentSHA`. Assert:
   - `plan.Outcome == pkg.PlanOutcomeNothingToRelease`
   - `plan.Reason` contains `"v0.3.1"` AND contains `incidentSHA`
   - `plan.PreconditionFailed` is empty
   - `result.Status == agentlib.AgentStatusDone` AND `result.NextPhase == "done"`
   - `result.Status != agentlib.AgentStatusNeedsInput`
   - the seam was consulted with the resolved tag: `owner, name, tag := tagsFetcher.CommitSHAForTagArgsForCall(0)` → `"bborbe"`, `"tts-mcp"`, `"v0.3.1"`
2. **No escalation frontmatter on the completing path** — same setup. Assert `md.Frontmatter` does NOT have key `"previous_assignee"` (`Expect(md.Frontmatter).NotTo(HaveKey("previous_assignee"))`), `md.Frontmatter["assignee"]` equals `"github-releaser-agent"` (byte-identical to the input value), and `md.Frontmatter["status"] == "completed"` / `md.Frontmatter["phase"] == "done"`.
3. **Short ref still completes when within bounds** — task ref `"7657fc0"` (7 chars) against `CommitSHAForTagReturns(incidentSHA, nil)` → `outcome=nothing_to_release`.
4. **Negative — different commit escalates** — `CommitSHAForTagReturns("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil)`, task ref `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"` → `result.Status == agentlib.AgentStatusNeedsInput`, `plan.Outcome == pkg.PlanOutcomeNeedsInput`, `plan.PreconditionFailed == pkg.PreconditionP1UnreleasedNotFirst`, `md.Frontmatter["previous_assignee"] == pkg.AgentLogin`, `md.Frontmatter["assignee"] == ""`.
5. **Negative — tag absent escalates** — `CommitSHAForTagReturns("", githubtags.ErrTagNotFound)`, task ref `incidentSHA` → `needs_input` + `P1_unreleased_not_first`.
6. **Negative — lookup failure escalates (fail-open)** — `CommitSHAForTagReturns("", stderrors.New("list tags: status 503: server error"))`, task ref `incidentSHA` → `needs_input` + `P1_unreleased_not_first`. A GitHub outage never completes a task.
7. **Negative — short ref cannot manufacture a match** — task ref `"a"`, `CommitSHAForTagReturns("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)` → `needs_input` + `P1_unreleased_not_first`.
8. **Negative — non-hex ref cannot match** — task ref `"main"`, `CommitSHAForTagReturns(incidentSHA, nil)` → `needs_input` + `P1_unreleased_not_first`.
9. **Negative — other preconditions never consult the tag seam** — changelog `"## Unreleased\n\n## v0.3.1\n\n- old\n"` (Unreleased first, no bullets → `P2_unreleased_empty`), task ref `incidentSHA`, `CommitSHAForTagReturns(incidentSHA, nil)` → `needs_input` + `plan.PreconditionFailed == pkg.PreconditionP2UnreleasedEmpty` AND `tagsFetcher.CommitSHAForTagCallCount() == 0`. (Counterfeiter's `Invocations()` is a map keyed by method name; asserting the per-method call count is the precise form of the spec's "zero calls for that input".)
10. **Negative — happy path makes no extra request** — changelog `"## Unreleased\n\n- feat: add foo\n\n## v0.3.1\n\n- old\n"`, runner returning `{"bump":"patch","reasoning":"stub"}` → `plan.Outcome == pkg.PlanOutcomeReady` AND `tagsFetcher.CommitSHAForTagCallCount() == 0`. Normal releases gain no latency.
11. **`sameCommit` bounds — `DescribeTable` over `pkg.SameCommitForTest`:**
    - `("7657fc0", incidentSHA)` → true (7-char prefix matches 40-char SHA)
    - `("7657fc1", incidentSHA)` → false (differs in the last character)
    - `("7657FC0", incidentSHA)` → true (case-insensitive)
    - `(incidentSHA, strings.ToUpper(incidentSHA))` → true
    - `("765fc0", incidentSHA)` → false (6 chars, below the bound)
    - `("main", incidentSHA)` → false (non-hex)
    - `("", incidentSHA)` → false
    - `(incidentSHA, "")` → false
    - `("7657fc0", "7657fc0")` → true (equal short-vs-short at the bound)
12. **`releaseTagVerdict` tokens — `pkg.ReleaseTagVerdictForTest`:** match → `"nothing_to_release"`; mismatch → `"escalate"`. Assert the literal strings, not only the constants, so the greppable log token is pinned.
13. **Deliverer boundary — the completing disposition must survive the framework.** Every test above stops at `step.Run(ctx, md)` and asserts on `md.Frontmatter`, but what actually lands in the task file is decided *after* the step returns, by the framework's result deliverer — and that boundary is exactly where bug 048 lived. Mirror the existing bug-048 regression `It` at `pkg/steps_planning_test.go:2288-2386` (which pins the *escalation* half of this same invariant): build `agentlib.NewAgent(agentlib.NewPhase(domain.TaskPhasePlanning, step))`, deliver through `delivery.NewFileResultDeliverer(delivery.NewPassthroughContentGenerator(), taskFile)`, and call `agent.Run`. Use the released-changelog fixture, task `ref: incidentSHA`, and `CommitSHAForTagReturns(incidentSHA, nil)`. Assert the file the deliverer actually wrote **contains** `status: completed`, `phase: done`, and `assignee: github-releaser-agent` (byte-identical to input), and does **NOT** contain `previous_assignee`. This is the only test that proves AC 2 and AC 3 at the level where they operationally mean something — the deliverer demonstrably rewrites `assignee` on the NeedsInput path, so asserting it is untouched here is a real assertion, not a tautology.

Existing tests must keep passing untouched: every existing planning test uses `ref: master` (non-hex → escalate) and a `mocks.TagsFetcher` whose `CommitSHAForTag` zero value returns `("", nil)` → non-match → the current escalation behavior. Do NOT edit existing test bodies.

## 8. Coverage

```
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/... && go tool cover -func=/tmp/cover.out | grep -E "steps_planning|total"
```
New code in `pkg` must reach ≥ 80% statement coverage.

## 9. `docs/dod.md` — escalation-contract carve-out

In the `## Git-Write Safety` section, replace the existing bullet

```
- Escalation contract preserved: a planning step that cannot proceed returns `NeedsInput`/`human_review` (assignee cleared, `previous_assignee: github-releaser-agent`) — never auto-advances to `done`
```

with the single-line bullet

```
- Escalation contract preserved: a planning step that cannot proceed returns `NeedsInput`/`human_review` (assignee cleared, `previous_assignee: github-releaser-agent`) — never auto-advances to `done`, except a SHA-verified nothing_to_release plan (spec 002): when a `P1_unreleased_not_first` failure coincides with GitHub reporting that the task `ref` IS the commit the current version's tag points at, planning writes `outcome: nothing_to_release` and returns `Done`/`NextPhase: done` with no escalation frontmatter. That exception requires a positive, observed match (hex-only, at least 7 characters, case-insensitive prefix); a mismatch, an absent tag, or any lookup error falls back to the escalation above
```

The substring `except a SHA-verified nothing_to_release` must appear **verbatim, with no backticks and no underscores changed** — `grep -n 'except a SHA-verified nothing_to_release' docs/dod.md` is an acceptance criterion. Keep it on ONE line. Do not restructure the rest of `docs/dod.md`.

## 10. CHANGELOG

Add to `/workspace/CHANGELOG.md` under `## Unreleased` (append to the existing section if prompt 1 already created it — do not replace it). **If `## Unreleased` does not exist** — the file currently starts at `## v0.3.2`, so this is the case on any run where prompt 1's CHANGELOG edit was reverted or dropped — create the section **below** the preamble block (the "adheres to Semantic Versioning" line) and **above** `## v0.3.2`, never between the `# Changelog` title and the preamble, per `docs/dod.md` § Documentation:

```
- feat(planning): close a release task as `completed` instead of escalating when the task ref is the commit the effective current version's tag already points at — a release check re-fired against a release commit is nothing to release, not a malformed changelog. The tag's commit SHA is consulted only after a `P1_unreleased_not_first` validation failure, matched hex-only, case-insensitively, at 7 characters or more, and every other case (different commit, absent tag, lookup error, short or non-hex ref, any other precondition) escalates byte-identically to before
```

Exactly ONE line in the whole file may contain the phrase `nothing to release` — the acceptance criterion is `awk '/^## /{s=$0} /nothing to release/{print s}' CHANGELOG.md` printing exactly one line, `## Unreleased`. Do not add a second bullet carrying that phrase, and do not reword prompt 1's entry.
</requirements>

<constraints>
- The `P1` reason string and the `precondition_failed` value are UNCHANGED on the escalation path. Downstream greps and the operator-side sweep skill key on the current wording.
- `changelog.ValidateUnreleased` is NOT modified. Its notion of a malformed changelog is untouched.
- No new client is wired into the planning step; the lookup uses the `tagsFetcher` seam already injected into `planningStep`. `NewPlanningStep`'s signature and `pkg/factory/factory.go` are unchanged.
- The check runs ONLY after a `P1_unreleased_not_first` validation failure. The happy path and every other precondition make no extra API call.
- One additional tags-list pagination on the P1 path — never one request per tag.
- Match bound: hex-only, minimum 7 characters, case-insensitive, shorter-is-prefix-of-longer. A `nothing_to_release` verdict may only be written on a positive, observed match meeting all four conditions.
- Fail-open is mandatory: every error path from the lookup (transport, rate-limit, decode, tag absent) falls through to today's escalation. Never a silent completion.
- Existing `PlanOutput` fields keep their JSON names and semantics; the change is additive (one new outcome constant).
- Do NOT touch the execution step's `released`/`superseded` post-check (`pkg/steps_execution.go`) or `pkg/steps_ai_review.go`.
- Do NOT add a scenario file, a config knob, a Prometheus metric, or a feature flag — the spec's Non-goals forbid new surface here.
- Errors use `github.com/bborbe/errors` with context wrapping — never `fmt.Errorf`, never `context.Background()`. Logging is `glog` with `V(n)`-gated `Info`; the one operator-facing lookup failure uses `glog.Warningf`, mirroring `resolveCurrentVersion`.
- Keep `Run` within the funlen budget (80 lines / 50 statements): the replacement block is shorter than what it replaces, so do not inline the new logic into `Run`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unmodified.
</constraints>

<verification>
Run from `/workspace`:

```
cd /workspace && make test
```

Then:

```
cd /workspace && grep -n 'nothing_to_release' pkg/plan_output.go
cd /workspace && grep -n 'planning: release_tag_check ref=' pkg/steps_planning.go
cd /workspace && grep -n 'planning: release_tag_check ref=' pkg/steps_planning.go | grep 'tag=' | grep 'tag_commit=' | grep 'verdict='
cd /workspace && grep -n 'nothing to release' CHANGELOG.md
cd /workspace && test "$(awk '/^## /{s=$0} /nothing to release/{print s}' CHANGELOG.md)" = '## Unreleased' && echo 'AC13 OK' || { echo 'AC13 FAILED: bullet is not under a single ## Unreleased'; exit 1; }
cd /workspace && grep -q 'except a SHA-verified nothing_to_release' docs/dod.md && echo 'AC14 OK' || { echo 'AC14 FAILED: dod.md carve-out missing'; exit 1; }
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/... && go tool cover -func=/tmp/cover.out | tail -1
```

The AC13/AC14 lines above are self-asserting: they exit non-zero and print `… FAILED` if the CHANGELOG bullet is not under exactly one `## Unreleased` heading, or if the `docs/dod.md` carve-out string is missing. Do not report success while either prints FAILED.

Finally run `make precommit` — must exit 0 (fmt, generate, test, lint, vet, vuln, license).
</verification>

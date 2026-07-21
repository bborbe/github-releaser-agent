---
status: completed
spec: [001-bug-stale-current-version-collision]
summary: 'Wired pkg/githubtags TagsFetcher into planningStep to resolve current_version from remote latest semver tag at plan time (spec 001), with three-way fallback: remote tag wins, ErrNoTags clean fallback, transient error fallback with warning'
execution_id: github-releaser-agent-plantime-version-exec-002-spec-001-wire-plan-resolver
dark-factory-version: dev
created: "2026-07-21T16:41:00Z"
queued: "2026-07-21T16:52:51Z"
started: "2026-07-21T17:17:28Z"
completed: "2026-07-21T17:31:49Z"
branch: dark-factory/bug-stale-current-version-collision
---

<summary>
- Planning now resolves the release's "current version" from the target repo's actual latest remote tag at plan time, instead of trusting the version snapshotted when the task was emitted.
- When the remote has tags, the highest one wins and the next release is computed above it — fixing the headline bug where a repo tagged between emit and run silently dropped its release.
- When the remote has no usable tags (fresh repo), planning quietly falls back to the emitted snapshot with no warning.
- When the tag lookup fails transiently (network / 5xx), planning falls back to the snapshot but surfaces a non-fatal warning on the plan block so an operator can grep it — it never fails-closed on a transport hiccup.
- The escalation path for a genuinely-unresolvable version (empty snapshot AND no remote tags) is unchanged — still escalates for human input.
- The resolved version keeps the remote tag's own prefix style, so downstream version headers are unaffected.
- The new tags fetcher is wired into the agent's dependency graph and passed through both production and local-CLI entry points.
- Ginkgo tests cover all four branches (remote-latest wins, no-tags fallback, transient-error fallback + warning, escalation preserved).
</summary>

<objective>
Wire the `pkg/githubtags` fetcher (built in prompt 1) into `pkg/steps_planning.go` so the planning step resolves the effective `current_version` from the target repo's latest remote tag at plan entry — before `semver.BumpVersion` — falling back to the emit-time frontmatter snapshot only on no-tags (clean) or transient-error (with a surfaced warning). This closes the stale-snapshot collision that dropped vault-cli and jira-task-creator releases as `superseded`.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions.

Read these files BEFORE writing code:
- `/workspace/pkg/steps_planning.go` — the step you are modifying. Study especially: `planningStep` struct (fields `runner`, `fetcher`, `maintainerConfig`, `allowMajor`), `NewPlanningStep(runner, fetcher, maintainerConfig, allowMajor)`, `Run` (where `missingField, currentVersion, repo, cloneURL, ref := s.readRequired(md)` is read, then `parseOwnerRepo`, then `resolveMaintainerConfig`, then `runClassification`), and `resolveMaintainerConfig` — the EXACT fallback pattern you must mirror (`ErrFileNotFound` → clean default + `V(2)` log + empty warning; other transport error → default + `glog.Warningf` + non-empty warning string returned; the warning is threaded through `fetchWarning` into `PlanOutput.ConfigFetchWarning`).
- `/workspace/pkg/plan_output.go` — `PlanOutput.ConfigFetchWarning` (the field you reuse to surface the transient-tag-fetch warning), `PlanOutput.CurrentVersion`, `PreconditionBadCurrentVersion`, `PlanOutcomeReady`/`PlanOutcomeNeedsInput`.
- `/workspace/pkg/githubtags/tags.go` (from prompt 1) — `TagsFetcher` interface, `LatestSemverTag(ctx, owner, repo) (string, error)`, `ErrNoTags` sentinel. If this file does not yet exist, prompt 1 has not shipped; STOP and report `Status: failed` with message `"pkg/githubtags not yet present (prompt 1)"` — do NOT stub it.
- `/workspace/pkg/factory/factory.go` — `CreateAgent` (constructs `githubchangelog.NewHTTPFetcher(ghToken)`, `maintainerconfig.NewHTTPFetcher(ghToken)`, then `releaserpkg.NewPlanningStep(planningRunner, fetcher, maintainerConfigFetcher, allowMajor)`). You add a third fetcher here.
- `/workspace/main.go` (around `factory.CreateAgentProvider`) and `/workspace/cmd/run-task/main.go` (around `factory.CreateAgentProvider`) — the TWO sibling entry points. Both call `CreateAgentProvider` → `CreateAgent`; neither constructs `NewPlanningStep` directly, so the wiring change is confined to `factory.go`. Confirm this by reading both call sites; if either constructs the planning step or fetcher itself, update it too.
- `/workspace/pkg/steps_planning_test.go` — the existing Ginkgo test structure: `withChangelogRewriteTrue()` helper returning `*mocks.MaintainerConfigFetcher`, `fakeFetcher := &mocks.Fetcher{}` (the CHANGELOG mock), `fakeRunner := &mocks.ClaudeRunnerMock{}`, and how `pkg.NewPlanningStep(...)` is called + how `agentlib.ExtractSection[pkg.PlanOutput](ctx, md, "## Plan")` reads back the plan. Around line 1079 there is an existing `ConfigFetchWarning` assertion pattern to mirror for the tags-warning test.
- `/workspace/mocks/tags_fetcher.go` (from prompt 1) — the `TagsFetcher` counterfeiter mock (`FetchReturns` equivalent: `LatestSemverTagReturns(string, error)`).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — factory zero-logic, `Create*` prefix, constructor composition.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega, counterfeiter mocks, coverage ≥80%.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors`, sentinel `errors.Is`.
</context>

<requirements>

## 1. Add the tags fetcher to the planning step struct + constructor

In `/workspace/pkg/steps_planning.go`:

- Add field `tagsFetcher githubtags.TagsFetcher` to `planningStep`.
- Add import `"github.com/bborbe/github-releaser-agent/pkg/githubtags"`.
- Extend `NewPlanningStep` to accept the fetcher as a new parameter and store it:
  ```go
  func NewPlanningStep(
      runner claudelib.ClaudeRunner,
      fetcher githubchangelog.Fetcher,
      maintainerConfig maintainerconfig.Fetcher,
      tagsFetcher githubtags.TagsFetcher,
      allowMajor bool,
  ) agentlib.Step
  ```
  Go has no default parameters — every caller and every test that calls `NewPlanningStep` MUST be updated in the same prompt (factory in requirement 4; tests in requirement 5). Update the constructor doc comment to list the new tag-resolver seam.

## 2. Add the resolver method `resolveCurrentVersion`

Add a method mirroring `resolveMaintainerConfig`'s three-way fallback shape. Insert it near `resolveMaintainerConfig`:

```go
// resolveCurrentVersion returns the effective current_version to bump from,
// resolving it from the target repo's highest remote semver tag at plan
// time (spec 001). The frontmatter snapshot is used ONLY as a fallback.
// Fallback semantics mirror resolveMaintainerConfig:
//
//   - Remote has a semver tag        → (remoteTag, "", nil)   // remote wins; prefix preserved
//   - Remote has no usable tag       → (snapshot, "", nil)    // ErrNoTags → clean fallback, V(2) only, no warning
//   - Transient fetch error (5xx/net)→ (snapshot, "<warn>", nil) // degrade, surface non-fatal warning; do NOT fail-closed
//
// The middle return is a non-fatal warning surfaced on PlanOutput.ConfigFetchWarning
// so an operator can grep a repo whose release bumped from the (possibly stale)
// snapshot on a transient GitHub flake. Empty on the remote-wins and clean-no-tags paths.
func (s *planningStep) resolveCurrentVersion(
    ctx context.Context,
    owner, name, snapshot string,
) (effective string, fetchWarning string, err error) {
    latest, ferr := s.tagsFetcher.LatestSemverTag(ctx, owner, name)
    if ferr != nil {
        if stderrors.Is(ferr, githubtags.ErrNoTags) {
            glog.V(2).Infof(
                "planning: no remote tags for %s/%s — using snapshot current_version=%s",
                owner, name, snapshot,
            )
            return snapshot, "", nil
        }
        glog.Warningf(
            "planning: remote tag fetch failed for %s/%s (using snapshot %s): %v",
            owner, name, snapshot, ferr,
        )
        return snapshot, fmt.Sprintf(
            "remote tag lookup failed (using snapshot current_version=%s): %s",
            snapshot, ferr.Error(),
        ), nil
    }
    glog.V(2).Infof(
        "planning: resolved current_version from remote latest tag %s/%s: %s (snapshot was %s)",
        owner, name, latest, snapshot,
    )
    return latest, "", nil
}
```

`stderrors "errors"` is already imported in this file (used by `resolveMaintainerConfig`). `fmt` and `glog` are already imported.

The method returns `err` in its signature for symmetry with `resolveMaintainerConfig`, but every branch above returns `nil` err — the resolver NEVER fails-closed. Do NOT add an error-returning branch. (Keep the `err` named return to mirror the sibling; it stays nil.)

## 3. Call the resolver in `Run` and thread the warning

In `Run`, AFTER `parseOwnerRepo` succeeds (so `owner`/`name` exist) and BEFORE `runClassification` is invoked, resolve the effective current version and MERGE the two possible warnings (tag-fetch + maintainer-config-fetch) so both are grep-able on the plan block:

- Immediately after the `owner, name, ok := parseOwnerRepo(repo)` success path and the existing `resolveMaintainerConfig` call, add:
  ```go
  effectiveVersion, tagWarning, _ := s.resolveCurrentVersion(ctx, owner, name, currentVersion)
  ```
  Place this so `effectiveVersion` REPLACES `currentVersion` as the value passed into `runClassification` (the value that flows into `resolveBumpVerdict`'s prompt and into `semver.BumpVersion`). The frontmatter `currentVersion` remains the escalation-context value (see requirement 3b).
- Combine warnings: the existing `runClassification` already receives a `fetchWarning` (from `resolveMaintainerConfig`). Merge `tagWarning` into it so BOTH surface on `PlanOutput.ConfigFetchWarning`. Join with `"; "` when both are non-empty; use whichever is non-empty when only one is; empty when neither. Introduce a small local join (inline, or a tiny helper `joinWarnings(a, b string) string` in this file). Pass the merged string as the `fetchWarning` argument to `runClassification`.
- Update the `runClassification` call to pass `effectiveVersion` as the `currentVersion` argument. Do NOT change `runClassification`'s signature — it already takes `currentVersion string`; you are just passing the resolved value.

## 3b. Preserve the escalation contract (do NOT regress)

The escalation path for a genuinely-missing version must be UNCHANGED:

- The `missingField` check at the top of `Run` (which escalates when frontmatter `current_version` is empty) runs BEFORE any remote fetch — leave it exactly as-is. An empty frontmatter `current_version` still escalates immediately with `PreconditionMissingFrontmatter + "current_version"`; the resolver is never reached in that case. This preserves the "empty snapshot AND no remote tags → escalate" contract because empty snapshot escalates first.
- The `semver.BumpVersion` failure branch in `runClassification` (which escalates with `PreconditionBadCurrentVersion`) must remain. If the remote returns a semver tag it will parse fine; if the resolver fell back to a malformed snapshot, `BumpVersion` still fails → still escalates exactly as today. Do NOT weaken this.
- The `escalate(...)` calls carry `currentVersion` in their `escalation{currentVersion: ...}` field. Keep passing the FRONTMATTER `currentVersion` (the snapshot) to `escalate` for escalation-context fidelity — NOT `effectiveVersion`. The `effectiveVersion` is only the bump input.

## 4. Wire the fetcher in the factory (and confirm sibling entry points)

In `/workspace/pkg/factory/factory.go`, `CreateAgent`:
- Add import `"github.com/bborbe/github-releaser-agent/pkg/githubtags"`.
- After `maintainerConfigFetcher := maintainerconfig.NewHTTPFetcher(ghToken)`, add:
  ```go
  tagsFetcher := githubtags.NewHTTPTagsFetcher(ghToken)
  ```
- Pass it into `NewPlanningStep`:
  ```go
  planningStep := releaserpkg.NewPlanningStep(
      planningRunner,
      fetcher,
      maintainerConfigFetcher,
      tagsFetcher,
      allowMajor,
  )
  ```
- The factory must remain pure composition — no conditionals, no I/O, no `context.Background()`.

Confirm both `/workspace/main.go` and `/workspace/cmd/run-task/main.go` reach the planning step ONLY through `factory.CreateAgentProvider` → `factory.CreateAgent` (verified in context). Neither constructs `NewPlanningStep` or a fetcher directly, so no change is needed in either `main.go` — but read both to confirm. If either constructs the planning step itself, update it identically.

## 5. Update existing tests that call `NewPlanningStep`

Every `pkg.NewPlanningStep(...)` call site in `/workspace/pkg/steps_planning_test.go` (and any other `*_test.go` in `pkg/`) gains the new `tagsFetcher` argument. For existing tests whose behavior should be UNAFFECTED by remote resolution, inject a tags fetcher that returns `ErrNoTags` so planning falls back to the existing frontmatter snapshot and every existing assertion (next_version, outcomes, etc.) stays green:

Add a helper near `withChangelogRewriteTrue()`:
```go
// withNoRemoteTags returns a TagsFetcher mock that reports no usable
// remote tag, so the planning step falls back to the frontmatter
// snapshot — preserving pre-spec-001 test expectations.
func withNoRemoteTags() *mocks.TagsFetcher {
    m := &mocks.TagsFetcher{}
    m.LatestSemverTagReturns("", githubtags.ErrNoTags)
    return m
}
```
Import `"github.com/bborbe/github-releaser-agent/pkg/githubtags"` in the test file. Update EVERY existing `NewPlanningStep(...)` call to pass `withNoRemoteTags()` in the new position. Run `make test` and confirm all pre-existing planning tests still pass.

## 6. New Ginkgo tests for the four branches (spec ACs #3–#6)

Add a new `Context("plan-time current_version resolution (spec 001)", func() { ... })` block in `/workspace/pkg/steps_planning_test.go`. Each test uses `fakeFetcher := &mocks.Fetcher{}` returning a valid `## Unreleased` CHANGELOG (copy the fixture shape from existing tests — a body with at least one bullet and a `## v...` prior header), a `fakeRunner := &mocks.ClaudeRunnerMock{}` returning `{"bump":"patch","reasoning":"..."}`, `withChangelogRewriteTrue()` OR a config mock returning `ErrFileNotFound` (use `maintainerconfig.ErrFileNotFound` — read the existing tests for the exact idiom), and a `TagsFetcher` mock configured per-case. Read back the plan with `agentlib.ExtractSection[pkg.PlanOutput](ctx, md, "## Plan")`.

Set the task frontmatter `current_version: v0.101.0` in each case (mirror how existing tests build the `*agentlib.Markdown` with frontmatter — read an existing test that sets `current_version`).

1. **Remote-latest wins (headline bug, AC #3)**: `TagsFetcher.LatestSemverTagReturns("v0.101.1", nil)`, frontmatter snapshot `v0.101.0`, Claude bump `patch` → assert `plan.Outcome == PlanOutcomeReady`, `plan.NextVersion == "0.101.2"` (bumped from the REMOTE `v0.101.1`, NOT `v0.101.1` from the snapshot). Assert `plan.CurrentVersion` reflects the resolved `v0.101.1` (the value passed into `runClassification`). Assert `plan.ConfigFetchWarning` is empty.
2. **No-tags fallback (AC #4)**: `TagsFetcher.LatestSemverTagReturns("", githubtags.ErrNoTags)`, snapshot `v0.101.0`, bump `patch` → `plan.NextVersion == "0.101.1"` (bumped from the SNAPSHOT), `plan.ConfigFetchWarning` is empty (no warning on clean no-tags fallback).
3. **Transient-error fallback + warning (AC #5)**: `TagsFetcher.LatestSemverTagReturns("", stderrors.New("list tags: status 503: ..."))` (a NON-`ErrNoTags` error), snapshot `v0.101.0`, bump `patch` → `plan.NextVersion == "0.101.1"` (bumped from the snapshot, NOT fail-closed), AND `plan.ConfigFetchWarning` is non-empty and contains `"remote tag lookup failed"` and `"503"`. This is the operator-grep evidence.
4. **Escalation preserved — empty snapshot AND no remote tags (AC #6)**: build a task page whose frontmatter `current_version` is EMPTY (or absent). The `missingField` guard fires before the resolver → assert the result `Status == agentlib.AgentStatusNeedsInput`, `plan.Outcome == PlanOutcomeNeedsInput`, `plan.PreconditionFailed == pkg.PreconditionMissingFrontmatter + "current_version"`, and `md.Frontmatter["previous_assignee"] == pkg.AgentLogin`. (The tags fetcher may be `withNoRemoteTags()`; it is never reached because the empty-frontmatter guard escalates first — assert `LatestSemverTagCallCount() == 0` to pin that ordering.)

## 7. Coverage

`pkg` package coverage for the changed planning code must stay ≥80% and cover all four new branches. Verify:
```
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/... && go tool cover -func=/tmp/cover.out | grep -E "resolveCurrentVersion|steps_planning"
```
</requirements>

<constraints>
- Remote resolution is READ-ONLY. No git clone / `git ls-remote` in the planning step (it is pure-API today; keep it that way).
- Fallback semantics mirror `resolveMaintainerConfig` EXACTLY: `ErrNoTags` → clean snapshot fallback, `V(2)` log, NO warning; other transport error → snapshot fallback + non-fatal warning surfaced on `PlanOutput.ConfigFetchWarning` + `glog.Warningf`. NEVER fail-closed on a transport error.
- Prefix preservation: the resolved version keeps the remote tag's own prefix style. When the remote latest is `v0.101.1`, the bump input is `v0.101.1` (with `v`); `semver.BumpVersion` already tolerates the `v` prefix, and `HeaderPrefixStyle`/`NextVersionHeader` are inferred from the CHANGELOG, unaffected. Do NOT normalize the remote tag string.
- Escalation contract preserved: empty frontmatter `current_version` still escalates via the existing `missingField` guard BEFORE any fetch; a malformed resolved version still escalates via the existing `BumpVersion`→`PreconditionBadCurrentVersion` branch. The fix must NOT turn any escalation into a silent default. Pass the FRONTMATTER snapshot (not the resolved value) into `escalate(...)` for escalation context.
- The resolver never fails-closed — `resolveCurrentVersion` returns nil err on every branch. Do NOT add a fail-closed path (that would regress the spec's "do NOT fail-closed on transport errors" constraint).
- Go has no default parameters: updating `NewPlanningStep`'s signature requires updating the factory call site AND every `*_test.go` call site in the SAME prompt. Missing one breaks the build.
- Factory stays pure composition — no conditionals, no I/O.
- Errors use `github.com/bborbe/errors`; sentinel checks use `stderrors "errors"`.`Is`. Logging is `glog` `V(n)`.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass (that is why every existing `NewPlanningStep` call gets `withNoRemoteTags()`).
</constraints>

<verification>
Run from `/workspace`:
```
cd /workspace && make test
```
Then confirm the four branches and wiring:
```
cd /workspace && grep -n "resolveCurrentVersion\|tagsFetcher" pkg/steps_planning.go
cd /workspace && grep -n "NewHTTPTagsFetcher\|tagsFetcher" pkg/factory/factory.go
cd /workspace && grep -n "plan-time current_version resolution" pkg/steps_planning_test.go
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/... && go tool cover -func=/tmp/cover.out | grep resolveCurrentVersion
```
Finally run `make precommit` — must pass (fmt, generate, test, lint, vet, vuln, license).
</verification>

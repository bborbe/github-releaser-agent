---
status: completed
spec: [003-bug-releaser-claims-released-without-remote-tag]
summary: Execution post-check now prefix-compares the observed full remote SHA against the recorded short SHA, so a landed release verdicts released instead of superseded, with two new regression specs.
execution_id: github-releaser-agent-exec-007-spec-003-postcheck-prefix-compare
dark-factory-version: v0.193.0
created: "2026-09-13T19:45:00Z"
queued: "2026-09-13T17:55:23Z"
started: "2026-09-13T17:58:04Z"
completed: "2026-09-13T18:00:59Z"
---

# Prefix-compare the execution post-check's observed remote SHA against the short expected SHA

<summary>
- A landed release is verdicted "released" again instead of "superseded": the remote reports the full 40-character commit id while the agent recorded a 7-character one, and the existing comparison demanded exact equality, so the success verdict was unreachable.
- The comparison becomes a prefix match — a recorded short id that matches the leading characters of the remote's full id counts as the same commit.
- A genuinely different commit still verdicts "superseded", so the existing downgrade branch is untouched.
- When no expected commit is recorded (the failure path), the check keeps its current do-nothing behaviour instead of claiming a release happened.
- Two new regression specs cover the exact defect: a full remote id whose prefix equals the recorded short id (expects "released"), and a full remote id for a different commit (expects "superseded").
- The stale comment that still says the success verdict requires an exact id match is rewritten, so no comment describes the removed comparison.
- Nothing else changes: the empty-remote and remote-error branches still leave the written outcome untouched, and no new remote call, timeout, or outcome value is introduced.
- This is the first of three prompts for spec 003; it lands alone because the second prompt's remote verification compares short against full with the same rule.
</summary>

<objective>
Make the execution step's post-check compare the observed full remote SHA against the recorded short SHA by prefix, so a released-and-landed release is verdicted `released` (and the task's frontmatter upgrades to `completed` / `done`) instead of being mis-verdicted `superseded`. This is the mechanically coupled second defect in the spec: `Result.commit_sha` comes from `git rev-parse --short HEAD` (7 chars) while `LsRemote` returns the full 40-char SHA, so the existing `sha == expectedSHA` test can never hold.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done this prompt is validated against.

Read these files BEFORE changing anything:
- `pkg/steps_execution.go` — read `postCheck` (the verdict branch is the change site), `fail` (its doc comment carries the stale `sha == expectedSHA` claim), `Run` (its sequence comment), and `executeLocalRelease`. Note `lsRemoteTimeout` (30s) and that `postCheck`'s empty-remote and error branches are deliberate no-ops.
- `pkg/steps_execution_test.go` — read `Context("post-check (spec 064)")` (its `sharedHappySetup` helper wires `CommitReturns("abc1234", nil)`, `CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)`, a `CloneStub` that writes the changelog, and returns the parsed `taskMD` fixture with plan version v1.2.8). Your new specs live in that Context and use `sharedHappySetup`. Also note the existing specs must keep passing: the released case at `fakeOps.LsRemoteReturns("abc1234", nil)`, the superseded case at `"deadbee"`, the empty case at `""`, the error case, the failure-path case, and the two idempotency cases.
- `pkg/resolution_output.go` — `ResolutionOutput` and the `ResolutionVerdictReleased` / `ResolutionVerdictSuperseded` constants your assertions use.
- `pkg/git/os_exec_git_ops.go` — `LsRemote` returns the dereferenced full commit SHA; its `ref` parameter is traceability only and is never passed to git.
- `pkg/steps_planning.go` — `sameCommit` is the codebase's existing short-vs-full prefix idiom (case-insensitive, hex-guarded). Read it for style. Do NOT reuse it here — this change requires the literal `strings.HasPrefix(observed, expected)` in `pkg/steps_execution.go`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega, external `_test` package, Counterfeiter fakes.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `glog` with `V(n)`-gated lines.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`.
</context>

<requirements>

## 1. Prefix comparison in `postCheck` — `pkg/steps_execution.go`

In `postCheck`, the verdict branch currently reads:

```go
	// 3. Compare observed vs expected.
	verdict := ResolutionVerdictSuperseded
	if expectedSHA != "" && sha == expectedSHA {
		verdict = ResolutionVerdictReleased
	}
```

Change it to a prefix comparison:

```go
	// 3. Compare observed vs expected by prefix. `expectedSHA` is the short
	// SHA from `git rev-parse --short HEAD`; the remote reports the full
	// 40-char SHA. Exact equality can never hold, so the released verdict
	// would be unreachable. An empty expected SHA keeps the superseded-only
	// behaviour (the failure path passes "").
	verdict := ResolutionVerdictSuperseded
	if expectedSHA != "" && strings.HasPrefix(sha, expectedSHA) {
		verdict = ResolutionVerdictReleased
	}
```

Two things are load-bearing and must not change:
- `strings` is already imported in this file (used by `strings.TrimPrefix`) — no import change needed.
- The `expectedSHA != ""` guard MUST stay. `strings.HasPrefix(sha, "")` is always true, so dropping the guard would flip the failure path's post-check (which deliberately passes `""` so it stays superseded-only) into claiming `released`.

## 2. Rewrite the comments that describe the removed comparison

The `fail` doc comment currently claims:

```
// check on the failure path is superseded-only: the local commit/tag
// step never produced a SHA that could match the remote tag, so the
// "released" verdict (which requires sha == expectedSHA) is by
// construction unreachable here.
```

That string (`sha == expectedSHA`) must not survive this change — no comment may describe the comparison that was removed. Rewrite the parenthetical so it states the new rule, e.g. `(which requires the observed full SHA to start with the expected short SHA)`, keeping the paragraph's meaning: on the failure path the expected SHA is passed as `""`, so the released verdict stays unreachable there.

Also extend the `postCheck` doc comment's step 3 so it says the comparison is a prefix comparison (`observed` starts with `expected`) and that an empty expected SHA keeps the superseded-only behaviour. Leave `postCheck`'s other documented invariants (idempotency guard, missing-context guard, `no-op-remote-empty`, `no-op-remote-error`, the `V(2)` log line format, "the released → failed downgrade is impossible") intact and accurate.

After this step: `grep -n 'sha == expectedSHA' pkg/steps_execution.go` must return zero matches, and `grep -n 'HasPrefix' pkg/steps_execution.go` must return at least one.

## 3. Two new specs in `pkg/steps_execution_test.go`

Both live inside the existing `Context("post-check (spec 064)")` block, use its `sharedHappySetup()` helper (which already wires `CommitReturns("abc1234", nil)`), and follow the shape of the existing post-check specs (build the step with `pkg.NewExecutionStep(fakeOps, "test-token")`, run it, assert `result.Status`, frontmatter, and the extracted `## Resolution` block). Each spec's name carries the fixture name so the intent is greppable.

### 3a. `ShortExpectedMatchesFullObserved` — the defect regression

Fixture: `fakeOps.LsRemoteReturns("abc1234e3cca37862f4e612a7b14c4e00af6b935", nil)` (a full 40-char SHA whose first 7 characters are exactly the short SHA `Commit` returned). Use that SHA literal verbatim — it is 40 characters.

Assertions:
- `result.Status` equals `agentlib.AgentStatusDone`
- `fakeOps.LsRemoteCallCount()` equals 1
- `md.Frontmatter["status"]` equals `"completed"` and `md.Frontmatter["phase"]` equals `"done"`
- the extracted `pkg.ResolutionOutput` from `## Resolution` has `Verdict` == `pkg.ResolutionVerdictReleased`, `PlannedVersion` == `"v1.2.8"`, and `ObservedRemoteSHA` == `"abc1234e3cca37862f4e612a7b14c4e00af6b935"`
- the extracted `pkg.ResultOutput` from `## Result` still has `Outcome` == `"released"` (the post-check appends `## Resolution`; it never replaces `## Result`)

Before the fix this spec fails with verdict `superseded` — that is the discriminating property. The bare symbol `ResolutionVerdictReleased` already appears in this file for the short-vs-short case, so the fixture name is the grep anchor, not the constant.

### 3b. `DifferentCommitSuperseded` — the different-commit control

Fixture: `fakeOps.LsRemoteReturns("deadbeef1234567890abcdef1234567890abcdef", nil)` (a full 40-char SHA that does NOT start with `abc1234`). Use that SHA literal verbatim.

Assertions:
- `result.Status` equals `agentlib.AgentStatusDone`
- `md.Frontmatter["status"]` equals `"completed"` and `md.Frontmatter["phase"]` equals `"done"`
- the extracted `## Resolution` has `Verdict` == `pkg.ResolutionVerdictSuperseded`, `PlannedVersion` == `"v1.2.8"`, `ObservedRemoteSHA` == `"deadbeef1234567890abcdef1234567890abcdef"`

## 4. Leave the other existing specs alone

Do not modify the existing post-check specs (the short-vs-short released case, the `"deadbee"` superseded case, the empty-remote no-op, the `LsRemote` error no-op, the failure-path case at the end of the Context, or the two idempotency specs). They must all keep passing unchanged — the new comparison is a strict relaxation of the old one for non-empty expected SHAs, so every existing expectation still holds.

## 5. Self-check before finishing

Re-run `<verification>` and confirm it passes. Walk each numbered requirement above and confirm: the comparison is a prefix comparison, the non-empty guard is intact, the `sha == expectedSHA` string is gone from `pkg/steps_execution.go`, both new specs exist under their fixture names, and no other spec in `pkg/steps_execution_test.go` was altered.
</requirements>

<constraints>
- **Prefix comparison is mandatory.** `Result.commit_sha` is a short SHA; the remote returns a full 40-char SHA. Compare with `strings.HasPrefix(observedSHA, expectedSHA)`. Never restore exact equality.
- **The execution-step post-check keeps its empty-remote no-op** and keeps the `expectedSHA != ""` guard, so the failure path stays superseded-only. Do not downgrade or upgrade anything on the empty-remote or `LsRemote`-error branches.
- **Rejected alternative — no new outcome value.** Do not add `pending` / `unverified` (or anything else) to the `## Result` outcome set. The fix uses existing values and fields only.
- **No new error-category enum values.** Reuse `git.ClassifyError` and its existing categories if an error category is touched at all.
- **No new retry loop and no new timeout.** Do not touch `lsRemoteTimeout` (30s).
- **Remote state is read-only here.** No tag deletion, no force-push.
- **Git-write safety invariant untouched.** CHANGELOG-only commit via explicit pathspec; deterministic Go writes; the LLM only classifies.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)` — no `fmt.Print*`.
- **Existing tests keep passing** (Ginkgo v2 / Gomega, Counterfeiter fakes, no live Claude or network calls in tests).
- **Do NOT modify `pkg/git`.** `LsRemote` already prefers the dereferenced `^{}` line and falls back to the plain line for lightweight tags — reuse it, do not re-implement it.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make test` — exit 0, with the two new specs present and the existing post-check specs unchanged.
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).

```bash
grep -n 'HasPrefix' pkg/steps_execution.go        # at least one match (zero before this change)
grep -n 'sha == expectedSHA' pkg/steps_execution.go   # zero matches
grep -c 'ShortExpectedMatchesFullObserved' pkg/steps_execution_test.go   # >= 1
grep -c 'DifferentCommitSuperseded' pkg/steps_execution_test.go          # >= 1
grep -c 'abc1234e3cca37862f4e612a7b14c4e00af6b935' pkg/steps_execution_test.go  # >= 1
grep -n 'ResolutionVerdictReleased' pkg/steps_execution_test.go          # the happy-path verdict control
```
</verification>

---
status: approved
spec: [005-create-github-release-object]
created: "2026-11-09T17:38:03Z"
queued: "2026-11-09T17:55:37Z"
branch: dark-factory/create-github-release-object
---

# Create the GitHub Release object on a confirmed release

<summary>
- A confirmed release now creates its GitHub Release object in the same run, using the pushed tag as the only input.
- The release title is the tag and the body is the CHANGELOG section already written for that tag.
- The Review block gains a `release_exists` check.
- An existing release is success: re-running a released task does not fail.
- An unreachable GitHub fails closed and parks the task for a human, never a silent pass.
- A rejected call is worded differently from an unreachable one so the operator knows which happened.
- The release is created before the image check, so a missing release is reported even when the image result would mask it.
- A CHANGELOG section that cannot be read does not block the release: it is created with an empty body plus a note.
- Nothing changes on the failed-outcome short-circuit path; its zero value is already the correct report.
</summary>

<objective>
In the AI-review step, after the pushed tag is confirmed on the remote and before the image check, create the GitHub Release object for that tag through the `githubreview.Client.CreateRelease` seam added by prompt 1. Record `release_exists` in `ReviewChecks`, and fail closed to `human_review` when GitHub cannot be reached or rejects the call. An already-existing release is success. The tag is the only input; the body comes from the workdir's CHANGELOG section for that tag.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done.

Read these files BEFORE changing anything:
- `pkg/githubreview/client.go` — the `CreateRelease(ctx, owner, repo, tag, title, body) (bool, error)` contract and the `ErrReleaseUnreachable` / `ErrReleaseRejected` sentinels added by prompt 1.
- `pkg/steps_ai_review.go` — `ReviewChecks` and `ReviewOutput` (the JSON contract), the stable check-name const block (`CheckFaithfulness`, `CheckUnexpectedFileChange`, `CheckPush`, `CheckImage`), `finishApproved` (where `Push` runs, `reconcilePostPush` confirms the tag, and `applyImageCheck` is called on the `confirmed` branch), `applyImageCheck` (the exact shape of a post-push check that can return a terminal result), `appendReviewNote`, `checkFaithfulness` (the existing `changelog.ExtractSectionBody` read of the workdir and the `strings.TrimPrefix(plan.NextVersionHeader, "## ")` derivation), and `writeShortCircuit`.
- `pkg/changelog/changelog.go` — `ExtractSectionBody(ctx, content []byte, heading string) (string, error)`: `heading` is the heading TEXT without `## ` (pass `result.LocalTag` directly). It returns the verbatim section body, including a trailing blank line, and an error whose message is `<heading> header not found` when absent.
- `pkg/git/os_exec_git_ops.go` — `git.RedactToken(string) string`.
- `pkg/steps_ai_review_test.go` — the `Describe("Run")` fixtures: `taskWithResultFull(commitSHA, tag, outcome, workdir)` (carries `clone_url` and `ref`), `runStep`, `extractReview`, `setupReleasedWorkdir` (writes `## v1.0.0` with body `- feat: add foo`), and the existing `Context("post-push image-existence check")` whose `runConfirmedRelease` helper shows the confirmed-tag setup (`fakeOps.LsRemoteReturns("abc1234e3cca37862f4e612a7b14c4e00af6b935", nil)` plus `taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir)`).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`

<!-- OPEN QUESTION FOR THE AUDITOR: the spec's `## Open Questions` doctrine/policy question
     (whether a GitHub Release is created per tag) was resolved by the operator approving this
     spec (approved 2026-11-09T16:44:40Z). AC (j) — rewriting docs/releasing-github-releaser-agent.md
     § "GitHub Release (optional milestone)" — is operator-owned and is implemented by no prompt.
     AC (i) (post-deploy proof on bborbe/go-skeleton) is likewise an operator rung. Do not add
     either to this prompt. Also note the spec's Assumption: the existing GitHub App token carries
     `contents: write`; if it does not, the call 403s and the check fails closed, which is the
     intended visible outcome. -->
</context>

<requirements>

## 1. Add the check field and the stable check name

In `pkg/steps_ai_review.go`:

1. Add to `ReviewChecks`, after `ImageExists`:

   ```go
   // ReleaseExists is true when the GitHub Release object for the released
   // tag is present after the pushed tag is confirmed. It is true both when
   // this run created the release and when GitHub reported that it already
   // existed. False when the call failed or when the short-circuit path
   // (a failed outcome) never created one.
   ReleaseExists bool `json:"release_exists"`
   ```

   Write the field as `ReleaseExists bool` on one line with the struct tag, so the tag is exactly `json:"release_exists"`.

2. Add `CheckRelease = "Release"` to the stable check-name const block beside `CheckImage`.

3. Update the doc comments that enumerate the check set:
   - `ReviewChecks`'s comment says "the three original structural checks, the ai-review-side gates, and the image-existence fields" — mention the release-existence field.
   - `ReviewOutput.FailedChecks`'s comment lists "One or more of: CheckFaithfulness, CheckUnexpectedFileChange, CheckPush, CheckImage." — add `CheckRelease`.

## 2. Implement `applyReleaseCreation`

Add a method mirroring `applyImageCheck`'s shape (it may return a terminal result on the fail-closed path):

```go
func (s *aiReviewStep) applyReleaseCreation(
	ctx context.Context,
	md *agentlib.Markdown,
	result *ResultOutput,
	owner, name string,
	output *ReviewOutput,
	workdirShouldCleanup *bool,
) (*agentlib.Result, error)
```

Behavior, in order:

1. Set `output.Checks.ReleaseExists = false`.
2. Read the body from the workdir:
   - If `result.Workdir != ""`, `os.ReadFile(filepath.Join(result.Workdir, changelogFileName))`, then `changelog.ExtractSectionBody(ctx, content, result.LocalTag)`.
   - On a read error or an extraction error, leave `body` as `""`, append the note `"release body could not be read: " + git.RedactToken(err.Error())` via `appendReviewNote`, and continue. A missing body is not a reason to skip the release (Desired Behavior 3).
   - If `result.Workdir == ""`, leave `body` as `""` and append the same note with a short reason (this cannot happen on the approved path, but must not panic).
3. Call:

   ```go
   created, err := s.client.CreateRelease(ctx, owner, name, result.LocalTag, result.LocalTag, body)
   ```

   The tag is passed as both the tag and the title. Do not re-derive the version, do not read `## Plan`, and do not re-read `## Unreleased`.
4. On `err != nil`, fail closed:
   - `output.Checks.ReleaseExists = false`
   - `output.Overall = OverallFail`
   - `output.Approved = false`
   - Append `CheckRelease` to `output.FailedChecks` only if `!slices.Contains(output.FailedChecks, CheckRelease)`.
   - Compose the note by failure class:
     - `stderrors.Is(err, githubreview.ErrReleaseUnreachable)` → `"release creation could not reach GitHub: " + git.RedactToken(err.Error())`
     - otherwise → `"GitHub rejected the release: " + git.RedactToken(err.Error())`
   - Append the note via `appendReviewNote`.
   - Marshal `## Review` with `agentlib.MarshalSectionTyped(ctx, "## Review", *output)`, `md.ReplaceSection(section)`, and set `*workdirShouldCleanup = true`.
   - Return `&agentlib.Result{Status: agentlib.AgentStatusNeedsInput, NextPhase: string(domain.TaskPhaseHumanReview), Message: output.Notes}, nil`.
   - Do **not** use `AgentStatusFailed`: a missing release does not fix itself on retry, and `Failed` re-runs the push and the faithfulness LLM call. `AgentStatusNeedsInput` + `human_review` is the same routing `applyImageCheck` uses.
5. On `err == nil` (whether `created` is true or false): set `output.Checks.ReleaseExists = true`, emit one `glog.V(2)` line naming `task_id`, the tag and `created=%t`, and return `(nil, nil)`. Do not treat `created == false` as a failure — it means GitHub reported `already_exists`.
6. Exactly one `CreateRelease` call per run. No retry, no follow-up GET.

## 3. Wire it into `finishApproved`

Inside the existing `if confirmed { ... }` block, call `applyReleaseCreation` **before** `applyImageCheck`:

```go
if confirmed {
	terminal, checkErr := s.applyReleaseCreation(
		ctx, md, result, owner, name, &output, workdirShouldCleanup,
	)
	if checkErr != nil {
		return nil, checkErr
	}
	if terminal != nil {
		return terminal, nil
	}
	terminal, checkErr = s.applyImageCheck(
		ctx, md, result, owner, name, &output, workdirShouldCleanup,
	)
	if checkErr != nil {
		return nil, checkErr
	}
	if terminal != nil {
		return terminal, nil
	}
}
```

Do not call it on any other branch: a failed push returns `finishHumanReview` before this block, and a non-confirmed consult never enters it. Do not call it from `Run`.

## 4. Leave the short-circuit path alone

`writeShortCircuit` builds `ReviewChecks` field-by-field; `ReleaseExists` is `false` there by the zero value, which is the correct report for a `failed` outcome. Do not add a release call or an explicit field assignment there. (Spec constraint: "The short-circuit path needs no change.")

## 5. Step-level tests in `pkg/steps_ai_review_test.go`

Add `Context("post-push release creation (spec 005)", func() { ... })` inside `Describe("Run")`, after the `Context("post-push image-existence check")` block. It inherits the `Describe("Run")` `BeforeEach` (tmpdir with `## v1.0.0` body `- feat: add foo`, faithful LLM stub). Define a local helper:

```go
runConfirmedRelease := func(tag string) (*agentlib.Result, *agentlib.Markdown) {
	DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
	fakeOps.LsRemoteReturns("abc1234e3cca37862f4e612a7b14c4e00af6b935", nil)
	return runStep(taskWithResultFull("abc1234", tag, "released", tmpDir))
}
```

Required specs, with the name verbatim in the `It(...)` description:

- **`CreateReleaseOnConfirmedTag`** (AC (a)) — `fakeClient.CreateReleaseReturns(true, nil)`. **Compute the expected body BEFORE running the step**: the step removes the workdir on its terminal transition, so the fixture file is gone once `runStep` returns.
    ```go
    content, err := os.ReadFile(filepath.Join(tmpDir, "CHANGELOG.md"))
    Expect(err).NotTo(HaveOccurred())
    expectedBody, err := changelog.ExtractSectionBody(context.Background(), content, "v1.0.0")
    Expect(err).NotTo(HaveOccurred())
    ```
  - Then run `result, md := runConfirmedRelease("v1.0.0")`.
  - Assert `result.NextPhase == "done"` and `result.Status == agentlib.AgentStatusDone`.
  - Assert `fakeClient.CreateReleaseCallCount() == 1`.
  - Destructure `_, _, _, tagArg, titleArg, bodyArg := fakeClient.CreateReleaseArgsForCall(0)` and assert `tagArg == "v1.0.0"`, `titleArg == "v1.0.0"`, and `bodyArg == expectedBody` (equality, not containment). The equality is what stops a hardcoded constant or an empty body from passing.
  - Assert `extractReview(md).Checks.ReleaseExists` is true (this also round-trips the `release_exists` JSON tag through `agentlib.ExtractSection[ReviewOutput]`).
  - Import `github.com/bborbe/github-releaser-agent/pkg/changelog` in the test file.
- **`CreateReleaseAlreadyExistsIsSuccess`** (AC (b)) — `fakeClient.CreateReleaseReturns(false, nil)`. Run `runConfirmedRelease("v1.0.0")`. Assert `extractReview(md).Checks.ReleaseExists` is true, `result.NextPhase == "done"`, and `review.FailedChecks` does not contain `pkg.CheckRelease`.
- **`CreateReleaseUnreachableFailsClosed`** (AC (c)) — `fakeClient.CreateReleaseReturns(false, fmt.Errorf("%w: dial tcp: connection refused", githubreview.ErrReleaseUnreachable))`. Run `runConfirmedRelease("v1.0.0")`. Assert:
  - `result.Status == agentlib.AgentStatusNeedsInput`, `result.NextPhase == "human_review"`
  - `review.Checks.ReleaseExists` is false
  - `review.FailedChecks` contains `pkg.CheckRelease`
  - `review.Notes` contains the literal `could not reach GitHub`
  - `review.Overall == pkg.OverallFail` and `review.Approved` is false
- **`CreateReleaseRejectedIsDistinctFromUnreachable`** (AC (d)) — `fakeClient.CreateReleaseReturns(false, fmt.Errorf("%w: status 403: forbidden", githubreview.ErrReleaseRejected))`. Run `runConfirmedRelease("v1.0.0")`. Assert `review.Notes` contains `rejected the release` and does **not** contain `could not reach GitHub`, `review.Checks.ReleaseExists` is false, and `result.NextPhase == "human_review"`.
- **`CreateReleaseBodyUnreadableStillCreates`** (Failure Modes: CHANGELOG section body unreadable) — build a fixture whose `## Result.local_tag` is `v9.9.9` while the plan header and the on-disk section are `v1.0.0`, so the faithfulness check still passes on `v1.0.0` but the release-body extraction for `v9.9.9` fails. The simplest construction is `strings.Replace(taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir), `"local_tag":"v1.0.0"`, `"local_tag":"v9.9.9"`, 1)`. Stub `fakeOps.LsRemoteReturns("abc1234e3cca37862f4e612a7b14c4e00af6b935", nil)`, call `runStep`, and assert `fakeClient.CreateReleaseCallCount() == 1`, the body argument is `""`, `review.Notes` contains `release body could not be read`, and `review.Checks.ReleaseExists` is true. (`strings`, `fmt` and `githubreview` are already imported in this test file; `pkg/changelog` is added above.)
- **`CreateReleaseNotCalledWhenTagUnconfirmed`** (Failure Modes: tag confirmation failed earlier) — `fakeOps.LsRemoteReturns("", nil)` with `taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir)`. Assert `fakeClient.CreateReleaseCallCount() == 0`, `result.Status == agentlib.AgentStatusFailed`, `result.NextPhase == "human_review"`.
- In the existing `Context("post-push image-existence check")`, spec `"fails the check when the image is genuinely absent"`, add `Expect(review.Checks.ReleaseExists).To(BeTrue())` beside the existing assertions. This pins the ordering required by Desired Behavior 1: the release is created (and reported present) before the image check can park the task. Do not change any other assertion in that spec.

## 6. Revert probe (required by AC (a)(iii))

Temporarily delete the `applyReleaseCreation` call from `finishApproved`, run only the named spec, and confirm it fails because `ReleaseExists` is false:

```bash
go test ./pkg/ -run TestSuite -args -ginkgo.focus='CreateReleaseOnConfirmedTag' 2>&1 | tee /tmp/spec005-probe.log
grep -q 'Ran 1 of' /tmp/spec005-probe.log
```

A focus that matches zero specs still exits 0, so the `Ran 1 of` check is part of the probe. Restore the call afterwards and re-run `make test`. Record the captured failing output in your final summary; the tree you leave behind is the fixed one.

</requirements>

<constraints>
- **The tag is the only input.** `tag_name` and `name` are `result.LocalTag`; the body is `changelog.ExtractSectionBody(ctx, content, result.LocalTag)`. No version re-derivation, no plan re-read, no `## Unreleased` re-read.
- **Creation is idempotent.** `(false, nil)` from `CreateRelease` means `already_exists` → `release_exists: true`, no failure.
- **The check fails closed.** Any error → `release_exists: false`, `Release` in `failed_checks`, `overall: "fail"`, `AgentStatusNeedsInput` / `NextPhase: "human_review"`. Never `AgentStatusFailed` on this path.
- **The notes distinguish the two failure classes.** Transport error → `release creation could not reach GitHub: …`; HTTP rejection → `GitHub rejected the release: …`. Both fail; the operator must be able to tell which.
- **One call per run.** No retry, no second GET to confirm what the POST reported.
- **A body that cannot be read is not a reason to skip the release.** Create it with an empty body and record a note.
- **Do NOT create a release on the failed-outcome short-circuit path.** `writeShortCircuit` is unchanged.
- **Do NOT add a repo-level opt-out, a retry loop, a second GitHub call, or a new timeout.**
- **Do NOT change the `image_exists` / `image_expected` semantics or their opt-in gating.** The image check is untouched except for the one added assertion in its existing spec.
- **Do NOT change the release body format beyond the CHANGELOG section already written for the tag.**
- **Do NOT add a new result outcome, result field, or error-category value.** The result contract has two shapes; this change touches none of them.
- **The step writes no frontmatter.** Status and phase remain the deliverer's decision.
- **Tokens are redacted.** Every error string that reaches `## Review` notes passes through `git.RedactToken`.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` with `V(n)`-gated `Info`.
- **Existing test conventions hold.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network, no live Claude calls.
- **`CHANGELOG.md` is prompt 3's job.** Do not add a `## Unreleased` entry here; report the changelog criterion as deferred to prompt 3 if your self-review checks it.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).
Run `make test` — exit 0.

```bash
grep -cE 'ReleaseExists[[:space:]]+bool[[:space:]]+`json:"release_exists"' pkg/steps_ai_review.go   # 1
grep -cE 'CheckRelease[[:space:]]*=[[:space:]]*"Release"' pkg/steps_ai_review.go                     # 1
grep -c 'CreateReleaseOnConfirmedTag' pkg/steps_ai_review_test.go                                    # >= 1
grep -c 'CreateReleaseArgsForCall' pkg/steps_ai_review_test.go                                       # >= 1
grep -c 'CreateReleaseAlreadyExistsIsSuccess' pkg/steps_ai_review_test.go                            # >= 1
grep -c 'CreateReleaseUnreachableFailsClosed' pkg/steps_ai_review_test.go                            # >= 1
grep -c 'CreateReleaseRejectedIsDistinctFromUnreachable' pkg/steps_ai_review_test.go                 # >= 1
grep -c 'applyReleaseCreation' pkg/steps_ai_review.go                                                # >= 2 (definition + call site)
! sed -n '/func (s \*aiReviewStep) applyReleaseCreation/,/^}/p' pkg/steps_ai_review.go | grep -q 'AgentStatusFailed'   # the fail-closed path uses NeedsInput, never Failed
```
</verification>

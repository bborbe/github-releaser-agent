---
status: draft
kind: bug
---

## Summary

- The release agent rewrites `## Unreleased` → `## vX.Y.Z`, commits, tags, and pushes. It then verifies the tag on the remote and probes for an image. **It never creates a GitHub Release object**, and no check in the review block asks whether one exists.
- The task therefore closes `status: completed` / `"overall": "pass"` on a repository whose Releases page is empty. Measured 2026-11-09: of 99 repos carrying a completed `github-release` task since 2026-11-01, **95 have no release published at or after that job's `job_started_at`**.
- The image half of the original defect has already been fixed elsewhere — `bborbe/github-release-watcher` dispatches an in-cluster build Job per release tag for opted-in repos, and `bborbe/github-releaser-agent` v0.8.0 added the `image_exists` check. Neither touches the Release object.
- Fix: after the pushed tag is confirmed on the remote, the review step creates the GitHub Release for that tag, records `release_exists` in the check set, and **fails closed** — an unreachable GitHub is a failure, never a silent pass.

## Problem

A release is three independent facts: the tag, the image, and the Release object. The agent now checks the first two (and creates neither — the watcher creates the image). The third is never created and never checked, so the review block's "all checks passed" is a claim about a set that does not contain the thing consumers look at.

The observable damage is a repository that carries `v0.8.3` in its tag list and an image in the registry, while `gh api repos/bborbe/go-skeleton/releases` returns `0`. Anyone reading the release history — the operator, the release sweep, a promotion metric — sees nothing.

## Reproduction

Read-only host probes, gathered 2026-11-09.

```bash
# The agent has no release-creation code at all.
git grep -nEi "releases|release create|CreateRelease" origin/master -- '*.go' '*.yml' 'Makefile'
#   → .github/workflows/check-unfinished-pipeline.yml:45 (a download URL)
#   → pkg/steps_execution.go:129-178 (two local variables named releaseSuccess)
#   No POST /repos/{owner}/{repo}/releases anywhere.

# The blast radius, per repo with a completed release task since 2026-11-01.
python3 -I -u ~/.claude/scripts/regression-metrics/rel.py
#   → completed-since-2026-11-01 repos 99 no-release-after-job 95

# A concrete repo: tag and image present, zero releases.
gh api repos/bborbe/go-skeleton/releases --jq 'length'          # 0
git -C ~/Documents/workspaces/go-skeleton tag --sort=-v:refname | head -1   # v0.8.3
kubectlnukeprod -n build logs job/build-go-skeleton-v0-8-3-20261108-175350-jxig --tail=5
#   → pushing manifest for docker.prod.nuke.benjamin-borbe.de:443/bborbe/go-skeleton:v0.8.3@sha256:1ee9f467…
```

The only four repos that read `ok` are `bborbe/driftline`, `bborbe/echo-sky-run`, `bborbe/git-ai-sync` and `bborbe/vault-ui` — their release objects were created by something other than this agent.

## Expected vs Actual

| | |
|---|---|
| **Expected** | A confirmed release produces a GitHub Release object for its tag, and the review block records whether one exists. A GitHub that cannot be reached produces a **failed** check, not a pass. |
| **Actual** | `pkg/steps_ai_review.go` creates nothing. `ReviewChecks` (`:74-101`) carries seven booleans — `tag_exists`, `tag_at_expected_sha`, `changelog_header_rewritten`, `faithfulness_ok`, `unexpected_file_change`, `image_expected`, `image_exists` — and none of them is about a Release object. `finishApproved` (`:398-467`) pushes, reconciles the tag, runs `applyImageCheck`, writes `## Review`, returns `done`. |

## Why this is a bug

`overall: "pass"` is a statement about the check set, and the check set is the agent's own definition of a successful release. That definition omits the artifact every human and every consumer reads first. A check set that cannot fail for the observed reason is not a check — which is exactly the finding that opened this work, one artifact later.

## Goal

A confirmed release produces a GitHub Release object for its tag in the same run, with the tag as the only input and no manual step. The review block records `release_exists` and `release_expected`, and a GitHub that cannot be reached or that rejects the call fails the `Release` check and parks the task at `human_review` rather than closing it. Re-running an already-released task does not fail: an existing release is success.

## Non-goals

- Do NOT build the image publication path. It exists in `bborbe/github-release-watcher` `pkg/build/` and is out of scope here.
- Do NOT change the `image_exists` / `image_expected` semantics, including their opt-in gating. That defect is filed separately.
- Do NOT repair the 95 already-published-without-a-release records. They are operator work; this spec stops new ones.
- Do NOT create releases on the failed-outcome short-circuit path (`writeShortCircuit`). A `failed` outcome has no confirmed release; the release object follows the tag, and that path already parks unless the remote carries the tag.
- Do NOT add a retry loop, a second GitHub call, or a new timeout beyond the client's existing one.
- Do NOT change the release body format beyond the CHANGELOG section already written for the tag.

## Acceptance Criteria

- [ ] **(a) A confirmed release creates the GitHub Release object.** A Ginkgo spec named `CreateReleaseOnConfirmedTag` runs the review step on a `released` fixture whose remote confirms the pushed tag, with a fake review client, and asserts `review.Checks.ReleaseExists == true` and `result.NextPhase == "done"`. Evidence: file content plus a recorded red run. (i) `grep -c 'CreateReleaseOnConfirmedTag' pkg/steps_ai_review_test.go` returns ≥1 and is 0 today. (ii) The named block carries the assertion: `grep -A 15 'CreateReleaseOnConfirmedTag' pkg/steps_ai_review_test.go | grep -c 'ReleaseExists'` returns ≥1. (iii) **Revert probe**: deleting the `applyReleaseCreation` call makes exactly that spec fail; record the failing output in the prompt's final summary.
- [ ] **(b) An already-existing release is success, not a failure.** A spec named `CreateReleaseAlreadyExistsIsSuccess` drives the fake client to return `(false, nil)` (the idempotent 422 path) and asserts `ReleaseExists == true`, `NextPhase == "done"`, and that `Release` is absent from `FailedChecks`. Evidence: file content plus a recorded red run; the name is absent today.
- [ ] **(c) An unreachable GitHub fails closed.** A spec named `CreateReleaseUnreachableFailsClosed` drives the fake client to return a transport error and asserts `ReleaseExists == false`, `CheckRelease` ∈ `FailedChecks`, `NextPhase == "human_review"`, and that `review.Notes` contains the literal `could not reach GitHub`. Evidence: file content plus a recorded red run; the name is absent today. This is the AC that stops "treat any non-success as fine" from passing.
- [ ] **(d) A rejected call is distinguished from an unreachable one.** A spec named `CreateReleaseRejectedIsDistinctFromUnreachable` drives the fake client to return an HTTP-status error and asserts the notes contain `rejected the release` and **not** `could not reach GitHub`, with `ReleaseExists == false` and `NextPhase == "human_review"`. Evidence: file content plus a recorded red run; the name is absent today.
- [ ] **(e) The client is idempotent at the HTTP layer.** A client-level spec named `CreateReleaseTreatsAlreadyExistsAsSuccess` serves a 422 whose body names `already_exists` and asserts `(false, nil)`; a second spec named `CreateReleaseReturnsWrappedErrorOnOtherNon2xx` serves a 500 and asserts a non-nil error. Evidence: file content; both names absent today; `make test` exits 0.
- [ ] **(f) The check set gains the field and the failure name.** `ReviewChecks` carries `ReleaseExists bool` with json tag `release_exists`, and `CheckRelease = "Release"` is declared beside the other stable check names. Evidence: `grep -c 'ReleaseExists bool .json:"release_exists"' pkg/steps_ai_review.go` returns 1; `grep -c 'CheckRelease  *= "Release"' pkg/steps_ai_review.go` returns 1.
- [ ] **(g) `CHANGELOG.md` gains a `## Unreleased` entry** naming the fix, created above the newest version header per `docs/dod.md`. Evidence: `grep -n '^## Unreleased' CHANGELOG.md` returns exactly one line and `grep -n '^## ' CHANGELOG.md | head -1` prints that same line number.
- [ ] **(h) `make precommit` exits 0.** Evidence: exit code.

Scenario coverage: none added. The behaviour is reached at the step boundary with the GitHub client faked, in-process, no network. The runtime proof is the operator observation named in Verification.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```bash
make precommit                                                                    # exit 0
make test                                                                         # exit 0
grep -c 'CreateReleaseOnConfirmedTag' pkg/steps_ai_review_test.go                 # ≥1
grep -c 'CreateReleaseAlreadyExistsIsSuccess' pkg/steps_ai_review_test.go         # ≥1
grep -c 'CreateReleaseUnreachableFailsClosed' pkg/steps_ai_review_test.go         # ≥1
grep -c 'CreateReleaseRejectedIsDistinctFromUnreachable' pkg/steps_ai_review_test.go  # ≥1
grep -c 'CreateReleaseTreatsAlreadyExistsAsSuccess' pkg/githubreview/client_test.go   # ≥1
grep -c 'ReleaseExists bool `json:"release_exists"`' pkg/steps_ai_review.go       # 1
grep -n '^## Unreleased' CHANGELOG.md                                             # exactly one line
grep -n '^## ' CHANGELOG.md | head -1                                             # the same line
```

### Operator-executable

Deployment and the live observation are the parent task's Definition of Done, not this spec's. After the fixed image runs: re-trigger a release on `bborbe/go-skeleton` (a fixture repo, already opted into `dockerBuild`) and confirm both artifacts descend from the one new tag — `gh api repos/bborbe/go-skeleton/releases --jq '.[0].tag_name'` returns that tag, and the watcher's build Job logs `pushing manifest for …:<that tag>`.

## Desired Behavior

1. **The release is created after the tag is confirmed, before the image check.** The call site is `finishApproved`, on the `confirmed` branch, ahead of `applyImageCheck` — so a missing release is reported before an image result can mask it. A skipped or failed tag confirmation never reaches it.
2. **The tag is the only input.** The release's `tag_name` and `name` are `result.LocalTag`. No version is re-derived, no plan section is re-read.
3. **The body is the CHANGELOG section already written for that tag.** Read from the workdir via the existing `changelog.ExtractSectionBody` with the heading text derived the same way `checkFaithfulness` derives it (`strings.TrimPrefix(plan.NextVersionHeader, "## ")`). A body that cannot be read is not a reason to skip the release — create it with an empty body and record a note.
4. **Creation is idempotent.** A 422 whose body names `already_exists` means the release is published; the check reports `release_exists: true`. Any other non-2xx, and any transport error, is a failure.
5. **The check fails closed.** An error from `CreateRelease` — unreachable, timed out, rejected — records `release_exists: false`, appends `CheckRelease` to `failed_checks`, sets `overall: "fail"`, and returns `AgentStatusNeedsInput` / `NextPhase: "human_review"`. A missing release does not fix itself on retry, so `AgentStatusFailed` (which re-runs the push and the LLM call) is not used.
6. **The notes distinguish the two failure classes.** A transport error reads `release creation could not reach GitHub: <err>`; an HTTP rejection reads `GitHub rejected the release: status <n>: <body preview>`. Both fail; the operator needs to know which.
7. **The review block reports the observation, not a literal.** `release_exists` is true only when the client reported the release present or created. The short-circuit path records `release_exists: false` and does not fail on it.
8. **One call per run.** No retry, no second GET to confirm what the POST already reported.
9. **The changelog records the fix** under a newly created `## Unreleased` section.

## Constraints

- **The fix lands in this repo only.** The watcher's build path and the shared agent library are untouched.
- **Reuse the existing HTTP client seam.** `CreateRelease` joins `githubreview.Client` beside `TagExists`, `ResolveTagCommit` and `FetchChangelog`, reuses `setHeaders` (so `Accept`, the API version and the bearer token are applied identically), the 15s `http.Client` timeout, and the existing `errBodyPreviewMax` preview rule. A second HTTP client is out.
- **The client returns a wrapped error, never a sentinel that reads as success.** The only non-error "did not create" outcome is `already_exists`, and it returns `(false, nil)` — present, not created. Every other outcome is `(false, err)` or `(true, nil)`.
- **The mock is regenerated, not hand-edited.** `//counterfeiter:generate` on the interface already points at `mocks/review_client.go`; run `make generate`.
- **No new result outcome, result field, or error-category value.** The result contract has two shapes; this change touches none of them.
- **The step writes no frontmatter.** Status and phase remain the deliverer's decision.
- **Existing test conventions hold.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network, no live Claude calls.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)`; a token is never logged (`git.RedactToken` where an error may carry one).

## Assumptions

- **The GitHub App token the agent already holds can create a release.** It is the same token that pushes tags and calls the review API; release creation is the same `contents: write` scope class. If the App lacks it, the call returns a 403 and the check fails closed — the operator sees the error rather than a silent pass.
- **`gh api repos/bborbe/go-skeleton/releases` returning `0` is absence, not a filter artifact.** It is the repo-level releases endpoint with no state filter, and the same call returns non-zero on the four repos that do publish.
- **The watcher's build Job is triggered by the tag push, not by the Release object.** So creating the release does not affect image publication, and the ordering in Desired Behavior 1 is free.
- **`result.Workdir` still exists when the release is created.** It is cleaned up by the deferred `cleanupWorkdir` at the terminal return, after the `## Review` write — the same window `checkFaithfulness` reads it in.

## Failure Modes

| Trigger | Expected behavior | Detection / Recovery |
|---|---|---|
| GitHub unreachable (DNS, TLS, proxy) | `release_exists: false`, `CheckRelease` failed, notes `could not reach GitHub: …`, park `human_review` | Detection: notes text plus the `V(2)` line. Recovery: re-run once reachable — creation is idempotent |
| App token lacks release permission (403) | Same, notes `GitHub rejected the release: status 403: …` | Detection: notes. Recovery: operator grants the scope, re-run |
| Release already exists | `release_exists: true`, no failure, task closes | Detection: `release_exists: true` with `created: false` in the log line. Recovery: none needed |
| Two runs of the same task race | Both POST; one creates, one gets `already_exists`; both report present | Detection: two log lines, one `created=true`. Recovery: none needed |
| Tag confirmation failed earlier in the same run | The release call never happens — the `confirmed` branch was not taken | Detection: existing `human_review` routing. Recovery: existing path |
| CHANGELOG section body unreadable in the workdir | Release created with an empty body plus a note | Detection: note in `## Review`. Recovery: none — the tag is what matters |
| GitHub rate-limits the POST (403 with rate body) | Fails closed as a rejection | Detection: status 403 + body preview. Recovery: re-run after the window |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `githubreview.Client.CreateRelease` + the idempotent 422 handling + mock regeneration + client-level specs | 4, Constraints | (e) | — |
| 2 | `ReleaseExists` / `CheckRelease`, `applyReleaseCreation`, the fail-closed routing, the body read, and the step-level specs | 1, 2, 3, 5, 6, 7, 8 | (a), (b), (c), (d), (f) | prompt 1 |
| 3 | `## Unreleased` changelog entry and `make precommit` green | 9 | (g), (h) | prompts 1, 2 |

## Do-Nothing Option

Every release the agent cuts stays invisible on the repository's Releases page, and the gap is exactly as wide as the fleet: 95 of 99 repos with a completed release task since 2026-11-01 have no release behind it. The image half of this defect was fixed because a component happened to be built for it; the Release half has no such component and will not fix itself. Doing nothing also leaves the review block asserting `overall: "pass"` over a check set that does not contain the artifact, which is the precise failure this whole line of work exists to remove.

---
status: approved
approved: "2026-11-09T16:44:40Z"
branch: dark-factory/create-github-release-object
---

## Summary

- The release agent rewrites `## Unreleased` → `## vX.Y.Z`, commits, tags, and pushes. It then verifies the tag on the remote and probes for an image. **It never creates a GitHub Release object**, and no check in the review block asks whether one exists.
- The task therefore closes `status: completed` / `"overall": "pass"` on a repository whose Releases page is empty. Measured 2026-11-09: of 99 repos carrying a completed `github-release` task since 2026-11-01, **95 have no release published at or after that job's `job_started_at`**.
- The image half of the original defect has already been fixed elsewhere — `bborbe/github-release-watcher` dispatches an in-cluster build Job per release tag for opted-in repos, and `bborbe/github-releaser-agent` v0.8.0 added the `image_exists` check. Neither touches the Release object.
- Fix: after the pushed tag is confirmed on the remote, the review step creates the GitHub Release for that tag, records `release_exists` in the check set, and **fails closed** — an unreachable GitHub is a failure, never a silent pass.
- **This spec is blocked on a policy question.** See `## Open Questions` before reading anything else.

## Open Questions

**Doctrine status — the direction is the operator's, not this spec's.** `docs/releasing-github-releaser-agent.md:76-86` § *GitHub Release (optional milestone)* states that a GitHub Release is *"a separate, deliberate act — create one only to surface a milestone, not for every internal tag"*, written at repo setup (`cc251dd`, 2026-07-15) and never revised. This spec ships the opposite. Raised to the operator on attention card `dddc2607f12f9edce2bc4dc2545f2043`; the spec stays `status: draft` until answered.

**What would falsify the doctrine reading** — i.e. what would show the doc describes the agent's current behaviour rather than the fleet's intent: **a repo that wanted a release per tag building one itself instead of accepting the agent's omission.** That evidence exists. `bborbe/driftline/.github/workflows/release.yml` fires on `push: tags: ['v*']` and its header says *"A v\* tag is a release … create the GitHub release with the tag's CHANGELOG section as notes"*; all four repos that publish releases (`driftline`, `echo-sky-run`, `git-ai-sync`, `vault-ui`) do so as `github-actions[bot]` on every tag. Four independent per-repo workarounds are the strongest available evidence that the fleet wants the behaviour and the agent is the component missing it. This does not settle the policy — it removes the "the doc is the specification" reading as the safe default.

If the operator rules **tag-only**, this spec is void: ACs (a)–(i) all describe creating a release, and the correct change is to scope the regression metric to the publishing set. AC (j) is the only one that survives either way.

## Problem

A release is three independent facts: the tag, the image, and the Release object. The agent now checks the first two (and creates neither — the watcher creates the image). The third is never created and never checked, so the review block's "all checks passed" is a claim about a set that does not contain the thing consumers look at.

The observable damage is a repository that carries `v0.8.3` in its tag list and an image in the registry, while `gh api repos/bborbe/go-skeleton/releases` returns `0`.

The rejected alternative is to keep the work in `bborbe/github-release-watcher`, which already reacts to every tag. It is the wrong owner: the watcher's job is to dispatch builds, the release object is a property of the release the agent just cut, and putting it in the watcher would create the object for tags the agent never released. The second rejected alternative is to leave it per-repo as `bborbe/driftline` did — that is what four repos already do, and it means every new repo re-implements the same 30-line workflow.

## Reproduction

Read-only host probes, gathered 2026-11-09, against `bborbe/github-releaser-agent` `origin/master` @ `d66629b` (`release v0.8.1`) and `bborbe/github-release-watcher` `origin/master` @ `d4c2ae6`.

```bash
# The agent has no release-creation code at all.
git grep -nEi "releases|release create|CreateRelease" origin/master -- '*.go' '*.yml' 'Makefile'
#   → .github/workflows/check-unfinished-pipeline.yml:45 (a download URL)
#   → pkg/steps_execution.go:129-178 (two local variables named releaseSuccess)
#   No POST /repos/{owner}/{repo}/releases anywhere.

# A concrete repo: tag and image present, zero releases.
gh api repos/bborbe/go-skeleton/releases --jq 'length'                      # 0
git -C ~/Documents/workspaces/go-skeleton tag --sort=-v:refname | head -1   # v0.8.3
kubectlnukeprod -n build logs job/build-go-skeleton-v0-8-3-20261108-175350-jxig --tail=5
#   → pushing manifest for docker.prod.nuke.benjamin-borbe.de:443/bborbe/go-skeleton:v0.8.3@sha256:1ee9f467…

# The fleet-wide gap, inlined so it reproduces off this host.
# Task filenames contain spaces ("Release bborbe-x 7a59d90.md"), so read them
# line-wise — an `xargs` pipeline splits on the space and greps a file per word.
D=~/Documents/Obsidian/private-agent/tasks
grep -rl --include='*.md' -E '^task_type: github-release' "$D" \
| while IFS= read -r f; do
    grep -qE '^status: completed' "$f" || continue
    js=$(awk '/^job_started_at:/{print $2; exit}' "$f" | tr -d '"')
    [ -n "$js" ] && [[ ! "$js" < "2026-11-01" ]] || continue
    awk '/^repo:/{print $2; exit}' "$f" | tr -d '"'
  done | sort -u \
| while IFS= read -r r; do
    printf '%s %s\n' "$r" \
      "$(gh api "repos/$r/releases?per_page=1" --jq 'length' 2>/dev/null || echo ERR)"
  done \
| awk '$2=="0"{bad++} {n++} END{print n" repos, "bad" with no release"}'
#   → 99 repos, 95 with no release   (2026-11-09)
```

The four that read ok are `bborbe/driftline`, `bborbe/echo-sky-run`, `bborbe/git-ai-sync` and `bborbe/vault-ui` — all `github-actions[bot]` releases created by a per-repo `release.yml` on tag push.

## Expected vs Actual

| | |
|---|---|
| **Expected** | A confirmed release produces a GitHub Release object for its tag, and the review block records whether one exists. A GitHub that cannot be reached produces a **failed** check, not a pass. |
| **Actual** | The review step creates nothing. The check set is `ReviewChecks` (`pkg/steps_ai_review.go:74-101`) — `tag_exists`, `tag_at_expected_sha`, `changelog_header_rewritten`, `faithfulness_ok`, `unexpected_file_change`, `image_expected`, `image_exists` — and none of them is about a Release object. `finishApproved` pushes, reconciles the tag, runs `applyImageCheck`, writes `## Review`, returns `done`. |

## Why this is a bug

`overall: "pass"` is a statement about the check set, and the check set is the agent's own definition of a successful release. That definition omits the artifact every human and every consumer reads first. A check set that cannot fail for the observed reason is not a check — which is exactly the finding that opened this work, one artifact later.

## Goal

A confirmed release produces a GitHub Release object for its tag in the same run, with the tag as the only input and no manual step. The review block records `release_exists`, and a GitHub that cannot be reached or that rejects the call fails the `Release` check and parks the task at `human_review` rather than closing it. Re-running an already-released task does not fail: an existing release is success.

## Non-goals

- Do NOT build the image publication path. It exists in `bborbe/github-release-watcher` `pkg/build/`.
- Do NOT change the `image_exists` / `image_expected` semantics, including their opt-in gating. That defect is filed separately.
- Do NOT add a repo-level opt-out for release creation. A switch that disables the behaviour this spec exists to add negates it; if a repo must not publish, that is a separate spec.
- Do NOT repair the 95 existing records. They are operator work; this spec stops new ones.
- Do NOT create releases on the failed-outcome short-circuit path (`writeShortCircuit`). A `failed` outcome has no confirmed release.
- Do NOT add a retry loop, a second GitHub call, or a new timeout beyond the client's existing one.
- Do NOT change the release body format beyond the CHANGELOG section already written for the tag.

## Acceptance Criteria

- [ ] **(a) A confirmed release creates the GitHub Release object, for the right tag, with the right body.** A Ginkgo spec named `CreateReleaseOnConfirmedTag` runs the review step on a `released` fixture whose remote confirms the pushed tag, with a fake review client, and asserts: `review.Checks.ReleaseExists == true`; `result.NextPhase == "done"`; `fake.CreateReleaseCallCount() == 1`; `fake.CreateReleaseArgsForCall(0)` carries `result.LocalTag` as the tag and as the title; and the body argument **equals** the fixture's `## vX.Y.Z` section body — equality, not containment, so a hardcoded constant cannot satisfy it. Evidence: file content plus a recorded red run. (i) `grep -c 'CreateReleaseOnConfirmedTag' pkg/steps_ai_review_test.go` returns ≥1 and is 0 today. (ii) `grep -c 'CreateReleaseArgsForCall' pkg/steps_ai_review_test.go` returns ≥1, so a named-but-empty block fails. (iii) **Revert probe**: deleting the `applyReleaseCreation` call makes exactly that spec fail; record the failing output in the prompt's final summary. The argument assertions are what stop the laziest passing implementation — create a release for `v0.0.0` with an empty body — from satisfying this AC.
- [ ] **(b) An already-existing release is success, not a failure.** A spec named `CreateReleaseAlreadyExistsIsSuccess` drives the fake client to return `(false, nil)` and asserts `ReleaseExists == true`, `NextPhase == "done"`, and that `Release` is absent from `FailedChecks`. Evidence: file content plus a recorded red run; the name is absent today.
- [ ] **(c) An unreachable GitHub fails closed.** A spec named `CreateReleaseUnreachableFailsClosed` drives the fake client to return a transport error and asserts `ReleaseExists == false`, `CheckRelease` ∈ `FailedChecks`, `NextPhase == "human_review"`, and that `review.Notes` contains the literal `could not reach GitHub`. Evidence: file content plus a recorded red run; the name is absent today.
- [ ] **(d) A rejected call is distinguished from an unreachable one.** A spec named `CreateReleaseRejectedIsDistinctFromUnreachable` drives the fake client to return an HTTP-status error and asserts the notes contain `rejected the release` and **not** `could not reach GitHub`, with `ReleaseExists == false` and `NextPhase == "human_review"`. Evidence: file content plus a recorded red run; the name is absent today.
- [ ] **(e) The client is idempotent at the HTTP layer.** A client-level spec named `CreateReleaseTreatsAlreadyExistsAsSuccess` serves a 422 whose body names `already_exists` and asserts `(false, nil)`; `CreateReleaseReturnsWrappedErrorOnOtherNon2xx` serves a 500 and asserts a non-nil error. Evidence: both names absent today; `make test` exits 0.
- [ ] **(f) The check set gains the field and the failure name.** `ReviewChecks` carries `release_exists`, and `CheckRelease = "Release"` is declared beside the other stable check names. Evidence: ``grep -cE 'ReleaseExists[[:space:]]+bool[[:space:]]+`json:"release_exists"' pkg/steps_ai_review.go`` returns 1; `grep -cE 'CheckRelease[[:space:]]*=[[:space:]]*"Release"' pkg/steps_ai_review.go` returns 1.
- [ ] **(g) `CHANGELOG.md` gains a `## Unreleased` entry** naming the fix, created above the newest version header per `docs/dod.md`. Evidence: `grep -n '^## Unreleased' CHANGELOG.md` returns exactly one line and `grep -n '^## ' CHANGELOG.md | head -1` prints that same line number.
- [ ] **(h) `make precommit` exits 0.** Evidence: exit code.
- [ ] **Post-Deploy (Rung-3):** **(i) a confirmed release on a fixture repo produces its Release object from the running image.** After the fixed image runs, cut one release on `bborbe/go-skeleton` (a fixture repo, already opted into `dockerBuild`) and observe both artifacts from that one tag: `gh api repos/bborbe/go-skeleton/releases --jq '.[0].tag_name'` prints the new tag, and the same tag's build Job log contains `pushing manifest for`. No intervening manual `gh release create`. Evidence: both command outputs quoted.
  - `deploy_check:` `kubectlnukeprod -n development-sandbox get pods -l agent.benjamin-borbe.de/assignee=github-releaser-agent -o jsonpath='{range .items[*]}{.spec.containers[0].image}{"\n"}{end}' | awk -F: '{print $NF}' | sort -u | tail -1`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **(j) The project's release documentation states the contract this change ships.** Direction is the operator's (card `dddc2607f12f9edce2bc4dc2545f2043`), and the criterion holds in **both** outcomes — the doc must not be left contradicting the code. If the ruling is "create the release": rewrite `docs/releasing-github-releaser-agent.md` § *GitHub Release (optional milestone)* so that `grep -c 'optional milestone' docs/releasing-github-releaser-agent.md` returns 0 and `grep -c 'Release object' docs/releasing-github-releaser-agent.md` returns ≥1. If the ruling is "tag-only": this spec is withdrawn and the criterion is recorded on the parent task instead. Evidence: file content, plus the ruling referenced in the PR body.

Scenario coverage: none added. The repo has no `scenarios/` harness, and the behaviour is reached at the step boundary with the GitHub client faked. The runtime proof is AC (i).

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```bash
make precommit                                                                    # exit 0
make test                                                                         # exit 0
grep -c 'CreateReleaseOnConfirmedTag' pkg/steps_ai_review_test.go                 # ≥1
grep -c 'CreateReleaseArgsForCall' pkg/steps_ai_review_test.go                    # ≥1
grep -c 'CreateReleaseAlreadyExistsIsSuccess' pkg/steps_ai_review_test.go         # ≥1
grep -c 'CreateReleaseUnreachableFailsClosed' pkg/steps_ai_review_test.go         # ≥1
grep -c 'CreateReleaseRejectedIsDistinctFromUnreachable' pkg/steps_ai_review_test.go  # ≥1
grep -c 'CreateReleaseTreatsAlreadyExistsAsSuccess' pkg/githubreview/client_test.go   # ≥1
grep -c 'CreateReleaseReturnsWrappedErrorOnOtherNon2xx' pkg/githubreview/client_test.go   # ≥1
grep -cE 'ReleaseExists[[:space:]]+bool[[:space:]]+`json:"release_exists"' pkg/steps_ai_review.go   # 1
grep -cE 'CheckRelease[[:space:]]*=[[:space:]]*"Release"' pkg/steps_ai_review.go       # 1
grep -n '^## Unreleased' CHANGELOG.md                                             # exactly one line
grep -n '^## ' CHANGELOG.md | head -1                                             # the same line
```

### Operator-executable

AC (i) and AC (j). Deployment of the agent image and the fixture release are the operator's; the spec's own rungs stop at the container.

## Desired Behavior

1. **The release is created after the tag is confirmed, before the image check.** The call site is `finishApproved`, on the `confirmed` branch, ahead of `applyImageCheck` — so a missing release is reported before an image result can mask it. A skipped or failed tag confirmation never reaches it.
2. **The tag is the only input.** The release's `tag_name` and `name` are the released tag. No version is re-derived, no plan section is re-read.
3. **The body is the CHANGELOG section already written for that tag** — the same section text the faithfulness check reads, taken from the workdir by the same extraction. A body that cannot be read is not a reason to skip the release: create it with an empty body and record a note.
4. **Creation is idempotent.** A 422 whose body names `already_exists` means the release is published; the check reports `release_exists: true`. Any other non-2xx, and any transport error, is a failure.
5. **The check fails closed.** An error from `CreateRelease` — unreachable, timed out, rejected — records `release_exists: false`, appends `Release` to `failed_checks`, sets `overall: "fail"`, and returns `AgentStatusNeedsInput` / `NextPhase: "human_review"`. A missing release does not fix itself on retry, so `AgentStatusFailed` (which re-runs the push and the LLM call) is not used.
6. **The notes distinguish the two failure classes.** A transport error reads `release creation could not reach GitHub: <err>`; an HTTP rejection reads `GitHub rejected the release: status <n>: <body preview>`. Both fail; the operator needs to know which.
7. **One call per run.** No retry, no second GET to confirm what the POST already reported.

## Constraints

- **The fix lands in this repo only.** The watcher's build path and the shared agent library are untouched.
- **Reuse the existing HTTP client seam.** `CreateRelease` joins `githubreview.Client` beside `TagExists`, `ResolveTagCommit` and `FetchChangelog`, reuses `setHeaders` (so `Accept`, the API version and the bearer token are applied identically), the client's existing timeout, and the existing `errBodyPreviewMax` preview rule. A second HTTP client is out.
- **The client returns a wrapped error, never a sentinel that reads as success.** The only non-error "did not create" outcome is `already_exists`, and it returns `(false, nil)` — present, not created. Every other outcome is `(false, err)` or `(true, nil)`.
- **The mock is regenerated, not hand-edited.** `//counterfeiter:generate` on the interface already points at `mocks/review_client.go`; run `make generate`.
- **No new result outcome, result field, or error-category value.** The result contract has two shapes; this change touches none of them.
- **The step writes no frontmatter.** Status and phase remain the deliverer's decision.
- **The short-circuit path needs no change.** `writeShortCircuit` builds `ReviewChecks` field-by-field, so `release_exists` is `false` there by the zero value, which is the correct report for a `failed` outcome.
- **Existing test conventions hold.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network, no live Claude calls.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)`.

## Security / Abuse Cases

- **Credential handling.** The bearer token is applied only by the existing `setHeaders`; it is never placed in a URL, a note, a log line, or the release body. Every error path that may carry it passes through `git.RedactToken` before it reaches `## Review` notes or a `glog` line.
- **Input surface.** The only externally-influenced input is the CHANGELOG body, which becomes the release body verbatim. It is never executed, never interpolated into a shell, and never used to build a URL path.
- **Abuse.** A malformed or oversized release body is bounded by the CHANGELOG the agent itself just wrote; there is no user-supplied field in this call.
- **Failure disclosure.** A 403 from a scope-limited App token is surfaced as a failed check with a body preview capped at `errBodyPreviewMax` (200 bytes) — never a silent pass, never a full response dump.

## Assumptions

- **The GitHub App token the agent already holds can create a release.** It is the same token that pushes tags and calls the review API; release creation is the same `contents: write` scope class. If the App lacks it, the call returns a 403 and the check fails closed — the operator sees the error rather than a silent pass.
- **`gh api repos/<repo>/releases` returning `0` is absence, not a filter artifact.** It is the repo-level releases endpoint with no state filter, and the same call returns non-zero on the four repos that do publish.
- **The watcher's build Job is triggered by the tag push, not by the Release object.** Creating the release does not affect image publication, and the ordering in Desired Behavior 1 is free.
- **The workdir still exists when the release is created.** It is cleaned up at the terminal return, after the `## Review` write — the same window the faithfulness check reads it in.

## Failure Modes

| Trigger | Expected behavior | Detection / Recovery |
|---|---|---|
| GitHub unreachable (DNS, TLS, proxy) | `release_exists: false`, `Release` failed, notes `could not reach GitHub: …`, park `human_review` | Detection: notes text plus the `V(2)` line. Recovery: `kubectlnukeprod -n development-sandbox get pods \| grep github-releaser-agent` shows the next run; re-trigger the release task and confirm `"release_exists": true` in `## Review`. Creation is idempotent, so the retry is free |
| App token lacks release permission (403) | Same, notes `GitHub rejected the release: status 403: …` | Detection: notes. Recovery: operator confirms the App installation carries `contents: write` for the repo, then re-triggers; confirm `"release_exists": true` in `## Review` |
| Release already exists | `release_exists: true`, no failure, task closes | Detection: `release_exists: true` with `created: false` in the log line. Recovery: none needed |
| Two runs of the same task race | Both POST; one creates, one gets `already_exists`; both report present | Detection: two log lines, one `created=true`. Recovery: none needed |
| Tag confirmation failed earlier in the same run | The release call never happens — the `confirmed` branch was not taken | Detection: existing `human_review` routing. Recovery: existing path |
| CHANGELOG section body unreadable in the workdir | Release created with an empty body plus a note | Detection: note in `## Review`. Recovery: none — the tag is what matters |
| GitHub rate-limits the POST (403 with rate body) | Fails closed as a rejection | Detection: status 403 + body preview. Recovery: re-trigger after the window and confirm `"release_exists": true` in `## Review` |
| **Image check fails after the release was created** | `release_exists: true` and the task parks `human_review` with `Image` failed — a public release object exists for a release that reads as failed | Detection: `## Review` shows `release_exists: true` alongside `Image` in `failed_checks`. Recovery: none automatic — the release is public; the operator either fixes the image or deletes the release by hand (`gh release delete <tag>`) |
| **Agent crashes between the release POST and the `## Review` write** | Next run re-pushes the tag and the POST returns `already_exists` → `release_exists: true`, task closes | Detection: two runs, one `created=true` in the log. Recovery: none needed |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `githubreview.Client.CreateRelease` + the idempotent 422 handling + mock regeneration + client-level specs | 4 | (e) | — |
| 2 | `ReleaseExists` / `CheckRelease`, `applyReleaseCreation`, the fail-closed routing, the body read, and the step-level specs | 1, 2, 3, 5, 6, 7 | (a), (b), (c), (d), (f) | prompt 1 |
| 3 | `## Unreleased` changelog entry, `make precommit` green | — | (g), (h) | prompts 1, 2 |

Rationale: prompt 1 is the HTTP seam and is independently testable. Prompt 2 is the behavioural core and needs the seam to exist. Prompt 3 is the tail, and the changelog entry can only be honest once the code is in. ACs (i) and (j) are operator rungs and belong to no prompt.

## Do-Nothing Option

Every release the agent cuts stays invisible on the repository's Releases page, and the gap is as wide as the fleet: 95 of 99 repos with a completed release task since 2026-11-01 have no release behind it. Four repos have already paid to work around it with their own `release.yml`, which is the standing evidence that the behaviour is wanted and the per-repo duplication is the cost of not having it centrally. Doing nothing also leaves the review block asserting `overall: "pass"` over a check set that does not contain the artifact — the precise failure this whole line of work exists to remove.

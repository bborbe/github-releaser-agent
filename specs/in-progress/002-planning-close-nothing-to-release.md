---
status: prompted
approved: "2026-08-02T13:15:26Z"
generating: "2026-08-02T13:16:24Z"
prompted: "2026-08-02T13:31:21Z"
branch: dark-factory/planning-close-nothing-to-release
---

## Summary

- A release task that fires against a commit which has **already been released** currently parks forever as `needs_input`, waiting for an operator who has nothing to decide.
- The agent rejects it because `CHANGELOG.md` has no `## Unreleased` section — but at an already-released commit that section is *supposed* to be gone. It was consumed by that very release.
- Planning gains one check before that rejection: ask GitHub which commit the current version's tag points at. If it is the exact commit this task targets, there is nothing to release.
- That case completes the task instead of escalating it. Everything else escalates exactly as today.
- This extends the release pipeline's existing "GitHub is the truth, not local state" principle from the execution step, where it already ships, to the planning step, where it is missing.

## Problem

The release pipeline already knows how to recognise work that is already done — but only *after* planning succeeds. The `released`/`superseded` post-check added for [Auto-Close Release Tasks When Superseded] lives at the tail of the **execution** step. When planning halts on a precondition, execution never runs, so that post-check never fires and the task parks in `status: in_progress, phase: planning` indefinitely.

The precondition that halts it is `P1_unreleased_not_first`: `## Unreleased` is not the first `## ` section in `CHANGELOG.md`. That check is correct for a malformed changelog. It is wrong for the one case where a release-check re-triggers against a release commit itself — there, the newest `## vX.Y.Z` section legitimately sits first, because the release that just happened renamed `## Unreleased` into it. The agent reads a healthy post-release changelog and calls it malformed.

Observed 2026-08-01 on `bborbe/tts-mcp`: task ref `7657fc0` is the commit titled `release v0.3.1`, is the commit tag `v0.3.1` points to, and is the repo's `main` HEAD. The agent recorded `outcome: needs_input, precondition_failed: P1_unreleased_not_first`. The task sat open until an operator closed it by hand on 2026-08-02 after manually confirming the tag. The confirmation an operator performs by hand — "is this commit already tagged?" — is a single API call the agent can make itself.

## Goal

When planning is about to reject a task for `P1_unreleased_not_first`, it first asks GitHub which commit the effective current version's tag points at. If that commit is the task's own `ref`, planning writes a terminal `nothing_to_release` plan and the task ends `completed` with no operator involvement. In every other situation — the tag points elsewhere, the tag is absent, the ref is too short or not hex, or the lookup fails — planning escalates precisely as it does today, so a genuinely malformed changelog still reaches a human.

## Assumptions

- **GitHub's tags-list endpoint (`GET /repos/{owner}/{repo}/tags`) reports `commit.sha` already dereferenced to the underlying commit for annotated tags.** Verified 2026-08-02 against the incident repo: `gh api repos/bborbe/tts-mcp/tags --jq '.[] | select(.name=="v0.3.1") | .commit.sha'` returns `7657fc0af1a115b2518ac4c4d332722d8fc3d35c` — the commit — while `v0.3.1`'s annotated tag *object* is `9aa07bf6c9a64221e340b5529f7e47faf2f189fd`. This is why no second dereference request and no annotated-vs-lightweight branch is needed. The Verification section re-runs this check so the premise is proven, not assumed.
- The agent's existing GitHub token already carries read access to any repo it receives a release task for. No new scope is required.
- A task's `ref` frontmatter field is the commit the release-check fired against, not a branch name. Non-hex values are treated as non-matches rather than resolved.

## Non-goals

- Does **not** touch the execution-step post-check (`released` / `superseded` verdicts). That logic ships and stays as-is.
- Does **not** relax the `P1` changelog validation itself. The parser's definition of malformed is unchanged; only the disposition for one SHA-verified case changes.
- Does **not** apply to `P2_unreleased_empty`, `bad_current_version`, or missing-frontmatter preconditions. `P1` only.
- Does **not** change the watcher. Emitting a task for an already-released commit stays legal; the agent absorbs it.
- Does **not** deploy. Merging and tagging is the end of this spec; rolling the image to dev/prod is separate work.
- Does **not** add a scenario. The behavior is reachable by unit tests against a mocked tag seam.

## Acceptance Criteria

- [ ] Planning emits `nothing_to_release` when the task ref is the tag's commit — evidence: a unit test whose name contains `nothing to release` drives planning with a task ref equal to the mocked tag commit SHA and asserts `PlanOutput.Outcome` equals `nothing_to_release` and `PlanOutput.Reason` contains both the tag name and the observed commit SHA; `make test` exits 0.
- [ ] That outcome terminates the task as done via the **explicit** disposition — evidence: the same test asserts the returned `*agentlib.Result` has `Status == agentlib.AgentStatusDone` **and** `NextPhase == "done"` (the explicit form used by `pkg/steps_ai_review.go`, never the empty-`NextPhase` auto-advance documented as bug 048 at `pkg/steps_planning.go:624`), and `Status != agentlib.AgentStatusNeedsInput`.
- [ ] The completing path does not write escalation frontmatter — evidence: a unit test asserts that after the `nothing_to_release` path the task frontmatter contains no `previous_assignee` key and `assignee` is byte-identical to its input value (the adjacent `escalate()` path writes both; a copy-paste implementation must not).
- [ ] **Negative control — different commit still escalates:** a unit test with task ref `aaaaaaa…` and mocked tag commit `bbbbbbb…` asserts `PlanOutput.Outcome` equals `needs_input` and `PlanOutput.PreconditionFailed` equals `P1_unreleased_not_first` — byte-identical to today's behavior.
- [ ] **Negative control — tag absent still escalates:** a unit test where the tag lookup returns the no-tag sentinel asserts `outcome: needs_input` + `precondition_failed: P1_unreleased_not_first`.
- [ ] **Negative control — lookup failure still escalates:** a unit test where the tag lookup returns a transport error asserts `outcome: needs_input` + `precondition_failed: P1_unreleased_not_first`. The check fails open; a GitHub outage never converts an escalation into a silent completion.
- [ ] **Negative control — short and non-hex refs cannot manufacture a match:** a unit test asserts that `ref: "a"` against a mocked tag commit beginning `a…` yields `outcome: needs_input` + `P1_unreleased_not_first`, and that a non-hex ref (e.g. `main`) against any tag commit also escalates.
- [ ] **Negative control — other preconditions untouched:** a unit test asserts a `P2_unreleased_empty` validation failure still escalates without consulting the tag seam — evidence: the mocked tag fetcher records zero calls for that input (`Invocations()` count is 0).
- [ ] **Negative control — happy path makes no extra request:** a unit test with a valid `## Unreleased` changelog asserts the tag-commit lookup records zero invocations (`Invocations()` count is 0), so normal releases gain no latency.
- [ ] The tag→commit lookup returns the dereferenced **commit** SHA for an annotated tag — evidence: a `githubtags` unit test serves a canned tags-list payload whose entry has `"commit": {"sha": "7657fc0…"}` and asserts the returned SHA is `7657fc0…`, not a tag-object SHA.
- [ ] SHA comparison tolerates short-vs-full within the declared bounds — evidence: a unit test asserts a 7-character task ref matches a 40-character tag commit SHA sharing that prefix; that a 7-character ref differing in the last character does **not** match; and that comparison is case-insensitive (`7657FC0` matches `7657fc0…`).
- [ ] The deciding fact is greppable in logs on both branches — evidence: `grep -n 'planning: release_tag_check ref=' pkg/steps_planning.go` returns ≥1 line whose format string also contains `tag=`, `tag_commit=`, and `verdict=`; and a unit test asserts the emitted verdict token is `nothing_to_release` on the match path and `escalate` on the mismatch path.
- [ ] `CHANGELOG.md` gains an entry — evidence: `grep -n 'nothing to release' CHANGELOG.md` returns a line, and `awk '/^## /{s=$0} /nothing to release/{print s}' CHANGELOG.md` prints exactly one line, `## Unreleased`.
- [ ] The escalation-contract carve-out is documented — evidence: `grep -n 'except a SHA-verified nothing_to_release' docs/dod.md` returns ≥1 line inside the Git-Write Safety section, on or adjacent to the existing "never auto-advances to `done`" bullet, so the rule and its one exception cannot drift apart.
- [ ] `make precommit` exits 0. (Global — applies to every prompt in the decomposition, not one row.)

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — fmt, generate, test, lint, vet, vuln, license
- `make test` — unit suite
- `grep -n 'nothing_to_release' pkg/plan_output.go` — the outcome constant exists
- `grep -n 'planning: release_tag_check ref=' pkg/steps_planning.go` — the log line exists at the decision site
- `grep -n 'nothing to release' CHANGELOG.md` — changelog entry landed
- `grep -n 'except a SHA-verified nothing_to_release' docs/dod.md` — DoD carve-out landed

### Operator-executable (runs on the host after PR merge)

- `gh api repos/bborbe/tts-mcp/tags --jq '.[] | select(.name=="v0.3.1") | .commit.sha'` returns `7657fc0af1a115b2518ac4c4d332722d8fc3d35c` — proves the Assumptions premise against live GitHub (every unit test uses a canned payload, which encodes the assumption rather than testing it) and proves this change would have closed the exact task from the Problem section.

## Desired Behavior

1. When `changelog.ValidateUnreleased` fails and the failure classifies as `P1_unreleased_not_first`, planning consults the tag seam for the commit SHA of the tag named by the effective current version returned by `resolveCurrentVersion` — which is the remote's highest semver tag when one exists, and the frontmatter snapshot on the no-tags and transient-failure fallbacks. On a snapshot fallback the tag lookup usually finds nothing, which escalates (Failure Modes row 3).
2. When that commit SHA and the task's `ref` are the same commit, planning writes a `## Plan` block with `outcome: nothing_to_release` and a `reason` naming the tag and the shared SHA, and returns `Status: AgentStatusDone` with `NextPhase: "done"` — the explicit terminal form used by the ai_review step, never the empty-`NextPhase` auto-advance (bug 048). The task ends `status: completed, phase: done`. Neither `assignee` nor `previous_assignee` is written; there is nothing for an operator to pick up.
3. Two SHAs are "the same commit" only when: both are hex-only, the `ref` is at least 7 characters, and the shorter is a prefix of the longer after lowercasing both. Anything else — shorter ref, non-hex ref, empty ref — is a non-match and escalates. This bound is what prevents a truncated or hand-edited `ref` from matching an unrelated commit.
4. When the SHAs are not the same commit, the tag does not exist, or the lookup errors, planning escalates with `outcome: needs_input, precondition_failed: P1_unreleased_not_first` and the original validator reason — the current behavior, unchanged in wording.
5. Any precondition other than `P1_unreleased_not_first` escalates without consulting the tag seam at all.
6. Whenever the P1 check runs, planning logs exactly one line prefixed `planning: release_tag_check ref=` carrying the task ref, the tag consulted (`tag=`), the observed commit SHA (`tag_commit=`, empty when the tag is absent or the lookup failed), and the outcome (`verdict=nothing_to_release` or `verdict=escalate`). The line is emitted on **both** branches — a neutral prefix so an escalating task never reads as a completion in the logs — so an operator can grep the deciding fact for any task that hit the check.
7. The tag seam exposes a tag's **commit** SHA. GitHub's tags-list endpoint reports `commit.sha` already dereferenced for annotated tags (see Assumptions), so resolving a tag to its commit requires no second request.

## Constraints

- The `P1` reason string and `precondition_failed` value are unchanged on the escalation path. Downstream greps and the operator-side sweep skill key on the current wording.
- `changelog.ValidateUnreleased` is not modified. Its notion of a malformed changelog is untouched.
- The new lookup is added to the existing tag-fetch seam that planning already injects — no new client is wired into the planning step, and no `context.Background()` is introduced.
- The check runs **only** after a `P1` validation failure. The happy path (valid `## Unreleased`) makes no extra API call, so normal releases gain no latency and no new failure surface.
- **Request cost on the P1 path:** the new lookup is a separate seam method that performs one additional tags-list pagination against the same endpoint `resolveCurrentVersion` already uses — never one request per tag — and runs only on the P1 path.
- **Match bound:** hex-only, minimum 7 characters, case-insensitive, shorter-is-prefix-of-longer. A `nothing_to_release` verdict may only be written on a positive, observed match meeting all four conditions.
- Fail-open is mandatory: every error path from the lookup falls through to today's escalation.
- Existing `PlanOutput` fields keep their JSON names and semantics; the change is additive.
- Errors use `github.com/bborbe/errors` with context wrapping; `Info` logging is `V(n)`-gated (repo DoD).

## Failure Modes

| Trigger | Expected behavior | Detection | Reversibility | Recovery |
|---|---|---|---|---|
| GitHub unreachable / 5xx during tag lookup | Escalate as today (`needs_input` + `P1`) | Warning log naming the lookup failure; task appears in operator queue | Reversible — no state written | Operator re-sets `assignee: github-releaser-agent` on the task file and confirms a fresh `## Plan` block appears on the next controller run |
| GitHub rate-limit (403 with rate-limit headers) | Same as unreachable — escalate, never complete | Warning log; task parks as it does today | Reversible | Same re-delegation, after the limit window; confirm via a new `## Plan` block |
| Tag named by current version does not exist on remote | Escalate as today | Log line records tag-absent | Reversible | None needed — this is a genuine escalation |
| Tag exists but points at a different commit | Escalate as today | Log line records both SHAs | Reversible | None needed — genuine escalation |
| Task `ref` empty, non-hex, or shorter than 7 chars | Escalate; never treat as a match | Existing missing-frontmatter precondition fires first for empty; the match bound rejects the rest | Reversible | Operator corrects the task file, then re-delegates |
| Repo tagged with a lightweight (non-annotated) tag | Same code path; tags-list reports the commit SHA directly | Covered by unit test on the canned payload | n/a | n/a |
| GitHub changes the tags payload shape or pagination contract (schema drift) | JSON decode fails → wrapped error → fail-open escalation. Never a false completion. | Decode error in the warning log; `githubtags` tests fail against the new shape | Reversible | Update the decoder; re-delegate parked tasks |
| Task re-triggered after already completing as `nothing_to_release` | Re-running reaches the same verdict from the same inputs and writes the same block — no duplicate side effect, since planning writes no git state | `## Plan` block content is identical between runs | Reversible | None needed |
| Clock skew / stale tag cache at GitHub | Worst case is a stale "tag absent" answer → escalation, never a false completion | Operator sees an escalated task | Reversible | Re-delegate |

## Security / Abuse

The lookup is a read-only GitHub API call against the repo the task already names, using the token the agent already holds for that repo. No new credential, scope, or endpoint class is introduced.

The abuse question worth stating: could someone get a release skipped by making the agent believe a commit is already tagged? Only by pushing a tag pointing at that exact commit — which is indistinguishable from actually releasing it, and requires push access to the repo. The verdict is derived from remote state, never from the task file, so editing a task's `ref` cannot manufacture a match; the match bound (hex-only, ≥7 chars, prefix) means a truncated or crafted `ref` can only cause a mismatch, which escalates.

Fail-open is the safety property: every uncertain answer produces an escalation to a human, never a silent completion.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Tag seam: commit-SHA-for-tag lookup, tags-list `commit.sha` decoding, single-pagination cost constraint, mock regeneration | 7 | 10 | — |
| 2 | Planning: `nothing_to_release` outcome constant, the pre-escalation check, bounded SHA match, log line, terminal disposition, and all eight positive/negative unit tests | 1-6 | 1-9, 11, 12 | prompt 1 |
| 3 | Docs: `CHANGELOG.md` `## Unreleased` entry + `docs/dod.md` escalation-contract carve-out | — | 13, 14 | prompt 2 |

AC 15 (`make precommit` exits 0) is global — every prompt must satisfy it, not one row.

Rationale: prompt 1 establishes the lookup with no behavior change; prompt 2 is the whole behavioral change and owns its own tests; prompt 3 is text-only and depends on the final naming.

## Do-Nothing Option

Cost of not doing this: every release-check re-trigger against a release commit produces a task that no one can act on except by manually confirming a tag and closing the file. That has already happened once and will recur at the rate of spurious re-triggers across the fleet — the pipeline is configured for ~76 repos, and the operator-side sweep skill exists precisely because this class of dust accumulates.

The alternative to doing it in the agent is doing it in the sweep skill, which is where it currently lives. That works but leaves the agent writing a verdict it can prove wrong with one API call, and requires an operator to remember to run a sweep. The pipeline's stated direction — GitHub truth over local state, mechanical resolutions never reaching the human queue — puts the fix in the agent.

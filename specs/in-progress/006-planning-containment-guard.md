---
status: approved
approved: "2026-11-10T13:29:34Z"
branch: dark-factory/spec-006
---

## Summary

Close a release task as `nothing_to_release` when its ref is already contained in the released tag, on every planning pass — not only after a malformed-changelog failure.

- The guard runs on every pass, before the changelog fetch and before `.maintainer.yaml` is read.
- Containment is decided by GitHub's compare of `ref...tag-commit`, not by commit equality.
- Any uncertainty — error, absent tag, unknown status, absent `behind_by` key — plans normally.
- A ref ahead of or diverged from the tag still plans a release.

## Supersedes

`002-planning-close-nothing-to-release` is completed and therefore immutable. This spec reverses nine of its normative statements; everything else in 002 stands unchanged.

| 002 element | Status after 006 | Replacement |
|---|---|---|
| Constraints — "The check runs **only** after a `P1` validation failure. The happy path … makes no extra API call" | Superseded | The check runs on every planning pass (Constraints below) |
| AC 8 — "other preconditions untouched … zero invocations" | Superseded | The tag seam is consulted once per pass. Other preconditions escalate **only when the ref is not contained** — a contained ref closes at the guard before the precondition is ever classified (AC 12) |
| AC 9 — "happy path makes no extra request" | Superseded | The happy path consults the seam once and still plans (AC 12) |
| AC 7 — "short and non-hex refs cannot manufacture a match" | Superseded | Refs are resolved server-side (AC 9, Failure Modes row 7) |
| Desired Behavior 3 — hex-only / ≥7 chars / prefix bound | Superseded | No client-side bound; GitHub resolves the ref (§ Security) |
| Desired Behavior 5 — "any other precondition escalates without consulting the tag seam at all" | Superseded | The containment guard consults the seam on every pass, before the changelog is read |
| Assumption 3 — "non-hex values are treated as non-matches rather than resolved" | Superseded | Non-hex values are resolved by GitHub (§ Security) |
| Constraint — "Match bound" | Superseded | No match bound; see § Security |
| Non-goals — "Does not apply to `P2_unreleased_empty`, `bad_current_version`, or missing-frontmatter preconditions. **P1 only**" | Superseded | The guard applies to every pass, including those preconditions — a contained ref closes before they are classified |
| § Security — a non-hex ref "can only cause a mismatch, which escalates" | Superseded | A resolvable ref can close a task if GitHub reports containment (§ Security) |

## Problem

A releaser record ships version X, then re-enters planning and plans X+1. It never writes a terminal state for X, so it reads as permanently live to every has-a-Job probe.

Mechanism, read from `pkg/steps_planning.go` at `cf36b7a`:

- `Run` → `runClassification` → `resolveRewriteAndPublish` → `publishPlan` returns `Done` + `NextPhase: execution` whenever `changelog.ValidateUnreleased` passes. `ShouldRun` returns `true` unconditionally, so a re-trigger always re-enters.
- The only terminal writer reachable from planning is `publishNothingToRelease`, and its guard is reached **only** from `handleValidationFailure` under `precondition == P1_unreleased_not_first`.
- So "has this ref already shipped?" is asked only when the changelog looks malformed. On the happy path it is never asked.
- The record's `ref` still points at the pre-release commit, where `## Unreleased` is present and valid, because the release renames it in a **child** commit. Every re-trigger therefore plans again.
- The existing guard is `sameCommit` — prefix **equality** — which cannot fire: the tag commit is a child of `ref`, not equal to it.

Measured 2026-11-08: 24 records rated "auto superseded" by the close-on-shipped sweep, each with 3–6 succeeded Jobs. Concrete instance: record `37145af6` shipped `v0.23.3` while its ref was one commit behind the tag commit (`ahead 1, behind 0`), and the record never got its terminal write.

## Goal

A record whose ref is already released closes as `completed`/`done` on the planning pass that follows its release, without classifying a bump, fetching a changelog, or creating a successor record.

## Assumptions

- GitHub's compare endpoint reports `behind_by = 0` when the head commit already contains the base commit. Verified against the incident shape (`ahead 1, behind 0`).
- The planning step's `tagsFetcher` seam is the right home for the compare call: same auth, same client, same error style, already injected.
- A ref **ahead of** the tag means unreleased work exists and must still plan. Only containment closes.

## Non-goals

- Disposing the existing superseded records — that is the close-on-shipped sweep's job.
- Any change to the execution or ai-review steps.
- A config knob, feature flag, or metric.
- Prod rollout — this ships to dev first; the prod pin is tracked separately.

## Acceptance Criteria

- [ ] **AC1 — A contained ref closes terminal.** A record whose ref is contained in the effective current version's tag closes with `outcome: nothing_to_release`. — evidence: `PlanOutput.Outcome == "nothing_to_release"`, `result.Status == Done`, `result.NextPhase == "done"`.
- [ ] **AC2 — No escalation frontmatter on that path.** `assignee` is byte-identical and `previous_assignee` is never written. — evidence: `md.Frontmatter` has no `previous_assignee` key; `assignee` equals its input value.
- [ ] **AC3 — The closure survives the framework.** Delivered through `FileResultDeliverer`, the file on disk carries the terminal state. — evidence: `grep -c 'status: completed' <task-file>` returns 1; `grep -c previous_assignee <task-file>` returns 0.
- [ ] **AC4 — No work is done on the closing path.** — evidence: `fakeRunner.RunCallCount() == 0`, `fakeFetcher.FetchCallCount() == 0`, `maintainerConfig.FetchCallCount() == 0`, `tagsFetcher.CommitSHAForTagCallCount() == 1`, `tagsFetcher.CompareCommitsCallCount() == 1`.
- [ ] **AC5 — Equal ref still closes.** — evidence: compare `{Status: "identical", BehindBy: 0}` → `nothing_to_release`.
- [ ] **AC6 — Ref ahead of the tag still plans.** — evidence: compare `{Status: "behind", BehindBy: 2}` → `outcome: ready`.
- [ ] **AC7 — Diverged ref still plans.** — evidence: compare `{Status: "diverged", BehindBy: 2}` → `outcome: ready`.
- [ ] **AC8 — Fail-open on compare error, absent tag, unknown status, and absent `behind_by`.** — evidence: four cases each yielding `outcome: ready`; the absent-tag case additionally asserts `CompareCommitsCallCount() == 0`; the absent-`behind_by` case asserts a payload with `status: ahead` and `BehindBy: nil` does not close.
- [ ] **AC9 — A non-hex ref is resolved server-side, both ways.** — evidence: `ref: "main"` with a non-contained compare → `outcome: ready`; with a contained compare → `nothing_to_release`.
- [ ] **AC10 — The compare endpoint is contract-tested.** — evidence: the recorded request path equals `/repos/owner/repo/compare/base...head`; the request carries `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28` and the bearer `Authorization` header; a 404 and malformed JSON each return a wrapped error; the four empty-argument cases error without reaching the handler; a payload without `behind_by` decodes to `BehindBy == nil`.
- [ ] **AC11 — The deciding fact is greppable on both branches.** — evidence: `containment_check ref=` appears once per pass with `released=true` on the closing branch and `released=false` on the planning branch.
- [ ] **AC12 — The two superseded tests are updated, not deleted.** — evidence: `CommitSHAForTagCallCount() == 1` in both, each still asserting its original outcome (`P2` escalation; `outcome: ready`), and each stubbing `CompareCommitsReturns` with an explicit **non-contained** payload (`Status: "behind", BehindBy: 2`) rather than the mock's zero value — otherwise they assert fail-open, not non-containment.
- [ ] **AC13 — The containment reason is recorded and distinguishable.** — evidence: `PlanOutput.Reason` contains the tag and the tag commit, and does not contain the equality wording "is the commit tag".
- [ ] **AC14 — `docs/dod.md` carries the new carve-out and no longer the old rule.** — evidence: `grep -c 'containment-verified nothing_to_release plan' docs/dod.md` returns 1; `grep -c 'It is joined by a second exception' docs/dod.md` returns 1; the replaced tail (`That exception requires a positive, observed match`) is gone — `grep -c` returns 0.
- [ ] **AC15 — `CLAUDE.md` no longer states the uncarved rule.** — evidence: the Key Design Decisions bullet reading "never auto-advances to `done`" carries the carve-out reference, and a Containment-closure bullet is present.
- [ ] **AC16 — `README.md` and the CHANGELOG name the new behaviour.** — evidence: `grep -c 'already contained in the released tag' README.md` returns 1; `grep -c 'already contained in the effective current version' CHANGELOG.md` returns 1 and that bullet sits under `## Unreleased` (`awk '/^## /{s=$0} /already contained in the effective current version/{print s}' CHANGELOG.md` prints `## Unreleased`).

## Verification

- **Container-executable:** `make test`; `make precommit`; then `go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/... && go tool cover -func=/tmp/cover.out | grep -E 'refContainedInTag|refAlreadyReleased|CompareCommits|total'` — `make test` alone prints only per-package totals and cannot evidence per-function coverage. `refContainedInTag` must read 100.0%; `refAlreadyReleased` and `CompareCommits` at or above 80.0%. Also greps for the `containment_check` log token, the `docs/dod.md` carve-out, the `CLAUDE.md` bullet, and the CHANGELOG bullet under `## Unreleased`.
- **Operator-executable:** deploy the fixed image to the dev stage and observe a post-deploy dev release record reach `completed`. The deploy target is the core `agent` chart's `github-releaser-agent` Config — `agent/values-dev.yaml` in **`bborbe/nuke`**, not in this repo — and not the `github-releaser` Helm release, whose `agent.enabled: false` renders the watcher only. This is the acceptance read for the linked vault task, not for this spec.

## Desired Behavior

1. Planning resolves the effective current version's tag, then asks whether the record's `ref` is already contained in that tag's commit.
2. The ask is GitHub's compare of `base = ref`, `head = tag commit`; `behind_by == 0` with status `ahead` or `identical` means contained.
3. When contained, planning writes `outcome: nothing_to_release` with the tag and its commit in the reason, marks the task `completed`/`done`, and returns `Done` with `NextPhase: "done"`.
4. The ask happens before the changelog is fetched and before `.maintainer.yaml` is read, so a closing record does no content work and is unaffected by a malformed config.
5. When not contained — ahead, diverged, or any status other than `ahead`/`identical` — planning proceeds exactly as today.
6. When the answer cannot be obtained — tag absent, transport or decode error, `behind_by` absent from the payload — planning proceeds exactly as today.
7. The decision is logged on both branches with the ref, the tag, the tag commit, the status, both distances, and the verdict.

## Constraints

- The check runs on **every** planning pass. This deliberately reverses 002's "happy path makes no extra API call" constraint: the guard is worthless if it does not run on the path the defect takes.
- Cost is incremental: `+1` tags-list pagination (`CommitSHAForTag`) and `+1` compare call per pass, on top of the `LatestSemverTag` pagination every pass already makes. On a P1 failure that then escalates, `handleValidationFailure` calls `releaseTagCommit` a second time, so that path pays one further pagination. Accepted.
- Fail-open is mandatory on every error path, including a payload that omits `behind_by`. An outage or a shape change can never close a task.
- `sameCommit`, `isHexString`, `minRefMatchLen` and `releaseTagVerdict` survive unchanged and remain live for the P1 equality path; the containment guard does not use them, and `sameCommit`'s comment ("a nothing_to_release verdict may only be written on a positive match here") is corrected to say so.
- **Frozen names** — the implementation and the coverage gate depend on these three symbols existing with these names: `githubtags.CompareResult` (with `Status`, `AheadBy`, and pointer `BehindBy`), `refContainedInTag`, and `refAlreadyReleased`. The per-function coverage grep in § Verification is unimplementable if they are renamed.
- The P1 malformed-changelog path keeps its existing wording and behaviour.
- `changelog.ValidateUnreleased`, `classifyValidationFailure`, `escalate`, and the `escalation` struct are unchanged.
- The guard runs before `resolveMaintainerConfig`, so a malformed `.maintainer.yaml` is bypassed when the ref is already contained. That is intended — there is nothing to configure for a task that will not release — and must be stated in the prompt.

## Failure Modes

| # | Trigger | Expected behavior | Recovery |
|---|---|---|---|
| 1 | Compare returns 5xx or a transport error | Plan normally; `glog.Warningf` names the failure | Self-clearing on the next pass |
| 2 | Tag absent (`ErrTagNotFound`) | Plan normally; no compare attempted | Self-clearing when the tag appears |
| 3 | Compare returns an unknown status | Plan normally; not treated as containment | Self-clearing on a payload fix |
| 4 | Ref ahead of the tag | Plan normally | No action — the next pass logs `containment_check … released=false` |
| 5 | Ref diverged from the tag | Plan normally | No action — the next pass logs `containment_check … released=false` |
| 6 | Payload omits or renames `behind_by` | **Plan normally** — `BehindBy` is a pointer, so absent is not zero | Self-clearing; detection is the `pkg/githubtags` contract test pinning the key |
| 7 | Ref is a branch name, not a SHA | Resolved server-side; containment decided on the branch's current tip | Re-fire with a SHA if the branch moved |
| 8 | Rate limit on the added compare call | Compare errors → plan normally | Self-clearing on the next pass |

## Security / Abuse

The `ref` comes from the watcher's frontmatter, not from user input, and the guard can only **close** a task, never release one. Spec 002 bounded the ref client-side (`isHexString`, ≥7 chars) because it compared SHAs locally; here the comparison is server-side, so a short or branch-name ref is resolved by GitHub and cannot manufacture containment. This is a deliberate widening of 002's Security paragraph, which asserted a non-hex ref could only ever mismatch.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `CompareCommits` on the tags client (pointer `BehindBy`), Counterfeiter mock regeneration, and the endpoint contract test | DB1, DB2, DB6 | AC10 | — |
| 2 | `refContainedInTag`, `refAlreadyReleased`, the guard in `Run`, the parameterised reason string (`nothing to release: task ref %s is already contained in tag %s (%s)`), and the five corrected doc comments (`PlanOutcomeNothingToRelease`, `handleValidationFailure`, `Run`, `sameCommit`, `publishNothingToRelease`) | DB1, DB2, DB3, DB4, DB5, DB6, DB7 | AC1, AC2, AC5–AC9, AC11, AC13 | 1 |
| 3 | The two updated tests plus the new guard tests, including the deliverer boundary | DB1, DB6 | AC3, AC4, AC12 | 2 |
| 4 | `docs/dod.md`, `CLAUDE.md`, `README.md`, CHANGELOG | DB3 | AC14, AC15, AC16 | — |

## Do-Nothing Option

The defect re-accumulates: every future release leaves a record that reads live forever, and the close-on-shipped sweep keeps disposing them. Measured cost on 2026-11-08 was 24 records across one sweep — the same shape recurs on every release cycle.

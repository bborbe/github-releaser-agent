---
status: approved
tags:
    - dark-factory
    - spec
approved: "2026-07-15T20:51:08Z"
branch: dark-factory/classifier-clamp-major-bump
---

## Summary

- Today a release whose changelog implies a breaking change halts at `human_review` whenever a major bump is not permitted — the operator must intervene even though a safe minor release is available.
- This spec replaces that dead-end escalation with a deterministic **clamp**: when major is not permitted, a would-be major bump caps at `minor` and the release ships.
- "Major permitted" = the target repo's `.maintainer.yaml` `release.allowMajorBump` OR the per-run `--allow-major` / `ALLOW_MAJOR` override. Either one → full `major | minor | patch` range, no clamp.
- Two layers: the bump-classifier prompt is told at call time not to return `major` (soft guidance), and the planning code clamps `major`→`minor` unconditionally (hard guarantee even if the LLM ignores the prompt).
- Mirrors the existing "Pre-1.0 cap" behavior already in the classifier prompt. The upstream spec-060 decision table (escalate) lives in `bborbe/maintainer` and needs a separate follow-up update — out of scope here.

## Problem

When the bump classifier resolves a release to `major` but major bumps are not permitted (neither the repo's `.maintainer.yaml` opt-in nor the per-run override is set), the planning step escalates to `human_review` and clears the assignee. The release is stuck: a human must manually re-classify or re-delegate, even though the changelog contains a perfectly shippable set of minor/patch changes. This creates operator toil and stalls the release pipeline on exactly the changes that should flow automatically. The system already knows how to safely cap a bump — the "Pre-1.0 cap" does this for `0.x` streams — but the allow-major policy path escalates instead of capping.

## Goal

When a release is classified and a major bump is not permitted, the planning step ships the release as a `minor` (capping a would-be `major`) instead of escalating to `human_review`. A release never enters `human_review` solely because a major bump is disallowed. When a major bump IS permitted (either opt-in source), the classifier's full `major | minor | patch` range is honored unchanged.

## Non-goals

- Do NOT change the Pre-1.0 cap: `0.x` / `v0.x` streams still cap at `minor` with `reasoning` mentioning `pre-1.0`, independent of the allow-major policy.
- Do NOT change behavior when major IS permitted — the `--allow-major` / `ALLOW_MAJOR` override and `release.allowMajorBump: true` still mean "full range, no clamp".
- Do NOT edit the spec-060 decision table in this repo — that spec lives in `bborbe/maintainer`; its update is a named follow-up (see Do-Nothing / Constraints), not part of this work.
- Do NOT add a config flag or opt-out to disable the clamp — the clamp IS the guarantee; an escape hatch that re-enables the escalation dead-end would re-introduce the very problem this spec removes.
- Do NOT change how the two opt-in sources are resolved (single `.maintainer.yaml` fetch, per-run flag) — only how a disallowed `major` verdict is handled after resolution.

## Desired Behavior

1. The effective "major allowed" decision is `allowMajorBumpConfig` (from `.maintainer.yaml` `release.allowMajorBump`) OR the per-run override (`--allow-major` / `ALLOW_MAJOR`). Both false → major NOT allowed.
2. When major is NOT allowed, the bump-classification prompt receives an injected "major-bump policy" section at call time (mirroring how the `## Current version` section is concatenated) instructing the LLM: it MUST NOT return `major`, it MUST cap at `minor`, and its `reasoning` MUST mention the major-bump cap.
3. When major IS allowed, no policy section is injected (or it states the full range is permitted) — the classifier may return `major | minor | patch` exactly as today.
4. When the classifier returns `major` and major is NOT allowed, the planning code deterministically downgrades the bump to `minor`, recomputes the next version as a minor bump, emits a `glog.V(2)` clamp audit line, and proceeds to publish the plan — the release ships as a minor.
5. The clamp is unconditional on the code path: even if the LLM ignores the injected prompt and returns `major`, the code still caps to `minor`. The prompt guides; the code guarantees.
6. A clamped release produces the same `ready` plan outcome and `Done` result (advancing to `execution`) as any normal minor release — it does NOT return `needs_input` and does NOT clear the assignee.
7. The Pre-1.0 cap continues to fire first for `0.x` streams regardless of the allow-major policy — a pre-1.0 release is already capped at minor before the allow-major clamp is relevant.

## Constraints

- The static embedded classifier prompt (`pkg/prompts/bump_classification.md`) stays intact; the major-bump policy is a **runtime-injected** section, exactly like the existing `## Current version` injection in `resolveBumpVerdict`. The Pre-1.0 cap text in the embedded prompt is unchanged.
- The single-fetch invariant for `.maintainer.yaml` is preserved — no new fetch of the config; the effective-major decision is derived from the already-resolved `allowMajorBumpConfig` plus the existing `s.allowMajor` field.
- The git-write safety contract (Definition of Done) holds: the LLM only classifies; the clamp and version recompute are deterministic Go; commits still touch `CHANGELOG.md` only.
- The bump-verdict parser (`ParseBumpVerdict` / `validateVerdict`) still accepts `patch | minor | major` — the parser is not the clamp point; the classifier may legally emit `major` and the code caps it. Do not tighten the parser.
- **Upstream spec dependency:** the current escalate-on-disallowed-major behavior is codified in spec-060's FROZEN decision table, which lives in `bborbe/maintainer` (NOT this repo). This change flips that table (escalate → clamp). Updating that upstream spec is a REQUIRED follow-up tracked separately — this spec must not edit or fabricate a spec-060 file in this repo.
- Definition of Done per `docs/dod.md`: error wrapping via `github.com/bborbe/errors`, `glog` `V(n)`-gated logging, Ginkgo v2 / Gomega + Counterfeiter tests, ≥80% coverage on changed code, and a `## Unreleased` CHANGELOG bullet.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---------|-------------------|----------|-----------|
| Classifier returns `major`, major NOT allowed | Clamp to `minor`, recompute next version, `glog.V(2)` audit line, publish `ready` plan | None needed — release ships as minor | `kubectl logs` shows the clamp audit line; `## Plan` shows `bump: minor` |
| Classifier returns `major`, major IS allowed (either source) | No clamp; publish `major` release | None needed | `## Plan` shows `bump: major` |
| Classifier returns `minor` or `patch`, major NOT allowed | No-op for the clamp; publish as returned | None needed | `## Plan` shows the returned bump |
| Pre-1.0 (`0.x`) stream + breaking change | Pre-1.0 cap yields `minor` before allow-major clamp is reached; ships minor | None needed | `reasoning` mentions `pre-1.0` |
| `.maintainer.yaml` malformed / non-boolean `allowMajorBump` | Unchanged: fail-closed to `human_review` with `error_category=invalid_config` (config-typo boundary, not a bump decision) | Operator fixes the YAML and re-fires | `## Plan` shows `error_category=invalid_config` |
| `semver.BumpVersion` fails recomputing the clamped minor | Escalate via existing `PreconditionBadCurrentVersion` path (malformed `current_version`) | Operator fixes `current_version` and re-fires | `## Plan` shows `precondition_failed=bad_current_version` |
| LLM ignores injected prompt and returns `major` when not allowed | Code clamps to `minor` regardless | None needed | Same as row 1 |

## Acceptance Criteria

- [ ] With major NOT allowed (both sources false) and the classifier forced to return `major`, the planning result is `AgentStatusDone` with `NextPhase=execution` — evidence: test asserts `result.Status == AgentStatusDone` and `NextPhase == "execution"` (NOT `AgentStatusNeedsInput`).
- [ ] The same clamp case publishes `## Plan` with `bump: minor` and a next version computed as a minor bump of `current_version` — evidence: parsed `PlanOutput.Bump == "minor"` and `NextVersion` equals the minor-bumped value.
- [ ] The clamp case does NOT clear the assignee and does NOT set `outcome: needs_input` — evidence: `PlanOutput.Outcome == ready` and frontmatter `assignee` unchanged (not emptied).
- [ ] A `glog.V(2)` clamp audit line is emitted on the clamp path naming the original `major` verdict and the clamped `minor` result — evidence: log line captured in test.
- [ ] When major NOT allowed, the prompt sent to the runner contains an injected major-bump policy instruction forbidding `major` and requiring `reasoning` to mention the cap — evidence: fake `ClaudeRunner` captures the prompt; test asserts the injected substring is present.
- [ ] When major IS allowed (repo opt-in OR per-run override), the injected policy does NOT forbid `major` and a `major` verdict publishes `bump: major` — evidence: fake runner prompt lacks the forbid-major instruction AND `PlanOutput.Bump == "major"`.
- [ ] Pre-1.0 (`0.x`) behavior is unchanged — evidence: existing Pre-1.0 test(s) still pass; `reasoning` still mentions `pre-1.0`.
- [ ] Existing tests asserting `PreconditionMajorBumpNotAllowed` escalation are updated to assert clamp-to-minor (bump=minor, next version minor, outcome ready/Done, NOT needs_input) — evidence: `grep -n "PreconditionMajorBumpNotAllowed" pkg/steps_planning_test.go` returns zero escalation-assertion hits, or only audit-field references.
- [ ] `make precommit` exits 0 — evidence: exit code 0.
- [ ] A `## Unreleased` CHANGELOG bullet describes the clamp behavior change — evidence: `grep -n "clamp\|major" CHANGELOG.md` under `## Unreleased` returns ≥1 line.

## Suggested Decomposition

Natural prompt seams (the prompt-creator may merge b+c if sizing allows):

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|--------------|------------|------------|------------|
| a | Effective-major decision + runtime prompt-policy injection in `resolveBumpVerdict` | 1, 2, 3 | 5, 6 | — |
| b | Deterministic clamp (`major`→`minor`) + version recompute + `glog.V(2)` audit line in `applyMajorBumpGuard` | 4, 5, 6, 7 | 1, 2, 3, 4 | a |
| c | Migrate tests off `PreconditionMajorBumpNotAllowed` escalation → clamp assertions; dead-code decision in `plan_output.go` | — | 7, 8 | b |
| d | CHANGELOG `## Unreleased` bullet + `make precommit` green | — | 9, 10 | a, b, c |

## Verification

```
make precommit
```

Expected: exit 0 (fmt, generate, test, lint, vet, vuln, license all pass); the updated planning tests assert clamp-to-minor; coverage on changed code ≥80%.

## Dead-Code Decision

- `PreconditionMajorBumpNotAllowed` constant (`pkg/plan_output.go`): removing the escalate site makes it dead unless still referenced. **Recommendation:** remove it if `grep -rn "PreconditionMajorBumpNotAllowed" pkg/` shows no non-test references after the change; retain (with a doc comment noting it is historical) only if another path references it. The implementer decides at impl time based on the grep result.
- `PlanOutput.AllowMajorBumpConfig` / `AllowMajorBumpFlag` fields and the `escalation` struct's matching fields: these were populated only on the trip case. **Recommendation:** remove them together with the escalate site if no other writer remains; the clamp path does not need them. If retained for audit, they MUST have a live writer — an unwritten `omitempty` field is dead. Implementer decides at impl time.

## Do-Nothing Option

If we do nothing, every release whose changelog implies a breaking change under a repo that has not opted into major bumps stalls at `human_review`, requiring manual operator intervention on changes that could ship safely as a minor. This is the current behavior and is the source of avoidable release-pipeline toil. Rejected: the whole point of the allow-major policy is to *bound* the release, not to *block* it — capping is the correct bounded behavior, exactly as the Pre-1.0 cap already demonstrates.

**Required follow-up (separate, out of scope):** update spec-060 in `bborbe/maintainer` so its decision table reads "disallowed major → clamp to minor" instead of "→ escalate", keeping the upstream spec and this implementation in sync.

---
status: approved
spec: [004-bug-releaser-publish-blocked-by-branch-protection]
created: "2026-10-21T16:44:00Z"
queued: "2026-10-21T16:54:49Z"
branch: dark-factory/bug-releaser-publish-blocked-by-branch-protection
---

# Document the pull-request fallback and record the bypass evidence

<summary>
- The changelog gets an `## Unreleased` bullet for the next self-release, describing the pull-request fallback in the repo's conventional prefix style.
- The README's "How it works" section now describes what happens when the bypass fast path does not land: branch push, pull request, tag push, human merge.
- The Definition of Done's Git-Write Safety section names the PR fallback as part of the approved write path, so the invariant text no longer implies the only write is a direct push.
- The releasing runbook records the observed bypass evidence and the leading hypothesis, so the unresolved root cause is written down instead of living only in a task thread.
- Nothing in the code changes; existing tests and the full precommit gate must stay green.
- This is the last of three prompts for spec 004; it lands after the PR fallback.
</summary>

<objective>
Close the documentation half of spec 004: add the `## Unreleased` changelog entry the self-release bot will turn into the next version, update the README and `docs/dod.md` so the documented release contract includes the pull-request fallback, and record the bypass evidence plus the leading hypothesis in the releasing runbook.
</objective>

<context>
Read `CLAUDE.md` (Releasing section) and `docs/dod.md` (Documentation section — it fixes the required position of `## Unreleased`).

Read these files BEFORE editing:
- `CHANGELOG.md` — the preamble ends after the SemVer lines; the newest section header today is `## v0.4.19` and there is no `## Unreleased` section. Note the existing bullet style (`- fix: ...`, `- chore: ...`).
- `README.md` § How it works (steps 1–5 and the bypass-actor paragraph that follows) and § Layout.
- `docs/dod.md` § Git-Write Safety (four bullets: deterministic Go writes, CHANGELOG-only commits, no other file writes, and the escalation contract).
- `docs/releasing-github-releaser-agent.md` — the whole file, especially § "Self-release loop (the recursion)" and § "Binary/image release".
- `specs/in-progress/004-bug-releaser-publish-blocked-by-branch-protection.md` § Problem, § Assumptions, and § Goal — the evidence and hypothesis to record.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>

## 1. Create the `## Unreleased` section in `CHANGELOG.md`

Insert a new section directly below the preamble block and above `## v0.4.19`, so the order reads: `# Changelog` → preamble → `## Unreleased` → `## v0.4.19` → the rest. Never between the `# Changelog` title and the preamble.

The section carries exactly one bullet starting with `- fix:`. The bullet must mention the **pull-request fallback** explicitly and state the reader-visible behaviour: when a push is rejected by branch protection, the releaser publishes the release commit to a `release/<tag>` branch, opens a pull request, pushes the tag, and parks the task at `human_review` with the PR URL instead of failing. Keep it one bullet in the repo's existing style; do not add a second bullet.

Leave every existing `## vX.Y.Z` section byte-identical. Do not rename `## Unreleased` and do not create a version header.

## 2. Update `README.md` § How it works

After step 5 (the direct commit + tag push), add a step 6 or a short paragraph describing the fallback:

- when the direct push is rejected by branch protection (`GH013` / `GH006`), the agent pushes the release commit to `release/<tag>`, opens a pull request titled `release <tag>` with the `CHANGELOG` section as the body, pushes the annotated tag, and parks the task at `human_review` naming the PR URL;
- a human merges the PR; the agent never merges it and never force-pushes;
- an already-open PR for the same head branch is reused rather than duplicated.

Keep the existing bypass-actor paragraph: the fast path is still the intended one. The new text describes what happens when it does not land.

## 3. Update `docs/dod.md` § Git-Write Safety

Add one bullet to that section stating that the protected-branch fallback is part of the approved write path and is limited to exactly: push `HEAD` to `refs/heads/release/<tag>`, push `refs/tags/<tag>`, and create at most one pull request — no force-push, no automatic merge, no other file writes. Keep the existing bullets unchanged; this is additive.

## 4. Record the bypass evidence and hypothesis in `docs/releasing-github-releaser-agent.md`

Add a short subsection (e.g. `### Branch-protection bypass`) under § "Self-release loop (the recursion)" or § "Binary/image release", containing:

- the observed evidence: the releaser app installation (`Integration 3907226`) is listed with `bypass_mode: always` in the `master-protection` ruleset of `bborbe/go-skeleton`, `bborbe/nuke`, and `bborbe/sun`; `nuke` and `sun` released successfully on 2026-10-21 while `go-skeleton` was rejected with `GH013`; `go-skeleton` alone sets `strict_required_status_checks_policy: true` with three required checks;
- the leading hypothesis: the bypass is not reaching this repo/app pair (or the push identity differs from the documented assumption); the root cause is unresolved and tracked as a follow-up;
- the operational consequence: the agent now falls back to a pull request, so a protected repo still produces a reviewable release; a task parked at `human_review` with a PR URL is resolved by the operator merging the PR (spec § Failure Modes, "Tag pushed, PR never merged"), not by re-driving the task.

Do not present the hypothesis as a confirmed cause, and do not claim the bypass is broken in general — only that it did not apply to `go-skeleton` in the observed run.

## 5. Self-check

Re-run `<verification>` and confirm it passes. Then read the four files and confirm: `## Unreleased` is the first `## ` header and sits above `## v0.4.19`; no `## vX.Y.Z` section changed; the README still documents the bypass fast path alongside the fallback; the `docs/dod.md` bullet is additive; the runbook records evidence, hypothesis, and the operator recovery step.
</requirements>

<constraints>
- **Do not rename `## Unreleased` and do not create a version header.** The maintainer bot renames the section at release time; hand-renaming it or adding `## vX.Y.Z` races the bot (`docs/releasing-github-releaser-agent.md`).
- **Documentation and changelog only.** Do not change any `.go` file, test, `Makefile`, or `.dark-factory.yaml` in this prompt.
- **One changelog bullet.** Do not split the entry per file or per sub-behaviour.
- **The hypothesis stays a hypothesis.** Do not write a confirmed root cause; the spec records it as unresolved.
- **Existing tests keep passing** and the full precommit gate must be green.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).

```bash
test "$(grep -n '^## ' CHANGELOG.md | head -1 | cut -d: -f2-)" = "## Unreleased"
test "$(grep -c '^## Unreleased' CHANGELOG.md)" -eq 1
awk '/^## /{sec=$0} /^- fix:/{ if (sec=="## Unreleased") print }' CHANGELOG.md | grep -qi 'pull request'
grep -q '^## v0.4.19' CHANGELOG.md
awk '/^## How it works/{s=1;next} /^## /{s=0} s' README.md | grep -qi 'pull request'
awk '/^## Git-Write Safety/{s=1;next} /^## /{s=0} s' docs/dod.md | grep -qi 'pull request'
grep -qi 'bypass' docs/releasing-github-releaser-agent.md
grep -qi 'hypothesis\|unresolved' docs/releasing-github-releaser-agent.md
```
</verification>

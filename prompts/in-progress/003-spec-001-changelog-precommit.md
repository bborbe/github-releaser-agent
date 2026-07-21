---
status: approved
spec: [001-bug-stale-current-version-collision]
created: "2026-07-21T16:42:00Z"
queued: "2026-07-21T16:52:52Z"
branch: dark-factory/bug-stale-current-version-collision
---

<summary>
- Records the plan-time version-resolution fix in the changelog so the release bot can classify the version bump.
- Adds a single `## Unreleased` entry describing that planning now resolves the current version from the repo's latest remote tag instead of the stale emitted snapshot.
- Runs the full precommit suite one final time to confirm the whole change (prompts 1 and 2 plus this changelog) is green.
- No production code changes in this prompt — it is the changelog + final validation gate.
</summary>

<objective>
Add the `## Unreleased` CHANGELOG entry describing the spec-001 plan-time `current_version` resolution fix, and confirm `make precommit` passes for the complete change set (prompts 1 + 2 + this entry). This is the release-hygiene + final-gate prompt.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions.

Read:
- `/workspace/CHANGELOG.md` — note there is currently NO `## Unreleased` block; the top release header is `## v0.2.0`. You will INSERT a new `## Unreleased` section immediately below the intro paragraph (the "and this project adheres to Semantic Versioning" line) and ABOVE `## v0.2.0`. Follow the existing bullet style: `- <prefix>(<scope>): <what> [why]`.
- `/workspace/specs/in-progress/001-bug-stale-current-version-collision.md` — the spec, for the precise fix description (resolve `current_version` from the repo's latest remote tag at plan time; fall back to the emit-time snapshot on no-tags or transient error; escalation contract unchanged).

Reference doc (in-container path):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, prefix rules, anti-patterns.

Depends on prompts 1 and 2 having landed. If `pkg/githubtags/tags.go` OR `resolveCurrentVersion` in `pkg/steps_planning.go` is missing, prompts 1/2 have not shipped; STOP and report `Status: failed` with message `"spec-001 code not yet present (prompts 1/2)"` — do NOT add the changelog entry for undelivered code.
</context>

<requirements>

1. Insert a new `## Unreleased` section in `/workspace/CHANGELOG.md` immediately below the intro block (the line ending "…Semantic Versioning].") and directly above `## v0.2.0`. If a `## Unreleased` section already exists, append to it instead of creating a duplicate.

2. Add exactly ONE bullet (the fix is one logical change), `fix` prefix so dark-factory classifies a patch bump. Use a scope of `planning`. The bullet must name the observable behavior, not the file. Suggested text (tighten as needed, keep it one bullet):

   ```
   - fix(planning): resolve `current_version` from the target repo's latest remote semver tag at plan time instead of the emit-time frontmatter snapshot, so a repo tagged between task emit and run (e.g. a second release cut for a different `## Unreleased` item) bumps above the true latest tag and cuts the correct next version rather than colliding with an existing tag and dropping the release as `superseded`. On zero remote tags (fresh repo) planning falls back to the snapshot cleanly; on a transient tag-fetch error it degrades to the snapshot and surfaces a non-fatal warning on the `## Plan` block — never fail-closed. The missing-`current_version` escalation contract is unchanged.
   ```

3. Do NOT rename `## Unreleased` to a version header, do NOT create a git tag, do NOT touch `## v0.2.0` or any prior section. (This repo is released by an instance of itself post-merge — hand-renaming is forbidden per `/workspace/CLAUDE.md` § Releasing.)

</requirements>

<constraints>
- Exactly one `## Unreleased` bullet, `fix(planning):` prefix — one logical change, patch bump.
- Do NOT copy verification bash comments or the prompt filename into the changelog. Describe what was IMPLEMENTED (the plan-time resolution behavior), not what was tested.
- Do NOT hand-rename `## Unreleased` or create a tag — the release bot does that post-merge.
- No production code changes in this prompt.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Confirm the entry and that the whole change set is green:
```
cd /workspace && grep -n "## Unreleased" CHANGELOG.md
cd /workspace && grep -nE "^- fix\\(planning\\): resolve current_version" CHANGELOG.md
```
Then run `make precommit` — must pass (fmt, generate, test, lint, vet, vuln, license). A non-zero exit code = report `status: failed`.
</verification>

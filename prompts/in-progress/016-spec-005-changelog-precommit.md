---
status: approved
spec: [005-create-github-release-object]
created: "2026-11-09T17:38:03Z"
queued: "2026-11-09T17:58:03Z"
branch: dark-factory/create-github-release-object
---

# Record the release-object fix in the changelog and pass precommit

<summary>
- The project changelog gains an `## Unreleased` entry describing the new Release-object behaviour.
- The entry sits in the required position: below the preamble and above the newest version header.
- The entry names the fix, the `release_exists` check, the fail-closed routing, and the already-exists-as-success rule.
- No existing version header, bullet, or preamble line is changed.
- `make precommit` is the final gate and must exit 0 over the code from the first two prompts.
- This is the last prompt; the post-deploy proof and the release-documentation rewrite are operator rungs and are not implemented here.
</summary>

<objective>
Add the `## Unreleased` changelog entry for the GitHub Release object fix, in the position `docs/dod.md` requires, and confirm the full repository passes `make precommit`. This is the only prompt that touches `CHANGELOG.md`.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` — the Definition of Done the daemon validates every prompt against. Its changelog rule is:

> CHANGELOG.md has an entry under `## Unreleased`. If that section does not exist yet, create it **below** the preamble block and **above** the newest `## vX.Y.Z` section — never between the `# Changelog` title and the preamble. The final order is always: `# Changelog` → preamble → `## Unreleased` → `## vX.Y.Z` (newest first).

Read `CHANGELOG.md`. It currently has no `## Unreleased` section; the first `## ` heading is `## v0.8.1`. Read the two preceding prompts (`prompts/1-spec-005-create-release-client.md`, `prompts/2-spec-005-create-release-step.md`) so the entry describes what actually landed, not a guess.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/git-commit-guide.md`

<!-- OPEN QUESTION FOR THE AUDITOR: ACs (i) and (j) of the spec are operator rungs and are
     implemented by no prompt. AC (i) is the post-deploy proof on bborbe/go-skeleton (create a
     release with the fixed image and observe the Release object plus the build Job log); AC (j)
     is the operator's rewrite of docs/releasing-github-releaser-agent.md § "GitHub Release
     (optional milestone)" once the doctrine ruling is applied. Do not implement either here. -->
</context>

<requirements>

1. Add a `## Unreleased` section to `CHANGELOG.md` in exactly this position: after the existing preamble (the block ending with the SemVer sentence) and before `## v0.8.1`. The resulting top of the file must read:

   ```markdown
   # Changelog

   All notable changes to this project will be documented in this file.

   The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
   and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

   ## Unreleased

   - fix: a confirmed release now creates the GitHub Release object for its tag in the same run, with the tag as the only input. The `## Review` block records `release_exists`, and an unreachable or rejecting GitHub fails the `Release` check and parks the task at `human_review` instead of closing it. An already-existing release counts as present, so re-running a released task does not fail; a CHANGELOG section that cannot be read is created with an empty body plus a note.

   ## v0.8.1
   ```

2. The bullet must name all four shipped facts: the Release object is created for the confirmed tag, `release_exists` is recorded in the Review block, an unreachable/rejecting GitHub fails closed to `human_review`, and an existing release is success. Use a single `fix:` bullet; do not add a second bullet.

3. Do not change the preamble, the `# Changelog` title, any existing version header, or any existing bullet.

4. Run `make precommit` and confirm exit 0. If it fails, fix the cause in the code or the test from the earlier prompts — do not weaken the changelog or skip the gate.

</requirements>

<constraints>
- **One `## Unreleased` section, in the position above.** Do not place it between `# Changelog` and the preamble, and do not duplicate it.
- **Do not rename or reorder existing version headers.** `## v0.8.1` stays directly below the new section.
- **Do not add an entry for any other change** and do not backfill old releases.
- **Do not hand-edit prompt/spec frontmatter or status.**
- **`make precommit` must pass** (fmt, generate, test, lint, vet, vuln, license).
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make precommit` — exit 0.
Run `make test` — exit 0.

```bash
grep -n '^## Unreleased' CHANGELOG.md          # exactly one line
grep -n '^## ' CHANGELOG.md | head -1          # the same line number as the previous command
grep -c '^## v0\.8\.1' CHANGELOG.md            # 1 — the newest version header is still present
grep -ci 'release object' CHANGELOG.md         # >= 1 — the entry names the artifact
grep -c 'release_exists' CHANGELOG.md          # >= 1 — the entry names the new check
```
</verification>

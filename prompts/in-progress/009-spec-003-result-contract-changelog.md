---
status: approved
spec: [003-bug-releaser-claims-released-without-remote-tag]
created: "2026-09-13T19:55:00Z"
queued: "2026-09-13T17:55:23Z"
---

# Record the remote-verified release outcome in the changelog and correct the Result contract comment

<summary>
- The repository gets a changelog entry for the fix, in the section the release bot rewrites at the next release.
- The entry names what changed for a reader: a release is recorded as successful only once the remote confirms the tag.
- The documented meaning of the success outcome is corrected so the contract says what the code now does — the comment that called it "direct-push succeeded" no longer misleads the next reader.
- Nothing else about the result contract changes: it still has exactly two shapes and the same JSON fields, so existing consumers are unaffected.
- The verification is the full precommit gate plus three greps that prove the changelog section sits in the right place.
- This is the last of three prompts for spec 003; it lands after the two code changes it documents.
</summary>

<objective>
Close the documentation half of spec 003: add the `## Unreleased` entry that records the remote-verified result reconciliation, and correct the `## Result` contract comment in `pkg/result_output.go` so the documented meaning of `outcome: released` matches the behaviour the two preceding prompts shipped. Leaving the comment stale would re-create the documented-meaning drift this spec exists to remove.
</objective>

<context>
Read `CLAUDE.md` (Releasing section), `docs/dod.md` (Documentation section — it fixes the required position of `## Unreleased`), and `docs/releasing-github-releaser-agent.md` (this repo is self-released by the bot: the next release renames `## Unreleased`, so the entry must be a bullet under that exact heading and nothing else may be renamed).

Read these files BEFORE editing:
- `CHANGELOG.md` — the preamble ends after the SemVer lines; the newest section header today is `## v0.4.10`, and there is no `## Unreleased` section yet. Note the existing bullet style (`- fix: ...`, `- chore: ...`).
- `pkg/result_output.go` — the `ResultOutput` doc comment with its two-shape contract (`Outcome="released"` … `Outcome="failed"` …) and the `ResultOutcomeReleased` / `ResultOutcomeFailed` constants. This prompt changes comments only — never the struct, its JSON tags, or the constants.
- `pkg/steps_ai_review.go` (as changed by the preceding prompt) and `pkg/steps_execution.go`'s `postCheck` — the behaviour the new comment must describe accurately.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — changelog entry shape and conventional bullet prefixes.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`.
</context>

<requirements>

## 1. Create the `## Unreleased` section in `CHANGELOG.md`

Insert a new section directly below the preamble block and above the newest `## vX.Y.Z` header, so the file order reads: `# Changelog` → preamble → `## Unreleased` → `## v0.4.10` → the rest. Never between the `# Changelog` title and the preamble.

The section carries exactly one bullet, in the repo's existing style, starting with `- fix:` and naming the remote-verified result reconciliation. The bullet must contain the word "remote" and must state the reader-visible behaviour change, e.g. that a release task's `## Result` records `outcome: released` only after the remote confirms the planned tag at the released commit, and that a rejected push whose tag never lands, a review rejection that never pushed, or an unverifiable remote leaves a non-success outcome naming the failure. Mentioning the short-vs-full commit comparison in the same bullet is allowed; a second bullet is not required.

Leave every existing `## vX.Y.Z` section byte-identical.

## 2. Correct the contract comment in `pkg/result_output.go`

The `ResultOutput` doc comment currently states:

```
// Two shapes are valid:
//   - Outcome="released" — direct-push succeeded; CommitSHA + Tag populated; ErrorCategory empty
//   - Outcome="failed"   — any failure; ErrorCategory + Error populated; CommitSHA + Tag empty
```

Rewrite it so both lines describe what the code now does, and so the file contains the word "remote" (today it contains none):

- the `released` line must say the remote confirms the planned tag at the released commit (the remote is the authority, not the local push call), and keep the field facts (`CommitSHA` + `Tag` populated, `ErrorCategory` empty)
- the `failed` line must stay the failure shape (`ErrorCategory` + `Error` populated, `CommitSHA` + `Tag` empty) and must cover the new sources of that shape: a rejected push whose tag never lands, a review rejection without a push, a tag the remote does not carry at the expected commit, and a remote that could not be verified
- the existing "Two shapes are valid" statement and the "Future fields require a spec amendment" line stay true and must be preserved

Change comments only. Do not touch the struct, its JSON tags, the `json:"...,omitempty"` markers, or the `ResultOutcomeReleased` / `ResultOutcomeFailed` / `ResultPathDirectPush` constants.

## 3. Self-check before finishing

Re-run `<verification>` and confirm it passes. Then confirm, by reading the files: `## Unreleased` is the first `## ` header in `CHANGELOG.md` and sits above `## v0.4.10`; no `## vX.Y.Z` section was modified (`grep -c '^## v0.4' CHANGELOG.md` still prints 11, the count before your edit); `pkg/result_output.go` still declares exactly the same fields and constants as before the edit; and every earlier `## Unreleased` entry in the file (there is none today) would have been preserved had one existed.
</requirements>

<constraints>
- **No new outcome value and no new fields.** The `## Result` contract keeps exactly two shapes and the same JSON field set; this prompt documents the behaviour, it does not extend the contract.
- **Do not rename `## Unreleased` and do not create a version header.** The maintainer bot renames the section at release time; hand-renaming it or adding `## vX.Y.Z` races the bot (`docs/releasing-github-releaser-agent.md`).
- **Do not touch any other file.** No source, test, Makefile, or doc change beyond `CHANGELOG.md` and the `pkg/result_output.go` comment.
- **Existing tests keep passing** (Ginkgo v2 / Gomega, Counterfeiter fakes) — this prompt changes no behaviour.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).

```bash
grep -n '^## Unreleased' CHANGELOG.md          # exactly one line
grep -n '^## ' CHANGELOG.md | head -1          # prints the SAME line number as above
grep -n '^## Unreleased' -A 3 CHANGELOG.md | grep -ci 'remote'   # >= 1 (0 before this change; the unanchored form already returns 1 pre-fix, so anchor it)
grep -n 'remote' pkg/result_output.go          # >= 1 (zero before this change)
grep -c 'direct-push succeeded' pkg/result_output.go   # 0 (1 before this change)
grep -n 'ResultOutcomeReleased\|ResultOutcomeFailed' pkg/result_output.go   # the constants are unchanged
grep -n '^## v0.4.10' CHANGELOG.md             # the newest existing section is still present
grep -c '^## v0.4' CHANGELOG.md                # 11 — the pre-edit count, unchanged (no version section added, renamed or removed)
```
</verification>

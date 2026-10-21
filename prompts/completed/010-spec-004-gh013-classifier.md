---
status: completed
spec: [004-bug-releaser-publish-blocked-by-branch-protection]
summary: Added a GH013 fragment to classifierTable mapping repository-rule rejections to ErrorCategoryProtectedBranchRejected, plus three table rows covering the bare code, the verbatim prod sample, and ordering before non-fast-forward.
execution_id: github-releaser-agent-exec-010-spec-004-gh013-classifier
dark-factory-version: v0.198.7
created: "2026-10-21T16:42:00Z"
queued: "2026-10-21T16:54:45Z"
started: "2026-10-21T16:54:47Z"
completed: "2026-10-21T16:57:34Z"
branch: dark-factory/bug-releaser-publish-blocked-by-branch-protection
---

# Classify a bare `GH013` push rejection as `protected_branch_rejected`

<summary>
- A push rejected by a GitHub repository rule is now recognised by the rejection code alone, without needing the secondary "required status checks" wording.
- The recorded prod sample already classified correctly by luck; a message that carries only `GH013` and "Changes must be made through a pull request" now classifies correctly by design.
- The protected-branch fragment wins even when a server response also carries a generic push token such as `non-fast-forward`, so the PR fallback in the next prompt always sees this category.
- Three new table rows pin the behaviour: a bare-`GH013` sample, a verbatim prod sample, and an ordering guard that keeps the branch-protection category ahead of a generic push token.
- No enum value, no other category, and no production routing changes in this prompt.
- This is the first of three prompts for spec 004; it is a self-contained classifier change.
</summary>

<objective>
Make `git.ClassifyError` map a `GH013` stderr fragment to the existing `ErrorCategoryProtectedBranchRejected` category. Today the recorded prod sample classifies correctly only because it also contains "required status checks"; a GH013 message without that token falls through to `unknown`, which would send the next prompt's PR fallback down the generic failure path.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read these files BEFORE changing anything:
- `pkg/git/error_classifier.go` — the closed `ErrorCategory` enum, the `classifierEntry` struct, the ordered `classifierTable` (protected-branch fragments first, then non-fast-forward), and `ClassifyError`. The category `ErrorCategoryProtectedBranchRejected` already exists with the value `"protected_branch_rejected"`; do NOT add a new enum value.
- `pkg/git/error_classifier_test.go` — the `DescribeTable("maps git stderr fragments to the closed enum", ...)` block and its existing protected-branch entries (`GH006`, `required status checks`). New rows go in this table.
- `specs/in-progress/004-bug-releaser-publish-blocked-by-branch-protection.md` § Reproduction — the verbatim prod stderr sample to copy into the test.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>

## 1. Add the `GH013` fragment to `classifierTable`

In `pkg/git/error_classifier.go`, add one entry to the protected-branch group of `classifierTable`:

```go
// GH013 is GitHub's repository-rules rejection (branch protection /
// ruleset), e.g. "GH013: Repository rule violations found for
// refs/heads/master" followed by "Changes must be made through a pull
// request". Spec 004 — the push path falls back to a pull request on
// this category.
{Fragment: "GH013", Category: ErrorCategoryProtectedBranchRejected},
```

Placement rules:
- It belongs in the protected-branch group, alongside `protected branch`, `GH006`, `Required reviews`, and `required status checks`.
- It MUST appear before the `non-fast-forward` entries. The table is scanned in order and `ClassifyError` returns the first fragment match; a server response that carries both `GH013` and a generic push token must resolve to `ErrorCategoryProtectedBranchRejected`.
- Do not change the order or content of any existing entry. Do not add an enum constant. Do not touch `ClassifyError` itself.

Update the `classifierTable` doc comment's order rationale only if needed to keep it accurate — the existing sentence "Protected-branch fragments scanned BEFORE generic push fragments" already covers the requirement; a one-line mention of `GH013` as the repository-rules code is sufficient.

## 2. Add table rows to `pkg/git/error_classifier_test.go`

Add entries to the existing `DescribeTable` (do not create a new test suite). Three rows are required:

1. **Bare `GH013`, no other classifier token** — the regression this prompt exists for. The stderr must contain `GH013` and `Changes must be made through a pull request`, and must NOT contain `required status checks`, `protected branch`, `GH006`, `Required reviews`, or `non-fast-forward`. Expected: `git.ErrorCategoryProtectedBranchRejected`.

2. **Verbatim prod sample from the spec** — copy the reproduction block (the `remote: error: GH013: Repository rule violations found for refs/heads/master.` … `! [remote rejected] HEAD -> master (push declined due to repository rule violations)` … `error: failed to push some refs` text) into an `errors.New(...)` argument. Expected: `git.ErrorCategoryProtectedBranchRejected`. This sample also carries `3 of 3 required status checks are expected`; the row documents that the full real message classifies correctly.

3. **Ordering guard** — a message containing both `GH013` and `non-fast-forward` must return `ErrorCategoryProtectedBranchRejected`, not `ErrorCategoryPushNonFastForward`. Build it as e.g. `errors.New("remote: error: GH013: Repository rule violations found\n! [rejected] master -> master (non-fast-forward)")`.

Keep every existing entry and the existing non-`DescribeTable` `It` blocks unchanged.

## 3. Self-check

After the change, confirm by reading the file that `GH013` appears in both the production table and the test table, and that no other fragment changed. Run the verification below.
</requirements>

<constraints>
- **No new error-category enum value.** `ErrorCategoryProtectedBranchRejected` already exists; this prompt only adds a fragment that maps to it.
- **Ordering is load-bearing.** `GH013` must be scanned before the non-fast-forward fragments.
- **Classifier only.** Do not edit `pkg/steps_ai_review.go`, `pkg/result_output.go`, or any other production file in this prompt — the PR fallback is the next prompt.
- **No `CHANGELOG.md` / `README.md` / `docs/` edits in this prompt.** The `## Unreleased` bullet, the README § How it works update, and the `docs/dod.md` § Git-Write Safety update are owned by the final prompt of this set. Do not create `## Unreleased` here — doing so makes that prompt's "the section does not exist yet" premise false and risks a duplicate bullet.
- **Existing tests keep passing** (Ginkgo v2 / Gomega). Do not rename or delete existing table entries.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `ROOTDIR=/workspace make test` — exit 0.

```bash
grep -n 'GH013' pkg/git/error_classifier.go        # >= 1 (0 before this change)
grep -n 'GH013' pkg/git/error_classifier_test.go   # >= 2 (bare sample + verbatim sample)
go test -mod=mod ./pkg/git/...                     # PASS
```

Then run `ROOTDIR=/workspace make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).
</verification>

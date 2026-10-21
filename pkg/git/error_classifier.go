// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import "strings"

// ErrorCategory is the closed enum returned by ClassifyError. Production
// code (the execution step) branches on these values to write
// ## Result.error_category and to drive retry policy in the controller.
//
// The set is CLOSED — adding a new category requires a spec amendment.
type ErrorCategory string

const (
	// ErrorCategoryAuth — git server rejected credentials (401/403, missing username).
	ErrorCategoryAuth ErrorCategory = "auth"
	// ErrorCategoryRepoNotFound — clone target does not exist on the server (404).
	// NOTE: GitHub returns "Repository not found" for BOTH a typo'd repo URL
	// AND an unauthenticated request for a private repo. pr-reviewer
	// intentionally classifies this as auth; github-releaser treats it as
	// repo_not_found because the watcher allowlist + IAT auth eliminate the
	// private-repo confounder upstream. If watcher emits it, the repo is
	// public — a 404 truly means it does not exist.
	ErrorCategoryRepoNotFound ErrorCategory = "repo_not_found"
	// ErrorCategoryChangelogMissing — CHANGELOG.md is absent in the cloned repo.
	// Detected at the filesystem layer (os.ReadFile ENOENT), not via git stderr.
	ErrorCategoryChangelogMissing ErrorCategory = "changelog_missing"
	// ErrorCategoryUnreleasedNotFound — RewriteUnreleasedHeader could not find
	// the "## Unreleased" line. Detected via changelog package error, not git stderr.
	ErrorCategoryUnreleasedNotFound ErrorCategory = "unreleased_not_found"
	// ErrorCategoryTagCollision — annotated tag already exists on the remote.
	ErrorCategoryTagCollision ErrorCategory = "tag_collision"
	// ErrorCategoryProtectedBranchRejected — branch protection rejected the push.
	// Consumed by the PR-fallback spec (separate).
	ErrorCategoryProtectedBranchRejected ErrorCategory = "protected_branch_rejected"
	// ErrorCategoryUnexpectedDiff — the release commit touched files other than
	// CHANGELOG.md. Pre-push guard fails closed: nothing is tagged or pushed.
	// Defense-in-depth on the direct-push (a release must only rewrite the
	// changelog header).
	//
	// TWO-LAYER CLASSIFICATION: this category is set DIRECTLY by the execution
	// step (steps_execution.go guardCommittedFiles), NOT by ClassifyError.
	// ClassifyError maps git *stderr* onto categories; unexpected_diff is a
	// *semantic* assertion on the committed file set (git diff-tree succeeded,
	// the output was just wrong), so there is no stderr fragment to match. Same
	// split as changelog_missing / unreleased_not_found, which are set at the
	// filesystem / changelog-package layer. Do NOT add a classifierTable entry
	// for it — it would never fire (no matching stderr) and would imply the
	// wrong layer owns the check.
	ErrorCategoryUnexpectedDiff ErrorCategory = "unexpected_diff"
	// ErrorCategoryPluginManifestInvalid — plugin manifest (.claude-plugin/plugin.json or
	// .claude-plugin/marketplace.json) is malformed (JSON parse error inside the version-locator
	// scan) or its version field is absent or not a quoted semver-shaped string. Detected at
	// the plugin manifest package layer (manifest.go bump operation), not git stderr.
	//
	// TWO-LAYER CLASSIFICATION: this category is set DIRECTLY by the execution step
	// (steps_execution.go), NOT by ClassifyError. ClassifyError maps git *stderr* onto
	// categories; plugin_manifest_invalid is a *semantic* assertion on the manifest content
	// (the bump operation failed, git never ran), so there is no stderr fragment to match.
	// Same split as changelog_missing / unreleased_not_found / unexpected_diff, which are
	// set at the filesystem / changelog-package / guard layer respectively. Do NOT add
	// a classifierTable entry for it — it would never fire (no matching stderr) and would
	// imply the wrong layer owns the check.
	ErrorCategoryPluginManifestInvalid ErrorCategory = "plugin_manifest_invalid"
	// ErrorCategoryPushNonFastForward — remote moved between clone and push;
	// controller retry will re-fetch.
	ErrorCategoryPushNonFastForward ErrorCategory = "push_non_fast_forward"
	// ErrorCategoryUnknown — message does not match any known fragment. Bug
	// signal: if this fires repeatedly, add a new substring to the table.
	ErrorCategoryUnknown ErrorCategory = "unknown"
)

// classifierEntry maps a substring fragment to a category. Order matters:
// more-specific fragments must come first (protected-branch tokens before
// generic push errors).
type classifierEntry struct {
	Fragment string
	Category ErrorCategory
}

// classifierTable is the canonical substring→category mapping. Distinct
// fragment per category — adding entries requires a spec amendment.
//
// Order rationale:
//   - Protected-branch fragments scanned BEFORE generic push fragments
//     ("non-fast-forward") because GitHub's GH006 message can include
//     both "Protected branch" and "non-fast-forward" tokens in some
//     server responses.
//   - tag-collision fragments before auth/repo because tag failures
//     are short-circuited at the tag step, but defensive ordering
//     remains.
var classifierTable = []classifierEntry{
	// Protected-branch fragments (push step).
	{Fragment: "protected branch", Category: ErrorCategoryProtectedBranchRejected},
	{Fragment: "GH006", Category: ErrorCategoryProtectedBranchRejected},
	{Fragment: "Required reviews", Category: ErrorCategoryProtectedBranchRejected},
	{Fragment: "required status checks", Category: ErrorCategoryProtectedBranchRejected},
	// Non-fast-forward (push step).
	{Fragment: "non-fast-forward", Category: ErrorCategoryPushNonFastForward},
	{
		Fragment: "Updates were rejected because the remote contains work",
		Category: ErrorCategoryPushNonFastForward,
	},
	// Tag collision (tag step).
	{Fragment: "already exists", Category: ErrorCategoryTagCollision},
	// Repo not found (clone step).
	{Fragment: "Repository not found", Category: ErrorCategoryRepoNotFound},
	{Fragment: "returned error: 404", Category: ErrorCategoryRepoNotFound},
	// Auth (clone step).
	{Fragment: "Authentication failed", Category: ErrorCategoryAuth},
	{Fragment: "could not read Username", Category: ErrorCategoryAuth},
	{Fragment: "returned error: 403", Category: ErrorCategoryAuth},
	{Fragment: "returned error: 401", Category: ErrorCategoryAuth},
}

// ClassifyError maps a git stderr-wrapped error to the closed enum.
//
// Returns the empty-string sentinel `ErrorCategory("")` when err is nil —
// this distinguishes "no error to classify, this was a success" from
// ErrorCategoryUnknown ("an error occurred but no fragment matched"). The
// execution step branches on `category != ""` to decide whether to write
// a failure result section. Mapping nil to "unknown" instead would let a
// missing nil-check silently emit `error_category: unknown` in a
// successful `## Result` — a real bug we want to surface.
//
// changelog_missing and unreleased_not_found are NEVER returned by this
// function — those categories are set by the execution step at the
// filesystem / changelog-package layer, not at the git-stderr layer.
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return ErrorCategory("")
	}
	msg := err.Error()
	for _, entry := range classifierTable {
		if strings.Contains(msg, entry.Fragment) {
			return entry.Category
		}
	}
	return ErrorCategoryUnknown
}

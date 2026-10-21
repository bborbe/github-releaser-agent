// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "github.com/bborbe/github-releaser-agent/pkg/git"

// ResultOutput is the typed contract for the `## Result` JSON section the
// execution step writes for every release task. Round-trips with
// agentlib.MarshalSectionTyped + agentlib.ExtractSection[ResultOutput].
//
// Three shapes are valid:
//   - Outcome="released" — the remote confirms the planned tag at the released
//     commit; CommitSHA + Tag populated; ErrorCategory empty. The remote is the
//     authority, not the local push call: a push that reported success is only
//     `released` once the tag is observed there.
//   - Outcome="failed"   — any failure; ErrorCategory + Error populated;
//     CommitSHA + Tag empty. Covers a rejected push whose tag never lands on the
//     remote, a review rejection that never pushed, a tag the remote does not
//     carry at the expected commit, and a remote that could not be verified.
//   - Outcome="failed", Path="pr-merge" — branch protection rejected the direct
//     push; the release commit was pushed to release/<tag> and a pull request was
//     opened. PullRequestURL names it and ErrorCategory is
//     `protected_branch_rejected`. CommitSHA + Tag stay empty because the default
//     branch does not carry the commit.
//
// Future fields require a spec amendment.
type ResultOutput struct {
	Outcome       string            `json:"outcome"`
	Path          string            `json:"path"`
	CommitSHA     string            `json:"commit_sha,omitempty"`
	Tag           string            `json:"tag,omitempty"`
	ErrorCategory git.ErrorCategory `json:"error_category,omitempty"`
	Error         string            `json:"error,omitempty"`

	// Workdir is the absolute path of the local clone created by execution.
	// ai-review reads CHANGELOG.md and runs `git log -1 --name-only` against
	// this path. Empty on the failure path (no clone survives).
	Workdir string `json:"workdir,omitempty"`

	// LocalTag is the annotated tag created in the local clone. ai-review
	// checks the tag exists and points at CommitSHA before pushing. Empty
	// on the failure path.
	LocalTag string `json:"local_tag,omitempty"`

	// PullRequestURL is the html_url of the pull request opened when a
	// branch-protection rejection forced the release through the PR path.
	// Empty on every other path.
	PullRequestURL string `json:"pull_request_url,omitempty"`
}

// Outcome values for ResultOutput.Outcome.
const (
	ResultOutcomeReleased = "released"
	ResultOutcomeFailed   = "failed"
)

// Path values for ResultOutput.Path.
const (
	// ResultPathDirectPush marks the direct push to the default branch.
	ResultPathDirectPush = "direct-push"

	// ResultPathPRMerge marks the protected-branch fallback: the release commit
	// was pushed to release/<tag> and a pull request was opened for a human to
	// merge. The outcome is still "failed" — the default branch does not carry
	// the commit.
	ResultPathPRMerge = "pr-merge"
)

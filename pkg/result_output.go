// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "github.com/bborbe/github-releaser-agent/pkg/git"

// ResultOutput is the typed contract for the `## Result` JSON section the
// execution step writes for every release task. Round-trips with
// agentlib.MarshalSectionTyped + agentlib.ExtractSection[ResultOutput].
//
// Two shapes are valid:
//   - Outcome="released" — direct-push succeeded; CommitSHA + Tag populated; ErrorCategory empty
//   - Outcome="failed"   — any failure; ErrorCategory + Error populated; CommitSHA + Tag empty
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
}

// Outcome values for ResultOutput.Outcome.
const (
	ResultOutcomeReleased = "released"
	ResultOutcomeFailed   = "failed"
)

// Path values for ResultOutput.Path. Only one value today; the PR-fallback
// spec will add a second (`"pr-merge"`).
const ResultPathDirectPush = "direct-push"

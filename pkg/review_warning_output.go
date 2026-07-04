// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// ReviewWarningOutput is the typed contract for the `## Review Warning`
// JSON section the ai_review step writes when a review rejection
// coincides with a confirmed remote tag at the agent's expected SHA
// (or at a different SHA, indicating a later release won the slot).
// The section preserves the review verdict durably on the task body
// for the operator audit trail, while the task closes as `completed`
// (the post-check's `released` / `superseded` verdict).
//
// Fields:
//   - FailedChecks:    the names of the review checks that failed
//     (from ReviewOutput.FailedChecks, e.g. "Faithfulness")
//   - PlannedVersion:  the version the agent was attempting to release
//     (e.g. "v1.2.8")
//   - ObservedRemoteSHA: the SHA the remote shows for the planned tag
//   - Note:            a one-line human-readable summary
type ReviewWarningOutput struct {
	FailedChecks      []string `json:"failed_checks"`
	PlannedVersion    string   `json:"planned_version"`
	ObservedRemoteSHA string   `json:"observed_remote_sha"`
	Note              string   `json:"note,omitempty"`
}

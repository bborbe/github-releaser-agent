// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// ResolutionOutput is the typed contract for the `## Resolution` JSON
// section the post-check helper appends when it upgrades a verdict from
// local-failed or local-succeeded to remote-confirmed (released or
// superseded). Round-trips with agentlib.MarshalSectionTyped +
// agentlib.ExtractSection[ResolutionOutput].
//
// Two shapes are valid:
//   - Verdict="released"     — remote shows the planned tag at the SHA
//     the agent just produced
//   - Verdict="superseded"   — remote shows the planned tag at a
//     different SHA; a later release won the
//     slot
//
// Both shapes populate PlannedVersion (the bare semver tag, e.g.
// "v1.2.8") and ObservedRemoteSHA (the dereferenced commit SHA git
// ls-remote returned for refs/tags/<tag>). Future fields require a
// spec amendment.
type ResolutionOutput struct {
	Verdict           string `json:"verdict"`
	PlannedVersion    string `json:"planned_version"`
	ObservedRemoteSHA string `json:"observed_remote_sha"`
}

// Verdict values for ResolutionOutput.Verdict. Exhaustive: every
// resolution written by the post-check helper carries one of these two
// strings. A future verdict (e.g. "force-overwritten") requires a
// spec amendment that updates this constant set AND the
// postCheck helper's switch.
const (
	// ResolutionVerdictReleased is the verdict when the remote's
	// refs/tags/<tag> resolves to the SHA the execution step just
	// produced. The release is recorded as completed.
	ResolutionVerdictReleased = "released"

	// ResolutionVerdictSuperseded is the verdict when the remote's
	// refs/tags/<tag> resolves to a SHA DIFFERENT from the one the
	// execution step just produced. A later release won the slot.
	ResolutionVerdictSuperseded = "superseded"
)

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "time"

// SetImageWaitForTest shrinks the image-check wait budget and poll interval on
// an *aiReviewStep, so the external _test package can drive the retry loop
// without sleeping for the production ten-minute budget. A non-aiReviewStep is
// left untouched, which keeps the helper safe to call on any agentlib.Step.
func SetImageWaitForTest(step any, budget, interval time.Duration) {
	s, ok := step.(*aiReviewStep)
	if !ok {
		return
	}
	s.imageWaitBudget = budget
	s.imagePollInterval = interval
}

// ParseOwnerRepoForTest exposes the unexported parseOwnerRepo helper so
// the external _test package can exercise it directly.
var ParseOwnerRepoForTest = parseOwnerRepo

// ClassifyValidationFailureForTest exposes the unexported
// classifyValidationFailure helper for direct testing of its branches.
var ClassifyValidationFailureForTest = classifyValidationFailure

// IsSubsetIncludingChangelogForTest exposes the unexported
// isSubsetIncludingChangelog helper so the external _test package can
// exercise it directly.
var IsSubsetIncludingChangelogForTest = isSubsetIncludingChangelog

// DeriveUnprefixedVersionForTest exposes the unexported deriveUnprefixedVersion
// helper so the external _test package can exercise it directly.
var DeriveUnprefixedVersionForTest = deriveUnprefixedVersion

// NormalizeCloneURLToHTTPSForTest exposes the unexported
// normalizeCloneURLToHTTPS helper so the external _test package can
// exercise the SCP / SSH / HTTPS forms directly.
var NormalizeCloneURLToHTTPSForTest = normalizeCloneURLToHTTPS

// InjectTokenForTest exposes the unexported injectToken helper so the
// external _test package can exercise the token-prefix transformation
// and the empty-token / non-HTTPS passthrough branches directly.
var InjectTokenForTest = injectToken

// SameCommitForTest exposes the unexported sameCommit helper so the
// external _test package can exercise the four match bounds directly.
var SameCommitForTest = sameCommit

// ReleaseTagVerdictForTest exposes the unexported releaseTagVerdict helper
// so the external _test package can assert the exact verdict token emitted
// on each branch of the release-tag check.
var ReleaseTagVerdictForTest = releaseTagVerdict

// RefContainedInTagForTest exposes the unexported refContainedInTag helper
// so the external _test package can pin the compare-payload contract,
// including the nil-BehindBy case that must never read as containment.
var RefContainedInTagForTest = refContainedInTag

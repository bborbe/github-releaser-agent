// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

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

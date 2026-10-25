// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

var RedactTokenForTest = redactToken

var ParseLsRemoteOutputForTest = parseLsRemoteOutput

// CmdEnvForTest exposes the git subprocess env allowlist for tests.
func CmdEnvForTest() []string {
	return (&osExecGitOps{}).cmdEnv()
}

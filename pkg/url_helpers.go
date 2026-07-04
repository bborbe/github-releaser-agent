// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "strings"

// normalizeCloneURLToHTTPS converts the common GitHub clone-URL forms to
// canonical HTTPS so the installation-token auth in injectToken always
// applies. The github-releaser authenticates with a GitHub App installation
// token (HTTPS only) and the runtime image has no ssh client, so an SSH
// clone URL can never succeed — it must be rewritten to HTTPS before
// injectToken runs.
//
//	git@github.com:owner/repo.git        → https://github.com/owner/repo.git
//	ssh://git@github.com/owner/repo.git  → https://github.com/owner/repo.git
//	https://github.com/owner/repo.git    → unchanged
//	https://github.com/owner/repo        → unchanged (no .git is fine)
//
// Any form it does not recognize is returned unchanged so the failure
// surfaces loudly downstream rather than being silently mangled.
func normalizeCloneURLToHTTPS(raw string) string {
	const (
		scpPrefix = "git@github.com:"
		sshPrefix = "ssh://git@github.com/"
		httpsBase = "https://github.com/"
	)
	switch {
	case strings.HasPrefix(raw, scpPrefix):
		return httpsBase + strings.TrimPrefix(raw, scpPrefix)
	case strings.HasPrefix(raw, sshPrefix):
		return httpsBase + strings.TrimPrefix(raw, sshPrefix)
	default:
		return raw
	}
}

// injectToken transforms an HTTPS GitHub URL into a token-authenticated form.
// https://github.com/owner/repo.git → https://x-access-token:<token>@github.com/owner/repo.git
// Empty token returns the input unchanged (anonymous; fine for tests).
func injectToken(cloneURL, ghToken string) string {
	if ghToken == "" {
		return cloneURL
	}
	const prefix = "https://"
	if !strings.HasPrefix(cloneURL, prefix) {
		return cloneURL
	}
	return prefix + "x-access-token:" + ghToken + "@" + strings.TrimPrefix(cloneURL, prefix)
}

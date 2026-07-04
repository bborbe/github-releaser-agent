// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// BuildEnv assembles the env map forwarded into the Claude CLI subprocess.
// Only set values are forwarded so the subprocess sees a clean env —
// non-empty strings and `allowMajor=true`. Shared by both the Kafka entry
// point (main.go) and the local-CLI entry point (cmd/run-task/main.go).
//
// allowMajor is the spec 060 per-run opt-in for the major-bump guard.
// When true, the subprocess sees `ALLOW_MAJOR=true` in its env so the
// planning step's guard (which reads the same env) can audit the
// operator's override on the task page.
func BuildEnv(
	ghToken, anthropicBaseURL, anthropicAuthToken, anthropicModel string,
	allowMajor bool,
) map[string]string {
	env := map[string]string{}
	if ghToken != "" {
		env["GH_TOKEN"] = ghToken
	}
	if anthropicBaseURL != "" {
		env["ANTHROPIC_BASE_URL"] = anthropicBaseURL
	}
	if anthropicAuthToken != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = anthropicAuthToken
	}
	if anthropicModel != "" {
		env["ANTHROPIC_MODEL"] = anthropicModel
	}
	if allowMajor {
		env["ALLOW_MAJOR"] = "true"
	}
	return env
}

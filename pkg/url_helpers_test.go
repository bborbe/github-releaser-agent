// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	pkg "github.com/bborbe/github-releaser-agent/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("url_helpers", func() {
	Describe("normalizeCloneURLToHTTPS", func() {
		DescribeTable(
			"rewrites the supported clone-URL forms to canonical HTTPS",
			func(input, want string) {
				Expect(pkg.NormalizeCloneURLToHTTPSForTest(input)).To(Equal(want))
			},
			Entry(
				"SCP form → HTTPS",
				"git@github.com:owner/repo.git",
				"https://github.com/owner/repo.git",
			),
			Entry(
				"SSH URL form → HTTPS",
				"ssh://git@github.com/owner/repo.git",
				"https://github.com/owner/repo.git",
			),
			Entry(
				"already HTTPS with .git → unchanged",
				"https://github.com/owner/repo.git",
				"https://github.com/owner/repo.git",
			),
			Entry(
				"already HTTPS without .git → unchanged",
				"https://github.com/owner/repo",
				"https://github.com/owner/repo",
			),
			Entry(
				"unrecognized form → unchanged (loud failure downstream)",
				"file:///tmp/repo",
				"file:///tmp/repo",
			),
		)
	})

	Describe("injectToken", func() {
		It("prefixes HTTPS URLs with x-access-token:<token>@", func() {
			got := pkg.InjectTokenForTest("https://github.com/owner/repo.git", "tok")
			Expect(got).To(Equal("https://x-access-token:tok@github.com/owner/repo.git"))
		})

		It("returns input unchanged when ghToken is empty (anonymous)", func() {
			got := pkg.InjectTokenForTest("https://github.com/owner/repo.git", "")
			Expect(got).To(Equal("https://github.com/owner/repo.git"))
		})

		It("returns input unchanged when URL is not HTTPS (non-https guard)", func() {
			got := pkg.InjectTokenForTest("http://github.com/owner/repo.git", "tok")
			Expect(got).To(Equal("http://github.com/owner/repo.git"))
		})

		It(
			"returns input unchanged for SSH form (non-https guard; caller must normalize first)",
			func() {
				got := pkg.InjectTokenForTest("git@github.com:owner/repo.git", "tok")
				Expect(got).To(Equal("git@github.com:owner/repo.git"))
			},
		)

		// Precondition: the function assumes the input has no existing
		// userinfo. The bborbe clone_url contract (vault + frontmatter)
		// is always the canonical GitHub HTTPS form (no userinfo). A
		// caller passing a userinfo-authed URL would produce a malformed
		// result; that path is YAGNI for now and explicitly out of
		// scope. This test pins the assumption so a future regression
		// re-opens the question via a failing test.
		It("documents the no-existing-userinfo precondition", func() {
			// With existing userinfo, the function naively re-prefixes
			// — the resulting URL is malformed. The contract is "caller
			// MUST normalize first via normalizeCloneURLToHTTPS, which
			// only emits userinfo-less canonical HTTPS for the bborbe
			// clone-URL shapes we accept."
			got := pkg.InjectTokenForTest("https://user:pass@github.com/owner/repo.git", "tok")
			Expect(got).To(Equal("https://x-access-token:tok@user:pass@github.com/owner/repo.git"))
		})
	})
})

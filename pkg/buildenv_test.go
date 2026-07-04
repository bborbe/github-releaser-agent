// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	pkg "github.com/bborbe/github-releaser-agent/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("BuildEnv", func() {
	DescribeTable(
		"assembles only non-empty values",
		func(
			ghToken, anthropicBaseURL, anthropicAuthToken, anthropicModel string,
			allowMajor bool,
			expected map[string]string,
		) {
			Expect(pkg.BuildEnv(
				ghToken, anthropicBaseURL, anthropicAuthToken, anthropicModel, allowMajor,
			)).To(Equal(expected))
		},
		Entry("all empty -> empty map",
			"", "", "", "", false,
			map[string]string{}),
		Entry("only GH_TOKEN",
			"gh", "", "", "", false,
			map[string]string{"GH_TOKEN": "gh"}),
		Entry("only ANTHROPIC_BASE_URL",
			"", "https://api.example", "", "", false,
			map[string]string{"ANTHROPIC_BASE_URL": "https://api.example"}),
		Entry("only ANTHROPIC_AUTH_TOKEN",
			"", "", "tok", "", false,
			map[string]string{"ANTHROPIC_AUTH_TOKEN": "tok"}),
		Entry("only ANTHROPIC_MODEL",
			"", "", "", "sonnet", false,
			map[string]string{"ANTHROPIC_MODEL": "sonnet"}),
		Entry("allow_major false -> no ALLOW_MAJOR key",
			"", "", "", "", false,
			map[string]string{}),
		Entry("allow_major true -> ALLOW_MAJOR set",
			"", "", "", "", true,
			map[string]string{"ALLOW_MAJOR": "true"}),
		Entry("all four set plus allow_major",
			"gh", "https://api.example", "tok", "sonnet", true,
			map[string]string{
				"GH_TOKEN":             "gh",
				"ANTHROPIC_BASE_URL":   "https://api.example",
				"ANTHROPIC_AUTH_TOKEN": "tok",
				"ANTHROPIC_MODEL":      "sonnet",
				"ALLOW_MAJOR":          "true",
			}),
	)

	It("omits empty values rather than setting them to empty string", func() {
		env := pkg.BuildEnv("gh", "", "", "sonnet", false)
		Expect(env).To(HaveKey("GH_TOKEN"))
		Expect(env).To(HaveKey("ANTHROPIC_MODEL"))
		Expect(env).NotTo(HaveKey("ANTHROPIC_BASE_URL"))
		Expect(env).NotTo(HaveKey("ANTHROPIC_AUTH_TOKEN"))
		Expect(env).NotTo(HaveKey("ALLOW_MAJOR"))
		Expect(env).To(Equal(map[string]string{
			"GH_TOKEN":        "gh",
			"ANTHROPIC_MODEL": "sonnet",
		}))
	})

	It("sets ALLOW_MAJOR only when allowMajor is true", func() {
		env := pkg.BuildEnv("gh", "", "", "sonnet", true)
		Expect(env).To(HaveKeyWithValue("ALLOW_MAJOR", "true"))
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package changelog_test

import (
	"context"

	"github.com/bborbe/github-releaser-agent/pkg/changelog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ValidateUnreleased", func() {
	DescribeTable("ValidateUnreleased",
		func(content []byte, valid bool, reason string, line int) {
			v, r, l := changelog.ValidateUnreleased(content)
			Expect(v).To(Equal(valid))
			Expect(r).To(Equal(reason))
			Expect(l).To(Equal(line))
		},
		Entry("P1 valid - Unreleased first",
			[]byte("# Changelog\n\n## Unreleased\n\n- feat: add foo\n\n## v1.0.0\n\n- initial\n"),
			true, "", 0),
		Entry(
			"P1 fail - Unreleased not first",
			[]byte(
				"# Changelog\n\n\n\n\n\n\n\n\n\n## 1.2.6\n\n- some fix\n\n## Unreleased\n\n- feat: add foo\n",
			),
			false,
			"Unreleased is not the first ## section; found '1.2.6' at line 11. Move ## Unreleased above all release headings.",
			11,
		),
		Entry(
			"no Unreleased section",
			[]byte("# Changelog\n\n## v1.0.0\n\n- initial\n"),
			false,
			"Unreleased is not the first ## section; found 'v1.0.0' at line 3. Move ## Unreleased above all release headings.",
			3,
		),
		Entry(
			"P2 fail - empty Unreleased",
			[]byte(
				"# Changelog\n\n\n\n\n\n\n\n\n\n## Unreleased\n\nNo bullets here.\n\n## v1.0.0\n\n- initial\n",
			),
			false,
			"Unreleased section has no bullet entries.",
			11,
		),
		Entry("trailing whitespace heading tolerated",
			[]byte("# Changelog\n\n## Unreleased   \n\n- feat: add foo\n\n## v1.0.0\n"),
			true, "", 0),
		Entry("nil content returns not found",
			nil,
			false, "Unreleased section not found.", 0),
		Entry("empty content returns not found",
			[]byte{},
			false, "Unreleased section not found.", 0),
		Entry(
			"star bullet not counted",
			[]byte(
				"# Changelog\n\n\n\n\n\n\n\n\n\n## Unreleased\n\n* feat: add foo\n\n## v1.0.0\n",
			),
			false,
			"Unreleased section has no bullet entries.",
			11,
		),
		Entry(
			"plus bullet not counted",
			[]byte(
				"# Changelog\n\n\n\n\n\n\n\n\n\n## Unreleased\n\n+ feat: add foo\n\n## v1.0.0\n",
			),
			false,
			"Unreleased section has no bullet entries.",
			11,
		),
	)
})

var _ = Describe("ExtractUnreleasedBullets", func() {
	DescribeTable("ExtractUnreleasedBullets",
		func(content []byte, expected []string) {
			result := changelog.ExtractUnreleasedBullets(content)
			Expect(result).To(Equal(expected))
		},
		Entry(
			"extracts bullets in order",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n- feat: add foo\n- fix: add bar\n- refactor: add baz\n\n## v1.0.0\n",
			),
			[]string{"feat: add foo", "fix: add bar", "refactor: add baz"},
		),
		Entry("empty Unreleased returns non-nil empty slice",
			[]byte("# Changelog\n\n## Unreleased\n\nNo bullets here.\n\n## v1.0.0\n"),
			[]string{}),
		Entry("absent Unreleased returns nil",
			[]byte("# Changelog\n\n## v1.0.0\n\n- initial\n"),
			nil),
		Entry(
			"only first Unreleased block is parsed",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n- first bullet\n\n## v1.0.0\n\n## Unreleased\n\n- second bullet\n",
			),
			[]string{"first bullet"},
		),
		Entry("nil content returns nil",
			nil,
			nil),
		Entry("empty content returns nil",
			[]byte{},
			nil),
		Entry("bullet with leading whitespace after dash space",
			[]byte("# Changelog\n\n## Unreleased\n\n-  leading space after dash\n\n## v1.0.0\n"),
			[]string{" leading space after dash"}),
	)
})

var _ = Describe("InferHeaderPrefixStyle", func() {
	DescribeTable("InferHeaderPrefixStyle",
		func(content []byte, expected string) {
			result := changelog.InferHeaderPrefixStyle(content)
			Expect(result).To(Equal(expected))
		},
		Entry("v-prefix historic",
			[]byte("# Changelog\n\n## Unreleased\n\n## v1.2.3\n"),
			"v"),
		Entry("no-prefix historic",
			[]byte("# Changelog\n\n## Unreleased\n\n## 1.2.3\n"),
			""),
		Entry("no historic release defaults to v",
			[]byte("# Changelog\n\n## Unreleased\n"),
			"v"),
		Entry("nil content defaults to v",
			nil,
			"v"),
		Entry("empty content defaults to v",
			[]byte{},
			"v"),
		Entry("only Unreleased heading defaults to v",
			[]byte("# Changelog\n\n## Unreleased\n\n- feat: add foo\n"),
			"v"),
		Entry("v-prefix with longer version",
			[]byte("# Changelog\n\n## Unreleased\n\n## v10.20.30\n"),
			"v"),
		Entry("no-prefix with longer version",
			[]byte("# Changelog\n\n## Unreleased\n\n## 10.20.30\n"),
			""),
		Entry("malformed heading keeps scanning",
			[]byte("# Changelog\n\n## Unreleased\n\n## v1.2.3\n\n## InvalidHeading\n"),
			"v"),
	)
})

var _ = Describe("RewriteUnreleasedHeader", func() {
	DescribeTable("replaces ## Unreleased line with the given header",
		func(input []byte, newHeader string, expected []byte) {
			got, err := changelog.RewriteUnreleasedHeader(context.Background(), input, newHeader)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(string(expected)))
		},
		Entry(
			"rewrite unreleased — happy path replaces the heading and preserves bullets",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n- feat: add foo\n- fix: bar\n\n## v1.0.0\n\n- initial\n",
			),
			"## v1.0.1",
			[]byte(
				"# Changelog\n\n## v1.0.1\n\n- feat: add foo\n- fix: bar\n\n## v1.0.0\n\n- initial\n",
			),
		),
		Entry("rewrite unreleased — tolerates trailing whitespace on the heading line",
			[]byte("# Changelog\n\n## Unreleased   \n\n- feat: bar\n\n## v0.9.0\n\n- old\n"),
			"## v0.9.1",
			[]byte("# Changelog\n\n## v0.9.1\n\n- feat: bar\n\n## v0.9.0\n\n- old\n")),
		Entry("rewrite unreleased — first occurrence only when duplicate ## Unreleased present",
			[]byte("## Unreleased\n\n- a\n\n## Unreleased\n\n- b\n"),
			"## v1.2.8",
			[]byte("## v1.2.8\n\n- a\n\n## Unreleased\n\n- b\n")),
		Entry(
			"rewrite unreleased — empty newHeader replaces the heading with a blank line (current behavior)",
			[]byte("# Changelog\n\n## Unreleased\n\n- feat: add foo\n\n## v1.0.0\n\n- initial\n"),
			"",
			[]byte("# Changelog\n\n\n\n- feat: add foo\n\n## v1.0.0\n\n- initial\n"),
		),
	)

	DescribeTable("returns a wrapped error when ## Unreleased is absent",
		func(input []byte) {
			_, err := changelog.RewriteUnreleasedHeader(context.Background(), input, "## v1.2.3")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unreleased header not found"))
		},
		Entry("rewrite unreleased — error when no Unreleased heading present",
			[]byte("# Changelog\n\n## v1.0.0\n\n- initial\n")),
		Entry("rewrite unreleased — error on empty content",
			[]byte("")),
	)
})

var _ = Describe("ExtractUnreleasedBody", func() {
	DescribeTable(
		"returns verbatim body of ## Unreleased section",
		func(content []byte, expected string) {
			got, err := changelog.ExtractUnreleasedBody(context.Background(), content)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(expected))
		},
		Entry(
			"typical body with bullets is returned verbatim (incl. blank line right after heading)",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n- feat: add foo\n- fix: bar\n\n## v1.0.0\n\n- old\n",
			),
			"\n- feat: add foo\n- fix: bar\n\n",
		),
		Entry("body without trailing blank line before next heading (incl. leading blank)",
			[]byte("## Unreleased\n\n- feat: x\n## v1.0.0\n"),
			"\n- feat: x\n"),
		Entry("body with extra leading blank line is preserved (no trim)",
			[]byte("## Unreleased\n\n\n- feat: x\n\n## v1.0.0\n"),
			"\n\n- feat: x\n\n"),
		Entry("multi-line body with blank lines between bullets is preserved",
			[]byte("## Unreleased\n\n- feat: a\n\n- fix: b\n\n## v1.0.0\n"),
			"\n- feat: a\n\n- fix: b\n\n"),
		Entry("body with trailing whitespace is preserved (line-ending is normalized to \\n)",
			[]byte("## Unreleased\n\n- feat: x   \n\n## v1.0.0\n"),
			"\n- feat: x   \n\n"),
		Entry("## Unreleased immediately followed by next heading returns empty string",
			[]byte("## Unreleased\n## v1.0.0\n\n- old\n"),
			""),
		Entry(
			"## Unreleased with only blank lines before next heading returns just those blank lines",
			[]byte("## Unreleased\n\n\n## v1.0.0\n\n- old\n"),
			"\n\n",
		),
		Entry("## Unreleased at end of file with no body returns empty string",
			[]byte("# Changelog\n\n## Unreleased\n"),
			""),
		Entry("only first Unreleased block body is returned",
			[]byte("## Unreleased\n\n- first\n\n## v1.0.0\n\n## Unreleased\n\n- second\n"),
			"\n- first\n\n"),
	)

	DescribeTable("returns a wrapped error when ## Unreleased is absent",
		func(content []byte) {
			_, err := changelog.ExtractUnreleasedBody(context.Background(), content)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unreleased header not found"))
		},
		Entry("absent Unreleased heading returns error",
			[]byte("# Changelog\n\n## v1.0.0\n\n- initial\n")),
		Entry("nil content returns error",
			nil),
		Entry("empty content returns error",
			[]byte{}),
	)
})

var _ = Describe("ReplaceUnreleasedBody", func() {
	DescribeTable(
		"replaces ## Unreleased body with newBody; preserves text before/after",
		func(input []byte, newBody string, expected []byte) {
			got, err := changelog.ReplaceUnreleasedBody(
				context.Background(),
				input,
				newBody,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(string(expected)))
		},
		Entry(
			"typical replacement preserves text before and after",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n- raw commit line one\n- raw commit line two\n\n## v1.0.0\n\n- initial\n",
			),
			"- feat: cleaned\n",
			[]byte(
				"# Changelog\n\n## Unreleased\n- feat: cleaned\n## v1.0.0\n\n- initial\n",
			),
		),
		Entry(
			"empty new body produces just the heading + blank line + next heading",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n- raw line\n\n## v1.0.0\n\n- initial\n",
			),
			"",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n## v1.0.0\n\n- initial\n",
			),
		),
		Entry(
			"newBody without trailing \\n gets a single \\n appended before the next heading",
			[]byte(
				"# Changelog\n\n## Unreleased\n\n- raw line\n\n## v1.0.0\n\n- initial\n",
			),
			"- feat: cleaned",
			[]byte(
				"# Changelog\n\n## Unreleased\n- feat: cleaned\n## v1.0.0\n\n- initial\n",
			),
		),
		Entry(
			"newBody already ends with \\n is not double-newlined",
			[]byte(
				"## Unreleased\n\n- raw line\n\n## v1.0.0\n",
			),
			"- feat: cleaned\n",
			[]byte(
				"## Unreleased\n- feat: cleaned\n## v1.0.0\n",
			),
		),
		Entry(
			"## Unreleased at end of file with newBody inserts cleanly",
			[]byte("# Changelog\n\n## Unreleased\n"),
			"- feat: cleaned\n",
			[]byte("# Changelog\n\n## Unreleased\n- feat: cleaned\n"),
		),
		Entry(
			"input without trailing newline preserves that property",
			[]byte("## Unreleased\n\n- raw line\n"),
			"- feat: cleaned\n",
			[]byte("## Unreleased\n- feat: cleaned\n"),
		),
		Entry(
			"first occurrence of ## Unreleased is replaced; later duplicate is left alone",
			[]byte(
				"## Unreleased\n\n- a\n\n## v1.0.0\n\n## Unreleased\n\n- b\n",
			),
			"- feat: cleaned\n",
			[]byte(
				"## Unreleased\n- feat: cleaned\n## v1.0.0\n\n## Unreleased\n\n- b\n",
			),
		),
	)

	DescribeTable("returns a wrapped error when ## Unreleased is absent",
		func(input []byte) {
			_, err := changelog.ReplaceUnreleasedBody(
				context.Background(),
				input,
				"- feat: cleaned\n",
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unreleased header not found"))
		},
		Entry("no ## Unreleased heading returns error",
			[]byte("# Changelog\n\n## v1.0.0\n\n- initial\n")),
		Entry("nil content returns error",
			nil),
		Entry("empty content returns error",
			[]byte{}),
	)
})

// lenientCase carries a single CHANGELOG byte slice for the lenient-detection
// DescribeTable below. Each Entry row is one spec acceptance criterion from
// spec 065 and exercises the same lenient "first non-version H2" rule across
// the package's exported functions. The assertions vary per AC, so the body
// dispatches on the fixture to the matching assertion block.
type lenientCase struct {
	content []byte
}

var _ = DescribeTable("lenient unreleased-section detection (spec 065)",
	func(c lenientCase) {
		ctx := context.Background()
		// AC: literal_Unreleased — ValidateUnreleased returns (true, "", 0)
		//     AND ExtractUnreleasedBullets returns the fixture's bullets in order.
		if string(c.content) == "# Changelog\n\n## Unreleased\n\n- feat: x\n\n## v1.2.8\n" {
			v, r, l := changelog.ValidateUnreleased(c.content)
			Expect(v).To(BeTrue())
			Expect(r).To(Equal(""))
			Expect(l).To(Equal(0))
			bullets := changelog.ExtractUnreleasedBullets(c.content)
			Expect(bullets).To(Equal([]string{"feat: x"}))
			return
		}
		// AC: lowercase_unreleased — ValidateUnreleased returns (true, "", 0)
		//     AND ExtractUnreleasedBody returns the fixture body.
		if string(c.content) == "# Changelog\n\n## unreleased\n\n- feat: x\n\n## v1.2.8\n" {
			v, r, l := changelog.ValidateUnreleased(c.content)
			Expect(v).To(BeTrue())
			Expect(r).To(Equal(""))
			Expect(l).To(Equal(0))
			body, err := changelog.ExtractUnreleasedBody(ctx, c.content)
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(ContainSubstring("- feat: x"))
			return
		}
		// AC: extended_Unreleased_changes — ValidateUnreleased returns (true, "", 0)
		//     AND ExtractUnreleasedBullets returns the fixture's bullets in order.
		if string(c.content) == "# Changelog\n\n## Unreleased changes\n\n- feat: x\n\n## v1.2.8\n" {
			v, r, l := changelog.ValidateUnreleased(c.content)
			Expect(v).To(BeTrue())
			Expect(r).To(Equal(""))
			Expect(l).To(Equal(0))
			bullets := changelog.ExtractUnreleasedBullets(c.content)
			Expect(bullets).To(Equal([]string{"feat: x"}))
			return
		}
		// AC: WIP_heading — ValidateUnreleased returns (true, "", 0) AND
		//     ReplaceUnreleasedBody preserves the "## WIP" heading line verbatim
		//     while replacing the body.
		if string(c.content) == "# Changelog\n\n## WIP\n\n- feat: x\n- fix: y\n" {
			v, r, l := changelog.ValidateUnreleased(c.content)
			Expect(v).To(BeTrue())
			Expect(r).To(Equal(""))
			Expect(l).To(Equal(0))
			got, err := changelog.ReplaceUnreleasedBody(ctx, c.content, "- chore: clean\n")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(ContainSubstring("## WIP\n"))
			return
		}
		// AC: Next_heading — ValidateUnreleased returns (true, "", 0) AND
		//     ExtractUnreleasedBullets returns the fixture's bullets in order.
		if string(c.content) == "# Changelog\n\n## Next\n\n- feat: z\n" {
			v, r, l := changelog.ValidateUnreleased(c.content)
			Expect(v).To(BeTrue())
			Expect(r).To(Equal(""))
			Expect(l).To(Equal(0))
			bullets := changelog.ExtractUnreleasedBullets(c.content)
			Expect(bullets).To(Equal([]string{"feat: z"}))
			return
		}
		// AC: version_header_first_no_unreleased — ValidateUnreleased returns
		//     the "is not the first ## section" reason with the version header
		//     text and line number.
		if string(c.content) == "# Changelog\n\n## v0.35.0\n\n- shipped\n" {
			v, r, l := changelog.ValidateUnreleased(c.content)
			Expect(v).To(BeFalse())
			Expect(r).To(Equal(
				"Unreleased is not the first ## section; found 'v0.35.0' at line 3. Move ## Unreleased above all release headings.",
			))
			Expect(l).To(Equal(3))
			return
		}
		// AC: empty_lenient_section — ValidateUnreleased returns the
		//     "no bullet entries" reason at the line of the lenient heading.
		if string(c.content) == "# Changelog\n\n## WIP\n\n## v0.35.0\n\n- shipped\n" {
			v, r, l := changelog.ValidateUnreleased(c.content)
			Expect(v).To(BeFalse())
			Expect(r).To(Equal("Unreleased section has no bullet entries."))
			Expect(l).To(Equal(3))
			return
		}
		// AC: two_non_version_h2s_first_wins — ExtractUnreleasedBullets
		//     returns ONLY the bullets between "## Unreleased" and "## Next"
		//     (the second non-version H2 closes the unreleased scan).
		if string(
			c.content,
		) == "# Changelog\n\n## Unreleased\n\n- a\n- b\n\n## Next\n\n- c\n- d\n\n## v0.35.0\n" {
			bullets := changelog.ExtractUnreleasedBullets(c.content)
			Expect(bullets).To(Equal([]string{"a", "b"}))
			return
		}
		// AC: rewrite_lowercase_to_canonical — RewriteUnreleasedHeader
		//     canonicalizes the lenient input "## unreleased" to "## v0.73.0".
		if string(c.content) == "## unreleased\n\n- feat: x\n" {
			got, err := changelog.RewriteUnreleasedHeader(ctx, c.content, "## v0.73.0")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(HavePrefix("## v0.73.0\n"))
			return
		}
		// AC: extract_section_body_version_exact — ExtractSectionBody with a
		//     version-string heading still returns the version body, NOT the
		//     unreleased body. Confirms the lenient rule did NOT bleed into
		//     the version-heading lookup path.
		if string(c.content) == "## Unreleased\n\n- feat: x\n\n## v1.2.8\n\n- old\n" {
			body, err := changelog.ExtractSectionBody(ctx, c.content, "v1.2.8")
			Expect(err).NotTo(HaveOccurred())
			Expect(body).To(ContainSubstring("- old"))
			return
		}
		// AC: infer_prefix_style_with_lenient_unreleased — InferHeaderPrefixStyle
		//     skips the lenient "## WIP" heading and infers "v" from the first
		//     version header.
		if string(c.content) == "## WIP\n\n- feat: x\n\n## v1.2.8\n" {
			style := changelog.InferHeaderPrefixStyle(c.content)
			Expect(style).To(Equal("v"))
			return
		}
		Fail("unhandled lenientCase: " + string(c.content))
	},
	Entry("literal_Unreleased", lenientCase{
		content: []byte("# Changelog\n\n## Unreleased\n\n- feat: x\n\n## v1.2.8\n"),
	}),
	Entry("lowercase_unreleased", lenientCase{
		content: []byte("# Changelog\n\n## unreleased\n\n- feat: x\n\n## v1.2.8\n"),
	}),
	Entry("extended_Unreleased_changes", lenientCase{
		content: []byte("# Changelog\n\n## Unreleased changes\n\n- feat: x\n\n## v1.2.8\n"),
	}),
	Entry("WIP_heading", lenientCase{
		content: []byte("# Changelog\n\n## WIP\n\n- feat: x\n- fix: y\n"),
	}),
	Entry("Next_heading", lenientCase{
		content: []byte("# Changelog\n\n## Next\n\n- feat: z\n"),
	}),
	Entry("version_header_first_no_unreleased", lenientCase{
		content: []byte("# Changelog\n\n## v0.35.0\n\n- shipped\n"),
	}),
	Entry("empty_lenient_section", lenientCase{
		content: []byte("# Changelog\n\n## WIP\n\n## v0.35.0\n\n- shipped\n"),
	}),
	Entry("two_non_version_h2s_first_wins", lenientCase{
		content: []byte(
			"# Changelog\n\n## Unreleased\n\n- a\n- b\n\n## Next\n\n- c\n- d\n\n## v0.35.0\n",
		),
	}),
	Entry("rewrite_lowercase_to_canonical", lenientCase{
		content: []byte("## unreleased\n\n- feat: x\n"),
	}),
	Entry("extract_section_body_version_exact", lenientCase{
		content: []byte("## Unreleased\n\n- feat: x\n\n## v1.2.8\n\n- old\n"),
	}),
	Entry("infer_prefix_style_with_lenient_unreleased", lenientCase{
		content: []byte("## WIP\n\n- feat: x\n\n## v1.2.8\n"),
	}),
)

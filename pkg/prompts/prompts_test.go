// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package prompts_test

import (
	"context"

	"github.com/bborbe/github-releaser-agent/pkg/prompts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("BumpClassificationPrompt", func() {
	It("returns non-empty string", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).NotTo(BeEmpty())
	})

	It("contains patch | minor | major", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("patch | minor | major"))
	})

	It("contains BREAKING CHANGE", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("BREAKING CHANGE"))
	})

	It("contains feat:", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("feat:"))
	})

	It("contains bump field", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring(`"bump":`))
	})

	It("contains major → minor → patch priority order", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("major → minor → patch"))
	})
})

var _ = Describe("BumpClassificationPrompt pre-1.0 cap (spec 063)", func() {
	It("names pre-1.0 in the rule text", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("pre-1.0"))
	})

	It("names the 0.x prefix pattern", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("0."))
	})

	It("names the v0.x prefix pattern", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("v0."))
	})

	It("states major is forbidden for pre-1.0", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("MUST NOT return `bump: major`"))
	})

	It("states minor is the strongest allowed bump", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("strongest allowed bump is `minor`"))
	})

	It("states reasoning must mention pre-1.0 for audit trail", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("reasoning"))
		Expect(p).To(ContainSubstring("`pre-1.0`"))
	})

	It("preserves the major → minor → patch priority order", func() {
		p := prompts.BumpClassificationPrompt()
		Expect(p).To(ContainSubstring("major → minor → patch"))
	})
})

var _ = DescribeTable("ParseBumpVerdict",
	func(input, wantBump, wantReasoning, wantErrSubstr string) {
		verdict, err := prompts.ParseBumpVerdict(context.Background(), input)
		if wantErrSubstr == "" {
			Expect(err).NotTo(HaveOccurred())
			Expect(verdict.Bump).To(Equal(wantBump))
			Expect(verdict.Reasoning).To(Equal(wantReasoning))
		} else {
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parse bump verdict"))
			Expect(err.Error()).To(ContainSubstring(wantErrSubstr))
			Expect(verdict).To(Equal(prompts.BumpVerdict{}))
		}
	},
	Entry("plain JSON parsed",
		`{"bump":"patch","reasoning":"bug fix only"}`,
		"patch", "bug fix only", ""),
	Entry(
		"fenced JSON block extracted from prose",
		"Here is my verdict:\n\n```json\n{\"bump\":\"minor\",\"reasoning\":\"new feat: foo\"}\n```\n",
		"minor",
		"new feat: foo",
		"",
	),
	Entry("plain JSON with extra fields tolerated",
		`{"bump":"major","reasoning":"removed API","confidence":0.9}`,
		"major", "removed API", ""),
	Entry("empty input errors",
		``,
		"", "", "no JSON found"),
	Entry("invalid bump value errors",
		`{"bump":"giant","reasoning":"x"}`,
		"", "", "invalid bump value"),
	Entry("missing reasoning errors",
		`{"bump":"patch","reasoning":""}`,
		"", "", "missing reasoning"),
	Entry("malformed JSON errors",
		`{"bump": "patch"`,
		"", "", "no JSON found"),
	Entry("prose only no JSON errors",
		`Claude says: the answer is patch but I am not formatting JSON.`,
		"", "", "no JSON found"),
	Entry(
		"pre-1.0 breaking change capped to minor (spec 063)",
		`{"bump":"minor","reasoning":"breaking change capped to minor due to pre-1.0 stream (current_version 0.69.0)"}`,
		"minor",
		"breaking change capped to minor due to pre-1.0 stream (current_version 0.69.0)",
		"",
	),
	Entry(
		"raw invalid JSON escape in reasoning is tolerated",
		`{"bump":"patch","reasoning":"restores the (?<![\w-])personal regex guard"}`,
		"patch",
		`restores the (?<![\w-])personal regex guard`,
		"",
	),
)

var _ = Describe("ChangelogQualityGuide", func() {
	It("returns non-empty string", func() {
		g := prompts.ChangelogQualityGuide()
		Expect(g).NotTo(BeEmpty())
	})

	It("contains the Conventional Prefixes heading", func() {
		g := prompts.ChangelogQualityGuide()
		Expect(g).To(ContainSubstring("Conventional Prefixes"))
	})

	It("contains feat: rule", func() {
		g := prompts.ChangelogQualityGuide()
		Expect(g).To(ContainSubstring("feat:"))
	})

	It("contains fix: rule", func() {
		g := prompts.ChangelogQualityGuide()
		Expect(g).To(ContainSubstring("fix:"))
	})

	It("contains the Anti-Patterns section", func() {
		g := prompts.ChangelogQualityGuide()
		Expect(g).To(ContainSubstring("Anti-Patterns"))
	})
})

var _ = Describe("ChangelogRewritePrompt", func() {
	It("returns non-empty string", func() {
		p := prompts.ChangelogRewritePrompt()
		Expect(p).NotTo(BeEmpty())
	})

	It("mentions cleaning the ## Unreleased section", func() {
		p := prompts.ChangelogRewritePrompt()
		Expect(p).To(ContainSubstring("## Unreleased"))
	})

	It("cites the Changelog Quality Guide", func() {
		p := prompts.ChangelogRewritePrompt()
		Expect(p).To(ContainSubstring("Changelog Quality Guide"))
	})

	It("contains faithfulness constraint", func() {
		p := prompts.ChangelogRewritePrompt()
		Expect(p).To(ContainSubstring("Faithfulness"))
	})

	It("contains JSON output format", func() {
		p := prompts.ChangelogRewritePrompt()
		Expect(p).To(ContainSubstring("rewrite_needed"))
		Expect(p).To(ContainSubstring("rewritten_unreleased"))
		Expect(p).To(ContainSubstring("reasoning"))
	})
})

var _ = DescribeTable("ParseRewriteVerdict",
	func(
		input string,
		wantRewriteNeeded bool,
		wantRewritten string,
		wantReasoning string,
		wantErrSubstr string,
	) {
		verdict, err := prompts.ParseRewriteVerdict(context.Background(), input)
		if wantErrSubstr == "" {
			Expect(err).NotTo(HaveOccurred())
			Expect(verdict.RewriteNeeded).To(Equal(wantRewriteNeeded))
			Expect(verdict.RewrittenUnreleased).To(Equal(wantRewritten))
			Expect(verdict.Reasoning).To(Equal(wantReasoning))
		} else {
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parse rewrite verdict"))
			Expect(err.Error()).To(ContainSubstring(wantErrSubstr))
			Expect(verdict).To(Equal(prompts.RewriteVerdict{}))
		}
	},
	Entry(
		"plain JSON parsed — rewrite_needed=true",
		`{"rewrite_needed":true,"rewritten_unreleased":"- feat: add foo\n","reasoning":"missing prefix"}`,
		true,
		"- feat: add foo\n",
		"missing prefix",
		"",
	),
	Entry(
		"fenced JSON block extracted from prose",
		"Here is my verdict:\n\n```json\n{\"rewrite_needed\":true,\"rewritten_unreleased\":\"- fix: x\\n\",\"reasoning\":\"git log style\"}\n```\n",
		true,
		"- fix: x\n",
		"git log style",
		"",
	),
	Entry(
		"rewrite_needed=false with empty rewritten_unreleased passes",
		`{"rewrite_needed":false,"rewritten_unreleased":"","reasoning":"all bullets already conform"}`,
		false,
		"",
		"all bullets already conform",
		"",
	),
	Entry("rewrite_needed=true with empty rewritten_unreleased errors",
		`{"rewrite_needed":true,"rewritten_unreleased":"","reasoning":"x"}`,
		true, "", "x", "rewritten_unreleased is empty"),
	Entry("rewrite_needed=false with non-empty rewritten_unreleased errors",
		`{"rewrite_needed":false,"rewritten_unreleased":"- feat: x\n","reasoning":"x"}`,
		false, "- feat: x\n", "x", "rewritten_unreleased is non-empty"),
	Entry("missing reasoning errors",
		`{"rewrite_needed":true,"rewritten_unreleased":"- feat: x\n","reasoning":""}`,
		true, "- feat: x\n", "", "missing reasoning"),
	Entry("empty input errors",
		``,
		false, "", "", "no JSON found"),
	Entry("malformed JSON errors with parse rewrite verdict substring",
		`{"rewrite_needed": true`,
		false, "", "", "parse rewrite verdict"),
	Entry("prose only no JSON errors",
		`Claude says: the answer is yes but I am not formatting JSON.`,
		false, "", "", "no JSON found"),
	Entry(
		"plain JSON with extra fields tolerated",
		`{"rewrite_needed":true,"rewritten_unreleased":"- chore: deps\n","reasoning":"bump dump","confidence":0.8}`,
		true,
		"- chore: deps\n",
		"bump dump",
		"",
	),
	Entry(
		"raw invalid JSON escape in rewritten_unreleased is tolerated",
		`{"rewrite_needed":true,"rewritten_unreleased":"- fix: match (?<![\w-])personal\n","reasoning":"kept the guard"}`,
		true,
		`- fix: match (?<![\w-])personal`+"\n",
		"kept the guard",
		"",
	),
)

var _ = Describe("ChangelogFaithfulnessPrompt", func() {
	It("returns non-empty string", func() {
		p := prompts.ChangelogFaithfulnessPrompt()
		Expect(p).NotTo(BeEmpty())
	})

	It("mentions semantic faithfulness", func() {
		p := prompts.ChangelogFaithfulnessPrompt()
		Expect(p).To(ContainSubstring("semantic faithfulness"))
	})

	It("describes silent-drop", func() {
		p := prompts.ChangelogFaithfulnessPrompt()
		Expect(p).To(ContainSubstring("silent-drop"))
	})

	It("describes hallucinated", func() {
		p := prompts.ChangelogFaithfulnessPrompt()
		Expect(p).To(ContainSubstring("hallucinated"))
	})

	It("contains per_entry schema", func() {
		p := prompts.ChangelogFaithfulnessPrompt()
		Expect(p).To(ContainSubstring(`"per_entry"`))
	})

	It("contains extras schema", func() {
		p := prompts.ChangelogFaithfulnessPrompt()
		Expect(p).To(ContainSubstring(`"extras"`))
	})

	It("contains overall schema", func() {
		p := prompts.ChangelogFaithfulnessPrompt()
		Expect(p).To(ContainSubstring(`"overall"`))
	})
})

var _ = DescribeTable(
	"ParseFaithfulnessResponse",
	func(input string, wantOverall string, wantPerEntryLen, wantExtrasLen int, wantErrSubstr string) {
		resp, err := prompts.ParseFaithfulnessResponse(context.Background(), input)
		if wantErrSubstr == "" {
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Overall).To(Equal(wantOverall))
			Expect(resp.PerEntry).To(HaveLen(wantPerEntryLen))
			Expect(resp.Extras).To(HaveLen(wantExtrasLen))
		} else {
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parse faithfulness response"))
			Expect(err.Error()).To(ContainSubstring(wantErrSubstr))
			Expect(resp).To(Equal(prompts.FaithfulnessLLMResponse{}))
		}
	},
	Entry(
		"plain JSON all-present → overall=pass",
		`{"per_entry":[{"entry":"- feat: x","verdict":"present","note":"ok"}],"extras":[],"overall":"pass"}`,
		"pass",
		1,
		0,
		"",
	),
	Entry(
		"fenced JSON with one silent-drop → overall=fail",
		"Here is the verdict:\n\n```json\n"+
			`{"per_entry":[{"entry":"- fix: y","verdict":"silent-drop","note":"missing"}],"extras":[],"overall":"fail"}`+"\n```\n",
		"fail", 1, 0, "",
	),
	Entry(
		"plain JSON with one extras entry → overall=fail",
		`{"per_entry":[{"entry":"- feat: x","verdict":"present","note":"ok"}],"extras":[{"entry":"- chore: z","verdict":"hallucinated","note":"added"}],"overall":"fail"}`,
		"fail",
		1,
		1,
		"",
	),
	Entry(
		"bad per_entry verdict errors",
		`{"per_entry":[{"entry":"- feat: x","verdict":"maybe","note":"?"}],"extras":[],"overall":"pass"}`,
		"",
		0,
		0,
		"per_entry[0] invalid verdict",
	),
	Entry(
		"bad extras verdict errors",
		`{"per_entry":[],"extras":[{"entry":"- chore: z","verdict":"fictional","note":"?"}],"overall":"pass"}`,
		"",
		0,
		0,
		"extras[0] invalid verdict",
	),
	Entry("missing overall errors",
		`{"per_entry":[],"extras":[],"overall":""}`,
		"", 0, 0, "invalid overall value"),
	Entry("empty input errors",
		``,
		"", 0, 0, "no JSON found"),
	Entry(
		"plain JSON with extra fields tolerated",
		`{"per_entry":[{"entry":"- feat: x","verdict":"present","note":"ok","extra":"junk"}],"extras":[],"overall":"pass","confidence":0.9}`,
		"pass",
		1,
		0,
		"",
	),
	Entry(
		"raw invalid JSON escape in echoed entry parses (backslash-w and backslash-d)",
		`{"per_entry":[{"entry":"- fix: match with (?<![\w-])personal and \d+ digits","verdict":"present","note":"ok"}],"extras":[],"overall":"pass"}`,
		"pass",
		1,
		0,
		"",
	),
)

var _ = Describe("ParseFaithfulnessResponse invalid JSON escapes", func() {
	// A model asked to echo a changelog bullet verbatim may emit a regex such
	// as (?<![\w-]) without JSON-escaping the backslash. encoding/json rejects
	// that outright, which discarded a passing Faithfulness verdict on
	// bborbe/claude-supervisor 2026-10-04 and parked the release at
	// human_review. These two specs are the regression pair: the first fails
	// against the unfixed parser, the second fails against a blanket escaper
	// that doubles every backslash instead of only the invalid ones.
	It("parses an entry carrying invalid escapes and decodes it back verbatim", func() {
		raw := `{"per_entry":[{"entry":"- fix: (?<![\w-])personal and \d+ digits","verdict":"present","note":"ok"}],"extras":[],"overall":"pass"}`

		resp, err := prompts.ParseFaithfulnessResponse(context.Background(), raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Overall).To(Equal("pass"))
		Expect(resp.PerEntry).To(HaveLen(1))
		Expect(resp.PerEntry[0].Verdict).To(Equal("present"))
		Expect(resp.PerEntry[0].Entry).
			To(Equal(`- fix: (?<![\w-])personal and \d+ digits`))
	})

	It("leaves every valid JSON escape decoding unchanged", func() {
		raw := `{"per_entry":[{"entry":"quote \" newline \n tab \t backslash \\ slash \/ unicode ` + "\\" + `u00e9","verdict":"present","note":"ok"}],"extras":[],"overall":"pass"}`

		resp, err := prompts.ParseFaithfulnessResponse(context.Background(), raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(resp.PerEntry).To(HaveLen(1))
		Expect(resp.PerEntry[0].Entry).To(Equal(
			"quote \" newline \n tab \t backslash \\ slash / unicode é",
		))
	})

	// Pins the boundary of the repair, not a defect in it. Backslash-b IS a
	// valid JSON escape (backspace), so it is deliberately NOT doubled: the
	// parser cannot tell a regex word-boundary the model wrote from a
	// backspace the model meant, and doubling every one would corrupt the
	// latter. The consequence is recorded on the task that introduced this
	// repair — a `\b` in an echoed entry decodes to a control character and
	// the recorded entry text is subtly wrong, without failing the parse.
	// This spec exists so a future "fix" cannot silently start doubling it.
	It("does not repair backslash-b, which is a valid JSON escape (backspace)", func() {
		raw := `{"per_entry":[{"entry":"word boundary \b","verdict":"present","note":"ok"}],"extras":[],"overall":"pass"}`

		resp, err := prompts.ParseFaithfulnessResponse(context.Background(), raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(resp.PerEntry).To(HaveLen(1))
		Expect(resp.PerEntry[0].Entry).To(Equal("word boundary \b"))
	})
})

// The scanner's own edge cases, exercised through the exported parser rather
// than by calling escapeInvalidJSONEscapes directly: what matters is the value
// a caller finally reads, not the intermediate string.
var _ = DescribeTable("ParseFaithfulnessResponse scanner edge cases",
	func(entryJSON, wantEntry string, wantErr bool) {
		raw := `{"per_entry":[{"entry":` + entryJSON +
			`,"verdict":"present","note":"ok"}],"extras":[],"overall":"pass"}`

		resp, err := prompts.ParseFaithfulnessResponse(context.Background(), raw)

		if wantErr {
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parse faithfulness response"))
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.PerEntry).To(HaveLen(1))
		Expect(resp.PerEntry[0].Entry).To(Equal(wantEntry))
	},
	Entry("consecutive backslashes decode to one", `"a\\b"`, `a\b`, false),
	Entry("an invalid escape decodes to its literal backslash", `"a\wb"`, `a\wb`, false),
	Entry("a malformed unicode escape is repaired", `"a\uZZZZb"`, `a\uZZZZb`, false),
	Entry("a short unicode escape is repaired", `"a\u12b"`, `a\u12b`, false),
	Entry("a brace-form unicode escape is repaired", `"a\u{1F600}b"`, `a\u{1F600}b`, false),
	Entry("a valid unicode escape decodes", `"a`+"\\"+`u00e9b"`, "aéb", false),
)

var _ = Describe("ParseFaithfulnessResponse trailing backslash", func() {
	// The one input the repair cannot fix: a backslash with nothing after it is
	// left alone, so the document stays malformed and the caller's
	// malformed-input handling still applies. This pins the guard as a
	// deliberate fall-through rather than an accidental silent success.
	It("leaves a document ending in a bare backslash malformed", func() {
		raw := `{"per_entry":[{"entry":"a` + string(rune(0x5c))

		resp, err := prompts.ParseFaithfulnessResponse(context.Background(), raw)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("parse faithfulness response"))
		Expect(resp).To(Equal(prompts.FaithfulnessLLMResponse{}))
	})
})

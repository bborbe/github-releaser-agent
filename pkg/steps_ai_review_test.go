// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	agentlib "github.com/bborbe/agent"
	claudelib "github.com/bborbe/agent/claude"
	"github.com/bborbe/github-releaser-agent/mocks"
	pkg "github.com/bborbe/github-releaser-agent/pkg"
	"github.com/bborbe/github-releaser-agent/pkg/githubreview"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// mustJSON serializes v as a JSON string; panics on error (test helper).
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// splitNonEmptyLines returns the non-blank lines of s.
func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

var _ = Describe("AIReviewStep", func() {

	var (
		fakeClient *mocks.ReviewClient
		fakeRunner *mocks.ClaudeRunnerMock
		fakeOps    *mocks.GitOps
		token      string
		step       agentlib.Step
	)

	BeforeEach(func() {
		fakeClient = &mocks.ReviewClient{}
		fakeRunner = &mocks.ClaudeRunnerMock{}
		fakeOps = &mocks.GitOps{}
		token = "test-token"
		step = pkg.NewAIReviewStep(fakeClient, fakeRunner, fakeOps, token)
	})

	// taskWithResult builds a task markdown with ## Result section.
	// Backticks cannot appear in Go raw string literals, so we build
	// the fenced JSON blocks via string concatenation. The plan embeds
	// original_unreleased + next_version_header, and the result embeds
	// workdir + local_tag so the new ai-review flow (faithfulness LLM
	// + workdir diff check + push) can be exercised end-to-end.
	// workdir is the on-disk workdir; pass the empty string for tests
	// that don't exercise the post-execution flow.
	taskWithResult := func(commitSHA, tag, outcome, workdir string) string {
		const fm = "---\n" +
			"status: in_progress\n" +
			"phase: ai_review\n" +
			"assignee: github-releaser-agent\n" +
			"task_type: github-release\n" +
			"repo: bborbe/example\n" +
			"task_identifier: gh-release-001\n" +
			"---\n\n"
		plan := "## Plan\n\n" +
			"```json\n" +
			`{"outcome":"ready","next_version":"1.0.0","next_version_header":"## v1.0.0","original_unreleased":"- feat: add foo\n"}` + "\n" +
			"```\n\n"
		result := "## Result\n\n" +
			"```json\n" +
			fmt.Sprintf(
				`{"outcome":%q,"path":"direct-push","commit_sha":%q,"tag":%q,"workdir":%q,"local_tag":%q}`,
				outcome,
				commitSHA,
				tag,
				workdir,
				tag,
			) + "\n" +
			"```\n"
		return fm + plan + result
	}

	taskWithFailedResult := func() string {
		const fm = "---\n" +
			"status: in_progress\n" +
			"phase: ai_review\n" +
			"assignee: github-releaser-agent\n" +
			"task_type: github-release\n" +
			"repo: bborbe/example\n" +
			"task_identifier: gh-release-001\n" +
			"---\n\n"
		plan := "## Plan\n\n" +
			"```json\n" +
			`{"outcome":"ready"}` + "\n" +
			"```\n\n"
		result := "## Result\n\n" +
			"```json\n" +
			`{"outcome":"failed","error_category":"unknown","error":"clone failed"}` + "\n" +
			"```\n"
		return fm + plan + result
	}

	taskWithMalformedResult := func() string {
		const fm = "---\n" +
			"status: in_progress\n" +
			"phase: ai_review\n" +
			"assignee: github-releaser-agent\n" +
			"task_type: github-release\n" +
			"repo: bborbe/example\n" +
			"task_identifier: gh-release-001\n" +
			"---\n\n"
		result := "## Result\n\n" +
			"```json\n" +
			`{"outcome": "released", "invalid-json` + "\n" +
			"```\n"
		return fm + result
	}

	taskWithoutResult := func() string {
		return "---\n" +
			"status: in_progress\n" +
			"phase: ai_review\n" +
			"assignee: github-releaser-agent\n" +
			"task_type: github-release\n" +
			"repo: bborbe/example\n" +
			"task_identifier: gh-release-001\n" +
			"---\n\n" +
			"## Plan\n\n" +
			"```json\n" +
			`{"outcome":"ready"}` + "\n" +
			"```\n"
	}

	taskWithoutRepo := func(outcome string) string {
		const fm = "---\n" +
			"status: in_progress\n" +
			"phase: ai_review\n" +
			"assignee: github-releaser-agent\n" +
			"task_type: github-release\n" +
			"task_identifier: gh-release-001\n" +
			"---\n\n"
		plan := "## Plan\n\n" +
			"```json\n" +
			`{"outcome":"ready"}` + "\n" +
			"```\n\n"
		result := "## Result\n\n" +
			"```json\n" +
			fmt.Sprintf(
				`{"outcome":%q,"path":"direct-push","commit_sha":"abc123","tag":"v1.0.0"}`,
				outcome,
			) + "\n" +
			"```\n"
		return fm + plan + result
	}

	// runStep is used for tests where the step is expected to return (result, nil).
	runStep := func(taskMD string) (*agentlib.Result, *agentlib.Markdown) {
		md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
		Expect(err).NotTo(HaveOccurred())
		result, err := step.Run(context.Background(), md)
		Expect(err).NotTo(HaveOccurred())
		return result, md
	}

	// setupReleasedWorkdir prepares the on-disk workdir expected by the
	// new ai-review flow (faithfulness LLM + file-diff check) and
	// pre-configures fakeOps so the unexpected-file check passes.
	// Returns the path. Caller registers DeferCleanup if it needs the
	// directory to outlive Run (otherwise ai-review's own cleanup
	// removes it).
	setupReleasedWorkdir := func(workdir string) string {
		Expect(os.MkdirAll(workdir, 0o750)).To(Succeed())
		changelogContent := []byte(
			"# Changelog\n\n## v1.0.0\n\n- feat: add foo\n\n## Unreleased\n\n- old\n",
		)
		Expect(os.WriteFile(
			filepath.Join(workdir, "CHANGELOG.md"),
			changelogContent,
			0o600,
		)).To(Succeed())
		fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
		return workdir
	}

	// stubFaithfulLLM wires fakeRunner to return a pass verdict
	// declaring the captured original `entry` was preserved. The
	// faithfulness prompt embeds the original_unreleased lines; the
	// stub inspects them so multi-entry plans produce multi-entry
	// verdicts in a single call.
	stubFaithfulLLM := func(originalUnreleased string) {
		lines := splitNonEmptyLines(originalUnreleased)
		perEntry := make([]map[string]string, 0, len(lines))
		for _, line := range lines {
			perEntry = append(perEntry, map[string]string{
				"entry":   line,
				"verdict": pkg.FaithfulnessPresent,
				"note":    "preserved",
			})
		}
		resp := map[string]interface{}{
			"per_entry": perEntry,
			"extras":    []map[string]string{},
			"overall":   pkg.OverallPass,
		}
		fakeRunner.RunReturns(&claudelib.ClaudeResult{Result: mustJSON(resp)}, nil)
	}

	// extractReview calls ExtractSection and returns the pointer result.
	// Fails the test if the section is missing or malformed.
	extractReview := func(md *agentlib.Markdown) *pkg.ReviewOutput {
		review, err := agentlib.ExtractSection[pkg.ReviewOutput](
			context.Background(),
			md,
			"## Review",
		)
		Expect(err).NotTo(HaveOccurred())
		return review
	}

	// extractReviewWarning returns the typed ReviewWarningOutput for
	// the `## Review Warning` block. Returns nil when the section is
	// absent (the override branch did not fire) — callers assert on
	// the typed fields directly.
	extractReviewWarning := func(md *agentlib.Markdown) *pkg.ReviewWarningOutput {
		warning, err := agentlib.ExtractSection[pkg.ReviewWarningOutput](
			context.Background(),
			md,
			"## Review Warning",
		)
		if err != nil {
			return nil
		}
		return warning
	}

	Describe("Name", func() {
		It("returns github-release-ai-review", func() {
			Expect(step.Name()).To(Equal("github-release-ai-review"))
		})
	})

	Describe("ShouldRun", func() {
		It("returns true, nil (always runs, idempotent overwrite)", func() {
			md, err := agentlib.ParseMarkdown(context.Background(), "")
			Expect(err).NotTo(HaveOccurred())
			shouldRun, err := step.ShouldRun(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldRun).To(BeTrue())
		})
	})

	Describe("Run", func() {
		// Default wiring for every Run test: pre-create a tmpdir,
		// write a CHANGELOG.md with the version section, and stub
		// the faithfulness LLM to a pass verdict. Individual tests
		// may override any of these in their own BeforeEach / It.
		var tmpDir string
		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "air-test-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
			setupReleasedWorkdir(tmpDir)
			stubFaithfulLLM("- feat: add foo\n")
		})

		Context("7a. Happy path", func() {
			It("all three checks pass → approved:true, status:done, next_phase:done", func() {
				fakeClient.TagExistsReturns("abc123", nil)
				fakeClient.ResolveTagCommitReturns("abc123", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.0.0\n\n- feat\n\n## Unreleased\n\n- old"),
					nil,
				)

				result, md := runStep(taskWithResult("abc123", "v1.0.0", "released", tmpDir))

				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("done"))

				review := extractReview(md)
				Expect(review.Approved).To(BeTrue())
				Expect(review.Checks.TagExists).To(BeTrue())
				Expect(review.Checks.TagAtExpectedSHA).To(BeTrue())
				Expect(review.Checks.ChangelogHeaderRewritten).To(BeTrue())
				Expect(review.Notes).To(ContainSubstring("passed"))
			})
		})

		Context("7d-bis. Short-vs-full SHA equivalence (regression for prod-1)", func() {
			// Bug observed in prod 2026-06-01: execution step writes
			// Result.CommitSHA via `git rev-parse --short HEAD` (7 chars),
			// GitHub API returns 40 chars. Naive == compare false-positived
			// every release. Fix: bidirectional strings.HasPrefix match.
			It("short stored vs full from API → matches → approved:true", func() {
				short := "dcd3195"
				full := "dcd3195e3cca37862f4e612a7b14c4e00af6b935"
				fakeClient.TagExistsReturns("tag-sha", nil)
				fakeClient.ResolveTagCommitReturns(full, nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v0.9.0\n\n- feat"), nil,
				)

				result, md := runStep(taskWithResult(short, "v0.9.0", "released", tmpDir))

				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("done"))

				review := extractReview(md)
				Expect(review.Approved).To(BeTrue())
				Expect(review.Checks.TagAtExpectedSHA).To(BeTrue())
			})

			It("full stored vs short from API → matches → approved:true", func() {
				short := "dcd3195"
				full := "dcd3195e3cca37862f4e612a7b14c4e00af6b935"
				fakeClient.TagExistsReturns("tag-sha", nil)
				fakeClient.ResolveTagCommitReturns(short, nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v0.9.0\n\n- feat"), nil,
				)

				result, md := runStep(taskWithResult(full, "v0.9.0", "released", tmpDir))

				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("done"))

				review := extractReview(md)
				Expect(review.Approved).To(BeTrue())
				Expect(review.Checks.TagAtExpectedSHA).To(BeTrue())
			})

		})

		Context("7f. CHANGELOG top heading is a version (pass case)", func() {
			It("header rewritten to version → approved:true, status:done", func() {
				fakeClient.TagExistsReturns("abc123", nil)
				fakeClient.ResolveTagCommitReturns("abc123", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.0.0\n\n- feat\n\n## Unreleased\n\n- old"),
					nil,
				)

				result, md := runStep(taskWithResult("abc123", "v1.0.0", "released", tmpDir))

				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				review := extractReview(md)
				Expect(review.Approved).To(BeTrue())
			})
		})

		Context("7g. Short-circuit: Result.outcome != released", func() {
			It("no HTTP calls, approved:true, status:done, next_phase:done", func() {
				result, md := runStep(taskWithFailedResult())

				Expect(fakeClient.TagExistsCallCount()).To(Equal(0))
				Expect(fakeClient.ResolveTagCommitCallCount()).To(Equal(0))
				Expect(fakeClient.FetchChangelogCallCount()).To(Equal(0))

				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("done"))

				review := extractReview(md)
				Expect(review.Approved).To(BeTrue())
				Expect(review.Checks.TagExists).To(BeTrue())
				Expect(review.Checks.TagAtExpectedSHA).To(BeTrue())
				Expect(review.Checks.ChangelogHeaderRewritten).To(BeTrue())
				Expect(review.Notes).To(ContainSubstring("nothing to verify"))
			})
		})

		Context("7h. Malformed ## Result JSON", func() {
			It("step returns wrapped error, no ## Review written", func() {
				md, err := agentlib.ParseMarkdown(context.Background(), taskWithMalformedResult())
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)

				// Malformed JSON → step returns wrapped error (controller retries)
				Expect(result).To(BeNil())
				Expect(err).NotTo(BeNil())
				Expect(err.Error()).To(ContainSubstring("ai_review: extract ## Result section"))

				// No ## Review section written
				review, err := agentlib.ExtractSection[pkg.ReviewOutput](
					context.Background(), md, "## Review")
				Expect(err).To(HaveOccurred())
				Expect(review).To(BeNil())
			})
		})

		Context("7i. Missing ## Result section", func() {
			It("step returns wrapped error, no ## Review", func() {
				md, err := agentlib.ParseMarkdown(context.Background(), taskWithoutResult())
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)

				// Missing section → step returns wrapped error (controller retries)
				Expect(result).To(BeNil())
				Expect(err).NotTo(BeNil())
				Expect(err.Error()).To(ContainSubstring("ai_review: extract ## Result section"))

				// No ## Review section written
				review, err := agentlib.ExtractSection[pkg.ReviewOutput](
					context.Background(), md, "## Review")
				Expect(err).To(HaveOccurred())
				Expect(review).To(BeNil())
			})
		})

		Context("7j. Missing frontmatter repo", func() {
			It("step returns wrapped error mentioning 'read frontmatter repo'", func() {
				md, err := agentlib.ParseMarkdown(context.Background(),
					taskWithoutRepo("released"))
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)

				// Missing repo → step returns error
				Expect(result).To(BeNil())
				Expect(err).NotTo(BeNil())
				Expect(err.Error()).To(ContainSubstring("read frontmatter repo"))
			})
		})

		Context("7l. Step does NOT write ## Failure section", func() {
			It("failure case has no ## Failure section in markdown", func() {
				fakeClient.TagExistsReturns("", githubreview.ErrTagNotFound)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.0.0\n\n- feat\n"),
					nil,
				)

				_, md := runStep(taskWithResult("abc123", "v1.0.0", "released", tmpDir))

				fullMarkdown, err := md.Marshal(context.Background())
				Expect(err).NotTo(HaveOccurred())
				Expect(fullMarkdown).NotTo(ContainSubstring("## Failure"))
			})
		})

		// Spec 058 prompt 3 — Req 4: mapping boundary test. The
		// LLM returns per_entry + extras as separate lists; the
		// ai-review step flattens them into a single PerEntry list
		// with extras entries tagged Verdict=hallucinated. This
		// integration seam catches regressions where extras is
		// dropped or mis-mapped.
		Context(
			"FaithfulnessLLMResponse → ReviewOutput.PerEntry mapping flattens extras as hallucinated",
			func() {
				It(
					"flattens extras with Verdict=hallucinated in the order LLM emitted them",
					func() {
						faithfulResp := map[string]interface{}{
							"per_entry": []map[string]string{
								{"entry": "- feat: X", "verdict": "present", "note": "ok"},
								{"entry": "- fix: Y", "verdict": "silent-drop", "note": "gone"},
							},
							"extras": []map[string]string{
								{"entry": "- chore: Z", "verdict": "hallucinated", "note": "added"},
							},
							"overall": pkg.OverallFail,
						}
						fakeRunner.RunReturns(
							&claudelib.ClaudeResult{Result: mustJSON(faithfulResp)},
							nil,
						)

						result, md := runStep(
							taskWithResult("abc123", "v1.0.0", "released", tmpDir),
						)

						Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
						Expect(result.NextPhase).To(Equal("human_review"))

						review := extractReview(md)
						Expect(review.PerEntry).To(HaveLen(3))
						Expect(review.PerEntry[0]).To(Equal(pkg.FaithfulnessVerdict{
							Entry:   "- feat: X",
							Verdict: pkg.FaithfulnessPresent,
							Note:    "ok",
						}))
						Expect(review.PerEntry[1]).To(Equal(pkg.FaithfulnessVerdict{
							Entry:   "- fix: Y",
							Verdict: pkg.FaithfulnessSilentDrop,
							Note:    "gone",
						}))
						Expect(review.PerEntry[2]).To(Equal(pkg.FaithfulnessVerdict{
							Entry:   "- chore: Z",
							Verdict: pkg.FaithfulnessHallucinated,
							Note:    "added",
						}))
						Expect(review.Overall).To(Equal(pkg.OverallFail))
						Expect(review.FailedChecks).To(ContainElement(pkg.CheckFaithfulness))
					},
				)
			},
		)

		// Spec 058 prompt 3 — Req 8: faithful rewrite happy path.
		Context("faithful rewrite", func() {
			It("overall=pass, push happens, NextPhase=done", func() {
				fakeClient.TagExistsReturns("abc1234", nil)
				fakeClient.ResolveTagCommitReturns("abc1234", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.2.8\n\n- feat: add foo\n- fix: bar\n"),
					nil,
				)

				result, md := runStep(taskWithResult("abc1234", "v1.2.8", "released", tmpDir))

				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("done"))
				Expect(fakeOps.PushCallCount()).To(Equal(1))

				review := extractReview(md)
				Expect(review.Approved).To(BeTrue())
				Expect(review.Overall).To(Equal(pkg.OverallPass))
				Expect(review.FailedChecks).To(BeEmpty())
			})
		})

		// Spec 058 prompt 3 — Req 9: faithfulness failure modes.
		Context("faithfulness failures", func() {
			It(
				"silent-drop → overall=fail, failed_checks contains Faithfulness, no push, ## Review captured",
				func() {
					fakeClient.TagExistsReturns("abc1234", nil)
					fakeClient.ResolveTagCommitReturns("abc1234", nil)
					fakeClient.FetchChangelogReturns(
						[]byte("## v1.2.8\n\n- feat: add foo\n"),
						nil,
					)
					resp := map[string]interface{}{
						"per_entry": []map[string]string{
							{"entry": "- fix: bar", "verdict": "silent-drop", "note": "missing"},
						},
						"extras":  []map[string]string{},
						"overall": pkg.OverallFail,
					}
					fakeRunner.RunReturns(
						&claudelib.ClaudeResult{Result: mustJSON(resp)},
						nil,
					)
					DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

					result, md := runStep(taskWithResult("abc1234", "v1.2.8", "released", tmpDir))

					Expect(fakeOps.PushCallCount()).To(Equal(0))
					Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
					Expect(result.NextPhase).To(Equal("human_review"))

					review := extractReview(md)
					Expect(review.Approved).To(BeFalse())
					Expect(review.FailedChecks).To(ContainElement(pkg.CheckFaithfulness))
					Expect(review.Notes).To(ContainSubstring(pkg.CheckFaithfulness))
					Expect(review.PerEntry).To(ContainElement(pkg.FaithfulnessVerdict{
						Entry:   "- fix: bar",
						Verdict: pkg.FaithfulnessSilentDrop,
						Note:    "missing",
					}))
				},
			)

			It("hallucinated → overall=fail, per_entry contains hallucinated entry", func() {
				fakeClient.TagExistsReturns("abc1234", nil)
				fakeClient.ResolveTagCommitReturns("abc1234", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.2.8\n\n- feat: add foo\n- chore: z\n"),
					nil,
				)
				resp := map[string]interface{}{
					"per_entry": []map[string]string{
						{"entry": "- feat: add foo", "verdict": "present"},
					},
					"extras": []map[string]string{
						{"entry": "- chore: z", "verdict": "hallucinated", "note": "added"},
					},
					"overall": pkg.OverallFail,
				}
				fakeRunner.RunReturns(
					&claudelib.ClaudeResult{Result: mustJSON(resp)},
					nil,
				)
				DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

				result, md := runStep(taskWithResult("abc1234", "v1.2.8", "released", tmpDir))

				Expect(fakeOps.PushCallCount()).To(Equal(0))
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.NextPhase).To(Equal("human_review"))

				review := extractReview(md)
				Expect(review.Approved).To(BeFalse())
				Expect(review.PerEntry).To(ContainElement(pkg.FaithfulnessVerdict{
					Entry:   "- chore: z",
					Verdict: pkg.FaithfulnessHallucinated,
					Note:    "added",
				}))
			})

			It(
				"unexpected file change → overall=fail, unexpected_files lists the offending file",
				func() {
					fakeClient.TagExistsReturns("abc1234", nil)
					fakeClient.ResolveTagCommitReturns("abc1234", nil)
					fakeClient.FetchChangelogReturns(
						[]byte("## v1.2.8\n\n- feat: add foo\n"),
						nil,
					)
					fakeOps.CommittedFilesReturns(
						[]string{"CHANGELOG.md", "secrets.env"},
						nil,
					)
					DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

					result, md := runStep(taskWithResult("abc1234", "v1.2.8", "released", tmpDir))

					Expect(fakeOps.PushCallCount()).To(Equal(0))
					Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
					Expect(result.NextPhase).To(Equal("human_review"))

					review := extractReview(md)
					Expect(review.Approved).To(BeFalse())
					Expect(review.Checks.UnexpectedFileChange).To(BeTrue())
					Expect(review.UnexpectedFiles).To(ContainElement("secrets.env"))
					Expect(review.FailedChecks).To(ContainElement(pkg.CheckUnexpectedFileChange))
				},
			)
		})

		// Spec 058 prompt 3 — Req 11: LLM unavailable.
		Context("ai-review LLM error", func() {
			It("overall=unknown, approved=false, no push", func() {
				fakeClient.TagExistsReturns("abc1234", nil)
				fakeClient.ResolveTagCommitReturns("abc1234", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.0.0\n\n- feat\n"),
					nil,
				)
				fakeRunner.RunReturns(
					nil,
					errors.New("dial tcp: connection refused"),
				)
				DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

				result, md := runStep(taskWithResult("abc1234", "v1.0.0", "released", tmpDir))

				Expect(fakeOps.PushCallCount()).To(Equal(0))
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.NextPhase).To(Equal("human_review"))

				review := extractReview(md)
				Expect(review.Approved).To(BeFalse())
				Expect(review.Overall).To(Equal(pkg.OverallUnknown))
				Expect(review.FailedChecks).To(ContainElement(pkg.CheckFaithfulness))
			})
		})

		// Spec 058 prompt 3 — Req 12: push failure.
		Context("push fails after approval", func() {
			It("overall=pass but task ends in human_review with push-failed note", func() {
				fakeClient.TagExistsReturns("abc1234", nil)
				fakeClient.ResolveTagCommitReturns("abc1234", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.0.0\n\n- feat\n"),
					nil,
				)
				fakeOps.PushReturns(errors.New("dial tcp: rate limited"))
				DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

				result, md := runStep(taskWithResult("abc1234", "v1.0.0", "released", tmpDir))

				Expect(fakeOps.PushCallCount()).To(Equal(1))
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.NextPhase).To(Equal("human_review"))

				review := extractReview(md)
				Expect(review.Notes).To(ContainSubstring("push failed"))
			})
		})

		// Spec 058 prompt 3 — Req 13: concurrent push.
		Context("concurrent push (tag already exists on upstream)", func() {
			It("human_review, push-failed note recorded", func() {
				fakeClient.TagExistsReturns("abc1234", nil)
				fakeClient.ResolveTagCommitReturns("abc1234", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.0.0\n\n- feat\n"),
					nil,
				)
				fakeOps.PushReturns(errors.New(
					"! [rejected] refs/tags/v1.0.0 -> refs/tags/v1.0.0 (already exists)",
				))
				DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })

				result, md := runStep(taskWithResult("abc1234", "v1.0.0", "released", tmpDir))

				Expect(fakeOps.PushCallCount()).To(Equal(1))
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.NextPhase).To(Equal("human_review"))

				review := extractReview(md)
				Expect(review.Notes).To(ContainSubstring("push failed"))
			})
		})

		// Spec 059 prompt 3 — Req 1a/1b: transport errors on the
		// two structural checks (TagAtExpectedSHA,
		// ChangelogHeaderRewritten) must fail closed: the boolean
		// is set to false AND the failed-check name is appended.
		// Empty workdir short-circuits the faithfulness path to
		// OverallUnknown (per existing semantics), so we drive it
		// through taskWithResult with workdir="" rather than the
		// default BeforeEach(tmpDir). The faithfulness path then
		// records CheckFaithfulness, but the rollup surfaces
		// OverallUnknown — which the spec instructs NOT to assert on
		// (the override masks the underlying failure for triage
		// purposes; the boolean + FailedChecks assertions are the
		// load-bearing contract).
		Context("transport-error fail-closed", func() {
			It(
				"CommittedFiles error sets UnexpectedFileChange=true and appends CheckUnexpectedFileChange",
				func() {
					// Spec 059 prompt 4 — C1 fail-closed regression
					// guard. Prior to the fix, a transient
					// CommittedFiles error left the check silently
					// passing; a single git blip could let an
					// unexpected-file commit land without the human
					// reviewer being alerted.
					//
					// Use a real on-disk workdir so the
					// result.Workdir != "" short-circuit is bypassed
					// and the CommittedFiles call actually fires.
					workdir, err := os.MkdirTemp("", "ai-review-test-")
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(func() { _ = os.RemoveAll(workdir) })

					Expect(os.WriteFile(
						filepath.Join(workdir, "CHANGELOG.md"),
						[]byte("## v1.0.0\n\n- feat\n"),
						0o600,
					)).To(Succeed())

					// Two structural checks pass: tag exists at expected
					// SHA + changelog header is rewritten. Only the
					// CommittedFiles path needs to fail.
					fakeClient.TagExistsReturns("abc123", nil)
					fakeClient.ResolveTagCommitReturns("abc123", nil)
					fakeClient.FetchChangelogReturns(
						[]byte("## v1.0.0\n\n- feat\n"),
						nil,
					)
					fakeOps.CommittedFilesReturns(
						nil,
						errors.New("git: lstat workdir: no such file or directory"),
					)
					// Drive checkFaithfulness to OverallUnknown so the
					// assertion on output.Approved = false is not
					// contaminated by a pass-path artifact.
					fakeRunner.RunReturns(nil, errors.New("claude unavailable"))

					result, md := runStep(taskWithResult("abc123", "v1.0.0", "released", workdir))

					Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
					Expect(result.NextPhase).To(Equal("human_review"))

					review := extractReview(md)
					Expect(review.Approved).To(BeFalse())
					Expect(review.Checks.UnexpectedFileChange).To(BeTrue())
					Expect(review.FailedChecks).To(ContainElement(pkg.CheckUnexpectedFileChange))
				},
			)
		})

		// Spec 059 prompt 3 — Req 3: plugin-manifest branch in the
		// unexpected-file-change check. Case A applies:
		// plugin.DetectManifests CAN return an error (manifest.go
		// line 52 — non-IsNotExist Stat failures). 3a proves a
		// committed plugin manifest is in the expected set, 3b
		// proves that a DetectManifests error falls back to the
		// changelog-only expected set so an extra committed file
		// surfaces as unexpected.
		Context("plugin-manifest branch in unexpected-file-change check", func() {
			// taskWithResultWithWorkdir is a thin wrapper that lets us
			// supply an arbitrary workdir path (the default helper is
			// tagged to the BeforeEach tmpDir).
			taskWithResultWithWorkdir := func(
				workdir, commitSHA, tag string,
			) string {
				return taskWithResult(commitSHA, tag, "released", workdir)
			}

			// 3a — plugin manifest in expected set passes. Seed a
			// real on-disk temp workdir with a valid plugin.json so
			// DetectManifests returns it. Committed files match.
			It(
				"committed plugin manifest is in the expected set → UnexpectedFileChange=false, FailedChecks does NOT contain CheckUnexpectedFileChange",
				func() {
					workdir, err := os.MkdirTemp("", "ai-review-test-")
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(func() { _ = os.RemoveAll(workdir) })

					Expect(
						os.MkdirAll(filepath.Join(workdir, ".claude-plugin"), 0o750),
					).To(Succeed())
					Expect(os.WriteFile(
						filepath.Join(workdir, ".claude-plugin", "plugin.json"),
						[]byte(`{"name":"x","version":"0.9.0"}`),
						0o600,
					)).To(Succeed())
					Expect(os.WriteFile(
						filepath.Join(workdir, "CHANGELOG.md"),
						[]byte("## v1.0.0\n\n- feat\n"),
						0o600,
					)).To(Succeed())

					fakeClient.TagExistsReturns("abc1234", nil)
					fakeClient.ResolveTagCommitReturns("abc1234", nil)
					fakeClient.FetchChangelogReturns(
						[]byte("## v1.0.0\n\n- feat\n"),
						nil,
					)
					fakeOps.CommittedFilesReturns(
						[]string{"CHANGELOG.md", ".claude-plugin/plugin.json"},
						nil,
					)
					// Default BeforeEach stubFaithfulLLM produces pass.

					result, md := runStep(taskWithResultWithWorkdir(workdir, "abc1234", "v1.0.0"))

					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(result.NextPhase).To(Equal("done"))

					review := extractReview(md)
					Expect(review.Approved).To(BeTrue())
					Expect(review.Checks.UnexpectedFileChange).To(BeFalse())
					Expect(review.UnexpectedFiles).To(BeEmpty())
					Expect(review.FailedChecks).NotTo(ContainElement(pkg.CheckUnexpectedFileChange))
				},
			)

			// 3b — DetectManifests error → falls back to
			// changelog-only expected set. Seed the workdir with
			// INVALID JSON in plugin.json so Stat on a child file
			// would NOT raise a non-IsNotExist error directly;
			// instead we chmod the parent .claude-plugin dir to
			// 0000 so Stat on the children returns EACCES, which
			// DetectManifests converts to a wrapped error.
			It(
				"DetectManifests error + unexpected committed file → UnexpectedFileChange=true, FailedChecks contains CheckUnexpectedFileChange, UnexpectedFiles lists the file",
				func() {
					// chmod 0000 on Linux non-root blocks Stat of the children;
					// skip on platforms where this is unreliable (Darwin, root containers).
					if runtime.GOOS == "darwin" || os.Geteuid() == 0 {
						Skip("requires unprivileged Linux for non-IsNotExist Stat failure")
					}

					workdir, err := os.MkdirTemp("", "ai-review-test-")
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(func() { _ = os.RemoveAll(workdir) })

					Expect(
						os.MkdirAll(filepath.Join(workdir, ".claude-plugin"), 0o750),
					).To(Succeed())
					Expect(os.WriteFile(
						filepath.Join(workdir, ".claude-plugin", "plugin.json"),
						[]byte("not-json"),
						0o600,
					)).To(Succeed())
					Expect(os.WriteFile(
						filepath.Join(workdir, "CHANGELOG.md"),
						[]byte("## v1.0.0\n\n- feat\n"),
						0o600,
					))
					// Chmod the .claude-plugin dir to 0000 so that
					// DetectManifests (which calls Stat on
					// .claude-plugin/plugin.json) returns EACCES, a
					// non-IsNotExist error. The execution
					// executeLocalRelease already runs
					// DetectManifests earlier in the pipeline; here we
					// are on the ai-review side. We just need the
					// ai-review check to see the error path. To force
					// that, the simplest reliable way is to delete the
					// plugin.json after writing it and put an
					// UNREADABLE directory in its place: delete the
					// file and leave .claude-plugin/ but chmod it to
					// 0000 so Stat on .claude-plugin/plugin.json
					// returns EACCES (a non-IsNotExist error →
					// DetectManifests returns the wrapped error).
					Expect(
						os.Remove(filepath.Join(workdir, ".claude-plugin", "plugin.json")),
					).To(Succeed())
					Expect(os.Chmod(filepath.Join(workdir, ".claude-plugin"), 0o000)).To(Succeed())
					DeferCleanup(func() {
						_ = os.Chmod(
							filepath.Join(workdir, ".claude-plugin"),
							0o750,
						) // #nosec G302 -- restore test fixture dir mode
					})

					fakeClient.TagExistsReturns("abc1234", nil)
					fakeClient.ResolveTagCommitReturns("abc1234", nil)
					fakeClient.FetchChangelogReturns(
						[]byte("## v1.0.0\n\n- feat\n"),
						nil,
					)
					// Fallback: expected = [CHANGELOG.md]. An extra
					// committed "plugin.json" then surfaces as
					// unexpected.
					fakeOps.CommittedFilesReturns(
						[]string{"CHANGELOG.md", "plugin.json"},
						nil,
					)
					// Default BeforeEach stubFaithfulLLM produces pass.

					result, md := runStep(taskWithResultWithWorkdir(workdir, "abc1234", "v1.0.0"))

					Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
					Expect(result.NextPhase).To(Equal("human_review"))

					review := extractReview(md)
					Expect(review.Approved).To(BeFalse())
					Expect(review.Checks.UnexpectedFileChange).To(BeTrue())
					Expect(review.FailedChecks).To(ContainElement(pkg.CheckUnexpectedFileChange))
					Expect(review.UnexpectedFiles).To(ContainElement("plugin.json"))
				},
			)

			// 3c — no-op manifest case: both manifests seeded into the
			// workdir (so DetectManifests finds them → in allowed set),
			// but CommittedFiles returns only CHANGELOG.md (manifest
			// was already at target version). Guard must pass:
			// UnexpectedFileChange=false, CheckUnexpectedFileChange NOT
			// in FailedChecks, Approved=true.
			It(
				"no-op manifest (already at target version) → UnexpectedFileChange=false, CheckUnexpectedFileChange NOT in FailedChecks, Approved=true",
				func() {
					workdir, err := os.MkdirTemp("", "ai-review-test-")
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(func() { _ = os.RemoveAll(workdir) })

					Expect(
						os.MkdirAll(filepath.Join(workdir, ".claude-plugin"), 0o750),
					).To(Succeed())
					Expect(os.WriteFile(
						filepath.Join(workdir, ".claude-plugin", "plugin.json"),
						[]byte(`{"name":"x","version":"0.9.0"}`),
						0o600,
					)).To(Succeed())
					Expect(os.WriteFile(
						filepath.Join(workdir, ".claude-plugin", "marketplace.json"),
						[]byte(`{"version":"0.9.0"}`),
						0o600,
					)).To(Succeed())
					Expect(os.WriteFile(
						filepath.Join(workdir, "CHANGELOG.md"),
						[]byte("## v1.0.0\n\n- feat\n"),
						0o600,
					)).To(Succeed())

					fakeClient.TagExistsReturns("abc1234", nil)
					fakeClient.ResolveTagCommitReturns("abc1234", nil)
					fakeClient.FetchChangelogReturns(
						[]byte("## v1.0.0\n\n- feat\n"),
						nil,
					)
					// No manifests in the committed set — they were already at target.
					fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
					// Default BeforeEach stubFaithfulLLM produces pass.

					result, md := runStep(taskWithResultWithWorkdir(workdir, "abc1234", "v1.0.0"))

					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(result.NextPhase).To(Equal("done"))

					review := extractReview(md)
					Expect(review.Approved).To(BeTrue())
					Expect(review.Checks.UnexpectedFileChange).To(BeFalse())
					Expect(review.UnexpectedFiles).To(BeEmpty())
					Expect(review.FailedChecks).NotTo(ContainElement(pkg.CheckUnexpectedFileChange))
				},
			)
		})

		// Spec 064 — ai_review review-warning override. When the local
		// review rejects a release but the remote confirms the tag is
		// already at the agent's expected SHA (released) or at a
		// different SHA (superseded), the task closes as `completed`
		// and a `## Review Warning` block is appended to the task body
		// alongside the rejected `## Review` verdict. The override is a
		// SUB-DECISION on the existing `!approved` path; the existing
		// `human_review` path stands when the remote is empty or the
		// LsRemote query errors.
		Context("review-warning override (spec 064)", func() {
			// taskWithResultFull wires ## Result + frontmatter with
			// the `clone_url` and `ref` keys required by
			// checkReviewOverride (the default taskWithResult helper
			// omits them). Tests that drive the override path need
			// these keys present.
			taskWithResultFull := func(
				commitSHA, tag, outcome, workdir string,
			) string {
				const fm = "---\n" +
					"status: in_progress\n" +
					"phase: ai_review\n" +
					"assignee: github-releaser-agent\n" +
					"task_type: github-release\n" +
					"repo: bborbe/example\n" +
					"task_identifier: gh-release-001\n" +
					"clone_url: https://github.com/bborbe/example.git\n" +
					"ref: main\n" +
					"---\n\n"
				plan := "## Plan\n\n" +
					"```json\n" +
					`{"outcome":"ready","next_version":"1.0.0","next_version_header":"## v1.0.0","original_unreleased":"- feat: add foo\n"}` + "\n" +
					"```\n\n"
				result := "## Result\n\n" +
					"```json\n" +
					fmt.Sprintf(
						`{"outcome":%q,"path":"direct-push","commit_sha":%q,"tag":%q,"workdir":%q,"local_tag":%q}`,
						outcome,
						commitSHA,
						tag,
						workdir,
						tag,
					) + "\n" +
					"```\n"
				return fm + plan + result
			}

			// driveRejectingFaithfulnessLLM makes the LLM call return
			// a faithfulness fail (one entry silently dropped). The
			// verifier is configured to pass the structural checks
			// (TagExists + ResolveTagCommit) so the rollup is driven
			// purely by CheckFaithfulness.
			driveRejectingFaithfulnessLLM := func() {
				fakeClient.TagExistsReturns("abc123", nil)
				fakeClient.ResolveTagCommitReturns("abc123", nil)
				fakeClient.FetchChangelogReturns(
					[]byte("## v1.2.8\n\n- feat: add foo\n"),
					nil,
				)
				resp := map[string]interface{}{
					"per_entry": []map[string]string{
						{
							"entry":   "- fix: bar",
							"verdict": "silent-drop",
							"note":    "missing",
						},
					},
					"extras":  []map[string]string{},
					"overall": pkg.OverallFail,
				}
				fakeRunner.RunReturns(
					&claudelib.ClaudeResult{Result: mustJSON(resp)},
					nil,
				)
			}

			// (a) remote SHA matches expected → completed + ## Review
			// Warning. The new branch is taken; the workdir cleanup
			// sentinel is set so the on-disk clone is removed at the
			// terminal transition.
			It(
				"remote SHA matches expected → completed, ## Review Warning, ## Review preserved",
				func() {
					driveRejectingFaithfulnessLLM()
					DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
					fakeOps.LsRemoteReturns("abc123", nil)

					result, md := runStep(
						taskWithResultFull("abc123", "v1.2.8", "released", tmpDir),
					)

					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(result.NextPhase).To(Equal("done"))

					Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))
					Expect(fakeOps.PushCallCount()).To(Equal(0))

					Expect(md.Frontmatter["status"]).To(Equal("completed"))
					Expect(md.Frontmatter["phase"]).To(Equal("done"))

					// ## Review (rejected verdict) preserved.
					review := extractReview(md)
					Expect(review.Approved).To(BeFalse())
					Expect(review.FailedChecks).To(ContainElement(pkg.CheckFaithfulness))

					// ## Review Warning appended.
					warning := extractReviewWarning(md)
					Expect(warning).NotTo(BeNil())
					Expect(warning.FailedChecks).To(ContainElement(pkg.CheckFaithfulness))
					Expect(warning.PlannedVersion).To(Equal("v1.2.8"))
					Expect(warning.ObservedRemoteSHA).To(Equal("abc123"))
					Expect(warning.Note).To(ContainSubstring("review rejected"))
					Expect(warning.Note).To(ContainSubstring("abc123"))
				},
			)

			// (b) remote SHA differs from expected → completed +
			// ## Review Warning (superseded mirror). The override
			// fires on a non-matching SHA too, but the warning's
			// ObservedRemoteSHA records the remote's value, not the
			// agent's expected one.
			It(
				"remote SHA differs from expected → completed, ## Review Warning, ObservedRemoteSHA = remote SHA",
				func() {
					driveRejectingFaithfulnessLLM()
					DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
					fakeOps.LsRemoteReturns("deadbee", nil)

					result, md := runStep(
						taskWithResultFull("abc123", "v1.2.8", "released", tmpDir),
					)

					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(result.NextPhase).To(Equal("done"))
					Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))

					Expect(md.Frontmatter["status"]).To(Equal("completed"))
					Expect(md.Frontmatter["phase"]).To(Equal("done"))

					review := extractReview(md)
					Expect(review.Approved).To(BeFalse())

					warning := extractReviewWarning(md)
					Expect(warning).NotTo(BeNil())
					Expect(warning.PlannedVersion).To(Equal("v1.2.8"))
					Expect(warning.ObservedRemoteSHA).To(Equal("deadbee"))
				},
			)

			// (c) remote empty → existing human_review path stands.
			// checkReviewOverride returns nil on the empty result; the
			// `!approved` branch falls through to finishHumanReview.
			It("remote empty → human_review, no ## Review Warning block", func() {
				driveRejectingFaithfulnessLLM()
				DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
				fakeOps.LsRemoteReturns("", nil)

				result, md := runStep(
					taskWithResultFull("abc123", "v1.2.8", "released", tmpDir),
				)

				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.NextPhase).To(Equal("human_review"))
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))

				review := extractReview(md)
				Expect(review.Approved).To(BeFalse())
				Expect(extractReviewWarning(md)).To(BeNil())
			})

			// (d) LsRemote errors → existing human_review path stands.
			// The error is logged via glog.V(2) (redacted) but the
			// verdict-downgrade does NOT happen — the empty/error
			// branches both short-circuit checkReviewOverride to nil.
			It("LsRemote errors → human_review, no ## Review Warning block", func() {
				driveRejectingFaithfulnessLLM()
				DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
				fakeOps.LsRemoteReturns("", errors.New("ls-remote boom"))

				result, md := runStep(
					taskWithResultFull("abc123", "v1.2.8", "released", tmpDir),
				)

				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.NextPhase).To(Equal("human_review"))
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))

				review := extractReview(md)
				Expect(review.Approved).To(BeFalse())
				Expect(extractReviewWarning(md)).To(BeNil())
			})

			// (e) happy path (Approved=true) is unchanged — the new
			// branch is unreachable. LsRemote must NOT be called.
			It("Approved=true happy path is unchanged (no LsRemote, no ## Review Warning)", func() {
				DeferCleanup(func() { _ = os.RemoveAll(tmpDir) })
				fakeOps.LsRemoteReturns("abc123", nil)

				result, md := runStep(
					taskWithResultFull("abc123", "v1.0.0", "released", tmpDir),
				)

				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("done"))
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(0))

				review := extractReview(md)
				Expect(review.Approved).To(BeTrue())
				Expect(extractReviewWarning(md)).To(BeNil())
			})

			// (f) short-circuit path (Result.Outcome == "failed") is
			// unchanged. The new branch is unreachable. LsRemote
			// must NOT be called.
			It(
				"Result.Outcome=failed short-circuits without LsRemote (no ## Review Warning)",
				func() {
					fakeOps.LsRemoteReturns("abc123", nil)

					result, md := runStep(taskWithFailedResult())

					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(result.NextPhase).To(Equal("done"))
					Expect(fakeOps.LsRemoteCallCount()).To(Equal(0))

					review := extractReview(md)
					Expect(review.Approved).To(BeTrue())
					Expect(extractReviewWarning(md)).To(BeNil())
				},
			)
		})
	})
})

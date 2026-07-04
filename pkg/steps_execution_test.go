// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	agentlib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	gitmocks "github.com/bborbe/github-releaser-agent/mocks"
	pkg "github.com/bborbe/github-releaser-agent/pkg"
	"github.com/bborbe/github-releaser-agent/pkg/git"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ExecutionStep", func() {
	// Verify Name() and ShouldRun() exist and return the expected values.
	// These are simple delegation methods; direct test ensures they are not dead code.
	Describe("interface methods", func() {
		It("Name returns github-release-execute", func() {
			fakeOps := &gitmocks.GitOps{}
			step := pkg.NewExecutionStep(fakeOps, "")
			Expect(step.Name()).To(Equal("github-release-execute"))
		})

		It("ShouldRun returns true, nil", func() {
			fakeOps := &gitmocks.GitOps{}
			step := pkg.NewExecutionStep(fakeOps, "")
			minimalMD := `---
status: in_progress
phase: execution
task_identifier: test
clone_url: https://github.com/test/test.git
ref: main
---

## Plan

` + "```json" + `{"outcome":"ready","next_version":"1.0.0","next_version_header":"## v1.0.0"}` + "```"
			md, err := agentlib.ParseMarkdown(context.Background(), minimalMD)
			Expect(err).NotTo(HaveOccurred())
			shouldRun, err := step.ShouldRun(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldRun).To(BeTrue())
		})
	})
	const taskMD = `---
status: in_progress
phase: execution
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: https://github.com/bborbe/example.git
ref: master
current_version: v1.2.7
task_identifier: gh-release-bborbe-example-master-049a
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "patch",
  "reasoning": "fix-only batch",
  "current_version": "v1.2.7",
  "next_version": "1.2.8",
  "next_version_header": "## v1.2.8",
  "header_prefix_style": "v",
  "bullets": ["fix: thing"]
}
` + "```" + `
`

	writeChangelog := func(workdir string) {
		Expect(os.MkdirAll(workdir, 0o750)).To(Succeed())
		content := []byte("# Changelog\n\n## Unreleased\n\n- fix: thing\n\n## v1.2.6\n\n- old\n")
		Expect(os.WriteFile(filepath.Join(workdir, "CHANGELOG.md"), content, 0o600)).To(Succeed())
	}

	Context("happy path", func() {
		It(
			"clones, rewrites, commits, tags (no push); writes ## Result(released); returns Done/NextPhase=ai_review",
			func() {
				fakeOps := &gitmocks.GitOps{}

				// Capture the workdir that the step passed to Clone so we can
				// write a CHANGELOG.md there before Commit reads it.
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					return nil
				}

				// Per spec AC #11(e): the bytes on disk at the moment Commit is
				// invoked MUST contain `## v1.2.8` AND NOT contain `## Unreleased`.
				// This proves RewriteUnreleasedHeader ran BEFORE Commit, not as
				// a hardcoded JSON-output-only step. Read the CHANGELOG inside
				// the stub (before the defer cleanup runs).
				fakeOps.CommitStub = func(_ context.Context, workdir, _ string, _ ...string) (string, error) {
					content, readErr := os.ReadFile(filepath.Join(workdir, "CHANGELOG.md"))
					Expect(readErr).NotTo(HaveOccurred())
					Expect(string(content)).To(ContainSubstring("## v1.2.8"))
					Expect(string(content)).NotTo(ContainSubstring("## Unreleased"))
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("ai_review"))

				// All GitOps methods called exactly once. CommittedFiles in
				// particular proves the pre-push guard is invoked on the happy
				// path — if guardCommittedFiles were dropped from
				// executeLocalRelease this assertion (not Tag) would catch it.
				Expect(fakeOps.CloneCallCount()).To(Equal(1))
				Expect(fakeOps.CommitCallCount()).To(Equal(1))
				Expect(fakeOps.CommittedFilesCallCount()).To(Equal(1))
				Expect(fakeOps.TagCallCount()).To(Equal(1))
				// Push has moved out of execution; the local tag is held
				// until ai_review (next phase) pushes it.
				Expect(fakeOps.PushCallCount()).To(Equal(0))

				// Tag name + message verbatim from plan.next_version_header[3:].
				_, _, tagName, tagMsg := fakeOps.TagArgsForCall(0)
				Expect(tagName).To(Equal("v1.2.8"))
				Expect(tagMsg).To(Equal("release v1.2.8"))

				// Commit message uses the same canonical "release v1.2.8".
				_, _, commitMsg, _ := fakeOps.CommitArgsForCall(0)
				Expect(commitMsg).To(Equal("release v1.2.8"))

				// ## Result body shape.
				got, err := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Outcome).To(Equal("released"))
				Expect(got.Path).To(Equal("direct-push"))
				Expect(got.CommitSHA).To(Equal("abc1234"))
				Expect(got.Tag).To(Equal("v1.2.8"))
				Expect(got.LocalTag).To(Equal("v1.2.8"))
				Expect(got.Workdir).NotTo(BeEmpty())
				Expect(string(got.ErrorCategory)).To(BeEmpty())

				// Clone URL had token injected.
				_, gotCloneURL, _, _ := fakeOps.CloneArgsForCall(0)
				Expect(
					gotCloneURL,
				).To(Equal("https://x-access-token:test-token@github.com/bborbe/example.git"))
			},
		)
	})

	// MOVED: push failure tests now live in the ai-review push-gating spec (spec 058 prompt 3).
	//
	// The Context("protected_branch_rejected", ...) block that previously lived
	// here exercised Push returns → Status=Failed. Push has moved out of
	// execution into ai-review; the same failure surface will be re-tested in
	// the ai-review push-gating prompt (next prompt). See spec 058 prompt 3.

	Context("pre-push guard (CommittedFiles)", func() {
		// The guard is the primary security assertion of the release trust
		// model: a release commit must change ONLY CHANGELOG.md. These
		// specs prove it fails closed — Tag is NEVER reached when the
		// committed file set is wrong or unobtainable.
		runGuard := func(committed []string, committedErr error) (*agentlib.Result, *gitmocks.GitOps, *agentlib.Markdown) {
			fakeOps := &gitmocks.GitOps{}
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitStub = func(_ context.Context, _, _ string, _ ...string) (string, error) {
				return "def5678", nil
			}
			fakeOps.CommittedFilesReturns(committed, committedErr)
			fakeOps.TagReturns(nil)

			step := pkg.NewExecutionStep(fakeOps, "")
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())
			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			return result, fakeOps, md
		}

		assertFailClosed := func(fakeOps *gitmocks.GitOps, md *agentlib.Markdown, wantCategory string) {
			// The guard ran exactly once — proves it is actually invoked on
			// this path (not silently skipped).
			Expect(fakeOps.CommittedFilesCallCount()).To(Equal(1))
			// Fail closed: nothing tagged, nothing pushed.
			Expect(fakeOps.TagCallCount()).To(Equal(0))
			Expect(fakeOps.PushCallCount()).To(Equal(0))
			got, err := agentlib.ExtractSection[pkg.ResultOutput](
				context.Background(),
				md,
				"## Result",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Outcome).To(Equal("failed"))
			Expect(string(got.ErrorCategory)).To(Equal(wantCategory))
			Expect(got.Tag).To(BeEmpty())
		}

		It("extra files → Status=Failed, error_category=unexpected_diff, no tag", func() {
			result, fakeOps, md := runGuard([]string{"CHANGELOG.md", "config.yml"}, nil)
			Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
			assertFailClosed(fakeOps, md, "unexpected_diff")
		})

		It("empty file list → Status=Failed, error_category=unexpected_diff, no tag", func() {
			// git diff-tree can legitimately return no files (e.g. a root
			// commit); len(files)!=1 must still fail closed, not tag blindly.
			result, fakeOps, md := runGuard([]string{}, nil)
			Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
			assertFailClosed(fakeOps, md, "unexpected_diff")
		})

		It(
			"wrong single file → Status=Failed, error_category=unexpected_diff, no tag",
			func() {
				result, fakeOps, md := runGuard([]string{"main.go"}, nil)
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				assertFailClosed(fakeOps, md, "unexpected_diff")
			},
		)

		It("CommittedFiles error → Status=Failed, error_category=unknown, no tag", func() {
			result, fakeOps, md := runGuard(
				nil,
				errors.Errorf(context.Background(), "git diff-tree boom"),
			)
			Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
			assertFailClosed(fakeOps, md, "unknown")
		})
	})

	Context("workdir lifetime", func() {
		// Failure path: workdir is removed by the cleanup defer.
		// The pre-prompt behavior (always-cleanup defer) is preserved on
		// failure so we don't leak tmpdirs when Clone / Commit / Tag fails.
		It("removes the workdir on the failure path", func() {
			fakeOps := &gitmocks.GitOps{}
			capturedWorkdir := ""
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				capturedWorkdir = workdir
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitStub = func(_ context.Context, _, _ string, _ ...string) (string, error) {
				return "", errors.Errorf(context.Background(), "commit boom")
			}
			step := pkg.NewExecutionStep(fakeOps, "")
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			_, err = step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())

			Expect(capturedWorkdir).NotTo(BeEmpty())
			_, statErr := os.Stat(capturedWorkdir)
			Expect(
				os.IsNotExist(statErr),
			).To(BeTrue(), "workdir %s should be removed on failure", capturedWorkdir)
		})

		// Happy path: workdir survives Run's return so ai-review can
		// read it. See the "does NOT push and workdir survives" spec
		// below for the full assertion.
	})

	Context("happy-path workdir + no-push", func() {
		It("does NOT push and workdir survives execution return", func() {
			fakeOps := &gitmocks.GitOps{}
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitReturns("abc1234", nil)
			fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
			fakeOps.TagReturns(nil)

			step := pkg.NewExecutionStep(fakeOps, "")
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			_, err = step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())

			got, err := agentlib.ExtractSection[pkg.ResultOutput](
				context.Background(),
				md,
				"## Result",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Workdir).NotTo(BeEmpty())
			Expect(got.LocalTag).To(Equal("v1.2.8"))

			// Register cleanup IMMEDIATELY after parsing so the
			// Ginkgo DeferCleanup fires even on subsequent assertion
			// failure. A trailing os.RemoveAll in the spec body would
			// be skipped on assertion failure and leak tmpdirs in CI.
			DeferCleanup(func() {
				_ = os.RemoveAll(got.Workdir)
			})

			_, statErr := os.Stat(got.Workdir)
			Expect(
				os.IsNotExist(statErr),
			).To(BeFalse(), "workdir %s should survive Run's return", got.Workdir)

			// Push was not invoked from the execution step.
			Expect(fakeOps.PushCallCount()).To(Equal(0))
		})
	})

	Context("rewrite_needed", func() {
		const rewriteTaskMD = `---
status: in_progress
phase: execution
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: https://github.com/bborbe/example.git
ref: master
current_version: v1.2.7
task_identifier: gh-release-bborbe-example-master-rewrite
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "patch",
  "reasoning": "fix-only batch",
  "current_version": "v1.2.7",
  "next_version": "1.2.8",
  "next_version_header": "## v1.2.8",
  "header_prefix_style": "v",
  "bullets": ["feat: cleaned"],
  "rewrite_needed": true,
  "rewritten_unreleased": "- feat: cleaned\n"
}
` + "```" + `
`

		const noRewriteTaskMD = `---
status: in_progress
phase: execution
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: https://github.com/bborbe/example.git
ref: master
current_version: v1.2.7
task_identifier: gh-release-bborbe-example-master-norewrite
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "patch",
  "reasoning": "fix-only batch",
  "current_version": "v1.2.7",
  "next_version": "1.2.8",
  "next_version_header": "## v1.2.8",
  "header_prefix_style": "v",
  "bullets": ["feat: original"],
  "rewrite_needed": false,
  "rewritten_unreleased": ""
}
` + "```" + `
`

		writeNoisyChangelog := func(workdir string) {
			Expect(os.MkdirAll(workdir, 0o750)).To(Succeed())
			content := []byte(
				"# Changelog\n\n## Unreleased\n\n- raw commit line one\n- raw commit line two\n\n## v1.2.6\n\n- old\n",
			)
			Expect(
				os.WriteFile(filepath.Join(workdir, "CHANGELOG.md"), content, 0o600),
			).To(Succeed())
		}

		writeOriginalChangelog := func(workdir string) {
			Expect(os.MkdirAll(workdir, 0o750)).To(Succeed())
			content := []byte(
				"# Changelog\n\n## Unreleased\n\n- feat: original\n\n## v1.2.6\n\n- old\n",
			)
			Expect(
				os.WriteFile(filepath.Join(workdir, "CHANGELOG.md"), content, 0o600),
			).To(Succeed())
		}

		It(
			"rewrite_needed=true: ## Unreleased body is replaced before header rename",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeNoisyChangelog(workdir)
					return nil
				}
				// Single commit covering BOTH the body rewrite AND the
				// header rename. If the body replacement ran as a
				// separate commit this assertion would catch it
				// (CommitCallCount would be 2, not 1).
				fakeOps.CommitStub = func(_ context.Context, workdir, _ string, _ ...string) (string, error) {
					content, readErr := os.ReadFile(filepath.Join(workdir, "CHANGELOG.md"))
					Expect(readErr).NotTo(HaveOccurred())
					bytes := string(content)
					Expect(bytes).To(ContainSubstring("- feat: cleaned"))
					Expect(bytes).NotTo(ContainSubstring("raw commit line one"))
					Expect(bytes).NotTo(ContainSubstring("## Unreleased"))
					Expect(bytes).To(ContainSubstring("## v1.2.8"))
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), rewriteTaskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				Expect(fakeOps.CommitCallCount()).To(Equal(1))
				Expect(fakeOps.PushCallCount()).To(Equal(0))
			},
		)

		It(
			"rewrite_needed=false: ## Unreleased body is preserved, only header is renamed",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeOriginalChangelog(workdir)
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, workdir, _ string, _ ...string) (string, error) {
					content, readErr := os.ReadFile(filepath.Join(workdir, "CHANGELOG.md"))
					Expect(readErr).NotTo(HaveOccurred())
					bytes := string(content)
					Expect(bytes).To(ContainSubstring("- feat: original"))
					Expect(bytes).NotTo(ContainSubstring("## Unreleased"))
					Expect(bytes).To(ContainSubstring("## v1.2.8"))
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), noRewriteTaskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				Expect(fakeOps.CommitCallCount()).To(Equal(1))
				Expect(fakeOps.PushCallCount()).To(Equal(0))
			},
		)
	})

	// Spec 059 prompt 3 — Req 1: cover the rewrite_needed branch with
	// a clean rewritten body under the new header. Captures the
	// post-Commit CHANGELOG.md bytes via a closure variable and asserts
	// the verbatim rewritten body appears under `## v1.0.0`, with no
	// leftover `## Unreleased` heading or noisy original body.
	Describe("rewrite_needed branch", func() {
		const happyTaskMD = `---
status: in_progress
phase: execution
assignee: github-releaser-agent
task_type: github-release
repo: x/y
clone_url: https://github.com/x/y.git
ref: main
current_version: v0.9.0
task_identifier: rewrite-happy
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "minor",
  "reasoning": "new feature",
  "current_version": "v0.9.0",
  "next_version": "1.0.0",
  "next_version_header": "## v1.0.0",
  "header_prefix_style": "v",
  "bullets": ["feat: clean one", "feat: clean two"],
  "rewrite_needed": true,
  "rewritten_unreleased": "- clean entry one\n- clean entry two\n"
}
` + "```" + `
`
		const noUnreleasedTaskMD = `---
status: in_progress
phase: execution
assignee: github-releaser-agent
task_type: github-release
repo: x/y
clone_url: https://github.com/x/y.git
ref: main
current_version: v0.9.0
task_identifier: rewrite-no-unreleased
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "minor",
  "reasoning": "new feature",
  "current_version": "v0.9.0",
  "next_version": "1.0.0",
  "next_version_header": "## v1.0.0",
  "header_prefix_style": "v",
  "bullets": ["feat: x"],
  "rewrite_needed": true,
  "rewritten_unreleased": "- clean entry one\n"
}
` + "```" + `
`

		// Happy path: capture the bytes Commit saw on disk so the spec
		// can assert the rewritten body landed under the new header.
		It(
			"rewrite_needed=true rewrites body under new header; Commit captures bytes; Status=Done",
			func() {
				var capturedBytes []byte
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					Expect(os.MkdirAll(workdir, 0o750)).To(Succeed())
					content := []byte(
						"## Unreleased\n\n- noisy original entry that gets rewritten\n- another noisy one\n\n## v0.9.0\n\n- old release\n",
					)
					Expect(
						os.WriteFile(filepath.Join(workdir, "CHANGELOG.md"), content, 0o600),
					).To(Succeed())
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, workdir, _ string, _ ...string) (string, error) {
					content, readErr := os.ReadFile(filepath.Join(workdir, "CHANGELOG.md"))
					Expect(readErr).NotTo(HaveOccurred())
					capturedBytes = content
					return "deadbee", nil
				}
				fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), happyTaskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal(string(domain.TaskPhaseAIReview)))

				// capturedBytes MUST contain the rewritten body under the new
				// header verbatim, MUST NOT contain the noisy original, MUST
				// NOT contain `## Unreleased`.
				Expect(
					string(capturedBytes),
				).To(ContainSubstring("## v1.0.0\n- clean entry one\n- clean entry two\n"))
				Expect(string(capturedBytes)).NotTo(ContainSubstring("noisy original entry"))
				Expect(string(capturedBytes)).NotTo(ContainSubstring("## Unreleased"))
			},
		)

		// Error mapping: missing `## Unreleased` heading causes
		// ReplaceUnreleasedBody to return "unreleased header not
		// found"; the execution step must surface this as
		// error_category=unreleased_not_found and Status=Failed.
		It(
			"rewrite_needed=true with no ## Unreleased heading → Status=Failed, error_category=unreleased_not_found, Commit not invoked",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					Expect(os.MkdirAll(workdir, 0o750)).To(Succeed())
					// No `## Unreleased` line — ReplaceUnreleasedBody will
					// return "unreleased header not found".
					content := []byte("## v0.9.0\n\n- old\n")
					Expect(
						os.WriteFile(filepath.Join(workdir, "CHANGELOG.md"), content, 0o600),
					).To(Succeed())
					return nil
				}
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), noUnreleasedTaskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))

				// Commit was NOT invoked — the failure occurs before commit.
				Expect(fakeOps.CommitCallCount()).To(Equal(0))

				got, err := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Outcome).To(Equal(pkg.ResultOutcomeFailed))
				Expect(got.ErrorCategory).To(Equal(git.ErrorCategoryUnreleasedNotFound))
			},
		)
	})

	Context("re-fire idempotency", func() {
		// A second invocation of Run against the same ## Plan MUST
		// produce exactly one new commit ahead of origin/master and
		// exactly one tag named vX.Y.Z on the local clone. The
		// contract "no duplicate" is enforced at the local-filesystem
		// layer by setupWorkdir's RemoveAll; this test asserts the
		// mock counts to prove the re-fire path is exercised
		// end-to-end without invoking Push.
		It("re-fire produces no duplicate commit and no duplicate tag", func() {
			fakeOps := &gitmocks.GitOps{}
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitReturns("abc1234", nil)
			fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
			fakeOps.TagReturns(nil)

			step := pkg.NewExecutionStep(fakeOps, "")

			// First invocation.
			md1, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())
			result1, err := step.Run(context.Background(), md1)
			Expect(err).NotTo(HaveOccurred())
			Expect(result1.Status).To(Equal(agentlib.AgentStatusDone))
			got1, err := agentlib.ExtractSection[pkg.ResultOutput](
				context.Background(),
				md1,
				"## Result",
			)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = os.RemoveAll(got1.Workdir)
			})

			// Second invocation against the same taskMD.
			md2, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())
			result2, err := step.Run(context.Background(), md2)
			Expect(err).NotTo(HaveOccurred())
			Expect(result2.Status).To(Equal(agentlib.AgentStatusDone))
			got2, err := agentlib.ExtractSection[pkg.ResultOutput](
				context.Background(),
				md2,
				"## Result",
			)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = os.RemoveAll(got2.Workdir)
			})

			Expect(fakeOps.CloneCallCount()).To(Equal(2))
			Expect(fakeOps.CommitCallCount()).To(Equal(2))
			Expect(fakeOps.TagCallCount()).To(Equal(2))
			Expect(fakeOps.PushCallCount()).To(Equal(0))
		})
	})

	Context("clone_url normalization end-to-end", func() {
		const sshTaskMD = `---
status: in_progress
phase: execution
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: git@github.com:bborbe/example.git
ref: master
current_version: v1.2.7
task_identifier: gh-release-bborbe-example-master-ssh
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "patch",
  "reasoning": "fix-only batch",
  "current_version": "v1.2.7",
  "next_version": "1.2.8",
  "next_version_header": "## v1.2.8",
  "header_prefix_style": "v",
  "bullets": ["fix: thing"]
}
` + "```" + `
`

		It("rewrites an SSH clone_url to token-authenticated HTTPS before Clone", func() {
			fakeOps := &gitmocks.GitOps{}
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitReturns("abc1234", nil)
			fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
			fakeOps.TagReturns(nil)

			step := pkg.NewExecutionStep(fakeOps, "test-token")
			md, err := agentlib.ParseMarkdown(context.Background(), sshTaskMD)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

			Expect(fakeOps.CloneCallCount()).To(Equal(1))
			_, gotCloneURL, _, _ := fakeOps.CloneArgsForCall(0)
			Expect(
				gotCloneURL,
			).To(Equal("https://x-access-token:test-token@github.com/bborbe/example.git"))
		})
	})

	DescribeTable(
		"normalizes clone_url before clone (empty token isolates normalization)",
		func(inputCloneURL, wantCloneURL string) {
			taskMD := `---
status: in_progress
phase: execution
task_identifier: gh-release-norm-table
clone_url: ` + inputCloneURL + `
ref: master
---

## Plan

` + "```json" + `
{"outcome":"ready","next_version":"1.2.8","next_version_header":"## v1.2.8"}
` + "```" + `
`
			fakeOps := &gitmocks.GitOps{}
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitReturns("abc1234", nil)
			fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
			fakeOps.TagReturns(nil)

			step := pkg.NewExecutionStep(fakeOps, "") // empty token → injectToken is a no-op
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			_, err = step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeOps.CloneCallCount()).To(Equal(1))
			_, gotCloneURL, _, _ := fakeOps.CloneArgsForCall(0)
			Expect(gotCloneURL).To(Equal(wantCloneURL))
		},
		Entry("scp form", "git@github.com:owner/repo.git", "https://github.com/owner/repo.git"),
		Entry(
			"ssh:// form",
			"ssh://git@github.com/owner/repo.git",
			"https://github.com/owner/repo.git",
		),
		Entry(
			"https with .git unchanged",
			"https://github.com/owner/repo.git",
			"https://github.com/owner/repo.git",
		),
		Entry(
			"https without .git unchanged",
			"https://github.com/owner/repo",
			"https://github.com/owner/repo",
		),
		Entry(
			"unrecognized form unchanged",
			"git://example.com/owner/repo.git",
			"git://example.com/owner/repo.git",
		),
	)

	Context("clone failure", func() {
		It("Clone returns error → Result(failed); Status=Failed; Commit NOT called", func() {
			fakeOps := &gitmocks.GitOps{}
			// Clone fails with an auth error.
			fakeOps.CloneReturns(errors.Errorf(context.Background(), "Authentication failed"))
			step := pkg.NewExecutionStep(fakeOps, "")
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
			Expect(fakeOps.CommitCallCount()).To(Equal(0))
			// Guard never reached — failure surfaced before Commit.
			Expect(fakeOps.CommittedFilesCallCount()).To(Equal(0))

			got, _ := agentlib.ExtractSection[pkg.ResultOutput](
				context.Background(),
				md,
				"## Result",
			)
			Expect(got.Outcome).To(Equal("failed"))
			Expect(string(got.ErrorCategory)).To(Equal("auth"))
		})
	})

	Context("changelog missing", func() {
		It(
			"Clone succeeds but CHANGELOG.md absent → Result(failed, error_category=changelog_missing)",
			func() {
				fakeOps := &gitmocks.GitOps{}
				// CloneStub creates the workdir but does NOT write CHANGELOG.md.
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					Expect(os.MkdirAll(workdir, 0o750)).To(Succeed())
					return nil
				}
				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(fakeOps.CommitCallCount()).To(Equal(0))
				// Guard never reached — failure surfaced before Commit.
				Expect(fakeOps.CommittedFilesCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("changelog_missing"))
			},
		)
	})

	Context("plan output validation", func() {
		It(
			"non-ready plan → Result(failed, error_category=unknown); Status=Failed; Clone NOT called",
			func() {
				nonReadyMD := `---
status: in_progress
phase: execution
task_identifier: gh-release-x-y-master-049b
clone_url: https://github.com/x/y.git
ref: master
---

## Plan

` + "```json" + `
{"outcome":"needs_input","reason":"upstream changelog regression"}
` + "```" + `
`
				fakeOps := &gitmocks.GitOps{}
				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), nonReadyMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(fakeOps.CloneCallCount()).To(Equal(0))
				// Guard never reached — failure surfaced before Clone.
				Expect(fakeOps.CommittedFilesCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("unknown"))
			},
		)
	})

	Describe("isSubsetIncludingChangelog", func() {
		DescribeTable(
			"asymmetric subset including CHANGELOG.md",
			func(committed, allowed []string, want bool) {
				Expect(pkg.IsSubsetIncludingChangelogForTest(committed, allowed)).To(Equal(want))
			},
			Entry(
				"committed ⊆ allowed + CHANGELOG.md present → true",
				[]string{"CHANGELOG.md", ".claude-plugin/plugin.json"},
				[]string{"CHANGELOG.md", ".claude-plugin/plugin.json"},
				true,
			),
			Entry(
				"committed ⊆ allowed (subset) + CHANGELOG.md present → true",
				[]string{"CHANGELOG.md"},
				[]string{"CHANGELOG.md", ".claude-plugin/plugin.json"},
				true,
			),
			Entry(
				"committed contains file NOT in allowed → false",
				[]string{"CHANGELOG.md", "README.md"},
				[]string{"CHANGELOG.md"},
				false,
			),
			Entry(
				"committed missing CHANGELOG.md → false",
				[]string{".claude-plugin/plugin.json"},
				[]string{"CHANGELOG.md", ".claude-plugin/plugin.json"},
				false,
			),
			Entry(
				"exact match → true",
				[]string{
					"CHANGELOG.md",
					".claude-plugin/plugin.json",
					".claude-plugin/marketplace.json",
				},
				[]string{
					"CHANGELOG.md",
					".claude-plugin/plugin.json",
					".claude-plugin/marketplace.json",
				},
				true,
			),
		)
	})

	Describe("deriveUnprefixedVersion", func() {
		DescribeTable(
			"strips ## prefix and v prefix",
			func(header, want string) {
				Expect(pkg.DeriveUnprefixedVersionForTest(header)).To(Equal(want))
			},
			Entry("## v0.10.0", "## v0.10.0", "0.10.0"),
			Entry("## 0.10.0", "## 0.10.0", "0.10.0"),
			Entry("0.10.0", "0.10.0", "0.10.0"),
			Entry("empty string → empty", "", ""),
		)
	})

	Context("plugin manifests", func() {
		const taskMDPlugin = `---
status: in_progress
phase: execution
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: https://github.com/bborbe/example.git
ref: master
current_version: v0.9.12
task_identifier: gh-release-bborbe-example-master-plugin
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "minor",
  "reasoning": "new feature",
  "current_version": "v0.9.12",
  "next_version": "0.10.0",
  "next_version_header": "## v0.10.0",
  "header_prefix_style": "v",
  "bullets": ["feat: new thing"]
}
` + "```" + `
`

		readFixture := func(name string) []byte {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			Expect(err).NotTo(HaveOccurred())
			return data
		}

		writeManifest := func(workdir, relPath, fixtureName string) {
			Expect(os.MkdirAll(filepath.Join(workdir, ".claude-plugin"), 0o750)).To(Succeed())
			Expect(
				os.WriteFile(filepath.Join(workdir, relPath), readFixture(fixtureName), 0o600),
			).To(Succeed())
		}

		writeChangelogAndBothManifests := func(workdir string) {
			writeChangelog(workdir)
			writeManifest(workdir, ".claude-plugin/plugin.json", "plugin.json.pre")
			writeManifest(workdir, ".claude-plugin/marketplace.json", "marketplace.json.pre")
		}

		It(
			"bumps plugin.json and marketplace.json to unprefixed semver; commits exactly those files plus CHANGELOG.md; guard passes",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelogAndBothManifests(workdir)
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, workdir, _ string, _ ...string) (string, error) {
					pluginActual, err := os.ReadFile(
						filepath.Join(workdir, ".claude-plugin", "plugin.json"),
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(pluginActual).To(Equal(readFixture("plugin.json.post")))

					marketplaceActual, err := os.ReadFile(
						filepath.Join(workdir, ".claude-plugin", "marketplace.json"),
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(marketplaceActual).To(Equal(readFixture("marketplace.json.post")))
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns(
					[]string{
						"CHANGELOG.md",
						".claude-plugin/plugin.json",
						".claude-plugin/marketplace.json",
					},
					nil,
				)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				_, _, _, commitPaths := fakeOps.CommitArgsForCall(0)
				Expect(
					commitPaths,
				).To(Equal([]string{"CHANGELOG.md", ".claude-plugin/plugin.json", ".claude-plugin/marketplace.json"}))

				Expect(fakeOps.TagCallCount()).To(Equal(1))
				Expect(fakeOps.PushCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("released"))
			},
		)

		It(
			"plugin.json only → commits {CHANGELOG.md, .claude-plugin/plugin.json}; guard passes",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					writeManifest(workdir, ".claude-plugin/plugin.json", "plugin.json.pre")
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, _, _ string, paths ...string) (string, error) {
					Expect(paths).To(Equal([]string{"CHANGELOG.md", ".claude-plugin/plugin.json"}))
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns(
					[]string{"CHANGELOG.md", ".claude-plugin/plugin.json"},
					nil,
				)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
			},
		)

		It(
			"marketplace.json only → commits {CHANGELOG.md, .claude-plugin/marketplace.json}; guard passes",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					writeManifest(
						workdir,
						".claude-plugin/marketplace.json",
						"marketplace.json.pre",
					)
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, _, _ string, paths ...string) (string, error) {
					Expect(
						paths,
					).To(Equal([]string{"CHANGELOG.md", ".claude-plugin/marketplace.json"}))
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns(
					[]string{"CHANGELOG.md", ".claude-plugin/marketplace.json"},
					nil,
				)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
			},
		)

		It("no .claude-plugin/ dir → commits only CHANGELOG.md; guard passes", func() {
			fakeOps := &gitmocks.GitOps{}
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitStub = func(_ context.Context, _, _ string, paths ...string) (string, error) {
				Expect(paths).To(Equal([]string{"CHANGELOG.md"}),
					"commit paths must be exactly [CHANGELOG.md] when no manifests exist")
				return "abc1234", nil
			}
			fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
			fakeOps.TagReturns(nil)

			step := pkg.NewExecutionStep(fakeOps, "")
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

			got, _ := agentlib.ExtractSection[pkg.ResultOutput](
				context.Background(),
				md,
				"## Result",
			)
			Expect(got.Outcome).To(Equal("released"))
		})

		It(
			"CommittedFiles returns unexpected file → Result(failed, error_category=unexpected_diff); Tag NOT called",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelogAndBothManifests(workdir)
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, _, _ string, _ ...string) (string, error) {
					return "def5678", nil
				}
				fakeOps.CommittedFilesReturns(
					[]string{
						"CHANGELOG.md",
						".claude-plugin/plugin.json",
						".claude-plugin/marketplace.json",
						"README.md",
					},
					nil,
				)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))

				Expect(fakeOps.CommittedFilesCallCount()).To(Equal(1))
				Expect(fakeOps.TagCallCount()).To(Equal(0))
				Expect(fakeOps.PushCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("unexpected_diff"))
				Expect(got.Tag).To(BeEmpty())
				Expect(got.CommitSHA).To(BeEmpty())
			},
		)

		It(
			"plugin.json is malformed JSON → Result(failed, error_category=plugin_manifest_invalid); Commit NOT called; Tag NOT called",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					Expect(
						os.MkdirAll(filepath.Join(workdir, ".claude-plugin"), 0o750),
					).To(Succeed())
					malformedPlugin := []byte(`{"name": "example", "version": }`)
					Expect(
						os.WriteFile(
							filepath.Join(workdir, ".claude-plugin", "plugin.json"),
							malformedPlugin,
							0o600,
						),
					).To(Succeed())
					return nil
				}
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))

				Expect(fakeOps.CommitCallCount()).To(Equal(0))
				Expect(fakeOps.CommittedFilesCallCount()).To(Equal(0))
				Expect(fakeOps.TagCallCount()).To(Equal(0))
				Expect(fakeOps.PushCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("plugin_manifest_invalid"))
				Expect(got.Error).To(ContainSubstring(".claude-plugin/plugin.json"))
				Expect(got.Tag).To(BeEmpty())
				Expect(got.CommitSHA).To(BeEmpty())
			},
		)

		It(
			"DetectManifests I/O error → Result(failed, error_category=unknown); Commit/Tag not called",
			func() {
				// chmod 0000 on Linux non-root blocks Stat of the children;
				// skip on platforms where this is unreliable (Darwin, root containers).
				if runtime.GOOS == "darwin" || os.Geteuid() == 0 {
					Skip("requires unprivileged Linux for non-IsNotExist Stat failure")
				}

				fakeOps := &gitmocks.GitOps{}
				var capturedWorkdir string
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					capturedWorkdir = workdir
					writeChangelog(workdir)
					// Create .claude-plugin as a directory with mode 0000 so Stat on
					// its children returns EACCES (a non-IsNotExist error path).
					Expect(
						os.MkdirAll(filepath.Join(workdir, ".claude-plugin"), 0o750),
					).To(Succeed())
					Expect(os.Chmod(filepath.Join(workdir, ".claude-plugin"), 0o000)).To(Succeed())
					return nil
				}
				// DeferCleanup restores the directory mode so the workdir-cleanup RemoveAll succeeds.
				DeferCleanup(func() {
					if capturedWorkdir != "" {
						const testDirRestoreMode os.FileMode = 0o750 //nolint:gosec // restore test fixture dir mode set on line 900
						_ = os.Chmod(
							filepath.Join(capturedWorkdir, ".claude-plugin"),
							testDirRestoreMode,
						)
					}
				})

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))

				Expect(fakeOps.CommitCallCount()).To(Equal(0))
				Expect(fakeOps.CommittedFilesCallCount()).To(Equal(0))
				Expect(fakeOps.TagCallCount()).To(Equal(0))
				Expect(fakeOps.PushCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("unknown"))
			},
		)

		It(
			"marketplace.json is malformed JSON → Result(failed, error_category=plugin_manifest_invalid); Commit NOT called; Tag NOT called",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					Expect(
						os.MkdirAll(filepath.Join(workdir, ".claude-plugin"), 0o750),
					).To(Succeed())
					malformedMarketplace := []byte(`{"metadata": {"version": }}`)
					Expect(
						os.WriteFile(
							filepath.Join(workdir, ".claude-plugin", "marketplace.json"),
							malformedMarketplace,
							0o600,
						),
					).To(Succeed())
					return nil
				}
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))

				Expect(fakeOps.CommitCallCount()).To(Equal(0))
				Expect(fakeOps.CommittedFilesCallCount()).To(Equal(0))
				Expect(fakeOps.TagCallCount()).To(Equal(0))
				Expect(fakeOps.PushCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("plugin_manifest_invalid"))
				Expect(got.Error).To(ContainSubstring(".claude-plugin/marketplace.json"))
				Expect(got.Tag).To(BeEmpty())
				Expect(got.CommitSHA).To(BeEmpty())
			},
		)

		// 8a — no-op manifest case: both manifests written to workdir (in allowed
		// set), but CommittedFiles returns only CHANGELOG.md. The manifest was
		// already at the target version so it never entered the commit.
		It(
			"no-op manifest (already at target version) → guard accepts; Tag called; outcome=released",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					// Write both manifests so DetectManifests finds them → allowed set.
					writeManifest(workdir, ".claude-plugin/plugin.json", "plugin.json.pre")
					writeManifest(
						workdir,
						".claude-plugin/marketplace.json",
						"marketplace.json.pre",
					)
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, _, _ string, paths ...string) (string, error) {
					return "abc1234", nil
				}
				// No manifests in the committed set — they were already at target version.
				fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(fakeOps.TagCallCount()).To(Equal(1))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("released"))
				Expect(string(got.ErrorCategory)).To(Equal(""))
			},
		)

		// 8b — mixed case: both manifests written to workdir, but only plugin.json
		// changed (marketplace.json already at target). CommittedFiles includes
		// CHANGELOG + plugin.json.
		It(
			"mixed (one manifest unchanged) → guard accepts; Tag called; outcome=released",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					writeManifest(workdir, ".claude-plugin/plugin.json", "plugin.json.pre")
					writeManifest(
						workdir,
						".claude-plugin/marketplace.json",
						"marketplace.json.pre",
					)
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, _, _ string, paths ...string) (string, error) {
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns(
					[]string{"CHANGELOG.md", ".claude-plugin/plugin.json"},
					nil,
				)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(fakeOps.TagCallCount()).To(Equal(1))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("released"))
				Expect(string(got.ErrorCategory)).To(Equal(""))
			},
		)

		// 8c — out-of-set reject: CommittedFiles includes a file not in the
		// allowed set (README.md). Guard must fail closed.
		It(
			"committed file outside allowed set → Result(failed, error_category=unexpected_diff); Tag NOT called",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, _, _ string, _ ...string) (string, error) {
					return "abc1234", nil
				}
				fakeOps.CommittedFilesReturns(
					[]string{"CHANGELOG.md", "README.md"},
					nil,
				)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(fakeOps.TagCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("unexpected_diff"))
			},
		)

		// 8d — missing changelog reject: CommittedFiles contains only the manifest
		// (CHANGELOG.md never entered the commit). Guard must fail closed.
		It(
			"committed without CHANGELOG.md → Result(failed, error_category=unexpected_diff); Tag NOT called",
			func() {
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					writeManifest(workdir, ".claude-plugin/plugin.json", "plugin.json.pre")
					return nil
				}
				fakeOps.CommitStub = func(_ context.Context, _, _ string, _ ...string) (string, error) {
					return "abc1234", nil
				}
				// CHANGELOG.md absent from committed set.
				fakeOps.CommittedFilesReturns([]string{".claude-plugin/plugin.json"}, nil)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "")
				md, err := agentlib.ParseMarkdown(context.Background(), taskMDPlugin)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(fakeOps.TagCallCount()).To(Equal(0))

				got, _ := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(got.Outcome).To(Equal("failed"))
				Expect(string(got.ErrorCategory)).To(Equal("unexpected_diff"))
			},
		)
	})

	// Spec 064 prompt 2 — the post-check tail. After ## Result(outcome=released)
	// is written on the success path, the execution step shells out `git
	// ls-remote refs/tags/<tag>` against the same authed URL the success
	// path's Clone used, and uses the answer to decide the terminal
	// verdict. The post-check is internal — verdict change rides on
	// md.Frontmatter and the new ## Resolution block. See
	// pkg/post_check_test.go for the unit-level coverage; this Context
	// covers the integration into Run.
	Context("post-check (spec 064)", func() {
		// sharedHappySetup wires the success-path mocks and returns the
		// canonical plan fixture (v1.2.8 / abc1234). The post-check
		// tail always sees tag=v1.2.8 and expectedSHA=abc1234 in the
		// success path; LsRemote stub is the per-test driver.
		sharedHappySetup := func() (*gitmocks.GitOps, *agentlib.Markdown) {
			fakeOps := &gitmocks.GitOps{}
			fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
				writeChangelog(workdir)
				return nil
			}
			fakeOps.CommitReturns("abc1234", nil)
			fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
			fakeOps.TagReturns(nil)
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())
			return fakeOps, md
		}

		It(
			"LsRemote returns expected SHA → verdict=released, status=completed, phase=done, ## Resolution appended",
			func() {
				fakeOps, md := sharedHappySetup()
				// LsRemote returns the SHA Commit just produced — released branch.
				fakeOps.LsRemoteReturns("abc1234", nil)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				// LsRemote was invoked exactly once.
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))

				// Frontmatter: status=completed / phase=done.
				Expect(md.Frontmatter["status"]).To(Equal("completed"))
				Expect(md.Frontmatter["phase"]).To(Equal("done"))

				// ## Resolution block present and shaped correctly.
				got, err := agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md,
					"## Resolution",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Verdict).To(Equal(pkg.ResolutionVerdictReleased))
				Expect(got.PlannedVersion).To(Equal("v1.2.8"))
				Expect(got.ObservedRemoteSHA).To(Equal("abc1234"))

				// Existing ## Result(outcome=released) STILL on disk — the
				// post-check appends ## Resolution, it does NOT replace ## Result.
				resultOutput, err := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(resultOutput.Outcome).To(Equal("released"))
				Expect(resultOutput.CommitSHA).To(Equal("abc1234"))
			},
		)

		It(
			"LsRemote returns a different SHA → verdict=superseded, status=completed, phase=done, ## Resolution cites observed SHA",
			func() {
				fakeOps, md := sharedHappySetup()
				// LsRemote returns a different SHA — superseded branch.
				fakeOps.LsRemoteReturns("deadbee", nil)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				Expect(md.Frontmatter["status"]).To(Equal("completed"))
				Expect(md.Frontmatter["phase"]).To(Equal("done"))

				got, err := agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md,
					"## Resolution",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Verdict).To(Equal(pkg.ResolutionVerdictSuperseded))
				Expect(got.PlannedVersion).To(Equal("v1.2.8"))
				Expect(got.ObservedRemoteSHA).To(Equal("deadbee"))
			},
		)

		It(
			"LsRemote returns (\"\", nil) → post-check no-op, existing ## Result(released) stands, no ## Resolution",
			func() {
				fakeOps, md := sharedHappySetup()
				// LsRemote returns empty — the post-check must NOT write
				// anything. The existing success-path verdict stands.
				fakeOps.LsRemoteReturns("", nil)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				// LsRemote was called once (we always consult the remote).
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))

				// Frontmatter NOT rewritten by the post-check — the
				// success path's status is what stands.
				Expect(md.Frontmatter["status"]).NotTo(Equal("completed"))

				// ## Resolution absent.
				_, err = agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md,
					"## Resolution",
				)
				Expect(
					err,
				).To(HaveOccurred(), "## Resolution must not exist on empty-result branch")
			},
		)

		It(
			"LsRemote returns error → post-check no-op, existing verdict stands, error logged via redactToken",
			func() {
				fakeOps, md := sharedHappySetup()
				// LsRemote error path.
				fakeOps.LsRemoteReturns(
					"",
					errors.Errorf(
						context.Background(),
						"git ls-remote: fatal: unable to access 'https://x-access-token:ghp_LEAKEDTOKEN@github.com/owner/repo'",
					),
				)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				// Frontmatter NOT rewritten.
				Expect(md.Frontmatter["status"]).NotTo(Equal("completed"))

				// ## Resolution absent.
				_, err = agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md,
					"## Resolution",
				)
				Expect(
					err,
				).To(HaveOccurred(), "## Resolution must not exist on LsRemote-error branch")
			},
		)

		It(
			"failure path participates: Clone fails → s.fail fires post-check with LsRemote returning a SHA → superseded → AgentStatusDone (no retry-storm)",
			func() {
				// Drive Run to a failure (Clone errors out), LsRemote
				// returns a non-empty SHA so the superseded branch
				// fires on the failure path. The verdict upgrade flips
				// the returned status from Failed to Done so the
				// controller does NOT retry on a completed task.
				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneReturns(errors.Errorf(context.Background(), "auth failed"))
				fakeOps.LsRemoteReturns("deadbee", nil)
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				// Verdict was upgraded → Done, not Failed. Without this
				// the controller would re-fire, redo clone/commit/tag
				// every cycle, and the idempotency guard would keep
				// the verdict stable but waste the work indefinitely.
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("ai_review"))

				// Post-check fired (Clone failure path still calls LsRemote).
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))

				// ## Result(failed) on disk.
				gotResult, err := agentlib.ExtractSection[pkg.ResultOutput](
					context.Background(),
					md,
					"## Result",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(gotResult.Outcome).To(Equal("failed"))

				// Frontmatter rewritten to completed/done — a later release won the slot.
				Expect(md.Frontmatter["status"]).To(Equal("completed"))
				Expect(md.Frontmatter["phase"]).To(Equal("done"))

				// ## Resolution block present, verdict=superseded.
				gotResolution, err := agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md,
					"## Resolution",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(gotResolution.Verdict).To(Equal(pkg.ResolutionVerdictSuperseded))
				Expect(gotResolution.ObservedRemoteSHA).To(Equal("deadbee"))
			},
		)

		It(
			"idempotency on already-terminal: frontmatter status=completed → LsRemote NEVER called, ## Resolution unchanged",
			func() {
				// Frontmatter says completed from the start — the
				// idempotency guard short-circuits BEFORE LsRemote.
				// The ## Resolution block in the fixture is the
				// pre-existing one; the post-check must NOT touch it.
				terminalMD := `---
status: completed
phase: done
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: https://github.com/bborbe/example.git
ref: master
current_version: v1.2.7
task_identifier: gh-release-bborbe-example-master-terminal
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "patch",
  "reasoning": "fix-only batch",
  "current_version": "v1.2.7",
  "next_version": "1.2.8",
  "next_version_header": "## v1.2.8",
  "header_prefix_style": "v",
  "bullets": ["fix: thing"]
}
` + "```" + `

## Result

` + "```json" + `
{"outcome":"released","path":"direct-push","commit_sha":"abc1234","tag":"v1.2.8","workdir":"/tmp/whatever","local_tag":"v1.2.8"}
` + "```" + `

## Resolution

` + "```json" + `
{"verdict":"released","planned_version":"v1.2.8","observed_remote_sha":"abc1234"}
` + "```" + `

`

				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					return nil
				}
				fakeOps.CommitReturns("abc1234", nil)
				fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
				fakeOps.TagReturns(nil)
				// LsRemote intentionally NOT stubbed; if the guard fails
				// to short-circuit, the counterfeiter zero-value return
				// ("", nil) is what would happen. The test asserts
				// LsRemoteCallCount() == 0 to prove the guard fired.

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				md, err := agentlib.ParseMarkdown(context.Background(), terminalMD)
				Expect(err).NotTo(HaveOccurred())

				// Snapshot pre-run typed contents — comparing the typed
				// struct (not raw bytes) survives benign agentlib
				// re-encoding (trailing newlines, key ordering) while
				// still proving the semantic state is unchanged.
				preResolution, err := agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md,
					"## Resolution",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(preResolution).NotTo(BeNil())

				_, err = step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())

				// LsRemote was NEVER called — the guard short-circuited
				// at the helper's first statement.
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(0))

				// ## Resolution typed content unchanged.
				postResolution, err := agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md,
					"## Resolution",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(postResolution).NotTo(BeNil())
				Expect(postResolution.Verdict).To(Equal(preResolution.Verdict))
				Expect(postResolution.PlannedVersion).To(Equal(preResolution.PlannedVersion))
				Expect(postResolution.ObservedRemoteSHA).To(Equal(preResolution.ObservedRemoteSHA))
			},
		)

		It(
			"idempotency on already-terminal aborted: frontmatter status=aborted → LsRemote NEVER called",
			func() {
				// Twin of the status=completed idempotency case: the
				// guard MUST short-circuit on aborted too, otherwise an
				// aborted task could be silently mutated by a re-fire.
				abortedMD := `---
status: aborted
phase: done
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: https://github.com/bborbe/example.git
ref: master
current_version: v1.2.7
task_identifier: gh-release-bborbe-example-master-aborted
---

# release task

## Plan

` + "```json" + `
{
  "outcome": "ready",
  "bump": "patch",
  "reasoning": "fix-only batch",
  "current_version": "v1.2.7",
  "next_version": "1.2.8",
  "next_version_header": "## v1.2.8",
  "header_prefix_style": "v",
  "bullets": ["fix: thing"]
}
` + "```" + `

`

				fakeOps := &gitmocks.GitOps{}
				fakeOps.CloneStub = func(_ context.Context, _, _, workdir string) error {
					writeChangelog(workdir)
					return nil
				}
				fakeOps.CommitReturns("abc1234", nil)
				fakeOps.CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)
				fakeOps.TagReturns(nil)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				md, err := agentlib.ParseMarkdown(context.Background(), abortedMD)
				Expect(err).NotTo(HaveOccurred())

				_, err = step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())

				// Guard fired before LsRemote — same contract as the
				// status=completed case.
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(0))

				// Status stays aborted; no ## Resolution block appended.
				Expect(md.Frontmatter["status"]).To(Equal("aborted"))
				_, found := md.FindSection("## Resolution")
				Expect(
					found,
				).To(BeFalse(), "post-check must not append ## Resolution on aborted")
			},
		)

		It(
			"idempotent re-fire: second Run against an already-released task does NOT re-consult the remote",
			func() {
				// Re-fire semantics: the FIRST run rewrote frontmatter
				// to status=completed and appended ## Resolution. The
				// SECOND run's post-check must see status=completed
				// and short-circuit BEFORE LsRemote — the call counter
				// stays at 1 across both runs and the ## Resolution
				// block typed-extracts to the SAME ResolutionOutput
				// (verdict / planned_version / observed_remote_sha).
				fakeOps, md1 := sharedHappySetup()
				fakeOps.LsRemoteReturns("abc1234", nil)

				step := pkg.NewExecutionStep(fakeOps, "test-token")
				_, err := step.Run(context.Background(), md1)
				Expect(err).NotTo(HaveOccurred())

				// First-run assertions: ## Resolution present, frontmatter rewritten.
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))
				Expect(md1.Frontmatter["status"]).To(Equal("completed"))
				firstResolution, err := agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md1,
					"## Resolution",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(firstResolution).NotTo(BeNil())

				// Second-run: simulate the persisted post-first-run
				// on-disk state by marshaling md1 and re-parsing. md2
				// carries the ## Resolution block AND the
				// status=completed frontmatter from the first run.
				firstBytes, err := md1.Marshal(context.Background())
				Expect(err).NotTo(HaveOccurred())
				md2, err := agentlib.ParseMarkdown(context.Background(), string(firstBytes))
				Expect(err).NotTo(HaveOccurred())
				Expect(md2.Frontmatter["status"]).To(Equal("completed"))

				// Re-run with the second MD — the success path's
				// Clone / Commit / Tag will run again (production
				// contract), but the post-check short-circuits at
				// the idempotency guard and NOT call LsRemote.
				_, err = step.Run(context.Background(), md2)
				Expect(err).NotTo(HaveOccurred())

				// LsRemote was called exactly once across BOTH runs
				// — the second run's post-check exited at the
				// idempotency guard.
				Expect(fakeOps.LsRemoteCallCount()).To(Equal(1))
				// The ## Resolution block typed-extracts to the same
				// verdict / planned_version / observed_remote_sha
				// as the first run — the helper did not double-write
				// or rewrite it.
				secondResolution, err := agentlib.ExtractSection[pkg.ResolutionOutput](
					context.Background(),
					md2,
					"## Resolution",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(secondResolution).NotTo(BeNil())
				Expect(secondResolution.Verdict).To(Equal(firstResolution.Verdict))
				Expect(secondResolution.PlannedVersion).To(Equal(firstResolution.PlannedVersion))
				Expect(
					secondResolution.ObservedRemoteSHA,
				).To(Equal(firstResolution.ObservedRemoteSHA))
			},
		)
	})
})

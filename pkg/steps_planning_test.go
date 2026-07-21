// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"

	agentlib "github.com/bborbe/agent"
	claudelib "github.com/bborbe/agent/claude"
	delivery "github.com/bborbe/agent/delivery"
	"github.com/bborbe/github-releaser-agent/mocks"
	pkg "github.com/bborbe/github-releaser-agent/pkg"
	"github.com/bborbe/github-releaser-agent/pkg/factory"
	"github.com/bborbe/github-releaser-agent/pkg/githubtags"
	"github.com/bborbe/github-releaser-agent/pkg/maintainerconfig"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// withChangelogRewriteTrue returns a MaintainerConfigFetcher mock whose
// Fetch returns a YAML byte slice with `release.changelogRewrite: true`.
// spec 059: tests that expect the 058 rewrite pipeline to run must use
// this helper so the planning step resolves the flag as true.
func withChangelogRewriteTrue() *mocks.MaintainerConfigFetcher {
	m := &mocks.MaintainerConfigFetcher{}
	m.FetchReturns(
		[]byte("release:\n  changelogRewrite: true\n"),
		nil,
	)
	return m
}

// withNoRemoteTags returns a TagsFetcher mock that reports no usable
// remote tag, so the planning step falls back to the frontmatter
// snapshot — preserving pre-spec-001 test expectations.
func withNoRemoteTags() *mocks.TagsFetcher {
	m := &mocks.TagsFetcher{}
	m.LatestSemverTagReturns("", githubtags.ErrNoTags)
	return m
}

var _ = Describe("steps_planning", func() {
	// spec 063 — pre-1.0 cap. The pre-1.0 cap is enforced in the
	// classifier's prompt (the rule that caps pre-1.0 breaking changes at
	// minor is in the embedded rules text). This block pins two end-to-end
	// envelopes:
	//
	//   (a) Pre-1.0 breaking-change input (vault-cli 2026-06-06 incident) +
	//       Claude returning bump=minor with the pre-1.0 reasoning string
	//       → outcome=ready, next_version=0.70.0 (the human-shipped shape).
	//   (b) Post-1.0 breaking-change input (v1.2.3) + Claude returning
	//       bump=major → outcome=needs_input, precondition_failed=
	//       major_bump_not_allowed (the spec-060 guard still trips).
	//
	// (a) is the spec's primary behavioral fix: prior to 063 a pre-1.0
	// rename would trip the major-bump guard and require a human operator
	// to override. After 063 the classifier caps the bump at minor and the
	// release proceeds unattended. The fixture replays the originating
	// incident so the audit trail is self-documenting.
	//
	// (b) is the spec's required negative evidence: the guard remains
	// intact for post-1.0 versions. Without (b) a future refactor could
	// silently extend the pre-1.0 cap to 1.x and pass (a) while breaking
	// the spec 060 contract.
	Context("pre-1.0 cap (spec 063)", func() {
		// Vault-cli 2026-06-06 regression: /refine-task → /plan-task rename
		// at v0.69.0 halted at planning with major_bump_not_allowed. The
		// spec-063 fix teaches the classifier to cap pre-1.0 breaking
		// changes at minor. This fixture replays the exact incident and
		// asserts the release proceeds unattended to outcome=ready with
		// next_version=0.70.0 (the human-shipped shape).
		vaultCliChangelog := []byte(
			"## Unreleased\n\n" +
				"- refactor: rename /refine-task to /plan-task\n\n" +
				"## v0.69.0\n\n- old\n",
		)

		It(
			"vault-cli v0.69.0 + rename bullet + Claude returns minor:pre-1.0 → outcome=ready, next_version=0.70.0",
			func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(vaultCliChangelog, nil)

				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(
					&claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"breaking change capped to minor due to pre-1.0 stream (current_version 0.69.0)"}`,
					},
					nil,
				)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false,
				)

				taskMD := "---\n" +
					"status: in_progress\n" +
					"phase: planning\n" +
					"assignee: github-releaser-agent\n" +
					"task_type: github-release\n" +
					"repo: bborbe/vault-cli\n" +
					"clone_url: https://github.com/bborbe/vault-cli.git\n" +
					"ref: master\n" +
					"current_version: 0.69.0\n" +
					"task_identifier: gh-release-bborbe-vault-cli-001\n" +
					"---\n\n" +
					"# release task\n"

				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())

				// Status/NextPhase: planning succeeded, advance to execution.
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("execution"))

				// ## Plan JSON content: outcome=ready, bump=minor, next_version=0.70.0.
				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(), md, "## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.Outcome).To(Equal(pkg.PlanOutcomeReady))
				Expect(plan.Bump).To(Equal("minor"))
				Expect(plan.CurrentVersion).To(Equal("0.69.0"))
				Expect(plan.NextVersion).To(Equal("0.70.0"))
				Expect(plan.NextVersionHeader).To(Equal("## v0.70.0"))
				Expect(plan.HeaderPrefixStyle).To(Equal("v"))
				Expect(plan.PreconditionFailed).To(BeEmpty())

				// FROZEN spec-047 escalation contract is NOT triggered on
				// the happy path: status/phase unchanged, assignee is
				// untouched (the planning step does not mutate the
				// frontmatter on the success path; the controller's
				// status→frontmatter switch handles phase advance).
				gotStatus, _ := md.Frontmatter.String("status")
				Expect(gotStatus).To(Equal("in_progress"))
				gotPhase, _ := md.Frontmatter.String("phase")
				Expect(gotPhase).To(Equal("planning"))
			},
		)

		// Post-1.0 fixture: v1.2.3 + breaking-change bullet + Claude
		// returns bump=major. The pre-1.0 cap does NOT apply (1.x is
		// post-1.0) so Claude legally returns major. With neither opt-in
		// present (allowMajorBumpConfig=false, allowMajor=false) the
		// disallowed major is CLAMPED to minor and the release ships —
		// it does not escalate to human_review.
		It(
			"post-1.0 v1.2.3 + breaking-change bullet + Claude returns major, major not allowed: clamps to minor (no escalation)",
			func() {
				post1Changelog := []byte(
					"## Unreleased\n\n" +
						"- refactor(lib): rename TaskTypeClaude → TaskTypeLLM\n\n" +
						"## v1.2.3\n\n- old\n",
				)
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(post1Changelog, nil)

				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(
					&claudelib.ClaudeResult{
						Result: `{"bump":"major","reasoning":"BREAKING CHANGE: refactor(lib) renames TaskTypeClaude → TaskTypeLLM"}`,
					},
					nil,
				)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false,
				)

				taskMD := "---\n" +
					"status: in_progress\n" +
					"phase: planning\n" +
					"assignee: github-releaser-agent\n" +
					"task_type: github-release\n" +
					"repo: bborbe/post-1-0-lib\n" +
					"clone_url: https://github.com/bborbe/post-1-0-lib.git\n" +
					"ref: master\n" +
					"current_version: v1.2.3\n" +
					"task_identifier: gh-release-bborbe-post-1-0-001\n" +
					"---\n\n" +
					"# release task\n"

				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())

				// Status: Done, advancing to execution — the clamp ships
				// the release instead of escalating to human_review.
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("execution"))

				// ## Plan JSON: the would-be major is capped to minor.
				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(), md, "## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.Outcome).To(Equal(pkg.PlanOutcomeReady))
				Expect(plan.Bump).To(Equal("minor"))
				Expect(plan.NextVersion).To(Equal("1.3.0"))

				// No escalation: assignee is NOT cleared and
				// previous_assignee is NOT set.
				gotAssignee, _ := md.Frontmatter.String("assignee")
				Expect(gotAssignee).To(Equal("github-releaser-agent"))
				gotPrevAssignee, _ := md.Frontmatter.String("previous_assignee")
				Expect(gotPrevAssignee).To(Equal(""))
				gotStatus, _ := md.Frontmatter.String("status")
				Expect(gotStatus).To(Equal("in_progress"))
			},
		)
	})

	Context("prompt assembly with current_version (spec 063)", func() {
		It(
			"assembled prompt contains ## Current version section before ## Bullets to classify",
			func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(
					[]byte(
						"## Unreleased\n\n- refactor: rename /refine-task to /plan-task\n\n## v0.69.0\n\n- old\n",
					),
					nil,
				)

				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(&claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"stub"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false,
				)

				taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v0.69.0\ntask_identifier: gh-release-bborbe-vault-cli-001\n---\n\n# release task\n"

				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				_, err = step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeRunner.RunCallCount()).To(Equal(1))

				// Inspect the prompt string the runner received.
				_, promptArg := fakeRunner.RunArgsForCall(0)

				// (a) ## Current version heading present.
				Expect(promptArg).To(ContainSubstring("## Current version"))

				// (b) The literal version string is in the section body.
				Expect(promptArg).To(ContainSubstring("v0.69.0"))

				// (c) ## Current version appears BEFORE ## Bullets to classify.
				currentVersionIdx := strings.Index(promptArg, "## Current version")
				bulletsIdx := strings.Index(promptArg, "## Bullets to classify")
				Expect(currentVersionIdx).To(BeNumerically(">=", 0))
				Expect(bulletsIdx).To(BeNumerically(">=", 0))
				Expect(currentVersionIdx).To(BeNumerically("<", bulletsIdx))

				// (d) The embedded rules (returned by BumpClassificationPrompt)
				// appear BEFORE ## Current version. This proves the version
				// section is sandwiched between the rules and the bullets,
				// not prepended to the entire prompt.
				rulesIdx := strings.Index(promptArg, "# Classify the next semantic-version bump")
				Expect(rulesIdx).To(Equal(0))
				Expect(currentVersionIdx).To(BeNumerically(">", rulesIdx))
			},
		)
	})

	Context("major-bump policy injection + clamp (classifier-clamp)", func() {
		It("major not allowed: injects the forbid-major policy into the prompt", func() {
			fakeFetcher := &mocks.Fetcher{}
			fakeFetcher.FetchReturns(
				[]byte(
					"## Unreleased\n\n- refactor(lib): rename Foo → Bar\n\n## v1.2.3\n\n- old\n",
				),
				nil,
			)
			fakeRunner := &mocks.ClaudeRunnerMock{}
			fakeRunner.RunReturns(&claudelib.ClaudeResult{
				Result: `{"bump":"minor","reasoning":"capped"}`,
			}, nil)

			step := pkg.NewPlanningStep(
				fakeRunner,
				fakeFetcher,
				&mocks.MaintainerConfigFetcher{},
				withNoRemoteTags(),
				false,
			)

			taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/lib\nclone_url: https://github.com/bborbe/lib.git\nref: master\ncurrent_version: v1.2.3\ntask_identifier: gh-release-bborbe-lib-001\n---\n\n# release task\n"
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			_, err = step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeRunner.RunCallCount()).To(Equal(1))
			_, promptArg := fakeRunner.RunArgsForCall(0)
			Expect(promptArg).To(ContainSubstring("## Major-bump policy"))
			Expect(promptArg).To(ContainSubstring("MUST NOT return `major`"))
		})

		It("major allowed (per-run override): omits the policy and publishes major", func() {
			fakeFetcher := &mocks.Fetcher{}
			fakeFetcher.FetchReturns(
				[]byte(
					"## Unreleased\n\n- refactor(lib): rename Foo → Bar\n\n## v1.2.3\n\n- old\n",
				),
				nil,
			)
			fakeRunner := &mocks.ClaudeRunnerMock{}
			fakeRunner.RunReturns(&claudelib.ClaudeResult{
				Result: `{"bump":"major","reasoning":"BREAKING CHANGE: rename Foo"}`,
			}, nil)

			// allowMajor=true (per-run override) → full range, no clamp.
			step := pkg.NewPlanningStep(
				fakeRunner, fakeFetcher, &mocks.MaintainerConfigFetcher{}, withNoRemoteTags(), true,
			)

			taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/lib\nclone_url: https://github.com/bborbe/lib.git\nref: master\ncurrent_version: v1.2.3\ntask_identifier: gh-release-bborbe-lib-002\n---\n\n# release task\n"
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeRunner.RunCallCount()).To(Equal(1))
			_, promptArg := fakeRunner.RunArgsForCall(0)
			Expect(promptArg).NotTo(ContainSubstring("## Major-bump policy"))

			Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
			plan, err := agentlib.ExtractSection[pkg.PlanOutput](
				context.Background(), md, "## Plan",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Bump).To(Equal("major"))
			Expect(plan.NextVersion).To(Equal("2.0.0"))
		})

		It("major allowed (repo YAML opt-in): omits the policy and publishes major", func() {
			fakeFetcher := &mocks.Fetcher{}
			fakeFetcher.FetchReturns(
				[]byte(
					"## Unreleased\n\n- refactor(lib): rename Foo → Bar\n\n## v1.2.3\n\n- old\n",
				),
				nil,
			)
			fakeRunner := &mocks.ClaudeRunnerMock{}
			fakeRunner.RunReturns(&claudelib.ClaudeResult{
				Result: `{"bump":"major","reasoning":"BREAKING CHANGE: rename Foo"}`,
			}, nil)

			// Repo opt-in via .maintainer.yaml (release.allowMajorBump: true);
			// per-run override OFF → config alone permits the full range.
			maintainerFetcher := &mocks.MaintainerConfigFetcher{}
			maintainerFetcher.FetchReturns([]byte("release:\n  allowMajorBump: true\n"), nil)
			step := pkg.NewPlanningStep(
				fakeRunner,
				fakeFetcher,
				maintainerFetcher,
				withNoRemoteTags(),
				false,
			)

			taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/lib\nclone_url: https://github.com/bborbe/lib.git\nref: master\ncurrent_version: v1.2.3\ntask_identifier: gh-release-bborbe-lib-003\n---\n\n# release task\n"
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeRunner.RunCallCount()).To(Equal(1))
			_, promptArg := fakeRunner.RunArgsForCall(0)
			Expect(promptArg).NotTo(ContainSubstring("## Major-bump policy"))

			Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
			plan, err := agentlib.ExtractSection[pkg.PlanOutput](
				context.Background(), md, "## Plan",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Bump).To(Equal("major"))
			Expect(plan.NextVersion).To(Equal("2.0.0"))
		})
	})

	Describe("PlanningStep", func() {
		Context("happy path", func() {
			It("ready path: emits ## Plan with outcome=ready and NextPhase=execution", func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(
					[]byte("## Unreleased\n\n- feat: add foo\n- fix: bar\n\n## v1.7.7\n\n- old\n"),
					nil,
				)

				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(&claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false, // spec 060: per-run allowMajor; false unless the test exercises the override path.
				)
				// maintainerConfigFetcher mock returns nil/nil by default — yields changelogRewrite=false via Parse(empty) contract.

				taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-001\n---\n\n# release task\n"

				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(result.NextPhase).To(Equal("execution"))
				Expect(fakeFetcher.FetchCallCount()).To(Equal(1))

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(),
					md,
					"## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.Outcome).To(Equal("ready"))
				Expect(plan.Bump).To(Equal("minor"))
				Expect(plan.CurrentVersion).To(Equal("v1.7.7"))
				Expect(plan.NextVersion).To(Equal("1.8.0"))
				Expect(plan.NextVersionHeader).To(Equal("## v1.8.0"))
				Expect(plan.HeaderPrefixStyle).To(Equal("v"))
				Expect(plan.Bullets).To(ContainElements("feat: add foo", "fix: bar"))
			})
		})

		Context("P1 escalation", func() {
			It(
				"P1 escalation: ## Unreleased not first → outcome=needs_input + assignee cleared",
				func() {
					badChangelog := []byte(
						"# Changelog\n\nIntro text.\n\n## v1.2.6\n\n- old release\n\n## Unreleased\n\n- new bullet\n",
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(badChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{} // not called on escalation

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						&mocks.MaintainerConfigFetcher{},
						withNoRemoteTags(),
						false, // spec 060
					)
					// maintainerConfigFetcher mock returns nil/nil by default — never reached on P1 escalation.

					taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.2.6\ntask_identifier: gh-release-bborbe-maintainer-master-001\n---\n\n# release task\n"

					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())

					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusNeedsInput))
					// NextPhase empty — caller stays in planning per spec 047 Desired Behavior 6.
					Expect(result.NextPhase).To(BeEmpty())

					// Fetcher called, Claude NOT called (escalation short-circuits before claude).
					Expect(fakeRunner.RunCallCount()).To(Equal(0))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal("needs_input"))
					Expect(plan.PreconditionFailed).To(Equal("P1_unreleased_not_first"))
					Expect(plan.Reason).To(ContainSubstring("not the first ## section"))

					// Frontmatter mutations:
					gotAssignee, _ := md.Frontmatter.String("assignee")
					Expect(gotAssignee).To(Equal(""))
					gotPrevAssignee, _ := md.Frontmatter.String("previous_assignee")
					Expect(gotPrevAssignee).To(Equal("github-releaser-agent"))
					gotStatus, _ := md.Frontmatter.String("status")
					Expect(gotStatus).To(Equal("in_progress"))
					gotPhase, _ := md.Frontmatter.String("phase")
					Expect(gotPhase).To(Equal("planning"))
				},
			)
		})

		Context("missing frontmatter", func() {
			It(
				"missing clone_url → outcome=needs_input + precondition_failed=missing_frontmatter_clone_url",
				func() {
					fakeFetcher := &mocks.Fetcher{}
					fakeRunner := &mocks.ClaudeRunnerMock{}
					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						&mocks.MaintainerConfigFetcher{},
						withNoRemoteTags(),
						false, // spec 060
					)
					// maintainerConfigFetcher mock returns nil/nil by default — never reached on missing-frontmatter.

					taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-001\n---\n"
					// clone_url intentionally missing

					md, _ := agentlib.ParseMarkdown(context.Background(), taskMD)
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusNeedsInput))
					Expect(fakeFetcher.FetchCallCount()).To(Equal(0))

					plan, _ := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(plan.Outcome).To(Equal("needs_input"))
					Expect(plan.PreconditionFailed).To(Equal("missing_frontmatter_clone_url"))
				},
			)
		})

		Context("fetch error", func() {
			It("fetcher transport error → Status=Failed", func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(nil, stderrors.New("dial tcp: connection refused"))
				fakeRunner := &mocks.ClaudeRunnerMock{}

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false, // spec 060
				)
				// maintainerConfigFetcher mock returns nil/nil by default — never reached on CHANGELOG fetch error.

				taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-001\n---\n"

				md, _ := agentlib.ParseMarkdown(context.Background(), taskMD)
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.Message).To(ContainSubstring("fetch CHANGELOG.md"))
			})
		})

		Context("claude parse error", func() {
			It("claude returns malformed JSON → Status=Failed", func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns([]byte("## Unreleased\n\n- feat: x\n"), nil)
				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(&claudelib.ClaudeResult{Result: "not-json-at-all"}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false, // spec 060
				)
				// maintainerConfigFetcher mock returns nil/nil by default — yields changelogRewrite=false (no rewrite call).

				taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-001\n---\n"

				md, _ := agentlib.ParseMarkdown(context.Background(), taskMD)
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
				Expect(result.Message).To(ContainSubstring("parse bump verdict"))
			})
		})

		Context("bad current_version", func() {
			It(
				"malformed current_version → outcome=needs_input + precondition_failed=bad_current_version",
				func() {
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns([]byte("## Unreleased\n\n- feat: x\n"), nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(
						&claudelib.ClaudeResult{Result: `{"bump":"minor","reasoning":"x"}`},
						nil,
					)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						&mocks.MaintainerConfigFetcher{},
						withNoRemoteTags(),
						false, // spec 060
					)
					// maintainerConfigFetcher mock returns nil/nil by default — never reached on bad current_version (escalation after bump).

					taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: garbage\ntask_identifier: gh-release-001\n---\n"

					md, _ := agentlib.ParseMarkdown(context.Background(), taskMD)
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusNeedsInput))
					plan, _ := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(plan.Outcome).To(Equal("needs_input"))
					Expect(plan.PreconditionFailed).To(Equal("bad_current_version"))
				},
			)
		})

		Context("P2 escalation", func() {
			It(
				"empty Unreleased bullets → outcome=needs_input + precondition_failed=P2_unreleased_empty",
				func() {
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns([]byte("## Unreleased\n\n## v1.0.0\n\n- old\n"), nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						&mocks.MaintainerConfigFetcher{},
						withNoRemoteTags(),
						false, // spec 060
					)
					// maintainerConfigFetcher mock returns nil/nil by default — never reached on P2 escalation.

					taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.0.0\ntask_identifier: gh-release-001\n---\n"

					md, _ := agentlib.ParseMarkdown(context.Background(), taskMD)
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusNeedsInput))
					plan, _ := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(plan.Outcome).To(Equal("needs_input"))
					Expect(plan.PreconditionFailed).To(Equal("P2_unreleased_empty"))
				},
			)
		})

		Context("idempotency", func() {
			It("idempotent: re-running with existing ## Plan replaces it", func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(
					[]byte("## Unreleased\n\n- feat: add foo\n- fix: bar\n\n## v1.7.7\n\n- old\n"),
					nil,
				)
				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(&claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false, // spec 060
				)
				// maintainerConfigFetcher mock returns nil/nil by default — yields changelogRewrite=false (no rewrite call).

				taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-001\n---\n\n# release task\n\n## Plan\n\n```json\n{\"outcome\":\"stale\"}\n```\n"

				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				// After Run, there should be exactly one ## Plan section with fresh outcome
				var planCount int
				for _, sec := range md.Sections {
					if sec.Heading == "## Plan" {
						planCount++
					}
				}
				Expect(planCount).To(Equal(1))

				// And the plan should be fresh (not "stale")
				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(),
					md,
					"## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.Outcome).To(Equal("ready"))
			})
		})

		// spec 058 — planning step now invokes Claude twice: once for the
		// bump verdict, once for the rewrite verdict. The fixtures below use
		// RunReturnsOnCall(0, ...) and RunReturnsOnCall(1, ...) so the mock
		// dispatches by call index. Order matters: bump first, rewrite second.
		Context("rewrite decision", func() {
			const taskMD = "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-rewrite\n---\n\n# release task\n"

			It("clean Unreleased → rewrite_needed=false with empty rewritten_unreleased", func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(
					[]byte("## Unreleased\n\n- feat: add foo\n- fix: bar\n\n## v1.7.7\n\n- old\n"),
					nil,
				)
				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)
				fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
					Result: `{"rewrite_needed":false,"rewritten_unreleased":"","reasoning":"all bullets already conform"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					withChangelogRewriteTrue(),
					withNoRemoteTags(),
					false,
				) // spec 060
				// spec 059: opt-in flag true → 058 rewrite pipeline runs.
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
				Expect(fakeRunner.RunCallCount()).To(Equal(2))

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(),
					md,
					"## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.RewriteNeeded).To(BeFalse())
				Expect(plan.RewrittenUnreleased).To(BeEmpty())
				Expect(plan.OriginalUnreleased).NotTo(BeEmpty())
			})

			It(
				"noisy git log dump → rewrite_needed=true with every line conventional-prefix-conformant",
				func() {
					noisyBody := "- abc1234 2026-05-12 foo author — bump foo\n" +
						"- def5678 2026-05-13 bar author — update docs\n" +
						"- 9abc012 2026-05-14 baz author — internal rename\n"
					fixture := "## Unreleased\n\n" + noisyBody + "## v1.7.7\n\n- old\n"
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns([]byte(fixture), nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)
					fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
						Result: `{"rewrite_needed":true,"rewritten_unreleased":"- chore: bump foo\n- docs: update docs\n- refactor: internal rename\n","reasoning":"reframed raw git log lines"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						withChangelogRewriteTrue(),
						withNoRemoteTags(),
						false,
					) // spec 060
					// spec 059: opt-in flag true → 058 rewrite pipeline runs.
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.RewriteNeeded).To(BeTrue())
					Expect(plan.RewrittenUnreleased).NotTo(BeEmpty())

					// Every non-blank line must match a conventional prefix.
					prefixRegex := `^- (feat|fix|refactor|chore|docs|test|build|ci|perf|style)(\([^)]*\))?:\s+\S`
					for _, line := range strings.Split(plan.RewrittenUnreleased, "\n") {
						if strings.TrimSpace(line) == "" {
							continue
						}
						Expect(line).To(
							MatchRegexp(prefixRegex),
							"line %q in rewritten_unreleased does not match conventional prefix",
							line,
						)
					}
				},
			)

			It("missing-prefix entry → rewrite adds prefix", func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(
					[]byte("## Unreleased\n\n- add foo\n\n## v1.7.7\n\n- old\n"),
					nil,
				)
				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)
				fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
					Result: `{"rewrite_needed":true,"rewritten_unreleased":"- feat: add foo\n","reasoning":"added missing feat: prefix"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					withChangelogRewriteTrue(),
					withNoRemoteTags(),
					false,
				) // spec 060
				// spec 059: opt-in flag true → 058 rewrite pipeline runs.
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(),
					md,
					"## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.RewriteNeeded).To(BeTrue())
				Expect(plan.RewrittenUnreleased).To(ContainSubstring("feat: add foo"))
			})

			It(
				"chore: bump dump (10 lines) → folded into a single dependency-updates entry",
				func() {
					var dumpLines []string
					for i := 0; i < 10; i++ {
						dumpLines = append(dumpLines, "- chore: bump x-v0.0.0")
					}
					dump := strings.Join(dumpLines, "\n")
					fixture := "## Unreleased\n\n" + dump + "\n\n## v1.7.7\n\n- old\n"
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns([]byte(fixture), nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
						Result: `{"bump":"patch","reasoning":"chore dump"}`,
					}, nil)
					fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
						Result: `{"rewrite_needed":true,"rewritten_unreleased":"- chore: routine dependency updates\n","reasoning":"folded 10 adjacent chore bump lines into one entry"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						withChangelogRewriteTrue(),
						withNoRemoteTags(),
						false,
					) // spec 060
					// spec 059: opt-in flag true → 058 rewrite pipeline runs.
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.RewriteNeeded).To(BeTrue())
					Expect(
						plan.RewrittenUnreleased,
					).To(ContainSubstring("routine dependency updates"))

					// Count the non-blank bullet lines: must be exactly 1.
					bulletCount := 0
					for _, line := range strings.Split(plan.RewrittenUnreleased, "\n") {
						if strings.HasPrefix(line, "- ") {
							bulletCount++
						}
					}
					Expect(bulletCount).To(Equal(1))
				},
			)

			It("captures original_unreleased verbatim regardless of rewrite decision", func() {
				noisyBody := "- abc1234 2026-05-12 foo author — bump foo\n" +
					"- def5678 2026-05-13 bar author — update docs\n" +
					"- 9abc012 2026-05-14 baz author — internal rename\n"
				fixture := "## Unreleased\n\n" + noisyBody + "## v1.7.7\n\n- old\n"
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns([]byte(fixture), nil)
				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)
				fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
					Result: `{"rewrite_needed":true,"rewritten_unreleased":"- chore: bump foo\n- docs: update docs\n- refactor: internal rename\n","reasoning":"reframed raw git log lines"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					withChangelogRewriteTrue(),
					withNoRemoteTags(),
					false,
				) // spec 060
				// spec 059: opt-in flag true → 058 rewrite pipeline runs.
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(),
					md,
					"## Plan",
				)
				Expect(err).NotTo(HaveOccurred())

				// The captured original must BYTE-EQUAL the body slice that
				// ExtractUnreleasedBody would emit. That is: every line
				// AFTER the "## Unreleased" heading up to (but excluding)
				// the next "## " heading, with lines joined by "\n".
				// This is the security-relevant invariant: capture-time
				// must match the bytes ai-review later reads.
				expected := "\n" + noisyBody
				Expect(plan.OriginalUnreleased).To(Equal(expected))
			})
		})

		// spec 059 prompt 4 — Req M2: re-fire after a transient
		// rewrite-LLM failure must reuse the cached bump verdict so
		// the bump LLM call is NOT re-invoked. The cache lives in
		// the prior ## Plan section — the controller persists the
		// task page between fires and the next fire reads it fresh.
		//
		// The test models the production round-trip via Marshal +
		// ParseMarkdown to obtain a fresh *Markdown for Run #2, NOT
		// a re-used in-memory one. In-memory re-use would prove
		// nothing about the on-disk round-trip.
		Context("bump verdict cache (re-fire after rewrite LLM transient failure)", func() {
			const taskMD = "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-m2\n---\n\n# release task\n"

			It(
				"re-fire after rewrite failure reuses cached bump verdict across write+reload",
				func() {
					ctx := context.Background()
					fixture := "## Unreleased\n\n- feat: add foo\n\n## v1.7.7\n\n- old\n"
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns([]byte(fixture), nil)

					fakeRunner := &mocks.ClaudeRunnerMock{}
					// Run #1: bump LLM call (#0) succeeds, rewrite LLM call (#1) fails.
					fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat detected"}`,
					}, nil)
					fakeRunner.RunReturnsOnCall(1, nil, stderrors.New("rewrite transient"))
					// Run #2: ONLY the rewrite LLM should fire (call index 2).
					// Bump LLM call must NOT be invoked again — the cache short-circuits it.
					fakeRunner.RunReturnsOnCall(2, &claudelib.ClaudeResult{
						Result: `{"rewrite_needed":false,"rewritten_unreleased":"","reasoning":"already conforms"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						withChangelogRewriteTrue(),
						withNoRemoteTags(),
						false,
					) // spec 060

					// Run #1 — fresh task page.
					md1, err := agentlib.ParseMarkdown(ctx, taskMD)
					Expect(err).NotTo(HaveOccurred())
					res1, err := step.Run(ctx, md1)
					Expect(err).NotTo(HaveOccurred())
					Expect(res1.Status).To(Equal(agentlib.AgentStatusFailed))
					Expect(
						fakeRunner.RunCallCount(),
					).To(Equal(2))
					// bump + rewrite both fired in Run #1

					// CRITICAL: model the production round-trip. The controller
					// persists the task page after each fire and the next
					// fire reads it fresh — re-using the in-memory `md1` would
					// not prove the cache survives the on-disk round-trip.
					// Serialize via (*Markdown).Marshal then re-parse via
					// ParseMarkdown to obtain a fresh *Markdown for Run #2.
					serialized, err := md1.Marshal(ctx)
					Expect(err).NotTo(HaveOccurred())
					Expect(serialized).To(ContainSubstring("## Plan"))
					// json.MarshalIndent (used by MarshalSectionTyped) inserts
					// a space after `:` so the actual on-disk form is
					// `"bump": "minor"` — assert on that.
					Expect(serialized).To(ContainSubstring(`"bump": "minor"`))

					md2, err := agentlib.ParseMarkdown(ctx, serialized)
					Expect(err).NotTo(HaveOccurred())

					// Sanity: the re-parsed plan section MUST carry bump=minor —
					// this is the precondition the cache lookup depends on. If
					// this fails, the M2 design needs revisiting (the cache
					// cannot survive the round-trip).
					prior, perr := agentlib.ExtractSection[pkg.PlanOutput](ctx, md2, "## Plan")
					Expect(perr).NotTo(HaveOccurred())
					Expect(prior.Bump).To(Equal("minor"))

					// Run #2 — fresh *Markdown re-parsed from serialized form.
					// Expect Done and ONLY ONE additional LLM call (the rewrite
					// retry). Total = 3, proving bump LLM was NOT re-fired.
					res2, err := step.Run(ctx, md2)
					Expect(err).NotTo(HaveOccurred())
					Expect(res2.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(
						fakeRunner.RunCallCount(),
					).To(Equal(3))
					// +1 rewrite only; bump NOT re-fired

					// Defensive: confirm the third runner call was the rewrite
					// prompt, not a re-issued bump prompt. Use RunArgsForCall(2)
					// to inspect.
					_, promptArg := fakeRunner.RunArgsForCall(2)
					Expect(promptArg).To(ContainSubstring("rewrite"))
				},
			)
		})

		// spec 059 prompt 4 — Req M4: surface .maintainer.yaml transport
		// fetch failures on the task page (config_fetch_warning) so a
		// repo that opted into rewrite is not silently downgraded on a
		// transient flake. Three specs:
		//   - non-404 fetch error → warning populated, ChangelogRewrite=false
		//   - happy path → warning empty
		//   - 404 (ErrFileNotFound) → warning empty (legitimate-absent)
		Context("config_fetch_warning surfacing", func() {
			const taskMD = "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-m4\n---\n\n# release task\n"
			fixture := "## Unreleased\n\n- feat: add foo\n\n## v1.7.7\n\n- old\n"

			It(
				"non-404 .maintainer.yaml fetch error surfaces config_fetch_warning on plan and logs glog.Warningf",
				func() {
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns([]byte(fixture), nil)

					// .maintainer.yaml fetcher returns a transport error (not ErrFileNotFound).
					fakeMaintainerCfg := &mocks.MaintainerConfigFetcher{}
					fakeMaintainerCfg.FetchReturns(
						nil,
						stderrors.New("dial tcp api.github.com:443: connection refused"),
					)

					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat detected"}`,
					}, nil)
					// Rewrite LLM should NOT fire because changelogRewrite resolves to false.

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						fakeMaintainerCfg,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					// Only the bump LLM fired (no rewrite — flag defaulted to false).
					Expect(fakeRunner.RunCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(), md, "## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal(pkg.PlanOutcomeReady))
					Expect(plan.ConfigFetchWarning).NotTo(BeEmpty())
					Expect(
						plan.ConfigFetchWarning,
					).To(ContainSubstring(".maintainer.yaml fetch failed"))
					Expect(plan.ConfigFetchWarning).To(ContainSubstring("connection refused"))
					// ChangelogRewrite resolved to default false.
					Expect(plan.ChangelogRewrite).NotTo(BeNil())
					Expect(*plan.ChangelogRewrite).To(BeFalse())
				},
			)

			It("happy path leaves config_fetch_warning empty", func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns([]byte(fixture), nil)

				fakeMaintainerCfg := &mocks.MaintainerConfigFetcher{}
				fakeMaintainerCfg.FetchReturns(
					[]byte("release:\n  autoRelease: true\n"),
					nil,
				)

				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(&claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					fakeMaintainerCfg,
					withNoRemoteTags(),
					false,
				) // spec 060
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())
				_, err = step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(), md, "## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.ConfigFetchWarning).To(BeEmpty())
			})

			It(
				"ErrFileNotFound leaves config_fetch_warning empty (legitimate-absent file is not a warning)",
				func() {
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns([]byte(fixture), nil)

					fakeMaintainerCfg := &mocks.MaintainerConfigFetcher{}
					fakeMaintainerCfg.FetchReturns(nil, maintainerconfig.ErrFileNotFound)

					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(&claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						fakeMaintainerCfg,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					_, err = step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(), md, "## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.ConfigFetchWarning).To(BeEmpty())
				},
			)
		})

		// spec 059 — the release.changelogRewrite opt-in flag in
		// .maintainer.yaml gates the 058 rewrite LLM call. The
		// maintainerConfigFetcher mock is wired per test to exercise
		// the four resolved values (false, true, network-error,
		// parse-error) plus the three "absent config" cases (404, nil
		// bytes, no release: block). Each It asserts the LLM call
		// count, the resolved PlanOutput.ChangelogRewrite value, and
		// the marshaled JSON encoding contract.
		Context("changelogRewrite opt-in flag", func() {
			const taskMD = "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-059\n---\n\n# release task\n"
			cleanChangelog := []byte(
				"## Unreleased\n\n- feat: add foo\n- fix: bar\n\n## v1.7.7\n\n- old\n",
			)
			noisyChangelog := []byte(
				"## Unreleased\n\n" +
					"- abc1234 2026-05-12 foo author — bump foo\n" +
					"- def5678 2026-05-13 bar author — update docs\n" +
					"- 9abc012 2026-05-14 baz author — internal rename\n" +
					"\n## v1.7.7\n\n- old\n",
			)

			It(
				"flag absent (file 404) → rewrite_needed=false, LLM not called for rewrite, PlanOutput.ChangelogRewrite is *false",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(nil, maintainerconfig.ErrFileNotFound)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(&claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

					// Only the bump LLM call — the rewrite call was skipped.
					Expect(fakeRunner.RunCallCount()).To(Equal(1))
					Expect(maintainerFetcher.FetchCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal("ready"))
					Expect(plan.RewriteNeeded).To(BeFalse())
					Expect(plan.RewrittenUnreleased).To(BeEmpty())
					Expect(plan.ChangelogRewrite).NotTo(BeNil())
					Expect(*plan.ChangelogRewrite).To(BeFalse())

					// JSON encoding contract: happy-path false → field SET
					// to &false → JSON emits literal `false` (json.MarshalIndent
					// inserts a space, so the substring is `"changelog_rewrite": false`).
					section, ok := md.FindSection("## Plan")
					Expect(ok).To(BeTrue())
					Expect(section.Body).To(MatchRegexp(`"changelog_rewrite":\s*false`))
				},
			)

			It(
				"Fetch returns (nil, nil): empty bytes → Parse zero config → default false, rewrite LLM not called",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(nil, nil)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(&claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

					Expect(fakeRunner.RunCallCount()).To(Equal(1))
					Expect(maintainerFetcher.FetchCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.RewriteNeeded).To(BeFalse())
					Expect(*plan.ChangelogRewrite).To(BeFalse())
				},
			)

			It(
				"flag absent (file present, no release: block) → rewrite_needed=false, rewrite LLM not called",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(
						[]byte("prReviewer:\n  autoApprove: true\n"),
						nil,
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(&claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

					Expect(fakeRunner.RunCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.RewriteNeeded).To(BeFalse())
					Expect(*plan.ChangelogRewrite).To(BeFalse())
				},
			)

			It("flag explicit false → rewrite_needed=false, rewrite LLM not called", func() {
				maintainerFetcher := &mocks.MaintainerConfigFetcher{}
				maintainerFetcher.FetchReturns(
					[]byte("release:\n  changelogRewrite: false\n"),
					nil,
				)
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(noisyChangelog, nil)
				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturns(&claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					maintainerFetcher,
					withNoRemoteTags(),
					false,
				) // spec 060
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				Expect(fakeRunner.RunCallCount()).To(Equal(1))

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(),
					md,
					"## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.RewriteNeeded).To(BeFalse())
				Expect(*plan.ChangelogRewrite).To(BeFalse())
			})

			It("flag true + noisy Unreleased → rewrite_needed=true, rewrite LLM IS called", func() {
				maintainerFetcher := &mocks.MaintainerConfigFetcher{}
				maintainerFetcher.FetchReturns(
					[]byte("release:\n  changelogRewrite: true\n"),
					nil,
				)
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(noisyChangelog, nil)
				fakeRunner := &mocks.ClaudeRunnerMock{}
				fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
					Result: `{"bump":"minor","reasoning":"feat: stub"}`,
				}, nil)
				fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
					Result: `{"rewrite_needed":true,"rewritten_unreleased":"- chore: bump foo\n- docs: update docs\n- refactor: internal rename\n","reasoning":"reframed raw git log lines"}`,
				}, nil)

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					maintainerFetcher,
					withNoRemoteTags(),
					false,
				) // spec 060
				md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
				Expect(err).NotTo(HaveOccurred())
				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				Expect(fakeRunner.RunCallCount()).To(Equal(2))

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(),
					md,
					"## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.RewriteNeeded).To(BeTrue())
				Expect(plan.RewrittenUnreleased).NotTo(BeEmpty())
				Expect(*plan.ChangelogRewrite).To(BeTrue())

				// JSON encoding contract: happy-path true → field SET
				// to &true → JSON emits literal `true` (json.MarshalIndent
				// inserts a space, so the substring is `"changelog_rewrite": true`).
				section, ok := md.FindSection("## Plan")
				Expect(ok).To(BeTrue())
				Expect(section.Body).To(MatchRegexp(`"changelog_rewrite":\s*true`))
			})

			It(
				"flag true + clean Unreleased → rewrite_needed=false (LLM judges clean), rewrite LLM IS called",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(
						[]byte("release:\n  changelogRewrite: true\n"),
						nil,
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(cleanChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)
					fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
						Result: `{"rewrite_needed":false,"rewritten_unreleased":"","reasoning":"already clean"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

					Expect(fakeRunner.RunCallCount()).To(Equal(2))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.RewriteNeeded).To(BeFalse())
					Expect(plan.RewrittenUnreleased).To(BeEmpty())
					Expect(*plan.ChangelogRewrite).To(BeTrue())
				},
			)

			It(
				"network error on .maintainer.yaml fetch → treated as default false, NOT fail-closed",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(
						nil,
						stderrors.New("dial tcp: connection refused"),
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(&claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					// Planning succeeded (NOT fail-closed).
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					// Rewrite LLM call was skipped.
					Expect(fakeRunner.RunCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal("ready"))
					Expect(plan.RewriteNeeded).To(BeFalse())
					Expect(*plan.ChangelogRewrite).To(BeFalse())
				},
			)

			It(
				"invalid value: string \"foo\" → outcome=failed, error_category=invalid_config, human_review",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(
						[]byte("release:\n  changelogRewrite: \"foo\"\n"),
						nil,
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{} // never called

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())

					// Fail-closed: no LLM call, AgentStatusFailed + human_review.
					Expect(fakeRunner.RunCallCount()).To(Equal(0))
					Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
					Expect(result.NextPhase).To(Equal("human_review"))
					Expect(result.Message).To(ContainSubstring("release.changelogRewrite"))
					Expect(result.Message).To(ContainSubstring("foo"))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal("failed"))
					Expect(plan.ErrorCategory).To(Equal("invalid_config"))
					Expect(plan.InvalidField).To(Equal("release.changelogRewrite"))
					Expect(plan.InvalidValue).To(Equal("foo"))

					// Failure path: `changelog_rewrite` token is OMITTED from
					// the JSON (pointer is nil + omitempty).
					section, ok := md.FindSection("## Plan")
					Expect(ok).To(BeTrue())
					Expect(section.Body).NotTo(ContainSubstring("changelog_rewrite"))
				},
			)

			It(
				"invalid value: number 1 → outcome=failed, error_category=invalid_config, human_review",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(
						[]byte("release:\n  changelogRewrite: 1\n"),
						nil,
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())

					Expect(fakeRunner.RunCallCount()).To(Equal(0))
					Expect(result.Status).To(Equal(agentlib.AgentStatusFailed))
					Expect(result.NextPhase).To(Equal("human_review"))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal("failed"))
					Expect(plan.ErrorCategory).To(Equal("invalid_config"))
					Expect(plan.InvalidField).To(Equal("release.changelogRewrite"))
					Expect(plan.InvalidValue).To(Equal("1"))
				},
			)

			It(
				"task page audit trail: PlanOutput.ChangelogRewrite is the resolved value, not the file content",
				func() {
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(
						[]byte("release:\n  changelogRewrite: true\n"),
						nil,
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)
					fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
						Result: `{"rewrite_needed":true,"rewritten_unreleased":"- chore: bump foo\n","reasoning":"ok"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					_, err = step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())

					// Audit-trail invariant: the JSON value is the BOOLEAN
					// `true`, not the string "true". Go's pointer-encoded
					// bool emits a literal `true` token in JSON.
					Expect(plan.ChangelogRewrite).NotTo(BeNil())
					Expect(*plan.ChangelogRewrite).To(BeTrue())

					section, ok := md.FindSection("## Plan")
					Expect(ok).To(BeTrue())
					Expect(section.Body).To(MatchRegexp(`"changelog_rewrite":\s*true`))
					// Negative: the value is NOT quoted.
					Expect(section.Body).NotTo(MatchRegexp(`"changelog_rewrite":\s*"true"`))
				},
			)

			It(
				"flag-read-once: mutating the mock mid-run does not affect the in-flight planning step",
				func() {
					callCount := 0
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchCalls(
						func(_ context.Context, _ string, _ string, _ string) ([]byte, error) {
							callCount++
							if callCount == 1 {
								return []byte("release:\n  changelogRewrite: true\n"), nil
							}
							return []byte("release:\n  changelogRewrite: false\n"), nil
						},
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(noisyChangelog, nil)
					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturnsOnCall(0, &claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)
					fakeRunner.RunReturnsOnCall(1, &claudelib.ClaudeResult{
						Result: `{"rewrite_needed":true,"rewritten_unreleased":"- chore: bump foo\n","reasoning":"ok"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false,
					) // spec 060
					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())
					_, err = step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())

					// The first call's value (true) is what was resolved —
					// the spec's flag-read-once semantics. A subsequent
					// mutation of the file (simulated here by returning
					// different bytes on call 2) has no effect on the
					// already-resolved planning step.
					Expect(maintainerFetcher.FetchCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(*plan.ChangelogRewrite).To(BeTrue())
				},
			)

			// major-bump range clamp (classifier-clamp). Decision table:
			//   bump=major + allowMajorBumpConfig==true  → proceed (major)
			//   bump=major + per-run allowMajor==true     → proceed (major, CLI override)
			//   bump=major + neither                      → CLAMP major→minor (ship as minor)
			//   bump=patch/minor (any)                    → proceed (no-op)
			// Clamp case: planning step writes ## Plan(outcome=ready, bump=minor)
			// and advances to execution — it does NOT escalate to human_review
			// or clear the assignee.
			Context("major-bump guard (spec 060)", func() {
				// Trip-case CHANGELOG: contains the literal regression bullet from
				// the originating incident (spec 060 § Problem). A prefix-only
				// classifier would mark this `refactor:` as patch; the spec-060
				// guard ensures the operator gets a NeedsInput regardless of what
				// the classifier returns. Mocked ClaudeRunner forces bump=major
				// so the trip case is deterministic — independent of the
				// prefix-only classifier rules.
				tripChangelog := []byte(
					"## Unreleased\n\n" +
						"- refactor(lib): rename TaskTypeClaude → TaskTypeLLM\n\n" +
						"## v1.7.7\n\n- old\n",
				)

				It(
					"major bump clamps to minor when neither opt-in present",
					func() {
						// Default-mock maintainerConfigFetcher → (nil, nil) →
						// Parse(empty) → cfg.Release.AllowMajorBump=false.
						// per-run allowMajor=false. Bump classifier returns
						// `major` with reasoning that contains "BREAKING CHANGE"
						// and cites the offending bullet.
						maintainerFetcher := &mocks.MaintainerConfigFetcher{}
						maintainerFetcher.FetchReturns(nil, nil)

						fakeFetcher := &mocks.Fetcher{}
						fakeFetcher.FetchReturns(tripChangelog, nil)

						fakeRunner := &mocks.ClaudeRunnerMock{}
						fakeRunner.RunReturns(&claudelib.ClaudeResult{
							Result: `{"bump":"major","reasoning":"BREAKING CHANGE: refactor(lib) renames TaskTypeClaude → TaskTypeLLM"}`,
						}, nil)

						step := pkg.NewPlanningStep(
							fakeRunner,
							fakeFetcher,
							maintainerFetcher,
							withNoRemoteTags(),
							false, // spec 060: per-run override OFF
						)

						taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-spec060\n---\n\n# release task\n"

						md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
						Expect(err).NotTo(HaveOccurred())

						result, err := step.Run(context.Background(), md)
						Expect(err).NotTo(HaveOccurred())

						// (a) Status: Done, advancing to execution — clamped,
						// not escalated.
						Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
						Expect(result.NextPhase).To(Equal("execution"))

						// (b,c,d) ## Plan JSON: the disallowed major is capped
						// to minor and the release ships.
						plan, err := agentlib.ExtractSection[pkg.PlanOutput](
							context.Background(),
							md,
							"## Plan",
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(plan.Outcome).To(Equal("ready"))
						Expect(plan.Bump).To(Equal("minor"))
						Expect(plan.NextVersion).To(Equal("1.8.0"))

						// (e) No escalation: assignee NOT cleared,
						// previous_assignee NOT set.
						gotAssignee, _ := md.Frontmatter.String("assignee")
						Expect(gotAssignee).To(Equal("github-releaser-agent"))
						gotPrevAssignee, _ := md.Frontmatter.String("previous_assignee")
						Expect(gotPrevAssignee).To(Equal(""))
						gotStatus, _ := md.Frontmatter.String("status")
						Expect(gotStatus).To(Equal("in_progress"))

						// The clamp annotates the reasoning for the audit trail.
						Expect(plan.Reasoning).To(ContainSubstring("capped to minor"))
					},
				)

				It(
					"major bump proceeds when repo opt-in true",
					func() {
						// Repo opt-in: release.allowMajorBump: true.
						maintainerFetcher := &mocks.MaintainerConfigFetcher{}
						maintainerFetcher.FetchReturns(
							[]byte("release:\n  allowMajorBump: true\n"),
							nil,
						)

						fakeFetcher := &mocks.Fetcher{}
						fakeFetcher.FetchReturns(tripChangelog, nil)

						fakeRunner := &mocks.ClaudeRunnerMock{}
						fakeRunner.RunReturns(&claudelib.ClaudeResult{
							Result: `{"bump":"major","reasoning":"BREAKING CHANGE detected"}`,
						}, nil)

						step := pkg.NewPlanningStep(
							fakeRunner,
							fakeFetcher,
							maintainerFetcher,
							withNoRemoteTags(),
							false, // spec 060: per-run override OFF — repo opt-in alone is enough
						)

						taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-spec060-repo\n---\n\n# release task\n"

						md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
						Expect(err).NotTo(HaveOccurred())

						result, err := step.Run(context.Background(), md)
						Expect(err).NotTo(HaveOccurred())
						Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
						Expect(result.NextPhase).To(Equal("execution"))

						plan, err := agentlib.ExtractSection[pkg.PlanOutput](
							context.Background(),
							md,
							"## Plan",
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(plan.Outcome).To(Equal("ready"))
						Expect(plan.Bump).To(Equal("major"))
						// The AllowMajorBump* audit-trail fields are only
						// populated on the trip case (PlanOutput doc: "NOT
						// populated on outcome=ready"); the happy path
						// omits them via omitempty. The guard's proceed
						// decision is fully captured by Status/NextPhase +
						// outcome=ready.
					},
				)

				It(
					"major bump proceeds when CLI flag set",
					func() {
						// Default-mock maintainerConfigFetcher → AllowMajorBump=false.
						// per-run allowMajor=true is the lever.
						maintainerFetcher := &mocks.MaintainerConfigFetcher{}
						maintainerFetcher.FetchReturns(nil, nil)

						fakeFetcher := &mocks.Fetcher{}
						fakeFetcher.FetchReturns(tripChangelog, nil)

						fakeRunner := &mocks.ClaudeRunnerMock{}
						fakeRunner.RunReturns(&claudelib.ClaudeResult{
							Result: `{"bump":"major","reasoning":"BREAKING CHANGE detected"}`,
						}, nil)

						step := pkg.NewPlanningStep(
							fakeRunner,
							fakeFetcher,
							maintainerFetcher,
							withNoRemoteTags(),
							true, // spec 060: per-run CLI override ON
						)

						taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-spec060-flag\n---\n\n# release task\n"

						md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
						Expect(err).NotTo(HaveOccurred())

						result, err := step.Run(context.Background(), md)
						Expect(err).NotTo(HaveOccurred())
						Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
						Expect(result.NextPhase).To(Equal("execution"))

						plan, err := agentlib.ExtractSection[pkg.PlanOutput](
							context.Background(),
							md,
							"## Plan",
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(plan.Outcome).To(Equal("ready"))
						// The AllowMajorBump* audit-trail fields are only
						// populated on the trip case (PlanOutput doc: "NOT
						// populated on outcome=ready"); the happy path
						// omits them via omitempty. The guard's proceed
						// decision is fully captured by Status/NextPhase +
						// outcome=ready.
					},
				)

				It("minor bump unaffected by guard", func() {
					// Default-mock: no YAML opt-in, no CLI override. Classifier
					// returns `minor` — guard is a no-op.
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(nil, nil)

					cleanChangelog := []byte(
						"## Unreleased\n\n- feat: add foo\n- fix: bar\n\n## v1.7.7\n\n- old\n",
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(cleanChangelog, nil)

					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(&claudelib.ClaudeResult{
						Result: `{"bump":"minor","reasoning":"feat: stub"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false, // spec 060: per-run override OFF
					)

					taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-spec060-minor\n---\n\n# release task\n"

					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())

					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(result.NextPhase).To(Equal("execution"))

					// (d) MaintainerConfigFetcher was called once — the guard
					// did NOT short-circuit, the normal planning path ran.
					Expect(maintainerFetcher.FetchCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal("ready"))
					Expect(plan.Bump).To(Equal("minor"))
					Expect(plan.NextVersion).To(Equal("1.8.0"))
				})

				It("patch bump unaffected by guard", func() {
					// Same shape as the minor no-op case, but with bump=patch.
					// Spec 060 § Desired Behavior 3 says the guard fires only
					// on `major`; this asserts the condition is the blanket
					// `!= "major"` and not `== "minor"` (a regression that
					// would silently let patch bumps trip).
					maintainerFetcher := &mocks.MaintainerConfigFetcher{}
					maintainerFetcher.FetchReturns(nil, nil)

					cleanChangelog := []byte(
						"## Unreleased\n\n- fix: typo\n\n## v1.7.7\n\n- old\n",
					)
					fakeFetcher := &mocks.Fetcher{}
					fakeFetcher.FetchReturns(cleanChangelog, nil)

					fakeRunner := &mocks.ClaudeRunnerMock{}
					fakeRunner.RunReturns(&claudelib.ClaudeResult{
						Result: `{"bump":"patch","reasoning":"fix: stub"}`,
					}, nil)

					step := pkg.NewPlanningStep(
						fakeRunner,
						fakeFetcher,
						maintainerFetcher,
						withNoRemoteTags(),
						false, // spec 060: per-run override OFF
					)

					taskMD := "---\nstatus: in_progress\nphase: planning\nassignee: github-releaser-agent\ntask_type: github-release\nrepo: bborbe/maintainer\nclone_url: https://github.com/bborbe/maintainer.git\nref: master\ncurrent_version: v1.7.7\ntask_identifier: gh-release-bborbe-maintainer-master-spec060-patch\n---\n\n# release task\n"

					md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
					Expect(err).NotTo(HaveOccurred())

					result, err := step.Run(context.Background(), md)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(agentlib.AgentStatusDone))
					Expect(result.NextPhase).To(Equal("execution"))
					Expect(maintainerFetcher.FetchCallCount()).To(Equal(1))

					plan, err := agentlib.ExtractSection[pkg.PlanOutput](
						context.Background(),
						md,
						"## Plan",
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(plan.Outcome).To(Equal("ready"))
					Expect(plan.Bump).To(Equal("patch"))
					Expect(plan.NextVersion).To(Equal("1.7.8"))
				})
			})
		})
	})
})

var _ = Describe("parseOwnerRepo", func() {
	DescribeTable("splits owner/name",
		func(input, wantOwner, wantName string, wantOK bool) {
			owner, name, ok := pkg.ParseOwnerRepoForTest(input)
			Expect(ok).To(Equal(wantOK))
			Expect(owner).To(Equal(wantOwner))
			Expect(name).To(Equal(wantName))
		},
		Entry("empty string", "", "", "", false),
		Entry("no slash", "badrepo", "", "", false),
		Entry("empty owner", "/name", "", "", false),
		Entry("empty name", "owner/", "", "", false),
		Entry("happy path", "owner/name", "owner", "name", true),
	)
})

var _ = Context("plan-time current_version resolution (spec 001)", func() {
	// Common fixtures shared by all four spec-001 branches.
	const taskMDTemplate = `---
status: in_progress
phase: planning
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/maintainer
clone_url: https://github.com/bborbe/maintainer.git
ref: master
current_version: v0.101.0
task_identifier: gh-release-bborbe-maintainer-spec001
---

# release task
`
	standardChangelog := []byte(
		"## Unreleased\n\n- feat: add foo\n\n## v0.101.0\n\n- old\n",
	)
	standardRunner := func() *mocks.ClaudeRunnerMock {
		r := &mocks.ClaudeRunnerMock{}
		r.RunReturns(&claudelib.ClaudeResult{
			Result: `{"bump":"patch","reasoning":"feat: stub"}`,
		}, nil)
		return r
	}

	Context("AC #3: remote-latest wins", func() {
		It(
			"remote semver tag becomes effective current_version — bump computed from remote, not snapshot",
			func() {
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(standardChangelog, nil)

				tagsFetcher := &mocks.TagsFetcher{}
				tagsFetcher.LatestSemverTagReturns("v0.101.1", nil)

				fakeRunner := standardRunner()

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					tagsFetcher,
					false,
				)

				md, err := agentlib.ParseMarkdown(context.Background(), taskMDTemplate)
				Expect(err).NotTo(HaveOccurred())

				result, err := step.Run(context.Background(), md)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

				plan, err := agentlib.ExtractSection[pkg.PlanOutput](
					context.Background(), md, "## Plan",
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(plan.Outcome).To(Equal(pkg.PlanOutcomeReady))
				// Bumped from the REMOTE v0.101.1, NOT the snapshot v0.101.0
				Expect(plan.NextVersion).To(Equal("0.101.2"))
				// CurrentVersion reflects the resolved remote tag
				Expect(plan.CurrentVersion).To(Equal("v0.101.1"))
				// No warning on the happy path
				Expect(plan.ConfigFetchWarning).To(BeEmpty())
			},
		)
	})

	Context("AC #4: no-tags fallback", func() {
		It("ErrNoTags → uses snapshot current_version, no warning", func() {
			fakeFetcher := &mocks.Fetcher{}
			fakeFetcher.FetchReturns(standardChangelog, nil)

			tagsFetcher := &mocks.TagsFetcher{}
			tagsFetcher.LatestSemverTagReturns("", githubtags.ErrNoTags)

			fakeRunner := standardRunner()

			step := pkg.NewPlanningStep(
				fakeRunner,
				fakeFetcher,
				&mocks.MaintainerConfigFetcher{},
				tagsFetcher,
				false,
			)

			md, err := agentlib.ParseMarkdown(context.Background(), taskMDTemplate)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

			plan, err := agentlib.ExtractSection[pkg.PlanOutput](
				context.Background(), md, "## Plan",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Outcome).To(Equal(pkg.PlanOutcomeReady))
			// Bumped from the SNAPSHOT v0.101.0 → 0.101.1
			Expect(plan.NextVersion).To(Equal("0.101.1"))
			// No warning on clean no-tags fallback
			Expect(plan.ConfigFetchWarning).To(BeEmpty())
		})
	})

	Context("AC #5: transient-error fallback + warning", func() {
		It("non-ErrNoTags error → uses snapshot, surfaces non-fatal warning", func() {
			fakeFetcher := &mocks.Fetcher{}
			fakeFetcher.FetchReturns(standardChangelog, nil)

			tagsFetcher := &mocks.TagsFetcher{}
			tagsFetcher.LatestSemverTagReturns(
				"",
				stderrors.New("list tags: status 503: server error"),
			)

			fakeRunner := standardRunner()

			step := pkg.NewPlanningStep(
				fakeRunner,
				fakeFetcher,
				&mocks.MaintainerConfigFetcher{},
				tagsFetcher,
				false,
			)

			md, err := agentlib.ParseMarkdown(context.Background(), taskMDTemplate)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal(agentlib.AgentStatusDone))

			plan, err := agentlib.ExtractSection[pkg.PlanOutput](
				context.Background(), md, "## Plan",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Outcome).To(Equal(pkg.PlanOutcomeReady))
			// Bumped from the SNAPSHOT v0.101.0 → 0.101.1 (NOT fail-closed)
			Expect(plan.NextVersion).To(Equal("0.101.1"))
			// Warning is non-empty and contains operator-grep evidence
			Expect(plan.ConfigFetchWarning).NotTo(BeEmpty())
			Expect(plan.ConfigFetchWarning).To(ContainSubstring("remote tag lookup failed"))
			Expect(plan.ConfigFetchWarning).To(ContainSubstring("503"))
		})
	})

	Context("AC #6: escalation preserved — empty snapshot AND no remote tags", func() {
		It("empty frontmatter current_version escalates before reaching tags fetcher", func() {
			// Changelog fetcher is irrelevant — empty frontmatter guard fires first.
			fakeFetcher := &mocks.Fetcher{}
			fakeFetcher.FetchReturns(standardChangelog, nil)

			// Tags fetcher must NOT be called — empty-frontmatter guard short-circuits first.
			tagsFetcher := &mocks.TagsFetcher{}
			tagsFetcher.LatestSemverTagReturns("", githubtags.ErrNoTags)

			fakeRunner := &mocks.ClaudeRunnerMock{} // never reached

			step := pkg.NewPlanningStep(
				fakeRunner,
				fakeFetcher,
				&mocks.MaintainerConfigFetcher{},
				tagsFetcher,
				false,
			)

			// frontmatter current_version is EMPTY
			taskMD := `---
status: in_progress
phase: planning
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/maintainer
clone_url: https://github.com/bborbe/maintainer.git
ref: master
current_version:
task_identifier: gh-release-bborbe-maintainer-spec001-empty
---

# release task
`
			md, err := agentlib.ParseMarkdown(context.Background(), taskMD)
			Expect(err).NotTo(HaveOccurred())

			result, err := step.Run(context.Background(), md)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal(agentlib.AgentStatusNeedsInput))

			plan, err := agentlib.ExtractSection[pkg.PlanOutput](
				context.Background(), md, "## Plan",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Outcome).To(Equal(pkg.PlanOutcomeNeedsInput))
			Expect(
				plan.PreconditionFailed,
			).To(Equal(pkg.PreconditionMissingFrontmatter + "current_version"))
			Expect(md.Frontmatter["previous_assignee"]).To(Equal(pkg.AgentLogin))

			// Tags fetcher was never called — empty-frontmatter guard fired first.
			Expect(tagsFetcher.LatestSemverTagCallCount()).To(Equal(0))
		})
	})
})

var _ = Describe("classifyValidationFailure", func() {
	DescribeTable("maps validator reason to precondition",
		func(reason, want string) {
			Expect(pkg.ClassifyValidationFailureForTest(reason)).To(Equal(want))
		},
		Entry("not-first branch",
			"Unreleased is not the first ## section; found 'x' at line 1.",
			"P1_unreleased_not_first"),
		Entry("no bullet entries branch",
			"Unreleased section has no bullet entries.",
			"P2_unreleased_empty"),
		Entry("not found branch",
			"Unreleased section not found.",
			"P2_unreleased_empty"),
		Entry("default branch",
			"some unexpected reason",
			"P2_unreleased_empty"),
	)
})

var _ = Describe("steps_planning integration (spec 048 regression guard)", func() {
	// This test wires the full agent via factory.CreateAgent and runs it
	// against the real FileResultDeliverer to exercise the framework-side
	// status→frontmatter switch. The bug fixed in spec 048 lived in that
	// switch: AgentStatusDone on escalation auto-advances to
	// phase: done, status: completed; AgentStatusNeedsInput preserves
	// phase and writes status: in_progress.
	//
	// The step-level Fetcher is mocked so the test runs OFFLINE — no real
	// GitHub network calls. The Claude runner is also mocked but is never
	// invoked on a P1 escalation path (escalation short-circuits before
	// classification).
	//
	// Fixture: a CHANGELOG where ## Unreleased is NOT the first ## heading
	// — triggers P1 escalation. Per spec 047 § Desired Behavior 4, this
	// path returns the NeedsInput verdict in ## Plan + clears assignee +
	// sets previous_assignee, while leaving status/phase alone.
	Context("P1 escalation via FileResultDeliverer", func() {
		var tmpDir string
		var taskFile string

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "spec-048-*")
			Expect(err).NotTo(HaveOccurred())
			taskFile = filepath.Join(tmpDir, "task.md")
		})

		AfterEach(func() {
			_ = os.RemoveAll(tmpDir)
		})

		It(
			"framework deliverer leaves status: in_progress and phase: planning unchanged on escalation",
			func() {
				// Fixture: ## Unreleased is the SECOND ## heading → P1 fail.
				initialTask := `---
status: in_progress
phase: planning
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/maintainer
clone_url: https://github.com/bborbe/maintainer.git
ref: master
current_version: v1.2.6
task_identifier: gh-release-bborbe-maintainer-master-spec048
---

# release task
`
				Expect(os.WriteFile(taskFile, []byte(initialTask), 0o600)).To(Succeed())

				// Inject the mock Fetcher via package-level seam: we cannot use
				// factory.CreateAgent directly because it wires the real
				// HTTPFetcher. Build the planning step manually with the mock
				// fetcher, wrap it in a one-phase Agent identical in shape to
				// what factory.CreateAgent produces. This is intentional — the
				// factory's job is just composition; the integration we care
				// about is the agent.Run + FileResultDeliverer chain, which
				// this exercises identically.
				badChangelog := []byte(
					"# Changelog\n\nIntro.\n\n## v1.2.6\n\n- old release\n\n## Unreleased\n\n- new bullet\n",
				)
				fakeFetcher := &mocks.Fetcher{}
				fakeFetcher.FetchReturns(badChangelog, nil)
				fakeRunner := &mocks.ClaudeRunnerMock{} // never called on P1

				step := pkg.NewPlanningStep(
					fakeRunner,
					fakeFetcher,
					&mocks.MaintainerConfigFetcher{},
					withNoRemoteTags(),
					false, // spec 060
				)
				// maintainerConfigFetcher mock returns nil/nil by default — never reached on P1 escalation.
				agent := agentlib.NewAgent(agentlib.NewPhase(domain.TaskPhasePlanning, step))

				// Use the real FileResultDeliverer + passthrough generator —
				// same wiring as cmd/run-task. This is the deliverer whose
				// Status switch contains the bug being fixed.
				deliverer := delivery.NewFileResultDeliverer(
					delivery.NewPassthroughContentGenerator(),
					taskFile,
				)

				result, err := agent.Run(
					context.Background(),
					domain.TaskPhasePlanning,
					initialTask,
					deliverer,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).NotTo(BeNil())
				Expect(result.Status).To(Equal(agentlib.AgentStatusNeedsInput))

				// Read back the file the deliverer wrote.
				mutated, err := os.ReadFile(taskFile)
				Expect(err).NotTo(HaveOccurred())
				mutatedStr := string(mutated)

				// Regression assertions — the bug-fix invariant lives here.
				// Each of these failed against the OLD code (AgentStatusDone
				// on escalation) because the framework switch wrote
				// phase: done + status: completed.
				Expect(mutatedStr).To(ContainSubstring("status: in_progress"))
				Expect(mutatedStr).To(ContainSubstring("phase: planning"))

				// Defense in depth: explicitly negate the bug state.
				Expect(mutatedStr).NotTo(ContainSubstring("status: completed"))
				Expect(mutatedStr).NotTo(ContainSubstring("phase: done"))

				// Sanity: assignee cleared, previous_assignee set
				// (these were already correct in the buggy version — included
				// here so a future refactor doesn't accidentally regress the
				// escalation rule's other half).
				// Note: YAML serializes empty string as "assignee: " (no quotes).
				// We use a regexp to match the line exactly (start of line, assignee:,
				// optional space, then newline — not "assignee: github-releaser-agent").
				assigneeLineRegex := `(?m)^assignee:\s*$\n`
				Expect(mutatedStr).To(MatchRegexp(assigneeLineRegex))
				Expect(mutatedStr).To(ContainSubstring("previous_assignee: github-releaser-agent"))

				// Claude must NOT have been invoked — P1 escalation
				// short-circuits before classification.
				Expect(fakeRunner.RunCallCount()).To(Equal(0))

				// Avoid "imported and not used" if claudelib is otherwise
				// unreferenced by this block.
				var _ claudelib.ClaudeRunner = fakeRunner
			},
		)
	})
})

// Compile-time assertion that factory.CreateAgent is the symbol we mean
// to keep coupled to this integration test, even though the test builds
// its own Agent to inject the mock fetcher. If this signature changes,
// update the integration test to match.
var _ = func() *agentlib.Agent {
	return factory.CreateAgent(
		claudelib.ClaudeConfigDir("/tmp"),
		claudelib.AgentDir("/tmp"),
		claudelib.ClaudeModel("sonnet"),
		"",
		map[string]string{},
		false, // spec 060: per-run allowMajor
	)
}

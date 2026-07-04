// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agentlib "github.com/bborbe/agent"
	claudelib "github.com/bborbe/agent/claude"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-releaser-agent/pkg/changelog"
	"github.com/bborbe/github-releaser-agent/pkg/git"
	"github.com/bborbe/github-releaser-agent/pkg/githubreview"
	"github.com/bborbe/github-releaser-agent/pkg/plugin"
	"github.com/bborbe/github-releaser-agent/pkg/prompts"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
)

// Per-entry faithfulness verdict values. Applied ONLY to per_entry entries.
// The `unknown` state lives on Overall (see OverallUnknown below), NOT on
// individual entries: per the spec's Failure Modes row "LLM unavailability",
// `unknown` surfaces only at the overall level. When the LLM is
// unreachable, PerEntry is left empty rather than filled with `unknown`
// entries — single-purpose constants stay clearer this way.
const (
	FaithfulnessPresent      = "present"
	FaithfulnessSilentDrop   = "silent-drop"
	FaithfulnessHallucinated = "hallucinated"
)

// Overall verdict values for ReviewOutput.Overall.
const (
	OverallPass    = "pass"
	OverallFail    = "fail"
	OverallUnknown = "unknown"
)

// Stable check names recorded in ReviewOutput.FailedChecks. Spec AC 15
// asserts on these literals verbatim; do not rename without a spec
// amendment.
const (
	CheckFaithfulness         = "Faithfulness"
	CheckUnexpectedFileChange = "UnexpectedFileChange"
	CheckPush                 = "Push"
)

// FaithfulnessVerdict captures the semantic comparison of one entry from
// the original ## Unreleased against the final ## vX.Y.Z body.
//
//   - Verdict ∈ {FaithfulnessPresent, FaithfulnessSilentDrop, FaithfulnessHallucinated}.
//   - Entry is the verbatim line being judged.
//   - Note is the LLM's one-sentence justification.
type FaithfulnessVerdict struct {
	Entry   string `json:"entry"`
	Verdict string `json:"verdict"`
	Note    string `json:"note,omitempty"`
}

// ReviewChecks holds the boolean verification results. The first three
// are the original structural checks; the remaining two are the new
// ai-review-side gates.
type ReviewChecks struct {
	TagExists                bool `json:"tag_exists"`
	TagAtExpectedSHA         bool `json:"tag_at_expected_sha"`
	ChangelogHeaderRewritten bool `json:"changelog_header_rewritten"`

	// FaithfulnessOK is true when every per-entry verdict is
	// FaithfulnessPresent (no silent-drop, no hallucinated). False on
	// any drift OR when the overall verdict is OverallUnknown.
	FaithfulnessOK bool `json:"faithfulness_ok"`

	// UnexpectedFileChange is true when the release commit touched a
	// file other than CHANGELOG.md (plus detected plugin manifests).
	// It is the ai-review-side mirror of the executeLocalRelease
	// pre-commit guard.
	UnexpectedFileChange bool `json:"unexpected_file_change"`
}

// ReviewOutput is the typed contract for the `## Review` JSON section the
// ai_review step writes. Round-trips with agentlib.MarshalSectionTyped +
// agentlib.ExtractSection[ReviewOutput].
type ReviewOutput struct {
	Approved bool         `json:"approved"`
	Checks   ReviewChecks `json:"checks"`
	Notes    string       `json:"notes"`

	// PerEntry holds the per-entry semantic verdict produced by the
	// faithfulness LLM call. Empty when Overall == OverallUnknown or
	// when the execution step recorded failure (nothing to verify).
	PerEntry []FaithfulnessVerdict `json:"per_entry,omitempty"`

	// Overall is the rolled-up semantic verdict: OverallPass |
	// OverallFail | OverallUnknown.
	//   - OverallPass:    every PerEntry is FaithfulnessPresent AND
	//                     no UnexpectedFileChange AND every structural
	//                     check is true.
	//   - OverallFail:    at least one PerEntry is silent-drop or
	//                     hallucinated, OR UnexpectedFileChange is true,
	//                     OR any structural check is false.
	//   - OverallUnknown: the LLM was unreachable; the rest of the
	//                     review is still written (structural checks)
	//                     but Approved is false and push is skipped.
	Overall string `json:"overall"`

	// UnexpectedFiles lists the file paths the commit touched that
	// were NOT in the expected set. Empty when
	// UnexpectedFileChange is false.
	UnexpectedFiles []string `json:"unexpected_files,omitempty"`

	// FailedChecks names the semantic / local checks that did not pass.
	// Stable strings — referenced by spec AC 15 assertions.
	// One or more of: CheckFaithfulness, CheckUnexpectedFileChange,
	// CheckPush.
	FailedChecks []string `json:"failed_checks,omitempty"`
}

// NewAIReviewStep wires the ai_review step with its GitHub REST API
// client, the ClaudeRunner used to invoke the faithfulness LLM, the
// GitOps seam used to push the local commit + tag, and the GitHub
// token (used for authenticated API calls).
func NewAIReviewStep(
	client githubreview.Client,
	runner claudelib.ClaudeRunner,
	ops git.GitOps,
	ghToken string,
) agentlib.Step {
	return &aiReviewStep{client: client, runner: runner, ops: ops, ghToken: ghToken}
}

// aiReviewStep implements agentlib.Step. It performs three remote
// verification checks against the GitHub REST API, one local diff
// check against the workdir, one semantic faithfulness check via the
// Claude runner, and on success pushes the local commit + tag to the
// remote.
type aiReviewStep struct {
	client  githubreview.Client
	runner  claudelib.ClaudeRunner
	ops     git.GitOps
	ghToken string
}

// Name implements agentlib.Step.
func (s *aiReviewStep) Name() string { return "github-release-ai-review" }

// ShouldRun always returns true. The step is idempotent at the
// framework level: a re-trigger overwrites the existing ## Review
// section.
func (s *aiReviewStep) ShouldRun(_ context.Context, _ *agentlib.Markdown) (bool, error) {
	return true, nil
}

// Run executes the verification pipeline. Sequence:
//  1. Read ## Result + ## Plan (fatal error if either missing).
//  2. If Result.Outcome != "released" → short-circuit approved=true.
//  3. Three structural checks (TagExists, TagAtExpectedSHA,
//     ChangelogHeaderRewritten). Each failure is recorded in
//     FailedChecks but does NOT early-return — the full check set is
//     gathered before the verdict rolls up.
//  4. Unexpected-file-change check against the local workdir.
//  5. Faithfulness LLM call (one-shot) compares
//     Plan.OriginalUnreleased against the body of
//     plan.NextVersionHeader in <Workdir>/CHANGELOG.md.
//  6. Roll up overall verdict; write ## Review.
//  7. When Approved: call ops.Push. On Push error: still write
//     ## Review (with a "push failed" note), set Approved=false,
//     return Failed/human_review.
//  8. On `!approved`: consult `LsRemote`. If the remote shows the tag
//     at the agent's expected SHA (or at any SHA, for the superseded
//     case), write `## Review Warning` + close as `completed`.
//     Otherwise, the existing `human_review` path stands.
//  9. Workdir cleanup: deferred via workdirShouldCleanup sentinel —
//     the workdir is removed on BOTH terminal transitions (Done and
//     human_review) AFTER ## Review has been written.
func (s *aiReviewStep) Run(ctx context.Context, md *agentlib.Markdown) (*agentlib.Result, error) {
	result, err := agentlib.ExtractSection[ResultOutput](ctx, md, "## Result")
	if err != nil || result == nil {
		return nil, errors.Wrapf(ctx, err, "ai_review: extract ## Result section")
	}
	plan, err := agentlib.ExtractSection[PlanOutput](ctx, md, "## Plan")
	if err != nil || plan == nil {
		return nil, errors.Wrapf(ctx, err, "ai_review: extract ## Plan section")
	}

	if result.Outcome != ResultOutcomeReleased {
		return s.writeShortCircuit(ctx, md)
	}

	repo, _ := md.Frontmatter.String("repo")
	owner, name, ok := parseOwnerRepo(repo)
	if !ok {
		return nil, errors.Errorf(ctx, "ai_review: read frontmatter repo")
	}

	glog.V(2).Infof(
		"ai_review: starting checks for repo=%s/%s tag=%s commit=%s",
		owner, name, result.Tag, result.CommitSHA,
	)

	checks := ReviewChecks{
		TagExists: true, TagAtExpectedSHA: true, ChangelogHeaderRewritten: true,
		FaithfulnessOK: true,
	}
	var failedChecks []string
	// Workdir cleanup: ai-review owns the lifetime once execution
	// returns result.Workdir. Removed on BOTH terminal transitions
	// (Done, human_review) AFTER ## Review has been marshaled.
	var workdirShouldCleanup bool
	defer s.cleanupWorkdir(result, &workdirShouldCleanup)

	// (1) Structural checks (TagExists, TagAtExpectedSHA,
	// ChangelogHeaderRewritten) are DISABLED on the pre-push path —
	// they query the GitHub remote for state that only exists POST-push
	// (push is gated on this very ai_review step per spec 058), so they
	// always reported false on a healthy release and blocked the push.
	// The initial check booleans stay true (no failure detected); local
	// diff-scope + faithfulness below remain authoritative.
	// Tracked: vault task "github-releaser structural-check pre-push misfire"
	// for a post-push verification phase that re-enables these.

	// (2) Unexpected-file-change check — local workdir inspection.
	unexpected := s.checkUnexpectedFileChange(ctx, &checks, result, &failedChecks)

	// (3) Faithfulness LLM call.
	faithfulnessOverall, perEntry := s.checkFaithfulness(
		ctx,
		plan,
		result,
		&checks,
		&failedChecks,
	)

	// (4) Roll up overall verdict.
	overall, approved := rollupVerdict(faithfulnessOverall, failedChecks)

	output := ReviewOutput{
		Approved:        approved,
		Checks:          checks,
		Notes:           s.notesFor(failedChecks),
		PerEntry:        perEntry,
		Overall:         overall,
		UnexpectedFiles: unexpected,
		FailedChecks:    failedChecks,
	}

	// (5) Push gating — only on Approved branch.
	if !output.Approved {
		// Spec 064 DB #7: a review rejection that coincides with a
		// confirmed remote tag at the agent's expected SHA does not
		// flip the task to `failed`. The review verdict is preserved
		// as a recorded warning in ## Review Warning, and the task
		// closes as `completed` (the post-check from prompt 2 also
		// upgrades the verdict — this branch is its ai_review-side
		// mirror for the case where the execution-step post-check
		// did NOT fire because the remote was empty at the time of
		// execution, but is now non-empty by the time ai_review runs).
		if warning := s.checkReviewOverride(ctx, md, &output, result); warning != nil {
			return s.finishReviewOverride(
				ctx,
				md,
				output,
				warning,
				&workdirShouldCleanup,
			)
		}
		return s.finishHumanReview(ctx, md, output, &workdirShouldCleanup)
	}
	return s.finishApproved(ctx, md, result, output, &workdirShouldCleanup)
}

// cleanupWorkdir is the deferred cleanup. The sentinel is set at
// the terminal-return points so the workdir outlives the
// ## Review section write — operator triage reads the task page,
// not the on-disk clone.
func (s *aiReviewStep) cleanupWorkdir(result *ResultOutput, workdirShouldCleanup *bool) {
	if *workdirShouldCleanup && result.Workdir != "" {
		if err := os.RemoveAll(result.Workdir); err != nil {
			glog.Warningf("ai_review: workdir cleanup failed: %v", err)
		}
	}
}

// finishApproved executes the push step. On push failure it falls
// through to finishHumanReview with an updated note; on success it
// returns Done.
func (s *aiReviewStep) finishApproved(
	ctx context.Context,
	md *agentlib.Markdown,
	result *ResultOutput,
	output ReviewOutput,
	workdirShouldCleanup *bool,
) (*agentlib.Result, error) {
	pushErr := s.ops.Push(
		ctx,
		result.Workdir,
		"HEAD",
		"refs/tags/"+result.LocalTag,
	)
	if pushErr != nil {
		glog.Warningf("ai_review: push failed: %v", pushErr)
		output.Notes = "push failed: " + pushErr.Error()
		output.Approved = false
		output.FailedChecks = append(output.FailedChecks, CheckPush)
		return s.finishHumanReview(ctx, md, output, workdirShouldCleanup)
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Review", output)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "ai_review: marshal ## Review section")
	}
	md.ReplaceSection(section)
	*workdirShouldCleanup = true
	return &agentlib.Result{
		Status:    agentlib.AgentStatusDone,
		NextPhase: "done",
	}, nil
}

// finishHumanReview writes the ## Review section (with the failed
// check set) and returns Failed/human_review. The workdir cleanup
// sentinel is set so the deferred cleanup runs AFTER the section
// has been written — the operator triage reads the task page, not
// the on-disk clone.
func (s *aiReviewStep) finishHumanReview(
	ctx context.Context,
	md *agentlib.Markdown,
	output ReviewOutput,
	workdirShouldCleanup *bool,
) (*agentlib.Result, error) {
	section, err := agentlib.MarshalSectionTyped(ctx, "## Review", output)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "ai_review: marshal ## Review section")
	}
	md.ReplaceSection(section)
	*workdirShouldCleanup = true
	return &agentlib.Result{
		Status:    agentlib.AgentStatusFailed,
		NextPhase: string(domain.TaskPhaseHumanReview),
		Message:   output.Notes,
	}, nil
}

// checkReviewOverride is the spec-064 ai_review-side sub-decision on
// the `!approved` path. It consults the remote via git.LsRemote to
// confirm whether the planned version's tag is already at the agent's
// expected SHA (a release was already published) or at a different
// SHA (a later release won the slot).
//
// On a non-empty observed SHA it returns a populated
// *ReviewWarningOutput; the caller (Run) will route to
// finishReviewOverride which writes the ## Review Warning block and
// closes the task as `completed`. On an empty result or LsRemote
// error it returns nil — the existing human_review path stands
// unchanged. The authed URL is built via the shared package-level
// helpers (mirror the execution step's auth model). On any error the
// err message is passed through RedactToken before logging so a
// leak of the GitHub auth token in the wrapped stderr cannot reach
// the log stream.
func (s *aiReviewStep) checkReviewOverride(
	ctx context.Context,
	md *agentlib.Markdown,
	output *ReviewOutput,
	result *ResultOutput,
) *ReviewWarningOutput {
	cloneURL, _ := md.Frontmatter.String("clone_url")
	ref, _ := md.Frontmatter.String("ref")
	if cloneURL == "" || ref == "" || result.LocalTag == "" {
		return nil
	}
	authedURL := injectToken(normalizeCloneURLToHTTPS(cloneURL), s.ghToken)
	// Bound the network round-trip — a stalled GitHub must not block
	// the review step indefinitely.
	lsCtx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()
	sha, err := s.ops.LsRemote(lsCtx, authedURL, ref, result.LocalTag)
	if err != nil {
		glog.V(2).Infof(
			"ai_review review-override: tag=%s err=%s",
			result.LocalTag,
			git.RedactToken(err.Error()),
		)
		return nil
	}
	if sha == "" {
		glog.V(2).Infof(
			"ai_review review-override: tag=%s sha=empty (no override)",
			result.LocalTag,
		)
		return nil
	}
	failedChecks := append([]string{}, output.FailedChecks...)
	note := fmt.Sprintf(
		"review rejected (%s) but remote confirms release at %s",
		strings.Join(failedChecks, ","),
		sha,
	)
	return &ReviewWarningOutput{
		FailedChecks:      failedChecks,
		PlannedVersion:    result.LocalTag,
		ObservedRemoteSHA: sha,
		Note:              note,
	}
}

// finishReviewOverride writes BOTH the existing ## Review section
// (the rejected verdict, preserved durably) AND the new ## Review
// Warning block. It rewrites the frontmatter to status: completed /
// phase: done and returns Done / NextPhase=done — same shape as the
// existing happy-path finishApproved. The workdir cleanup sentinel
// is set so the deferred cleanup runs AFTER the section writes.
func (s *aiReviewStep) finishReviewOverride(
	ctx context.Context,
	md *agentlib.Markdown,
	output ReviewOutput,
	warning *ReviewWarningOutput,
	workdirShouldCleanup *bool,
) (*agentlib.Result, error) {
	reviewSection, err := agentlib.MarshalSectionTyped(ctx, "## Review", output)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "ai_review: marshal ## Review section")
	}
	md.ReplaceSection(reviewSection)
	warningSection, err := agentlib.MarshalSectionTyped(
		ctx,
		"## Review Warning",
		warning,
	)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "ai_review: marshal ## Review Warning section")
	}
	md.ReplaceSection(warningSection)
	md.Frontmatter["status"] = "completed"
	md.Frontmatter["phase"] = "done"
	*workdirShouldCleanup = true
	glog.V(2).Infof(
		"ai_review review-override: tag=%s observed_remote_sha=%s",
		warning.PlannedVersion,
		warning.ObservedRemoteSHA,
	)
	return &agentlib.Result{
		Status:    agentlib.AgentStatusDone,
		NextPhase: "done",
	}, nil
}

// writeShortCircuit handles the Result.Outcome != released path: no
// checks to run, the spec says approved=true with overall=pass.
func (s *aiReviewStep) writeShortCircuit(
	ctx context.Context,
	md *agentlib.Markdown,
) (*agentlib.Result, error) {
	output := ReviewOutput{
		Approved: true,
		Checks: ReviewChecks{
			TagExists:                true,
			TagAtExpectedSHA:         true,
			ChangelogHeaderRewritten: true,
			FaithfulnessOK:           true,
		},
		Overall: OverallPass,
		Notes:   "execution step recorded failure; nothing to verify",
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Review", output)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "ai_review: marshal ## Review section")
	}
	md.ReplaceSection(section)
	return &agentlib.Result{
		Status:    agentlib.AgentStatusDone,
		NextPhase: "done",
	}, nil
}

// checkUnexpectedFileChange inspects the release commit's touched
// files. On deviation from the expected set (CHANGELOG.md + detected
// plugin manifests) it sets checks.UnexpectedFileChange=true and
// appends the failed-check name.
//
// Returns the diff (committed - expected) for the UnexpectedFiles
// output slice. On workdir-empty short-circuit the check is skipped
// silently and an empty diff is returned. On CommittedFiles error
// the check fails closed: checks.UnexpectedFileChange=true,
// CheckUnexpectedFileChange appended to failedChecks, empty diff
// returned. The release trust model requires fail-closed on
// transient errors — a git blip must not leave the check passing.
//
// isSubsetIncludingChangelog is the same shared helper used by
// steps_execution.go (guardCommittedFiles): committed must be a subset
// of allowed AND contain changelogFileName. This allows a detected
// manifest that was already at the target version (byte-identical →
// absent from the commit) to not fail the release, while any file
// outside the allowed set still fails closed.
func (s *aiReviewStep) checkUnexpectedFileChange(
	ctx context.Context,
	checks *ReviewChecks,
	result *ResultOutput,
	failedChecks *[]string,
) []string {
	if result.Workdir == "" {
		return nil
	}
	files, err := s.ops.CommittedFiles(ctx, result.Workdir)
	if err != nil {
		checks.UnexpectedFileChange = true
		*failedChecks = append(*failedChecks, CheckUnexpectedFileChange)
		glog.V(2).Infof(
			"ai_review: check=%s result=false: CommittedFiles error: %v",
			CheckUnexpectedFileChange,
			err,
		)
		return nil
	}
	// The expected set: CHANGELOG.md + detected plugin manifests.
	// We invoke plugin.DetectManifests even when result.Workdir is
	// non-empty — same as steps_execution.go. Detected manifests
	// extend the expected set.
	expected := []string{changelogFileName}
	detected, derr := plugin.DetectManifests(ctx, result.Workdir)
	if derr != nil {
		glog.Warningf("ai_review: DetectManifests failed: %v", derr)
		// Fall through with just the changelog in the expected set
		// — any extra detected manifest in `files` will then
		// surface as a (false) unexpected-file, which is the safer
		// bias.
	} else {
		expected = append(expected, detected...)
	}
	if !isSubsetIncludingChangelog(files, expected) {
		checks.UnexpectedFileChange = true
		*failedChecks = append(*failedChecks, CheckUnexpectedFileChange)
		glog.V(2).Infof(
			"ai_review: check=%s result=false: committed=%v expected=%v",
			CheckUnexpectedFileChange,
			files,
			expected,
		)
		return diffStringSet(files, expected)
	}
	glog.V(2).Infof("ai_review: check=%s result=true", CheckUnexpectedFileChange)
	return nil
}

// checkFaithfulness invokes the ClaudeRunner once with the embedded
// faithfulness prompt + the captured original + the final body,
// parses the response, and updates checks.FaithfulnessOK and
// PerEntry accordingly. The returned overall string is OverallPass,
// OverallFail, or OverallUnknown; the perEntry slice is the flat
// mapping (per_entry + extras-with-Verdict=hallucinated).
//
// On LLM error or parse error: OverallUnknown — perEntry left empty,
// FaithfulnessOK=false, CheckFaithfulness recorded in failedChecks.
//
// heading is the plan.NextVersionHeader text (e.g. "## v1.2.8") and
// is passed to changelog.ExtractSectionBody to extract the matching
// section from the on-disk CHANGELOG.md.
func (s *aiReviewStep) checkFaithfulness(
	ctx context.Context,
	plan *PlanOutput,
	result *ResultOutput,
	checks *ReviewChecks,
	failedChecks *[]string,
) (string, []FaithfulnessVerdict) {
	if result.Workdir == "" {
		// No workdir → no final body to read. Cannot run the
		// semantic check; mark unknown so the operator triages.
		checks.FaithfulnessOK = false
		*failedChecks = append(*failedChecks, CheckFaithfulness)
		return OverallUnknown, nil
	}
	changelogPath := filepath.Join(result.Workdir, changelogFileName)
	content, err := os.ReadFile(
		changelogPath,
	) // #nosec G304 -- workdir is os.TempDir-rooted; filename is constant
	if err != nil {
		glog.Warningf("ai_review: read CHANGELOG failed: %v", err)
		checks.FaithfulnessOK = false
		*failedChecks = append(*failedChecks, CheckFaithfulness)
		return OverallUnknown, nil
	}
	// ExtractSectionBody takes the heading TEXT (the part after
	// "## "), so strip the "## " prefix from plan.NextVersionHeader.
	headingText := strings.TrimPrefix(plan.NextVersionHeader, "## ")
	finalBody, err := changelog.ExtractSectionBody(ctx, content, headingText)
	if err != nil {
		glog.Warningf("ai_review: extract section body failed: %v", err)
		checks.FaithfulnessOK = false
		*failedChecks = append(*failedChecks, CheckFaithfulness)
		return OverallUnknown, nil
	}
	prompt := prompts.ChangelogFaithfulnessPrompt() +
		"\n\n## Original ## Unreleased body\n\n" + plan.OriginalUnreleased +
		"\n\n## Final " + plan.NextVersionHeader + " body\n\n" + finalBody
	claudeResult, err := s.runner.Run(ctx, prompt)
	if err != nil {
		glog.Warningf("ai_review: faithfulness LLM call failed: %v", err)
		checks.FaithfulnessOK = false
		*failedChecks = append(*failedChecks, CheckFaithfulness)
		return OverallUnknown, nil
	}
	resp, err := prompts.ParseFaithfulnessResponse(ctx, claudeResult.Result)
	if err != nil {
		glog.Warningf("ai_review: parse faithfulness response failed: %v", err)
		checks.FaithfulnessOK = false
		*failedChecks = append(*failedChecks, CheckFaithfulness)
		return OverallUnknown, nil
	}

	// Map FaithfulnessLLMResponse → flat ReviewOutput.PerEntry list.
	// per_entry is appended verbatim; extras is appended with
	// Verdict=FaithfulnessHallucinated (the LLM's per-extras
	// verdict is always "hallucinated" by parser contract).
	perEntry := make([]FaithfulnessVerdict, 0, len(resp.PerEntry)+len(resp.Extras))
	for _, e := range resp.PerEntry {
		perEntry = append(perEntry, FaithfulnessVerdict{
			Entry:   e.Entry,
			Verdict: e.Verdict,
			Note:    e.Note,
		})
	}
	for _, e := range resp.Extras {
		perEntry = append(perEntry, FaithfulnessVerdict{
			Entry:   e.Entry,
			Verdict: FaithfulnessHallucinated,
			Note:    e.Note,
		})
	}

	if resp.Overall == OverallPass {
		checks.FaithfulnessOK = true
		return OverallPass, perEntry
	}
	checks.FaithfulnessOK = false
	*failedChecks = append(*failedChecks, CheckFaithfulness)
	return OverallFail, perEntry
}

// notesFor returns a human-readable one-liner naming each failed check,
// or "all checks passed" on success. Mirrors today's behavior.
func (s *aiReviewStep) notesFor(failedChecks []string) string {
	if len(failedChecks) == 0 {
		return "all checks passed"
	}
	return "failed checks: " + strings.Join(failedChecks, ", ")
}

// rollupVerdict aggregates the per-check failure state into the
// single Overall string and the boolean Approved flag. The "unknown"
// verdict from the LLM override is layered on top of the binary
// pass/fail rollup so the human reviewer sees the distinction
// between "checks failed" and "LLM unreachable".
func rollupVerdict(
	faithfulnessOverall string,
	failedChecks []string,
) (overall string, approved bool) {
	overall = OverallPass
	approved = true
	if len(failedChecks) > 0 || faithfulnessOverall == OverallUnknown {
		overall = OverallFail
		approved = false
	}
	if faithfulnessOverall == OverallUnknown {
		// Override: when the LLM is unreachable we surface
		// "unknown" at the overall level per spec failure-mode
		// "LLM unavailability".
		overall = OverallUnknown
	}
	return overall, approved
}

// diffStringSet returns the set difference a - b as a sorted slice.
// Used to populate ReviewOutput.UnexpectedFiles with the offending
// paths in deterministic order.
func diffStringSet(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, v := range b {
		set[v] = struct{}{}
	}
	var out []string
	for _, v := range a {
		if _, ok := set[v]; !ok {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

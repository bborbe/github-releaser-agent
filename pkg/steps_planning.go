// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"

	agentlib "github.com/bborbe/agent"
	claudelib "github.com/bborbe/agent/claude"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-releaser-agent/pkg/changelog"
	"github.com/bborbe/github-releaser-agent/pkg/githubchangelog"
	"github.com/bborbe/github-releaser-agent/pkg/githubtags"
	"github.com/bborbe/github-releaser-agent/pkg/maintainerconfig"
	"github.com/bborbe/github-releaser-agent/pkg/prompts"
	"github.com/bborbe/github-releaser-agent/pkg/semver"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
)

// AgentLogin is the GitHub-task-system identity used in escalation frontmatter
// (previous_assignee). Per spec 047 § Constraints, this MUST be
// "github-releaser-agent" — grep-asserted by acceptance criteria.
const AgentLogin = "github-releaser-agent"

// requiredFrontmatterFields are the keys read from the task's frontmatter
// before the step does any IO. Missing OR empty → outcome=needs_input
// with precondition_failed = "missing_frontmatter_<field>".
//
// Order matters for deterministic error messages: first missing field wins.
var requiredFrontmatterFields = []string{
	"repo",
	"clone_url",
	"ref",
	"current_version",
	"task_identifier",
}

// planningStep implements agentlib.Step. Fields are constructor-injected;
// no global state, no IO outside the runner and fetchers.
type planningStep struct {
	runner           claudelib.ClaudeRunner
	fetcher          githubchangelog.Fetcher
	maintainerConfig maintainerconfig.Fetcher
	tagsFetcher      githubtags.TagsFetcher
	allowMajor       bool
}

// NewPlanningStep wires the planning step with its five IO seams:
//   - the Claude runner (LLM verdict for bump + rewrite)
//   - the CHANGELOG.md fetcher (GitHub contents API)
//   - the .maintainer.yaml fetcher (GitHub contents API, spec 059)
//   - the spec-060 per-run override: when true, the major-bump guard
//     is bypassed; equivalent to cfg.Release.AllowMajorBump==true.
//   - the GitHub tags fetcher (spec 001: resolves current_version from
//     the remote's highest semver tag at plan time).
func NewPlanningStep(
	runner claudelib.ClaudeRunner,
	fetcher githubchangelog.Fetcher,
	maintainerConfig maintainerconfig.Fetcher,
	tagsFetcher githubtags.TagsFetcher,
	allowMajor bool,
) agentlib.Step {
	return &planningStep{
		runner:           runner,
		fetcher:          fetcher,
		maintainerConfig: maintainerConfig,
		tagsFetcher:      tagsFetcher,
		allowMajor:       allowMajor,
	}
}

// Name implements agentlib.Step.
func (s *planningStep) Name() string { return "github-release-plan" }

// ShouldRun always returns true. The planning step is idempotent: a
// re-trigger replaces the existing ## Plan section in place. Returning
// false here would silently skip routing.
func (s *planningStep) ShouldRun(_ context.Context, _ *agentlib.Markdown) (bool, error) {
	return true, nil
}

// Run executes the planning pipeline: read frontmatter, fetch CHANGELOG,
// validate, classify bump (spec 001/063), resolve changelogRewrite (spec 059),
// optionally rewrite (spec 058), publish ## Plan.
func (s *planningStep) Run(ctx context.Context, md *agentlib.Markdown) (*agentlib.Result, error) {
	missingField, currentVersion, repo, cloneURL, ref := s.readRequired(md)
	if missingField != "" {
		glog.V(2).Infof("planning: missing frontmatter field=%s — escalating", missingField)
		return s.escalate(ctx, md, escalation{
			reason:             "required frontmatter field missing: " + missingField,
			preconditionFailed: PreconditionMissingFrontmatter + missingField,
			currentVersion:     currentVersion,
		})
	}

	owner, name, ok := parseOwnerRepo(repo)
	if !ok {
		glog.V(2).Infof("planning: malformed repo=%q — escalating", repo)
		return s.escalate(ctx, md, escalation{
			reason:             `frontmatter "repo" must be "owner/name"; got ` + repo,
			preconditionFailed: PreconditionMissingFrontmatter + "repo",
			currentVersion:     currentVersion,
		})
	}
	_ = cloneURL // currently unused by planning; future execution step will use it

	// spec 001: resolve the effective current_version from the remote's
	// highest semver tag. The frontmatter snapshot is the fallback.
	effectiveVersion, tagWarning, _ := s.resolveCurrentVersion(ctx, owner, name, currentVersion)

	cachedBump, cachedReasoning := s.readCachedBump(ctx, md)

	changelogBytes, err := s.fetcher.Fetch(ctx, owner, name, ref)
	if err != nil {
		glog.V(2).Infof("planning: fetch CHANGELOG.md failed: %v", err)
		return &agentlib.Result{
			Status:  agentlib.AgentStatusFailed,
			Message: "fetch CHANGELOG.md: " + err.Error(),
		}, nil
	}
	glog.V(2).
		Infof("planning: fetched CHANGELOG.md owner=%s name=%s ref=%s bytes=%d", owner, name, ref, len(changelogBytes))
	valid, reason, _ := changelog.ValidateUnreleased(changelogBytes)
	if !valid {
		precondition := classifyValidationFailure(reason)
		glog.V(2).
			Infof("planning: validate Unreleased failed precondition=%s reason=%q", precondition, reason)
		return s.escalate(ctx, md, escalation{
			reason:             reason,
			preconditionFailed: precondition,
			currentVersion:     currentVersion,
		})
	}
	bullets := changelog.ExtractUnreleasedBullets(changelogBytes)
	prefixStyle := changelog.InferHeaderPrefixStyle(changelogBytes)
	originalBody, err := changelog.ExtractUnreleasedBody(ctx, changelogBytes)
	if err != nil {
		glog.V(2).Infof("planning: extract unreleased body failed: %v", err)
		return &agentlib.Result{
			Status:  agentlib.AgentStatusFailed,
			Message: "extract unreleased body: " + err.Error(),
		}, nil
	}
	changelogRewrite, allowMajorBump, configWarning, err := s.resolveMaintainerConfig(
		ctx,
		owner,
		name,
		ref,
	)
	if err != nil {
		// Fail-closed: .maintainer.yaml is malformed OR contains a
		// non-boolean release.changelogRewrite. Write a ## Plan(failed)
		// block, set the controller to human_review, do NOT advance.
		return s.failInvalidConfig(ctx, md, currentVersion, "release.changelogRewrite", err)
	}

	// Merge tag-fetch and config-fetch warnings so both are grep-able
	// on the plan block. Join with "; " when both non-empty; use whichever
	// is non-empty when only one is; empty when neither.
	fetchWarning := joinWarnings(configWarning, tagWarning)

	return s.runClassification(
		ctx,
		md,
		effectiveVersion,
		bullets,
		prefixStyle,
		originalBody,
		changelogRewrite,
		allowMajorBump,
		cachedBump,
		cachedReasoning,
		fetchWarning,
	)
}

// joinWarnings merges two non-fatal warning strings. Returns the non-empty
// one when only one is set, joins with "; " when both are non-empty, and
// returns "" when neither is set.
func joinWarnings(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// readCachedBump returns the bump verdict from a prior partial run (e.g.
// a re-fire after the rewrite LLM transiently failed). Empty on a fresh
// run or when the prior plan was outcome=needs_input/failed (those don't
// carry a real bump). When set, runClassification skips the bump LLM call
// and reuses verdict+reasoning verbatim.
//
// ExtractSection error is non-fatal — a fresh task page has no ## Plan
// section yet; that is the common case.
func (s *planningStep) readCachedBump(
	ctx context.Context,
	md *agentlib.Markdown,
) (string, string) {
	prior, perr := agentlib.ExtractSection[PlanOutput](ctx, md, "## Plan")
	if perr != nil {
		return "", ""
	}
	if prior.Outcome != PlanOutcomeReady || prior.Bump == "" {
		return "", ""
	}
	glog.V(2).Infof(
		"planning: reusing cached bump=%s from prior ## Plan (skipping bump LLM)",
		prior.Bump,
	)
	return prior.Bump, prior.Reasoning
}

// resolveMaintainerConfig fetches .maintainer.yaml at the ref's tip and
// returns the parsed release.changelogRewrite (spec 059) and
// release.allowMajorBump (spec 060) flag values, with these semantics
// (per spec 059 § Desired Behavior and § Failure Modes, and spec 060
// § Desired Behavior):
//
//   - File absent (ErrFileNotFound)        → (false, false, "", nil)  // default, no warning
//   - File present, malformed YAML         → (false, false, "", wrappedErr) // fail-closed; caller maps to human_review
//   - File present, non-boolean value      → (false, false, "", wrappedErr) // fail-closed; same
//   - Any other fetch error (5xx, network) → (false, false, "<warning>", nil)  // treated as default; warning surfaced on plan
//
// The "any other fetch error → default" rule is the spec's Failure Modes
// "Repo has no .maintainer.yaml" + spec § Desired Behavior 6: missing-yaml
// is treated as `false` cleanly. The spec does NOT extend fail-closed to
// transport errors (those are usually transient GitHub flakes; the
// operator can re-fire). Only the parse / non-boolean boundary is
// fail-closed (a config typo on a high-trust field, per spec § Security).
//
// The middle-return value is a non-fatal warning string surfaced on the
// ## Plan task-page block so a repo that opted into rewrite is not
// silently downgraded on a transient flake — operators can grep
// PlanOutput.ConfigFetchWarning to confirm. Empty on the happy path
// and on the legitimate-absent (404) path.
//
// The single-fetch invariant is preserved: this helper is the ONE call
// site for `s.maintainerConfig.Fetch` at planning entry. Spec 059's
// "flag-read-once" rule and spec 060's "resolve both opt-in flags
// together" rule both flow through here — the call site gets both
// resolved bools from one parse.
func (s *planningStep) resolveMaintainerConfig(
	ctx context.Context,
	owner, name, ref string,
) (changelogRewrite bool, allowMajorBump bool, fetchWarning string, err error) {
	bytes, err := s.maintainerConfig.Fetch(ctx, owner, name, ref)
	if err != nil {
		if stderrors.Is(err, maintainerconfig.ErrFileNotFound) {
			glog.V(2).Infof(
				"planning: .maintainer.yaml absent at ref=%s — using default changelogRewrite=false, allowMajorBump=false",
				ref,
			)
			return false, false, "", nil
		}
		// Transport / non-404 error: log and default to false. NOT a
		// fail-closed condition (see spec 059 § Failure Modes).
		glog.Warningf("planning: .maintainer.yaml fetch failed (treated as default): %v", err)
		return false, false, fmt.Sprintf(
			".maintainer.yaml fetch failed (treated as default changelogRewrite=false, allowMajorBump=false): %s",
			err.Error(),
		), nil
	}
	cfg, err := maintainerconfig.Parse(ctx, bytes)
	if err != nil {
		// YAML parse error or non-boolean value: fail-closed. Surface the
		// original error so the caller can include it in the human_review
		// task-page block.
		return false, false, "", errors.Wrapf(ctx, err, "parse .maintainer.yaml")
	}
	return cfg.Release.ChangelogRewrite, cfg.Release.AllowMajorBump, "", nil
}

// resolveCurrentVersion returns the effective current_version to bump from,
// resolving it from the target repo's highest remote semver tag at plan
// time (spec 001). The frontmatter snapshot is used ONLY as a fallback.
// Fallback semantics mirror resolveMaintainerConfig:
//
//   - Remote has a semver tag        → (remoteTag, "", nil)   // remote wins; prefix preserved
//   - Remote has no usable tag       → (snapshot, "", nil)    // ErrNoTags → clean fallback, V(2) only, no warning
//   - Transient fetch error (5xx/net)→ (snapshot, "<warn>", nil) // degrade, surface non-fatal warning; do NOT fail-closed
//
// The middle return is a non-fatal warning surfaced on PlanOutput.ConfigFetchWarning
// so an operator can grep a repo whose release bumped from the (possibly stale)
// snapshot on a transient GitHub flake. Empty on the remote-wins and clean-no-tags paths.
//
//nolint:unparam // err is always nil; kept for API symmetry with resolveMaintainerConfig.
func (s *planningStep) resolveCurrentVersion(
	ctx context.Context,
	owner, name, snapshot string,
) (effective string, fetchWarning string, err error) {
	latest, ferr := s.tagsFetcher.LatestSemverTag(ctx, owner, name)
	if ferr != nil {
		if stderrors.Is(ferr, githubtags.ErrNoTags) {
			glog.V(2).Infof(
				"planning: no remote tags for %s/%s — using snapshot current_version=%s",
				owner, name, snapshot,
			)
			return snapshot, "", nil
		}
		glog.Warningf(
			"planning: remote tag fetch failed for %s/%s (using snapshot %s): %v",
			owner, name, snapshot, ferr,
		)
		return snapshot, fmt.Sprintf(
			"remote tag lookup failed (using snapshot current_version=%s): %s",
			snapshot, ferr.Error(),
		), nil
	}
	glog.V(2).Infof(
		"planning: resolved current_version from remote latest tag %s/%s: %s (snapshot was %s)",
		owner, name, latest, snapshot,
	)
	return latest, "", nil
}

// majorBumpPolicySection returns the runtime-injected classifier policy for
// the allowed bump range, concatenated into the bump-classification prompt
// exactly like the "## Current version" section. When major is NOT allowed
// it forbids a `major` verdict and requires the reasoning to note the cap —
// mirroring the embedded Pre-1.0 cap. When major IS allowed it returns the
// empty string, leaving the classifier its full major|minor|patch range.
func majorBumpPolicySection(majorAllowed bool) string {
	if majorAllowed {
		return ""
	}
	return "\n\n## Major-bump policy\n\n" +
		"Major bumps are NOT permitted for this release. You MUST NOT return `major`. " +
		"If a bullet would otherwise be a breaking or major change, return `minor` instead, " +
		"and your `reasoning` MUST mention that the major bump was capped."
}

// resolveBumpVerdict returns the bump verdict either from a prior cached
// ## Plan (M2 cache) or by issuing a fresh LLM call. On runner or parse
// error it returns a non-nil *agentlib.Result carrying AgentStatusFailed
// so the caller can short-circuit cleanly.
func (s *planningStep) resolveBumpVerdict(
	ctx context.Context,
	bullets []string,
	currentVersion string,
	majorAllowed bool,
	cachedBump, cachedReasoning string,
) (prompts.BumpVerdict, *agentlib.Result) {
	if cachedBump != "" {
		glog.V(2).Infof("planning: skipping bump LLM call (cached bump=%s)", cachedBump)
		return prompts.BumpVerdict{Bump: cachedBump, Reasoning: cachedReasoning}, nil
	}
	userMsg := strings.Join(bullets, "\n")
	versionSection := "\n\n## Current version\n\n" + currentVersion
	fullPrompt := prompts.BumpClassificationPrompt() +
		versionSection +
		majorBumpPolicySection(majorAllowed) +
		"\n\n## Bullets to classify\n\n" + userMsg
	runResult, err := s.runner.Run(ctx, fullPrompt)
	if err != nil {
		glog.V(2).Infof("planning: claude runner failed: %v", err)
		return prompts.BumpVerdict{}, &agentlib.Result{
			Status:  agentlib.AgentStatusFailed,
			Message: "claude run: " + err.Error(),
		}
	}
	v, err := prompts.ParseBumpVerdict(ctx, runResult.Result)
	if err != nil {
		glog.V(2).Infof("planning: parse verdict failed: %v", err)
		return prompts.BumpVerdict{}, &agentlib.Result{
			Status:  agentlib.AgentStatusFailed,
			Message: "parse bump verdict: " + err.Error(),
		}
	}
	return v, nil
}

func (s *planningStep) runClassification(
	ctx context.Context,
	md *agentlib.Markdown,
	currentVersion string,
	bullets []string,
	prefixStyle string,
	originalBody string,
	changelogRewrite bool,
	allowMajorBumpConfig bool,
	cachedBump, cachedReasoning string,
	fetchWarning string,
) (*agentlib.Result, error) {
	// Effective "major allowed" = the target repo's .maintainer.yaml opt-in
	// OR the per-run --allow-major / ALLOW_MAJOR override. Either one → the
	// classifier keeps its full major|minor|patch range and no clamp fires.
	majorAllowed := allowMajorBumpConfig || s.allowMajor

	verdict, result := s.resolveBumpVerdict(
		ctx,
		bullets,
		currentVersion,
		majorAllowed,
		cachedBump,
		cachedReasoning,
	)
	if result != nil {
		return result, nil
	}

	nextNumeric, err := semver.BumpVersion(ctx, currentVersion, verdict.Bump)
	if err != nil {
		glog.V(2).Infof("planning: bump version failed: %v", err)
		return s.escalate(ctx, md, escalation{
			reason:             err.Error(),
			preconditionFailed: PreconditionBadCurrentVersion,
			currentVersion:     currentVersion,
		})
	}

	// Clamp a disallowed major bump down to minor instead of escalating.
	// A release never enters human_review solely because a major bump is
	// not permitted: a would-be major ships as a minor. The injected prompt
	// policy above guides the LLM to the same result; this code is the
	// deterministic guarantee even if the LLM returns `major` anyway.
	if verdict.Bump == "major" && !majorAllowed {
		clampedNext, cerr := semver.BumpVersion(ctx, currentVersion, "minor")
		if cerr != nil {
			glog.V(2).Infof("planning: clamp recompute failed: %v", cerr)
			return s.escalate(ctx, md, escalation{
				reason:             cerr.Error(),
				preconditionFailed: PreconditionBadCurrentVersion,
				currentVersion:     currentVersion,
			})
		}
		glog.V(2).Infof(
			"planning: major bump not allowed — clamping major→minor (allowMajorBumpConfig=%t, allowMajorFlag=%t): %s → %s",
			allowMajorBumpConfig, s.allowMajor, currentVersion, clampedNext,
		)
		verdict.Bump = "minor"
		verdict.Reasoning += " (major bump not permitted — capped to minor)"
		nextNumeric = clampedNext
	}

	return s.resolveRewriteAndPublish(
		ctx,
		md,
		currentVersion,
		bullets,
		prefixStyle,
		originalBody,
		verdict,
		nextNumeric,
		changelogRewrite,
		fetchWarning,
	)
}

// resolveRewriteAndPublish runs the rewrite verdict (gated by
// changelogRewrite) and publishes the plan. On rewrite failure it
// publishes a partial plan (with bump verdict populated, rewrite
// verdict zero) BEFORE returning Failed — the M2 re-fire cache
// needs the prior ## Plan to carry the bump verdict so the bump
// LLM call is NOT re-issued on retry. Without this, a transient
// rewrite-LLM failure would cause every re-fire to re-run bump
// classification.
func (s *planningStep) resolveRewriteAndPublish(
	ctx context.Context,
	md *agentlib.Markdown,
	currentVersion string,
	bullets []string,
	prefixStyle, originalBody string,
	verdict prompts.BumpVerdict,
	nextNumeric string,
	changelogRewrite bool,
	fetchWarning string,
) (*agentlib.Result, error) {
	rewriteVerdict, err := s.resolveRewriteVerdict(ctx, originalBody, changelogRewrite)
	if err != nil {
		glog.V(2).
			Infof("planning: rewrite failed: %v — publishing partial plan for re-fire cache", err)
		if _, perr := s.publishPlan(
			ctx,
			md,
			currentVersion,
			prefixStyle,
			bullets,
			originalBody,
			verdict,
			nextNumeric,
			prompts.RewriteVerdict{},
			changelogRewrite,
			fetchWarning,
		); perr != nil {
			return nil, errors.Wrap(ctx, perr, "publish partial plan (rewrite failure)")
		}
		return &agentlib.Result{
			Status:  agentlib.AgentStatusFailed,
			Message: err.Error(),
		}, nil
	}

	return s.publishPlan(
		ctx,
		md,
		currentVersion,
		prefixStyle,
		bullets,
		originalBody,
		verdict,
		nextNumeric,
		rewriteVerdict,
		changelogRewrite,
		fetchWarning,
	)
}

// resolveRewriteVerdict returns the rewrite verdict for the current
// Unreleased body, gated by the spec-059 changelogRewrite flag. When the
// flag is false the planning LLM is NOT invoked; the verdict is hard-coded
// to RewriteNeeded=false with a tracing reasoning string. When the flag
// is true the existing 058 rewrite pipeline runs unchanged.
func (s *planningStep) resolveRewriteVerdict(
	ctx context.Context,
	originalBody string,
	changelogRewrite bool,
) (prompts.RewriteVerdict, error) {
	if !changelogRewrite {
		glog.V(2).Infof("planning: changelogRewrite=false — skipping rewrite LLM call")
		return prompts.RewriteVerdict{
			RewriteNeeded:       false,
			RewrittenUnreleased: "",
			Reasoning:           "changelogRewrite flag is false (default or explicit) — pre-058 header-rename-only behavior",
		}, nil
	}
	return s.runRewrite(ctx, originalBody)
}

// publishPlan assembles the PlanOutput, marshals the ## Plan section,
// replaces it in the markdown, and returns the Done result.
func (s *planningStep) publishPlan(
	ctx context.Context,
	md *agentlib.Markdown,
	currentVersion, prefixStyle string,
	bullets []string,
	originalBody string,
	verdict prompts.BumpVerdict,
	nextNumeric string,
	rewriteVerdict prompts.RewriteVerdict,
	changelogRewrite bool,
	fetchWarning string,
) (*agentlib.Result, error) {
	header := "## " + prefixStyle + nextNumeric
	crValue := changelogRewrite
	output := PlanOutput{
		Outcome:             PlanOutcomeReady,
		Bump:                verdict.Bump,
		Reasoning:           verdict.Reasoning,
		CurrentVersion:      currentVersion,
		NextVersion:         nextNumeric,
		NextVersionHeader:   header,
		HeaderPrefixStyle:   prefixStyle,
		Bullets:             bullets,
		OriginalUnreleased:  originalBody,
		RewriteNeeded:       rewriteVerdict.RewriteNeeded,
		RewrittenUnreleased: rewriteVerdict.RewrittenUnreleased,
		ChangelogRewrite:    &crValue, // pointer so JSON distinguishes resolved-false from missing
		ConfigFetchWarning:  fetchWarning,
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Plan", output)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "marshal ## Plan section")
	}
	md.ReplaceSection(section)

	return &agentlib.Result{
		Status:    agentlib.AgentStatusDone,
		NextPhase: string(domain.TaskPhaseExecution),
	}, nil
}

// runRewrite runs the changelog-rewrite classification against the verbatim
// ## Unreleased body and returns the parsed verdict. The two Claude calls
// (bump and rewrite) are kept separate so each LLM gets a focused prompt
// and so the rewrite failure mode is distinguishable from a bump failure
// at the planning layer. Returns a wrapped error containing the relevant
// failure message — the caller maps it to AgentStatusFailed.
func (s *planningStep) runRewrite(
	ctx context.Context,
	originalBody string,
) (prompts.RewriteVerdict, error) {
	rewritePrompt := prompts.ChangelogRewritePrompt() +
		"\n\n## Changelog Quality Guide\n\n" + prompts.ChangelogQualityGuide() +
		"\n\n## Current ## Unreleased body\n\n" + originalBody
	runResult, err := s.runner.Run(ctx, rewritePrompt)
	if err != nil {
		return prompts.RewriteVerdict{},
			errors.Wrap(ctx, err, "claude run rewrite")
	}
	verdict, err := prompts.ParseRewriteVerdict(ctx, runResult.Result)
	if err != nil {
		return prompts.RewriteVerdict{},
			errors.Wrap(ctx, err, "parse rewrite verdict")
	}
	return verdict, nil
}

// escalation captures the fields the escalate path needs to assemble the
// needs_input PlanOutput. Keeping it as a value type makes the call sites
// explicit and prevents missing-field bugs.
type escalation struct {
	reason             string
	preconditionFailed string
	currentVersion     string
}

// escalate writes a ## Plan(needs_input) section, clears `assignee`,
// sets `previous_assignee: github-releaser-agent`, and returns
// NeedsInput. status + phase are LEFT UNCHANGED — per spec 047
// § Constraints and [[Agent Task File Contract]] escalation rule.
//
// Returning AgentStatusNeedsInput (NOT Done) is critical: the framework
// deliverer switch (FileResultDeliverer / KafkaResultDeliverer) maps
// NeedsInput to "status: in_progress, assignee cleared, phase preserved"
// — exactly the escalation contract. Returning Done with empty NextPhase
// instead auto-advances to "phase: done, status: completed" (bug 048).
// The controller does not retry NeedsInput; the human operator
// re-delegates by re-setting assignee.
func (s *planningStep) escalate(
	ctx context.Context,
	md *agentlib.Markdown,
	e escalation,
) (*agentlib.Result, error) {
	output := PlanOutput{
		Outcome:            PlanOutcomeNeedsInput,
		Reason:             e.reason,
		PreconditionFailed: e.preconditionFailed,
		CurrentVersion:     e.currentVersion,
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Plan", output)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "marshal ## Plan section (needs_input)")
	}
	md.ReplaceSection(section)

	// Frontmatter mutation: clear assignee, set previous_assignee.
	// Use direct map writes; TaskFrontmatter is map[string]interface{}.
	md.Frontmatter["assignee"] = ""
	md.Frontmatter["previous_assignee"] = AgentLogin

	return &agentlib.Result{
		Status:  agentlib.AgentStatusNeedsInput,
		Message: e.reason,
	}, nil
}

// failInvalidConfig writes a ## Plan(outcome=failed) section naming
// the invalid field and the wrapped error, and returns
// Status=AgentStatusFailed + NextPhase=human_review. The framework's
// agent runner treats that combination as a terminal human_review
// escalation (no retry, no advance). The task page is the audit
// surface — a reader can grep for `error_category=invalid_config`
// on the `## Plan` block to find the failure.
func (s *planningStep) failInvalidConfig(
	ctx context.Context,
	md *agentlib.Markdown,
	currentVersion, field string,
	cause error,
) (*agentlib.Result, error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	output := PlanOutput{
		Outcome:        PlanOutcomeFailed,
		ErrorCategory:  ErrorCategoryInvalidConfig,
		InvalidField:   field,
		InvalidValue:   extractInvalidValue(msg),
		CurrentVersion: currentVersion,
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Plan", output)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "marshal ## Plan section (failed)")
	}
	md.ReplaceSection(section)
	glog.V(2).Infof("planning: invalid config: field=%s err=%v", field, cause)
	return &agentlib.Result{
		Status:    agentlib.AgentStatusFailed,
		NextPhase: string(domain.TaskPhaseHumanReview),
		Message:   "invalid .maintainer.yaml: " + field + ": " + msg,
	}, nil
}

// extractInvalidValue pulls the raw bad value out of the wrapped
// parse error message so it lands verbatim in the task-page block.
// The yaml.v3 error format is e.g.
//
//	"yaml: unmarshal errors: line 2: cannot unmarshal !!str `yes` into bool"
//
// We surface the offending token; on parse-format drift, fall back
// to the full error string so the field is never blank.
func extractInvalidValue(msg string) string {
	if i := strings.Index(msg, "`"); i >= 0 {
		if j := strings.Index(msg[i+1:], "`"); j >= 0 {
			return msg[i+1 : i+1+j]
		}
	}
	return msg
}

// readRequired pulls the five required frontmatter fields. Returns the
// first missing field's name (or "" if all present), plus the resolved
// values for current_version, repo, clone_url, ref. Empty string counts
// as missing.
func (s *planningStep) readRequired(
	md *agentlib.Markdown,
) (missing, currentVersion, repo, cloneURL, ref string) {
	values := map[string]string{}
	for _, key := range requiredFrontmatterFields {
		v, _ := md.Frontmatter.String(key)
		if strings.TrimSpace(v) == "" {
			return key, values["current_version"], values["repo"], values["clone_url"], values["ref"]
		}
		values[key] = v
	}
	return "", values["current_version"], values["repo"], values["clone_url"], values["ref"]
}

// parseOwnerRepo splits an "owner/name" string. Empty or no-slash input
// returns ok=false.
func parseOwnerRepo(s string) (owner, name string, ok bool) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// classifyValidationFailure maps the validator's reason string to the
// typed PreconditionFailed value. The reason strings are produced by
// changelog.ValidateUnreleased in pkg/changelog/changelog.go.
func classifyValidationFailure(reason string) string {
	switch {
	case strings.Contains(reason, "is not the first ## section"):
		return PreconditionP1UnreleasedNotFirst
	case strings.Contains(reason, "no bullet entries"),
		strings.Contains(reason, "not found"):
		return PreconditionP2UnreleasedEmpty
	default:
		return PreconditionP2UnreleasedEmpty
	}
}

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
	allowMajor       bool
}

// NewPlanningStep wires the planning step with its four IO seams:
//   - the Claude runner (LLM verdict for bump + rewrite)
//   - the CHANGELOG.md fetcher (GitHub contents API)
//   - the .maintainer.yaml fetcher (GitHub contents API, spec 059)
//   - the spec-060 per-run override: when true, the major-bump guard
//     is bypassed; equivalent to cfg.Release.AllowMajorBump==true.
func NewPlanningStep(
	runner claudelib.ClaudeRunner,
	fetcher githubchangelog.Fetcher,
	maintainerConfig maintainerconfig.Fetcher,
	allowMajor bool,
) agentlib.Step {
	return &planningStep{
		runner:           runner,
		fetcher:          fetcher,
		maintainerConfig: maintainerConfig,
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

// Run executes the planning pipeline. Eight outcomes:
//  1. Missing frontmatter        → escalate (NeedsInput, ## Plan needs_input, clear assignee)
//  2. CHANGELOG fetch fails      → Failed (controller retries)
//  3. P1/P2 validation fails     → escalate
//  4. Claude verdict unparseable → Failed (controller retries)
//  5. semver.BumpVersion fails   → escalate
//  6. Resolve release.changelogRewrite from .maintainer.yaml at the ref's tip
//     - ErrFileNotFound or any fetch transport error → treat as false, log V(2), continue
//     - Parse error containing "unmarshal" → fail-closed (outcome=failed, error_category=invalid_config)
//     - Resolved true  → run rewrite LLM call (existing 058 path)
//     - Resolved false → SKIP rewrite LLM call, set plan.ChangelogRewrite=ptr(false), plan.RewriteNeeded=false
//  7. Rewrite LLM call (only if step 6 returned true)
//  8. Happy path                 → Done, NextPhase = execution, ## Plan ready
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

	changelogRewrite, allowMajorBump, fetchWarning, err := s.resolveMaintainerConfig(
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
	return s.runClassification(
		ctx,
		md,
		currentVersion,
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

// resolveBumpVerdict returns the bump verdict either from a prior cached
// ## Plan (M2 cache) or by issuing a fresh LLM call. On runner or parse
// error it returns a non-nil *agentlib.Result carrying AgentStatusFailed
// so the caller can short-circuit cleanly.
func (s *planningStep) resolveBumpVerdict(
	ctx context.Context,
	bullets []string,
	currentVersion string,
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
	verdict, result := s.resolveBumpVerdict(
		ctx,
		bullets,
		currentVersion,
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

	// Spec 060 major-bump guard. Decision table is enforced inside
	// applyMajorBumpGuard (see GoDoc on that helper).
	guardResult, gerr := s.applyMajorBumpGuard(
		ctx,
		md,
		verdict,
		allowMajorBumpConfig,
		currentVersion,
		nextNumeric,
		prefixStyle,
		bullets,
	)
	if gerr != nil {
		return nil, gerr
	}
	if guardResult != nil {
		return guardResult, nil
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

// applyMajorBumpGuard evaluates the spec 060 decision table on the
// Claude bump verdict + the two opt-in flag sources (target repo's
// .maintainer.yaml `release.allowMajorBump` and the per-run
// `--allow-major` / `ALLOW_MAJOR` CLI flag). The decision table is
// FROZEN per spec 060 § Desired Behavior 3; any change here MUST
// update the spec table AND the spec's acceptance criteria first.
//
//	| bump  | allowMajorBumpConfig | allowMajor (flag) | result                            |
//	|-------|----------------------|-------------------|-----------------------------------|
//	| major | false                | false             | TRIP → NeedsInput (escalate)      |
//	| major | true                 | *                 | proceed (repo opted in)           |
//	| major | false                | true              | proceed + glog.V(2) override log  |
//	| other | *                    | *                 | proceed (no-op for guard)         |
//
// On TRIP the function returns the escalation Result (s.escalate
// writes the needs_input ## Plan block, clears assignee, sets
// previous_assignee=github-releaser-agent). On proceed it returns
// (nil, nil) so the caller advances to the rewrite verdict /
// publishPlan step. The override-path glog line is emitted as a
// side-effect of the proceed branch (no control-flow impact — it is
// an audit trail for kubectl-logs greps).
//
// The preconditionFailed token on TRIP is the literal
// "major_bump_not_allowed" (PreconditionMajorBumpNotAllowed);
// operators grep the task page for that token to find this run.
func (s *planningStep) applyMajorBumpGuard(
	ctx context.Context,
	md *agentlib.Markdown,
	verdict prompts.BumpVerdict,
	allowMajorBumpConfig bool,
	currentVersion, nextNumeric, prefixStyle string,
	bullets []string,
) (*agentlib.Result, error) {
	if verdict.Bump != "major" {
		return nil, nil
	}
	if allowMajorBumpConfig {
		return nil, nil
	}
	if s.allowMajor {
		// CLI override; repo has not opted in. Log audit line and proceed.
		glog.V(2).Infof("planning: --allow-major override accepted for major bump")
		return nil, nil
	}
	// TRIP: bump=major, no opt-in from either source.
	glog.V(2).Infof(
		"planning: major bump not allowed: bump=major, allowMajorBumpConfig=%t, allowMajorFlag=%t, reasoning=%q",
		allowMajorBumpConfig, s.allowMajor, verdict.Reasoning,
	)
	header := "## " + prefixStyle + nextNumeric
	return s.escalate(ctx, md, escalation{
		reason:               "major bump not allowed: " + verdict.Reasoning,
		preconditionFailed:   PreconditionMajorBumpNotAllowed,
		currentVersion:       currentVersion,
		nextVersion:          nextNumeric,
		nextVersionHeader:    header,
		bump:                 verdict.Bump,
		bullets:              bullets,
		reasoning:            verdict.Reasoning,
		allowMajorBumpConfig: allowMajorBumpConfig,
		allowMajorBumpFlag:   s.allowMajor,
	})
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
	// Spec 060 trip-case fields. All optional — P1/P2 escalation
	// paths pass zero values; the major-bump guard trip case
	// populates all of them so the operator sees the would-be
	// release shape on the task page.
	nextVersion          string
	nextVersionHeader    string
	bump                 string
	bullets              []string
	reasoning            string
	allowMajorBumpConfig bool
	allowMajorBumpFlag   bool
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
		Outcome:              PlanOutcomeNeedsInput,
		Reason:               e.reason,
		PreconditionFailed:   e.preconditionFailed,
		CurrentVersion:       e.currentVersion,
		NextVersion:          e.nextVersion,
		NextVersionHeader:    e.nextVersionHeader,
		Bump:                 e.bump,
		Bullets:              e.bullets,
		Reasoning:            e.reasoning,
		AllowMajorBumpConfig: e.allowMajorBumpConfig,
		AllowMajorBumpFlag:   e.allowMajorBumpFlag,
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

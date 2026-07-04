// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	agentlib "github.com/bborbe/agent"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-releaser-agent/pkg/changelog"
	"github.com/bborbe/github-releaser-agent/pkg/git"
	"github.com/bborbe/github-releaser-agent/pkg/plugin"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
)

// lsRemoteTimeout caps the post-check's `git ls-remote` round-trip so
// a stalled GitHub, DNS hang, or TCP backoff cannot block the agent
// indefinitely. 30s is generous compared to the typical sub-second
// round-trip and small compared to the agent's overall step budget.
const lsRemoteTimeout = 30 * time.Second

// changelogFileName is the only file the execution step rewrites in the
// cloned target repo. Spec 049 § Non-goals explicitly defers mono-repo
// support (multiple CHANGELOGs in one repo).
const changelogFileName = "CHANGELOG.md"

// workdirPrefix is the os.TempDir-rooted prefix used for ephemeral clone
// workdirs. Full path: <tempdir>/<workdirPrefix><task_identifier>/.
// On the happy path the directory is INTENTIONALLY preserved past Run's
// return so the next phase (ai_review) can read `git log -1 --name-only`
// against it. The ai-review step owns workdir lifecycle for terminal
// transitions (Approved+push done → Done, or human_review exit). The
// failure-path defer below still removes it.
const workdirPrefix = "github-releaser-"

// executionStep implements agentlib.Step. Dependencies are constructor-injected;
// no global state. Both ops (clone/commit/tag/push) and cloneURLBuilder are
// mockable seams — the integration tests in steps_execution_test.go use a
// counterfeiter GitOps mock and a stub URL builder.
type executionStep struct {
	ops     git.GitOps
	ghToken string
}

// NewExecutionStep wires the execution step with its GitOps seam and the
// GitHub token (used for HTTPS auth URL transformation). Empty ghToken
// means clone goes out anonymously — fine for tests; production always
// supplies a token.
func NewExecutionStep(ops git.GitOps, ghToken string) agentlib.Step {
	return &executionStep{ops: ops, ghToken: ghToken}
}

// Name implements agentlib.Step.
func (s *executionStep) Name() string { return "github-release-execute" }

// ShouldRun returns true. The step is idempotent at the framework level:
// a re-trigger overwrites ## Result. The controller's per-task lock
// prevents concurrent invocations on the same task_identifier.
func (s *executionStep) ShouldRun(_ context.Context, _ *agentlib.Markdown) (bool, error) {
	return true, nil
}

// Run executes the local-release pipeline. Sequence:
//  1. Read & validate ## Plan(outcome=ready) + frontmatter
//  2. Create ephemeral workdir under os.TempDir()
//  3. Clone target repo via GitOps
//  4. Read + rewrite CHANGELOG.md (apply plan.RewrittenUnreleased body if
//     plan.RewriteNeeded, then rename the header to plan.NextVersionHeader)
//  5. Commit + annotated-tag
//  6. Write ## Result(outcome=released) and return Done/NextPhase=ai_review
//  7. Post-check tail — consult `git ls-remote refs/tags/<tag>` against
//     the same authed URL the Clone used. Remote shows tag at expected
//     SHA → upgrade verdict to "released" + ## Resolution + status:
//     completed / phase: done. Remote shows tag at a different SHA →
//     upgrade to "superseded". Remote empty / ls-remote error → no-op.
//     The post-check is internal to the agent (no Kafka envelope, no
//     agent-lib API change) and is idempotent on re-fires against a
//     task already in status: completed / aborted.
//
// Note: the network push happens in the ai_review step (spec 058 prompt 3),
// not here. The local clone + tag are preserved past Run's return so
// ai_review can read them.
//
// Failures at any step produce ## Result(outcome=failed) + error_category
// and return Status=Failed (controller retry per its cap). The workdir is
// removed on every failure path; the happy path keeps the workdir alive
// for ai_review.
func (s *executionStep) Run(ctx context.Context, md *agentlib.Markdown) (*agentlib.Result, error) {
	plan, err := s.validatePlan(ctx, md)
	if err != nil {
		return s.fail(
			ctx,
			md,
			git.ErrorCategoryUnknown,
			err,
			"",
			"",
			"",
			"",
		)
	}

	cloneURL, ref, taskID, err := s.extractFrontmatter(ctx, md)
	if err != nil {
		return s.fail(
			ctx,
			md,
			git.ErrorCategoryUnknown,
			err,
			"",
			strings.TrimPrefix(plan.NextVersionHeader, "## "),
			injectToken(normalizeCloneURLToHTTPS(cloneURL), s.ghToken),
			ref,
		)
	}

	workdir := s.setupWorkdir(taskID)
	// Conditional cleanup: only remove the workdir on the failure path.
	// The happy path leaves it in place so ai_review (next phase) can
	// read it via result.Workdir.
	releaseSuccess := false
	defer func() {
		if !releaseSuccess {
			if err := os.RemoveAll(workdir); err != nil {
				glog.Warningf("workdir cleanup failed: path=%s err=%v", workdir, err)
			}
		}
	}()

	tag := strings.TrimPrefix(plan.NextVersionHeader, "## ")
	authedURL := injectToken(normalizeCloneURLToHTTPS(cloneURL), s.ghToken)
	sha, tagName, failResult := s.executeLocalRelease(
		ctx,
		md,
		workdir,
		plan,
		ref,
		taskID,
		tag,
		authedURL,
	)
	if failResult != nil {
		return failResult, nil // fail() already called inside executeLocalRelease
	}

	output := ResultOutput{
		Outcome:   ResultOutcomeReleased,
		Path:      ResultPathDirectPush,
		CommitSHA: sha,
		Tag:       tagName,
		Workdir:   workdir,
		LocalTag:  tagName,
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Result", output)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal ## Result section")
	}
	md.ReplaceSection(section)

	// Post-check tail — consult the remote for the tag's actual SHA. A
	// non-empty observed SHA that matches `sha` upgrades the verdict to
	// "released" and rewrites the frontmatter to status: completed /
	// phase: done. A non-matching observed SHA fires the "superseded"
	// branch. An empty result or ls-remote error is a no-op — the
	// existing ## Result(outcome=released) stands unchanged.
	s.postCheck(ctx, md, taskID, tagName, authedURL, ref, sha)

	// Mark success: defer above will skip RemoveAll so the workdir (with
	// the local commit + tag) survives until ai_review finishes.
	releaseSuccess = true
	return &agentlib.Result{
		Status:    agentlib.AgentStatusDone,
		NextPhase: string(domain.TaskPhaseAIReview),
	}, nil
}

// validatePlan extracts and validates the ## Plan section.
func (s *executionStep) validatePlan(
	ctx context.Context,
	md *agentlib.Markdown,
) (*PlanOutput, error) {
	plan, err := agentlib.ExtractSection[PlanOutput](ctx, md, "## Plan")
	if err != nil || plan == nil {
		return nil, errors.Wrapf(ctx, err, "execution invoked but planning did not complete")
	}
	if plan.Outcome != PlanOutcomeReady || plan.NextVersion == "" || plan.NextVersionHeader == "" {
		return nil, errors.Errorf(
			ctx,
			"execution invoked with non-ready plan: outcome=%s next_version=%q next_version_header=%q",
			plan.Outcome,
			plan.NextVersion,
			plan.NextVersionHeader,
		)
	}
	return plan, nil
}

// extractFrontmatter reads the required frontmatter fields.
func (s *executionStep) extractFrontmatter(
	ctx context.Context,
	md *agentlib.Markdown,
) (cloneURL, ref, taskID string, _ error) {
	cloneURL, _ = md.Frontmatter.String("clone_url")
	ref, _ = md.Frontmatter.String("ref")
	taskID, _ = md.Frontmatter.String("task_identifier")
	if cloneURL == "" || ref == "" || taskID == "" {
		return "", "", "", errors.Errorf(
			ctx,
			"missing frontmatter: clone_url=%q ref=%q task_identifier=%q",
			cloneURL, ref, taskID,
		)
	}
	return cloneURL, ref, taskID, nil
}

// setupWorkdir returns the canonical workdir path for the given task ID
// and removes any stale copy from a prior run. Does NOT create the
// directory — the subsequent ops.Clone call creates it. Stale-removal
// failure is logged at Warning level and the path is returned anyway
// (Clone will then fail with a more actionable error).
func (s *executionStep) setupWorkdir(taskID string) string {
	workdir := filepath.Join(os.TempDir(), workdirPrefix+taskID)
	if err := os.RemoveAll(workdir); err != nil {
		glog.Warningf("remove stale workdir failed: path=%s err=%v", workdir, err)
	}
	return workdir
}

// executeLocalRelease runs the clone → (optional body rewrite) → header
// rename → commit → tag sequence. The network push happens in the ai_review
// step (spec 058 prompt 3), not here. The post-check (this prompt's helper,
// see postCheck on the type) is the only place the remote is consulted for
// tag state — every s.fail call site below threads taskID, tag, authedURL,
// ref, and an empty expectedSHA so the post-check can fire on the failure
// path. The authedURL is the same one Run built at entry; the tag is the
// bare semver derived from plan.NextVersionHeader.
//
// Returns (sha, tagName, nil) on success, or ( "", "", failResult) on failure
// where failResult is the result of calling s.fail() with the appropriate error.
//
//nolint:gocognit,funlen // multi-stage release pipeline with branching error paths
func (s *executionStep) executeLocalRelease(
	ctx context.Context,
	md *agentlib.Markdown,
	workdir string,
	plan *PlanOutput,
	ref string,
	taskID string,
	tag string,
	authedURL string,
) (sha, tagName string, _ *agentlib.Result) {
	if err := s.ops.Clone(ctx, authedURL, ref, workdir); err != nil {
		result, _ := s.fail(ctx, md, git.ClassifyError(err), err, taskID, tag, authedURL, ref)
		return "", "", result
	}

	// Detect plugin manifests BEFORE any writes.
	detectedManifests, err := plugin.DetectManifests(ctx, workdir)
	if err != nil {
		result, _ := s.fail(ctx, md, git.ErrorCategoryUnknown,
			errors.Wrapf(ctx, err, "detect plugin manifests in %s", workdir),
			taskID, tag, authedURL, ref)
		return "", "", result
	}

	changelogPath := filepath.Join(workdir, changelogFileName)
	content, err := os.ReadFile(
		changelogPath,
	) // #nosec G304 -- workdir is os.TempDir-rooted; filename is constant
	if err != nil {
		category := git.ErrorCategoryChangelogMissing
		if !os.IsNotExist(err) {
			category = git.ErrorCategoryUnknown
		}
		result, _ := s.fail(ctx, md, category, errors.Wrapf(ctx, err, "read %s", changelogPath),
			taskID, tag, authedURL, ref)
		return "", "", result
	}

	// Optional body rewrite: only when planning flagged the body as
	// non-conformant. Done in-memory BEFORE the header rename so the
	// final commit is a single atomic "rewrite + rename" change.
	rewritten := content
	if plan.RewriteNeeded {
		rewritten, err = changelog.ReplaceUnreleasedBody(
			ctx,
			content,
			plan.RewrittenUnreleased,
		)
		if err != nil {
			result, _ := s.fail(ctx, md, git.ErrorCategoryUnreleasedNotFound,
				errors.Wrap(ctx, err, "replace ## Unreleased body"),
				taskID, tag, authedURL, ref)
			return "", "", result
		}
	}

	rewritten, err = changelog.RewriteUnreleasedHeader(
		ctx,
		rewritten,
		plan.NextVersionHeader,
	)
	if err != nil {
		result, _ := s.fail(ctx, md, git.ErrorCategoryUnreleasedNotFound,
			errors.Wrap(ctx, err, "rewrite ## Unreleased"),
			taskID, tag, authedURL, ref)
		return "", "", result
	}
	if err := os.WriteFile(changelogPath, rewritten, 0o644); err != nil { // #nosec G306,G703 -- standard perms; workdir is os.TempDir-rooted
		result, _ := s.fail(ctx, md, git.ErrorCategoryUnknown,
			errors.Wrapf(ctx, err, "write %s", changelogPath),
			taskID, tag, authedURL, ref)
		return "", "", result
	}

	// Bump and write detected plugin manifests.
	unprefixedVersion := deriveUnprefixedVersion(plan.NextVersionHeader)
	for _, manifestPath := range detectedManifests {
		manifestAbsPath := filepath.Join(workdir, manifestPath)
		manifestContent, err := os.ReadFile(
			manifestAbsPath,
		) // #nosec G304 -- workdir is os.TempDir-rooted
		if err != nil {
			result, _ := s.fail(ctx, md, git.ErrorCategoryUnknown,
				errors.Wrapf(ctx, err, "read %s", manifestAbsPath),
				taskID, tag, authedURL, ref)
			return "", "", result
		}

		var rewrittenManifest []byte
		if strings.HasSuffix(manifestPath, "plugin.json") {
			rewrittenManifest, err = plugin.BumpPluginJSON(ctx, manifestContent, unprefixedVersion)
		} else if strings.HasSuffix(manifestPath, "marketplace.json") {
			rewrittenManifest, err = plugin.BumpMarketplaceJSON(ctx, manifestContent, unprefixedVersion)
		} else {
			result, _ := s.fail(ctx, md, git.ErrorCategoryUnknown,
				errors.Errorf(ctx, "unsupported manifest type: %s", manifestPath),
				taskID, tag, authedURL, ref)
			return "", "", result
		}
		if err != nil {
			result, _ := s.fail(ctx, md, git.ErrorCategoryPluginManifestInvalid,
				errors.Wrapf(ctx, err, "bump %s", manifestPath),
				taskID, tag, authedURL, ref)
			return "", "", result
		}

		if err := os.WriteFile(manifestAbsPath, rewrittenManifest, 0o644); err != nil { // #nosec G306,G703 -- standard perms; workdir is os.TempDir-rooted
			result, _ := s.fail(ctx, md, git.ErrorCategoryUnknown,
				errors.Wrapf(ctx, err, "write %s", manifestAbsPath),
				taskID, tag, authedURL, ref)
			return "", "", result
		}
	}

	tagName = strings.TrimPrefix(plan.NextVersionHeader, "## ")
	// Build the full commit path list: changelog + detected manifests (in that order).
	commitPaths := append([]string{changelogFileName}, detectedManifests...)
	sha, err = s.ops.Commit(ctx, workdir, "release "+tagName, commitPaths...)
	if err != nil {
		result, _ := s.fail(ctx, md, git.ClassifyError(err), err, taskID, tag, authedURL, ref)
		return "", "", result
	}
	// Pre-push guard: the release commit must touch exactly the files we
	// rewrote (changelog + detected manifests). Fail closed BEFORE tag
	// if anything else slipped in — the release trust model depends on
	// this commit being changelog+manifests-only. Push is no longer in
	// this step (moved to ai_review in spec 058), but the guard still
	// runs here so a non-conformant commit never gets a tag.
	expectedFiles := append([]string{changelogFileName}, detectedManifests...)
	if failResult := s.guardCommittedFiles(
		ctx, md, workdir, expectedFiles, taskID, tag, authedURL, ref,
	); failResult != nil {
		return "", "", failResult
	}
	if err := s.ops.Tag(ctx, workdir, tagName, "release "+tagName); err != nil {
		result, _ := s.fail(ctx, md, git.ClassifyError(err), err, taskID, tag, authedURL, ref)
		return "", "", result
	}
	return sha, tagName, nil
}

// guardCommittedFiles asserts the HEAD (release) commit changed exactly
// the expected files. On any deviation it writes a ## Result with
// error_category=unexpected_diff and returns a failed Result — the caller
// must abort before tag/push. Returns nil when the commit changed only
// the expected files. Both internal s.fail call sites (line 384 + 389)
// participate in the post-check via the threaded taskID / tag / authedURL
// / ref parameters.
func (s *executionStep) guardCommittedFiles(
	ctx context.Context,
	md *agentlib.Markdown,
	workdir string,
	expectedFiles []string,
	taskID string,
	tag string,
	authedURL string,
	ref string,
) *agentlib.Result {
	files, err := s.ops.CommittedFiles(ctx, workdir)
	if err != nil {
		result, _ := s.fail(ctx, md, git.ErrorCategoryUnknown,
			errors.Wrap(ctx, err, "inspect committed files"),
			taskID, tag, authedURL, ref)
		return result
	}
	if !isSubsetIncludingChangelog(files, expectedFiles) {
		result, _ := s.fail(ctx, md, git.ErrorCategoryUnexpectedDiff,
			errors.Errorf(ctx,
				"release commit must change only %v, got %v", expectedFiles, files),
			taskID, tag, authedURL, ref)
		return result
	}
	return nil
}

// fail writes a ## Result(outcome=failed) section with the supplied
// error_category + error string, and returns Status=Failed for controller
// retry. The workdir cleanup defer in Run still runs after this returns.
//
// Before returning, fail invokes the post-check helper so the failure
// verdict can be upgraded to "superseded" — the helper consults git
// ls-remote on the same authed URL the success path used. The post-
// check on the failure path is superseded-only: the local commit/tag
// step never produced a SHA that could match the remote tag, so the
// "released" verdict (which requires sha == expectedSHA) is by
// construction unreachable here. A non-empty remote SHA means a later
// release already won the slot — that's the superseded case.
func (s *executionStep) fail(
	ctx context.Context,
	md *agentlib.Markdown,
	category git.ErrorCategory,
	cause error,
	taskID string,
	tag string,
	authedURL string,
	ref string,
) (*agentlib.Result, error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	output := ResultOutput{
		Outcome:       ResultOutcomeFailed,
		Path:          ResultPathDirectPush,
		ErrorCategory: category,
		Error:         msg,
	}
	section, err := agentlib.MarshalSectionTyped(ctx, "## Result", output)
	if err != nil {
		// Failing to marshal the failure is a real error — surface it so
		// the framework records the panic-equivalent rather than swallowing.
		return nil, errors.Wrapf(ctx, err, "marshal ## Result section (failed)")
	}
	md.ReplaceSection(section)

	glog.V(2).Infof("execution failed: category=%s err=%v", category, cause)
	// Post-check tail: consult the remote for tag state. The failure
	// path never has an expected SHA to compare against — pass "" so
	// the post-check is strictly superseded-only (any non-empty remote
	// SHA means a later release won the slot). An empty result or a
	// subcommand error is a no-op — the existing ## Result(failed)
	// stands unchanged.
	s.postCheck(ctx, md, taskID, tag, authedURL, ref, "")
	// If the post-check upgraded the verdict (status flipped to
	// "completed"), surface that to the controller. Returning Failed
	// after the frontmatter says completed produces a retry-storm: the
	// controller re-fires the agent, the execution step re-runs
	// clone/commit/tag, the idempotency guard short-circuits the
	// post-check, and we return Failed again — every cycle redoes the
	// work without advancing the verdict. The post-check's Resolution
	// block is the deciding fact; ai_review still gets to run for the
	// structural checks on the resulting body.
	if status, _ := md.Frontmatter.String("status"); status == "completed" {
		return &agentlib.Result{
			Status:    agentlib.AgentStatusDone,
			NextPhase: string(domain.TaskPhaseAIReview),
		}, nil
	}
	return &agentlib.Result{
		Status:  agentlib.AgentStatusFailed,
		Message: msg,
	}, nil
}

// postCheck runs the spec-064 post-check tail on the execution step.
// After the execution step has written ## Result (success or failure),
// this method:
//
//  1. Reads md.Frontmatter["status"] — if it's already "completed" or
//     "aborted", return immediately. Idempotency guard: a re-fire on a
//     task whose verdict was already decided must not double-write a
//     ## Resolution block or rewrite the frontmatter again.
//  2. Asks the remote via ops.LsRemote(authedURL, ref, tag) what SHA,
//     if any, sits at refs/tags/<tag> for the planned version. The
//     authed URL is the same one the success path's Clone used — token
//     injection happens at the call site, not here.
//  3. Compares the observed SHA against the agent's expected SHA when
//     available. On the failure path the expected SHA is empty — any
//     non-empty observed SHA still fires the superseded branch (a
//     later release won the slot).
//  4. Upgrades the verdict: writes a ## Resolution block (replacing any
//     existing copy), rewrites md.Frontmatter["status"]="completed" and
//     ["phase"]="done", and emits one structured log line via
//     glog.V(2).
//
// On empty remote result or LsRemote error: the existing ## Result is
// NOT touched. The post-check is a no-op (other than the log line). On
// error the err is wrapped through redactToken before logging so a
// leak of the GitHub auth token in the wrapped stderr cannot reach
// the log stream.
//
// The structured log line format (glog.V(2), one line per call):
//
//	post-check: task_id=<taskID> planned_version=<tag> observed_remote_sha=<sha-or-empty> verdict=<verdict>
//
// where <verdict> ∈ {released, superseded, no-op-remote-empty,
// no-op-remote-error, no-op-already-terminal}. Operators can grep the
// log stream for `post-check:` to find the deciding fact for any
// task.
//
// The post-check NEVER mutates the body sections on the empty-result
// or error paths — only the frontmatter status/phase are touched on
// the released/superseded branches (and only there). The
// released → failed downgrade is impossible: the helper never writes
// status="failed" or "in_progress" — the only transitions are
// (in_progress → completed) and (in_progress → same-status).
//
//nolint:gocognit,funlen // multi-branch verdict upgrade with idempotency guard
func (s *executionStep) postCheck(
	ctx context.Context,
	md *agentlib.Markdown,
	taskID string,
	tag string,
	authedURL string,
	ref string,
	expectedSHA string,
) {
	// 1. Idempotency guard — the FIRST statement. When the task has
	// already been moved to a terminal frontmatter state, do nothing.
	// String() returns ("", false) for absent or non-string values;
	// both are treated as not-yet-terminal.
	if status, _ := md.Frontmatter.String("status"); status == "completed" || status == "aborted" {
		glog.V(2).Infof(
			"post-check: task_id=%s planned_version=%s observed_remote_sha=%s verdict=%s",
			taskID,
			tag,
			"",
			"no-op-already-terminal",
		)
		return
	}

	// 1b. Missing-context guard. The early s.fail sites (validatePlan
	// and extractFrontmatter failures) cannot populate taskID or the
	// authed URL — calling LsRemote would waste a network round-trip on
	// a malformed URL (the empty cloneURL path produces
	// "https://x-access-token:<TOK>@" with no host). Bail before the
	// shell-out; the existing failure verdict stands.
	if taskID == "" || authedURL == "" {
		glog.V(2).Infof(
			"post-check: task_id=%s planned_version=%s observed_remote_sha=%s verdict=%s",
			taskID,
			tag,
			"",
			"no-op-missing-context",
		)
		return
	}

	// 2. Ask the remote. The authed URL is the caller's responsibility;
	// the helper trusts it (mirrors the Clone / Commit call sites in
	// executeLocalRelease). A 30s timeout bounds the network round-trip
	// so a stalled GitHub / DNS hang / TCP backoff cannot block the
	// agent indefinitely.
	lsCtx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()
	sha, err := s.ops.LsRemote(lsCtx, authedURL, ref, tag)
	if err != nil {
		glog.V(2).Infof(
			"post-check: task_id=%s planned_version=%s observed_remote_sha=%s verdict=%s err=%s",
			taskID,
			tag,
			"",
			"no-op-remote-error",
			git.RedactToken(err.Error()),
		)
		return
	}
	if sha == "" {
		glog.V(2).Infof(
			"post-check: task_id=%s planned_version=%s observed_remote_sha=%s verdict=%s",
			taskID,
			tag,
			"",
			"no-op-remote-empty",
		)
		return
	}

	// 3. Compare observed vs expected.
	verdict := ResolutionVerdictSuperseded
	if expectedSHA != "" && sha == expectedSHA {
		verdict = ResolutionVerdictReleased
	}

	// 4. Append a ## Resolution block, rewrite the frontmatter, log.
	section, marshalErr := agentlib.MarshalSectionTyped(
		ctx,
		"## Resolution",
		ResolutionOutput{
			Verdict:           verdict,
			PlannedVersion:    tag,
			ObservedRemoteSHA: sha,
		},
	)
	if marshalErr != nil {
		// Marshalling our own typed struct should not fail; if it does,
		// log and bail — the post-check is best-effort and must not
		// downgrades a verdict on its own failure.
		glog.V(2).Infof(
			"post-check: task_id=%s planned_version=%s observed_remote_sha=%s verdict=%s err=%s",
			taskID,
			tag,
			sha,
			"no-op-marshal-failed",
			git.RedactToken(marshalErr.Error()),
		)
		return
	}
	md.ReplaceSection(section)
	md.Frontmatter["status"] = "completed"
	md.Frontmatter["phase"] = "done"

	glog.V(2).Infof(
		"post-check: task_id=%s planned_version=%s observed_remote_sha=%s verdict=%s",
		taskID,
		tag,
		sha,
		verdict,
	)
}

// deriveUnprefixedVersion strips "## " prefix and "v" prefix from
// a version header to produce the unprefixed semver string.
// "## v0.10.0" → "0.10.0"
// "## 0.10.0" → "0.10.0"
// "0.10.0" → "0.10.0"
func deriveUnprefixedVersion(header string) string {
	header = strings.TrimPrefix(header, "## ")
	header = strings.TrimPrefix(header, "v")
	return header
}

// isSubsetIncludingChangelog reports whether every file in committed is
// present in allowed AND committed contains changelogFileName. It relaxes
// the release-commit file-set gate so a detected manifest that was already
// at the target version (byte-identical → absent from the commit) does not
// fail the release, while any file outside the allowed set still fails
// closed. Shared by the execution pre-push guard and the ai_review file-set
// check — both must enforce the identical rule against the same commit.
func isSubsetIncludingChangelog(committed, allowed []string) bool {
	if !slices.Contains(committed, changelogFileName) {
		return false
	}
	for _, f := range committed {
		if !slices.Contains(allowed, f) {
			return false
		}
	}
	return true
}

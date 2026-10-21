---
status: completed
spec: [004-bug-releaser-publish-blocked-by-branch-protection]
summary: 'Added the branch-protection pull-request fallback: new githubpullrequest client, pr-merge result shape, ai-review routing with PR reuse and ordered tag push, factory wiring, and tests'
execution_id: github-releaser-agent-exec-012-spec-004-pr-fallback
dark-factory-version: v0.198.7
created: "2026-10-21T16:43:00Z"
queued: "2026-10-21T16:57:01Z"
started: "2026-10-21T16:59:25Z"
completed: "2026-10-21T17:02:38Z"
branch: dark-factory/bug-releaser-publish-blocked-by-branch-protection
---

# Publish through a pull request when branch protection rejects the push

<summary>
- A release that GitHub rejects with a branch-protection error no longer dies as a generic push failure: the agent publishes the release commit to a `release/<tag>` branch, opens a pull request titled `release <tag>` with the CHANGELOG section as its body, and pushes the annotated tag.
- The task is parked at `human_review` naming the pull request URL instead of burning another trigger, so the operator gets a one-click merge.
- The task page records the new `path: "pr-merge"` result shape: outcome failed, error category protected-branch-rejected, and a new `pull_request_url` field.
- An already-open pull request for the same head branch is reused — a re-drive never opens a second one.
- The pull request URL is recorded before the tag push, so a crash between the two leaves a recoverable pointer on the task page.
- Every other push failure keeps exactly today's shape and opens zero pull requests; a rejected push is never reconciled to `released` on this path.
- A new narrow GitHub client package covers the three REST calls (default branch, find open PR, create PR) with its own tests and counterfeiter fake.
- This is the second of three prompts for spec 004; it depends on the `GH013` classifier row from prompt 1.
</summary>

<objective>
Add the pull-request fallback to the AI-review push step: when the direct push is rejected with `ErrorCategoryProtectedBranchRejected`, push the release commit to `release/<tag>`, reuse or create one pull request against the default branch, push the tag, write the `path: "pr-merge"` result shape with the PR URL, and park the task at `human_review`. Non-protection push errors keep today's failure behaviour unchanged.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` § Git-Write Safety before changing anything.

Read these files BEFORE changing anything:
- `pkg/steps_ai_review.go` — `Run` (the call to `finishApproved` and its step-sequence comment), `finishApproved` (the single `Push` call and the existing push-error branch), `finishHumanReview` (writes `## Review`, sets the workdir-cleanup sentinel, returns `AgentStatusFailed` / `NextPhase: "human_review"`), `writeFailureResult` (the documented failure shape, `ResultPathDirectPush`), `checkFaithfulness` (the `os.ReadFile(workdir/CHANGELOG.md)` + `changelog.ExtractSectionBody` pattern to mirror for the PR body), and the `changelogFileName` constant in `pkg/steps_execution.go`.
- `pkg/result_output.go` — `ResultOutput`, `ResultOutcomeReleased`, `ResultOutcomeFailed`, `ResultPathDirectPush`, and the two-shape doc comment. This prompt adds one field and one path constant.
- `pkg/git/error_classifier.go` — `ClassifyError` and `ErrorCategoryProtectedBranchRejected` (already mapped from `GH013` by prompt 1).
- `pkg/git/git.go` — the `GitOps` interface. `Push(ctx, workdir string, refs ...string)` shells out `git push --atomic origin <refs...>`; a refspec such as `HEAD:refs/heads/release/v1.0.0` is a valid single argument.
- `pkg/githubreview/client.go` — the HTTP-client shape to mirror: `NewHTTPClient(token)`, package-private `newHTTPClientWithBase(token, apiBase)`, 15-second `http.Client`, `setHeaders` with `Accept: application/vnd.github+json` and `X-GitHub-Api-Version: 2022-11-28`, bearer auth, 200-byte error-body preview, wrapped `github.com/bborbe/errors`.
- `pkg/githubreview/export_test.go`, `pkg/githubreview/suite_test.go`, `pkg/githubreview/client_test.go` — the package test layout to mirror (`*_test` package, `httptest.Server`, `NewHTTPClientForTest`).
- `pkg/factory/factory.go` — `CreateAgent`, where `githubreview.NewHTTPClient(ghToken)` is built and passed to `releaserpkg.NewAIReviewStep`.
- `pkg/steps_ai_review_test.go` — the `BeforeEach` (fake construction + `pkg.NewAIReviewStep(...)` call), the helper block (`taskWithResultFull`, `setupReleasedWorkdir`, `stubFaithfulLLM`, `extractResult`, `extractReview`, `extractReviewWarning`), and the `Context("post-push remote reconciliation (spec 003)")` block.
- `specs/in-progress/004-bug-releaser-publish-blocked-by-branch-protection.md` — § Constraints and § Failure Modes are load-bearing: PR reuse by head branch, tag push ordered after PR create, and reconciliation must not upgrade the pr-merge result to `released`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`
</context>

<requirements>

## 1. New package `pkg/githubpullrequest/client.go`

Create the package with the standard BSD license header (copy the header from `pkg/githubreview/client.go`) and this exact interface. The names and signatures are the contract the rest of this prompt depends on:

```go
//counterfeiter:generate -o ../../mocks/pull_request_client.go --fake-name PullRequestClient . Client

// Client is the seam for the three GitHub REST API calls the protected-branch
// fallback needs. It is the only non-git write path in this agent.
type Client interface {
	// DefaultBranch returns the repository's default branch name.
	DefaultBranch(ctx context.Context, owner, repo string) (branch string, _ error)

	// FindOpenPullRequest returns the html_url of the open pull request whose
	// head branch is headBranch, and found=false when none exists.
	FindOpenPullRequest(ctx context.Context, owner, repo, headBranch string) (
		pullRequestURL string, found bool, _ error,
	)

	// CreatePullRequest opens one pull request and returns its html_url.
	CreatePullRequest(
		ctx context.Context,
		owner, repo, title, headBranch, baseBranch, body string,
	) (pullRequestURL string, _ error)
}
```

Constructors and implementation:
- `func NewHTTPClient(token string) Client` — returns the production client against `https://api.github.com`.
- `func newHTTPClientWithBase(token, apiBase string) Client` — package-private test seam, mirrors `githubreview.newHTTPClientWithBase`.
- Struct `httpClient` with `client *http.Client`, `token`, `apiBase`; `http.Client{Timeout: 15 * time.Second}`.
- A private `setHeaders(req *http.Request)` identical in behaviour to the one in `pkg/githubreview/client.go` (Accept, API version, `Authorization: Bearer <token>` when non-empty).
- A private `errBodyPreviewMax = 200` constant and a helper that truncates a non-2xx response body to it.

Endpoint contracts:
1. `DefaultBranch`: `GET {apiBase}/repos/{owner}/{repo}` with `url.PathEscape` on owner and repo. Decode `{"default_branch":"..."}`. Non-2xx → wrapped error `"DefaultBranch: status %d: %s"`. Empty `default_branch` → wrapped error.
2. `FindOpenPullRequest`: `GET {apiBase}/repos/{owner}/{repo}/pulls` with query values built via `url.Values`: `state=open`, `head={owner}:{headBranch}` (GitHub's head filter form; the `url.Values` encoding handles the `:` and `/`). Decode a JSON array of `{"html_url":"..."}`. Return the first entry's `html_url` with `found=true`; an empty array returns `("", false, nil)` — absence is NOT an error. Non-2xx / decode error → wrapped error.
3. `CreatePullRequest`: `POST {apiBase}/repos/{owner}/{repo}/pulls` with `Content-Type: application/json` and body `{"title":..., "head":..., "base":..., "body":...}`. Decode `{"html_url":"..."}`. Non-2xx → wrapped error naming the status and the body preview (a 422 is a real error; the caller falls back to the failure shape). An empty `html_url` in a 2xx response → wrapped error.

Validation: every method returns a wrapped `errors.Errorf(ctx, ...)` when `owner`, `repo`, or the method-specific required strings are empty.

Logging: one `glog.V(2).Infof` per successful call naming the endpoint/status, mirroring `githubreview`.

## 2. New package tests

Create:
- `pkg/githubpullrequest/suite_test.go` — copy the Ginkgo suite from `pkg/githubreview/suite_test.go` (`package githubpullrequest_test`, the `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` line, `RunSpecs(t, "GithubPullRequest Suite", ...)`).
- `pkg/githubpullrequest/export_test.go` — `var NewHTTPClientForTest = newHTTPClientWithBase`.
- `pkg/githubpullrequest/client_test.go` — `httptest.Server` specs, mirroring `pkg/githubreview/client_test.go`:
  - `DefaultBranch` returns the decoded value on 200; returns an error on 500.
  - `FindOpenPullRequest` returns the `html_url` and `found=true` for a one-element array; the handler MUST assert the request query carries `state=open` and `head=bborbe:release/v1.0.0`; returns `("", false, nil)` for `[]`; returns an error on 500.
  - `CreatePullRequest` decodes the posted JSON and asserts `title`, `head`, `base`, and `body` are the arguments passed; returns the `html_url` on 201; returns an error on 422.
  - Empty `owner` / `repo` arguments return an error without any HTTP call.

Run `make generate` after adding the counterfeiter directive so `mocks/pull_request_client.go` exists (do NOT hand-write the fake; `make generate` regenerates the whole `mocks/` directory).

## 3. Extend the `## Result` contract in `pkg/result_output.go`

1. Add the field to `ResultOutput` (JSON tag exactly `pull_request_url`, `omitempty`):

```go
	// PullRequestURL is the html_url of the pull request opened when a
	// branch-protection rejection forced the release through the PR path.
	// Empty on every other path.
	PullRequestURL string `json:"pull_request_url,omitempty"`
```

2. Add the path constant next to `ResultPathDirectPush`:

```go
// ResultPathPRMerge marks the protected-branch fallback: the release commit
// was pushed to release/<tag> and a pull request was opened for a human to
// merge. The outcome is still "failed" — the default branch does not carry
// the commit.
const ResultPathPRMerge = "pr-merge"
```

3. Update the `ResultOutput` doc comment from "Two shapes are valid" to three shapes: the existing `released` and `failed`/`direct-push` lines stay true; add a third line for `Outcome="failed"`, `Path="pr-merge"` that names `PullRequestURL`, states `ErrorCategory` is `protected_branch_rejected`, and states `CommitSHA` + `Tag` stay empty because the default branch does not carry the commit. Keep the "Future fields require a spec amendment" line.

## 4. AI-review step: protected-branch branch

### 4a. Constructor and struct

In `pkg/steps_ai_review.go`:

```go
func NewAIReviewStep(
	client githubreview.Client,
	prClient githubpullrequest.Client,
	runner claudelib.ClaudeRunner,
	ops git.GitOps,
	ghToken string,
) agentlib.Step {
	return &aiReviewStep{
		client:   client,
		prClient: prClient,
		runner:   runner,
		ops:      ops,
		ghToken:  ghToken,
	}
}
```

Add `prClient githubpullrequest.Client` to the `aiReviewStep` struct and the import `"github.com/bborbe/github-releaser-agent/pkg/githubpullrequest"`. Update the constructor doc comment to mention the PR client.

### 4b. Route the protected-branch push error

`finishApproved` gains the `plan *PlanOutput` parameter (the PR body needs `plan.NextVersionHeader`). New signature:

```go
func (s *aiReviewStep) finishApproved(
	ctx context.Context,
	md *agentlib.Markdown,
	result *ResultOutput,
	plan *PlanOutput,
	output ReviewOutput,
	workdirShouldCleanup *bool,
) (*agentlib.Result, error)
```

Update its one call site in `Run` to pass `plan`.

Inside `finishApproved`, after `pushErr := s.ops.Push(...)`, branch BEFORE the existing generic failure handling:

```go
if pushErr != nil {
	if git.ClassifyError(pushErr) == git.ErrorCategoryProtectedBranchRejected {
		return s.finishProtectedBranch(ctx, md, result, plan, output, pushErr, workdirShouldCleanup)
	}
	glog.Warningf("ai_review: push failed: %v", pushErr)
	// ... existing notes / Approved=false / FailedChecks handling unchanged ...
}
```

The existing generic path (`consultRemoteTag` + `reconcilePostPush`) stays byte-identical for every other error. The protected-branch path MUST return without calling `consultRemoteTag` or `reconcilePostPush` — a tag that lands on the remote must never upgrade this result to `released`, because the default branch does not carry the commit.

### 4c. Add `writeResult` and route `writeFailureResult` through it

Add one helper that marshals any `ResultOutput` into `## Result`:

```go
func (s *aiReviewStep) writeResult(
	ctx context.Context,
	md *agentlib.Markdown,
	output ResultOutput,
) error {
	section, err := agentlib.MarshalSectionTyped(ctx, "## Result", output)
	if err != nil {
		return errors.Wrapf(ctx, err, "ai_review: marshal ## Result section")
	}
	md.ReplaceSection(section)
	return nil
}
```

Change `writeFailureResult` to build its `ResultOutput` and call `s.writeResult(ctx, md, output)`; its observable behaviour (path `direct-push`, same wrapped error text) must not change.

### 4d. Add `finishProtectedBranch`

```go
func (s *aiReviewStep) finishProtectedBranch(
	ctx context.Context,
	md *agentlib.Markdown,
	result *ResultOutput,
	plan *PlanOutput,
	output ReviewOutput,
	pushErr error,
	workdirShouldCleanup *bool,
) (*agentlib.Result, error)
```

Implement this exact sequence. Every failure before the PR exists falls back to today's failure shape via a small helper (4e); nothing on those paths pushes the tag.

1. Read `repo` from frontmatter and split it with the existing `parseOwnerRepo`. On failure, return the failure shape with category `git.ErrorCategoryProtectedBranchRejected` and a message naming the unparseable repo.
2. `branch := "release/" + result.LocalTag`. When `result.LocalTag` is empty, return the failure shape naming the missing local tag.
3. Push the release commit to the release branch:
   `s.ops.Push(ctx, result.Workdir, "HEAD:refs/heads/"+branch)`.
   On error return the failure shape with `git.ClassifyError(branchErr)` and message `"release branch push failed: " + branchErr.Error()`. No PR is created and no tag is pushed.
4. Resolve the base branch: `base, err := s.prClient.DefaultBranch(ctx, owner, name)`. On error return the failure shape with `git.ErrorCategoryUnknown` and a message naming the default-branch lookup. No PR, no tag push.
5. Find an existing open PR: `prURL, found, err := s.prClient.FindOpenPullRequest(ctx, owner, name, branch)`. On error return the failure shape naming the lookup — do NOT create a duplicate PR. No tag push.
6. When `!found`: build the body (4f), then
   `prURL, err = s.prClient.CreatePullRequest(ctx, owner, name, "release "+result.LocalTag, branch, base, body)`.
   On body-build or create error return the failure shape naming the error. No tag push. When `found`, reuse `prURL` and make zero create calls.
7. Write the PR result BEFORE the tag push:

```go
prResult := ResultOutput{
	Outcome:        ResultOutcomeFailed,
	Path:           ResultPathPRMerge,
	ErrorCategory:  git.ErrorCategoryProtectedBranchRejected,
	Error:          "push rejected by branch protection: " + pushErr.Error(),
	PullRequestURL: prURL,
}
```

   Call `s.writeResult(ctx, md, prResult)`; on error return `nil, err`.
8. Push the annotated tag: `s.ops.Push(ctx, result.Workdir, "refs/tags/"+result.LocalTag)`. On error:
   - log `glog.Warningf("ai_review: tag push after PR create failed: %v", tagErr)`;
   - append `"; tag push failed: " + tagErr.Error()` to `prResult.Error` and call `s.writeResult` again so `## Result` names the tag failure;
   - keep `Path: "pr-merge"` and the `PullRequestURL`;
   - continue to step 9 (do NOT report `released`).
9. Set `output.Approved = false`, append `CheckPush` to `output.FailedChecks`, set
   `output.Notes = "push rejected by branch protection; pull request: " + prURL`
   and return `s.finishHumanReview(ctx, md, output, workdirShouldCleanup)`.

Keep the function under the lint budgets (`funlen` 80 lines / 50 statements, `gocognit` 20, `nestif` 4). The small failure-shape helper in 4e is what keeps it there.

### 4e. Failure-shape helper

```go
func (s *aiReviewStep) failProtectedBranch(
	ctx context.Context,
	md *agentlib.Markdown,
	output ReviewOutput,
	category git.ErrorCategory,
	message string,
	workdirShouldCleanup *bool,
) (*agentlib.Result, error) {
	if err := s.writeFailureResult(ctx, md, category, message); err != nil {
		return nil, err
	}
	output.Approved = false
	output.Notes = message
	output.FailedChecks = append(output.FailedChecks, CheckPush)
	return s.finishHumanReview(ctx, md, output, workdirShouldCleanup)
}
```

This preserves the documented failure shape (`path: "direct-push"`, `ErrorCategory` + `Error`, no PR URL) for every PR-path failure.

### 4f. Pull-request body helper

```go
func (s *aiReviewStep) pullRequestBody(
	ctx context.Context,
	result *ResultOutput,
	plan *PlanOutput,
) (string, error)
```

Read `filepath.Join(result.Workdir, changelogFileName)` with `os.ReadFile` (carry the same `// #nosec G304` rationale comment used by `checkFaithfulness`), then
`changelog.ExtractSectionBody(ctx, content, strings.TrimPrefix(plan.NextVersionHeader, "## "))`
and return `strings.TrimSpace(body)`. Wrap both errors with `errors.Wrapf`. An extraction failure is fail-closed: the caller returns the failure shape and creates no PR.

## 5. Wire the client in `pkg/factory/factory.go`

Import `"github.com/bborbe/github-releaser-agent/pkg/githubpullrequest"` and change the AI-review construction in `CreateAgent` to:

```go
prClient := githubpullrequest.NewHTTPClient(ghToken)
reviewStep := releaserpkg.NewAIReviewStep(
	reviewClient,
	prClient,
	aiReviewRunner,
	executionOps,
	ghToken,
)
```

No other factory function changes; `CreateAgent`'s signature is unchanged.

## 6. Tests in `pkg/steps_ai_review_test.go`

### 6a. Wire the fake

- Add `fakePRClient *mocks.PullRequestClient` to the `Describe("AIReviewStep")` var block and initialize it in `BeforeEach`.
- Change the `BeforeEach` construction to
  `step = pkg.NewAIReviewStep(fakeClient, fakePRClient, fakeRunner, fakeOps, token)`.
- The counterfeiter fake exposes `DefaultBranchReturns(string, error)`, `FindOpenPullRequestReturns(string, bool, error)`, `CreatePullRequestReturns(string, error)`, the matching `...CallCount()` methods, and `CreatePullRequestArgsForCall(0) (context.Context, string, string, string, string, string, string)`.

### 6b. New Context `"branch-protection PR fallback (spec 004)"`

Add it after `Context("post-push remote reconciliation (spec 003)")` and inside `Describe("Run")`, so it inherits that block's `BeforeEach` (`tmpDir` already prepared by `setupReleasedWorkdir(tmpDir)`, `CommittedFilesReturns([]string{"CHANGELOG.md"}, nil)`, and `stubFaithfulLLM("- feat: add foo\n")`). Every fixture is built with `taskWithResultFull("abc1234", "v1.0.0", "released", tmpDir)` so the review is approved and the push is reached; do NOT call `setupReleasedWorkdir` a second time. Use `fakeOps.PushReturnsOnCall(0, ...)` for the rejected direct push and `(1, nil)` / `(2, nil)` for the branch and tag pushes.

1. **Protected rejection → PR path.** Direct push returns the spec's `GH013` text. Assert:
   - `fakeOps.PushCallCount()` == 3; `PushArgsForCall(0)` refs == `[]string{"HEAD", "refs/tags/v1.0.0"}`; `PushArgsForCall(1)` refs == `[]string{"HEAD:refs/heads/release/v1.0.0"}`; `PushArgsForCall(2)` refs == `[]string{"refs/tags/v1.0.0"}`.
   - `fakePRClient.FindOpenPullRequestCallCount()` == 1 and `CreatePullRequestCallCount()` == 1.
   - The create arguments are owner `bborbe`, repo `example`, title `release v1.0.0`, head `release/v1.0.0`, base `master`, and a body containing `- feat: add foo` (the `## v1.0.0` body written by `setupReleasedWorkdir`).
   - `fakeOps.LsRemoteCallCount()` == 0 — the protected path never reconciles.
   - `result.Status` == `agentlib.AgentStatusFailed`, `result.NextPhase` == `"human_review"`.
   - Extracted `## Result`: `Outcome` == `pkg.ResultOutcomeFailed`, `Path` == `pkg.ResultPathPRMerge`, `ErrorCategory` == `git.ErrorCategoryProtectedBranchRejected`, `PullRequestURL` == the stubbed URL, `Error` contains `"GH013"`.
   - `extractReview(md).FailedChecks` contains `pkg.CheckPush`; `extractReviewWarning(md)` is nil.
2. **Existing PR is reused.** `FindOpenPullRequestReturns("https://github.com/bborbe/example/pull/9", true, nil)`. Assert `CreatePullRequestCallCount()` == 0, `## Result`.`PullRequestURL` == `.../pull/9`, `PushCallCount()` == 3 (tag still pushed).
3. **PR create fails.** `CreatePullRequestReturns("", errors.New("422 Validation Failed"))`. Assert `PushCallCount()` == 2 (no tag push), `## Result`.`Path` == `pkg.ResultPathDirectPush`, `PullRequestURL` empty, `Error` contains `"422"`, `NextPhase` == `"human_review"`.
4. **Tag push fails after PR create.** `PushReturnsOnCall(2, errors.New("remote: error: tag rejected"))`. Assert `PushCallCount()` == 3, `## Result`.`Path` == `pkg.ResultPathPRMerge`, `PullRequestURL` still set, `Error` contains `"tag push failed"`, `NextPhase` == `"human_review"`.
5. **Default-branch lookup fails.** `DefaultBranchReturns("", errors.New("403 Forbidden"))`. Assert `PushCallCount()` == 2, `CreatePullRequestCallCount()` == 0, `## Result`.`Path` == `pkg.ResultPathDirectPush`, `Error` contains `"default branch"`.
6. **Non-protection push errors open no PR.** Use a `DescribeTable` over `errors.New("! [rejected] master -> master (non-fast-forward)")` (expect `git.ErrorCategoryPushNonFastForward`) and `errors.New("fatal: cosmic ray flipped a bit")` (expect `git.ErrorCategoryUnknown`). For each: `fakeOps.PushCallCount()` == 1, `FindOpenPullRequestCallCount()` == 0, `CreatePullRequestCallCount()` == 0, `## Result`.`Path` == `pkg.ResultPathDirectPush`, `PullRequestURL` empty, `ErrorCategory` equals the expected category, `NextPhase` == `"human_review"`.

Do not modify any existing spec in the file beyond the `BeforeEach` constructor call and the two spec-003 fixtures below. The spec-003 reconciliation Context must stay green: its two `GH006: Protected branch update failed` fixtures now classify as `protected_branch_rejected` and would be routed into the new PR fallback, so their `PushCallCount` / `LsRemoteCallCount` assertions would fail. Replace both push errors with a non-protection error and keep their reconciliation assertions:
- `PushRejectedRemoteAbsent`: `fakeOps.PushReturns(errors.New("fatal: unable to access remote: connection reset by peer"))`; change `ContainSubstring("GH006")` to `ContainSubstring("connection reset")`; change the expected category from `git.ErrorCategoryProtectedBranchRejected` to `git.ErrorCategoryUnknown`.
- `PushRejectedButRemoteConfirms`: the same push-error replacement; change `ContainSubstring("GH006")` to `ContainSubstring("connection reset")`. Its `released`-kept assertion stays valid.

## 7. Self-check

Re-run `<verification>`. Then confirm by reading the files: the protected branch returns before any `consultRemoteTag` call; the PR URL is written to `## Result` before the tag push; a reused PR produces zero create calls; and every non-protection error still follows the pre-existing path.
</requirements>

<constraints>
- **Protected-branch category only.** The PR path fires only when `git.ClassifyError(pushErr) == git.ErrorCategoryProtectedBranchRejected`. Every other error keeps today's `Approved=false` / `FailedChecks=[Push]` / `human_review` shape.
- **No new write path beyond** `git push HEAD:refs/heads/release/<tag>`, `git push refs/tags/<tag>`, and exactly one pull-request create. Never force-push, never merge, never delete a branch or tag.
- **Order is load-bearing.** Branch push → find/create PR → write `## Result` with the PR URL → tag push. Record the PR URL before the irreversible tag push so a crash leaves a recoverable pointer.
- **PR reuse by head branch.** When an open PR with head `release/<tag>` exists, reuse its URL and make zero create calls.
- **No reconciliation on the PR path.** Do not call `consultRemoteTag` / `reconcilePostPush`; a tag present on the remote must not upgrade this result to `released`.
- **Result shape on the PR path** is `outcome: failed`, `path: "pr-merge"`, `error_category: protected_branch_rejected`, `pull_request_url` set, `CommitSHA` / `Tag` empty. A PR-path failure before the PR exists uses today's `path: "direct-push"` failure shape.
- **Tag push failure does not report success** and does not discard the PR URL; `## Result` names the tag error.
- **Escalation contract preserved.** The step returns `agentlib.AgentStatusFailed` with `NextPhase: "human_review"`. `previous_assignee: github-releaser-agent` is stamped by the agent-lib deliverer's `ClearAssigneeIfHumanReview` chokepoint — do NOT write `assignee` or `previous_assignee` in this step.
- **Git-write safety untouched.** The release commit still touches `CHANGELOG.md` only via explicit pathspec; the LLM still only classifies the bump. Do not edit `pkg/steps_execution.go` or `pkg/git/*` in this prompt.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)`; no `fmt.Print*`.
- **Existing tests keep passing** (Ginkgo v2 / Gomega, counterfeiter fakes, no live network in tests).
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make generate` so `mocks/pull_request_client.go` is generated from the new directive.
Run `ROOTDIR=/workspace make test` — exit 0.

```bash
grep -rn 'ResultPathPRMerge' pkg/result_output.go pkg/steps_ai_review.go   # >= 2
grep -n 'pull_request_url' pkg/result_output.go                            # 1
grep -n 'finishProtectedBranch' pkg/steps_ai_review.go                     # declaration + call site
grep -n 'HEAD:refs/heads/release/' pkg/steps_ai_review.go                  # >= 1
grep -rn 'NewAIReviewStep' pkg/factory/factory.go pkg/steps_ai_review_test.go  # both updated call sites
go test -mod=mod ./pkg/githubpullrequest/... ./pkg/...                     # PASS
```

Then run `ROOTDIR=/workspace make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).
</verification>

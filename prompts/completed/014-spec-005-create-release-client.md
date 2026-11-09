---
status: completed
spec: [005-create-github-release-object]
summary: Added CreateRelease to pkg/githubreview.Client with ErrReleaseUnreachable/ErrReleaseRejected sentinels, 422 already_exists idempotency, Content-Type JSON, tests, and a regenerated Counterfeiter mock; CHANGELOG intentionally deferred to prompt 3.
execution_id: be68957f-2d9c-5d8a-966a-0c54fe41d8bb-exec-014-spec-005-create-release-client
dark-factory-version: dev
created: "2026-11-09T17:38:03Z"
queued: "2026-11-09T17:49:51Z"
started: "2026-11-09T17:49:53Z"
completed: "2026-11-09T18:07:09Z"
branch: dark-factory/create-github-release-object
---

# Add `CreateRelease` to the GitHub review client

<summary>
- The GitHub REST client gains a fourth call: create the Release object for a tag.
- The call sends the tag as both the release tag and the release title, plus a body.
- Creating a release that already exists is reported as "present, not created" — not an error.
- A transport failure and an HTTP rejection are distinguishable by the caller, so the review step can word its notes correctly.
- Every other non-2xx response is a wrapped error, never a silent success.
- The call reuses the existing headers, timeout and error-body preview rule — no second HTTP client.
- The Counterfeiter mock is regenerated so the step tests can drive the new method.
- This is the first of three prompts; it adds the HTTP seam only. The step that calls it is the next prompt.
</summary>

<objective>
Extend `pkg/githubreview.Client` with `CreateRelease` so the AI-review step can create the GitHub Release object for a confirmed tag. The method must be idempotent at the HTTP layer (a 422 naming `already_exists` means the release is present), must distinguish a transport failure from an HTTP rejection through exported sentinel errors, and must reuse the existing `setHeaders`, timeout and `errBodyPreviewMax` preview rule.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done.

Read these files BEFORE changing anything:
- `pkg/githubreview/client.go` — the `Client` interface (currently three methods), the `//counterfeiter:generate` directive, `newHTTPClientWithBase`, the private `httpClient`, `setHeaders`, `errBodyPreviewMax`, and the existing per-method shape (`TagExists`, `ResolveTagCommit`, `FetchChangelog`). The new method follows the same structure: validate inputs, build the endpoint with `url.PathEscape`, `http.NewRequestWithContext`, `c.setHeaders(req)`, `c.client.Do(req)`, read the body, cap the preview at `errBodyPreviewMax`, `glog.V(2)` on success.
- `pkg/githubreview/client_test.go` — the external `githubreview_test` package, `NewHTTPClientForTest`, and the `httptest.NewServer` patterns used for status/header/transport/error-sanitization specs.
- `pkg/githubreview/export_test.go` — `NewHTTPClientForTest = newHTTPClientWithBase`.
- `pkg/githubreview/suite_test.go` — the `//go:generate ... counterfeiter` directive.
- `mocks/review_client.go` — the generated fake. Do not hand-edit it; `make generate` regenerates it from the interface.
- `pkg/steps_ai_review.go` — `applyImageCheck` (the shape of a post-push check) and `finishApproved`, so the return contract here fits the caller that the next prompt writes.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
</context>

<requirements>

## 1. Add two exported sentinel errors

In `pkg/githubreview/client.go`, beside the existing `ErrTagNotFound`:

```go
// ErrReleaseUnreachable marks a CreateRelease error caused by a request that
// could not be completed (DNS, TLS, proxy, timeout). Callers use
// errors.Is(err, ErrReleaseUnreachable) to word the failure as "could not
// reach GitHub" rather than "GitHub rejected the release".
var ErrReleaseUnreachable = stderrors.New("githubreview: release creation unreachable")

// ErrReleaseRejected marks a CreateRelease error caused by GitHub answering
// with a non-2xx status that is not the already_exists 422.
var ErrReleaseRejected = stderrors.New("githubreview: release creation rejected")
```

`stderrors` is already imported as `stderrors "errors"`. These are failure markers only — neither is ever returned together with a `nil` error, and neither reads as success.

## 2. Extend the `Client` interface

Add this method to `Client` (keep the existing three methods unchanged):

```go
// CreateRelease calls POST /repos/{owner}/{repo}/releases.
//
// Returns:
//   - (true, nil)  — GitHub created the release (any 2xx).
//   - (false, nil) — GitHub answered 422 and the body names already_exists;
//     the release is present, it was simply not created by this call.
//   - (false, err) — transport failure (wrapping ErrReleaseUnreachable) or
//     any other non-2xx (wrapping ErrReleaseRejected).
CreateRelease(ctx context.Context, owner, repo, tag, title, body string) (created bool, _ error)
```

Update the interface doc comment ("three GitHub REST API calls") to say four and to name the new call.

## 3. Implement `httpClient.CreateRelease`

Add a private request struct beside the other response structs:

```go
type createReleaseRequest struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
}
```

Implement:

```go
func (c *httpClient) CreateRelease(
	ctx context.Context,
	owner, repo, tag, title, body string,
) (bool, error)
```

Behavior, in order:
1. Reject empty `owner`, `repo`, or `tag` with `errors.Errorf(ctx, "CreateRelease: owner/repo/tag must be non-empty")`. `title` and `body` may be empty.
2. Build the endpoint as `fmt.Sprintf("%s/repos/%s/%s/releases", c.apiBase, url.PathEscape(owner), url.PathEscape(repo))`.
3. `json.Marshal(createReleaseRequest{TagName: tag, Name: title, Body: body})`; on error return `errors.Wrapf(ctx, err, "CreateRelease: marshal request")`.
4. `http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))`; on error return `errors.Wrapf(ctx, err, "CreateRelease: build request")`.
5. Call `c.setHeaders(req)` (so `Accept`, `X-GitHub-Api-Version` and the bearer token are applied exactly as the other methods) and additionally `req.Header.Set("Content-Type", "application/json")`. `setHeaders` does not set `Content-Type`; the POST body needs it.
6. `c.client.Do(req)`; on error return `(false, errors.Wrapf(ctx, ErrReleaseUnreachable, "CreateRelease: http: %v", err))`. The transport text is preserved in the message and `errors.Is(err, ErrReleaseUnreachable)` matches the sentinel.
7. Read the body with `io.ReadAll`; on error return `(false, errors.Wrapf(ctx, err, "CreateRelease: read body"))`.
8. Status handling:
   - Any 2xx → `glog.V(2).Infof("CreateRelease: POST %s status=%d created=true bytes=%d", endpoint, resp.StatusCode, len(body))` and return `(true, nil)`.
   - `http.StatusUnprocessableEntity` (422) **and** `strings.Contains(string(body), "already_exists")` → return `(false, nil)`. This is the only non-error "did not create" outcome.
   - Every other non-2xx → build `preview := string(body)` capped at `errBodyPreviewMax` (same slice rule as the other methods) and return `(false, errors.Wrapf(ctx, ErrReleaseRejected, "CreateRelease: status %d: %s", resp.StatusCode, preview))`.
9. The bearer token is applied only by `setHeaders` and never enters the URL, the request body, or an error string.

Add `bytes` to the import block. Do not add a second `http.Client`; reuse `c.client` and its existing timeout.

## 4. Regenerate the mock

Run `make generate` (it runs `rm -rf mocks` then `go generate ./...`). The regenerated `mocks/review_client.go` must carry `CreateReleaseStub`, `CreateReleaseCalls`, `CreateReleaseCallCount`, `CreateReleaseReturns`, `CreateReleaseReturnsOnCall`, and `CreateReleaseArgsForCall`. Do not hand-edit the generated file.

## 5. Client-level tests in `pkg/githubreview/client_test.go`

Add a `Describe("CreateRelease", ...)` block using `httptest.NewServer` and `NewHTTPClientForTest`, with no live network. Required specs (put each name verbatim in its `It(...)` description):

- **`CreateReleaseTreatsAlreadyExistsAsSuccess`** — server answers 422 with a body containing `"code":"already_exists"` (use the real GitHub shape, e.g. `{"message":"Validation Failed","errors":[{"resource":"Release","code":"already_exists","field":"tag_name"}]}`). Assert `created` is `false` and `err` is `nil`.
- **`CreateReleaseReturnsWrappedErrorOnOtherNon2xx`** — server answers 500. Assert `err` is non-nil and `errors.Is(err, githubreview.ErrReleaseRejected)` is true.
- **`CreateReleaseCreatesOn201`** — server captures the method, path, headers and body, and answers 201. Assert `created` is `true`, `err` is `nil`, the method is `POST`, the path is `/repos/bborbe/example/releases`, the decoded JSON body carries `tag_name` = tag, `name` = title and `body` = body, and `Authorization` is `Bearer <token>`, `Accept` is `application/vnd.github+json`, `X-GitHub-Api-Version` is `2022-11-28`, and `Content-Type` is `application/json`.
- **`CreateReleaseUnreachableWrapsSentinel`** — start an `httptest.NewServer`, close it immediately, then call. Assert `err` is non-nil and `errors.Is(err, githubreview.ErrReleaseUnreachable)` is true. (Mirror the existing `TagExists transport error` spec.)
- **`CreateReleaseRejectsEmptyOwnerRepoTag`** — assert the error message contains `CreateRelease` and `owner/repo/tag must be non-empty` for an empty owner (one spec is enough; the validation is one branch).
- **`CreateReleaseRejectionDoesNotLeakToken`** — use a token-shaped value (e.g. `ghp_...`), have the server answer 500, and assert the error string contains neither the token nor `ghp_`. (Mirror the existing `Error message sanitization` specs.)

Assert on the request body by decoding it (`json.NewDecoder(r.Body).Decode(&got)`), not by string matching, so field names are checked.

</requirements>

<constraints>
- **Reuse the existing HTTP client seam.** `CreateRelease` joins `githubreview.Client`; it reuses `setHeaders`, `c.client` (15s timeout), and the `errBodyPreviewMax` preview rule. A second HTTP client is out.
- **The only non-error "did not create" outcome is `already_exists`.** It returns `(false, nil)` — present, not created. Every other outcome is `(false, err)` or `(true, nil)`.
- **Do NOT add a retry, a confirmation GET, or a new timeout.** One POST per call.
- **Do NOT change `TagExists`, `ResolveTagCommit`, or `FetchChangelog`.**
- **Do NOT change the `## Result` contract** — no new result field, outcome value, or error-category value. The sentinel errors live in `pkg/githubreview`, not in the result contract.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` with `V(n)`-gated `Info`.
- **The mock is regenerated, not hand-edited.** Run `make generate`.
- **Existing tests must still pass.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network.
- **`CHANGELOG.md` is prompt 3's job.** Do not add a `## Unreleased` entry here; report the changelog criterion as deferred to prompt 3 if your self-review checks it.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).
Run `make test` — exit 0.

```bash
grep -c 'CreateRelease(' pkg/githubreview/client.go                                             # >= 2 (interface method + implementation)
grep -c 'ErrReleaseUnreachable' pkg/githubreview/client.go                                       # >= 1
grep -c 'ErrReleaseRejected' pkg/githubreview/client.go                                          # >= 1
grep -c 'CreateReleaseTreatsAlreadyExistsAsSuccess' pkg/githubreview/client_test.go              # >= 1
grep -c 'CreateReleaseReturnsWrappedErrorOnOtherNon2xx' pkg/githubreview/client_test.go          # >= 1
grep -c 'CreateReleaseArgsForCall' mocks/review_client.go                                        # >= 1
grep -c 'Content-Type' pkg/githubreview/client.go                                                # >= 1
```
</verification>

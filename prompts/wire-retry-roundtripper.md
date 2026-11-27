---
status: draft
---

<summary>
- Every GitHub HTTP client this agent builds now retries a rate-limit response instead of returning the error
- A 429 or a rate-limiting 403 from api.github.com is retried with the wait the response asks for, up to five times
- Each retry emits one log line naming the attempt, the status, the class and the wait
- The rate-limit class label is the corrected one, so a 429 carrying positive remaining quota reports secondary rather than unclassified
- The test seams that point a client at a stub server keep working, and gain retry coverage
- Nothing else changes: request building, pagination, error wording and sentinel errors are untouched
</summary>

<objective>
Route all four of this agent's GitHub HTTP clients through the shared rate-limit-aware retry transport, so a throttled response delays the agent instead of ending its work item.
</objective>

<context>
Read CLAUDE.md for project conventions.

Four packages build their own GitHub client against `api.github.com`, each a bare `&http.Client{...}` with `apiBase: "https://api.github.com"`:
- `pkg/githubtags/tags.go` — `NewHTTPTagsFetcher` (~line 95, client literal ~97) and the test seam `newHTTPTagsFetcherWithBase` (~105, literal ~107)
- `pkg/githubchangelog/fetcher.go` — `NewHTTPFetcher` (~44, literal ~46) and the test seam `newHTTPFetcherWithBase` (~54, literal ~56)
- `pkg/githubreview/client.go` — `NewHTTPClient` (~80) delegates to `newHTTPClientWithBase` (~89), whose client literal is ~91
- `pkg/maintainerconfig/fetcher.go` — `NewHTTPFetcher` (~85, client literal ~87) and the test seam `newHTTPFetcherWithBase` (~95, literal ~97). This package uses `Timeout: fetchTimeout` (a constant) rather than a literal duration.

`pkg/githubauth/githubauth.go` builds no client here — it delegates to `lib/githubapp.MintIAT` — and `pkg/imageregistry/client.go` builds one for a Docker/OCI registry rather than for `api.github.com`. Both are out of scope.

`github.com/bborbe/http` is pinned at v1.28.0 as an indirect dependency. Its `NewRoundTripperRetryWithBackoff(roundTripper, RetryBackoffOptions)` returns a `http.RoundTripper` that retries a 403/429 from the hosts in `RetryHosts`, waiting the `Retry-After` duration (and, for primary exhaustion, the `X-RateLimit-Reset` delta), up to `MaxAttempts` (default 5) retries, emitting `retry attempt=<n> status=<status> class=<class> wait=<duration>` per attempt.

This repository has no `vendor/` directory, so a dependency bump needs `go mod tidy` and nothing else.
</context>

<requirements>
1. In each of the four packages, add one unexported helper next to its constructor:

   ```go
   // retryTransportFor wraps the default transport with the shared rate-limit
   // retry round tripper. api.github.com is always retried; the host of apiBase
   // is added too when it differs, so a test seam pointing at a stub server is
   // covered without duplicating the production host.
   func retryTransportFor(apiBase string) http.RoundTripper {
       hosts := []string{"api.github.com"}
       if u, err := url.Parse(apiBase); err == nil && u.Hostname() != "" && u.Hostname() != "api.github.com" {
           hosts = append(hosts, u.Hostname())
       }
       return libhttp.NewRoundTripperRetryWithBackoff(
           http.DefaultTransport,
           libhttp.RetryBackoffOptions{RetryHosts: hosts},
       )
   }
   ```

   Import it as `libhttp "github.com/bborbe/http"`. Name the helper `retryTransportFor` in every package so the four are recognisably the same construct. `net/url` and `net/http` are already imported in these files; add only what is missing.

2. At each of the **seven** client literals listed in `<context>`, add `Transport: retryTransportFor(<the apiBase value for that constructor>)` to the `http.Client` literal. Qualify by package: in `githubtags.NewHTTPTagsFetcher`, `githubchangelog.NewHTTPFetcher` and `maintainerconfig.NewHTTPFetcher` the value is the literal `"https://api.github.com"`; in the four `*WithBase` seams — including `githubreview.newHTTPClientWithBase` — it is the `apiBase` parameter. `pkg/githubreview/client.go` has a single literal, so wiring it covers both `NewHTTPClient` and its seam — do not add a second literal there.

3. Do not change request building, pagination, header sets, error wording, sentinel errors (`ErrTagNotFound`, `ErrFileNotFound`), or any exported signature. The `*WithBase` seams must keep their current signatures.

4. Raise the dependency to the released tag: run `go get github.com/bborbe/http@v1.28.1` then `go mod tidy`. **The bump to v1.28.1 is required, not incidental** — v1.28.0 already has `NewRoundTripperRetryWithBackoff`, but it classifies a 429 carrying positive `X-RateLimit-Remaining` with no `Retry-After` as `unclassified`; v1.28.1 corrects that to `secondary`. Wiring v1.28.0 would emit the wrong `class=` value in every retry log line, which is the label this change exists to make honest. v1.28.1 may not be in the local module cache, so allow the fetch. Confirm `go.mod` lists `github.com/bborbe/http v1.28.1` in the direct `require` block.

5. Add one test per package, four in total, proving the retry actually fires through the client. Use each package's exported test alias from `export_test.go` — `NewHTTPTagsFetcherForTest`, `NewHTTPFetcherForTest`, `NewHTTPClientForTest` — never the unexported seam. Point it at an `httptest.Server` that returns `429` with `Retry-After: 1` on the first request and a valid `200` on the second, then assert **exactly two requests reached the server** and the call returned no error. A test that only asserts "no error" does not satisfy this — the request count is what proves the retry ran. The `200` body must suit the method each test exercises, or the call fails for an unrelated reason and the assertion stops meaning anything: `githubtags` needs a semver tag array such as `[{"name":"v1.0.0","commit":{"sha":"<40-hex>"}}]` (an empty array returns `ErrNoTags`); `githubchangelog` and `maintainerconfig` need a contents payload such as `{"encoding":"base64","content":"<base64>"}`; `githubreview` needs `{"object":{"sha":"<40-hex>"}}` for `TagExists`. Use the same Ginkgo v2 / Gomega style and external test package as the surrounding suite.

6. Update `CHANGELOG.md` with one `## Unreleased` bullet describing the retry wiring. If no `## Unreleased` section exists, create one directly above the newest released `## vX.Y.Z` heading; do not rename or renumber any released heading.

7. Before finishing, re-run `<verification>` and confirm it passes; then walk each numbered requirement against the change and report any that is unmet.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass unchanged
- Do not touch any file outside the four packages named above, `go.mod`, `go.sum` and `CHANGELOG.md`
- Do not change the `*WithBase` constructor signatures
- Keep the existing `github.com/bborbe/errors` and `glog` conventions; add no dependency beyond the `bborbe/http` bump
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` -- must pass.
</verification>

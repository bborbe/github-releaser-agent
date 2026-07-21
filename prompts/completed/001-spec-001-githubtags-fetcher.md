---
status: completed
spec: [001-bug-stale-current-version-collision]
summary: Created pkg/githubtags read-only GitHub REST API fetcher with full pagination, semver.Highest/IsValid helpers, counterfeiter mock, and 18 passing unit tests at 89.5% coverage
execution_id: github-releaser-agent-plantime-version-exec-001-spec-001-githubtags-fetcher
dark-factory-version: dev
created: "2026-07-21T16:40:00Z"
queued: "2026-07-21T16:52:51Z"
started: "2026-07-21T16:52:53Z"
completed: "2026-07-21T17:17:27Z"
branch: dark-factory/bug-stale-current-version-collision
---

<summary>
- Adds a new read-only package that asks GitHub for a repository's list of tags over the REST API.
- Exposes a small, mockable interface that returns the repository's highest semantic-version tag.
- Skips tag names that are not valid semantic versions rather than failing on them.
- Signals "this repo has no usable tags" with a distinct sentinel error so callers can fall back cleanly.
- Preserves each tag's original prefix style (e.g. keeps the leading `v`) so downstream version headers are unaffected.
- Mirrors the existing CHANGELOG fetcher: same construction shape, same GitHub App bearer-token auth, same ~15s client timeout, same counterfeiter mock convention.
- Ships full unit tests: highest-semver selection, mixed semver/non-semver tag lists, zero-tags, transport errors, empty inputs, auth-header forwarding.
- This package is not wired into the planning step yet — that is the next prompt.
</summary>

<objective>
Create a new read-only `pkg/githubtags` package that fetches a target repo's tags via the GitHub REST tags API and returns the highest-semver tag, mirroring the existing `pkg/githubchangelog` pure-API fetcher pattern (interface + counterfeiter mock + httptest-server unit tests). This is the network seam the planning step will use in prompt 2 to resolve `current_version` from the true remote latest instead of the stale emit-time snapshot.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions.

Read these files BEFORE writing code — you must mirror them closely:
- `/workspace/pkg/githubchangelog/fetcher.go` — the exact pattern to copy: `//counterfeiter:generate` annotation, `Fetcher` interface, `NewHTTPFetcher(token string)` public constructor, unexported `newHTTPFetcherWithBase(token, apiBase string)` test constructor, `httpFetcher` struct (`client *http.Client` with `Timeout: 15 * time.Second`, `token`, `apiBase`), URL escaping with `url.PathEscape`/`url.QueryEscape`, `Accept: application/vnd.github+json` + `X-GitHub-Api-Version: 2022-11-28` headers, `Authorization: Bearer <token>` only when token non-empty, error wrapping via `github.com/bborbe/errors`, `glog.V(2)` logging.
- `/workspace/pkg/githubchangelog/export_test.go` — the `NewHTTPFetcherForTest = newHTTPFetcherWithBase` seam-export idiom.
- `/workspace/pkg/githubchangelog/suite_test.go` — the Ginkgo suite bootstrap + `//go:generate` counterfeiter line to copy verbatim (renaming the suite string).
- `/workspace/pkg/githubchangelog/fetcher_test.go` — the httptest.NewServer test structure to mirror (happy path, auth header forwarded, no-auth-when-empty, URL path assertion, 404, 5xx, malformed JSON, empty owner/repo).
- `/workspace/pkg/maintainerconfig/fetcher.go` — the `ErrFileNotFound` sentinel pattern (`stderrors.New(...)`, doc comment noting `errors.Is` usage, project sentinel convention "mirrors pkg/githubreview.ErrTagNotFound"). You will define an analogous `ErrNoTags` sentinel.
- `/workspace/pkg/semver/semver.go` — the pure-Go leaf-library style (`BumpVersion(ctx, current, bump)`, `github.com/bborbe/errors` wrapping, no IO). You will ADD a comparison helper here (see requirements).

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface→constructor→struct, counterfeiter, error wrapping.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega, external test packages, coverage ≥80%.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors`, never `fmt.Errorf`, never `context.Background()` in pkg code.

The GitHub REST tags API endpoint is `GET /repos/{owner}/{repo}/tags` and returns a JSON array of objects each shaped `{"name": "v0.101.1", ...}` (paginated, 30 per page by default; request `?per_page=100`). A repo with zero tags returns HTTP 200 with an empty JSON array `[]` (NOT a 404).
</context>

<requirements>

## 1. Extend `pkg/semver` with a highest-semver selector (pure Go, no external deps)

`golang.org/x/mod/semver` is an indirect, NON-vendored dependency — do NOT import it. Implement the comparison in pure Go inside `pkg/semver` reusing the existing parse style in `BumpVersion`.

Add to `/workspace/pkg/semver/semver.go` (or a new file `/workspace/pkg/semver/highest.go` in the same `package semver`):

- A function `IsValid(v string) bool` that returns true iff `v` (with an optional leading `v`) parses as exactly three non-negative integer components `X.Y.Z` (reuse the same trim/split/atoi/negative-check logic already in `BumpVersion`). Pre-release/build-metadata suffixes (e.g. `v1.2.3-rc1`) are NOT valid for this fix — treat them as non-semver (skipped). Keep it strict: three numeric components only.
- A function:
  ```go
  // Highest returns the highest-semver tag from names, preserving that
  // tag's ORIGINAL string (including any "v" prefix) so downstream
  // header-prefix inference is unaffected. Non-semver names (per IsValid)
  // are skipped, not errored. Returns ("", false) when no name in names
  // is valid semver (empty input, or all names non-semver). Comparison is
  // numeric on (major, minor, patch) — creation order is irrelevant.
  func Highest(names []string) (string, bool)
  ```
  Compare numerically by major, then minor, then patch. On an exact numeric tie between two spellings (e.g. `v1.2.3` and `1.2.3`), return the first one encountered in `names` — do not error. Return the winner's original string verbatim.

Do NOT change the existing `BumpVersion` signature or behavior.

## 2. Create `pkg/githubtags/tags.go`

New file `/workspace/pkg/githubtags/tags.go`, `package githubtags`, with the standard project license header (copy the 3-line header from `pkg/githubchangelog/fetcher.go`).

Define:

- A package doc comment mirroring `pkg/githubchangelog`'s (read-only, sole network boundary for tag resolution, mockable, auth = bearer token at construction, empty token = anonymous).
- The counterfeiter annotation, targeting a NEW mock file so it does not collide with the existing `mocks/fetcher.go`:
  ```go
  //counterfeiter:generate -o ../../mocks/tags_fetcher.go --fake-name TagsFetcher . TagsFetcher
  ```
- The interface (name it `TagsFetcher` to avoid ambiguity with `githubchangelog.Fetcher`):
  ```go
  // TagsFetcher resolves a remote GitHub repo's highest-semver tag.
  // Implementations MUST be safe for concurrent use.
  //
  // LatestSemverTag returns the highest-semver tag string (original
  // spelling, e.g. "v0.101.1"). When the repo has zero tags OR none of
  // its tags are valid semver, it returns ErrNoTags so the caller can
  // fall back to the emit-time snapshot cleanly. All other failures
  // (transport, non-2xx, decode) return a wrapped error.
  type TagsFetcher interface {
      LatestSemverTag(ctx context.Context, owner, repo string) (string, error)
  }
  ```
- The sentinel (mirror `maintainerconfig.ErrFileNotFound` doc style):
  ```go
  // ErrNoTags signals the repo has no usable semver tag (empty tag list,
  // or every tag name is non-semver). Callers use errors.Is(err, ErrNoTags)
  // to fall back to the frontmatter snapshot with no warning (spec 001
  // no-tags branch). Mirrors pkg/maintainerconfig.ErrFileNotFound and
  // pkg/githubreview.ErrTagNotFound (project sentinel convention).
  var ErrNoTags = stderrors.New("githubtags: no usable semver tag on remote")
  ```
  Import `stderrors "errors"` for the sentinel (matching `maintainerconfig`).
- `NewHTTPTagsFetcher(token string) TagsFetcher` — public constructor, `http.Client{Timeout: 15 * time.Second}`, `apiBase: "https://api.github.com"`.
- `newHTTPTagsFetcherWithBase(token, apiBase string) TagsFetcher` — unexported test constructor.
- The `httpTagsFetcher` struct with `client *http.Client`, `token string`, `apiBase string`.

## 3. Implement `LatestSemverTag`

- Guard empty `owner` / `repo` → wrapped `errors.Errorf(ctx, "list tags: owner empty")` / `"list tags: repo empty"` (mirror the CHANGELOG fetcher's empty-arg guards).
- Build the first-page endpoint `fmt.Sprintf("%s/repos/%s/%s/tags?per_page=100", f.apiBase, url.PathEscape(owner), url.PathEscape(repo))`.
- **Paginate — REQUIRED, load-bearing.** GitHub's tags endpoint returns tags in refname order, NOT semver order, and caps each page at 100. Target repos (e.g. vault-cli, currently at `v0.101.x`) have well over 100 tags, so the highest semver can live on page 2+. Fetching only page 1 would silently miss it and reintroduce the exact stale-version bug this fix closes. Accumulate names across ALL pages.
- **Extract a per-page helper with an explicit return contract** so `resp` never leaks into the loop scope and `defer resp.Body.Close()` fires once per page:
  - `func (f *httpTagsFetcher) fetchPage(ctx context.Context, pageURL string) (names []string, nextURL string, err error)`.
  - Inside `fetchPage`: build `http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)` (wrap build failure `"list tags: build request"`); set the same three headers as the CHANGELOG fetcher; set `Authorization: Bearer <token>` only when `f.token != ""`.
  - `resp, err := f.client.Do(req)`; on transport error wrap `"list tags: http %s"` with `pageURL`. `defer resp.Body.Close()` (fires at helper return, i.e. per page).
  - `io.ReadAll` the body (wrap `"list tags: read body"`). Non-2xx (`< 200 || >= 300`): wrap `errors.Errorf(ctx, "list tags: status %d: %s", resp.StatusCode, preview)` with a 200-char truncated preview (copy the truncation block from the CHANGELOG fetcher). NOTE: a genuinely-missing repo returns 404 here — a hard error, NOT `ErrNoTags`. `ErrNoTags` is reserved for a 2xx response whose full (all-pages) tag list yields no semver.
  - `glog.V(2).Infof("list tags: GET %s status=%d bytes=%d", pageURL, resp.StatusCode, len(body))`.
  - Decode into `[]tagResponse` where `type tagResponse struct { Name string \`json:"name"\` }` (wrap `"list tags: decode json"`); collect each `.Name` into the returned `names`.
  - Return `nextURL = nextLink(resp.Header.Get("Link"))` — see the helper below.
- **`LatestSemverTag` drives the loop**: declare `names := []string{}` **before** the loop; then `for pageURL := endpoint; pageURL != ""; { pageNames, next, err := f.fetchPage(ctx, pageURL); if err != nil { return "", err }; names = append(names, pageNames...); pageURL = next }`. Cap the loop at 100 iterations to guard a self-referential `next`; on exceeding, return wrapped `errors.Errorf(ctx, "list tags: too many pages")`.
- **`nextLink(linkHeader string) string`** in the same file: pure string parsing of an RFC 5988 `Link` header — split on `,`, each part `<url>; rel="next"`; return the `<...>` URL of the part whose `rel` token is `next`, else `""`. No I/O, no ctx.
- After the loop, `names` holds every tag across all pages. Call `semver.Highest(names)`:
  - If `ok` → `glog.V(2).Infof("list tags: %s/%s highest=%s (of %d tags)", owner, repo, latest, len(names))`; return `(latest, nil)`.
  - If `!ok` → `glog.V(2).Infof("list tags: %s/%s no usable semver tag (%d tags)", owner, repo, len(names))`; return `("", ErrNoTags)`.

Import `"github.com/bborbe/github-releaser-agent/pkg/semver"`.

## 4. Test seam export

Create `/workspace/pkg/githubtags/export_test.go` (package `githubtags`) mirroring `githubchangelog/export_test.go`:
```go
var NewHTTPTagsFetcherForTest = newHTTPTagsFetcherWithBase
var NextLink = nextLink
```

## 5. Suite bootstrap

Create `/workspace/pkg/githubtags/suite_test.go` by copying `pkg/githubchangelog/suite_test.go` verbatim EXCEPT:
- package `githubtags_test`
- suite name string `"GithubTags Suite"`
Keep the `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` line.

## 6. Generate the counterfeiter mock

Run `cd /workspace && go generate ./pkg/githubtags/...` (or `go generate ./...`) so `/workspace/mocks/tags_fetcher.go` is produced with fake type `TagsFetcher`. If `go generate` is unavailable in-loop, run `make generate`. Verify the mock file exists and defines `type TagsFetcher struct` after generation.

## 7. Unit tests — `pkg/githubtags/tags_test.go` (package `githubtags_test`)

Use `httptest.NewServer` + `NewHTTPTagsFetcherForTest` exactly like `githubchangelog/fetcher_test.go`. Cover ALL of:

1. **Highest-semver selection**: server returns `[{"name":"v0.101.0"},{"name":"v0.101.1"},{"name":"v0.100.9"}]` → `LatestSemverTag` returns `"v0.101.1"`, no error.
2. **Creation-order is NOT semver-order**: server returns tags in scrambled order (e.g. `v1.0.0` last, `v0.9.0` first) → still returns `v1.0.0`. This asserts numeric compare, not list order.
3. **Non-semver tags skipped**: server returns `[{"name":"latest"},{"name":"v0.101.1"},{"name":"nightly"},{"name":"v0.101.0"}]` → returns `"v0.101.1"`; the non-semver names do not error.
4. **Prefix preserved**: server returns `[{"name":"0.101.1"},{"name":"0.101.0"}]` (no `v` prefix) → returns `"0.101.1"` verbatim (no `v` added).
5. **Zero tags → ErrNoTags**: server returns `[]` with HTTP 200 → `errors.Is(err, githubtags.ErrNoTags)` is true, returned string is empty. Use `stderrors "errors"` and `stderrors.Is` in the test import (or `errors.Is`).
6. **All-non-semver → ErrNoTags**: server returns `[{"name":"latest"},{"name":"nightly"}]` HTTP 200 → `ErrNoTags`.
7. **Transport/5xx → wrapped hard error (NOT ErrNoTags)**: server returns 503 → err occurs, `errors.Is(err, githubtags.ErrNoTags)` is FALSE, message contains `"list tags"` and `"status 503"`.
8. **404 → wrapped hard error (NOT ErrNoTags)**: server returns 404 → err occurs, `errors.Is(err, githubtags.ErrNoTags)` is FALSE, message contains `"status 404"`.
9. **Malformed JSON → wrapped decode error**: server returns `not-json` → message contains `"decode json"`.
10. **Auth header forwarded**: token `"test-token"` → captured `Authorization` header equals `"Bearer test-token"`.
11. **No auth header when token empty**: token `""` → captured `Authorization` header is empty.
12. **URL asserts path + per_page**: assert `r.URL.Path == "/repos/foo/bar/tags"` and `r.URL.Query().Get("per_page") == "100"`.
13. **Empty owner rejected**: `LatestSemverTag(ctx, "", "bar")` → err message contains `"owner empty"`.
14. **Empty repo rejected**: `LatestSemverTag(ctx, "foo", "")` → err message contains `"repo empty"`.
15. **Pagination — highest tag on page 2 (load-bearing)**: the httptest server serves page 1 (100 lower tags, e.g. `v0.100.0`..`v0.100.99`) with a `Link: <{base}/repos/foo/bar/tags?per_page=100&page=2>; rel="next"` header, and page 2 (`[{"name":"v0.101.1"}]`) with NO `Link` header. Assert `LatestSemverTag` returns `"v0.101.1"` — proving pages beyond the first are fetched and the cross-page maximum wins. Point the `next` URL at the test server's own base (rewrite `apiBase` into the Link header) so the loop stays on the httptest server.
16. **No Link header → single page**: server returns one page, no `Link` header → exactly one request is made (assert a request counter == 1) and the highest of that page is returned.
17. **`nextLink` helper unit test**: table-test the parser directly via the exported `NextLink` (from `export_test.go`) — `<https://api.github.com/...page=2>; rel="next", <...page=5>; rel="last"` → returns the `page=2` URL; a header with only `rel="last"`/`rel="prev"` → `""`; empty header → `""`.
18. **Too-many-pages cap**: the httptest server ALWAYS returns a `Link` header whose `rel="next"` points back at itself (a self-referential loop) → `LatestSemverTag` stops after the 100-iteration cap and returns a wrapped error whose message contains `"too many pages"` (asserts the infinite-loop guard fires).

## 8. Unit tests for the semver helper — extend `pkg/semver/semver_test.go`

Add a `DescribeTable` (or new `It` blocks) covering `semver.Highest` and `semver.IsValid`:
- `IsValid`: `"v1.2.3"`→true, `"1.2.3"`→true, `"v1.2.3-rc1"`→false, `"latest"`→false, `"v1.2"`→false, `"v1.2.3.4"`→false, `"v-1.2.3"`→false, `""`→false.
- `Highest`:
  - `["v0.101.0","v0.101.1","v0.100.9"]` → `("v0.101.1", true)`.
  - `["v1.0.0","v0.9.0"]` scrambled → `("v1.0.0", true)`.
  - `["latest","v0.5.0","nightly"]` → `("v0.5.0", true)`.
  - `["0.101.1","0.101.0"]` → `("0.101.1", true)` (prefix preserved, no `v` added).
  - `[]` → `("", false)`.
  - `["latest","nightly"]` → `("", false)`.
  - `["v1.2.3","1.2.3"]` (numeric tie) → returns first-encountered `"v1.2.3"`, `ok=true`.

## 9. Coverage

Both `pkg/githubtags` and the new `pkg/semver` code must reach ≥80% statement coverage. Verify:
```
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/githubtags/... ./pkg/semver/... && go tool cover -func=/tmp/cover.out
```
</requirements>

<constraints>
- Resolver is READ-ONLY (GitHub GET). No git clone, no `git ls-remote`, no write path anywhere in this package.
- Do NOT import `golang.org/x/mod/semver` — it is an indirect, non-vendored dependency. Implement semver comparison in pure Go inside `pkg/semver`.
- "Latest" = highest semver (numeric compare on major/minor/patch), NOT most-recently-created. Non-semver tag names are SKIPPED, not errored.
- Prefix preservation: return the winning tag's ORIGINAL string verbatim (keep or drop the `v` exactly as the remote spelled it). Never normalize.
- `ErrNoTags` is ONLY for a successful (2xx) response yielding no usable semver. Transport errors, 4xx, and 5xx are hard wrapped errors — NEVER downgrade them to `ErrNoTags` (that distinction is load-bearing for prompt 2's no-tags-vs-transient-error branch).
- Errors use `github.com/bborbe/errors` (`errors.Errorf`, `errors.Wrapf`) — never `fmt.Errorf`, never `context.Background()`. Sentinel uses `stderrors "errors"` alias exactly like `maintainerconfig`.
- Logging is `glog` with `V(2)`-gated `Info`.
- Interface + private struct + `New*` constructor; counterfeiter mock (never a manual mock); external `*_test` test package.
- Do NOT wire this package into `steps_planning.go` or `factory.go` — that is prompt 2. This prompt ships the seam + its own tests only.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from `/workspace`:
```
cd /workspace && go generate ./pkg/githubtags/... 2>/dev/null; make test
```
Then:
```
cd /workspace && test -f mocks/tags_fetcher.go && grep -q "type TagsFetcher struct" mocks/tags_fetcher.go && echo MOCK_OK
cd /workspace && grep -n counterfeiter pkg/githubtags/*.go
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/githubtags/... ./pkg/semver/... && go tool cover -func=/tmp/cover.out | tail -1
```
Finally run `make precommit` — must pass (fmt, generate, test, lint, vet, vuln, license).
</verification>

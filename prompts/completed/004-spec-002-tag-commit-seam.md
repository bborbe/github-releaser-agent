---
status: completed
spec: [002-planning-close-nothing-to-release]
summary: Added CommitSHAForTag lookup and ErrTagNotFound sentinel to githubtags package with full test coverage and shared collectTags pagination
execution_id: github-releaser-agent-nothing-to-release-exec-004-spec-002-tag-commit-seam
dark-factory-version: v0.192.9
created: "2026-08-02T15:20:00Z"
queued: "2026-08-02T13:42:58Z"
started: "2026-08-02T13:43:00Z"
completed: "2026-08-02T13:46:13Z"
branch: dark-factory/planning-close-nothing-to-release
---

<summary>
- Teaches the existing GitHub tags lookup one new question: "which commit does tag X point at?"
- Answers with the underlying commit, not the tag object, so annotated tags behave like lightweight ones.
- Signals "that tag does not exist on the remote" with its own distinct sentinel so callers can tell absence from failure.
- Costs one pass over the same tag listing the lookup already performs — no per-tag requests, no second round trip.
- Network failures, 404s, and malformed responses stay hard errors, never silently reported as "tag absent".
- Refactors the two lookups to share a single pagination pass so behavior cannot drift between them.
- Existing "highest semver tag" behavior is unchanged, including all of its current tests.
- Nothing consumes the new lookup yet — wiring it into the planning step is the next prompt.
</summary>

<objective>
Extend the existing `githubtags.TagsFetcher` seam with a `CommitSHAForTag` lookup that returns the **commit** SHA a named tag points at (GitHub's tags-list endpoint reports `commit.sha` already dereferenced for annotated tags), plus an `ErrTagNotFound` sentinel for the tag-absent case. This is the read-only seam the planning step will use in prompt 2 to decide whether a task's `ref` is already released. No behavior change ships in this prompt.
</objective>

<context>
Read `/workspace/CLAUDE.md` for project conventions.

Read these files fully BEFORE writing code:
- `/workspace/pkg/githubtags/tags.go` — the package you are extending. Note the existing `TagsFetcher` interface, `ErrNoTags` sentinel (`stderrors "errors"` alias), `NewHTTPTagsFetcher` / `newHTTPTagsFetcherWithBase` constructors, `httpTagsFetcher` struct, `LatestSemverTag`, the `fetchPage` per-page helper, the 100-page loop cap, and `nextLink`.
- `/workspace/pkg/githubtags/tags_test.go` — the httptest-server test style you must mirror (`tagJSON` helper, `githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)`, numbered `It("N: ...")` names). Your new tests continue that numbering.
- `/workspace/pkg/githubtags/export_test.go` and `/workspace/pkg/githubtags/suite_test.go` — the seam-export and suite bootstrap; no changes expected, but read them.
- `/workspace/pkg/githubreview/client.go` — the `ErrTagNotFound` sentinel doc style to mirror (`stderrors.New("githubreview: tag not found")`, `errors.Is` usage note). Your new sentinel is the `githubtags` analogue; it is a DIFFERENT symbol in a different package.
- `/workspace/mocks/tags_fetcher.go` — the generated counterfeiter fake you will regenerate.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface → constructor → struct, counterfeiter, error wrapping.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega, external `_test` package, coverage ≥ 80%.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`, sentinel errors with the `stderrors` alias, never `fmt.Errorf`, never `context.Background()` in `pkg/`.

Load-bearing API fact (spec 002 § Assumptions, verified 2026-08-02 against the incident repo): `GET /repos/{owner}/{repo}/tags` reports `commit.sha` **already dereferenced to the underlying commit** for annotated tags. For `bborbe/tts-mcp` tag `v0.3.1` the payload's `commit.sha` is `7657fc0af1a115b2518ac4c4d332722d8fc3d35c` (the commit) while the annotated tag *object* SHA is `9aa07bf6c9a64221e340b5529f7e47faf2f189fd`. That is why this seam needs no second dereference request and no annotated-vs-lightweight branch. The payload shape is:

```json
[
  {
    "name": "v0.3.1",
    "commit": { "sha": "7657fc0af1a115b2518ac4c4d332722d8fc3d35c", "url": "..." },
    "zipball_url": "...", "tarball_url": "...", "node_id": "..."
  }
]
```
</context>

<requirements>

## 1. Extend the tag payload struct — `/workspace/pkg/githubtags/tags.go`

Replace the existing:

```go
type tagResponse struct {
	Name string `json:"name"`
}
```

with:

```go
// tagResponse is one entry of the GitHub tags-list payload. Commit.SHA is
// the COMMIT the tag points at — GitHub dereferences annotated tags on this
// endpoint, so no second request against the tag object is needed
// (spec 002 § Assumptions).
type tagResponse struct {
	Name   string        `json:"name"`
	Commit tagCommitInfo `json:"commit"`
}

// tagCommitInfo is the nested commit object of a tags-list entry.
type tagCommitInfo struct {
	SHA string `json:"sha"`
}
```

Unknown JSON fields (`zipball_url`, `node_id`, …) are ignored by `encoding/json` — do NOT add them.

## 2. Add the `ErrTagNotFound` sentinel

Next to the existing `ErrNoTags`, add:

```go
// ErrTagNotFound signals that the named tag is absent from the remote's
// tag list (a successful 2xx listing that contains no entry with that
// exact name). Callers use errors.Is(err, ErrTagNotFound) to distinguish
// "tag genuinely does not exist" from a transport/decode failure — spec
// 002 escalates on BOTH, but only the absent case is expected traffic.
// Mirrors pkg/githubreview.ErrTagNotFound and pkg/maintainerconfig.ErrFileNotFound
// (project sentinel convention).
var ErrTagNotFound = stderrors.New("githubtags: tag not found on remote")
```

Do NOT change `ErrNoTags`.

## 3. Extract the pagination into `collectTags` (shared by both lookups)

Add an unexported method that owns the argument guards, endpoint construction, and the page loop, returning every entry across all pages:

```go
// collectTags paginates the full tags list for owner/repo and returns every
// entry across all pages. GitHub returns tags in refname order (NOT semver
// order) capped at 100 per page, so both lookups must read every page.
// It is the single pagination site for this package — one tags-list
// pagination per call, never one request per tag.
func (f *httpTagsFetcher) collectTags(ctx context.Context, owner, repo string) ([]tagResponse, error)
```

Move into it, VERBATIM and in this order — **except the accumulator, which becomes `tags := []tagResponse{}`** (the helper returns `[]tagResponse`, not `[]string`) — the code currently at the top of `LatestSemverTag`:
- the `owner == ""` guard returning `errors.Errorf(ctx, "list tags: owner empty")`
- the `repo == ""` guard returning `errors.Errorf(ctx, "list tags: repo empty")`
- the `endpoint := fmt.Sprintf("%s/repos/%s/%s/tags?per_page=100", f.apiBase, url.PathEscape(owner), url.PathEscape(repo))` construction
- the `for pageURL := endpoint; pageURL != "";` loop with the `iterations > 100` cap returning `errors.Errorf(ctx, "list tags: too many pages")`

**Every error message string above must stay byte-identical** — existing tests assert on `"owner empty"`, `"repo empty"`, `"too many pages"`, `"status 503"`, `"status 404"`, and `"decode json"`.

Change `fetchPage`'s first return value from `names []string` to `tags []tagResponse`:

```go
func (f *httpTagsFetcher) fetchPage(
	ctx context.Context,
	pageURL string,
) (tags []tagResponse, nextURL string, err error)
```

Inside `fetchPage`, keep everything else unchanged (headers, auth-only-when-token-non-empty, `defer resp.Body.Close()`, body read, non-2xx preview truncation at 200 chars, both `glog.V(2)`/`V(3)` lines, `json.Unmarshal` into `[]tagResponse` wrapped as `"list tags: decode json"`, `nextLink`). Delete the `for _, tag := range tags { names = append(names, tag.Name) }` conversion — return the decoded slice directly. The current code computes the next link TWICE (`next := nextLink(linkHdr)` near the log lines and `nextURL = nextLink(resp.Header.Get("Link"))` at the bottom); delete the second computation and `return tags, next, nil`.

## 4. Rewrite `LatestSemverTag` on top of `collectTags` — behavior unchanged

```go
func (f *httpTagsFetcher) LatestSemverTag(ctx context.Context, owner, repo string) (string, error) {
	tags, err := f.collectTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	latest, ok := semver.Highest(names)
	if !ok {
		glog.V(2).Infof("list tags: %s/%s no usable semver tag (%d tags)", owner, repo, len(names))
		return "", ErrNoTags
	}
	glog.V(2).Infof("list tags: %s/%s highest=%s (of %d tags)", owner, repo, latest, len(names))
	return latest, nil
}
```

Keep the existing doc comment on `LatestSemverTag` (adjust only if it now misstates where pagination lives). All existing `pkg/githubtags` tests must still pass unmodified (the file's numbered `It` blocks run to 18, but there are 21 in total because of `17b` and `17c` — do not renumber any of them).

## 5. Add `CommitSHAForTag` to the interface

Extend the `TagsFetcher` interface — do NOT create a second interface, and do NOT change `LatestSemverTag`'s signature:

```go
type TagsFetcher interface {
	LatestSemverTag(ctx context.Context, owner, repo string) (string, error)

	// CommitSHAForTag returns the COMMIT SHA the named tag points at.
	// GitHub's tags-list endpoint reports commit.sha already dereferenced
	// for annotated tags, so the returned SHA is always a commit, never a
	// tag object (spec 002 § Assumptions).
	//
	// Returns ErrTagNotFound when the listing succeeds but contains no
	// entry whose name equals tag exactly (case-sensitive, no v-prefix
	// normalisation). All other failures (empty args, transport, non-2xx,
	// decode, entry with an empty commit sha) return a wrapped error.
	// Costs one tags-list pagination — never one request per tag.
	CommitSHAForTag(ctx context.Context, owner, repo, tag string) (string, error)
}
```

Implementation on `*httpTagsFetcher`:

```go
func (f *httpTagsFetcher) CommitSHAForTag(
	ctx context.Context,
	owner, repo, tag string,
) (string, error) {
	if tag == "" {
		return "", errors.Errorf(ctx, "commit sha for tag: tag empty")
	}
	tags, err := f.collectTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	for _, entry := range tags {
		if entry.Name != tag {
			continue
		}
		if entry.Commit.SHA == "" {
			return "", errors.Errorf(ctx, "commit sha for tag: %s has empty commit sha", tag)
		}
		glog.V(2).Infof("list tags: %s/%s tag=%s commit=%s", owner, repo, tag, entry.Commit.SHA)
		return entry.Commit.SHA, nil
	}
	glog.V(2).Infof("list tags: %s/%s tag=%s not found (%d tags)", owner, repo, tag, len(tags))
	return "", ErrTagNotFound
}
```

Matching rule — single decision, no alternatives: **exact string equality on the tag name.** No `v`-prefix normalisation, no case folding, no fuzzy match. A caller asking for `0.3.1` when the remote tag is `v0.3.1` gets `ErrTagNotFound`, which the planning step (prompt 2) treats as an escalation. This is the spec's Failure Modes row 3 and is intentional.

Argument-guard ordering note: `tag == ""` is checked BEFORE `collectTags`, so an empty-tag call makes no HTTP request; empty `owner`/`repo` are still rejected by `collectTags` with the existing messages.

## 6. Regenerate the counterfeiter mock

```
cd /workspace && go generate ./pkg/githubtags/...
```
(fall back to `make generate` if that fails). Afterwards `/workspace/mocks/tags_fetcher.go` MUST contain `func (fake *TagsFetcher) CommitSHAForTag(` and `func (fake *TagsFetcher) CommitSHAForTagReturns(`. Do not hand-edit the generated file.

The `pkg` planning tests construct `&mocks.TagsFetcher{}` in many places; the regenerated fake's zero value returns `("", nil)` for the new method, which is harmless — nothing calls it yet. Do NOT modify `pkg/steps_planning_test.go` in this prompt.

## 7. Tests — append to `/workspace/pkg/githubtags/tags_test.go` (package `githubtags_test`)

Continue the existing numbered `It("N: ...")` naming (the file currently ends at 18; start at 19). Add a local payload helper next to `tagJSON` that emits the `commit` object:

```go
tagJSONWithCommits := func(entries [][2]string) string {
	items := make([]map[string]any, len(entries))
	for i, e := range entries {
		items[i] = map[string]any{
			"name":   e[0],
			"commit": map[string]string{"sha": e[1]},
		}
	}
	data, _ := json.Marshal(items)
	return string(data)
}
```

Cover ALL of:

19. **Dereferenced commit SHA for an annotated tag (spec AC #10).** Server returns the canned incident payload
    `[{"name":"v0.3.1","commit":{"sha":"7657fc0af1a115b2518ac4c4d332722d8fc3d35c"}},{"name":"v0.3.0","commit":{"sha":"1111111111111111111111111111111111111111"}}]`.
    `CommitSHAForTag(ctx, "bborbe", "tts-mcp", "v0.3.1")` returns `"7657fc0af1a115b2518ac4c4d332722d8fc3d35c"` with no error. Add an inline comment recording that the annotated tag OBJECT sha for that tag is `9aa07bf6c9a64221e340b5529f7e47faf2f189fd` and that the assertion pins the commit, not the tag object.
20. **Lightweight tag — same code path.** Same payload shape, different tag name, returns that entry's `commit.sha`.
21. **Tag absent → `ErrTagNotFound`.** Listing succeeds (HTTP 200, two entries), asked for `"v9.9.9"` → returned SHA is empty and `errors.Is(err, githubtags.ErrTagNotFound)` is true.
22. **Exact-match only.** Payload contains `v0.3.1`; asking for `0.3.1` (no `v`) → `ErrTagNotFound`. Asking for `V0.3.1` (capital V) → `ErrTagNotFound`.
23. **Empty tag argument.** `CommitSHAForTag(ctx, "foo", "bar", "")` → error message contains `"tag empty"`, and the httptest server recorded **zero** requests (assert a request counter == 0).
24. **Empty owner / empty repo.** Messages contain `"owner empty"` / `"repo empty"` respectively.
25. **Non-2xx is a hard error, NOT `ErrTagNotFound`.** Server returns 503 → error occurs, message contains `"status 503"`, and `errors.Is(err, githubtags.ErrTagNotFound)` is **false**. Repeat for 404 (`"status 404"`, sentinel false) — a missing repo must never read as a missing tag.
26. **Malformed JSON → decode error.** Body `not-json` → message contains `"decode json"`, sentinel false.
27. **Entry with empty commit sha → hard error.** Payload `[{"name":"v0.3.1","commit":{"sha":""}}]` → error message contains `"empty commit sha"`, `errors.Is(err, githubtags.ErrTagNotFound)` is false.
28. **Pagination — tag lives on page 2.** Page 1 serves 100 unrelated entries plus a `Link: <{server.URL}/repos/foo/bar/tags?per_page=100&page=2>; rel="next"` header; page 2 serves `[{"name":"v0.3.1","commit":{"sha":"7657fc0…"}}]` with no `Link`. `CommitSHAForTag(ctx,"foo","bar","v0.3.1")` returns the page-2 SHA. Mirror the existing pagination test's server-side page routing.
29. **Request cost — exactly one pagination.** Single page, no `Link` header → assert the server's request counter is exactly `1` after one `CommitSHAForTag` call (spec § Constraints: one additional tags-list pagination, never one request per tag).
30. **Auth header forwarded.** Token `"test-token"` → captured `Authorization` equals `"Bearer test-token"`; token `""` → header empty.

## 8. Coverage

```
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/githubtags/... && go tool cover -func=/tmp/cover.out
```
`pkg/githubtags` must stay ≥ 80% statement coverage.

## 9. CHANGELOG

Add to `/workspace/CHANGELOG.md` under `## Unreleased` (create the section below the preamble and above `## v0.3.2` if it does not exist):

```
- feat(githubtags): add `TagsFetcher.CommitSHAForTag` — resolves a tag name to the commit SHA it points at via the tags-list endpoint (`commit.sha` is already dereferenced for annotated tags, so no second request), with an `ErrTagNotFound` sentinel for the tag-absent case and a shared `collectTags` pagination pass
```

Write it VERBATIM. In particular it must NOT contain the phrase `nothing to release` — prompt 2 owns that exact phrase and the spec requires exactly one `## Unreleased` line carrying it.
</requirements>

<constraints>
- This package stays READ-ONLY (GitHub GET). No git clone, no `git ls-remote`, no write path.
- `ErrTagNotFound` is ONLY for a 2xx listing with no name match. Transport errors, 4xx, 5xx, decode failures, and empty-commit-sha entries are hard wrapped errors — NEVER downgrade them to a sentinel. That distinction is load-bearing for prompt 2's fail-open branch.
- `ErrNoTags` semantics, `LatestSemverTag`'s signature and behavior, and every existing error message string are unchanged.
- One tags-list pagination per lookup call. No per-tag requests, no second dereference request, no new HTTP client, no new endpoint.
- Errors use `github.com/bborbe/errors` (`errors.Errorf`, `errors.Wrapf`) — never `fmt.Errorf`, never `context.Background()`. Sentinels use the existing `stderrors "errors"` alias.
- Logging is `glog` with `V(n)`-gated `Info`.
- Interface + private struct + `New*` constructor; counterfeiter mock (never a manual mock); external `*_test` test package.
- Do NOT wire this into `pkg/steps_planning.go`, `pkg/plan_output.go`, or `pkg/factory/factory.go` — that is prompt 2. Do NOT edit `pkg/steps_planning_test.go`.
- Do NOT touch `docs/dod.md` — prompt 2 owns it.
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
cd /workspace && grep -n "CommitSHAForTag" pkg/githubtags/tags.go
cd /workspace && grep -n "ErrTagNotFound" pkg/githubtags/tags.go
cd /workspace && grep -c "func (fake \*TagsFetcher) CommitSHAForTag" mocks/tags_fetcher.go
cd /workspace && grep -n "7657fc0af1a115b2518ac4c4d332722d8fc3d35c" pkg/githubtags/tags_test.go
cd /workspace && grep -c "CommitSHAForTag" pkg/githubtags/tags_test.go
cd /workspace && grep -n "ErrTagNotFound" pkg/githubtags/tags_test.go
cd /workspace && go test -coverprofile=/tmp/cover.out -mod=mod ./pkg/githubtags/... && go tool cover -func=/tmp/cover.out | tail -1
cd /workspace && grep -n "CommitSHAForTag" CHANGELOG.md
```

Finally run `make precommit` — must exit 0 (fmt, generate, test, lint, vet, vuln, license).
</verification>

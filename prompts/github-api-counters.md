---
status: draft
---

# Count this agent's GitHub API calls per rate-limit bucket

<summary>
- Every GitHub API request the agent's own REST clients make is now counted, attributed to the agent's own App id
- The count is split by rate-limit bucket, so core, search and graphql spend are never merged
- A rate-limited response is counted separately, so throttling is visible without reading logs
- The counters ride the metrics this agent already pushes when a release run finishes
- All-zero series exist from the first run, so a missing count is visibly zero rather than absent
- The cluster is a label on every series, settable per deployment, so a dev run and a prod run are distinguishable once the deployment sets it
- No token, key or other secret can reach a label
- Nothing else changes: request building, pagination, error wording and the retry policy are untouched (its helper gains an inner-transport parameter, nothing else)
</summary>

<objective>
Make this agent's GitHub consumption attributable and sizeable — one query names its spend and one release run's call count is readable — by counting every request at the HTTP transport, split by the rate-limit bucket the request path spends and stamped with the App id and cluster.
</objective>

<context>
Read `CLAUDE.md` for project conventions, and `docs/dod.md` for the quality bar this change is held to.

**Why this is a push, not a scrape.** This agent is a Kubernetes Job, one pod per release task. `main.go` builds a fresh `prometheus.NewRegistry()` per run and wraps it in a `push.New(...)` pusher grouped `agent`/`task_type`; the deferred push at the end of `Run` is the only emission path. There is no `/metrics` endpoint and no HTTP server. The counters MUST be registered on that per-run registry — an implementation on `prometheus.DefaultRegisterer` or behind an HTTP handler emits nothing on the production path.

**Precondition — verify before writing any code.** The retry wiring from the sibling prompt `018-wire-retry-roundtripper.md` must already be merged: `github.com/bborbe/http` at the version that prompt pins, in `go.mod`'s direct `require` block (v1.28.1 at the time of writing), and a `retryTransportFor(apiBase string)` helper in each of `pkg/githubtags/tags.go`, `pkg/githubchangelog/fetcher.go`, `pkg/githubreview/client.go`, `pkg/maintainerconfig/fetcher.go`. If any of that is absent, stop and report it as a blocker — do not invent it and do not re-add it.

Read `~/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` for the counter pre-initialisation, naming and Help-string rules this change must satisfy.

**The four packages that build a GitHub client**, each a `&http.Client{...}` with `apiBase: "https://api.github.com"`, all calling `api.github.com` with the same App installation token:

| Package | Constructors | Client literal |
|---|---|---|
| `pkg/githubtags/tags.go` | `NewHTTPTagsFetcher`, `newHTTPTagsFetcherWithBase` | in both |
| `pkg/githubchangelog/fetcher.go` | `NewHTTPFetcher`, `newHTTPFetcherWithBase` | in both |
| `pkg/githubreview/client.go` | `NewHTTPClient` → `newHTTPClientWithBase` | in `newHTTPClientWithBase` only |
| `pkg/maintainerconfig/fetcher.go` | `NewHTTPFetcher`, `newHTTPFetcherWithBase` | in both (`Timeout: fetchTimeout`) |

`pkg/githubauth/githubauth.go` builds no client — it delegates to `lib/githubapp.MintIAT`. It is out of scope.

**Reference implementation** lives in another repo that is not mounted in this container (`github-pr-watcher`, the peer service that already ships these counters). The two parts that matter are inlined below; everything else follows this repo's own conventions.

The bucket function — the single load-bearing rule of this change:

```go
// Bucket returns the GitHub rate-limit bucket a request spends, derived from
// the request path only. A response header must never be consulted: the
// bucket a response names belongs to that response and arrives after the
// call, so reading it misattributes every call that follows a different
// bucket's response.
func Bucket(req *http.Request) string {
	if req == nil || req.URL == nil {
		return BucketCore
	}
	path := req.URL.Path
	switch {
	case strings.HasPrefix(path, "/search/"), strings.HasPrefix(path, "/api/v3/search/"):
		return BucketSearch
	case path == "/graphql", path == "/api/graphql":
		return BucketGraphQL
	default:
		return BucketCore
	}
}
```

The metric definitions — names and label sets are fixed by the cross-service query this change feeds:

```
github_api_calls_total        labels: service, credential, bucket, outcome, cluster
github_api_rate_limited_total labels: service, credential, bucket, class, cluster
```

`bucket` ∈ {`core`, `search`, `graphql`}; `outcome` ∈ {`success`, `error`}; `class` is always `unclassified` for now; `service` is the constant `github-releaser-agent` (NOT the factory's `maintainer-agent-github-releaser`); `credential` is the App id as a decimal string; `cluster` comes from a flag.
</context>

<requirements>
1. **New package `pkg/githubapimetrics`.** Follow the repo's Interface → Constructor → Struct → Method pattern and its `errors`/`glog` conventions.

   - `Bucket(req *http.Request) string` — exactly as inlined in `<context>`, with the exported constants `BucketCore = "core"`, `BucketSearch = "search"`, `BucketGraphQL = "graphql"`, `OutcomeSuccess = "success"`, `OutcomeError = "error"`, and `ServiceName = "github-releaser-agent"`. Do **not** re-declare the rate-limit class: use `string(libhttp.RateLimitClassUnclassified)` from `github.com/bborbe/http`, the same constant the retry transport classifies with, so the counter and the retry log line cannot drift apart.
   - `Credential(appID int64) string` — the App id as a decimal string, and `anonymous` when the id is `<= 0`. Never a token, never a PEM, never an empty string. Both entry points call this one expression.
   - `Metrics` interface with three methods: `IncCall(bucket, outcome string)`, `IncRateLimited(bucket, class string)`, and `Transport(base http.RoundTripper) http.RoundTripper`. The `Transport` method is deliberately on the same interface as the counters: this package owns both halves of one mechanism, and splitting them would let a caller install the transport without the counters.
   - `New(registry *prometheus.Registry, credential, cluster string) Metrics` — takes `*prometheus.Registry`, not `prometheus.Registerer`, and registers with `MustRegister`, mirroring `libmetrics.NewJobMetrics` (`errcheck` in `.golangci.yml` rejects an unchecked `Register` error). Build the two `prometheus.CounterVec`s with the exact names and label sets above and **these Help strings verbatim** — the same metric names are exposed by `github-pr-watcher` and `github-build-watcher`, and a differing `# HELP` on one name breaks the scrape:

     - calls: `Total GitHub API HTTP round trips, counted once per call at the transport including transport errors, by service, credential, rate-limit bucket, outcome and cluster. The bucket is derived from the request path (search, graphql or core), never from a response header; outcome is success for a status below 400 and error otherwise.`
     - rate limited: `Total GitHub API responses rejected by rate limiting, by service, credential, request-path bucket, class and cluster. Counted when the status is 429, or 403 with X-RateLimit-Remaining: 0 or a Retry-After header; class is always unclassified until a classification rule exists.`

     Then **pre-initialise every bounded label combination to 0** with `.WithLabelValues(...).Add(0)`: for each of the three buckets × each of the two outcomes → `github_api_calls_total` (six series), and `class="unclassified"` → `github_api_rate_limited_total` (three series). Six call series and three rate-limited series per process. The `search` and `graphql` buckets are unreachable in this agent today — every call site is a core `/repos/...` endpoint — and they are kept deliberately: the cross-service query groups `by (bucket)`, so a uniform bucket domain is what keeps this agent's rows joinable with the watchers'. Say so in the package doc comment rather than trimming the branches.
   - The counting transport's `RoundTrip` calls `inner.RoundTrip(req)` first, then records, and returns the response and error unchanged. Record: bucket from `Bucket(req)`; outcome `success` when the error is nil and `resp.StatusCode < 400`, otherwise `error` (a transport error with a nil response counts as `error`); additionally count one `IncRateLimited(bucket, string(libhttp.RateLimitClassUnclassified))` when the response is `429`, or `403` carrying `X-RateLimit-Remaining: 0` or a `Retry-After` header. Never read the response body.
   - No nil-`Metrics` path: `New` always returns a usable implementation and every call site in this change passes one. A nil interface would panic before `Transport` ran, so do not add a nil-receiver guard nothing exercises.

2. **Thread the recorder into the four clients.** Each of the four packages' constructors gains an `apiMetrics githubapimetrics.Metrics` parameter, including the unexported `*WithBase` seam so the seam and the production constructor take the same path. The client literal becomes:

   ```go
   client: &http.Client{
       Timeout:   15 * time.Second,
       Transport: retryTransportFor(apiBase, apiMetrics.Transport(http.DefaultTransport)),
   },
   ```

   (`Timeout: fetchTimeout` in `pkg/maintainerconfig`.) In the four **production** constructors there is no `apiBase` variable — pass the literal `"https://api.github.com"` there, and `apiBase` only in the `*WithBase` seams. The counters transport goes **inside** the retry transport, so every wire attempt is counted rather than one per logical call — a retried 429 is three real calls against the quota. To make that possible, change the helper's signature to `retryTransportFor(apiBase string, inner http.RoundTripper) http.RoundTripper` and pass `inner` to `NewRoundTripperRetryWithBackoff` instead of `http.DefaultTransport`. Keep the helper's host logic (always `api.github.com`, plus the `apiBase` host when it differs) exactly as it is.

3. **Wire the registry and the cluster flag in `main.go`.** Add a flag to the application struct beside the existing GitHub App fields:

   ```go
   Cluster string `required:"false" arg:"cluster" env:"CLUSTER" usage:"Cluster name stamped on the github_api_* counters' cluster label" default:"nuke"`
   ```

   In `Run`, immediately after `registry := prometheus.NewRegistry()`, build the recorder and pass it to `factory.CreateAgentProvider`:

   ```go
   apiMetrics := githubapimetrics.New(registry, githubapimetrics.Credential(a.AppID), a.Cluster)
   ```

4. **Wire `cmd/run-task` the same way.** It has its own `AppID` field. Build a recorder on a throwaway `prometheus.NewRegistry()` with the cluster constant `local` and `githubapimetrics.Credential(a.AppID)` (the CLI mode pushes nothing — the recorder exists so the client path is identical to production) and pass it into `factory.CreateAgentProvider`. Do not add a `Cluster` flag here; nothing reads it.

5. **Update `pkg/factory/factory.go`.** `CreateAgent` and `CreateAgentProvider` gain the `apiMetrics githubapimetrics.Metrics` parameter and pass it to `githubchangelog.NewHTTPFetcher`, `maintainerconfig.NewHTTPFetcher`, `githubtags.NewHTTPTagsFetcher` and `githubreview.NewHTTPClient`. Factory functions stay pure composition — no conditionals, no I/O.

6. **Update every call site the compiler flags** — including the per-package `export_test.go` aliases (`pkg/githubchangelog/export_test.go` and `pkg/maintainerconfig/export_test.go` both bind `NewHTTPFetcherForTest`, plus `NewHTTPTagsFetcherForTest` and `NewHTTPClientForTest`; all are `var` bindings, so the new parameter propagates to their existing test callers), the tests in `main_test.go`, `cmd/run-task/main_test.go` and `pkg/factory/factory_test.go`, and the compile-time `factory.CreateAgent` assertion in `pkg/steps_planning_test.go`. **The diff is expected to be large** — roughly a hundred test call sites across the four packages — and that is the change, not scope creep: do not shrink it by adding a second constructor that skips the recorder, which would leave a production path with no counters. In tests, pass `githubapimetrics.New(prometheus.NewRegistry(), "test", "test-cluster")` — never a shared global registry, which panics on the second `New`.

7. **Tests.** Add a suite for `pkg/githubapimetrics` in the repo's Ginkgo v2 / Gomega style (external test package, `suite_test.go` with `time.Local = time.UTC` as the siblings do), covering at least:
   - `Bucket` table: `/search/issues` → search; `/api/v3/search/issues` → search; `/graphql` and `/api/graphql` → graphql; `/repos/o/r/tags` → core; `/rate_limit` → core; `/search` without trailing slash → core; `/repos/o/search/x` → core; `/graphql/x` → core; a query string does not change the bucket; nil request → core.
   - **The interleaved test, which is the point of the change:** a search request, then a core request whose response carries `X-RateLimit-Resource: core`, then another search request, asserting `search` counted exactly 2 and `core` exactly 1. A response-derived implementation fails this; a single isolated search request does not prove it.
   - A response header contradicting the request path does not move the call to another bucket.
   - Every bounded combination is present at 0 before any request.
   - A transport error counts one `error`; a `429` counts one `error` plus one `unclassified` rate-limited; a `403` with `X-RateLimit-Remaining: 0` and a `403` with `Retry-After` each count one `unclassified` rate-limited; a plain `403` carrying neither header counts one `error` and no rate-limited.
   - `Credential` table: `0` and `-1` → `anonymous`; `123456` → `"123456"`.
   - One flag-contract check in `main_test.go`: run the binary built by the existing `gexec.Build` spec with `--cluster nuke-dev` and assert it fails on the missing required task input — the output must contain `task-content` and must not contain `flag provided but not defined` or `unknown flag`. The `arg`/`env` tags are reflection-read, so a wrong tag fails silently at runtime. The existing spec discards the built binary's path; hoist it to a package-level variable in a `BeforeSuite` so the check can run it.
   - One name/label contract check: call `registry.Gather()` and assert both family names are present with exactly the expected label keys. The pre-init test exercises label arity through `WithLabelValues`, not the metric names the cross-service query depends on.
   - One **wiring** test in `pkg/githubchangelog`: build the client through `NewHTTPFetcherForTest` against an `httptest.Server` that returns `200`, fetch, and assert the gathered sample's full label set is `service="github-releaser-agent"`, `credential="test"`, `bucket="core"`, `outcome="success"`, `cluster="test-cluster"` with value 1 — this proves the transport is installed on the client the production constructor builds **and** that the label values the cross-service query joins on are the values the code emits, which the package-level tests alone do not.

8. **`CHANGELOG.md` and `README.md`** — one `## Unreleased` bullet describing the counters in `CHANGELOG.md` (if no `## Unreleased` section exists, create it directly above the newest released `## vX.Y.Z` heading; never rename or renumber a released heading), and one `CLUSTER` row in the README's `## Configuration` env-var table — `docs/dod.md` requires the README to track configuration changes.

9. Before finishing, re-run `<verification>` and confirm it passes; then walk each numbered requirement against the change and report any that is unmet.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Blocked on the sibling prompt `018-wire-retry-roundtripper.md` (branch `feat/wire-retry-roundtripper`): approve and run this prompt only after that prompt has merged and this branch is rebased onto it — on the current tree the retry wiring is absent (`github.com/bborbe/http v1.28.0 // indirect`, no `retryTransportFor` helper anywhere)
- Do not add a counterfeiter mock for `githubapimetrics.Metrics` — `make precommit`'s `generate` target wipes and regenerates `mocks/`, which is outside the files this change may touch. Tests use a real `New(...)` on a fresh registry.
- Existing tests must still pass, with their assertions unchanged
- Do not change request building, pagination, header sets, error wording, sentinel errors (`ErrNoTags`, `ErrTagNotFound`, `ErrFileNotFound`) or any exported signature other than the constructor and factory parameters named above
- Do not remove or weaken the retry wiring — the counters transport goes inside it, never replaces it
- No token, PEM, installation token or any other secret may reach a label: `credential` is the App id or `anonymous`
- Use `github.com/bborbe/errors` with context wrapping — no `fmt.Errorf`, no bare `return err`
- Add no dependency; `github.com/prometheus/client_golang` is already a direct dependency
- Do not touch any file outside `pkg/githubapimetrics/`, the four client packages, `pkg/factory/`, `main.go`, `cmd/run-task/`, `CHANGELOG.md`, `README.md`, `pkg/steps_planning_test.go` and the test files beside them
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` -- must pass.

Then `go test -mod=mod ./pkg/githubapimetrics/...` -- must pass. (`make precommit` runs network-backed scanners whose failure can be swallowed inside the container; this second command cannot.)
</verification>

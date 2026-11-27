---
status: failed
created: "2026-11-27T09:59:40Z"
queued: "2026-11-27T09:59:40Z"
completed: "2026-11-27T10:00:50Z"
lastFailReason: |-
    setup workflow: git merge origin default branch: merge origin/master: error: Your local changes to the following files would be overwritten by merge:
      prompts/cancelled/017-planning-containment-guard.md
    Merge with strategy ort failed.: git merge origin/master failed: exit status 2
---

<summary>
- A rate-limit response asking for a long wait now returns promptly instead of stalling the client's timeout and failing with a context-deadline error
- The retry transport defers any wait longer than ten seconds, so the caller sees the real 403 or 429 and its next cycle can retry
- Short waits are still slept and retried exactly as before
- A plain permission 403 is still not retried
- Tests pin both behaviours, so neither can regress silently
</summary>

<objective>
Bound the retry transport's per-attempt wait below the HTTP client's timeout, so a long server-requested backoff defers instead of hanging until the client times out.
</objective>

<context>
Read CLAUDE.md for project conventions.

The four `retryTransportFor` helpers added in the previous change build `libhttp.RetryBackoffOptions{RetryHosts: hosts}` with every other field left at its zero value. `MaxWait` therefore defaults to five minutes.

Each client is built with `Timeout: 15 * time.Second` (`fetchTimeout` in `pkg/maintainerconfig`). That timeout covers the whole call, including the sleep the retry transport performs inside `RoundTrip`. So a response asking for a wait longer than roughly fourteen seconds — GitHub's typical secondary-limit `Retry-After` is 60 seconds — makes the transport sleep until the client's deadline fires, and the call fails with a wrapped `context deadline exceeded` instead of returning the rate-limit response. Measured against `bborbe/http@v1.28.1`: `Retry-After: 60` with `Timeout: 15s` returns after 15.001s with a nil response and that error.

`RetryBackoffOptions.MaxWait` exists for exactly this: a wait greater than `MaxWait` is deferred — the library logs `retry deferred wait=<d>` and returns the response untouched, so the caller sees the real 403/429 immediately and can retry on its next cycle.

The four helpers are at `pkg/githubtags/tags.go`, `pkg/githubchangelog/fetcher.go`, `pkg/githubreview/client.go` and `pkg/maintainerconfig/fetcher.go`.
</context>

<requirements>
1. In each of the four `retryTransportFor` helpers, add `MaxWait: 5 * time.Second` to the `RetryBackoffOptions` literal, so the field set becomes `libhttp.RetryBackoffOptions{RetryHosts: hosts, MaxWait: 5 * time.Second}`. Add `time` to the imports where it is not already present.

   Five seconds, not ten: the client timeout is fifteen seconds and must also cover the request itself plus any short retry, so the bound leaves ten seconds of headroom rather than five.

   Extend the helper's doc comment with one sentence stating why: a wait longer than this would otherwise exceed the client's 15-second timeout and surface as a context-deadline error rather than as the rate-limit response.

2. In each of the four packages, extend the retry test file with two cases beside the existing one:

   a. **Long wait defers.** Stub returns `429` with `Retry-After: 60` on every request. Assert the call returns an error, that the error text contains `status 429` (so an unrelated failure cannot satisfy the test), that **exactly one** request reached the server (the retry never fired), and that the call returned in well under the client timeout — assert elapsed time is less than five seconds, so a regression to sleeping the full wait fails the test rather than just running slowly.

   b. **Plain 403 is not retried.** Stub returns `403` with no `Retry-After` and no `X-RateLimit-Remaining`. Assert the call returns an error whose text contains `status 403`, and that **exactly one** request reached the server.

   Keep the existing `Retry-After: 1` → two-requests case unchanged. Use the same Ginkgo v2 / Gomega style and the same exported test alias already used in that file.

3. Do not change any production behaviour other than the added `MaxWait` field: no signature changes, no change to `RetryHosts`, no change to the client timeouts, no change to error wording or sentinel errors.

4. Update the `## Unreleased` bullet in `CHANGELOG.md` so it no longer claims a retry honours any wait the response asks for. It must state that a wait longer than five seconds is deferred so the caller sees the rate-limit response promptly, and that shorter waits are slept and retried.

5. Before finishing, re-run `<verification>` and confirm it passes; then walk each numbered requirement against the change and report any that is unmet.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must pass unchanged
- Do not touch any file outside the four packages named above and `CHANGELOG.md`
- Do not raise or remove the client `Timeout` values — the bound belongs on the retry wait, not on the client
- Do not add a dependency
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` -- must pass.
</verification>

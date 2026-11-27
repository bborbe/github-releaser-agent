---
status: draft
---

<summary>
- The retry transport's total sleep budget now fits inside the HTTP client's timeout, not just a single wait
- A repeated short Retry-After no longer accumulates past the client deadline and surfaces as a context-deadline error
- Long waits still defer, so the caller sees the real 403 or 429 promptly
- Short waits are still slept and retried
- A test pins the accumulation bound so a regression to the larger value fails
</summary>

<objective>
Lower the retry transport's per-wait bound so the worst-case total sleep across all retries stays inside the client timeout.
</objective>

<context>
Read CLAUDE.md for project conventions.

Four packages each carry an identical `retryTransportFor(apiBase)` helper whose options are `libhttp.RetryBackoffOptions{RetryHosts: hosts, MaxWait: 5 * time.Second}`:
- `pkg/githubtags/tags.go`
- `pkg/githubchangelog/fetcher.go`
- `pkg/githubreview/client.go`
- `pkg/maintainerconfig/fetcher.go`

All four clients use `Timeout: 15 * time.Second`.

⚠️ **`MaxWait` bounds a single wait, not the call.** `MaxAttempts` defaults to 5, so the worst case is 5 × `MaxWait` of sleeping inside one `http.Client.Do` — with `MaxWait: 5s` that is **25 seconds**, against a 15-second timeout. A server sending a repeated short `Retry-After` therefore still exhausts the deadline and surfaces `retry wait cancelled: context deadline exceeded` with a nil response, which is the failure `MaxWait` was added to prevent. This shipped in `v0.11.5`.
</context>

<requirements>
1. In all four `retryTransportFor` helpers, change `MaxWait: 5 * time.Second` to **`MaxWait: 1 * time.Second`**. Five retries at one second is a five-second worst case, leaving ten of the fifteen seconds for the requests themselves.

2. Extend each helper's doc comment so it states the budget arithmetic rather than only the per-wait rule — that `MaxAttempts` (default 5) multiplies the wait, so `MaxWait` is chosen so `MaxAttempts × MaxWait` stays inside the 15-second client timeout.

3. Add one test per package proving the **accumulation** bound, beside the existing retry test:
   - stub returns `429` with **`Retry-After: 4`** on every request;
   - assert the call returns an error whose text contains `status 429` (this repo's wording is `status %d`), that it does **not** contain `context deadline exceeded`, that **exactly one** request reached the server, and that elapsed time is under five seconds.

   Four seconds is the discriminating value: at the old `MaxWait: 5s` it is slept once per attempt (5 × 4s = 20s, past the 15s timeout, so the call dies with a deadline error); at the new `MaxWait: 1s` it exceeds the bound and is deferred immediately, so one request returns the real 429. A `Retry-After: 1` stub does **not** discriminate — five of those total 5s and pass at either bound.

4. Do not change `RetryHosts`, the client timeouts, the dependency version, error wording, or any exported signature. Do not touch the existing short-retry or plain-403 cases.

5. Update the `## Unreleased` bullet in `CHANGELOG.md` so it states the bound in terms of the total retry budget, not a single wait.

6. Before finishing, re-run `<verification>` and confirm it passes; then walk each numbered requirement against the change and report any that is unmet.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Only the four helpers, their doc comments, the four test files and `CHANGELOG.md` change
- Every other test must pass unchanged
- Add no dependency
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` -- must pass.
</verification>

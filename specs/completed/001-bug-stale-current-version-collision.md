---
status: completed
approved: "2026-07-21T16:30:46Z"
generating: "2026-07-21T16:32:44Z"
prompted: "2026-07-21T16:41:38Z"
verifying: "2026-07-21T17:32:59Z"
completed: "2026-07-21T19:56:59Z"
branch: dark-factory/bug-stale-current-version-collision
---

## Summary

The planning step bumps the next release version from a `current_version`
value snapshotted at **task-emit time** by the upstream watcher. When the
target repo is tagged between emit and run (e.g. a second release fires for a
different `## Unreleased` item), the snapshot is stale: planning computes a
version that already exists on the remote, the execution step's `git tag`
push is rejected, the verdict flips to `superseded`, and the repo silently
drops its release. Fix: resolve `current_version` from the repo's actual
latest remote tag at plan time, falling back to the emit-time snapshot only
when the tag lookup yields nothing.

## Reproduction

Observed during the 2026-07-19 x/text CVE fleet sweep. 35 of 37 merged repos
released cleanly; **vault-cli** and **jira-task-creator** never cut a tag
despite valid config (`## Unreleased` populated, `autoRelease` on, ruleset +
releaser bypass in place).

Concrete sequence for vault-cli:

1. Watcher emits a release task with `current_version: v0.101.0` (the latest
   tag at emit time).
2. Before the task runs, `v0.101.1` is cut for vault-cli's *other* Unreleased
   item (a separate release path advances the remote).
3. The planning step reads `current_version: v0.101.0` from frontmatter
   (`pkg/steps_planning.go` `readRequired`, line 655), classifies the bump as
   `patch`, and `semver.BumpVersion("v0.101.0", "patch")` → `v0.101.1`.
4. Execution runs `git tag v0.101.1` and pushes → remote already has that tag
   at `15ac249` → push rejected.
5. Post-check `git ls-remote refs/tags/v0.101.1` finds the tag → verdict
   upgraded to `superseded`. No release for the pending Unreleased item.

Observed evidence (verbatim): releaser pod
`github-releaser-agent-7ba3abc8-...cs5sz` (2026-07-19) — planned `v0.101.1`,
remote already at `15ac249`, push rejected `tag already exists`. OpenClaw
tasks `Release bborbe-vault-cli 654f684` / `5c122e4` closed `superseded`.

## Expected vs Actual

| | |
|---|---|
| **Expected** | Planning resolves the *current* latest tag from the remote at plan time, so a repo tagged between emit and run bumps from the true latest (`v0.101.1` → `v0.101.2`) and cuts the correct next tag. |
| **Actual** | Planning bumps from the emit-time frontmatter snapshot (`v0.101.0` → `v0.101.1`), collides with the tag cut meanwhile, and the release is dropped as `superseded`. |

## Why this is a bug

The release contract is "cut the next version above the repo's current latest
tag." `current_version` is meant to name that latest tag, but it is captured
at emit time and never re-validated against the remote at the moment the bump
is computed — so the invariant "next version > every existing tag" can be
violated by any tag that lands in the emit→run window. The agent's own
`CHANGELOG.md` and `.maintainer.yaml` are already re-read from the remote at
plan time (`fetcher.Fetch`, `resolveMaintainerConfig`); `current_version` is
the lone plan input still trusting a stale snapshot.

## Goal

At planning entry, resolve `current_version` from the target repo's latest
remote tag (highest semver), and bump from that. The emit-time frontmatter
`current_version` becomes a fallback used only when the remote lookup returns
no usable tag or fails transiently — never the primary source when the remote
has tags.

## Constraints

- **Resolver mechanism: GitHub REST tags API**, mirroring the existing
  `pkg/githubchangelog` pure-API fetcher pattern (`Fetch(ctx, owner, repo,
  ref)` interface, counterfeiter mock, GitHub App IAT bearer token, ~15s
  client timeout). Planning is pure-API today — it does **not** clone or shell
  git. Do not introduce `git ls-remote` / a clone into the planning step.
- **Fallback semantics mirror `resolveMaintainerConfig`** (spec 059 pattern):
  - Remote has tags → use the highest semver tag as `current_version`.
  - Remote has zero tags (fresh repo, `ErrNoTags` / 404-shaped) → fall back to
    the frontmatter snapshot cleanly, log `V(2)`, no warning.
  - Transient fetch error (5xx / network) → fall back to the frontmatter
    snapshot, surface a non-fatal warning on the `## Plan` block (reuse the
    `ConfigFetchWarning` surfacing convention) so the operator can grep it.
    Do **not** fail-closed on transport errors.
- **Prefix preservation**: the resolved version keeps the repo's existing tag
  prefix style (`v0.101.1` vs `0.101.1`) so `NextVersionHeader` /
  `HeaderPrefixStyle` are unaffected. If the frontmatter snapshot and the
  remote latest disagree on prefix, the remote tag's own string wins.
- **Semver selection**: "latest" = highest semver across the repo's tags, not
  the most recently created tag (creation order is not semver order). Reuse /
  extend `pkg/semver` for the comparison; non-semver tag names are skipped,
  not errored.
- **Escalation contract preserved**: a malformed frontmatter `current_version`
  that is *also* not recoverable from the remote still escalates
  (`NeedsInput` / `previous_assignee: github-releaser-agent`) exactly as
  today — the fix must not turn an escalation into a silent default.
- **No new write path**: the resolver is read-only (GitHub GET). Git-write
  safety (CHANGELOG-only commit, deterministic Go tag/push) is untouched.
- **Errors** use `github.com/bborbe/errors`; **logging** is `glog` `V(n)`.

## Acceptance Criteria

- [x] A new read-only tags fetcher (e.g. `pkg/githubtags`) exposes an
      interface returning the target repo's latest semver tag, backed by the
      GitHub REST tags API, with a counterfeiter mock — mirroring
      `pkg/githubchangelog`. Evidence: `grep -n counterfeiter pkg/githubtags/*.go`
      returns ≥1; the interface method is present.
- [x] `steps_planning.go` resolves the effective `current_version` from that
      fetcher at plan entry (before `semver.BumpVersion`), using the
      frontmatter snapshot only on no-tags / transient-error fallback.
      Evidence: covered by AC #3 (the observable is the computed next version).
- [x] Given a repo whose remote latest tag is `v0.101.1` and a task
      frontmatter `current_version: v0.101.0`, planning computes the bump from
      `v0.101.1` (→ `v0.101.2` for a patch), NOT from `v0.101.0`. Covered by a
      Ginkgo test with a fake tags fetcher + fake `ClaudeRunner`.
- [x] No-tags fallback: a fake fetcher returning "no tags" makes planning use
      the frontmatter snapshot with no warning (`V(2)` log only). Test present.
- [x] Transient-error fallback: a fake fetcher returning a 5xx/transport error
      makes planning use the snapshot AND surface a non-fatal warning on the
      `## Plan` block. Test present.
- [x] The escalation path for a genuinely-missing `current_version` (empty
      frontmatter AND no remote tags) is unchanged — still `NeedsInput` with
      `previous_assignee: github-releaser-agent`. Test present.
- [x] `make precommit` passes (fmt, generate, test, lint, vet, vuln, license).
- [x] `CHANGELOG.md` has an entry under `## Unreleased` describing the
      plan-time version resolution fix.

## Failure modes the fix must cover

| Trigger | Expected behavior | Detection / Recovery |
|---|---|---|
| Repo tagged in the emit→run window (headline bug) | Bump from the remote latest tag → correct next version | `## Plan` shows next > remote latest; tag cut, not `superseded` |
| Fresh repo, zero tags | Plan from the frontmatter snapshot, no crash | `V(2)` log "no remote tags — using snapshot"; no `## Plan` warning |
| Transient GitHub 5xx / network | Degrade to snapshot, do NOT fail-closed | Non-fatal warning on `## Plan` block; operator greps the warning line |
| Non-semver tags alongside semver tags | Skip non-semver, select highest semver | Ginkgo test with mixed tag list asserts the chosen version |
| Repo with >100 tags (e.g. vault-cli at `v0.101.x`) — highest tag on page 2+ | Paginate the tags API (follow `Link` `rel="next"`); select the cross-page maximum | Ginkgo test: highest tag served on page 2 is still selected |
| Empty frontmatter `current_version` AND no remote tags | Still escalates (unchanged) | `## Plan` outcome `needs_input`; `previous_assignee: github-releaser-agent` |

## Suggested Decomposition

| # | Prompt focus | Covers ACs | Depends on |
|---|---|---|---|
| 1 | `pkg/githubtags` fetcher (GitHub REST tags API) + counterfeiter mock + unit tests (latest-semver selection, no-tags, transport-error, non-semver skip) | #1 | — |
| 2 | Wire the resolver into `steps_planning.go` at plan entry + fallback semantics + Ginkgo tests for the four planning branches | #2, #3, #4, #5, #6 | 1 |
| 3 | `CHANGELOG.md` `## Unreleased` entry + `make precommit` green | #7, #8 | 2 |

## Verification

Bug verification per bug-workflow.md: replay the reproduction. Deploy the
fixed image to dev, drive a release task whose frontmatter `current_version`
is behind the remote latest tag (synthetic repo or a re-emit for
vault-cli/jira-task-creator), and confirm planning computes the next version
above the *remote* latest and the tag is cut — not dropped as `superseded`.
Landing vault-cli `v0.101.2` and jira-task-creator `v0.5.1` (their held x/text
bumps) is the real-world regression evidence.

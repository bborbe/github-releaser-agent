# Definition of Done

After completing your implementation, review your own changes against each criterion below. These are quality checks you perform by inspecting your work — not commands to run (linting and tests already ran via `testCommand`). Report any unmet criterion as a blocker.

## Code Quality

- Exported types, functions, and interfaces have doc comments
- Error handling uses `github.com/bborbe/errors` with context wrapping — no `fmt.Errorf`, no bare `return err`
- No debug output (`fmt.Print*`, `println`) — use `glog` with `V(n)`-gated `Info` lines
- Factory functions are pure composition — no conditionals, no I/O, no `context.Background()`
- Follow the Interface → Constructor → Struct → Method pattern
- `context.Context` is threaded through every IO call; no `context.Background()` outside `main`/tests

## Git-Write Safety (load-bearing — this repo pushes to protected branches)

- The git writes (rewrite header, commit, tag, push) stay **deterministic Go** — the Claude/LLM step only ever *classifies* the bump, never authors the commit
- Commits touch **`CHANGELOG.md` only** via an explicit pathspec — never `git add -A` / `git add .`
- No new code path lets the agent write any file other than `CHANGELOG.md` during a release
- Escalation contract preserved: a planning step that cannot proceed returns `NeedsInput`/`human_review` (assignee cleared, `previous_assignee: github-releaser-agent`) — never auto-advances to `done`

## Testing

- New code has good test coverage (target >= 80%)
- Changes to existing code have tests covering at least the changed behavior
- Tests use Ginkgo v2 / Gomega with Counterfeiter mocks (`mocks/` dir)
- LLM-dependent steps are tested with a fake `ClaudeRunner` returning canned verdicts — no live Claude calls in tests
- Prompt/parser changes update the verdict-parse tests (`pkg/prompts/`)

## Build

- `make precommit` passes (fmt, generate, test, lint, vet, vuln, license)
- No `exclude` or `replace` directives in `go.mod` (break remote install of the shared `github.com/bborbe/maintainer` lib)
- The image still builds: `VERSION=vX.Y.Z make buca` produces `docker.io/bborbe/github-releaser-agent:vX.Y.Z` (only when a runtime change warrants a manual check)

## Documentation

- README.md is updated if the change affects run modes, configuration, or the release/bump behavior
- CHANGELOG.md has an entry under `## Unreleased`. If that section does not exist yet, create it **below** the preamble block and **above** the newest `## vX.Y.Z` section — never between the `# Changelog` title and the preamble. The final order is always: `# Changelog` → preamble → `## Unreleased` → `## vX.Y.Z` (newest first)
- A change to the bump-classification rules (`pkg/prompts/bump_classification.md`) is mirrored in any doc that states the rules, so prompt and docs never drift

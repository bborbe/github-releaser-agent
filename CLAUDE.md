# CLAUDE.md

Autonomous release agent — given a repo with a non-empty `## Unreleased` block in `CHANGELOG.md`, it classifies the semver bump (LLM), then deterministically rewrites the header, commits, tags, and pushes the release in Go.

## Dark Factory Workflow

The headline reason to use prompts/specs: **safe unattended execution** inside a YOLO Claude container, sandboxed from the host. Queue work, step away, come back to commits — no permission interruptions.

### Choosing a Flow

**Canonical guide: `~/Documents/workspaces/dark-factory/docs/choosing-a-flow.md`** — read it, don't second-guess from memory. 30-second decision:

1. Is this code that runs in build / production / CI? No → **Direct** (edit by hand, no dark-factory). Markdown, config, yaml land here.
2. Yes — does the change carry a business-level "why" worth a permanent in-repo document? No → **Prompt**. Yes → **Spec → prompts**.

### Complete Flow

**Spec-based (multi-prompt features):**

1. Create spec → `/dark-factory:create-spec`
2. Audit spec → `/dark-factory:audit-spec`
3. User confirms → `dark-factory spec approve <name>`
4. dark-factory auto-generates prompts from spec (`autoGeneratePrompts: true`)
5. Audit prompts → `/dark-factory:audit-prompt`
6. User confirms → `dark-factory prompt approve <name>`
7. Start daemon → `dark-factory daemon` (use Bash `run_in_background: true`)

**Standalone prompts (simple changes):**

1. Create prompt → `/dark-factory:create-prompt`
2. Audit prompt → `/dark-factory:audit-prompt`
3. User confirms → `dark-factory prompt approve <name>`
4. Start daemon → `dark-factory daemon` (use Bash `run_in_background: true`)

### Claude Code Commands

| Command | Purpose |
|---------|---------|
| `/dark-factory:create-spec` | Create a spec file interactively |
| `/dark-factory:create-prompt` | Create a prompt file from spec or task description |
| `/dark-factory:audit-spec` | Audit spec against preflight checklist |
| `/dark-factory:audit-prompt` | Audit prompt against Definition of Done |
| `/dark-factory:verify-spec` | End-to-end verify a spec, then mark complete |

### CLI Commands

| Command | Purpose |
|---------|---------|
| `dark-factory spec approve <name>` | Approve spec (inbox → queue, triggers prompt generation) |
| `dark-factory prompt approve <name>` | Approve prompt (inbox → queue) |
| `dark-factory daemon` | Start daemon (watches queue, executes prompts) |
| `dark-factory run` | One-shot mode (process all queued, then exit) |
| `dark-factory status` | Combined status of prompts and specs |
| `dark-factory prompt cancel <name>` | Cancel a running/queued prompt (never `docker kill`) |

### Key rules

- Prompts go to **`prompts/`** (inbox) — never `prompts/in-progress/` or `prompts/completed/`
- Specs go to **`specs/`** (inbox) — never `specs/in-progress/` or `specs/completed/`
- Never number filenames — dark-factory assigns numbers on approve
- Never manually edit frontmatter status — use the CLI commands above
- Always audit before approving; always `/dark-factory:verify-spec <id>` before completing
- **Spec-linked prompts are daemon-generated** — after `spec approve`, wait for the `dark-factory-gen-<spec>` container; never hand-write prompts for an approved spec
- **BLOCKING: never run `prompt approve`, `spec approve`, or `daemon` without explicit user confirmation.** Write the prompt/spec, then STOP and ask.
- **Before starting the daemon** — run `dark-factory status` first; the daemon does not exit when the queue drains, so kill it once `Queue: 0`

## Development Standards

Follows the [coding-guidelines](https://github.com/bborbe/coding-guidelines). Go 1.26, vendored.

### Build and test

- `make precommit` — fmt, generate, test, lint, vet, vuln, license
- `make test` — tests only
- `VERSION=vX.Y.Z make buca` — build + push `docker.io/bborbe/github-releaser-agent:vX.Y.Z`, then apply

### Test conventions

- Ginkgo v2 / Gomega; Counterfeiter mocks (`mocks/`); external test packages (`*_test`)
- LLM steps are tested with a fake `ClaudeRunner` returning canned verdicts — no live Claude calls

## Architecture

Standalone binary; the shared lib is imported from `github.com/bborbe/maintainer` (Helm chart + deploy model live there). The upstream producer of release tasks is `bborbe/github-release-watcher`.

- `main.go` — Kubernetes Job entry (env-driven; `/main` in the image). Production path, Kafka/CQRS result delivery.
- `cmd/run-task/` — local CLI entry (flag-based; file I/O instead of Kafka), for local runs/debugging.
- `pkg/steps_planning.go` — planning step: classify bump (LLM) → compute next version → major-bump guard → publish `## Plan`.
- `pkg/steps_execution.go` — execution step: deterministic CHANGELOG rewrite + commit + tag + push.
- `pkg/steps_ai_review.go` — AI review + push gating.
- `pkg/prompts/` — embedded Claude prompts (`bump_classification.md`, rewrite, faithfulness) + typed verdict parsers.
- `pkg/changelog/` — `## Unreleased` validate / extract / rewrite.
- `pkg/semver/` — bump-version arithmetic.
- `pkg/git/` — deterministic git ops (CHANGELOG-only commit, tag, push).
- `pkg/githubauth/` — GitHub App auth (bypass actor on protected branches).
- `pkg/githubchangelog/` — fetch `CHANGELOG.md` via GitHub contents API.
- `pkg/maintainerconfig/` — parse target repo's `.maintainer.yaml` (`autoRelease`, `allowMajorBump`).
- `pkg/factory/` — wire dependencies (pure composition).

## Key Design Decisions

- **LLM only classifies; git writes are deterministic Go.** The Claude step returns a bump verdict only. The rewrite/commit/tag/push is plain code — a broken LLM can never author a commit.
- **Commits touch `CHANGELOG.md` only**, via explicit pathspec. Never `git add -A` / `git add .`.
- **Escalation over guessing.** A planning step that cannot proceed returns `NeedsInput`/`human_review` (clear assignee, set `previous_assignee: github-releaser-agent`) — never auto-advances to `done`.
- **Major-bump guard** — a `major` verdict without `release.allowMajorBump: true` (or `--allow-major`) escalates instead of cutting a major. Decision table is spec-frozen in `pkg/steps_planning.go` `applyMajorBumpGuard`; changing it MUST update the upstream spec first.
- **Factory functions are pure composition** — no conditionals, no I/O, no `context.Background()`.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` with `V(n)`-gated `Info`.

## Releasing

This repo is `release.autoRelease: true` (`.maintainer.yaml`) — it is released by an instance of itself post-merge. Keep `## Unreleased` bullets accurate, merge to master, let the bot tag. Do NOT hand-rename `## Unreleased` or `git tag`. Full procedure + deploy: [docs/releasing-github-releaser-agent.md](docs/releasing-github-releaser-agent.md).

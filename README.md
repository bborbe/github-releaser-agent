# Agent PR Reviewer

PR review agent. Takes a GitHub or Bitbucket Server PR URL, creates a local clone, runs Claude Code review inside a `claude-yolo` container, and posts the review back as a PR comment with a verdict (approve / request-changes).

**Internal architecture:** see [`docs/architecture.md`](docs/architecture.md) — three phases (planning → execution → review), where the verdict rubric lives, heuristic fallback, ai_review meta-verdict.

## Entry Points

| Binary | Location | Use Case |
|---|---|---|
| `run-task` | `cmd/run-task/main.go` | Local CLI — takes PR URL as positional arg, flag-based config |
| k8s job | `main.go` (service root) | Pattern B Job — env-driven, argument/v2 + service.Main, optional Kafka result delivery (planned, see task 2) |

## Local Usage

```bash
go run ./cmd/run-task https://github.com/owner/repo/pull/42
go run ./cmd/run-task -v --comment-only https://bitbucket.example.com/projects/FOO/repos/bar/pull-requests/42
```

Install:

```bash
make install   # builds ./cmd/run-task → $GOPATH/bin/run-task
```

## Configuration

`~/.config/maintainer/pr-reviewer.yaml` — see repo root [README.md](../../README.md) for the full schema.

Minimum:

```yaml
repos:
  - url: https://github.com/bborbe/maintainer
    path: ~/Documents/workspaces/maintainer
```

Environment variables:

| Var | Purpose |
|---|---|
| `GH_TOKEN` | GitHub token (PAT — **legacy**, being migrated to GitHub App, see [GitHub App Setup](#github-app-setup)) |
| `BITBUCKET_TOKEN` | Bitbucket Server bearer token |

## GitHub App Setup

The agent is being migrated from a Personal Access Token (user `pr-review-of-ben`, flagged "Spammy") to a GitHub App identity (`Ben's Pull Request Reviewer`). Apps are not subject to user-level spam classification, so their reviews remain visible via the REST `/reviews` API for auto-merge gates.

See [`docs/github-app-setup.md`](docs/github-app-setup.md) for App ID, Installation ID, PEM location, auth flow, and the `cmd/mint-iat` smoke-test workflow.

## How It Works

1. Parse PR URL → platform (GitHub / Bitbucket), owner/project, repo, PR number
2. Load `~/.config/maintainer/pr-reviewer.yaml`, find repo path
3. Fetch PR metadata (source + target branch) via GitHub / Bitbucket REST API
4. `git fetch` the local clone, then `git clone --local --branch <src>` into `/tmp/<pr-N>`
5. Pull the `claude-yolo` container image, mount the clone + `~/.claude-yolo` config
6. Run `/pr-review <target-branch>` inside the container
7. Parse JSON verdict from review output, strip verdict block from posted text
8. Post structured review (approve / request-changes) or plain comment based on verdict + `autoApprove`
9. Remove clone

## Smoke Test PR

**https://github.com/bborbe/maintainer/pull/2** — `test: delete-this-pr-never`

Permanent test fixture. Trivial diff (HTML comment added to `README.md`,
3 additions / 0 deletions). Use this PR for any local or k8s smoke test.
**Do not close, do not merge.**

Local smoke run (3-phase agent):

```bash
# Create task file with PR URL
cat > /tmp/pr-reviewer-smoke.md <<'EOF'
---
phase: planning
status: in_progress
---

Review https://github.com/bborbe/maintainer/pull/2 and return a verdict.
EOF

# Walk the 3 phases — task file is mutated in place between runs
go run ./cmd/run-task --task-file /tmp/pr-reviewer-smoke.md --phase planning
go run ./cmd/run-task --task-file /tmp/pr-reviewer-smoke.md --phase in_progress
go run ./cmd/run-task --task-file /tmp/pr-reviewer-smoke.md --phase ai_review
```

After the third run the file should contain `## Plan`, `## Review`, and
`## Verdict` JSON sections.

## Verdict Contract

Claude Code must emit a JSON block (fenced or bare) containing:

```json
{"verdict": "approve|request-changes", "reason": "<one-liner>"}
```

Fallback: heuristic section-header scan (`## Must Fix`, `## Blocking`). Horizontal rules (`---`) are not treated as must-fix content.

## Layout

```
agent/pr-reviewer/
├── cmd/run-task/      binary entry (local CLI, Pattern B Job)
├── pkg/
│   ├── bitbucket/     Bitbucket Data Center REST client
│   ├── config/        YAML config + home-dir expansion
│   ├── git/           worktree / clone manager
│   ├── github/        GitHub REST client via `gh` CLI
│   ├── prurl/         platform-agnostic PR URL parser
│   ├── review/        Docker-based claude-yolo reviewer
│   ├── verdict/       JSON verdict parser + markdown strip
│   └── version/       build-time version injection
├── mocks/             counterfeiter-generated test doubles
└── tools.go           build tools
```

## Related

- Repo root [README.md](../../README.md) — top-level project overview
- Goal [[Build Code Review Agent Pipeline]]
- Task [[Transform code-reviewer to multi-module layout]] (v0.9.0, original migration)
- Task [[Rename code-reviewer to maintainer]] (v0.24.0, current rename)

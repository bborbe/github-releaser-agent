# github-releaser-agent

Autonomous **release agent**. Given a repository with a non-empty `## Unreleased`
block in `CHANGELOG.md`, it classifies the semver bump, rewrites the header to
`## vX.Y.Z`, then commits, tags, and pushes the release — deterministically, in
Go (the Claude Code step only *classifies* the bump; the git writes are plain
code that touch `CHANGELOG.md` only).

Part of the `bborbe` agent-maintenance fleet: the shared library lives in
[`bborbe/maintainer`](https://github.com/bborbe/maintainer) (imported as
`github.com/bborbe/maintainer`), the Helm chart ships there too, and the
published image is `docker.io/bborbe/github-releaser-agent`. Extracted from the
former `bborbe/maintainer` monorepo (`agent/github-releaser`). The upstream
producer that emits release tasks is
[`bborbe/github-release-watcher`](https://github.com/bborbe/github-release-watcher).

## How it works

1. Read `CHANGELOG.md` and extract the `## Unreleased` bullets.
2. Classify the semver bump (major / minor / patch) from those bullets.
3. Compute the next version from the latest existing tag.
4. Rewrite `## Unreleased` → `## vX.Y.Z` in `CHANGELOG.md`.
5. Commit (`CHANGELOG.md` only — explicit path, never `git add -A`), tag
   `vX.Y.Z`, and push commit + tag to `master`.

On a protected branch the push works because the releaser's GitHub App is a
**bypass actor** in the repo's `master-protection` ruleset (see
[`bborbe/maintainer`](https://github.com/bborbe/maintainer) — safety model:
the git writes are deterministic Go, not the LLM, and can only ever land a
CHANGELOG + tag change).

## Run modes

| Mode | Entry | Use |
|---|---|---|
| Kubernetes Job | `main.go` (`/main` in the image) | Env-driven; spawned by the agent-task-executor from a Kafka release task. Production path. |
| Local CLI | `cmd/run-task` | Flag-based; for local runs / debugging. |

## Configuration

Env-driven (Kubernetes) — key variables:

| Var | Purpose |
|---|---|
| `APP_ID` / `INSTALLATION_ID` | GitHub App identity for the releaser (bypass actor on the target ruleset) |
| `PEM_KEY` | GitHub App private key (mounted from a Secret) |
| `REPO_ALLOWLIST` | Repos the agent may release (e.g. `github.com/bborbe/*,!github.com/bborbe/go-skeleton`) |

Per-repo opt-in is the target repo's `.maintainer.yaml` (`release.autoRelease: true`),
enforced upstream by the release watcher.

## Layout

```
.                    lib imported from github.com/bborbe/maintainer
├── main.go          Kubernetes Job entry (env-driven; /main in the image)
├── cmd/run-task/    local CLI
├── pkg/             CHANGELOG parse + rewrite, semver classify, git ops,
│                    GitHub App auth, bump plan/result output
└── helm chart + shared lib live in bborbe/maintainer
```

## Build

```bash
make precommit          # fmt, generate, test, lint, vet, vuln, license
VERSION=vX.Y.Z make buca # build + push docker.io/bborbe/github-releaser-agent:vX.Y.Z
```

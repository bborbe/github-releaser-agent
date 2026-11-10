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

1. Skip a task whose ref is already contained in the released tag — planning closes it as `completed` instead of re-planning the next version.
2. Read `CHANGELOG.md` and extract the `## Unreleased` bullets.
3. Classify the semver bump (major / minor / patch) from those bullets.
4. Compute the next version from the latest existing tag.
5. Rewrite `## Unreleased` → `## vX.Y.Z` in `CHANGELOG.md`.
6. Commit (`CHANGELOG.md` only — explicit path, never `git add -A`), tag
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

This agent does **not** read `REPO_ALLOWLIST`. Repo scope is enforced upstream by
`github-release-watcher` (which does read it) plus the installation-access-token
identity — see `pkg/git/error_classifier.go`, which omits a `repo_not_found` class
for exactly that reason. Setting `REPO_ALLOWLIST` on this agent has no effect.

Per-repo opt-in is the target repo's `.maintainer.yaml` (`release.autoRelease: true`),
enforced upstream by the release watcher.

A repo that also sets `dockerBuild.autoBuild: true` is expected to publish an image at
the released tag. After the pushed tag is confirmed on the remote, the `## Review` block
records `image_expected` / `image_exists` by probing the primary destination; a missing
image fails the `Image` check and parks the task at `human_review`. A repo that never
opted in, an empty `destinations` list, and an unreachable registry are not reported as a
missing image.

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

## License

BSD-style. See the [LICENSE](./LICENSE) file for details.

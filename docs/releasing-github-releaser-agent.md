# Releasing github-releaser-agent

How to ship a new version of the release agent. Read before any manual release step.

## One surface, one version stream

Unlike a plugin/tool, this repo ships a **single artifact**: the container image
`docker.io/bborbe/github-releaser-agent:vX.Y.Z`, versioned by the git tag `vX.Y.Z` and
the matching `## vX.Y.Z` section in `CHANGELOG.md`. There is **no plugin**, no
`.claude-plugin/`, no marketplace, no four-string version alignment. The tag is the
version; the image build pins to it.

## Self-release loop (the recursion)

This repo is opted into its own automation: `.maintainer.yaml` has
`release.autoRelease: true`. The **prod `github-releaser-agent`** (watching
`github.com/bborbe/*`) releases *this* repo the same way it releases every other —
an instance of the agent tags the agent. That is safe by construction: the git writes
are deterministic Go that only ever rewrite `CHANGELOG.md` + create a tag; the LLM step
only classifies the bump. A broken build can never push anything but a CHANGELOG + tag.

## Binary/image release — driver: github-releaser-agent (post-merge)

`.dark-factory.yaml` sets `autoRelease: false`, so the dark-factory daemon never tags on
a feature branch. The **only** release driver is the maintainer bot, post-merge:

1. You land commits on `master` carrying `## Unreleased` bullets in `CHANGELOG.md`.
2. The releaser classifies the semver bump from those bullets (`feat:` → minor,
   `fix:` → patch, breaking → major).
3. It rewrites `## Unreleased` → `## vX.Y.Z`, commits `release vX.Y.Z`, tags `vX.Y.Z`,
   pushes tag + commit to `master`.

Picks up within ~10 min of the merge. Force an immediate scan with
`/github-release-repo-trigger` (no arg — global scan).

**Your job in this flow:** keep `## Unreleased` bullets accurate, merge to master.
**Do NOT** rename `## Unreleased` → `## vX.Y.Z`, **do NOT** create a local tag — that
races the bot.

### Major-bump guard

A breaking-change bullet classifies as `major`. Unless the target repo's
`.maintainer.yaml` sets `release.allowMajorBump: true` (or the run passes
`--allow-major` / `ALLOW_MAJOR=true`), the planning step escalates to `human_review`
instead of cutting a major — the release will sit unreleased until a human signs off or
the bullet wording is softened to a non-breaking `feat`/`fix`. Keep this in mind when a
merged PR does not produce a tag.

## Verifying a release shipped

```bash
git fetch --tags
git describe --tags --abbrev=0                                # latest tag
git log "$(git describe --tags --abbrev=0)"..HEAD --oneline   # any commits beyond it
```

After a successful release both `git status` (clean) and
`git rev-list @{u}..HEAD --count` (zero) should hold.

## Deploy (image → cluster)

The tag alone does not deploy anything — it only makes the image buildable. This is a
**mirrored agent service** in the `bborbe` fleet; the shared lib + Helm chart live in
[`bborbe/maintainer`](https://github.com/bborbe/maintainer), and the running agent is
spawned by the agent-task-executor from a Kafka release task.

```bash
VERSION=vX.Y.Z make buca   # build + push docker.io/bborbe/github-releaser-agent:vX.Y.Z, then apply
```

Deploy specifics (version pin, dev vs prod, mirrored-agent apply) live on the service's
**Development Instructions** page — read it before deploying; recalled paths go stale
after monorepo → standalone splits. See the Development Guide "Deploy Mirrored Agent
Service" flow.

## GitHub Release (optional milestone)

The `vX.Y.Z` tag is sufficient for image builds and `git describe`. A **GitHub Release**
(Releases tab, notes, feed) is a separate, deliberate act — create one only to surface a
milestone, not for every internal tag:

```bash
TAG=$(git describe --tags --abbrev=0)
gh release create "$TAG" --target master --title "$TAG" \
  --notes "$(awk "/^## $TAG/,/^## v/" CHANGELOG.md | head -n -1)"
```

## See also

- `CLAUDE.md` — Dark Factory Workflow + architecture map + design constraints
- `docs/dod.md` — the Definition of Done the daemon validates each prompt against
- [`bborbe/maintainer`](https://github.com/bborbe/maintainer) — shared lib, Helm chart, deploy model

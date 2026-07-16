# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

- feat(planning): clamp a disallowed `major` bump down to `minor` instead of escalating to `human_review`. When major is not permitted (no `.maintainer.yaml` `release.allowMajorBump` and no `--allow-major`/`ALLOW_MAJOR` override), a would-be breaking release now ships as a minor — a release never stalls in `human_review` solely because a major bump is disallowed. Two layers: the bump-classification prompt is told at call time not to return `major` (soft guidance), and the planning code caps `major`→`minor` deterministically (hard guarantee). The pre-1.0 cap and the full range when major IS allowed are unchanged.
- fix(build): make `ROOTDIR` resolution git-optional (`git rev-parse … || $(CURDIR)`) in `Makefile.variables` + `Makefile.precommit`, so `make precommit` works inside a gitless container / git worktree — unblocks the dark-factory container preflight.

## v0.1.2

- fix(security): clear the precommit vulnerability baseline — bump Go 1.26.4 → 1.26.5 (GO-2026-5856, stdlib) and `golang.org/x/text` v0.38.0 → v0.40.0 (CVE-2026-56852); ignore the unfixable `golang.org/x/crypto/openpgp` advisory GO-2026-5932 (`VULNCHECK_IGNORE` + `.trivyignore`, package unmaintained by design).

## v0.1.1

- refactor: import the shared library from its new root module path `github.com/bborbe/maintainer` (was `github.com/bborbe/maintainer/lib`) and bump to `@v0.45.0`. The maintainer repo flattened `lib/` to its root to match the `bborbe/agent` layout. No behavior change.

## v0.1.0

- Extracted from the `bborbe/maintainer` monorepo (`agent/github-releaser`) into a standalone
  publish-only repository. Shared code now comes from the versioned
  `github.com/bborbe/maintainer/lib` module instead of a local `replace`. Builds and
  publishes `docker.io/bborbe/github-releaser-agent:<version>` via `make buca`.

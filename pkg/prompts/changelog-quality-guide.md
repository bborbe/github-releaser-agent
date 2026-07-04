---
tags:
  - guide
  - git
  - documentation
---

Guide for writing consistent, useful CHANGELOG.md entries in dark-factory projects.

## Goal

Produce changelog entries with conventional prefixes so dark-factory can determine the version bump automatically — no guessing from prose.

## File Structure

```markdown
# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## Unreleased

- feat: Add SpecWatcher to monitor specs/ for approved status changes
- fix: Remove stale Docker container before starting a new executor run

## v1.5.0

- feat: Add FuncRunner interface for executing functions with custom behavior
- fix: Fix WaiterUntil to handle equal times correctly
```

**Rules:**
- Preamble with SemVer explanation always present
- **Header is frozen**: everything from the start of the file to the FIRST `##` heading (the `# Changelog` title, the "All notable changes..." line, the SemVer link, and the MAJOR/MINOR/PATCH bullets) MUST NOT be moved, deleted, or have anything inserted above or inside it. Insert `## Unreleased` (or any version section) immediately AFTER the last header line — never before any header line. If the header is incomplete, restore it; never leave it partial.
- `## Unreleased` on feature branches — never a version number
- `## vX.Y.Z` on master — no date suffix
- Newest version first — `## Unreleased` goes directly above the highest `## vX.Y.Z`
- Flat list — no `### Added` / `### Fixed` categories

## Conventional Prefixes (REQUIRED)

Every `## Unreleased` entry must start with a conventional prefix:

| Prefix | Meaning | Version bump |
|--------|---------|-------------|
| `feat:` | New feature or capability | **Minor** (`vX.Y+1.0`) |
| `fix:` | Bug fix | Patch |
| `refactor:` | Code restructure, no behavior change | Patch |
| `test:` | Test additions or improvements | Patch |
| `docs:` | Documentation only | Patch |
| `chore:` | Dependency updates, build, tooling | Patch |
| `perf:` | Performance improvement | Patch |

dark-factory reads these prefixes to determine the version bump automatically. Any `feat:` entry → minor bump; everything else → patch bump.

## Entry Style

**Format:** `- <prefix>: <what> [context]`

**Be specific:**
- Name the exact type, function, command, or package touched
- Include versions for dependency updates
- Add brief context for non-obvious changes

## Anti-Patterns

❌ `- Add SpecWatcher` — missing prefix, bump detection fails
✅ `- feat: Add SpecWatcher to monitor specs/ for approved status changes`

❌ `- feat: update go and deps` — wrong prefix (chore), and too vague
✅ `- chore: Update Go from 1.25.5 to 1.26.0`
✅ `- chore: Update github.com/bborbe/errors to v1.5.2`

❌ `- fix: refactor` — what was refactored?
✅ `- refactor: Extract worktree cleanup to reduce cognitive complexity`

❌ `- test: add tests` — for what?
✅ `- test: Add processor test suite covering retry and failure paths (12 tests)`

❌ `- fix: fix bug` — which bug?
✅ `- fix: Fix NormalizeFilenames number conflict on non-standard filename format`

## Merge Conflicts in Unreleased

Multiple feature branches writing to `## Unreleased` will conflict. Resolution: keep both bullet lists, remove conflict markers.

```markdown
## Unreleased
- feat: Add SpecWatcher
- fix: Remove stale container before run
```

## Validation

- [ ] Every `## Unreleased` entry has a conventional prefix (`feat:`, `fix:`, etc.)
- [ ] `feat:` used only for genuine new features/capabilities
- [ ] Descriptions are specific — name types, functions, commands
- [ ] Dependency updates use `chore:` and include version numbers
- [ ] No vague entries (`fix: fix bug`, `chore: update deps`)
- [ ] Preamble present with SemVer link
- [ ] Newest version at top

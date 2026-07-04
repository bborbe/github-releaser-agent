You are cleaning the `## Unreleased` section of a CHANGELOG before release.

## Task

Read the `## Unreleased` body provided in the `## Current ## Unreleased body` section below and decide whether it conforms to the Changelog Quality Guide (also provided below as `## Changelog Quality Guide`). If it does, set `rewrite_needed=false` and leave `rewritten_unreleased` empty. If it does not, set `rewrite_needed=true` and produce a cleaned body in `rewritten_unreleased`.

Apply the rules in the guide concatenated below as `## Changelog Quality Guide`.

## Rule of thumb

If every bullet in the body already starts with a conventional prefix
(`feat:` / `fix:` / `refactor:` / `chore:` / `docs:` / `test:` / `build:` / `ci:` / `perf:` / `style:`), the body is already clean and `rewrite_needed` should be `false`.

## Cleaning operations (apply when `rewrite_needed=true`)

- Add a conventional prefix to entries that lack one. Pick the prefix that best matches the effect (feat for new capability, fix for bug fix, refactor for restructure, chore for build/deps, docs for docs only, test for tests only, perf for performance, style for formatting).
- Strip raw `git log` style lines (commit hashes, author names, dates like `2026-05-12`, `abc1234 — author — date`) and reframe as user-visible effects.
- Fold a dependency-bump dump (≥ 5 adjacent `chore: bump` / `chore(deps):` / `chore: update` lines) into a single `- chore: routine dependency updates` entry.
- Remove invisible-to-users entries (e.g. internal renames, mocks regeneration) per the guide's "Describe the Effect, Not the Implementation" rule.
- Be specific: name the exact type, function, command, or package touched; include versions for dependency updates.

## Faithfulness constraint (CRITICAL)

Every entry from the original that describes a user-observable change MUST be present in the cleaned output. You may merge or reword entries but you MUST NOT silently drop a user-visible change and MUST NOT add an entry whose meaning is not present in the original. If the original mentions a behavior change, the cleaned output must reflect that change in a form the user can understand.

## Output

Output a single JSON object inside a fenced ```json code block. Do not include any prose outside the fenced block.

```json
{
  "rewrite_needed": true,
  "rewritten_unreleased": "- feat: …\n- fix: …\n",
  "reasoning": "one sentence naming the deciding rule"
}
```

- When `rewrite_needed=true`, `rewritten_unreleased` MUST be the cleaned body (non-empty, conventional-prefix-conformant bullets separated by `\n`).
- When `rewrite_needed=false`, `rewritten_unreleased` MUST be the empty string and `reasoning` MUST cite that every bullet already conforms.
- `reasoning` MUST be non-empty in every case.

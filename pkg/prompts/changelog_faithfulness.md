# Audit a CHANGELOG rewrite for semantic faithfulness

You are auditing a CHANGELOG rewrite for semantic faithfulness. The
release agent captured the verbatim `## Unreleased` body at planning time,
then committed a new `## vX.Y.Z` body to the repo. Your job is to confirm
that the rewrite preserved every user-observable change, neither dropped
nor hallucinated any.

## Input

You will be given two blocks, in this order:

1. `## Original ## Unreleased body` — the verbatim text captured at
   planning time. This is the source of truth.
2. `## Final ## vX.Y.Z body` — the body that landed in the release commit.

## Task

For every line in the original that describes a user-observable change
(bullet entries; skip blank lines and pure comments), decide whether the
final body preserves the same meaning. The verdict per entry is one of:

- `present` — the meaning is preserved in the final body, even if the
  wording changed (re-ordering, capitalization, fixing a typo, or
  rephrasing all still count as `present`).
- `silent-drop` — the original entry is absent or unrecognizable in the
  final body. This is a release-quality failure.

Then scan the final body for any entries that are NOT derivable from the
original. Those are:

- `hallucinated` — the final body added a new bullet that is not implied
  by any original entry. This is a release-quality failure.

## Output

Output a single JSON object inside a fenced ```json code block. The
output MUST be valid JSON. Do not include any prose outside the fenced
block.

Schema:

```json
{
  "per_entry": [
    { "entry": "<verbatim original line>", "verdict": "present|silent-drop", "note": "one sentence" }
  ],
  "extras": [
    { "entry": "<verbatim final line>", "verdict": "hallucinated", "note": "one sentence" }
  ],
  "overall": "pass|fail"
}
```

Rules:

- `per_entry` carries one object per original user-observable change.
  Skip blank lines and pure comments in the original.
- `extras` carries one object per final-body entry that the LLM judged
  `hallucinated`. Leave empty when none.
- `overall` MUST be `pass` when every `per_entry.verdict == "present"`
  AND `extras` is empty. Otherwise `overall` MUST be `fail`.
- The `entry` string MUST be the verbatim line (whitespace included) so
  the caller can match it back to its source.

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package prompts holds the Claude bump-classification prompt (embedded
// from bump_classification.md) and a typed parser for Claude's JSON
// verdict response. Pure-Go leaf library: no IO beyond //go:embed, no
// third-party dependencies except github.com/bborbe/errors.
//
// The prompt text is the Phase 1 verbatim ruleset: ordered
// major -> minor -> patch with concrete trigger criteria. The parser
// tolerates the three real-world Claude output shapes seen in
// pr-reviewer's extractVerdict history: plain JSON, fenced JSON in
// prose, and JSON embedded inside arbitrary prose.
package prompts

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/bborbe/errors"
)

//go:embed bump_classification.md
var bumpClassificationPrompt string

// BumpClassificationPrompt returns the embedded Phase 1 bump-classification
// prompt. The string is non-empty and contains the Phase 1 priority-order
// rules (major -> minor -> patch). Callers feed this string to a Claude
// agent step alongside the CHANGELOG bullets to classify.
func BumpClassificationPrompt() string {
	return bumpClassificationPrompt
}

//go:embed changelog-quality-guide.md
var changelogQualityGuide string

// ChangelogQualityGuide returns the embedded Changelog Quality Guide text.
// It is concatenated into the planning prompt as the ruleset the LLM
// applies when producing `rewritten_unreleased`.
func ChangelogQualityGuide() string {
	return changelogQualityGuide
}

//go:embed changelog_rewrite.md
var changelogRewritePrompt string

// ChangelogRewritePrompt returns the LLM instructions for producing the
// rewrite verdict. The caller is responsible for concatenating
// ChangelogQualityGuide() and the verbatim ## Unreleased body onto the
// returned string before invoking Claude.
func ChangelogRewritePrompt() string {
	return changelogRewritePrompt
}

// BumpVerdict is the typed shape of Claude's JSON response to the
// bump-classification prompt. Bump is one of "patch" | "minor" | "major".
// Reasoning is a one-sentence justification from Claude.
type BumpVerdict struct {
	Bump      string `json:"bump"`
	Reasoning string `json:"reasoning"`
}

// ParseBumpVerdict extracts a BumpVerdict from Claude's raw output string.
// Three extraction strategies are tried in order:
//  1. Parse the trimmed input as a JSON object directly.
//  2. Strip leading/trailing ```json fences and parse the inner block.
//  3. Find the last balanced {...} block in the input and parse it.
//
// First success wins. After successful unmarshal, the verdict is
// validated: Bump must be one of patch|minor|major (case-sensitive),
// Reasoning must be non-empty.
//
// Errors are wrapped via github.com/bborbe/errors and always contain
// the literal substring "parse bump verdict" so callers can grep
// verdict-parse failures apart from clone/git failures.
func ParseBumpVerdict(ctx context.Context, claudeOutput string) (BumpVerdict, error) {
	trimmed := strings.TrimSpace(claudeOutput)

	var v BumpVerdict

	// Strategy 1: Parse the trimmed input as a JSON object directly.
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return validateVerdict(ctx, v)
	}

	// Strategy 2: Strip ```json fences (allow leading prose by also trying
	// after the first ```json marker; mirror pr-reviewer's TrimPrefix shape
	// for the simple case).
	stripped := strings.TrimSpace(strings.TrimSuffix(
		strings.TrimPrefix(strings.TrimPrefix(trimmed, "```json"), "```"),
		"```",
	))
	if err := json.Unmarshal([]byte(stripped), &v); err == nil {
		return validateVerdict(ctx, v)
	}

	// Strategy 3: Find the last balanced {...} block in the input.
	block, ok := lastJSONBlock(trimmed)
	if !ok {
		return BumpVerdict{}, errors.Errorf(ctx, "parse bump verdict: no JSON found")
	}
	if err := json.Unmarshal([]byte(block), &v); err != nil {
		return BumpVerdict{}, errors.Wrapf(ctx, err, "parse bump verdict: %s", block)
	}
	return validateVerdict(ctx, v)
}

// validateVerdict enforces the field-level invariants from spec 046
// Desired Behavior 9: Bump must be in {patch, minor, major}; Reasoning
// must be non-empty. On failure, returns a zero verdict + a wrapped
// error containing "parse bump verdict".
func validateVerdict(ctx context.Context, v BumpVerdict) (BumpVerdict, error) {
	switch v.Bump {
	case "patch", "minor", "major":
		// ok
	default:
		return BumpVerdict{}, errors.Errorf(
			ctx,
			"parse bump verdict: invalid bump value %q (want patch|minor|major)",
			v.Bump,
		)
	}
	if strings.TrimSpace(v.Reasoning) == "" {
		return BumpVerdict{}, errors.Errorf(ctx, "parse bump verdict: missing reasoning")
	}
	return v, nil
}

// RewriteVerdict is the typed shape of Claude's JSON response to the
// changelog-rewrite prompt.
//
//   - RewriteNeeded=true  → RewrittenUnreleased is the cleaned body (non-empty)
//   - RewriteNeeded=false → RewrittenUnreleased is the empty string
//
// Reasoning is always non-empty.
type RewriteVerdict struct {
	RewriteNeeded       bool   `json:"rewrite_needed"`
	RewrittenUnreleased string `json:"rewritten_unreleased"`
	Reasoning           string `json:"reasoning"`
}

// ParseRewriteVerdict extracts a RewriteVerdict from Claude's raw output.
// Uses the same three-strategy extraction as ParseBumpVerdict (plain JSON,
// fenced ```json block, last balanced {...} block). After unmarshal:
//
//   - Reasoning MUST be non-empty.
//   - When RewriteNeeded=true,  RewrittenUnreleased MUST be non-empty.
//   - When RewriteNeeded=false, RewrittenUnreleased MUST be empty.
//
// Errors are wrapped via github.com/bborbe/errors and always contain the
// literal substring "parse rewrite verdict" so callers can grep
// verdict-parse failures apart from clone/git failures.
func ParseRewriteVerdict(
	ctx context.Context,
	claudeOutput string,
) (RewriteVerdict, error) {
	trimmed := strings.TrimSpace(claudeOutput)

	var v RewriteVerdict

	// Strategy 1: Parse the trimmed input as a JSON object directly.
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return validateRewriteVerdict(ctx, v)
	}

	// Strategy 2: Strip ```json fences.
	stripped := strings.TrimSpace(strings.TrimSuffix(
		strings.TrimPrefix(strings.TrimPrefix(trimmed, "```json"), "```"),
		"```",
	))
	if err := json.Unmarshal([]byte(stripped), &v); err == nil {
		return validateRewriteVerdict(ctx, v)
	}

	// Strategy 3: Find the last balanced {...} block in the input.
	block, ok := lastJSONBlock(trimmed)
	if !ok {
		return RewriteVerdict{}, errors.Errorf(ctx, "parse rewrite verdict: no JSON found")
	}
	if err := json.Unmarshal([]byte(block), &v); err != nil {
		return RewriteVerdict{}, errors.Wrapf(ctx, err, "parse rewrite verdict: %s", block)
	}
	return validateRewriteVerdict(ctx, v)
}

// validateRewriteVerdict enforces the field-level invariants from
// spec 058 Desired Behavior: Reasoning must be non-empty; if
// RewriteNeeded is true RewrittenUnreleased must be non-empty;
// if RewriteNeeded is false RewrittenUnreleased must be the empty
// string. On failure, returns a zero verdict + a wrapped error
// containing "parse rewrite verdict".
func validateRewriteVerdict(
	ctx context.Context,
	v RewriteVerdict,
) (RewriteVerdict, error) {
	if strings.TrimSpace(v.Reasoning) == "" {
		return RewriteVerdict{}, errors.Errorf(
			ctx,
			"parse rewrite verdict: missing reasoning",
		)
	}
	if v.RewriteNeeded && strings.TrimSpace(v.RewrittenUnreleased) == "" {
		return RewriteVerdict{}, errors.Errorf(
			ctx,
			"parse rewrite verdict: rewrite_needed=true but rewritten_unreleased is empty",
		)
	}
	if !v.RewriteNeeded && v.RewrittenUnreleased != "" {
		return RewriteVerdict{}, errors.Errorf(
			ctx,
			"parse rewrite verdict: rewrite_needed=false but rewritten_unreleased is non-empty",
		)
	}
	return v, nil
}

//go:embed changelog_faithfulness.md
var changelogFaithfulnessPrompt string

// ChangelogFaithfulnessPrompt returns the embedded prompt that audits a
// CHANGELOG rewrite for semantic faithfulness. The caller concatenates
// the captured original body and the final body onto the returned string
// before invoking Claude.
func ChangelogFaithfulnessPrompt() string {
	return changelogFaithfulnessPrompt
}

// FaithfulnessEntry is one row of the faithfulness verdict.
//
//   - In per_entry the verdict is "present" or "silent-drop".
//   - In extras the verdict is always "hallucinated".
//
// The "unknown" verdict lives only at the overall level (LLM
// unavailability) — per_entry verdicts never carry it. See
// FaithfulnessLLMResponse.Overall for the rolled-up state.
type FaithfulnessEntry struct {
	Entry   string `json:"entry"`
	Verdict string `json:"verdict"`
	Note    string `json:"note,omitempty"`
}

// FaithfulnessLLMResponse is the typed shape of Claude's JSON response
// to the changelog-faithfulness prompt.
//
//   - per_entry: one object per original user-observable change.
//   - extras:   one object per final-body entry judged hallucinated.
//   - overall:  "pass" when every per_entry.verdict == "present" AND
//     extras is empty; otherwise "fail".
type FaithfulnessLLMResponse struct {
	PerEntry []FaithfulnessEntry `json:"per_entry"`
	Extras   []FaithfulnessEntry `json:"extras"`
	Overall  string              `json:"overall"`
}

// ParseFaithfulnessResponse extracts a FaithfulnessLLMResponse from
// Claude's raw output. Uses the same three-strategy extraction as
// ParseBumpVerdict / ParseRewriteVerdict (plain JSON, fenced ```json
// block, last balanced {...} block). After unmarshal:
//
//   - overall MUST be one of {"pass", "fail"}.
//   - per_entry[i].verdict MUST be one of {"present", "silent-drop"}.
//   - extras[i].verdict MUST be "hallucinated".
//
// Errors are wrapped via github.com/bborbe/errors and always contain
// the literal substring "parse faithfulness response" so callers can
// grep parser failures apart from clone/git failures.
func ParseFaithfulnessResponse(
	ctx context.Context,
	claudeOutput string,
) (FaithfulnessLLMResponse, error) {
	trimmed := strings.TrimSpace(claudeOutput)

	var v FaithfulnessLLMResponse

	// Strategy 1: Parse the trimmed input as a JSON object directly.
	if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
		return validateFaithfulness(ctx, v)
	}

	// Strategy 2: Strip ```json fences.
	stripped := strings.TrimSpace(strings.TrimSuffix(
		strings.TrimPrefix(strings.TrimPrefix(trimmed, "```json"), "```"),
		"```",
	))
	if err := json.Unmarshal([]byte(stripped), &v); err == nil {
		return validateFaithfulness(ctx, v)
	}

	// Strategy 3: Find the last balanced {...} block in the input.
	block, ok := lastJSONBlock(trimmed)
	if !ok {
		return FaithfulnessLLMResponse{}, errors.Errorf(
			ctx,
			"parse faithfulness response: no JSON found",
		)
	}
	if err := json.Unmarshal([]byte(block), &v); err != nil {
		return FaithfulnessLLMResponse{}, errors.Wrapf(
			ctx,
			err,
			"parse faithfulness response: %s",
			block,
		)
	}
	return validateFaithfulness(ctx, v)
}

// validateFaithfulness enforces the field-level invariants for the
// faithfulness LLM response.
//
//   - overall must be in {"pass", "fail"}.
//   - per_entry[i].verdict must be in {"present", "silent-drop"}.
//   - extras[i].verdict must be "hallucinated".
func validateFaithfulness(
	ctx context.Context,
	v FaithfulnessLLMResponse,
) (FaithfulnessLLMResponse, error) {
	switch v.Overall {
	case "pass", "fail":
		// ok
	default:
		return FaithfulnessLLMResponse{}, errors.Errorf(
			ctx,
			"parse faithfulness response: invalid overall value %q (want pass|fail)",
			v.Overall,
		)
	}
	for i, e := range v.PerEntry {
		switch e.Verdict {
		case "present", "silent-drop":
			// ok
		default:
			return FaithfulnessLLMResponse{}, errors.Errorf(
				ctx,
				"parse faithfulness response: per_entry[%d] invalid verdict %q (want present|silent-drop)",
				i,
				e.Verdict,
			)
		}
	}
	for i, e := range v.Extras {
		switch e.Verdict {
		case "hallucinated":
			// ok
		default:
			return FaithfulnessLLMResponse{}, errors.Errorf(
				ctx,
				"parse faithfulness response: extras[%d] invalid verdict %q (want hallucinated)",
				i,
				e.Verdict,
			)
		}
	}
	return v, nil
}

// lastJSONBlock returns the last balanced {...} substring in s, or
// "", false if none exists. Mirrors agent/pr-reviewer/pkg/steps_review.go
// lastJSONBlock — kept private to this package to avoid an unwanted
// dependency edge.
func lastJSONBlock(s string) (string, bool) {
	end := strings.LastIndex(s, "}")
	if end < 0 {
		return "", false
	}
	depth := 0
	for i := end; i >= 0; i-- {
		switch s[i] {
		case '}':
			depth++
		case '{':
			depth--
			if depth == 0 {
				return s[i : end+1], true
			}
		}
	}
	return "", false
}

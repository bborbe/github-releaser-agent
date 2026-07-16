// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// PlanOutput is the typed contract for the `## Plan` JSON section the
// planning step writes for every release task. Round-trips with
// agentlib.MarshalSectionTyped + agentlib.ExtractSection[PlanOutput].
//
// Three shapes are valid:
//   - Outcome="ready"        — planning succeeded; Bump/NextVersion populated
//   - Outcome="needs_input"  — precondition failure; Reason + PreconditionFailed populated
//   - Outcome="failed"       — invalid config; ErrorCategory + InvalidField + InvalidValue populated
//
// No `Details map[string]any`: concrete fields only. Future fields require
// a spec amendment.
type PlanOutput struct {
	Outcome            string   `json:"outcome"`
	Bump               string   `json:"bump,omitempty"`
	Reasoning          string   `json:"reasoning,omitempty"`
	CurrentVersion     string   `json:"current_version,omitempty"`
	NextVersion        string   `json:"next_version,omitempty"`
	NextVersionHeader  string   `json:"next_version_header,omitempty"`
	HeaderPrefixStyle  string   `json:"header_prefix_style,omitempty"`
	Bullets            []string `json:"bullets,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	PreconditionFailed string   `json:"precondition_failed,omitempty"`

	// OriginalUnreleased is the raw ## Unreleased body (verbatim, line-endings
	// preserved) captured at planning time. ai-review reads this from the task
	// page — never re-derives it from the repo — so an attacker who modifies
	// the repo between planning and review cannot mask drift.
	OriginalUnreleased string `json:"original_unreleased,omitempty"`

	// RewriteNeeded is true when the planning LLM judged the original body
	// does not conform to the Changelog Quality Guide and produced a cleaned
	// body in RewrittenUnreleased. When false, execution renames the header
	// only and leaves the body untouched.
	//
	// omitempty is deliberately NOT applied so a `false` decision is always
	// written explicitly — ai-review needs to distinguish "not decided" from
	// "decided no".
	RewriteNeeded bool `json:"rewrite_needed"`

	// RewrittenUnreleased is the cleaned body. Populated only when
	// RewriteNeeded is true. Execution replaces the ## Unreleased body with
	// this text before renaming the header.
	RewrittenUnreleased string `json:"rewritten_unreleased,omitempty"`

	// ChangelogRewrite records the spec-059 per-repo opt-in flag value
	// resolved at planning entry. *bool (not bool) so the JSON distinguishes
	// "not resolved" from "resolved false" — the planning step ALWAYS sets
	// this field on the happy path (no-rewrite-needed AND rewrite-needed
	// outcomes), so a reader can audit which mode the run took. Omitted
	// from JSON only on the failure path (outcome="failed" carries the
	// error info instead).
	//
	// JSON encoding contract (load-bearing for spec AC #14 and prompt-2
	// requirement 5a/6 evidence):
	//   - happy path, flag false → field SET to &false → JSON emits
	//     literal substring `"changelog_rewrite":false`
	//   - happy path, flag true  → field SET to &true  → JSON emits
	//     literal substring `"changelog_rewrite":true`
	//   - failure path           → field LEFT nil     → omitempty omits
	//     the token entirely; `changelog_rewrite` is absent from the JSON
	ChangelogRewrite *bool `json:"changelog_rewrite,omitempty"`

	// ConfigFetchWarning records a non-fatal .maintainer.yaml fetch failure
	// that the planning step recovered from by using the default
	// changelogRewrite=false. Populated only when the fetch errored with
	// something OTHER than ErrFileNotFound (transport, DNS, 5xx, timeout).
	// Operators reading the task page can grep this field to confirm
	// whether a repo that opted into rewrite was silently downgraded.
	// Empty on the happy path (file present) and on the legitimate-absent
	// path (404 → ErrFileNotFound).
	ConfigFetchWarning string `json:"config_fetch_warning,omitempty"`

	// ErrorCategory names the failure category on outcome="failed". For
	// spec 059 the only value is "invalid_config" (release.changelogRewrite
	// is non-boolean). Future failure categories may extend this set.
	ErrorCategory string `json:"error_category,omitempty"`

	// InvalidField names the .maintainer.yaml field that failed validation.
	// Populated on outcome="failed" only; today always "release.changelogRewrite".
	InvalidField string `json:"invalid_field,omitempty"`

	// InvalidValue captures the literal raw value that failed validation
	// (the YAML-decoded string/number/etc., as it appeared in the file).
	// Populated on outcome="failed" only.
	InvalidValue string `json:"invalid_value,omitempty"`
}

// Outcome values for PlanOutput.Outcome.
const (
	PlanOutcomeReady      = "ready"
	PlanOutcomeNeedsInput = "needs_input"
	// PlanOutcomeFailed signals a hard planning-time failure that
	// ends the task in human_review (Status=AgentStatusFailed,
	// NextPhase=human_review). Distinct from needs_input, which
	// keeps the task in_progress and waits for operator re-delegation.
	// See spec 059 § Desired Behavior 5 and § AC 11/12.
	PlanOutcomeFailed = "failed"
)

const (
	// ErrorCategoryInvalidConfig is the only valid value for
	// PlanOutput.ErrorCategory today. spec 059 § Failure Modes:
	// non-boolean release.changelogRewrite.
	ErrorCategoryInvalidConfig = "invalid_config"
)

// PreconditionFailed values. Keep in sync with spec 047 Desired Behavior 5.
const (
	PreconditionP1UnreleasedNotFirst = "P1_unreleased_not_first"
	PreconditionP2UnreleasedEmpty    = "P2_unreleased_empty"
	PreconditionBadCurrentVersion    = "bad_current_version"
	// PreconditionMissingFrontmatter is the PREFIX used for missing-field
	// precondition values; planning code appends the field name, e.g.
	// "missing_frontmatter_clone_url".
	PreconditionMissingFrontmatter = "missing_frontmatter_"
)

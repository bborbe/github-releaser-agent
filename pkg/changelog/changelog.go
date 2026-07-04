// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package changelog provides pure-Go functions for parsing CHANGELOG.md byte streams.
// It supports three operations: validating the Unreleased section for releaseability,
// extracting bullet entries from the Unreleased section, and inferring the historic
// header prefix style (versioned with "v" prefix or unprefixed).
package changelog

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	bborbeerrors "github.com/bborbe/errors"
)

// Package-level compiled regexes for InferHeaderPrefixStyle and the lenient
// version-header detection rule used by the unreleased-section parser. These
// are read-only and deterministic, preserving the pure-function contract.
//
// versionHeaderRe matches "X.Y.Z" or "vX.Y.Z" only — it does NOT accept
// extended text such as "Unreleased changes" or "WIP". The parser treats the
// first ## heading that does NOT match this pattern as the unreleased
// section (spec 065; parity with watcher/github-release/pkg/changelog.go).
var (
	vPrefixRE       = regexp.MustCompile(`^v[0-9]+\.`)
	noPrefixRE      = regexp.MustCompile(`^[0-9]+\.`)
	versionHeaderRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
)

// isVersionHeader reports whether headingText (the text after "## ") matches
// the version-header pattern "X.Y.Z" or "vX.Y.Z". Used by the lenient
// unreleased-section detection rule to distinguish release headings from
// the unreleased section. Trailing whitespace is already stripped by
// parseHeading, so the regex runs against the cleaned text.
func isVersionHeader(headingText string) bool {
	return versionHeaderRe.MatchString(headingText)
}

// ValidateUnreleased checks whether the CHANGELOG content is in a releaseable state.
// It returns (valid, reason, line) where valid is true if the Unreleased section exists
// as the first ## heading and contains at least one "- " bullet entry.
// The line number is 1-indexed and indicates where the issue was found (0 if valid or generic error).
func ValidateUnreleased(content []byte) (valid bool, reason string, line int) {
	if len(content) == 0 {
		return false, "Unreleased section not found.", 0
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	firstHeading, unreleasedLine := findFirstAndUnreleased(scanner)
	if firstHeading == nil {
		return false, "Unreleased section not found.", 0
	}

	if unreleasedLine == 0 {
		return false, fmt.Sprintf(
			"Unreleased is not the first ## section; found '%s' at line %d. Move ## Unreleased above all release headings.",
			firstHeading.text,
			firstHeading.line,
		), firstHeading.line
	}

	if firstHeading.line != unreleasedLine {
		return false, fmt.Sprintf(
			"Unreleased is not the first ## section; found '%s' at line %d. Move ## Unreleased above all release headings.",
			firstHeading.text,
			firstHeading.line,
		), firstHeading.line
	}

	scanner = bufio.NewScanner(bytes.NewReader(content))
	skipLines(scanner, unreleasedLine)

	if !hasBulletInBlock(scanner) {
		return false, "Unreleased section has no bullet entries.", unreleasedLine
	}

	return true, "", 0
}

// heading represents a parsed ## heading.
type heading struct {
	line int
	text string
}

// findFirstAndUnreleased scans the content and returns the FIRST ## heading
// (firstHeading) plus the line of the first ## heading that is NOT a version
// header (unreleasedLine). When the first ## heading is a non-version header
// (i.e. the lenient rule classifies it as the unreleased section),
// firstHeading.line == unreleasedLine and the "version header first" branch
// of ValidateUnreleased is skipped. When the first ## heading IS a version
// header, unreleasedLine is 0 unless a later ## heading is non-version.
//
// The lenient "first non-version H2" rule accepts ## Unreleased, ## unreleased,
// ## Unreleased changes, ## WIP, ## Next, and similar author variants. The
// pattern is mirrored from watcher/github-release/pkg/changelog.go.
func findFirstAndUnreleased(scanner *bufio.Scanner) (*heading, int) {
	var firstHeading *heading
	unreleasedLine := 0
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if !isHeading(line) {
			continue
		}

		headingText := parseHeading(line)

		if firstHeading == nil {
			firstHeading = &heading{line: lineNum, text: headingText}
		}

		if !isVersionHeader(headingText) {
			unreleasedLine = lineNum
			break
		}
	}

	return firstHeading, unreleasedLine
}

// isHeading returns true if the line is a ## heading.
func isHeading(line string) bool {
	return len(line) >= 3 && line[0] == '#' && line[1] == '#' && line[2] == ' '
}

// parseHeading extracts the heading text from a ## heading line.
func parseHeading(line string) string {
	return trimTrailingWhitespace(line[3:])
}

// skipLines advances the scanner by n lines.
func skipLines(scanner *bufio.Scanner, n int) {
	for i := 0; i < n; i++ {
		if !scanner.Scan() {
			return
		}
	}
}

// hasBulletInBlock returns true if the block after the Unreleased heading contains a -  bullet.
func hasBulletInBlock(scanner *bufio.Scanner) bool {
	for scanner.Scan() {
		line := scanner.Text()
		if isHeading(line) {
			break
		}
		if isBullet(line) {
			return true
		}
	}
	return false
}

// isBullet returns true if the line is a -  bullet.
func isBullet(line string) bool {
	return len(line) >= 2 && line[0] == '-' && line[1] == ' '
}

// ExtractUnreleasedBullets returns the bullet entries from the ## Unreleased section.
// It locates the first non-version "## " heading (lenient rule: matches ## Unreleased,
// ## unreleased, ## Unreleased changes, ## WIP, ## Next, etc.) and returns all lines
// starting with "- " until the next ## heading or EOF.
// Returns nil if no non-version ## heading exists.
// Returns a non-nil empty slice if a non-version ## heading exists but contains no bullets.
func ExtractUnreleasedBullets(content []byte) []string {
	if len(content) == 0 {
		return nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	lineNum := 0
	inUnreleased := false

	// Find the first non-version ## heading (the lenient unreleased section).
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if len(line) >= 3 && line[0] == '#' && line[1] == '#' && line[2] == ' ' {
			headingText := line[3:] // Strip "## "
			headingText = trimTrailingWhitespace(headingText)

			if !isVersionHeader(headingText) {
				inUnreleased = true
				break
			}
		}
	}

	if !inUnreleased {
		return nil
	}

	// Scan from the next line until next ## heading or EOF
	result := []string{}
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Stop at next ## heading
		if len(line) >= 3 && line[0] == '#' && line[1] == '#' && line[2] == ' ' {
			break
		}

		// Extract -  bullet
		if len(line) >= 2 && line[0] == '-' && line[1] == ' ' {
			// Strip exactly "- " (2 chars) from the beginning
			bullet := line[2:]
			result = append(result, bullet)
		}
	}

	return result
}

// InferHeaderPrefixStyle examines the first historic release heading (the first
// ## heading that is not the lenient-detected unreleased section) and returns
// the prefix style used. The lenient rule (spec 065) classifies ANY non-version
// ## heading — ## Unreleased, ## WIP, ## Next, etc. — as the unreleased section
// to skip, then infers the prefix style from the first version-header ## heading.
// Returns "v" if the heading matches "vX.Y.Z" format, "" if it matches "X.Y.Z"
// format, and "v" as a default if no historic release heading exists.
func InferHeaderPrefixStyle(content []byte) string {
	if len(content) == 0 {
		return "v"
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	for scanner.Scan() {
		line := scanner.Text()

		if len(line) >= 3 && line[0] == '#' && line[1] == '#' && line[2] == ' ' {
			headingText := line[3:] // Strip "## "
			headingText = trimTrailingWhitespace(headingText)

			// Skip the lenient-detected unreleased section: any ## heading
			// that is not a version header.
			if !isVersionHeader(headingText) {
				continue
			}

			// This is the first historic release heading
			if vPrefixRE.MatchString(headingText) {
				return "v"
			}
			if noPrefixRE.MatchString(headingText) {
				return ""
			}
			// If heading doesn't match either pattern, keep scanning
		}
	}

	// No historic release heading found, default to "v"
	return "v"
}

// trimTrailingWhitespace removes trailing whitespace from a string.
func trimTrailingWhitespace(s string) string {
	end := len(s)
	for end > 0 && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[:end]
}

// ExtractUnreleasedBody returns the verbatim body of the lenient-detected
// unreleased section (spec 065): every line after the first non-version "## "
// heading up to (but excluding) the next "## " heading or EOF. Line endings
// are normalized to '\n' between emitted lines (matching the
// RewriteUnreleasedHeader line-ending convention). Leading and trailing blank
// lines are NOT trimmed — the returned string is the raw slice of the section.
//
// The lenient rule accepts ## Unreleased plus author variants such as
// ## unreleased, ## Unreleased changes, ## WIP, ## Next.
//
// Returns a wrapped bborbe/errors error if no non-version "## " heading is
// present. The error message uses the literal phrase "unreleased header not
// found" (lowercase) so the ErrorCategoryUnreleasedNotFound classifier in
// git/error_classifier.go continues to match. The ctx parameter is used only
// for error wrapping consistency. No IO, deterministic. Safe for concurrent
// use.
//
// Distinct from ExtractSectionBody (which retains exact-match semantics for
// version-heading lookups in steps_ai_review.go:463). The lenient logic is
// applied in this wrapper only.
func ExtractUnreleasedBody(
	ctx context.Context,
	content []byte,
) (string, error) {
	if len(content) == 0 {
		return "", bborbeerrors.Errorf(
			ctx,
			"%s header not found",
			strings.ToLower("unreleased"),
		)
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	found := false
	var out bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		if !found {
			if isHeading(line) && !isVersionHeader(parseHeading(line)) {
				found = true
			}
			continue
		}
		if isHeading(line) {
			break
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", bborbeerrors.Wrap(ctx, err, "scan CHANGELOG content")
	}
	if !found {
		return "", bborbeerrors.Errorf(
			ctx,
			"%s header not found",
			strings.ToLower("unreleased"),
		)
	}
	return out.String(), nil
}

// ExtractSectionBody returns the verbatim body of the first section
// whose ## heading text matches heading (e.g. "Unreleased" or
// "v1.2.8"): every line after that heading up to (but excluding) the
// next `## ` heading or EOF. Line endings are normalized to '\n' between
// emitted lines. Leading and trailing blank lines are NOT trimmed —
// the returned string is the raw slice of the section.
//
// Returns a wrapped bborbe/errors error if the requested heading is
// not present. The ctx parameter is used only for error wrapping
// consistency. No IO, deterministic. Safe for concurrent use.
//
// heading is matched against the heading TEXT (the part after `## `),
// not the full markdown line — callers can pass "Unreleased" or
// "v1.2.8" interchangeably. On absence the error message uses the
// literal phrase "%s header not found" so callers (and tests) can
// match on it regardless of capitalization.
func ExtractSectionBody(
	ctx context.Context,
	content []byte,
	heading string,
) (string, error) {
	if len(content) == 0 {
		return "", bborbeerrors.Errorf(
			ctx,
			"%s header not found",
			strings.ToLower(heading),
		)
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	// The bufio default 64KB token limit is plenty for CHANGELOGs.

	found := false
	var out bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		if !found {
			if isHeading(line) && parseHeading(line) == heading {
				found = true
			}
			continue
		}
		if isHeading(line) {
			break
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", bborbeerrors.Wrap(ctx, err, "scan CHANGELOG content")
	}
	if !found {
		return "", bborbeerrors.Errorf(
			ctx,
			"%s header not found",
			strings.ToLower(heading),
		)
	}
	return out.String(), nil
}

// ReplaceUnreleasedBody returns content with the body of the lenient-detected
// unreleased section replaced by newBody. The lenient rule (spec 065) treats
// the first "## " heading that is not a version header as the unreleased
// section; the heading line itself is preserved VERBATIM (input "## WIP"
// stays "## WIP" on disk). Only the lines AFTER it (and BEFORE the next
// "## " heading or EOF) are swapped. Text before the heading and text
// starting at the next "## " heading is preserved verbatim.
//
// newBody is inserted as-is. If it does not end with '\n', a single '\n' is
// appended before the next heading line so the inserted body is followed
// by the original separator. Line endings are normalized to '\n' on output.
//
// Returns a wrapped bborbe/errors error if no non-version "## " heading is
// present. The error message uses the literal phrase "unreleased header not
// found" (lowercase) so the ErrorCategoryUnreleasedNotFound classifier in
// git/error_classifier.go continues to match. The caller (execution step)
// maps this to error_category: unreleased_not_found.
//
// The ctx parameter is used only for error wrapping consistency.
// No IO, deterministic. Safe for concurrent use.
func ReplaceUnreleasedBody(
	ctx context.Context,
	content []byte,
	newBody string,
) ([]byte, error) {
	if len(content) == 0 {
		return nil, bborbeerrors.New(
			ctx,
			"unreleased header not found: empty content",
		)
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	// The bufio default 64KB token limit is plenty for CHANGELOGs.

	var out bytes.Buffer
	// 0 = before the Unreleased heading; 1 = inside the Unreleased body
	// (consuming lines until the next heading); 2 = after the Unreleased
	// block (passing every line through verbatim).
	state := 0
	for scanner.Scan() {
		line := scanner.Text()
		switch state {
		case 0:
			if isHeading(line) && !isVersionHeader(parseHeading(line)) {
				out.WriteString(line)
				out.WriteByte('\n')
				out.WriteString(newBody)
				if len(newBody) == 0 || newBody[len(newBody)-1] != '\n' {
					out.WriteByte('\n')
				}
				state = 1
				continue
			}
			out.WriteString(line)
			out.WriteByte('\n')
		case 1:
			// Inside the Unreleased body: drop everything until the next
			// "## " heading, then transition to pass-through.
			if isHeading(line) {
				out.WriteString(line)
				out.WriteByte('\n')
				state = 2
				continue
			}
			// Otherwise silently discard the old body line.
		case 2:
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, bborbeerrors.Wrap(ctx, err, "scan CHANGELOG content")
	}
	if state == 0 {
		return nil, bborbeerrors.New(ctx, "unreleased header not found")
	}

	// Preserve a trailing-newline-less input: if the original content did
	// NOT end with '\n', drop the final '\n' we appended above.
	result := out.Bytes()
	if len(content) > 0 && content[len(content)-1] != '\n' && len(result) > 0 &&
		result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

	return result, nil
}

// RewriteUnreleasedHeader returns content with the lenient-detected
// unreleased heading line replaced by newHeader (e.g. "## v1.2.8"). The
// lenient rule (spec 065) matches the first "## " heading that is NOT a
// version header; the rewrite step is what canonicalizes the on-disk
// heading, so input "## unreleased" / "## WIP" / "## Next" all become
// "## vX.Y.Z" regardless of the input variant ("lenient on input,
// canonical on output"). Whitespace-tolerant: trailing spaces/tabs/CR on
// the unreleased heading are accepted and discarded along with the
// original line. All other lines (bullets, blank lines, other headings)
// are preserved verbatim, including their original line endings.
//
// Line endings are normalized to `\n` on rewrite.
//
// newHeader is inserted as-is. The caller is responsible for the leading
// "## " prefix and any trailing newline normalization is left to the
// existing content's line-ending convention (the function preserves the
// newline that followed the original lenient-detected heading line, if
// any).
//
// Returns a wrapped bborbe/errors error if no non-version "## " heading is
// present. The error message uses the literal phrase "unreleased header not
// found" (lowercase) so the ErrorCategoryUnreleasedNotFound classifier in
// git/error_classifier.go continues to match. The caller (execution step)
// maps this to error_category: unreleased_not_found.
//
// The ctx parameter is used only for error wrapping consistency.
// No IO, deterministic. Safe for concurrent use.
func RewriteUnreleasedHeader(
	ctx context.Context,
	content []byte,
	newHeader string,
) ([]byte, error) {
	if len(content) == 0 {
		return nil, bborbeerrors.New(
			ctx,
			"unreleased header not found: empty content",
		)
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	// The bufio default 64KB token limit is plenty for CHANGELOGs.

	var out bytes.Buffer
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if !found && isHeading(line) && !isVersionHeader(parseHeading(line)) {
			out.WriteString(newHeader)
			out.WriteByte('\n')
			found = true
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, bborbeerrors.Wrap(ctx, err, "scan CHANGELOG content")
	}
	if !found {
		return nil, bborbeerrors.New(ctx, "unreleased header not found")
	}

	// Preserve a trailing-newline-less input: if the original content did
	// NOT end with '\n', drop the final '\n' we appended above.
	result := out.Bytes()
	if len(content) > 0 && content[len(content)-1] != '\n' && len(result) > 0 &&
		result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

	return result, nil
}

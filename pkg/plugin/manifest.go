// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package plugin provides pure-Go functions for detecting Claude Code plugin manifests
// and bumping their version fields.
package plugin

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	bborbeerrors "github.com/bborbe/errors"
)

// Package-level compiled regex for semver-shaped string validation.
// Matches only the bare N.N.N pattern — no leading 'v', no suffix.
var semverRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Package-level compiled regex for the "plugins": [ array-opening line.
var pluginsArrayLineRE = regexp.MustCompile(`^\s*"plugins"\s*:\s*\[`)

// Package-level compiled regex for a named-object scope opener such as `"metadata": {`.
var isOpenScopeKeyRE = regexp.MustCompile(`^\s*"[^"]+"\s*:\s*\{`)

// DetectManifests returns the subset of known plugin manifest paths that exist
// as regular files in the given workdir. The returned paths are repo-relative
// (e.g. ".claude-plugin/plugin.json").
//
// Existence detection is not an error condition: missing manifests are silently
// omitted from the result. Errors are returned only for unexpected I/O failures.
func DetectManifests(ctx context.Context, workdir string) ([]string, error) {
	known := []string{
		".claude-plugin/plugin.json",
		".claude-plugin/marketplace.json",
	}

	var result []string
	for _, rel := range known {
		path := filepath.Join(workdir, rel)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, bborbeerrors.Wrapf(ctx, err, "detect manifests in %s", workdir)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		result = append(result, rel)
	}
	return result, nil
}

// BumpPluginJSON rewrites the top-level "version" field in a plugin.json byte stream.
// It validates the version parameter against semverRE before touching content.
// All other bytes are preserved verbatim (same indentation, key order, trailing newline).
func BumpPluginJSON(ctx context.Context, content []byte, version string) ([]byte, error) {
	if !semverRE.MatchString(version) {
		return nil, bborbeerrors.Errorf(ctx,
			"plugin.json bump rejected: version parameter %q is not a semver-shaped string",
			version)
	}

	if len(content) == 0 {
		return nil, bborbeerrors.New(ctx, "plugin.json version field not found")
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	var out bytes.Buffer
	found := false

	for scanner.Scan() {
		line := scanner.Text()

		if !found && isVersionKeyLine(line) {
			rewritten, err := rewriteVersionValue(ctx, line, version, "plugin.json")
			if err != nil {
				return nil, err
			}
			writeLine(&out, rewritten)
			found = true
			continue
		}

		writeLine(&out, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, bborbeerrors.Wrap(ctx, err, "bump plugin.json")
	}

	if !found {
		return nil, bborbeerrors.New(ctx, "plugin.json version field not found")
	}

	result := out.Bytes()
	if len(content) > 0 && content[len(content)-1] != '\n' && len(result) > 0 &&
		result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

	return result, nil
}

// scopeTracker holds the depth + scope flags for the marketplace.json streaming
// state machine. Zero value is the initial state (depth 0, all scopes false).
// Mutations only happen through update so the state graph lives in one place
// instead of as four loose booleans threaded through a long loop body.
type scopeTracker struct {
	depth          int
	inMetadata     bool
	inPlugin       bool
	inPluginsArray bool
}

// update advances the tracker for the next line of the stream. The caller still
// owns the bytes.Buffer — this method only mutates the tracker's depth + scope
// flags based on the current line.
func (s *scopeTracker) update(line, trimmed string) {
	oldDepth := s.depth

	// Handle closing at oldDepth BEFORE we update depth.
	if oldDepth == 2 && isCloseBrace(line) {
		// Closing metadata object or a plugin object.
		s.inMetadata = false
		s.inPlugin = false
	}

	// Detect plugins array opening when we're already inside the root object.
	// At oldDepth >= 1, "plugins": [ means we're entering the plugins array.
	if oldDepth >= 1 && pluginsArrayLineRE.MatchString(trimmed) {
		s.inPluginsArray = true
	}

	// Update depth.
	depthDelta := countOpenBraces(line) - countCloseBraces(line)
	s.depth = oldDepth + depthDelta

	// Handle scope entry at the new depth. oldDepth tells us what we came FROM.
	if depthDelta > 0 && oldDepth == 1 && isOpenScopeKey(trimmed) {
		if extractScopeKey(trimmed) == "metadata" {
			s.inMetadata = true
			s.inPlugin = false
			s.inPluginsArray = false
		}
	}

	// Entering a plugin object: we're in the plugins array and see a line
	// starting with {.
	if s.inPluginsArray && strings.HasPrefix(trimmed, "{") {
		if oldDepth == 2 || oldDepth == 1 || oldDepth == 0 {
			s.inPlugin = true
			s.inMetadata = false
		}
	}

	// Handle plugins array close: ] at oldDepth==2.
	if oldDepth == 2 && isCloseBracket(line) && s.inPluginsArray {
		s.inPluginsArray = false
		s.inPlugin = false
	}

	// Full scope exit.
	if s.depth == 0 {
		s.inMetadata = false
		s.inPlugin = false
		s.inPluginsArray = false
	}
}

// inVersionScope returns true when the tracker is inside the "metadata" object
// or inside a single plugin object — i.e. the scopes where a "version" key
// should be rewritten.
func (s *scopeTracker) inVersionScope() bool {
	return s.inMetadata || s.inPlugin
}

// BumpMarketplaceJSON rewrites metadata.version and every plugins[].version
// in a marketplace.json byte stream. It validates the version parameter against
// semverRE before touching content. All other bytes are preserved verbatim.
func BumpMarketplaceJSON(ctx context.Context, content []byte, version string) ([]byte, error) {
	if !semverRE.MatchString(version) {
		return nil, bborbeerrors.Errorf(ctx,
			"marketplace.json bump rejected: version parameter %q is not a semver-shaped string",
			version)
	}

	if len(content) == 0 {
		return nil, bborbeerrors.New(ctx, "marketplace.json version field not found")
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))

	var out bytes.Buffer
	var tracker scopeTracker
	foundAny := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := trimLine(line)

		tracker.update(line, trimmed)

		if lineHasVersionKey(trimmed) && tracker.inVersionScope() {
			rewritten, err := rewriteVersionValue(ctx, line, version, "marketplace.json")
			if err != nil {
				return nil, err
			}
			writeLine(&out, rewritten)
			foundAny = true
			continue
		}

		writeLine(&out, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, bborbeerrors.Wrap(ctx, err, "bump marketplace.json")
	}

	if !foundAny {
		return nil, bborbeerrors.New(ctx, "marketplace.json version field not found")
	}

	result := out.Bytes()
	if len(content) > 0 && content[len(content)-1] != '\n' && len(result) > 0 &&
		result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

	return result, nil
}

// isOpenScopeKey returns true if the trimmed line opens a named object scope
// with a key like "metadata" or "plugins" (e.g. `"metadata": {`).
func isOpenScopeKey(trimmed string) bool {
	return isOpenScopeKeyRE.MatchString(trimmed)
}

// extractScopeKey extracts the key name from a line like `"metadata": {` or `"plugins": {`.
func extractScopeKey(trimmed string) string {
	// Find the opening quote
	start := -1
	for i, ch := range trimmed {
		if ch == '"' {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	// Find the closing quote
	end := start
	for end < len(trimmed) && trimmed[end] != '"' {
		end++
	}
	if end > start {
		return trimmed[start:end]
	}
	return ""
}

// isVersionKeyLine returns true if the line contains a "version" key.
// It distinguishes the "version" key from "version" appearing inside
// another string value by checking that "version" is preceded by a
// JSON structural character (",", "{", "[", or whitespace).
func isVersionKeyLine(line string) bool {
	trimmed := trimLine(line)
	idx := strings.Index(trimmed, `"version":`)
	if idx < 0 {
		idx = strings.Index(trimmed, `"version" :`)
	}
	if idx < 0 {
		return false
	}
	if idx == 0 {
		return true
	}
	// Check that the char before '"' is a JSON structural char
	before := trimmed[idx-1]
	return before == ',' || before == '{' || before == '[' || before == ' ' || before == '\t'
}

// lineHasVersionKey returns true if the trimmed line opens a "version" key
// (e.g. `"version": "x"` or `"version" : "x"`). The function is identical
// to isVersionKeyLine but operates on a pre-trimmed input — extracted from
// the closure that used to live inside BumpMarketplaceJSON.
func lineHasVersionKey(trimmed string) bool {
	idx := strings.Index(trimmed, `"version":`)
	if idx < 0 {
		idx = strings.Index(trimmed, `"version" :`)
	}
	if idx < 0 {
		return false
	}
	if idx == 0 {
		return true
	}
	before := trimmed[idx-1]
	return before == ',' || before == '{' || before == '[' || before == ' ' || before == '\t'
}

// extractExistingVersion parses the existing value of a "version" key in `line`
// and returns the components rewriteVersionValue needs to rebuild the line:
// keyPart (everything up to and including the colon), the validated existing
// value, the trailing portion of the line after the closing quote (or after
// the unquoted value), the line's leading indent, and a quoted bool that is
// true when the original value was wrapped in matching quote characters.
// Returns an error if the line is malformed; the message explains why.
func extractExistingVersion(
	ctx context.Context,
	line string,
	fileType string,
) (keyPart, value, trailing, indent string, quoted bool, err error) {
	trimmed, colonIdx, err := locateVersionColon(ctx, line, fileType)
	if err != nil {
		return "", "", "", "", false, err
	}

	indent = getIndent(line)
	keyPart = trimmed[:colonIdx+1]
	valuePart := trimmed[colonIdx+1:]

	value, trailing, quoted, err = parseVersionValuePart(ctx, valuePart, fileType)
	if err != nil {
		return "", "", "", "", false, err
	}
	return keyPart, value, trailing, indent, quoted, nil
}

// locateVersionColon finds the index of the colon that follows the "version"
// key on the trimmed line. Returns an error when the line has no "version" key
// or no colon.
func locateVersionColon(
	ctx context.Context,
	line string,
	fileType string,
) (trimmed string, colonIdx int, err error) {
	trimmed = trimLine(line)
	versionKeyIdx := strings.Index(trimmed, `"version":`)
	if versionKeyIdx < 0 {
		versionKeyIdx = strings.Index(trimmed, `"version" :`)
	}
	if versionKeyIdx < 0 {
		return "", 0, bborbeerrors.New(ctx, fileType+" version line has no version key")
	}
	for i := versionKeyIdx; i < len(trimmed); i++ {
		if trimmed[i] == ':' {
			return trimmed, i, nil
		}
	}
	return "", 0, bborbeerrors.New(ctx, fileType+" version line has no colon")
}

// parseVersionValuePart extracts the value, trailing content, and quoting
// style from the portion of a "version" line that follows the colon (and any
// whitespace). The returned value has been validated against semverRE.
func parseVersionValuePart(
	ctx context.Context,
	valuePart, fileType string,
) (value, trailing string, quoted bool, err error) {
	if len(valuePart) == 0 {
		return "", "", false, bborbeerrors.Errorf(
			ctx,
			fileType+" existing version field is not a semver-shaped string: %q",
			"",
		)
	}
	rest := valuePart
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
		rest = rest[1:]
	}
	if len(rest) > 0 && (rest[0] == '"' || rest[0] == '\'') {
		value, trailing, err = parseQuotedVersionValue(ctx, rest, fileType)
		return value, trailing, true, err
	}
	value, trailing, err = parseUnquotedVersionValue(ctx, rest, fileType)
	return value, trailing, false, err
}

// parseQuotedVersionValue parses `"<value>"` from the start of rest and
// returns the inner value plus the trailing content after the closing quote.
// Handles backslash-escaped quote characters per the JSON spec.
func parseQuotedVersionValue(
	ctx context.Context,
	rest, fileType string,
) (value, trailing string, err error) {
	quote := rest[0]
	rest = rest[1:]
	closeIdx := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] != quote {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && rest[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return "", "", bborbeerrors.Errorf(ctx,
			fileType+" existing version field is not a semver-shaped string: %q", "")
	}
	value = rest[:closeIdx]
	if !semverRE.MatchString(value) {
		return "", "", bborbeerrors.Errorf(ctx,
			fileType+" existing version field is not a semver-shaped string: %q", value)
	}
	return value, rest[closeIdx+1:], nil
}

// parseUnquotedVersionValue parses an unquoted value (e.g. `0.9.12`) from
// the start of rest. The value extends until a JSON terminator or whitespace.
func parseUnquotedVersionValue(
	ctx context.Context,
	rest, fileType string,
) (value, trailing string, err error) {
	end := 0
	for end < len(rest) && rest[end] != ',' && rest[end] != '}' && rest[end] != ' ' && rest[end] != '\t' {
		end++
	}
	value = rest[:end]
	if !semverRE.MatchString(value) {
		return "", "", bborbeerrors.Errorf(ctx,
			fileType+" existing version field is not a semver-shaped string: %q", value)
	}
	return value, rest[end:], nil
}

// formatRewrittenVersion rebuilds a "version" key line with the new version
// string, preserving the original line's indent, keyPart, and trailing content.
// When quoted is true, the new value is rendered inside double quotes (matching
// the original line's quoting style); otherwise it is rendered unquoted.
// Assumes the components were produced by extractExistingVersion and are
// internally consistent.
func formatRewrittenVersion(indent, keyPart, version, trailing string, quoted bool) string {
	if quoted {
		return fmt.Sprintf("%s%s \"%s\"%s", indent, keyPart, version, trailing)
	}
	return fmt.Sprintf("%s%s %s%s", indent, keyPart, version, trailing)
}

// rewriteVersionValue replaces the value after ": " on the given line with the new
// version. The line must be a "version" key line. Returns an error if the existing
// value is not a semver (quoted or unquoted).
func rewriteVersionValue(
	ctx context.Context,
	line string,
	version string,
	fileType string,
) (string, error) {
	keyPart, _, trailing, indent, quoted, err := extractExistingVersion(ctx, line, fileType)
	if err != nil {
		return "", err
	}
	return formatRewrittenVersion(indent, keyPart, version, trailing, quoted), nil
}

// countOpenBraces returns the number of '{' and '[' characters in the line.
func countOpenBraces(line string) int {
	c := 0
	for _, ch := range line {
		if ch == '{' || ch == '[' {
			c++
		}
	}
	return c
}

// countCloseBraces returns the number of '}' and ']' characters in the line.
func countCloseBraces(line string) int {
	c := 0
	for _, ch := range line {
		if ch == '}' || ch == ']' {
			c++
		}
	}
	return c
}

// isCloseBrace returns true if the line contains a '}'.
func isCloseBrace(line string) bool {
	for _, ch := range line {
		if ch == '}' {
			return true
		}
	}
	return false
}

// isCloseBracket returns true if the line contains a ']'.
func isCloseBracket(line string) bool {
	for _, ch := range line {
		if ch == ']' {
			return true
		}
	}
	return false
}

// writeLine appends line + a single '\n' to buf. Used by both BumpPluginJSON
// and BumpMarketplaceJSON to dedupe the out.WriteString(line); out.WriteByte('\n')
// pair that appears on every loop iteration.
func writeLine(buf *bytes.Buffer, line string) {
	buf.WriteString(line)
	buf.WriteByte('\n')
}

// trimLine returns the line with leading and trailing whitespace removed.
func trimLine(line string) string {
	start := 0
	end := len(line)
	for start < end && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	for end > start && (line[end-1] == ' ' || line[end-1] == '\t' || line[end-1] == '\r') {
		end--
	}
	return line[start:end]
}

// getIndent returns the leading whitespace of the line.
func getIndent(line string) string {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[:i]
}

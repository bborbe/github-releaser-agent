// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package semver

import (
	"strconv"
	"strings"
)

// IsValid returns true iff v (with an optional leading "v") parses as exactly
// three non-negative integer components X.Y.Z. Pre-release/build-metadata
// suffixes (e.g. "v1.2.3-rc1") are NOT valid for this function.
func IsValid(v string) bool {
	stripped := strings.TrimPrefix(v, "v")
	parts := strings.Split(stripped, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		num, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		if num < 0 {
			return false
		}
	}
	return true
}

// Highest returns the highest-semver tag from names, preserving that
// tag's ORIGINAL string (including any "v" prefix) so downstream
// header-prefix inference is unaffected. Non-semver names (per IsValid)
// are skipped, not errored. Returns ("", false) when no name in names
// is valid semver (empty input, or all names non-semver). Comparison is
// numeric on (major, minor, patch) — creation order is irrelevant.
func Highest(names []string) (string, bool) {
	var best string
	var bestMajor, bestMinor, bestPatch int
	for _, name := range names {
		if !IsValid(name) {
			continue
		}
		stripped := strings.TrimPrefix(name, "v")
		parts := strings.Split(stripped, ".")
		major, _ := strconv.Atoi(parts[0])
		minor, _ := strconv.Atoi(parts[1])
		patch, _ := strconv.Atoi(parts[2])
		if best == "" {
			best = name
			bestMajor, bestMinor, bestPatch = major, minor, patch
			continue
		}
		if major > bestMajor ||
			(major == bestMajor && minor > bestMinor) ||
			(major == bestMajor && minor == bestMinor && patch > bestPatch) {
			best = name
			bestMajor, bestMinor, bestPatch = major, minor, patch
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package semver computes the next semantic version given a current
// version and a bump kind (patch | minor | major). It is a pure-Go
// leaf library with no IO; consumed by the github-releaser planning step.
//
// The function intentionally implements one Phase 1 quirk: when the
// current version is 0.0.0 (the first-release sentinel), every bump kind
// returns 0.1.0 — including "major". See spec 045 for rationale.
package semver

import (
	"context"
	"strconv"
	"strings"

	"github.com/bborbe/errors"
)

// BumpVersion returns the next version string given current ("vX.Y.Z" or
// "X.Y.Z") and bump (one of "patch", "minor", "major"). The returned
// version is numeric ("X.Y.Z") — the caller composes the final header by
// prepending any "v" prefix.
//
// Special case: when current is 0.0.0 the result is always 0.1.0,
// regardless of bump kind. Major-on-first-release does NOT yield 1.0.0.
//
// Errors are wrapped via github.com/bborbe/errors. Malformed current
// versions produce an error whose message contains "parse version";
// unknown bump kinds produce one containing "invalid bump".
//
// The ctx parameter is used only for error wrapping consistency.
// No IO, deterministic.
func BumpVersion(ctx context.Context, current string, bump string) (string, error) {
	stripped := strings.TrimPrefix(current, "v")
	parts := strings.Split(stripped, ".")
	if len(parts) != 3 {
		return "", errors.Errorf(
			ctx,
			"parse version: %q has %d components, want 3",
			current,
			len(parts),
		)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", errors.Wrapf(ctx, err, "parse version: %q major component", current)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", errors.Wrapf(ctx, err, "parse version: %q minor component", current)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", errors.Wrapf(ctx, err, "parse version: %q patch component", current)
	}

	// Reject negative components.
	if major < 0 || minor < 0 || patch < 0 {
		return "", errors.Errorf(ctx, "parse version: %q has negative component", current)
	}

	// First-release sentinel: every bump from 0.0.0 collapses to 0.1.0.
	if major == 0 && minor == 0 && patch == 0 {
		return "0.1.0", nil
	}

	switch bump {
	case "patch":
		patch++
	case "minor":
		minor++
		patch = 0
	case "major":
		major++
		minor = 0
		patch = 0
	default:
		return "", errors.Errorf(ctx, "invalid bump: %q (want patch|minor|major)", bump)
	}

	return strconv.Itoa(major) + "." + strconv.Itoa(minor) + "." + strconv.Itoa(patch), nil
}

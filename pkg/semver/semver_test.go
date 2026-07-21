// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package semver_test

import (
	"context"

	"github.com/bborbe/github-releaser-agent/pkg/semver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("BumpVersion",
	func(current, bump, wantNext, wantErrSubstr string) {
		next, err := semver.BumpVersion(context.Background(), current, bump)
		if wantErrSubstr == "" {
			Expect(err).NotTo(HaveOccurred())
			Expect(next).To(Equal(wantNext))
		} else {
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(wantErrSubstr))
			Expect(next).To(Equal(""))
		}
	},
	Entry("patch bump from v1.2.6", "v1.2.6", "patch", "1.2.7", ""),
	Entry("minor bump from v1.2.6", "v1.2.6", "minor", "1.3.0", ""),
	Entry("major bump from v1.2.6", "v1.2.6", "major", "2.0.0", ""),
	Entry("no v prefix input tolerated", "1.2.6", "patch", "1.2.7", ""),
	Entry("v0.0.0 patch defaults to 0.1.0", "v0.0.0", "patch", "0.1.0", ""),
	Entry("v0.0.0 minor defaults to 0.1.0", "v0.0.0", "minor", "0.1.0", ""),
	Entry("v0.0.0 major defaults to 0.1.0", "v0.0.0", "major", "0.1.0", ""),
	Entry("malformed current version", "not-a-version", "patch", "", "parse version"),
	Entry("invalid bump kind", "v1.2.3", "giant", "", "invalid bump"),
	Entry("alphabetic minor component", "v1.x.3", "patch", "", "parse version"),
	Entry("negative component rejected", "v-1.2.3", "patch", "", "parse version"),
	Entry("empty bump rejected", "v1.2.3", "", "", "invalid bump"),
	Entry("empty current version rejected", "", "patch", "", "parse version"),
)

var _ = DescribeTable("IsValid",
	func(v string, want bool) {
		Expect(semver.IsValid(v)).To(Equal(want))
	},
	Entry("v1.2.3 → true", "v1.2.3", true),
	Entry("1.2.3 → true", "1.2.3", true),
	Entry("v1.2.3-rc1 → false (pre-release)", "v1.2.3-rc1", false),
	Entry("latest → false", "latest", false),
	Entry("v1.2 → false (only 2 components)", "v1.2", false),
	Entry("v1.2.3.4 → false (4 components)", "v1.2.3.4", false),
	Entry("v-1.2.3 → false (negative-like)", "v-1.2.3", false),
	Entry("empty → false", "", false),
	Entry("v0.0.1 → true", "v0.0.1", true),
)

var _ = DescribeTable("Highest",
	func(names []string, wantTag string, wantOK bool) {
		tag, ok := semver.Highest(names)
		Expect(ok).To(Equal(wantOK))
		if wantOK {
			Expect(tag).To(Equal(wantTag))
		}
	},
	Entry("v0.101.0, v0.101.1, v0.100.9 → v0.101.1",
		[]string{"v0.101.0", "v0.101.1", "v0.100.9"}, "v0.101.1", true),
	Entry("scrambled order → v1.0.0 wins",
		[]string{"v0.9.0", "v1.0.0", "v0.5.0"}, "v1.0.0", true),
	Entry("non-semver skipped → v0.5.0",
		[]string{"latest", "v0.5.0", "nightly"}, "v0.5.0", true),
	Entry("no v prefix preserved → 0.101.1",
		[]string{"0.101.1", "0.101.0"}, "0.101.1", true),
	Entry("empty input → false",
		[]string{}, "", false),
	Entry("all non-semver → false",
		[]string{"latest", "nightly"}, "", false),
	Entry("numeric tie → first encountered (v1.2.3)",
		[]string{"v1.2.3", "1.2.3"}, "v1.2.3", true),
)

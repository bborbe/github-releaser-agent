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

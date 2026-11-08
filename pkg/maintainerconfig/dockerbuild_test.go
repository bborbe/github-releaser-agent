// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintainerconfig_test

import (
	"context"

	"github.com/bborbe/github-releaser-agent/pkg/maintainerconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ParseDockerBuild", func() {
	ctx := context.Background()

	DescribeTable(
		"resolves the opt-in and primary destination",
		func(raw string, expected bool, imageHost string, destinationsEmpty bool) {
			gotExpected, gotHost, gotEmpty, err := maintainerconfig.ParseDockerBuild(
				ctx,
				[]byte(raw),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(gotExpected).To(Equal(expected))
			Expect(gotHost).To(Equal(imageHost))
			Expect(gotEmpty).To(Equal(destinationsEmpty))
		},
		Entry("empty bytes", "", false, "", false),
		Entry("no dockerBuild block", "release:\n  autoRelease: true\n", false, "", false),
		Entry(
			"autoBuild absent",
			"dockerBuild:\n  destinations:\n    - dockerhub\n",
			false,
			"",
			false,
		),
		Entry(
			"autoBuild false",
			"dockerBuild:\n  autoBuild: false\n  destinations:\n    - dockerhub\n",
			false,
			"",
			false,
		),
		Entry(
			"autoBuild true with empty destinations",
			"dockerBuild:\n  autoBuild: true\n  destinations: []\n",
			false,
			"",
			true,
		),
		Entry(
			"dockerhub",
			"dockerBuild:\n  autoBuild: true\n  destinations:\n    - dockerhub\n",
			true,
			"docker.io",
			false,
		),
		Entry(
			"ghcr",
			"dockerBuild:\n  autoBuild: true\n  destinations:\n    - ghcr\n",
			true,
			"ghcr.io",
			false,
		),
		Entry(
			"internal",
			"dockerBuild:\n  autoBuild: true\n  destinations:\n    - internal\n",
			true,
			"docker.prod.nuke.benjamin-borbe.de:443",
			false,
		),
		Entry(
			"primary is the first destination",
			"dockerBuild:\n  autoBuild: true\n  destinations:\n    - ghcr\n    - dockerhub\n",
			true,
			"ghcr.io",
			false,
		),
	)

	It("returns an error for an unknown destination class", func() {
		expected, host, empty, err := maintainerconfig.ParseDockerBuild(
			ctx,
			[]byte("dockerBuild:\n  autoBuild: true\n  destinations:\n    - nowhere\n"),
		)
		Expect(err).To(HaveOccurred())
		Expect(expected).To(BeFalse())
		Expect(host).To(BeEmpty())
		Expect(empty).To(BeFalse())
		Expect(err.Error()).To(ContainSubstring("unknown dockerBuild destination class"))
	})

	It("returns an error for malformed YAML", func() {
		expected, host, empty, err := maintainerconfig.ParseDockerBuild(
			ctx,
			[]byte("dockerBuild: [not-valid"),
		)
		Expect(err).To(HaveOccurred())
		Expect(expected).To(BeFalse())
		Expect(host).To(BeEmpty())
		Expect(empty).To(BeFalse())
	})
})

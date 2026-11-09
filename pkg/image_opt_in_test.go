// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	pkg "github.com/bborbe/github-releaser-agent/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ImageOptIn", func() {
	It("lists every value the ## Review block can carry", func() {
		Expect(pkg.AvailableImageOptIns).To(ConsistOf(
			pkg.ImageOptInAutoBuild,
			pkg.ImageOptInNone,
			pkg.ImageOptInEmptyDestinations,
			pkg.ImageOptInNoConfig,
			pkg.ImageOptInSkipped,
		))
	})

	DescribeTable("Validate accepts every available value",
		func(value pkg.ImageOptIn) {
			Expect(value.Validate(context.Background())).To(Succeed())
			Expect(pkg.AvailableImageOptIns.Contains(value)).To(BeTrue())
		},
		Entry("auto_build", pkg.ImageOptInAutoBuild),
		Entry("none", pkg.ImageOptInNone),
		Entry("empty_destinations", pkg.ImageOptInEmptyDestinations),
		Entry("no_config", pkg.ImageOptInNoConfig),
		Entry("skipped", pkg.ImageOptInSkipped),
	)

	It("rejects an unknown value", func() {
		Expect(pkg.ImageOptIn("bogus").Validate(context.Background())).NotTo(Succeed())
		Expect(pkg.AvailableImageOptIns.Contains(pkg.ImageOptIn("bogus"))).To(BeFalse())
	})

	It("renders as its bare string", func() {
		Expect(pkg.ImageOptInNone.String()).To(Equal("none"))
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"encoding/json"

	pkg "github.com/bborbe/github-releaser-agent/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResultOutput JSON contract", func() {
	It("round-trips a released outcome with Workdir and LocalTag", func() {
		in := pkg.ResultOutput{
			Outcome:   pkg.ResultOutcomeReleased,
			Path:      pkg.ResultPathDirectPush,
			CommitSHA: "abc1234",
			Tag:       "v1.2.8",
			Workdir:   "/tmp/x",
			LocalTag:  "v1.2.8",
		}
		b, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		// Field-name stability: these substrings traverse the JSON encoder
		// boundary the ## Result section serialization crosses. Any rename
		// here is a silent contract break for ai-review (next prompt).
		Expect(string(b)).To(ContainSubstring(`"workdir":"/tmp/x"`))
		Expect(string(b)).To(ContainSubstring(`"local_tag":"v1.2.8"`))
		// Round-trip.
		var out pkg.ResultOutput
		Expect(json.Unmarshal(b, &out)).To(Succeed())
		Expect(out).To(Equal(in))
	})

	It("omits Workdir and LocalTag on a zero value (omitempty fires)", func() {
		in := pkg.ResultOutput{}
		b, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		// omitempty must fire for both new fields when they are empty strings.
		Expect(string(b)).NotTo(ContainSubstring(`"workdir"`))
		Expect(string(b)).NotTo(ContainSubstring(`"local_tag"`))
	})
})

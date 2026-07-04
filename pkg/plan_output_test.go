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

var _ = Describe("PlanOutput JSON contract", func() {
	It("round-trips happy-path (outcome=ready) with all fields", func() {
		in := pkg.PlanOutput{
			Outcome:           pkg.PlanOutcomeReady,
			Bump:              "minor",
			Reasoning:         "feat: stub",
			CurrentVersion:    "v1.7.7",
			NextVersion:       "1.8.0",
			NextVersionHeader: "## v1.8.0",
			HeaderPrefixStyle: "v",
			Bullets:           []string{"feat: stub"},
		}
		b, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		// Snake-case tags survive marshaling
		Expect(string(b)).To(ContainSubstring(`"outcome":"ready"`))
		Expect(string(b)).To(ContainSubstring(`"bump":"minor"`))
		Expect(string(b)).To(ContainSubstring(`"current_version":"v1.7.7"`))
		Expect(string(b)).To(ContainSubstring(`"next_version":"1.8.0"`))
		Expect(string(b)).To(ContainSubstring(`"next_version_header":"## v1.8.0"`))
		Expect(string(b)).To(ContainSubstring(`"header_prefix_style":"v"`))
		// Escalation fields omitted
		Expect(string(b)).NotTo(ContainSubstring(`"reason"`))
		Expect(string(b)).NotTo(ContainSubstring(`"precondition_failed"`))
		// Round-trip
		var out pkg.PlanOutput
		Expect(json.Unmarshal(b, &out)).To(Succeed())
		Expect(out).To(Equal(in))
	})

	It("omits happy-path fields on outcome=needs_input", func() {
		in := pkg.PlanOutput{
			Outcome:            pkg.PlanOutcomeNeedsInput,
			Reason:             "Unreleased is not the first ## section",
			PreconditionFailed: pkg.PreconditionP1UnreleasedNotFirst,
			CurrentVersion:     "v1.2.6",
		}
		b, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"outcome":"needs_input"`))
		Expect(string(b)).To(ContainSubstring(`"reason":"Unreleased is not the first ## section"`))
		Expect(string(b)).To(ContainSubstring(`"precondition_failed":"P1_unreleased_not_first"`))
		Expect(string(b)).To(ContainSubstring(`"current_version":"v1.2.6"`))
		// Happy-path-only fields absent
		Expect(string(b)).NotTo(ContainSubstring(`"bump"`))
		Expect(string(b)).NotTo(ContainSubstring(`"reasoning"`))
		Expect(string(b)).NotTo(ContainSubstring(`"next_version"`))
		Expect(string(b)).NotTo(ContainSubstring(`"next_version_header"`))
		Expect(string(b)).NotTo(ContainSubstring(`"header_prefix_style"`))
		Expect(string(b)).NotTo(ContainSubstring(`"bullets"`))
	})

	It("round-trips failed outcome with invalid_config details", func() {
		in := pkg.PlanOutput{
			Outcome:        pkg.PlanOutcomeFailed,
			ErrorCategory:  pkg.ErrorCategoryInvalidConfig,
			InvalidField:   "release.changelogRewrite",
			InvalidValue:   "yes",
			CurrentVersion: "v1.2.6",
		}
		b, err := json.Marshal(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"outcome":"failed"`))
		Expect(string(b)).To(ContainSubstring(`"error_category":"invalid_config"`))
		Expect(string(b)).To(ContainSubstring(`"invalid_field":"release.changelogRewrite"`))
		Expect(string(b)).To(ContainSubstring(`"invalid_value":"yes"`))
		Expect(string(b)).To(ContainSubstring(`"current_version":"v1.2.6"`))
		// Failure path: `changelog_rewrite` token is OMITTED (omitempty
		// + the pointer is nil on the failure path).
		Expect(string(b)).NotTo(ContainSubstring("changelog_rewrite"))
		// Round-trip.
		var out pkg.PlanOutput
		Expect(json.Unmarshal(b, &out)).To(Succeed())
		Expect(out).To(Equal(in))
	})
})

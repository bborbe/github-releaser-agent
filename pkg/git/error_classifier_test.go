// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"errors"

	"github.com/bborbe/github-releaser-agent/pkg/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ClassifyError", func() {
	DescribeTable("maps git stderr fragments to the closed enum",
		func(input error, expected git.ErrorCategory) {
			Expect(git.ClassifyError(input)).To(Equal(expected))
		},
		Entry("auth — Authentication failed",
			errors.New("fatal: Authentication failed for 'https://github.com/x/y.git/'"),
			git.ErrorCategoryAuth),
		Entry(
			"auth — could not read Username",
			errors.New(
				"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			),
			git.ErrorCategoryAuth,
		),
		Entry(
			"repo_not_found — 404 on clone",
			errors.New(
				"remote: Repository not found.\nfatal: repository 'https://github.com/x/missing.git/' not found",
			),
			git.ErrorCategoryRepoNotFound,
		),
		Entry("tag_collision — annotated tag already exists",
			errors.New("fatal: tag 'v1.2.7' already exists"),
			git.ErrorCategoryTagCollision),
		Entry(
			"protected_branch_rejected — GH006 on push",
			errors.New(
				"remote: error: GH006: Protected branch update failed for refs/heads/master.\nremote: error: At least 1 approving review is required by reviewers with write access",
			),
			git.ErrorCategoryProtectedBranchRejected,
		),
		Entry(
			"push_non_fast_forward — remote advanced during release",
			errors.New(
				"! [rejected] master -> master (non-fast-forward)\nerror: failed to push some refs to 'https://github.com/x/y.git'",
			),
			git.ErrorCategoryPushNonFastForward,
		),
		Entry("protected_branch_rejected — required status checks",
			errors.New("remote: error: branch master: required status checks have failed"),
			git.ErrorCategoryProtectedBranchRejected),
		Entry(
			"push_non_fast_forward — remote contains work",
			errors.New(
				"! [rejected] master -> master\nUpdates were rejected because the remote contains work that you do not have locally",
			),
			git.ErrorCategoryPushNonFastForward,
		),
		Entry("unknown — unrecognized message",
			errors.New("fatal: cosmic ray flipped a bit"),
			git.ErrorCategoryUnknown),
	)

	// The two filesystem/changelog categories are declared on the enum but
	// ClassifyError never returns them — they are set directly by the
	// execution step. This test documents that contract so a future
	// refactor doesn't accidentally introduce a substring match for them.
	It("never returns changelog_missing from ClassifyError", func() {
		// changelog_missing — declared on enum but emitted by execution step at fs layer
		Expect(git.ClassifyError(errors.New("CHANGELOG.md: no such file or directory"))).
			NotTo(Equal(git.ErrorCategoryChangelogMissing))
	})
	It("never returns unreleased_not_found from ClassifyError", func() {
		// unreleased_not_found — declared on enum but emitted by changelog pkg
		Expect(git.ClassifyError(errors.New("Unreleased header not found"))).
			NotTo(Equal(git.ErrorCategoryUnreleasedNotFound))
	})
	It("never returns plugin_manifest_invalid from ClassifyError", func() {
		// plugin_manifest_invalid — declared on enum but emitted by execution step
		// at the plugin manifest package layer; never reaches ClassifyError because
		// git never runs when the bump fails.
		Expect(git.ClassifyError(errors.New("plugin.json version field not found"))).
			NotTo(Equal(git.ErrorCategoryPluginManifestInvalid))
	})
	It("ErrorCategoryPluginManifestInvalid has string value plugin_manifest_invalid", func() {
		Expect(string(git.ErrorCategoryPluginManifestInvalid)).To(Equal("plugin_manifest_invalid"))
	})

	It(
		"returns empty-string sentinel on nil — distinguishes success from 'actually-unknown stderr'",
		func() {
			Expect(git.ClassifyError(nil)).To(Equal(git.ErrorCategory("")))
			Expect(git.ClassifyError(nil)).NotTo(Equal(git.ErrorCategoryUnknown))
		},
	)
})

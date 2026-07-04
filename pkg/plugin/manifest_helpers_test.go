// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package plugin

import (
	"bytes"
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("scopeTracker", func() {
	It("starts in the zero state (depth 0, no scope flags)", func() {
		var s scopeTracker
		Expect(s.depth).To(Equal(0))
		Expect(s.inMetadata).To(BeFalse())
		Expect(s.inPlugin).To(BeFalse())
		Expect(s.inPluginsArray).To(BeFalse())
		Expect(s.inVersionScope()).To(BeFalse())
	})

	It("enters metadata when seeing `\"metadata\": {` at depth 1", func() {
		var s scopeTracker
		s.depth = 1
		s.update(`  "metadata": {`, `"metadata": {`)
		Expect(s.inMetadata).To(BeTrue())
		Expect(s.inPlugin).To(BeFalse())
		Expect(s.inPluginsArray).To(BeFalse())
		Expect(s.inVersionScope()).To(BeTrue())
	})

	It("enters plugin when seeing `{` inside plugins array at depth 2", func() {
		var s scopeTracker
		s.depth = 2
		s.inPluginsArray = true
		s.update(`    {`, `{`)
		Expect(s.inPlugin).To(BeTrue())
		Expect(s.inMetadata).To(BeFalse())
		Expect(s.inVersionScope()).To(BeTrue())
	})

	It("exits plugin when seeing `}` at depth 2", func() {
		var s scopeTracker
		s.depth = 2
		s.inPlugin = true
		s.inMetadata = false
		s.update(`    }`, `}`)
		Expect(s.inPlugin).To(BeFalse())
		Expect(s.inMetadata).To(BeFalse())
	})

	It("exits plugins array when seeing `]` at depth 2 with inPluginsArray set", func() {
		var s scopeTracker
		s.depth = 2
		s.inPluginsArray = true
		s.inPlugin = true
		s.update(`  ]`, `]`)
		Expect(s.inPluginsArray).To(BeFalse())
		Expect(s.inPlugin).To(BeFalse())
	})

	It("fully exits when depth returns to 0", func() {
		var s scopeTracker
		s.depth = 1
		s.inMetadata = true
		s.inPlugin = true
		s.inPluginsArray = true
		s.update(`}`, `}`)
		Expect(s.depth).To(Equal(0))
		Expect(s.inMetadata).To(BeFalse())
		Expect(s.inPlugin).To(BeFalse())
		Expect(s.inPluginsArray).To(BeFalse())
	})

	It("inVersionScope is true when only inMetadata is set", func() {
		var s scopeTracker
		s.inMetadata = true
		s.inPlugin = false
		Expect(s.inVersionScope()).To(BeTrue())
	})

	It("inVersionScope is true when only inPlugin is set", func() {
		var s scopeTracker
		s.inMetadata = false
		s.inPlugin = true
		Expect(s.inVersionScope()).To(BeTrue())
	})

	It("inVersionScope is false when both flags are false", func() {
		var s scopeTracker
		s.inMetadata = false
		s.inPlugin = false
		Expect(s.inVersionScope()).To(BeFalse())
	})
})

var _ = Describe("lineHasVersionKey", func() {
	It("matches `\"version\": \"x\"` after `{`", func() {
		Expect(lineHasVersionKey(`{ "version": "x"`)).To(BeTrue())
	})

	It("matches `\"version\" : \"x\"` with a space before the colon", func() {
		Expect(lineHasVersionKey(`{ "version" : "x"`)).To(BeTrue())
	})

	It("does NOT match when 'version' appears inside a string value", func() {
		Expect(lineHasVersionKey(`"description": "version: x"`)).To(BeFalse())
	})

	It("matches after a comma", func() {
		Expect(lineHasVersionKey(`, "version": "x"`)).To(BeTrue())
	})

	It("returns false on a line with no version key", func() {
		Expect(lineHasVersionKey(`"name": "x"`)).To(BeFalse())
	})
})

var _ = Describe("writeLine", func() {
	It("writes line + '\\n' to the buffer", func() {
		var buf bytes.Buffer
		writeLine(&buf, "hello")
		Expect(buf.String()).To(Equal("hello\n"))
	})

	It("writes an empty line as just '\\n'", func() {
		var buf bytes.Buffer
		writeLine(&buf, "")
		Expect(buf.String()).To(Equal("\n"))
	})

	It("two consecutive calls produce the expected joined output", func() {
		var buf bytes.Buffer
		writeLine(&buf, "a")
		writeLine(&buf, "b")
		Expect(buf.String()).To(Equal("a\nb\n"))
	})
})

var _ = Describe("extractExistingVersion", func() {
	It("parses a quoted semver value", func() {
		keyPart, value, trailing, indent, quoted, err := extractExistingVersion(
			context.Background(),
			`  "version": "0.9.12"`,
			"plugin.json",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal("0.9.12"))
		Expect(quoted).To(BeTrue())
		Expect(keyPart).To(Equal(`"version":`))
		Expect(indent).To(Equal("  "))
		Expect(trailing).To(Equal(""))
	})

	It("parses an unquoted semver value", func() {
		keyPart, value, trailing, indent, quoted, err := extractExistingVersion(
			context.Background(),
			`"version": 0.9.12`,
			"plugin.json",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(value).To(Equal("0.9.12"))
		Expect(quoted).To(BeFalse())
		Expect(keyPart).To(Equal(`"version":`))
		Expect(indent).To(Equal(""))
		Expect(trailing).To(Equal(""))
	})

	It("returns an error on a non-semver quoted value", func() {
		_, _, _, _, _, err := extractExistingVersion(
			context.Background(),
			`"version": "latest"`,
			"plugin.json",
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not a semver-shaped string"))
	})

	It("returns an error on a missing colon", func() {
		// A line with "version" but no colon (no `"version":` substring)
		// surfaces a "no version key" error.
		_, _, _, _, _, err := extractExistingVersion(
			context.Background(),
			`"version" `,
			"plugin.json",
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no version key"))
	})
})

var _ = Describe("formatRewrittenVersion", func() {
	It("formats a quoted value with the new version", func() {
		got := formatRewrittenVersion(
			"  ",
			`"version":`,
			"0.10.0",
			"",
			true,
		)
		Expect(got).To(Equal(`  "version": "0.10.0"`))
	})

	It("formats an unquoted value with the new version", func() {
		got := formatRewrittenVersion(
			"  ",
			`"version":`,
			"0.10.0",
			"",
			false,
		)
		Expect(got).To(Equal(`  "version": 0.10.0`))
	})

	It("preserves trailing comma after the value", func() {
		got := formatRewrittenVersion(
			`  `,
			`"version":`,
			"0.10.0",
			`,`,
			true,
		)
		Expect(got).To(Equal(`  "version": "0.10.0",`))
	})
})

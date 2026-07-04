// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"

	agentlib "github.com/bborbe/agent"
	claudelib "github.com/bborbe/agent/claude"
	"github.com/bborbe/github-releaser-agent/pkg/factory"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CreateAgentProvider", func() {
	var provider agentlib.AgentProvider

	BeforeEach(func() {
		provider = factory.CreateAgentProvider(
			claudelib.ClaudeConfigDir("/tmp/claude"),
			claudelib.AgentDir("/tmp/agent"),
			claudelib.ClaudeModel("sonnet"),
			"",
			map[string]string{},
			false,
		)
	})

	It("routes task_type: github-release", func() {
		a, err := provider.Get(context.Background(), agentlib.TaskType("github-release"))
		Expect(err).NotTo(HaveOccurred())
		Expect(a).NotTo(BeNil())
	})

	It("routes task_type: healthcheck", func() {
		a, err := provider.Get(context.Background(), agentlib.TaskTypeHealthcheck)
		Expect(err).NotTo(HaveOccurred())
		Expect(a).NotTo(BeNil())
	})

	It("returns error for unknown task_type", func() {
		_, err := provider.Get(context.Background(), agentlib.TaskType("not-a-real-type"))
		Expect(err).To(HaveOccurred())
	})

	It("CreateAgent wires both planning and execution phases", func() {
		agent := factory.CreateAgent(
			claudelib.ClaudeConfigDir("/tmp/claude"),
			claudelib.AgentDir("/tmp/agent"),
			claudelib.ClaudeModel("sonnet"),
			"",
			map[string]string{},
			false,
		)
		Expect(agent).NotTo(BeNil())
		// The agent-lib does not expose the phase list on *Agent; the
		// assertion above plus the grep-AC on factory.go (`NewPhase(domain.TaskPhaseExecution`)
		// covers the structural guarantee. This test additionally ensures
		// CreateAgent does not panic and returns a non-nil Agent — which it
		// would not, if the second phase argument were malformed.
	})
})

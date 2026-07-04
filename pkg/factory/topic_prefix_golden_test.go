// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"

	agentlib "github.com/bborbe/agent"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/github-releaser-agent/pkg/factory"
	libkafkamocks "github.com/bborbe/kafka/mocks"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Golden regression test locking down the exact Kafka topic names produced
// by CreateKafkaResultDeliverer for the TopicPrefix values that matter in
// production: "develop" (dev stage), "master" (prod stage), and "" (no
// prefix). The literal suffix "request" comes from
// cdb.SchemaID.CommandTopic → cdb.BuildTopic(schemaID, prefix, "request")
// (github.com/bborbe/cqrs@v0.6.0/cdb/cdb_schema-id.go +
// cdb_build-topic.go), and the schema is agentlib.TaskV1SchemaID{Group:
// "agent", Kind: "task", Version: "v1"} (github.com/bborbe/agent@v0.72.0/
// agent_cdb-schema.go). If this test's expected literals ever need to
// change, the topic-prefix wiring has regressed — do not "fix" the test,
// fix the wiring.
var _ = Describe("CreateKafkaResultDeliverer topic naming (golden)", func() {
	currentDateTime := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		return libtime.DateTime{}
	})

	deliverAndCaptureTopic := func(topicPrefix string) string {
		syncProducer := &libkafkamocks.KafkaSyncProducer{}
		deliverer := factory.CreateKafkaResultDeliverer(
			syncProducer,
			base.TopicPrefix(topicPrefix),
			agentlib.TaskIdentifier("task-123"),
			"---\nstatus: in_progress\nphase: execution\n---\nbody",
			currentDateTime,
		)
		err := deliverer.DeliverResult(context.Background(), agentlib.AgentResultInfo{
			Status:    agentlib.AgentStatusDone,
			NextPhase: "done",
			Output:    "---\nstatus: in_progress\nphase: execution\n---\nbody",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(syncProducer.SendMessageCallCount()).To(Equal(1))
		_, msg := syncProducer.SendMessageArgsForCall(0)
		return msg.Topic
	}

	It("prefixes with develop for the dev stage", func() {
		Expect(deliverAndCaptureTopic("develop")).To(Equal("develop-agent-task-v1-request"))
	})

	It("prefixes with master for the prod stage", func() {
		Expect(deliverAndCaptureTopic("master")).To(Equal("master-agent-task-v1-request"))
	})

	It("has no prefix when TopicPrefix is empty", func() {
		Expect(deliverAndCaptureTopic("")).To(Equal("agent-task-v1-request"))
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command maintainer-agent-github-releaser is the Kafka entry point for the
// github-releaser agent — spawned as a K8s Job by task/executor with
// TASK_CONTENT + TASK_ID + PHASE + KAFKA_BROKERS env. The agent consumes one
// release task per Job invocation.
//
// Phase 2 graduation of the validated /github-release-repo slash command
// (Phase 1). See [[GitHub Release Agent Phase 1 Learnings]] for what
// carries from the prototype.
//
// Planning phase wiring per spec 047. Execution + ai_review phases ship in
// separate specs.
package main

import (
	"context"
	"os"
	"time"

	agentlib "github.com/bborbe/agent"
	claudelib "github.com/bborbe/agent/claude"
	delivery "github.com/bborbe/agent/delivery"
	libmetrics "github.com/bborbe/agent/metrics"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-releaser-agent/pkg"
	"github.com/bborbe/github-releaser-agent/pkg/factory"
	"github.com/bborbe/github-releaser-agent/pkg/githubauth"
	libkafka "github.com/bborbe/kafka"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

const agentName = "github-releaser-agent"

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	SentryDSN   string `required:"false" arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"SentryDSN"    display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"Sentry Proxy"`

	// Claude Code CLI configuration
	ClaudeConfigDir claudelib.ClaudeConfigDir `required:"false" arg:"claude-config-dir" env:"CLAUDE_CONFIG_DIR" usage:"Claude Code config directory"         default:"~/.claude"`
	AgentDir        claudelib.AgentDir        `required:"false" arg:"agent-dir"         env:"AGENT_DIR"         usage:"Agent directory with .claude/ config" default:"agent"`

	// Anthropic-compatible provider routing.
	AnthropicBaseURL   string                `required:"false" arg:"anthropic-base-url"   env:"ANTHROPIC_BASE_URL"   usage:"Anthropic-compatible API base URL"`
	AnthropicAuthToken string                `required:"false" arg:"anthropic-auth-token" env:"ANTHROPIC_AUTH_TOKEN" usage:"Bearer token for ANTHROPIC_BASE_URL"                                  display:"length"`
	AnthropicModel     claudelib.ClaudeModel `required:"false" arg:"anthropic-model"      env:"ANTHROPIC_MODEL"      usage:"Model name; also exposed to the claude subprocess as ANTHROPIC_MODEL"                  default:"sonnet"`

	// Per-run override for the spec 060 major-bump guard. When true, a
	// bump verdict of `major` proceeds to execution even when the
	// target repo's `.maintainer.yaml` does not have
	// `release.allowMajorBump: true`. Equivalent opt-in semantics;
	// either source is sufficient. Default false; the planning step
	// emits `glog.V(2) --allow-major override` so kubectl-logs greps
	// surface operator overrides.
	AllowMajor bool `required:"false" arg:"allow-major" env:"ALLOW_MAJOR" usage:"Per-run override: allow 'major' bump verdict even if repo has no release.allowMajorBump opt-in" default:"false"`

	// Task content from agent pipeline (raw markdown injected by task/executor).
	TaskContent string `required:"true" arg:"task-content" env:"TASK_CONTENT" usage:"Raw release task markdown"`

	// Branch for Kafka result delivery (dev / prod).
	Branch base.Branch `required:"true" arg:"branch" env:"BRANCH" usage:"branch"`

	// TopicPrefix selects the Kafka topic prefix used for CQRS topic construction
	// (e.g. "develop" / "master"); independent of Branch, which remains the
	// stage/image-tag identifier. Empty means unprefixed topics.
	TopicPrefix base.TopicPrefix `required:"false" arg:"topic-prefix" env:"TOPIC_PREFIX" usage:"Kafka topic prefix for CQRS topic construction"`

	// Phase to run (planning | execution | ai_review). Canonical values; CRD literal match.
	Phase domain.TaskPhase `required:"false" arg:"phase" env:"PHASE" usage:"Agent phase: planning | execution | ai_review" default:"planning"`

	// Kafka delivery (optional — only active when TASK_ID is set).
	KafkaBrokers libkafka.Brokers        `required:"false" arg:"kafka-brokers" env:"KAFKA_BROKERS" usage:"Comma separated list of Kafka brokers"`
	TaskID       agentlib.TaskIdentifier `required:"false" arg:"task-id"       env:"TASK_ID"       usage:"Agent task identifier for publishing results back to task controller"`

	// GitHub App authentication. AppID + InstallationID + (PEMKeyFile or
	// PEMKey) are required; the pod mints an installation access token at
	// startup and forwards it to the Claude/git subprocess (see Run()).
	AppID          int64  `required:"false" arg:"app-id"          env:"APP_ID"          usage:"GitHub App ID (numeric)"`
	InstallationID int64  `required:"false" arg:"installation-id" env:"INSTALLATION_ID" usage:"GitHub App Installation ID (numeric)"`
	PEMKeyFile     string `required:"false" arg:"pem-key-file"    env:"PEM_KEY_FILE"    usage:"Path to the GitHub App private key (PEM file mounted from k8s Secret)"`
	PEMKey         string `required:"false" arg:"pem-key"         env:"PEM_KEY"         usage:"GitHub App private key (PEM) as env var content; mutually exclusive with PEM_KEY_FILE" display:"length"`

	PushgatewayURL string `required:"false" arg:"pushgateway-url" env:"PUSHGATEWAY_URL" usage:"Prometheus PushGateway URL"          default:"http://pushgateway:9090"`
	TaskType       string `required:"false" arg:"task-type"       env:"TASK_TYPE"       usage:"Task type label for metric grouping" default:"unknown"`
}

func (a *application) Run(ctx context.Context, _ libsentry.Client) error {
	registry := prometheus.NewRegistry()
	jobMetrics := libmetrics.NewJobMetrics(registry, libtime.NewCurrentDateTime())
	pusher := push.New(a.PushgatewayURL, libmetrics.BuildJobMetricsName(agentName)).
		Grouping("agent", agentName).
		Grouping("task_type", a.TaskType).
		Collector(registry)
	defer func() {
		if err := pusher.PushContext(ctx); err != nil {
			glog.Warningf("prometheus push failed: %v", err)
			return
		}
		glog.V(2).Infof("prometheus push completed")
	}()
	start := libtime.NewCurrentDateTime().Now().Time()
	glog.V(2).Infof("%s started phase=%s", agentName, a.Phase)

	resolvedToken, err := githubauth.Resolve(ctx, githubauth.Config{
		AppID:          a.AppID,
		InstallationID: a.InstallationID,
		PEMKeyFile:     a.PEMKeyFile,
		PEMKey:         a.PEMKey,
	})
	if err != nil {
		jobMetrics.RecordRun(agentlib.AgentStatusFailed)
		jobMetrics.RecordDuration(time.Since(start))
		return errors.Wrap(ctx, err, "resolve github auth")
	}

	deliverer, cleanup, err := a.createDeliverer(ctx)
	if err != nil {
		jobMetrics.RecordRun(agentlib.AgentStatusFailed)
		jobMetrics.RecordDuration(time.Since(start))
		return errors.Wrap(ctx, err, "create deliverer")
	}
	defer cleanup()

	env := pkg.BuildEnv(
		resolvedToken,
		a.AnthropicBaseURL,
		a.AnthropicAuthToken,
		a.AnthropicModel.String(),
		a.AllowMajor,
	)
	provider := factory.CreateAgentProvider(
		a.ClaudeConfigDir,
		a.AgentDir,
		a.AnthropicModel,
		resolvedToken,
		env,
		a.AllowMajor,
	)
	agent, err := provider.Get(ctx, agentlib.TaskType(a.TaskType))
	if err != nil {
		jobMetrics.RecordRun(agentlib.AgentStatusFailed)
		jobMetrics.RecordDuration(time.Since(start))
		return errors.Wrap(ctx, err, "select agent for task_type")
	}

	result, err := agent.Run(ctx, a.Phase, a.TaskContent, deliverer)
	if err != nil {
		jobMetrics.RecordRun(agentlib.AgentStatusFailed)
		jobMetrics.RecordDuration(time.Since(start))
		return errors.Wrap(ctx, err, "agent run failed")
	}
	jobMetrics.RecordRun(result.Status)
	jobMetrics.RecordDuration(time.Since(start))
	return agentlib.PrintResult(ctx, result)
}

// createDeliverer builds the Kafka deliverer when TASK_ID is set,
// otherwise returns the noop deliverer (for local-pod debugging without
// Kafka).
func (a *application) createDeliverer(
	ctx context.Context,
) (agentlib.ResultDeliverer, func(), error) {
	if a.TaskID == "" {
		glog.V(2).Infof("TASK_ID not set, skipping task result publishing")
		return delivery.NewNoopResultDeliverer(), func() {}, nil
	}
	if len(a.KafkaBrokers) == 0 {
		return nil, nil, errors.Errorf(ctx, "KAFKA_BROKERS must be set when TASK_ID is set")
	}
	syncProducer, err := libkafka.NewSyncProducerWithName(
		ctx,
		a.KafkaBrokers,
		"agent-github-releaser",
	)
	if err != nil {
		return nil, nil, errors.Wrap(ctx, err, "create kafka sync producer")
	}
	cleanup := func() {
		if err := syncProducer.Close(); err != nil {
			glog.Warningf("close sync producer failed: %v", err)
		}
	}
	currentDateTime := libtime.NewCurrentDateTime()
	deliverer := factory.CreateDeliverer(
		syncProducer,
		a.TaskID,
		a.TopicPrefix,
		a.TaskContent,
		currentDateTime,
	)
	return deliverer, cleanup, nil
}

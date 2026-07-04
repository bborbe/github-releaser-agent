// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command run-task is the local-CLI entry point for
// maintainer-agent-github-releaser.
//
// Reads a markdown task file from disk, runs the agent against it, and
// writes the updated content back to the same file. Mirrors the Kafka
// entry point (../../main.go) but uses file I/O instead of Kafka/CQRS.
package main

import (
	"context"
	"os"

	agentlib "github.com/bborbe/agent"
	claudelib "github.com/bborbe/agent/claude"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-releaser-agent/pkg"
	"github.com/bborbe/github-releaser-agent/pkg/factory"
	"github.com/bborbe/github-releaser-agent/pkg/githubauth"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	"github.com/bborbe/vault-cli/pkg/domain"
)

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	SentryDSN   string `required:"false" arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"SentryDSN"    display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"Sentry Proxy"`

	ClaudeConfigDir claudelib.ClaudeConfigDir `required:"false" arg:"claude-config-dir" env:"CLAUDE_CONFIG_DIR" usage:"Claude Code config directory"         default:"~/.claude"`
	AgentDir        claudelib.AgentDir        `required:"false" arg:"agent-dir"         env:"AGENT_DIR"         usage:"Agent directory with .claude/ config" default:"agent"`

	Phase    domain.TaskPhase `required:"false" arg:"phase"     env:"PHASE"     usage:"Agent phase: planning | execution | ai_review" default:"planning"`
	TaskType string           `required:"false" arg:"task-type" env:"TASK_TYPE" usage:"Task type for provider dispatch"               default:"github-release"`

	TaskFilePath string `required:"true" arg:"task-file" env:"TASK_FILE" usage:"Path to the markdown task file"`

	// GitHub App authentication. AppID + InstallationID + (PEMKeyFile or
	// PEMKey) are required; the pod mints an installation access token at
	// startup and forwards it to the Claude/git subprocess (see Run()).
	AppID          int64  `required:"false" arg:"app-id"          env:"APP_ID"          usage:"GitHub App ID (numeric)"`
	InstallationID int64  `required:"false" arg:"installation-id" env:"INSTALLATION_ID" usage:"GitHub App Installation ID (numeric)"`
	PEMKeyFile     string `required:"false" arg:"pem-key-file"    env:"PEM_KEY_FILE"    usage:"Path to the GitHub App private key (PEM file mounted from k8s Secret)"`
	PEMKey         string `required:"false" arg:"pem-key"         env:"PEM_KEY"         usage:"GitHub App private key (PEM) as env var content; mutually exclusive with PEM_KEY_FILE" display:"length"`

	AnthropicBaseURL   string                `required:"false" arg:"anthropic-base-url"   env:"ANTHROPIC_BASE_URL"   usage:"Anthropic-compatible API base URL"`
	AnthropicAuthToken string                `required:"false" arg:"anthropic-auth-token" env:"ANTHROPIC_AUTH_TOKEN" usage:"Bearer token for ANTHROPIC_BASE_URL" display:"length"`
	AnthropicModel     claudelib.ClaudeModel `required:"false" arg:"anthropic-model"      env:"ANTHROPIC_MODEL"      usage:"Model name"                                           default:"sonnet"`

	// Per-run override for the spec 060 major-bump guard. When true, a
	// bump verdict of `major` proceeds to execution even when the
	// target repo's `.maintainer.yaml` does not have
	// `release.allowMajorBump: true`. Equivalent opt-in semantics;
	// either source is sufficient. Default false; the planning step
	// emits `glog.V(2) --allow-major override` so kubectl-logs greps
	// surface operator overrides.
	AllowMajor bool `required:"false" arg:"allow-major" env:"ALLOW_MAJOR" usage:"Per-run override: allow 'major' bump verdict even if repo has no release.allowMajorBump opt-in" default:"false"`
}

func (a *application) Run(ctx context.Context, _ libsentry.Client) error {
	resolvedToken, err := githubauth.Resolve(ctx, githubauth.Config{
		AppID:          a.AppID,
		InstallationID: a.InstallationID,
		PEMKeyFile:     a.PEMKeyFile,
		PEMKey:         a.PEMKey,
	})
	if err != nil {
		return errors.Wrap(ctx, err, "resolve github auth")
	}

	taskContent, err := os.ReadFile(
		a.TaskFilePath,
	) // #nosec G304 -- filePath from trusted CLI input
	if err != nil {
		return errors.Wrap(ctx, err, "read task file")
	}

	deliverer := factory.CreateFileResultDeliverer(a.TaskFilePath)

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
		return errors.Wrap(ctx, err, "select agent for task_type")
	}
	result, err := agent.Run(ctx, a.Phase, string(taskContent), deliverer)
	if err != nil {
		return errors.Wrap(ctx, err, "agent run failed")
	}
	return agentlib.PrintResult(ctx, result)
}

include ../../../../Makefile.variables

# cmd binaries are CLI tools (installed via `go install`), not k8s services.
# Stub buca/apply so the parent Makefile.folder recursion succeeds as a no-op
# instead of failing with "No rule to make target 'apply'".
.PHONY: buca
buca:
	@:

.PHONY: apply
apply:
	@:

TASK_FILE ?= dummy-task.md
BRANCH := dev
CLAUDE_CONFIG_DIR ?= $(HOME)/.claude-agent
AGENT_DIR ?= $(shell cd ../.. && pwd)/agent
MODEL ?= sonnet

# Test fixture for local development.
# Override GH_TOKEN / TASK_FILE to target a different scenario.
GH_TOKEN ?= $(shell gh auth token)

run-dummy-task: $(TASK_FILE)
	@GH_TOKEN=$$(GH_TOKEN) go run -mod=mod . \
		-task-file=$(TASK_FILE) \
		-branch=$(BRANCH) \
		-agent-dir=$(AGENT_DIR) \
		-claude-config-dir=$(CLAUDE_CONFIG_DIR) \
		-model=$(MODEL) \
		-v=2

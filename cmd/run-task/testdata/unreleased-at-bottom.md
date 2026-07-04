---
status: in_progress
phase: planning
assignee: github-releaser-agent
task_type: github-release
repo: bborbe/example
clone_url: https://github.com/bborbe/example.git
ref: branch-with-unreleased-at-bottom
current_version: v1.2.6
task_identifier: gh-release-bborbe-example-bad-001
---

# Release task — P1 escalation fixture

Drives the planning phase against a CHANGELOG where `## Unreleased` is NOT the first `##` heading. Expected outcome: `## Plan` with `outcome: needs_input`, `assignee` cleared, `previous_assignee: github-releaser-agent`.

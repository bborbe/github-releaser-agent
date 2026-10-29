---
status: completed
summary: 'Two-run chain spec drives the parked result through the real Kafka deliverer and asserts the published task resolves to status: in_progress / phase: human_review; ## Unreleased changelog entry added. Implemented directly in the worktree after the daemon died mid-011, not by a container run.'
spec: [004-bug-ai-review-closes-rejected-release-as-completed]
created: "2026-10-28T11:02:40Z"
queued: "2026-10-28T11:18:20Z"
completed: "2026-10-29T21:12:00Z"

---

# Prove the two-run chain resolves to in_progress / human_review and record the fix in the changelog

<summary>
- A new end-to-end spec runs the review step twice over the same task page and publishes the second result through the real Kafka deliverer.
- The published task record resolves to `status: in_progress` / `phase: human_review` — never `completed` / `done` — which is the whole point of the fix.
- The assertion is made on the message the deliverer actually sends, not on the step's return value, so a change in the status mapping cannot pass unnoticed.
- The changelog gains an `## Unreleased` section, in the position the release bot expects, with one bullet describing the fix.
- The full precommit gate is green, so the repository is ready to release itself.
- This is the last of three prompts; it lands after the decision and the reporting.
</summary>

<objective>
Close spec 004 with the two pieces that cannot exist earlier: an integration spec that drives the parked result through the real Kafka deliverer and reads the published task object's frontmatter, and the `## Unreleased` changelog entry. The deliverer-layer spec is the one that proves the record stops reading "completed" — the step's own return value is only an intermediate. The deliverer-layer spec is the one that proves the record stops reading "completed" — the step's own return value is only an intermediate.
</objective>

<context>
Read `CLAUDE.md` (Releasing section) and `docs/dod.md` (Documentation section — it fixes the required position of `## Unreleased`), and `docs/releasing-github-releaser-agent.md` (this repo is self-released: the next release renames `## Unreleased`, so the entry must be a bullet under that exact heading and nothing else may be renamed).

Read these files BEFORE editing:
- `pkg/factory/factory.go` — `CreateKafkaResultDeliverer(syncProducer libkafka.SyncProducer, topicPrefix base.TopicPrefix, taskID agentlib.TaskIdentifier, originalContent string, currentDateTime libtime.CurrentDateTimeGetter) agentlib.ResultDeliverer`. It is pure composition over `delivery.NewKafkaResultDeliverer`.
- `pkg/factory/topic_prefix_golden_test.go` — the existing fake-producer pattern this spec follows: `&libkafkamocks.KafkaSyncProducer{}`, `deliverer.DeliverResult(...)`, `syncProducer.SendMessageCallCount()`, `_, msg := syncProducer.SendMessageArgsForCall(0)`, and the `libtime.CurrentDateTimeGetterFunc` stub.
- `pkg/steps_ai_review_test.go` — the top-of-`Describe` helper block (`runStep`, `taskWithFailedResult()` as the first two prompts extended it) and the `Context("short-circuit remote decision (spec 004)")` block added by prompt 1 (prompt 2 adds its third `It` to it).
- `CHANGELOG.md` — the preamble ends after the SemVer line; the first `## ` header today is `## v0.6.1` and there is no `## Unreleased` section yet. Note the existing bullet style (`- fix: ...`, `- chore: ...`).

What the deliverer does with the result (verify by reading `delivery/result-deliverer.go` in `github.com/bborbe/agent@v0.112.0`, `applyResultFrontmatter`): `AgentStatusDone` with `NextPhase: "human_review"` writes `status: in_progress` and `phase: human_review`; only a resolved phase of `done` writes `status: completed`. The published message value is `json.Marshal(commandObject.Command)` — a `base.Command` whose `Data` field is the task event.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-cqrs.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
</context>

<requirements>

## 1. Add the deliverer-layer spec

Add `Context("two-run chain through the deliverer (spec 004)", func() { ... })` to `Describe("Run")` in `pkg/steps_ai_review_test.go`, after the short-circuit decision block.

**1a. Imports.** Add to the existing import block (this test file is package `pkg_test`, so importing `factory` is legal — no cycle):
```go
base "github.com/bborbe/cqrs/base"
"github.com/bborbe/github-releaser-agent/pkg/factory"
libkafkamocks "github.com/bborbe/kafka/mocks"
libtime "github.com/bborbe/time"
```
`encoding/json`, `context`, `agentlib` and `pkg` are already imported.

**1b. A local helper that delivers and unwraps.** Define it inside the `Context`, before the `It`, so the `It` body stays short enough for the 30-line verification windows:

```go
deliverAndUnwrapTask := func(md *agentlib.Markdown, res *agentlib.Result) *agentlib.Task
```

<!--
Spec ambiguity resolved here: AC (e) says "unwraps the published cdb.CommandObject
(base.ParseEvent)". The published message value is json.Marshal(commandObject.Command)
(cdb/cdb_command-object-sender.go createMessage), not the CommandObject, so unmarshalling
the value into a cdb.CommandObject fails. The agent library's own deliverer test avoids
this by injecting a CommandObjectSender fake, which factory.CreateKafkaResultDeliverer
does not expose — so the JSON route below is the only shape that satisfies AC (e) through
the factory. If the spec is ever amended, amend it to say "unmarshal the published
base.Command, then base.ParseEvent(cmd.Data)".
-->

It must:
1. `output, err := md.Marshal(context.Background())` — the content the deliverer publishes.
2. Build `syncProducer := &libkafkamocks.KafkaSyncProducer{}` and the deliverer:
   ```go
   deliverer := factory.CreateKafkaResultDeliverer(
       syncProducer,
       base.TopicPrefix(""),
       agentlib.TaskIdentifier("gh-release-001"),
       taskWithFailedResult(),
       libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return libtime.DateTime{} }),
   )
   ```
3. `Expect(deliverer.DeliverResult(context.Background(), agentlib.AgentResultInfo{Status: res.Status, NextPhase: res.NextPhase, Output: output, Message: res.Message})).To(Succeed())`.
4. `Expect(syncProducer.SendMessageCallCount()).To(Equal(1))`, then `_, msg := syncProducer.SendMessageArgsForCall(0)` and `raw, err := msg.Value.Encode()` — assert the error in place with `Expect(err).NotTo(HaveOccurred())`; the helper returns only the task, so every error inside it must be asserted where it is produced.
5. `var cmd base.Command; Expect(json.Unmarshal(raw, &cmd)).To(Succeed())` — the message value is the serialized command, not the command object.
6. `event, err := base.ParseEvent(context.Background(), cmd.Data)`, error asserted in place.
7. `eventJSON, err := json.Marshal(event)`, error asserted in place; then `var task agentlib.Task; Expect(json.Unmarshal(eventJSON, &task)).To(Succeed())`, and return `&task`.

The zero `libtime.DateTime` marshals to JSON `null` and unmarshals back to the zero value, so the event round-trip into `agentlib.Task` is safe with the zero-value clock stub.

Do not assert on the topic name — `pkg/factory/topic_prefix_golden_test.go` already locks that down.

**1c. The spec.** Name it verbatim in the `It` description:

`It("TwoRunChainParksHumanReview — two runs over the same page publish in_progress / human_review", ...)`

Body:
1. `fakeOps.LsRemoteReturns("", nil)` — the remote never carries the planned tag.
2. `md, err := agentlib.ParseMarkdown(context.Background(), taskWithFailedResult())`, error asserted.
3. First run: `first, err := step.Run(context.Background(), md)`; assert `first.Status == agentlib.AgentStatusDone` and `first.NextPhase == "human_review"`.
4. Second run over the SAME `md` — this is the re-trigger the bug exploits: `second, err := step.Run(context.Background(), md)`; assert `second.NextPhase == "human_review"` and `fakeOps.LsRemoteCallCount() == 2` (exactly one consult per run).
5. `task := deliverAndUnwrapTask(md, second)`.
6. Assert, in this order:
   ```go
   Expect(task.Frontmatter["status"]).To(Equal("in_progress"))
   Expect(task.Frontmatter["phase"]).To(Equal("human_review"))
   Expect(task.Frontmatter["status"]).NotTo(Equal("completed"))
   Expect(task.Frontmatter["phase"]).NotTo(Equal("done"))
   ```

The negative assertions are the load-bearing ones: they are what a re-introduced `Failed` status or an unconditional `done` would break. Keep all four; do not collapse them into a single `Or`.

## 2. Create the `## Unreleased` section in `CHANGELOG.md`

Insert the section directly below the preamble block and above `## v0.6.1`, so the file order reads: `# Changelog` → preamble → `## Unreleased` → `## v0.6.1` → the rest. Never between the `# Changelog` title and the preamble, and never below `## v0.6.1`.

The section carries exactly one bullet, in the repo's existing style, starting with `- fix:`. The bullet must:
- contain the word `remote`;
- state the reader-visible behaviour change: a release whose `## Result` is `failed` is no longer recorded as completed unless the remote confirms the planned tag; when the tag is absent, or the remote cannot be asked, the task parks at `human_review` / `status: in_progress` instead of closing;
- name the consequence that matters: the review block no longer reports checks it never ran, and the rejected release is not re-triggered into a false completion.

Leave every existing `## vX.Y.Z` section byte-identical. Do not rename `## Unreleased` and do not add a version header — the maintainer bot renames the section at release time.

## 3. Self-check before finishing

Re-run `<verification>` and confirm it passes. Then confirm by reading the files: the new spec exists with all eight assertions (two on each run, four on the published frontmatter) and the two negative ones intact; `## Unreleased` is the first `## ` header in `CHANGELOG.md` and sits above `## v0.6.1`; no version section was modified (`grep -c '^## v0' CHANGELOG.md` still prints 31, the pre-edit count); and `make precommit` is green.
</requirements>

<constraints>
- **The deliverer-layer assertion is on the published message**, not on the step's return value — that is the only place the `status: completed` / `in_progress` resolution happens.
- **Parking shape is `AgentStatusDone` + `NextPhase: "human_review"`.** Do not change the step's return values in this prompt; if the new spec fails, the earlier prompts are wrong, not the test.
- **No new result field, no new outcome value, no new error-category value, no new config key, no opt-out flag, no environment variable.**
- **No change to `pkg/factory`, `pkg/git`, or the shared agent library.** This prompt adds a test and a changelog entry only.
- **Do not repair the seventeen already-wedged release records** and do not replay a release. Deployment and replay are the parent task's Definition of Done, not this prompt's.
- **Do not rename `## Unreleased` and do not create a version header** — the maintainer bot owns that rename (`docs/releasing-github-releaser-agent.md`).
- **Existing test conventions hold.** Ginkgo v2 / Gomega, Counterfeiter fakes, no live network, no live Kafka, no live Claude calls.
- **Errors** use `github.com/bborbe/errors` with context wrapping; **logging** is `glog` `V(n)`.
- Repo-relative paths only. **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>
Run `make precommit` — exit 0 (fmt, generate, test, lint, vet, vuln, license).
Run `make test` — exit 0.

```bash
grep -n 'TwoRunChainParksHumanReview' pkg/steps_ai_review_test.go   # >= 1 (0 before this change)
grep -A 30 'TwoRunChainParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'Frontmatter'        # >= 2 (one per asserted key)
grep -A 30 'TwoRunChainParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'Equal("human_review")'  # >= 1 (the It description alone would satisfy a bare 'human_review' grep)
grep -A 30 'TwoRunChainParksHumanReview' pkg/steps_ai_review_test.go | grep -c 'LsRemoteCallCount'  # >= 1
grep -n 'CreateKafkaResultDeliverer' pkg/steps_ai_review_test.go    # >= 1 (0 before this change)
grep -n 'base.ParseEvent' pkg/steps_ai_review_test.go               # >= 1 (0 before this change)

grep -n '^## Unreleased' CHANGELOG.md                               # exactly one line (0 before this change)
awk '/^## /{print; exit}' CHANGELOG.md                              # prints "## Unreleased"
awk '/^## /{sec=$0} /remote/{if (sec=="## Unreleased") print}' CHANGELOG.md | wc -l   # >= 1 (0 before this change)
grep -n '^## v0.6.1' CHANGELOG.md                                   # still present, below the new section
grep -c '^## v0' CHANGELOG.md                                       # 31 — the pre-edit count, unchanged
```
</verification>

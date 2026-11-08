---
status: completed
summary: Added a post-push image-existence check to the release Review block, backed by a new pkg/imageregistry client, dockerBuild opt-in parsing, and ReviewChecks fields wired through the factory.
execution_id: github-releaser-agent-exec-013-image-exists-review-check
dark-factory-version: v0.198.17
created: "2026-11-08T12:54:14Z"
queued: "2026-11-08T12:54:14Z"
started: "2026-11-08T16:15:12Z"
completed: "2026-11-08T16:24:39Z"
---

# Add an image-existence check to the release Review block

<summary>
- A completed release currently records that the tag exists but says nothing about whether an image was published at that tag
- An operator reading a finished release task can see `outcome: released` while no deployable artifact exists
- The Review block gains two fields: whether an image was expected, and whether it was found
- Whether an image is expected comes from the repo's own `dockerBuild.autoBuild` opt-in
- A repo that never opted in is not reported as a failure
- A repo that opted in but has no image at the released tag is reported as a failed check
- The check runs only after the pushed tag is confirmed on the remote
- An unreachable registry is never reported as a missing image
- No change to the release itself: the tag, changelog and commit behaviour stay exactly as they are
</summary>

<objective>
Make an imageless release loud at the point the release completes, so a release that produced no deployable artifact is visible in its own Review block instead of surfacing later as a failed `docker pull` in a deploy.
</objective>

<context>
Read `CLAUDE.md` for project conventions and `docs/dod.md` for the Definition of Done. For the patterns this prompt leans on, read `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`, `go-testing-guide.md` and `go-composition.md`.

Read `pkg/steps_ai_review.go` — `ReviewChecks`, `ReviewOutput`, `Run`, `finishApproved`, `reconcilePostPush`, `writeShortCircuit`. Note the ordering: `Run` computes `rollupVerdict` and then calls `finishApproved`, which performs `ops.Push` and `reconcilePostPush`. Anything that needs the tag to exist on the remote belongs inside `finishApproved`, after `reconcilePostPush` has confirmed it — not in `Run`.

Read `pkg/maintainerconfig/fetcher.go` — `Fetcher.Fetch(ctx, owner, repo, ref)` already returns the raw bytes of `.maintainer.yaml` at a ref. It is mocked as `mocks.MaintainerConfigFetcher` and constructed at `pkg/factory/factory.go` for the planning step. Reuse it; do not add a second file fetcher, and do not use `pkg/githubreview.Client` (it has no file-at-ref method).

Read `pkg/githubreview/client.go` — the HTTP client shape, `setHeaders`, the `newHTTPClientWithBase` test seam and the `&http.Client{Timeout: 15 * time.Second}` construction. The new registry client mirrors all three.

Background: the fleet's image build is fired by `github-release-watcher` on a `refs/tags/*` push, but only for repos the watcher is configured to see, and only when the repo opts in via `.maintainer.yaml`:

```yaml
dockerBuild:
  autoBuild: true
  destinations:
    - dockerhub
```

`destinations` is a list of named classes, not registry URLs. The **image-reference host** and the **registry-API host** differ for Docker Hub: `dockerhub` → image reference `docker.io`, registry API `registry-1.docker.io` (`https://docker.io/v2/` is the website and 302s to `www.docker.com`); `ghcr` → `ghcr.io` for both; `internal` → `docker.prod.nuke.benjamin-borbe.de:443` for both. The first entry is the primary destination.

Public registries answer an anonymous `HEAD /v2/<repository>/manifests/<tag>` with `401` plus a `WWW-Authenticate: Bearer` challenge, so the client must follow the token flow: read `realm` and `service` from the challenge, `GET <realm>?service=<service>&scope=repository:<repository>:pull`, then retry the `HEAD` with `Authorization: Bearer <token>`. Without it every probe of a public destination errors and the check is inert.

The shared `github.com/bborbe/maintainer/maintainerconfig` schema pinned by this repo has no `dockerBuild` field and drops it on decode; `ParseStrict` ignores unknown top-level namespaces, so the field can be parsed from the raw bytes safely.
</context>

<requirements>
1. Add `pkg/imageregistry/` with a `Client` interface exposing `ManifestExists(ctx context.Context, imageRef string) (bool, error)`, a `NewClient()` constructor, and a package-private `newClientWithBase(baseURL string)` test seam mirroring `pkg/githubreview/client.go`, plus `pkg/imageregistry/export_test.go` exposing it to the external test package exactly as `pkg/githubreview/export_test.go` does (`var NewClientForTest = newClientWithBase`).
   - Parse `imageRef` as `<host>/<repository>:<tag>`, tolerating a host that carries a port (`docker.prod.nuke.benjamin-borbe.de:443`) — split on the last `:` only after the last `/`.
   - Map the image-reference host to the registry-API host in one package-private function `registryAPIHost(host string) (string, error)` (`docker.io` → `registry-1.docker.io`; `ghcr.io` and `docker.prod.nuke.benjamin-borbe.de:443` identity; anything else → error), called from `ManifestExists` when the test-seam base URL is empty.
   - `HEAD https://<api-host>/v2/<repository>/manifests/<tag>` with `Accept` naming `application/vnd.docker.distribution.manifest.list.v2+json`, `application/vnd.oci.image.index.v1+json`, `application/vnd.docker.distribution.manifest.v2+json`, `application/vnd.oci.image.manifest.v1+json`.
   - `200` → `true`. `404` → `false`. `401` with a Bearer challenge → follow the token flow once and retry. Any other status, a failed token exchange, a transport error, or an unparseable ref → a wrapped error, never a silent `false`.
   - Use `&http.Client{Timeout: 15 * time.Second}`.
   - Add `//counterfeiter:generate -o ../../mocks/image_registry_client.go --fake-name ImageRegistryClient . Client` on the interface, and add `pkg/imageregistry/suite_test.go` carrying the same `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` directive the other packages use (mirror `pkg/githubreview/suite_test.go`). `make precommit` runs `rm -rf mocks` then `go generate ./...`; without the directive the mock is never regenerated and the test package fails to compile.

2. Add `pkg/maintainerconfig/dockerbuild.go` with `ParseDockerBuild(ctx context.Context, raw []byte) (expected bool, imageHost string, destinationsEmpty bool, err error)`. `imageHost` is the **image-reference host** (`dockerhub` → `docker.io`, `ghcr` → `ghcr.io`, `internal` → `docker.prod.nuke.benjamin-borbe.de:443`), not the raw destination class — the client in `pkg/imageregistry` owns the single image-reference-host → registry-API-host mapping. Return `expected=false` when the block, `autoBuild`, or a true `autoBuild` is absent. Return `destinationsEmpty=true` (with `expected=false`, `imageHost=""`) when `autoBuild` is true and `destinations` is empty — the caller must be able to tell this config fault apart from a repo that never opted in. Unmarshal only the `dockerBuild` keys from the raw bytes. Return `expected=false` when the block, `autoBuild`, or a true `autoBuild` is absent. Resolve the destination class to its image-reference host in that same function, with an error for an unknown class. Do not add a second fetcher.

3. Extend `ReviewChecks` in `pkg/steps_ai_review.go` with `ImageExpected bool` (`json:"image_expected"`) and `ImageExists bool` (`json:"image_exists"`). Add `CheckImage = "Image"` alongside the existing check-name constants, and update the `ReviewChecks` and `FailedChecks` doc comments that enumerate the current fields.

4. In `finishApproved`, after `reconcilePostPush` returns and **only when it reports the tag confirmed on the remote**, evaluate the image check:
   - `maintainerconfig.Fetcher.Fetch(ctx, owner, name, <released tag>)` → `ParseDockerBuild`.
   - Not expected (no block, `autoBuild` false/absent) → `ImageExpected=false`, `ImageExists=false`, no entry in `failedChecks`, no note.
   - `destinationsEmpty` true → `ImageExpected=false`, `ImageExists=false`, and a note naming the config gap (`dockerBuild.destinations is empty`) — this is a config fault, not an image absence, and must not block the release.
   - The `owner` / `name` pair is already parsed in `Run`; pass it into `finishApproved` rather than re-parsing the frontmatter there.
   - `autoBuild: true` with at least one destination → `ImageExpected=true`; probe the **primary** (first) destination at the released tag. The probe reference is `<imageHost>/<owner>/<name>:<result.LocalTag>` (e.g. `docker.io/bborbe/github-releaser-agent:v0.7.0`) — `imageHost` from `ParseDockerBuild`, repository `owner/name`. Present → `ImageExists=true`. Absent → `ImageExists=false` and append `CheckImage` to `output.FailedChecks`.
   - `stderrors.Is(err, maintainerconfig.ErrFileNotFound)` from the fetch means the repo has no `.maintainer.yaml` at that tag: `ImageExpected=false`, `ImageExists=false`, no `CheckImage`, no note — the repo never opted in. Mirror `resolveMaintainerConfig` in `pkg/steps_planning.go`.
   - Any other registry or fetch error is not an absence: leave `ImageExpected=true` and `ImageExists=false`, add no `CheckImage`, and append a note naming the error and the image reference that was probed. The release still closes `done`.
   - On any non-confirmed push path, do not run the check at all.

5. When the probe reports absence, set `output.Overall=OverallFail`, append `CheckImage` (only if not already present), append the note to `output.Notes`, set `output.Approved=false`, write the `## Review` section, and return `&agentlib.Result{Status: agentlib.AgentStatusNeedsInput, NextPhase: string(domain.TaskPhaseHumanReview), Message: output.Notes}` from `finishApproved`. Do **not** reuse `finishHumanReview`: it returns `AgentStatusFailed`, which the agent library documents as a *transient infrastructure* failure the controller retries with `trigger_count++` — an absent image does not change on retry, so every retry would re-run `ops.Push` and the faithfulness LLM call. `AgentStatusNeedsInput` is the documented status for a semantic problem routed straight to `human_review`. The new terminal path must still honour the deferred workdir cleanup — set `workdirShouldCleanup` exactly as the existing terminal transitions do, so the workdir is removed after `## Review` is written. Do **not** change `rollupVerdict` — it runs pre-push in `Run` and already fails on any `failedChecks` entry. Do **not** change `notesFor` — it has no access to the image reference; append the note directly to `output.Notes`.

6. In `writeShortCircuit`, set `ImageExpected=false` and `ImageExists=false` explicitly so the `## Review` JSON is consistent across entry points.

7. Wire both the registry client and the `maintainerconfig.Fetcher` through `NewAIReviewStep` as constructor parameters rather than constructing them inside the step. Update the factory that builds the step (`pkg/factory/factory.go`) and the test call site.

8. Add tests:
   - Step-level Ginkgo/Gomega tests covering: not opted in → expected false, exists false, no failure; `autoBuild: false` → same; `ErrFileNotFound` fetch → expected false, exists false, no failure, no note; `autoBuild: true` with empty `destinations` → expected false, exists false, no failure, note names the config gap; opted in and present → expected true, exists true, no failure; opted in and absent → exists false, `CheckImage` in `FailedChecks`, not approved, note names the image reference; registry error → no `CheckImage`, note carries the error; non-confirmed push → the check does not run. Use Counterfeiter mocks for the registry client, the `maintainerconfig.Fetcher` and the GitHub client — no live network calls.
   - A table test over every destination class asserting the resolved image-reference `imageHost` (`dockerhub` → `docker.io`, `ghcr` → `ghcr.io`, `internal` → `docker.prod.nuke.benjamin-borbe.de:443`) and the error for an unknown class, plus a table test over `registryAPIHost` asserting `docker.io` → `registry-1.docker.io` and identity for the other two — `newClientWithBase` bypasses the mapping, so this function must be tested directly.
   - In the opted-in-and-present case assert the exact reference passed to the client (`ImageRegistryClient.ManifestExistsArgsForCall(0)` equals `<imageHost>/<owner>/<name>:<tag>`), so a wrong repository composition cannot pass.
   - `pkg/imageregistry/client_test.go` driving the real client against `httptest.NewServer` via `NewClientForTest`: assert the `HEAD` method, the `/v2/<repository>/manifests/<tag>` path, the `Accept` media types, `200`→true, `404`→false, `500`/transport→wrapped error, the `401`+Bearer token flow, and ref parsing for a host that carries a port.
   - Assert the Review fields through the existing `agentlib.ExtractSection[ReviewOutput]` helper so the JSON tags are round-trip tested.

9. Add a `## Unreleased` entry to `CHANGELOG.md` describing the new Review-block fields.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Error handling uses `github.com/bborbe/errors` with context wrapping — no `fmt.Errorf`, no bare `return err`.
- No `context.Background()` outside `main`/tests.
- Logging uses `glog` with `V(n)`-gated `Info` lines — no `fmt.Print*`.
- Constructor injection only — no package-level state, no client constructed inside the step.
- The release behaviour itself must not change: the tag, changelog rewrite, commit and push are untouched.
- The check must never block a release for a reason other than a genuinely absent image — an unreachable registry, an empty `destinations` list, or an unparseable config is not an absence. A missing `.maintainer.yaml` (404) is not an error at all: the repo never opted in, so `ImageExpected` stays false.
- Probing only the primary destination is deliberate: the fleet's build arm publishes one destination per Job.
- All paths in this prompt are repo-relative except the container mount `/workspace`.
</constraints>

<verification>
ROOTDIR=/workspace make precommit
</verification>

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package git wraps git shell-outs behind the GitOps interface so the
// execution step can be tested with a counterfeiter mock. Implementations
// are responsible for assembling argv slices, capturing stderr, and
// wrapping errors via bborbe/errors. They MUST NOT use sh -c or any
// shell-interpolated form — argv only.
//
// Auth model: HTTPS clones with a GitHub token are handled by URL
// transformation at the call site (cloneURL → https://x-access-token:<tok>@github.com/...).
// The package itself takes the transformed URL — it does not know about
// tokens directly.
//
// LsRemote is the read-only complement to Push: it queries the remote
// for tag state (the dereferenced commit SHA sitting at refs/tags/<tag>)
// without mutating anything. It follows the same auth model as Clone
// (caller injects the token into the URL).
package git

import "context"

//counterfeiter:generate -o ../../mocks/git_ops.go --fake-name GitOps . GitOps

// GitOps is the seam between the execution step and the git binary. Four
// methods cover the entire direct-push happy path: clone target repo,
// commit CHANGELOG rewrite, annotated tag, push commit + tag. LsRemote
// is the read-only complement to Push — it asks the remote what commit
// SHA, if any, sits at the planned version's tag.
//
// All methods are context-aware — callers can cancel mid-operation.
// workdir is the absolute path to the checkout (created and owned by the
// caller; the package does not manage workdir lifecycle).
//
//nolint:revive // GitOps is the spec-required name; rename to Ops would violate the frozen interface requirement
type GitOps interface {
	// Clone shells out `git clone <cloneURL> <workdir>` and checks out ref.
	// cloneURL MUST already include any auth token (the package does not
	// add credentials).
	Clone(ctx context.Context, cloneURL, ref, workdir string) error

	// Commit stages paths (relative to workdir) and creates a commit with
	// the bot identity. Returns the short SHA (7 chars) of the new commit.
	// The bot identity is set per-invocation via -c user.name / -c user.email
	// — never writes to the global gitconfig.
	Commit(ctx context.Context, workdir, message string, paths ...string) (sha string, err error)

	// Tag creates an annotated tag (git tag -a <tag> -m <message>).
	// Lightweight tags are NOT supported — annotated tags carry author
	// and date metadata.
	Tag(ctx context.Context, workdir, tag, message string) error

	// Push pushes the given refs (e.g. "HEAD", "refs/tags/v1.2.7") to origin.
	// Returns the underlying stderr-wrapped error on failure — callers
	// pass this to error_classifier to map onto the error_category enum.
	Push(ctx context.Context, workdir string, refs ...string) error

	// CommittedFiles returns the repo-relative paths changed by the HEAD
	// commit (git diff-tree --no-commit-id --name-only -r HEAD). The
	// execution step uses it as a pre-push guard: a release commit must
	// touch CHANGELOG.md and nothing else.
	CommittedFiles(ctx context.Context, workdir string) ([]string, error)

	// LsRemote shells out `git ls-remote <cloneURL> refs/tags/<tag>` and
	// returns the dereferenced commit SHA for the tag.
	//
	// For an annotated tag, git emits TWO lines for refs/tags/<tag>:
	//   <tag-object-sha>     refs/tags/<tag>
	//   <commit-sha>         refs/tags/<tag>^{}
	// LsRemote MUST return the commit-sha (the ^{} line). For a lightweight
	// tag where git emits only the first line, that SHA is returned (it IS
	// the commit SHA for lightweight tags).
	//
	// When the remote has no refs/tags/<tag> at all, returns ("", nil) — the
	// caller treats this as a no-op. The empty result is NOT an error.
	//
	// cloneURL MUST already include any auth token (matches the Clone contract).
	// ref is the branch/ref hint, logged only — the package does not pass it
	// to `git ls-remote` (we query the tag directly).
	LsRemote(ctx context.Context, cloneURL, ref, tag string) (sha string, err error)
}

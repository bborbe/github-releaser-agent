// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// BotIdentity holds the commit author/committer identity. Hardcoded
// intentionally: the only consumer is github-releaser, and a single
// value is the contract. If a future spec needs override capability,
// that spec adds the seam; until then, parameterization is YAGNI.
//
// Per spec 049 § Constraints + [[GitHub Release Agent Phase 1 Learnings]],
// this MUST match the Phase 1 slash-command identity verbatim — otherwise
// v1.7.8's release commit history breaks attribution continuity.
type BotIdentity struct {
	Name  string
	Email string
}

// DefaultBotIdentity returns the Phase 1 verbatim identity. The osExecGitOps
// struct reads this internally on every Commit / Tag — there is no override
// path. Exposed publicly for test assertions.
func DefaultBotIdentity() BotIdentity {
	return BotIdentity{
		Name:  "Benjamin Borbe",
		Email: "bborbe@users.noreply.github.com",
	}
}

// NewOSExecGitOps returns a GitOps implementation that shells out to the
// git binary via os/exec. Zero-arg: the bot identity is constant via
// DefaultBotIdentity().
func NewOSExecGitOps() GitOps {
	return &osExecGitOps{}
}

type osExecGitOps struct{}

// cmdEnv returns the env allowlist for git subprocesses: HOME (for ~/.gitconfig
// fallback) + PATH (to resolve git). Strict allowlist prevents pod-level
// secrets from leaking. Mirrors pr-reviewer's repoManager.cmdEnv.
func (g *osExecGitOps) cmdEnv() []string {
	return []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}
}

func (g *osExecGitOps) Clone(ctx context.Context, cloneURL, ref, workdir string) error {
	// git clone --depth 1 <cloneURL> <workdir>
	// Clones the remote's default-branch HEAD (not the trigger ref).  This is
	// correct because a release operates on the default branch: the agent rewrites
	// ## Unreleased, commits on top, and pushes a fast-forward to the default
	// branch.  The trigger ref (a SHA or branch name) is used only for
	// traceability in the success log and is never passed to git clone.
	// --depth 1 is acceptable because we only rewrite CHANGELOG and push a single
	// commit + tag; we don't need history beyond HEAD.
	// #nosec G204 -- cloneURL constructed in caller from validated frontmatter; workdir is os.TempDir-rooted; ref is logged only, not passed to git
	cmd := exec.CommandContext(
		ctx,
		"git",
		"clone",
		"--depth",
		"1",
		cloneURL,
		workdir,
	)
	cmd.Env = g.cmdEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.Errorf(ctx, "git clone: %s", redactToken(strings.TrimSpace(stderr.String())))
	}
	glog.V(2).Infof("git clone succeeded: ref=%s workdir=%s", ref, workdir)
	return nil
}

func (g *osExecGitOps) Commit(
	ctx context.Context,
	workdir, message string,
	paths ...string,
) (string, error) {
	// git -C <workdir> add <paths...>
	if len(paths) > 0 {
		addArgs := append([]string{"-C", workdir, "add", "--"}, paths...)
		// #nosec G204 -- workdir is os.TempDir-rooted; paths come from execution step (CHANGELOG.md only)
		if out, err := exec.CommandContext(ctx, "git", addArgs...).CombinedOutput(); err != nil {
			return "", errors.Errorf(ctx, "git add: %s", strings.TrimSpace(string(out)))
		}
	}

	// git -C <workdir> -c user.name=<name> -c user.email=<email> commit -m <message>
	id := DefaultBotIdentity()
	commitArgs := []string{
		"-C", workdir,
		"-c", "user.name=" + id.Name,
		"-c", "user.email=" + id.Email,
		"commit",
		"-m", message,
	}
	// #nosec G204 -- workdir is os.TempDir-rooted; identity is the bot constant; message comes from execution step
	if out, err := exec.CommandContext(ctx, "git", commitArgs...).CombinedOutput(); err != nil {
		return "", errors.Errorf(ctx, "git commit: %s", strings.TrimSpace(string(out)))
	}

	// git -C <workdir> rev-parse --short HEAD → short SHA
	// #nosec G204 -- workdir is os.TempDir-rooted; args are hardcoded
	shaBytes, err := exec.CommandContext(ctx, "git", "-C", workdir, "rev-parse", "--short", "HEAD").
		Output()
	if err != nil {
		return "", errors.Wrap(ctx, err, "git rev-parse HEAD")
	}
	return strings.TrimSpace(string(shaBytes)), nil
}

func (g *osExecGitOps) Tag(ctx context.Context, workdir, tag, message string) error {
	// git -C <workdir> -c user.name=<name> -c user.email=<email> tag -a <tag> -m <message>
	id := DefaultBotIdentity()
	args := []string{
		"-C", workdir,
		"-c", "user.name=" + id.Name,
		"-c", "user.email=" + id.Email,
		"tag", "-a", tag, "-m", message,
	}
	// #nosec G204 -- workdir is os.TempDir-rooted; identity is the bot constant; tag and message come from execution step
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = g.cmdEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.Errorf(ctx, "git tag: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (g *osExecGitOps) Push(ctx context.Context, workdir string, refs ...string) error {
	// git -C <workdir> push --atomic origin <refs...>
	// --atomic ensures HEAD + tag land together or neither lands. Without it,
	// GitHub may accept HEAD and reject the tag (or vice versa), leaving an
	// inconsistent state on the remote.
	args := append([]string{"-C", workdir, "push", "--atomic", "origin"}, refs...)
	// No --force / --force-with-lease — non-fast-forward maps to retry, not overwrite.
	// #nosec G204 -- workdir is os.TempDir-rooted; refs are constructed by execution step from validated frontmatter ref / tag
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = g.cmdEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.Errorf(ctx, "git push: %s", redactToken(strings.TrimSpace(stderr.String())))
	}
	return nil
}

// CommittedFiles returns the repo-relative paths changed by the HEAD commit.
func (g *osExecGitOps) CommittedFiles(ctx context.Context, workdir string) ([]string, error) {
	// git -C <workdir> diff-tree --no-commit-id --name-only -r HEAD
	// #nosec G204 -- workdir is os.TempDir-rooted; all other args are constants
	out, err := exec.CommandContext(
		ctx, "git", "-C", workdir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD",
	).Output()
	if err != nil {
		return nil, errors.Wrap(ctx, err, "git diff-tree HEAD")
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// strings.TrimSpace also strips a trailing CR, so \r\n / core.autocrlf
		// repos do not leave a "name\r" that would mis-compare in the guard.
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			files = append(files, trimmed)
		}
	}
	glog.V(2).Infof("git diff-tree HEAD: files=%v", files)
	return files, nil
}

// LsRemote shells out `git ls-remote <cloneURL> refs/tags/<tag>` and returns
// the dereferenced commit SHA. For an annotated tag, the `^{}` line wins;
// for a lightweight tag, the only emitted line is returned. A missing tag
// on the remote returns ("", nil) — the caller treats that as a no-op.
func (g *osExecGitOps) LsRemote(ctx context.Context, cloneURL, ref, tag string) (string, error) {
	// git ls-remote <cloneURL> refs/tags/<tag>
	// cloneURL is authed by caller from validated frontmatter; tag comes from
	// plan.NextVersionHeader which the planning step validated. The full
	// ref-path is a separate argv element — Git itself does the ref-expansion.
	// #nosec G204 -- cloneURL is authed by caller from validated frontmatter; tag comes from plan.NextVersionHeader which the planning step validated
	cmd := exec.CommandContext(ctx, "git", "ls-remote", cloneURL, "refs/tags/"+tag)
	cmd.Env = g.cmdEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", errors.Errorf(
			ctx,
			"git ls-remote: %s",
			redactToken(strings.TrimSpace(stderr.String())),
		)
	}
	sha := parseLsRemoteOutput(out, tag)
	glog.V(2).Infof("git ls-remote succeeded: ref=%s tag=%s sha=%s", ref, tag, sha)
	return sha, nil
}

// parseLsRemoteOutput extracts the preferred SHA from `git ls-remote` output
// for the given tag. The dereferenced commit SHA (the `^{}` line) wins for
// annotated tags; for lightweight tags the only emitted line IS the commit
// SHA, and is returned. Returns "" if neither line is present (remote has
// no refs/tags/<tag>). Exposed for testing via ParseLsRemoteOutputForTest.
func parseLsRemoteOutput(out []byte, tag string) string {
	derefSuffix := "refs/tags/" + tag + "^{}"
	plainSuffix := "refs/tags/" + tag
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), derefSuffix) {
			return strings.TrimSpace(strings.Fields(line)[0])
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), plainSuffix) {
			return strings.TrimSpace(strings.Fields(line)[0])
		}
	}
	return ""
}

// redactToken strips x-access-token:<TOK>@ patterns from stderr to prevent
// GH_TOKEN from landing in error logs. Git can echo the URL with embedded
// credentials on auth/clone failures (e.g.
// "fatal: unable to access 'https://x-access-token:ghp_AAA@github.com/...'").
// Apply to ALL Clone/Push stderr that gets wrapped into errors.
func redactToken(s string) string {
	// Replace x-access-token:<anything-up-to-@> with x-access-token:[REDACTED]
	return tokenURLRegexp.ReplaceAllString(s, "x-access-token:[REDACTED]@")
}

// RedactToken exposes the unexported redactToken helper for callers
// outside this package (e.g. the spec-064 post-check tail in
// pkg/steps_execution.go needs to log a wrapped err.Error() through
// the same redaction). Behavior is identical to redactToken — the
// split is purely a visibility boundary, not a re-implementation.
func RedactToken(s string) string {
	return redactToken(s)
}

// tokenURLRegexp is compiled once at package init (intentionally package-level,
// not inside redactToken) so the hot path does not recompile per call.
var tokenURLRegexp = regexp.MustCompile(`x-access-token:[^@\s]+@`)

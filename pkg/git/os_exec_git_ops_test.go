// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bborbe/github-releaser-agent/pkg/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DefaultBotIdentity", func() {
	It("returns Phase 1 verbatim identity", func() {
		id := git.DefaultBotIdentity()
		Expect(id.Name).To(Equal("Benjamin Borbe"))
		Expect(id.Email).To(Equal("bborbe@users.noreply.github.com"))
	})
})

var _ = Describe("NewOSExecGitOps", func() {
	It("returns a non-nil GitOps", func() {
		ops := git.NewOSExecGitOps()
		Expect(ops).NotTo(BeNil())
	})
})

// Boundary-crossing integration tests — exercise the real `git` binary
// against a local repo. These prove the -c user.name / -c user.email
// identity injection works through the actual shell-out, not just via mocks.
// Skip on systems without git (CI containers have it; macOS dev workstations
// have it). Use a per-test tempdir for isolation.
var _ = Describe("osExecGitOps boundary contracts", func() {
	var (
		ctx     context.Context
		workdir string
		ops     git.GitOps
	)

	BeforeEach(func() {
		if _, err := exec.LookPath("git"); err != nil {
			Skip("git binary not available")
		}
		ctx = context.Background()
		var err error
		workdir, err = os.MkdirTemp("", "github-releaser-git-test-*")
		Expect(err).NotTo(HaveOccurred())
		// Initialize a local repo so Commit/Tag have something to operate on.
		init := exec.Command("git", "-C", workdir, "init", "-b", "master")
		Expect(init.Run()).To(Succeed())
		// Seed an initial commit so `git commit` against CHANGELOG.md has a parent.
		Expect(
			os.WriteFile(
				filepath.Join(workdir, "CHANGELOG.md"),
				[]byte("# Changelog\n\n## Unreleased\n\n- feat: stub\n"),
				0o600,
			),
		).To(Succeed())
		ops = git.NewOSExecGitOps()
	})

	AfterEach(func() {
		os.RemoveAll(workdir)
	})

	It("Clone fetches the default-branch HEAD into an empty workdir via --depth 1", func() {
		// Build a real source repo on the default branch (master) with a known
		// file.  Passing a non-branch ref string proves Clone ignores it and clones
		// the default-branch HEAD instead.
		source, err := os.MkdirTemp("", "github-releaser-clone-source-*")
		Expect(err).NotTo(HaveOccurred())
		defer os.RemoveAll(source)

		Expect(exec.Command("git", "-C", source, "init", "-b", "master").Run()).
			To(Succeed())
		Expect(
			os.WriteFile(
				filepath.Join(source, "MARKER.txt"),
				[]byte("clone-me\n"),
				0o600,
			),
		).To(Succeed())
		Expect(exec.Command("git", "-C", source, "add", "MARKER.txt").Run()).To(Succeed())
		Expect(
			exec.Command(
				"git",
				"-C", source,
				"-c", "user.name=Test",
				"-c", "user.email=test@example.com",
				"commit", "-m", "seed",
			).Run(),
		).To(Succeed())

		// Clone into a fresh, non-existent dir (git clone requires the target
		// to be empty/absent).  Pass a ref that is NOT the branch name to prove
		// it is ignored.
		dest := filepath.Join(workdir, "cloned")
		Expect(ops.Clone(ctx, source, "release-branch", dest)).To(Succeed())

		// The default-branch file landed — proves default-branch HEAD was cloned.
		got, readErr := os.ReadFile(filepath.Join(dest, "MARKER.txt"))
		Expect(readErr).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("clone-me\n"))

		// HEAD resolves (the clone is a valid repo).
		headOut, headErr := exec.Command("git", "-C", dest, "rev-parse", "HEAD").CombinedOutput()
		Expect(headErr).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(headOut))).NotTo(BeEmpty())
	})

	It("CommittedFiles returns only the paths changed by the HEAD commit", func() {
		// Seed a parent commit so HEAD has a parent — `git diff-tree HEAD`
		// reports nothing for a root commit (would need --root). Production
		// always operates on a cloned repo with history, so HEAD always has
		// a parent; the seed mirrors that.
		Expect(exec.Command("git", "-C", workdir, "add", "CHANGELOG.md").Run()).To(Succeed())
		Expect(exec.Command("git", "-C", workdir,
			"-c", "user.name=Seed", "-c", "user.email=seed@example.com",
			"commit", "-m", "seed").Run()).To(Succeed())

		// Single file: rewrite + commit only CHANGELOG.md (the release shape).
		Expect(os.WriteFile(
			filepath.Join(workdir, "CHANGELOG.md"),
			[]byte("# Changelog\n\n## v1.2.8\n\n- feat: stub\n"),
			0o600,
		)).To(Succeed())
		_, err := ops.Commit(ctx, workdir, "release v1.2.8", "CHANGELOG.md")
		Expect(err).NotTo(HaveOccurred())
		files, err := ops.CommittedFiles(ctx, workdir)
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(Equal([]string{"CHANGELOG.md"}))

		// Multiple files, one with a space — proves the line-split parser
		// returns each path once and does NOT split on whitespace.
		Expect(os.WriteFile(
			filepath.Join(workdir, "CHANGELOG.md"),
			[]byte("# Changelog\n\n## v1.2.9\n"),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(workdir, "with space.txt"),
			[]byte("x\n"),
			0o600,
		)).To(Succeed())
		_, err = ops.Commit(ctx, workdir, "release v1.2.9", "CHANGELOG.md", "with space.txt")
		Expect(err).NotTo(HaveOccurred())
		files, err = ops.CommittedFiles(ctx, workdir)
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(ConsistOf("CHANGELOG.md", "with space.txt"))
	})

	It(
		"CommittedFiles returns empty for a root commit (documents the diff-tree HEAD limitation)",
		func() {
			// `git diff-tree HEAD` (without --root) emits NOTHING for a parentless
			// root commit. Production never hits this — releases operate on a cloned
			// repo with history, so HEAD always has a parent — but if it did, the
			// guard would see an empty file set (len != 1) and fail closed with
			// unexpected_diff rather than push. Asserted here so the edge case is
			// documented and intentional, not a surprise.
			Expect(exec.Command("git", "-C", workdir, "add", "CHANGELOG.md").Run()).To(Succeed())
			Expect(exec.Command("git", "-C", workdir,
				"-c", "user.name=Seed", "-c", "user.email=seed@example.com",
				"commit", "-m", "root").Run()).To(Succeed())
			files, err := ops.CommittedFiles(ctx, workdir)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(BeEmpty())
		},
	)

	It("Commit attributes the commit to DefaultBotIdentity via -c flags", func() {
		sha, err := ops.Commit(ctx, workdir, "release v1.2.8", "CHANGELOG.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).NotTo(BeEmpty())

		// Inspect the commit's author/committer name+email.
		out, runErr := exec.Command("git", "-C", workdir, "log", "-1", "--format=%an <%ae>|%cn <%ce>").
			CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred())
		expect := "Benjamin Borbe <bborbe@users.noreply.github.com>|Benjamin Borbe <bborbe@users.noreply.github.com>"
		Expect(strings.TrimSpace(string(out))).To(Equal(expect))
	})

	It("Tag creates an annotated tag attributed to the bot identity", func() {
		_, err := ops.Commit(ctx, workdir, "release v1.2.8", "CHANGELOG.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(ops.Tag(ctx, workdir, "v1.2.8", "release v1.2.8")).To(Succeed())

		// `taggername` is only set on annotated tags (not lightweight) — proves
		// we used `git tag -a` AND the -c flags were honored.
		out, runErr := exec.Command("git", "-C", workdir, "tag", "-l", "--format=%(taggername) <%(taggeremail)>", "v1.2.8").
			CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(out))).To(ContainSubstring("Benjamin Borbe"))
		Expect(
			strings.TrimSpace(string(out)),
		).To(ContainSubstring("bborbe@users.noreply.github.com"))
	})

	It("Push --atomic to a local bare remote lands HEAD and tag together", func() {
		// Make commit + tag locally.
		_, err := ops.Commit(ctx, workdir, "release v1.2.8", "CHANGELOG.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(ops.Tag(ctx, workdir, "v1.2.8", "release v1.2.8")).To(Succeed())

		// Set up a local bare remote and wire it as origin.
		remote, err := os.MkdirTemp("", "github-releaser-bare-*")
		Expect(err).NotTo(HaveOccurred())
		defer os.RemoveAll(remote)
		Expect(exec.Command("git", "init", "--bare", remote).Run()).To(Succeed())
		Expect(
			exec.Command("git", "-C", workdir, "remote", "add", "origin", remote).Run(),
		).To(Succeed())

		Expect(ops.Push(ctx, workdir, "HEAD:master", "refs/tags/v1.2.8")).To(Succeed())

		// Verify both refs landed on the bare remote.
		headOut, headErr := exec.Command("git", "-C", remote, "rev-parse", "master").
			CombinedOutput()
		Expect(headErr).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(headOut))).NotTo(BeEmpty())
		tagOut, tagErr := exec.Command("git", "-C", remote, "rev-parse", "v1.2.8").CombinedOutput()
		Expect(tagErr).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(tagOut))).NotTo(BeEmpty())
	})
})

var _ = Describe("redactToken", func() {
	It("strips x-access-token credentials from stderr-like strings", func() {
		in := "fatal: unable to access 'https://x-access-token:ghp_AAA@github.com/owner/repo/': repository not found"
		out := git.RedactTokenForTest(in)
		Expect(out).NotTo(ContainSubstring("ghp_AAA"))
		Expect(out).To(ContainSubstring("x-access-token:[REDACTED]@"))
	})
})

// LsRemote coverage — pure parser + subprocess integration. The pure parser
// table-test exercises the annotated/lightweight/empty fixtures directly so
// the boundary contract is testable without forking a subprocess. The
// subprocess tests use a faked `git` binary on PATH to exercise the argv-only
// + token-redaction contract end-to-end.
var _ = Describe("ParseLsRemoteOutput", func() {
	It("returns the dereferenced commit SHA for an annotated tag fixture", func() {
		// Two-line fixture: tag-object SHA first, commit SHA second.
		// Per git ls-remote semantics, the ^{} line is the commit SHA.
		in := []byte(
			"abc123def456abc123def456abc123def456abcd\trefs/tags/v1.2.8\n" +
				"def456abc123def456abc123def456abc123def45\trefs/tags/v1.2.8^{}\n",
		)
		Expect(git.ParseLsRemoteOutputForTest(in, "v1.2.8")).
			To(Equal("def456abc123def456abc123def456abc123def45"))
	})

	It("returns the tag-object SHA for a lightweight tag fixture (no ^{} line)", func() {
		in := []byte("abc123def456abc123def456abc123def456abcd\trefs/tags/v1.2.8\n")
		Expect(git.ParseLsRemoteOutputForTest(in, "v1.2.8")).
			To(Equal("abc123def456abc123def456abc123def456abcd"))
	})

	It("returns empty string when the tag is missing on the remote", func() {
		Expect(git.ParseLsRemoteOutputForTest([]byte(""), "v1.2.8")).To(BeEmpty())
		Expect(git.ParseLsRemoteOutputForTest([]byte("\n"), "v1.2.8")).To(BeEmpty())
	})

	It("ignores unrelated refs and still finds the requested tag", func() {
		in := []byte(
			"1111111111111111111111111111111111111111\trefs/tags/other\n" +
				"abc123def456abc123def456abc123def456abcd\trefs/tags/v1.2.8\n" +
				"def456abc123def456abc123def456abc123def45\trefs/tags/v1.2.8^{}\n",
		)
		Expect(git.ParseLsRemoteOutputForTest(in, "v1.2.8")).
			To(Equal("def456abc123def456abc123def456abc123def45"))
	})
})

// LsRemote integration — faked `git` on PATH so the boundary contract
// (argv-only shell-out, token-redacting error path, missing-tag returns
// ("", nil) and NOT an error) is testable without a live GitHub remote.
var _ = Describe("LsRemote", func() {
	var (
		ctx        context.Context
		ops        git.GitOps
		fakeGitDir string
	)

	BeforeEach(func() {
		if _, err := exec.LookPath("git"); err != nil {
			Skip("git binary not available")
		}
		ctx = context.Background()
		ops = git.NewOSExecGitOps()
		var err error
		fakeGitDir, err = os.MkdirTemp("", "github-releaser-lsremote-fake-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(fakeGitDir)
	})

	// writeFakeGit writes a bash script to fakeGitDir/git that:
	//   1. records $@ to <fakeGitDir>/args-<unique>
	//   2. prints the given stdout to stdout
	//   3. prints the given stderr to stderr (if non-empty), exits 1
	// The unique name is derived from the script-name so two tests don't
	// collide on the args file.
	writeFakeGit := func(scriptName, stdout, stderr string, exitCode int) {
		script := "#!/bin/sh\n" +
			"echo \"$@\" > \"" + fakeGitDir + "/args-" + scriptName + "\"\n" +
			"printf '%s' \"" + stdout + "\"\n"
		if stderr != "" {
			script += "printf '%s' \"" + stderr + "\" 1>&2\n"
		}
		script += "exit " + strconv.Itoa(exitCode) + "\n"
		path := filepath.Join(fakeGitDir, "git")
		// #nosec G306 -- the faked git binary must be executable for PATH lookup; lives in a per-test tempdir
		Expect(os.WriteFile(path, []byte(script), 0o755)).To(Succeed())
	}

	prependFakeGitToPath := func() {
		newPath := fakeGitDir + string(os.PathListSeparator) + os.Getenv("PATH")
		Expect(os.Setenv("PATH", newPath)).To(Succeed())
	}

	readRecordedArgs := func(scriptName string) string {
		raw, err := os.ReadFile(filepath.Join(fakeGitDir, "args-"+scriptName))
		Expect(err).NotTo(HaveOccurred())
		return strings.TrimSpace(string(raw))
	}

	It("returns the dereferenced commit SHA for an annotated tag (^{} line wins)", func() {
		stdout := "abc123def456abc123def456abc123def456abcd\trefs/tags/v1.2.8\n" +
			"def456abc123def456abc123def456abc123def45\trefs/tags/v1.2.8^{}\n"
		writeFakeGit("annotated", stdout, "", 0)
		prependFakeGitToPath()

		// #nosec G101 -- fake token string for argv fixture
		sha, err := ops.LsRemote(
			ctx, "https://x-access-token:ghp_FAKE@github.com/owner/repo", "master", "v1.2.8",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal("def456abc123def456abc123def456abc123def45"))
	})

	It("returns the tag-object SHA for a lightweight tag (no ^{} line)", func() {
		stdout := "abc123def456abc123def456abc123def456abcd\trefs/tags/v1.2.8\n"
		writeFakeGit("lightweight", stdout, "", 0)
		prependFakeGitToPath()

		sha, err := ops.LsRemote(ctx, "https://github.com/owner/repo", "main", "v1.2.8")
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal("abc123def456abc123def456abc123def456abcd"))
	})

	It("returns (\"\", nil) when the tag is absent on the remote (not an error)", func() {
		writeFakeGit("empty", "", "", 0)
		prependFakeGitToPath()

		sha, err := ops.LsRemote(ctx, "https://github.com/owner/repo", "main", "v1.2.8")
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(BeEmpty())
	})

	It("redacts the x-access-token from the wrapped error when the subprocess fails", func() {
		// Fixture: the fake git's stderr intentionally contains a literal
		// x-access-token:ghp_LEAKEDTOKEN@… so we can assert the wrapper
		// redacts it before the error reaches the caller. Not a real token.
		// #nosec G101 -- intentional fake credential fixture for redaction assertion
		stderr := "fatal: unable to access 'https://x-access-token:ghp_LEAKEDTOKEN@github.com/owner/repo/': repository not found"
		writeFakeGit("auth-fail", "", stderr, 1)
		prependFakeGitToPath()

		_, err := ops.LsRemote(
			ctx, "https://x-access-token:ghp_LEAKEDTOKEN@github.com/owner/repo", "main", "v1.2.8",
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("ghp_LEAKEDTOKEN"))
		Expect(err.Error()).To(ContainSubstring("x-access-token:[REDACTED]@"))
	})

	It(
		"passes the cloneURL and the refs/tags/<tag> as separate argv elements (no shell interpolation)",
		func() {
			writeFakeGit(
				"argv",
				"abc123def456abc123def456abc123def456abcd\trefs/tags/v1.2.8\n",
				"",
				0,
			)
			prependFakeGitToPath()

			// #nosec G101 -- fake token string used only to assert argv plumbing
			cloneURL := "https://x-access-token:ghp_FAKE@github.com/owner/repo"
			_, err := ops.LsRemote(ctx, cloneURL, "main", "v1.2.8")
			Expect(err).NotTo(HaveOccurred())

			recorded := readRecordedArgs("argv")
			// argv order: `git ls-remote <cloneURL> refs/tags/<tag>`. The recorded
			// `$@` is a single space-separated line, so we split on whitespace
			// and assert that BOTH the cloneURL and the refs/tags/<tag> appear as
			// discrete tokens — NOT concatenated into one shell-expanded string.
			fields := strings.Fields(recorded)
			Expect(fields).To(ContainElement(cloneURL))
			Expect(fields).To(ContainElement("refs/tags/v1.2.8"))
			// And there are no extraneous concatenated forms.
			Expect(recorded).NotTo(ContainSubstring(cloneURL + "refs/tags/"))
		},
	)
})

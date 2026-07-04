// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubreview_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/github-releaser-agent/pkg/githubreview"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("httpClient", func() {
	ctx := context.Background()

	Describe("TagExists", func() {
		It("returns tag SHA on 200", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"ref": "refs/tags/v1.0.0",
						"object": map[string]string{
							"sha":  "abc123def456",
							"type": "commit",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			sha, err := client.TagExists(ctx, "bborbe", "maintainer", "v1.0.0")

			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(Equal("abc123def456"))
		})

		It("returns ErrTagNotFound on 404", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "v99.0.0")

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, githubreview.ErrTagNotFound)).To(BeTrue())
		})

		It("returns wrapped error on 5xx", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TagExists"))
			Expect(err.Error()).To(ContainSubstring("status 503"))
		})

		It("rejects empty owner", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.TagExists(ctx, "", "maintainer", "v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TagExists"))
			Expect(err.Error()).To(ContainSubstring("owner/repo/tag must be non-empty"))
		})

		It("rejects empty repo", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.TagExists(ctx, "bborbe", "", "v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TagExists"))
			Expect(err.Error()).To(ContainSubstring("owner/repo/tag must be non-empty"))
		})

		It("rejects empty tag", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TagExists"))
			Expect(err.Error()).To(ContainSubstring("owner/repo/tag must be non-empty"))
		})

		It("sets Authorization header", func() {
			var capturedAuth string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAuth = r.Header.Get("Authorization")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"ref": "refs/tags/v1.0.0",
						"object": map[string]string{
							"sha":  "abc123",
							"type": "commit",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("my-secret-token", server.URL)
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "v1.0.0")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedAuth).To(Equal("Bearer my-secret-token"))
		})

		It("sets Accept and X-GitHub-Api-Version headers", func() {
			var capturedAccept, capturedVersion string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAccept = r.Header.Get("Accept")
					capturedVersion = r.Header.Get("X-GitHub-Api-Version")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"ref": "refs/tags/v1.0.0",
						"object": map[string]string{
							"sha":  "abc123",
							"type": "commit",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "v1.0.0")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedAccept).To(Equal("application/vnd.github+json"))
			Expect(capturedVersion).To(Equal("2022-11-28"))
		})

		It("returns error on malformed JSON", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte("not-json"))
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TagExists"))
			Expect(err.Error()).To(ContainSubstring("decode json"))
		})
	})

	Describe("ResolveTagCommit", func() {
		It("returns commit SHA for lightweight tag (type=commit)", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"sha": "tag-sha-123",
						"object": map[string]string{
							"sha":  "commit-sha-456",
							"type": "commit",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			sha, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "tag-sha-123")

			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(Equal("commit-sha-456"))
		})

		It("returns error for chained annotated tag (type=tag, tag-of-tag)", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"sha": "tag-sha-123",
						"object": map[string]string{
							"sha":  "wrapped-tag-sha-789",
							"type": "tag",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			sha, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "tag-sha-123")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("chained annotated tag"))
			Expect(err.Error()).To(ContainSubstring("tag-sha-123"))
			Expect(sha).To(BeEmpty())
		})

		It("returns error for unknown type", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"sha": "tag-sha-123",
						"object": map[string]string{
							"sha":  "some-sha",
							"type": "blob",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "tag-sha-123")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ResolveTagCommit"))
			Expect(err.Error()).To(ContainSubstring("unknown tag object type"))
		})

		It("returns wrapped error on 5xx", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "tag-sha-123")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ResolveTagCommit"))
			Expect(err.Error()).To(ContainSubstring("status 503"))
		})

		It("rejects empty owner", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.ResolveTagCommit(ctx, "", "maintainer", "tag-sha-123")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ResolveTagCommit"))
			Expect(err.Error()).To(ContainSubstring("owner/repo/tagSHA must be non-empty"))
		})

		It("rejects empty repo", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.ResolveTagCommit(ctx, "bborbe", "", "tag-sha-123")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ResolveTagCommit"))
			Expect(err.Error()).To(ContainSubstring("owner/repo/tagSHA must be non-empty"))
		})

		It("rejects empty tagSHA", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ResolveTagCommit"))
			Expect(err.Error()).To(ContainSubstring("owner/repo/tagSHA must be non-empty"))
		})

		It("returns error on malformed JSON", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte("not-json"))
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "tag-sha-123")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ResolveTagCommit"))
			Expect(err.Error()).To(ContainSubstring("decode json"))
		})
	})

	Describe("FetchChangelog", func() {
		It("returns decoded base64 content", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "base64",
						"content":  "IyMgVW5yZWxlYXNlZAo=",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			data, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).NotTo(HaveOccurred())
			Expect(data).To(Equal([]byte("## Unreleased\n")))
		})

		It("strips newlines from base64 content", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "base64",
						"content":  "IyMgVW5y\nZWxlYXNlZAo=",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			data, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).NotTo(HaveOccurred())
			Expect(data).To(Equal([]byte("## Unreleased\n")))
		})

		It("returns error for non-base64 encoding", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "utf-8",
						"content":  "some content",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FetchChangelog"))
			Expect(err.Error()).To(ContainSubstring("unsupported encoding"))
		})

		It("returns error on bad base64", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "base64",
						"content":  "!!!not-base64!!!",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FetchChangelog"))
			Expect(err.Error()).To(ContainSubstring("base64 decode"))
		})

		It("returns wrapped error on 5xx", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FetchChangelog"))
			Expect(err.Error()).To(ContainSubstring("status 503"))
		})

		It("rejects empty owner", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.FetchChangelog(ctx, "", "maintainer")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FetchChangelog"))
			Expect(err.Error()).To(ContainSubstring("owner/repo must be non-empty"))
		})

		It("rejects empty repo", func() {
			client := githubreview.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.FetchChangelog(ctx, "bborbe", "")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FetchChangelog"))
			Expect(err.Error()).To(ContainSubstring("owner/repo must be non-empty"))
		})

		It("returns error on malformed JSON", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte("not-json"))
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FetchChangelog"))
			Expect(err.Error()).To(ContainSubstring("decode json"))
		})

		It("does not include ?ref= in URL", func() {
			var capturedURL string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedURL = r.URL.String()
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "base64",
						"content":  "IyMgVW5yZWxlYXNlZAo=",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedURL).ToNot(ContainSubstring("?ref="))
			Expect(capturedURL).To(ContainSubstring("/contents/CHANGELOG.md"))
		})

		It("sets Authorization header", func() {
			var capturedAuth string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAuth = r.Header.Get("Authorization")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "base64",
						"content":  "IyMgVW5yZWxlYXNlZAo=",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("my-secret-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedAuth).To(Equal("Bearer my-secret-token"))
		})

		It("strips carriage returns from base64 content", func() {
			// Use raw JSON to include actual CR+LF bytes (not escape sequences)
			// since json.Encode would double-encode them.
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					// Base64 of "## Unreleased\n" is "IyMgVW5yZWxlYXNlZAo=\n"
					// We embed literal CR (0x0d) and LF (0x0a) bytes in the content.
					rawJSON := "{\"encoding\":\"base64\",\"content\":\"IyMgVW5yZWxlYXNlZAo=\\r\\n\"}"
					_, _ = w.Write([]byte(rawJSON))
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			data, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).NotTo(HaveOccurred())
			Expect(data).To(Equal([]byte("## Unreleased\n")))
		})
	})

	Describe("Error message sanitization", func() {
		It("bearer token does not appear in error messages", func() {
			// #nosec G101 -- test token, not a real credential
			token := "ghp_verylongtokenthatweshouldnotsee1234567890abcdef"
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest(token, server.URL)
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring(token))
			Expect(err.Error()).ToNot(ContainSubstring("ghp_"))
		})

		It("bearer token does not appear in FetchChangelog errors", func() {
			// #nosec G101 -- test token, not a real credential
			token := "ghp_verylongtokenthatweshouldnotsee1234567890abcdef"
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest(token, server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring(token))
			Expect(err.Error()).ToNot(ContainSubstring("ghp_"))
		})
	})

	Describe("ResolveTagCommit headers", func() {
		It("sets Authorization header", func() {
			var capturedAuth string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAuth = r.Header.Get("Authorization")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"sha": "tag-sha-123",
						"object": map[string]string{
							"sha":  "commit-sha-456",
							"type": "commit",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token-xyz", server.URL)
			_, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "tag-sha-123")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedAuth).To(Equal("Bearer test-token-xyz"))
		})

		It("sets Accept and X-GitHub-Api-Version headers", func() {
			var capturedAccept, capturedVersion string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAccept = r.Header.Get("Accept")
					capturedVersion = r.Header.Get("X-GitHub-Api-Version")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"sha": "tag-sha-123",
						"object": map[string]string{
							"sha":  "commit-sha-456",
							"type": "commit",
						},
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "tag-sha-123")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedAccept).To(Equal("application/vnd.github+json"))
			Expect(capturedVersion).To(Equal("2022-11-28"))
		})

		It("returns error on 404", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.ResolveTagCommit(ctx, "bborbe", "maintainer", "nonexistent")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ResolveTagCommit"))
			Expect(err.Error()).To(ContainSubstring("status 404"))
		})
	})

	Describe("FetchChangelog headers", func() {
		It("sets Authorization header", func() {
			var capturedAuth string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAuth = r.Header.Get("Authorization")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "base64",
						"content":  "IyMgVW5yZWxlYXNlZAo=",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token-abc", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedAuth).To(Equal("Bearer test-token-abc"))
		})

		It("sets Accept and X-GitHub-Api-Version headers", func() {
			var capturedAccept, capturedVersion string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAccept = r.Header.Get("Accept")
					capturedVersion = r.Header.Get("X-GitHub-Api-Version")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"encoding": "base64",
						"content":  "IyMgVW5yZWxlYXNlZAo=",
					})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).NotTo(HaveOccurred())
			Expect(capturedAccept).To(Equal("application/vnd.github+json"))
			Expect(capturedVersion).To(Equal("2022-11-28"))
		})

		It("returns error on 404", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				}),
			)
			defer server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.FetchChangelog(ctx, "bborbe", "maintainer")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FetchChangelog"))
			Expect(err.Error()).To(ContainSubstring("status 404"))
		})
	})

	Describe("TagExists transport error", func() {
		It("returns wrapped error on transport failure", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Close immediately without responding
					w.WriteHeader(http.StatusOK)
				}),
			)
			server.Close()

			client := githubreview.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.TagExists(ctx, "bborbe", "maintainer", "v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TagExists"))
			Expect(err.Error()).To(ContainSubstring("http"))
		})
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubchangelog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/github-releaser-agent/pkg/githubchangelog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("httpFetcher", func() {
	ctx := context.Background()

	It("happy path: returns decoded base64 content", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  "IyMgVW5yZWxlYXNlZAo=",
			})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		data, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(Equal([]byte("## Unreleased\n")))
	})

	It("authorization header forwarded", func() {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  "IyMgVW5yZWxlYXNlZAo=",
			})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")
		Expect(err).NotTo(HaveOccurred())
		Expect(capturedAuth).To(Equal("Bearer test-token"))
	})

	It("no auth header when token empty", func() {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  "IyMgVW5yZWxlYXNlZAo=",
			})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")
		Expect(err).NotTo(HaveOccurred())
		Expect(capturedAuth).To(BeEmpty())
	})

	It("url contains owner/repo/ref", func() {
		var capturedPath, capturedRef string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			capturedRef = r.URL.Query().Get("ref")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  "IyMgVW5yZWxlYXNlZAo=",
			})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "foo", "bar", "mybranch")
		Expect(err).NotTo(HaveOccurred())
		Expect(capturedPath).To(Equal("/repos/foo/bar/contents/CHANGELOG.md"))
		Expect(capturedRef).To(Equal("mybranch"))
	})

	It("404 returns wrapped error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("fetch CHANGELOG.md"))
		Expect(err.Error()).To(ContainSubstring("status 404"))
	})

	It("5xx returns wrapped error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("fetch CHANGELOG.md"))
		Expect(err.Error()).To(ContainSubstring("status 503"))
	})

	It("malformed JSON returns wrapped error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not-json"))
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decode json"))
	})

	It("unsupported encoding rejected", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "utf-8",
				"content":  "hi",
			})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unsupported encoding \"utf-8\""))
	})

	It("bad base64 returns wrapped error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  "!!!not-base64!!!",
			})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("base64 decode"))
	})

	It("empty owner rejected", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("owner empty"))
	})

	It("empty repo rejected", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("repo empty"))
	})

	It("empty ref rejected", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ref empty"))
	})

	It("newlines in base64 content stripped", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Base64 of "## Unreleased\n" with embedded newline
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  "IyMgVW5y\nZWxlYXNlZAo=",
			})
		}))
		defer server.Close()

		fetcher := githubchangelog.NewHTTPFetcherForTest("test-token", server.URL)
		data, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(Equal([]byte("## Unreleased\n")))
	})
})

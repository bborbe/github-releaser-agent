// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubpullrequest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/github-releaser-agent/pkg/githubpullrequest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("httpClient", func() {
	ctx := context.Background()

	Describe("DefaultBranch", func() {
		It("returns the decoded default branch on 200", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"default_branch": "master",
					})
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			branch, err := client.DefaultBranch(ctx, "bborbe", "example")

			Expect(err).NotTo(HaveOccurred())
			Expect(branch).To(Equal("master"))
		})

		It("returns an error on 500", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.DefaultBranch(ctx, "bborbe", "example")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("DefaultBranch"))
			Expect(err.Error()).To(ContainSubstring("status 500"))
		})

		It("returns an error on an empty default_branch", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"default_branch": ""})
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.DefaultBranch(ctx, "bborbe", "example")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("empty default_branch"))
		})

		It("rejects empty owner without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.DefaultBranch(ctx, "", "example")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("owner/repo must be non-empty"))
		})

		It("rejects empty repo without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.DefaultBranch(ctx, "bborbe", "")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("owner/repo must be non-empty"))
		})
	})

	Describe("FindOpenPullRequest", func() {
		It("returns the html_url and found=true for a one-element array", func() {
			var capturedQuery string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedQuery = r.URL.RawQuery
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode([]map[string]string{
						{"html_url": "https://github.com/bborbe/example/pull/7"},
					})
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			prURL, found, err := client.FindOpenPullRequest(
				ctx, "bborbe", "example", "release/v1.0.0",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(prURL).To(Equal("https://github.com/bborbe/example/pull/7"))
			Expect(capturedQuery).To(ContainSubstring("state=open"))
			Expect(capturedQuery).To(ContainSubstring("head=bborbe%3Arelease%2Fv1.0.0"))
		})

		It("returns found=false for an empty array", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode([]map[string]string{})
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			prURL, found, err := client.FindOpenPullRequest(
				ctx, "bborbe", "example", "release/v1.0.0",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(prURL).To(BeEmpty())
		})

		It("returns an error on 500", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			_, _, err := client.FindOpenPullRequest(
				ctx, "bborbe", "example", "release/v1.0.0",
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("FindOpenPullRequest"))
			Expect(err.Error()).To(ContainSubstring("status 500"))
		})

		It("rejects empty owner without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, _, err := client.FindOpenPullRequest(ctx, "", "example", "release/v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("owner/repo/headBranch must be non-empty"))
		})

		It("rejects empty repo without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, _, err := client.FindOpenPullRequest(ctx, "bborbe", "", "release/v1.0.0")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("owner/repo/headBranch must be non-empty"))
		})

		It("rejects empty head branch without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, _, err := client.FindOpenPullRequest(ctx, "bborbe", "example", "")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("owner/repo/headBranch must be non-empty"))
		})
	})

	Describe("CreatePullRequest", func() {
		It("posts the arguments and returns the html_url on 201", func() {
			var posted map[string]string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.Method).To(Equal(http.MethodPost))
					Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
					Expect(json.NewDecoder(r.Body).Decode(&posted)).To(Succeed())
					w.WriteHeader(http.StatusCreated)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"html_url": "https://github.com/bborbe/example/pull/8",
					})
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			prURL, err := client.CreatePullRequest(
				ctx,
				"bborbe",
				"example",
				"release v1.0.0",
				"release/v1.0.0",
				"master",
				"- feat: add foo",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(prURL).To(Equal("https://github.com/bborbe/example/pull/8"))
			Expect(posted).To(Equal(map[string]string{
				"title": "release v1.0.0",
				"head":  "release/v1.0.0",
				"base":  "master",
				"body":  "- feat: add foo",
			}))
		})

		It("returns an error on 422", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"message":"Validation Failed"}`))
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.CreatePullRequest(
				ctx, "bborbe", "example", "title", "head", "base", "body",
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("CreatePullRequest"))
			Expect(err.Error()).To(ContainSubstring("status 422"))
			Expect(err.Error()).To(ContainSubstring("Validation Failed"))
		})

		It("returns an error on an empty html_url", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusCreated)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"html_url": ""})
				}),
			)
			defer server.Close()

			client := githubpullrequest.NewHTTPClientForTest("test-token", server.URL)
			_, err := client.CreatePullRequest(
				ctx, "bborbe", "example", "title", "head", "base", "body",
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("empty html_url"))
		})

		It("rejects empty owner without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.CreatePullRequest(
				ctx, "", "example", "title", "head", "base", "body",
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).
				To(ContainSubstring("owner/repo/title/head/base must be non-empty"))
		})

		It("rejects empty repo without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.CreatePullRequest(
				ctx, "bborbe", "", "title", "head", "base", "body",
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).
				To(ContainSubstring("owner/repo/title/head/base must be non-empty"))
		})

		It("rejects an empty head branch without an HTTP call", func() {
			client := githubpullrequest.NewHTTPClientForTest("test-token", "https://api.github.com")
			_, err := client.CreatePullRequest(
				ctx, "bborbe", "example", "title", "", "base", "body",
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).
				To(ContainSubstring("owner/repo/title/head/base must be non-empty"))
		})
	})
})

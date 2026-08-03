// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubtags_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/bborbe/github-releaser-agent/pkg/githubtags"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("httpTagsFetcher", func() {
	ctx := context.Background()

	tagJSON := func(names []string) string {
		items := make([]map[string]string, len(names))
		for i, n := range names {
			items[i] = map[string]string{"name": n}
		}
		data, _ := json.Marshal(items)
		return string(data)
	}

	It("1: highest-semver selection", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"v0.101.0", "v0.101.1", "v0.100.9"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(latest).To(Equal("v0.101.1"))
	})

	It("2: creation-order is NOT semver-order (scrambled input)", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"v0.9.0", "v1.0.0", "v0.5.0"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(latest).To(Equal("v1.0.0"))
	})

	It("3: non-semver tags skipped", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"latest", "v0.101.1", "nightly", "v0.101.0"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(latest).To(Equal("v0.101.1"))
	})

	It("4: prefix preserved (no v added)", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"0.101.1", "0.101.0"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(latest).To(Equal("0.101.1"))
	})

	It("5: zero tags → ErrNoTags", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, githubtags.ErrNoTags)).To(BeTrue())
		Expect(latest).To(BeEmpty())
	})

	It("6: all-non-semver → ErrNoTags", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"latest", "nightly"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, githubtags.ErrNoTags)).To(BeTrue())
		Expect(latest).To(BeEmpty())
	})

	It("7: 5xx transport error → wrapped hard error, NOT ErrNoTags", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, githubtags.ErrNoTags)).To(BeFalse())
		Expect(err.Error()).To(ContainSubstring("list tags"))
		Expect(err.Error()).To(ContainSubstring("status 503"))
		Expect(latest).To(BeEmpty())
	})

	It("8: 404 → wrapped hard error, NOT ErrNoTags", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, githubtags.ErrNoTags)).To(BeFalse())
		Expect(err.Error()).To(ContainSubstring("list tags"))
		Expect(err.Error()).To(ContainSubstring("status 404"))
		Expect(latest).To(BeEmpty())
	})

	It("9: malformed JSON → wrapped decode error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not-json"))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decode json"))
		Expect(latest).To(BeEmpty())
	})

	It("10: auth header forwarded", func() {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"v1.0.0"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(capturedAuth).To(Equal("Bearer test-token"))
	})

	It("11: no auth header when token empty", func() {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"v1.0.0"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("", server.URL)
		_, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(capturedAuth).To(BeEmpty())
	})

	It("12: URL asserts path + per_page=100", func() {
		var capturedPath, capturedPerPage string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			capturedPerPage = r.URL.Query().Get("per_page")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"v1.0.0"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(capturedPath).To(Equal("/repos/foo/bar/tags"))
		Expect(capturedPerPage).To(Equal("100"))
	})

	It("13: empty owner rejected", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.LatestSemverTag(ctx, "", "bar")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("owner empty"))
	})

	It("14: empty repo rejected", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.LatestSemverTag(ctx, "foo", "")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("repo empty"))
	})

	It("15: pagination — highest tag on page 2 (load-bearing)", func() {
		mux := http.NewServeMux()
		var serverBase string
		mux.HandleFunc("/repos/foo/bar/tags", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				// Page 1: 100 low tags + Link header pointing to page 2
				var tags []map[string]string
				for i := 0; i < 100; i++ {
					tags = append(tags, map[string]string{"name": fmt.Sprintf("v0.100.%d", i)})
				}
				nextURL := serverBase + "/repos/foo/bar/tags?page=2"
				w.Header().Set("Link", "<"+nextURL+">; rel=\"next\"")
				data, _ := json.Marshal(tags)
				_, _ = w.Write(data)
			} else {
				// Page 2: highest tag, no Link header
				_, _ = w.Write([]byte(tagJSON([]string{"v0.101.1"})))
			}
		})
		server := httptest.NewServer(mux)
		serverBase = server.URL
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(latest).To(Equal("v0.101.1"))
	})

	It("16: no Link header → single page, exactly one request", func() {
		requestCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSON([]string{"v1.0.0", "v0.9.0"})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		latest, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).NotTo(HaveOccurred())
		Expect(latest).To(Equal("v1.0.0"))
		Expect(requestCount).To(Equal(1))
	})

	It("17: nextLink helper — next/last/prev", func() {
		header := `<https://api.github.com/repos/foo/bar/tags?per_page=100&page=2>; rel="next", <https://api.github.com/repos/foo/bar/tags?per_page=100&page=5>; rel="last"`
		result := githubtags.NextLink(header)
		Expect(result).To(Equal("https://api.github.com/repos/foo/bar/tags?per_page=100&page=2"))
	})

	It("17b: nextLink — only last/prev, no next", func() {
		header := `<https://api.github.com/repos/foo/bar/tags?per_page=100&page=5>; rel="last", <https://api.github.com/repos/foo/bar/tags?per_page=100&page=1>; rel="prev"`
		result := githubtags.NextLink(header)
		Expect(result).To(BeEmpty())
	})

	It("17c: nextLink — empty header", func() {
		result := githubtags.NextLink("")
		Expect(result).To(BeEmpty())
	})

	It("18: too-many-pages cap returns wrapped error", func() {
		mux := http.NewServeMux()
		var serverBase string
		mux.HandleFunc("/repos/foo/bar/tags", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Set Link BEFORE writing body (Write sends headers immediately)
			page := r.URL.Query().Get("page")
			nextPage := 2
			if page != "" {
				if p, err := strconv.Atoi(page); err == nil {
					nextPage = p + 1
				}
			}
			nextURL := serverBase + fmt.Sprintf(
				"/repos/foo/bar/tags?per_page=100&page=%d",
				nextPage,
			)
			w.Header().Set("Link", "<"+nextURL+">; rel=\"next\"")
			_, _ = w.Write([]byte(tagJSON([]string{"v0.0.1"})))
		})
		server := httptest.NewServer(mux)
		serverBase = server.URL
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.LatestSemverTag(ctx, "foo", "bar")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("too many pages"))
	})

	tagJSONWithCommits := func(entries [][2]string) string {
		items := make([]map[string]any, len(entries))
		for i, e := range entries {
			items[i] = map[string]any{
				"name":   e[0],
				"commit": map[string]string{"sha": e[1]},
			}
		}
		data, _ := json.Marshal(items)
		return string(data)
	}

	It("19: dereferenced commit SHA for annotated tag (spec AC #10)", func() {
		// The annotated tag OBJECT sha for v0.3.1 is 9aa07bf6c9a64221e340b5529f7e47faf2f189fd;
		// the tags-list endpoint returns the COMMIT sha directly (7657fc0af1a115b2518ac4c4d332722d8fc3d35c),
		// confirming GitHub dereferences annotated tags on this endpoint.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
				{"v0.3.1", "7657fc0af1a115b2518ac4c4d332722d8fc3d35c"},
				{"v0.3.0", "1111111111111111111111111111111111111111"},
			})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		sha, err := fetcher.CommitSHAForTag(ctx, "bborbe", "tts-mcp", "v0.3.1")

		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal("7657fc0af1a115b2518ac4c4d332722d8fc3d35c"))
	})

	It("20: lightweight tag — same code path", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
				{"v1.0.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				{"v0.9.0", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		sha, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v0.9.0")

		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	})

	It("21: tag absent → ErrTagNotFound", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
				{"v0.3.1", "7657fc0af1a115b2518ac4c4d332722d8fc3d35c"},
				{"v0.3.0", "1111111111111111111111111111111111111111"},
			})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		sha, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v9.9.9")

		Expect(sha).To(BeEmpty())
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, githubtags.ErrTagNotFound)).To(BeTrue())
	})

	It("22: exact-match only (no v-prefix normalisation)", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
				{"v0.3.1", "7657fc0af1a115b2518ac4c4d332722d8fc3d35c"},
			})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)

		// no v prefix
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "0.3.1")
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, githubtags.ErrTagNotFound)).To(BeTrue())

		// capital V
		_, err = fetcher.CommitSHAForTag(ctx, "foo", "bar", "V0.3.1")
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, githubtags.ErrTagNotFound)).To(BeTrue())
	})

	It("23: empty tag argument — no HTTP request", func() {
		requestCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("tag empty"))
		Expect(requestCount).To(Equal(0))
	})

	It("24: empty owner / empty repo", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)

		_, err := fetcher.CommitSHAForTag(ctx, "", "bar", "v1.0.0")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("owner empty"))

		_, err = fetcher.CommitSHAForTag(ctx, "foo", "", "v1.0.0")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("repo empty"))
	})

	It("25: non-2xx is hard error, NOT ErrTagNotFound", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v1.0.0")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("status 503"))
		Expect(errors.Is(err, githubtags.ErrTagNotFound)).To(BeFalse())
	})

	It("25b: 404 is hard error, NOT ErrTagNotFound", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v1.0.0")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("status 404"))
		Expect(errors.Is(err, githubtags.ErrTagNotFound)).To(BeFalse())
	})

	It("26: malformed JSON → decode error, NOT ErrTagNotFound", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not-json"))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v1.0.0")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decode json"))
		Expect(errors.Is(err, githubtags.ErrTagNotFound)).To(BeFalse())
	})

	It("27: entry with empty commit sha → hard error, NOT ErrTagNotFound", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
				{"v0.3.1", ""},
			})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v0.3.1")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty commit sha"))
		Expect(errors.Is(err, githubtags.ErrTagNotFound)).To(BeFalse())
	})

	It("28: pagination — tag lives on page 2", func() {
		mux := http.NewServeMux()
		var serverBase string
		mux.HandleFunc("/repos/foo/bar/tags", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				// Page 1: 100 unrelated tags + Link header pointing to page 2
				var tags []map[string]any
				for i := 0; i < 100; i++ {
					tags = append(tags, map[string]any{
						"name":   fmt.Sprintf("v0.100.%d", i),
						"commit": map[string]string{"sha": fmt.Sprintf("aaaaaaaa%02d", i)},
					})
				}
				nextURL := serverBase + "/repos/foo/bar/tags?page=2"
				w.Header().Set("Link", "<"+nextURL+">; rel=\"next\"")
				data, _ := json.Marshal(tags)
				_, _ = w.Write(data)
			} else {
				// Page 2: target tag, no Link header
				_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
					{"v0.3.1", "7657fc0af1a115b2518ac4c4d332722d8fc3d35c"},
				})))
			}
		})
		server := httptest.NewServer(mux)
		serverBase = server.URL
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		sha, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v0.3.1")

		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal("7657fc0af1a115b2518ac4c4d332722d8fc3d35c"))
	})

	It("29: request cost — exactly one pagination", func() {
		requestCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
				{"v1.0.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v1.0.0")

		Expect(err).NotTo(HaveOccurred())
		Expect(requestCount).To(Equal(1))
	})

	It("30: auth header forwarded", func() {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagJSONWithCommits([][2]string{
				{"v1.0.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			})))
		}))
		defer server.Close()

		fetcher := githubtags.NewHTTPTagsFetcherForTest("test-token", server.URL)
		_, err := fetcher.CommitSHAForTag(ctx, "foo", "bar", "v1.0.0")

		Expect(err).NotTo(HaveOccurred())
		Expect(capturedAuth).To(Equal("Bearer test-token"))
	})
})

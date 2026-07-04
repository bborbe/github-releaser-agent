// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintainerconfig_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-releaser-agent/pkg/maintainerconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("httpFetcher", func() {
	ctx := context.Background()

	It("happy path: 200 OK with valid base64 YAML returns decoded bytes", func() {
		yamlBytes := []byte("release:\n  changelogRewrite: true\n")
		encoded := base64.StdEncoding.EncodeToString(yamlBytes)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/repos/bborbe/maintainer/contents/.maintainer.yaml"))
			Expect(r.URL.Query().Get("ref")).To(Equal("master"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  encoded,
			})
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		data, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(Equal(yamlBytes))
	})

	It("404: file absent returns ErrFileNotFound", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		data, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(errors.Is(err, maintainerconfig.ErrFileNotFound)).To(BeTrue())
		Expect(data).To(BeNil())
	})

	It("500: server error returns wrapped non-2xx error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		data, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, maintainerconfig.ErrFileNotFound)).To(BeFalse())
		Expect(err.Error()).To(ContainSubstring("status 500"))
		Expect(data).To(BeNil())
	})

	It("500 response with sensitive body returns redacted error", func() {
		secret := "/internal/path/credentials.json missing — uid=42"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(secret))
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("status 500"))
		Expect(err.Error()).To(ContainSubstring("body_sha256_prefix="))
		Expect(err.Error()).To(ContainSubstring("body_bytes="))
		// CRITICAL: the raw body bytes MUST NOT leak into the error.
		Expect(err.Error()).NotTo(ContainSubstring("/internal/path"))
		Expect(err.Error()).NotTo(ContainSubstring("credentials.json"))
		Expect(err.Error()).NotTo(ContainSubstring("uid=42"))
	})

	It("oversize body rejected with cap in error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Write 2 MiB of data — exceeds the 1 MiB cap. Shape doesn't
			// matter; MaxBytesReader trips before JSON parse.
			big := make([]byte, 2<<20)
			_, _ = w.Write(big)
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		data, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("read body"))
		Expect(err.Error()).To(ContainSubstring("cap="))
		Expect(data).To(BeNil())
	})

	It("empty owner rejected with descriptive error", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "", "maintainer", "master")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("owner empty"))
	})

	It("empty repo rejected with descriptive error", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "", "master")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("repo empty"))
	})

	It("empty ref rejected with descriptive error", func() {
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		)
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ref empty"))
	})

	It("fetch bytes parse cleanly via the lib parser (round-trip test)", func() {
		yamlBytes := []byte("release:\n  changelogRewrite: true\n")
		encoded := base64.StdEncoding.EncodeToString(yamlBytes)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content":  encoded,
			})
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		data, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")
		Expect(err).NotTo(HaveOccurred())

		cfg, err := maintainerconfig.Parse(ctx, data)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Release.ChangelogRewrite).To(BeTrue())
	})

	It("authorization header forwarded when token is set", func() {
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"encoding": "base64",
				"content": base64.StdEncoding.EncodeToString(
					[]byte("release:\n  autoRelease: true\n"),
				),
			})
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("test-token", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")
		Expect(err).NotTo(HaveOccurred())
		Expect(capturedAuth).To(Equal("Bearer test-token"))
	})

	It("malformed JSON returns wrapped error", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("not-json"))
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
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

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`unsupported encoding "utf-8"`))
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

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("base64 decode"))
	})

	It("empty 200 OK body with no encoding field rejected", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		fetcher := maintainerconfig.NewHTTPFetcherForTest("", server.URL)
		_, err := fetcher.Fetch(ctx, "bborbe", "maintainer", "master")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`unsupported encoding ""`))
	})
})

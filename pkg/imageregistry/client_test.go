// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package imageregistry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/github-releaser-agent/pkg/imageregistry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("httpClient", func() {
	ctx := context.Background()

	Describe("ManifestExists", func() {
		It("sends a HEAD with the manifest Accept types and returns true on 200", func() {
			var method, path, accept string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					method = r.Method
					path = r.URL.Path
					accept = r.Header.Get("Accept")
					w.WriteHeader(http.StatusOK)
				}),
			)
			defer server.Close()

			client := imageregistry.NewClientForTest(server.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.io/bborbe/example:v1.0.0",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())
			Expect(method).To(Equal(http.MethodHead))
			Expect(path).To(Equal("/v2/bborbe/example/manifests/v1.0.0"))
			Expect(accept).To(ContainSubstring(
				"application/vnd.docker.distribution.manifest.list.v2+json",
			))
			Expect(accept).To(ContainSubstring("application/vnd.oci.image.index.v1+json"))
			Expect(accept).To(ContainSubstring(
				"application/vnd.docker.distribution.manifest.v2+json",
			))
			Expect(accept).To(ContainSubstring("application/vnd.oci.image.manifest.v1+json"))
		})

		It("returns false on 404", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}),
			)
			defer server.Close()

			client := imageregistry.NewClientForTest(server.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.io/bborbe/example:v9.9.9",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse())
		})

		It("returns a wrapped error on 500", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
			)
			defer server.Close()

			client := imageregistry.NewClientForTest(server.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.io/bborbe/example:v1.0.0",
			)

			Expect(err).To(HaveOccurred())
			Expect(exists).To(BeFalse())
			Expect(err.Error()).To(ContainSubstring("status 500"))
			Expect(err.Error()).To(ContainSubstring("docker.io/bborbe/example:v1.0.0"))
		})

		It("returns a wrapped error on a transport failure", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			)
			server.Close()

			client := imageregistry.NewClientForTest(server.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.io/bborbe/example:v1.0.0",
			)

			Expect(err).To(HaveOccurred())
			Expect(exists).To(BeFalse())
			Expect(err.Error()).To(ContainSubstring("image registry"))
		})

		It("follows the 401 Bearer challenge once and retries with the token", func() {
			var headCalls int
			var tokenQuery string
			var retriedAuth string
			var srv *httptest.Server
			srv = httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						tokenQuery = r.URL.RawQuery
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]string{
							"token": "pull-token",
						})
						return
					}
					headCalls++
					if r.Header.Get("Authorization") == "" {
						w.Header().Set(
							"WWW-Authenticate",
							`Bearer realm="`+srv.URL+`/token",service="registry.docker.io"`,
						)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					retriedAuth = r.Header.Get("Authorization")
					w.WriteHeader(http.StatusOK)
				}),
			)
			defer srv.Close()

			client := imageregistry.NewClientForTest(srv.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.io/bborbe/example:v1.0.0",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())
			Expect(headCalls).To(Equal(2))
			Expect(retriedAuth).To(Equal("Bearer pull-token"))
			Expect(tokenQuery).To(ContainSubstring("service=registry.docker.io"))
			Expect(tokenQuery).To(ContainSubstring(
				"scope=repository%3Abborbe%2Fexample%3Apull",
			))
		})

		It("returns an error when the 401 carries no usable Bearer challenge", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}),
			)
			defer server.Close()

			client := imageregistry.NewClientForTest(server.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.io/bborbe/example:v1.0.0",
			)

			Expect(err).To(HaveOccurred())
			Expect(exists).To(BeFalse())
			Expect(err.Error()).To(ContainSubstring("Bearer challenge"))
		})

		It("returns an error when the token exchange fails", func() {
			var srv *httptest.Server
			srv = httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.Header().Set(
						"WWW-Authenticate",
						`Bearer realm="`+srv.URL+`/token",service="registry.docker.io"`,
					)
					w.WriteHeader(http.StatusUnauthorized)
				}),
			)
			defer srv.Close()

			client := imageregistry.NewClientForTest(srv.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.io/bborbe/example:v1.0.0",
			)

			Expect(err).To(HaveOccurred())
			Expect(exists).To(BeFalse())
			Expect(err.Error()).To(ContainSubstring("token status 500"))
		})

		It("parses an image reference whose host carries a port", func() {
			var path string
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					path = r.URL.Path
					w.WriteHeader(http.StatusOK)
				}),
			)
			defer server.Close()

			client := imageregistry.NewClientForTest(server.URL)
			exists, err := client.ManifestExists(
				ctx,
				"docker.prod.nuke.benjamin-borbe.de:443/bborbe/example:v1.0.0",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())
			Expect(path).To(Equal("/v2/bborbe/example/manifests/v1.0.0"))
		})

		It("returns an error for an unparseable reference", func() {
			client := imageregistry.NewClientForTest("https://example.invalid")
			exists, err := client.ManifestExists(ctx, "docker.io/bborbe/example")

			Expect(err).To(HaveOccurred())
			Expect(exists).To(BeFalse())
			Expect(err.Error()).To(ContainSubstring("unparseable image reference"))
		})
	})

	Describe("registryAPIHost", func() {
		It("maps docker.io to registry-1.docker.io", func() {
			host, err := imageregistry.RegistryAPIHostForTest("docker.io")
			Expect(err).NotTo(HaveOccurred())
			Expect(host).To(Equal("registry-1.docker.io"))
		})

		It("maps ghcr.io identically", func() {
			host, err := imageregistry.RegistryAPIHostForTest("ghcr.io")
			Expect(err).NotTo(HaveOccurred())
			Expect(host).To(Equal("ghcr.io"))
		})

		It("maps the internal host identically, including its port", func() {
			host, err := imageregistry.RegistryAPIHostForTest(
				"docker.prod.nuke.benjamin-borbe.de:443",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(host).To(Equal("docker.prod.nuke.benjamin-borbe.de:443"))
		})

		It("returns an error for an unknown host", func() {
			host, err := imageregistry.RegistryAPIHostForTest("unknown.example")
			Expect(err).To(HaveOccurred())
			Expect(host).To(BeEmpty())
			Expect(err.Error()).To(ContainSubstring("unsupported image host"))
		})
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/bborbe/github-releaser-agent/pkg/githubauth"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const stubIAT = "ghs_test123456789"

// newIATServer returns an httptest server that mints stubIAT on the
// installation access-tokens endpoint. Mirrors lib/githubapp tests.
func newIATServer(installationID string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/"+installationID+"/access_tokens" &&
			r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(`{"token":"` + stubIAT + `","expires_at":"2099-01-01T00:00:00Z"}`),
			)
			return
		}
		http.NotFound(w, r)
	}))
}

var _ = Describe("Resolve", func() {
	var server *httptest.Server

	BeforeEach(func() {
		server = nil
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
			server = nil
		}
	})

	It(
		"App creds set → effective token is the minted IAT",
		func(ctx context.Context) {
			server = newIATServer("456")
			token, err := githubauth.Resolve(ctx, githubauth.Config{
				AppID:          123,
				InstallationID: 456,
				PEMKey:         string(generateRSAKey()),
				BaseURL:        server.URL,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(token).To(Equal(stubIAT))
		},
	)

	It(
		"App creds incomplete → error naming the required App env vars (no GH_TOKEN mention)",
		func(ctx context.Context) {
			token, err := githubauth.Resolve(ctx, githubauth.Config{})
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(githubauth.ErrAppCredentialsRequired))
			Expect(err.Error()).To(ContainSubstring("APP_ID"))
			Expect(err.Error()).To(ContainSubstring("INSTALLATION_ID"))
			Expect(err.Error()).NotTo(ContainSubstring("GH_TOKEN"))
			Expect(token).To(BeEmpty())
		},
	)

	It(
		"App creds with PEM file path → mints IAT (PEMKeyFile preferred over PEMKey)",
		func(ctx context.Context) {
			server = newIATServer("456")
			pemPath := writeTempPEM()
			token, err := githubauth.Resolve(ctx, githubauth.Config{
				AppID:          123,
				InstallationID: 456,
				PEMKeyFile:     pemPath,
				BaseURL:        server.URL,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(token).To(Equal(stubIAT))
		},
	)

	It(
		"both PEMKeyFile and PEMKey set → PEMKeyFile wins silently (file key mints the IAT)",
		func(ctx context.Context) {
			server = newIATServer("456")
			pemPath := writeTempPEM()
			token, err := githubauth.Resolve(ctx, githubauth.Config{
				AppID:          123,
				InstallationID: 456,
				PEMKeyFile:     pemPath,
				PEMKey:         "not-a-valid-pem",
				BaseURL:        server.URL,
			})
			// PEMKey is garbage; success proves PEMKeyFile (the valid file)
			// was the credential used to mint.
			Expect(err).NotTo(HaveOccurred())
			Expect(token).To(Equal(stubIAT))
		},
	)

	It(
		"PEMKeyFile points to a nonexistent path → mint error, empty token",
		func(ctx context.Context) {
			server = newIATServer("456")
			token, err := githubauth.Resolve(ctx, githubauth.Config{
				AppID:          123,
				InstallationID: 456,
				PEMKeyFile:     filepath.Join(GinkgoT().TempDir(), "does-not-exist.pem"),
				BaseURL:        server.URL,
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mint github app iat"))
			Expect(token).To(BeEmpty())
		},
	)

	It("malformed PEM → mint error before any token returned", func(ctx context.Context) {
		token, err := githubauth.Resolve(ctx, githubauth.Config{
			AppID:          123,
			InstallationID: 456,
			PEMKey:         "not-a-valid-pem",
		})
		Expect(err).To(HaveOccurred())
		Expect(token).To(BeEmpty())
	})
})

// ResolveAuthMode is exercised directly to lock the App-vs-None decision
// table independent of network.
var _ = Describe("ResolveAuthMode", func() {
	It("App when AppID+InstallationID+PEMKeyFile set", func() {
		Expect(githubauth.ResolveAuthMode(1, 2, "/k.pem", "")).
			To(Equal(githubauth.AuthModeGitHubApp))
	})
	It("App when AppID+InstallationID+PEMKey (env content) set", func() {
		Expect(githubauth.ResolveAuthMode(1, 2, "", "pem-content")).
			To(Equal(githubauth.AuthModeGitHubApp))
	})
	It("None when App ids present but no PEM", func() {
		Expect(githubauth.ResolveAuthMode(1, 2, "", "")).
			To(Equal(githubauth.AuthModeNone))
	})
	It("None when PEM present but ids missing", func() {
		Expect(githubauth.ResolveAuthMode(0, 0, "/k.pem", "")).
			To(Equal(githubauth.AuthModeNone))
	})
	It("None when nothing set", func() {
		Expect(githubauth.ResolveAuthMode(0, 0, "", "")).
			To(Equal(githubauth.AuthModeNone))
	})
})

// writeTempPEM writes a valid RSA PEM to a temp file in Ginkgo's
// auto-cleaned temp dir and returns its path. Using GinkgoT().TempDir()
// guarantees the file is removed after the spec, avoiding fd/file leaks
// across repeated runs.
func writeTempPEM() string {
	path := filepath.Join(GinkgoT().TempDir(), "githubauth-test.pem")
	Expect(os.WriteFile(path, generateRSAKey(), 0o600)).To(Succeed())
	return path
}

// generateRSAKey generates a valid RSA private key for testing.
// Copied verbatim from lib/githubapp/githubapp_test.go.
func generateRSAKey() []byte {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return pem.EncodeToMemory(block)
}

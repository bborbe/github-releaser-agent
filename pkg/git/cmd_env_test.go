// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"os"

	"github.com/bborbe/github-releaser-agent/pkg/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cmdEnv", func() {
	proxyKeys := []string{
		"HTTPS_PROXY",
		"https_proxy",
		"HTTP_PROXY",
		"http_proxy",
		"NO_PROXY",
		"no_proxy",
	}

	// setEnv sets key for the duration of the spec and restores the
	// previous value (or absence) afterwards.
	setEnv := func(key, value string) {
		previous, had := os.LookupEnv(key)
		DeferCleanup(func() {
			if had {
				Expect(os.Setenv(key, previous)).To(Succeed())
				return
			}
			Expect(os.Unsetenv(key)).To(Succeed())
		})
		Expect(os.Setenv(key, value)).To(Succeed())
	}

	unsetEnv := func(key string) {
		previous, had := os.LookupEnv(key)
		DeferCleanup(func() {
			if had {
				Expect(os.Setenv(key, previous)).To(Succeed())
			}
		})
		Expect(os.Unsetenv(key)).To(Succeed())
	}

	It("passes each proxy variable through when set", func() {
		for _, key := range proxyKeys {
			setEnv(key, "value-of-"+key)
		}

		env := git.CmdEnvForTest()

		for _, key := range proxyKeys {
			Expect(env).To(ContainElement(key + "=value-of-" + key))
		}
	})

	It("always carries HOME and PATH", func() {
		setEnv("HOME", "/home/test")
		setEnv("PATH", "/usr/bin:/bin")

		env := git.CmdEnvForTest()

		Expect(env).To(ContainElement("HOME=/home/test"))
		Expect(env).To(ContainElement("PATH=/usr/bin:/bin"))
	})

	It("omits proxy variables that are not set", func() {
		for _, key := range proxyKeys {
			unsetEnv(key)
		}

		env := git.CmdEnvForTest()

		Expect(env).To(HaveLen(2))
		for _, entry := range env {
			Expect(entry).To(Or(HavePrefix("HOME="), HavePrefix("PATH=")))
		}
	})

	It("does not leak unrelated pod-level variables", func() {
		setEnv("SOME_TOKEN", "secret-value")
		setEnv("GITHUB_TOKEN", "ghp-secret-value")
		for _, key := range proxyKeys {
			setEnv(key, "http://proxy:3128")
		}

		env := git.CmdEnvForTest()

		Expect(env).To(HaveLen(2 + len(proxyKeys)))
		for _, entry := range env {
			Expect(entry).NotTo(HavePrefix("SOME_TOKEN="))
			Expect(entry).NotTo(HavePrefix("GITHUB_TOKEN="))
			Expect(entry).NotTo(ContainSubstring("secret-value"))
		}
	})
})

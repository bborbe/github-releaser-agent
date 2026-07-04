// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package maintainerconfig fetches and exposes the .maintainer.yaml
// bytes for the github-releaser agent's planning step. The schema
// itself lives in github.com/bborbe/maintainer/lib/maintainerconfig
// (shared with the github-release watcher and pr-reviewer agent);
// this package adds ONLY the network seam.
package maintainerconfig

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"

	libmaintainerconfig "github.com/bborbe/maintainer/lib/maintainerconfig"
)

// Re-export the lib types so callers of this package need only one
// import for both the schema and the fetcher.
type (
	Config           = libmaintainerconfig.MaintainerConfig
	ReleaseConfig    = libmaintainerconfig.ReleaseConfig
	PrReviewerConfig = libmaintainerconfig.PrReviewerConfig
)

// Parse is a thin alias to lib/maintainerconfig.ParseStrict so the agent's
// planning step fails closed on unknown/typo'd keys. The watcher uses the
// lenient lib.Parse directly (fleet tolerance — see lib comment on Parse).
var Parse = libmaintainerconfig.ParseStrict

// fetchTimeout caps the GitHub contents-API call. Set high enough to
// survive typical transient latency, low enough to fail the planning
// step within the controller's per-step budget.
const fetchTimeout = 15 * time.Second

// maxConfigBodyBytes caps how many bytes the fetcher will read from the
// GitHub contents API response. .maintainer.yaml is realistically a few
// hundred bytes; 1 MiB is ~3000x that and still bounds malicious or
// misconfigured upstreams from exhausting agent memory.
const maxConfigBodyBytes = 1 << 20 // 1 MiB

// Fetcher reads .maintainer.yaml bytes from a remote GitHub repo at a ref.
// Implementations MUST be safe for concurrent use. Returned bytes are the
// raw decoded file contents (no base64, no JSON wrapper).
//
// HTTP 404 (file absent at the ref's tip) returns the sentinel ErrFileNotFound
// so callers can treat the absent-file case as a clean default-valued config
// (see spec 059 § Desired Behavior 6: missing .maintainer.yaml is treated as
// `changelogRewrite: false`).
//
//counterfeiter:generate -o ../../mocks/maintainer_config_fetcher.go --fake-name MaintainerConfigFetcher . Fetcher
type Fetcher interface {
	Fetch(ctx context.Context, owner, repo, ref string) ([]byte, error)
}

// ErrFileNotFound is returned by Fetch on HTTP 404. Callers use
// errors.Is(err, ErrFileNotFound) to treat the absent-file case as
// the default-valued config (same code path as "file absent"). Other
// errors are NOT covered by this sentinel and must NOT be silently
// downgraded to a default config.
//
// Sentinel pattern mirrors pkg/githubreview.ErrTagNotFound and
// pkg/githubauth.ErrAppCredentialsRequired (project convention).
var ErrFileNotFound = stderrors.New("maintainerconfig: .maintainer.yaml not found at ref")

// NewHTTPFetcher constructs a Fetcher backed by net/http against
// api.github.com. token is the bearer token (GitHub App IAT or PAT);
// empty token sends no Authorization header. The internal http.Client
// has a 15-second timeout — operations that exceed this return a wrapped
// context-deadline-exceeded error.
func NewHTTPFetcher(token string) Fetcher {
	return &httpFetcher{
		client:  &http.Client{Timeout: fetchTimeout},
		token:   token,
		apiBase: "https://api.github.com",
	}
}

// newHTTPFetcherWithBase is an internal constructor used by tests to
// point the fetcher at a test server. Not exported.
func newHTTPFetcherWithBase(token, apiBase string) Fetcher {
	return &httpFetcher{
		client:  &http.Client{Timeout: fetchTimeout},
		token:   token,
		apiBase: apiBase,
	}
}

type httpFetcher struct {
	client  *http.Client
	token   string
	apiBase string
}

// contentResponse is the slim JSON shape we read from /repos/.../contents/.
// Extra fields returned by GitHub are ignored.
type contentResponse struct {
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// Fetch implements Fetcher. Returns:
//   - ErrFileNotFound on HTTP 404 (file absent at the ref's tip)
//   - wrapped errors on:
//     empty owner/repo/ref (caller bug; "fetch .maintainer.yaml: owner empty" etc.)
//     request construction failure
//     HTTP transport failure (timeout, DNS, connection reset)
//     non-2xx non-404 response ("fetch .maintainer.yaml: status %d: %s")
//     JSON decode failure
//     unsupported encoding ("fetch .maintainer.yaml: unsupported encoding %q")
//     base64 decode failure
func (f *httpFetcher) Fetch(ctx context.Context, owner, repo, ref string) ([]byte, error) {
	if err := f.validateArgs(ctx, owner, repo, ref); err != nil {
		return nil, err
	}
	resp, err := f.doRequest(ctx, owner, repo, ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := f.readBody(ctx, resp)
	if err != nil {
		return nil, err
	}
	if err := f.checkStatus(ctx, resp, body, owner, repo, ref); err != nil {
		return nil, err
	}
	return f.decodeContent(ctx, body)
}

func (f *httpFetcher) validateArgs(
	ctx context.Context,
	owner, repo, ref string,
) error {
	if owner == "" {
		return errors.Errorf(ctx, "fetch .maintainer.yaml: owner empty")
	}
	if repo == "" {
		return errors.Errorf(ctx, "fetch .maintainer.yaml: repo empty")
	}
	if ref == "" {
		return errors.Errorf(ctx, "fetch .maintainer.yaml: ref empty")
	}
	return nil
}

func (f *httpFetcher) doRequest(
	ctx context.Context,
	owner, repo, ref string,
) (*http.Response, error) {
	// owner/repo are path segments, ref is a query value — escape each so a
	// crafted owner/repo/ref cannot corrupt the URL or inject query params.
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/contents/.maintainer.yaml?ref=%s",
		f.apiBase, url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(ref),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch .maintainer.yaml: build request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		glog.V(2).Infof(
			"fetch .maintainer.yaml: http transport error owner=%s repo=%s ref=%s: %v",
			owner, repo, ref, err,
		)
		return nil, errors.Wrapf(
			ctx,
			err,
			"fetch .maintainer.yaml: http %s/%s@%s",
			owner,
			repo,
			ref,
		)
	}
	return resp, nil
}

func (f *httpFetcher) readBody(ctx context.Context, resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, maxConfigBodyBytes))
	if err != nil {
		return nil, errors.Wrapf(
			ctx,
			err,
			"fetch .maintainer.yaml: read body (cap=%d bytes)",
			maxConfigBodyBytes,
		)
	}
	return body, nil
}

func (f *httpFetcher) checkStatus(
	ctx context.Context,
	resp *http.Response,
	body []byte,
	owner, repo, ref string,
) error {
	if resp.StatusCode == http.StatusNotFound {
		glog.V(2).Infof(
			"fetch .maintainer.yaml: GET %s/%s@%s status=404 (absent)",
			owner, repo, ref,
		)
		return ErrFileNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Body is redacted: a 5xx body from GitHub / a proxy can contain
		// internal paths, header echoes, or partial stack traces. Surface
		// the status code (operator-actionable) and a short SHA-256
		// fingerprint (so two reports of the same body can be correlated
		// without exposing the bytes).
		sum := sha256.Sum256(body)
		fingerprint := hex.EncodeToString(sum[:])[:8]
		return errors.Errorf(
			ctx,
			"fetch .maintainer.yaml: status %d body_sha256_prefix=%s body_bytes=%d",
			resp.StatusCode,
			fingerprint,
			len(body),
		)
	}
	glog.V(2).Infof(
		"fetch .maintainer.yaml: GET %s/%s@%s status=%d bytes=%d",
		owner, repo, ref, resp.StatusCode, len(body),
	)
	return nil
}

func (f *httpFetcher) decodeContent(ctx context.Context, body []byte) ([]byte, error) {
	var cr contentResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch .maintainer.yaml: decode json")
	}
	if cr.Encoding != "base64" {
		return nil, errors.Errorf(
			ctx,
			"fetch .maintainer.yaml: unsupported encoding %q (want base64)",
			cr.Encoding,
		)
	}
	// GitHub embeds literal newlines inside the base64 string; strip them.
	cleaned := strings.NewReplacer("\n", "", "\r", "").Replace(cr.Content)
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch .maintainer.yaml: base64 decode")
	}
	return decoded, nil
}

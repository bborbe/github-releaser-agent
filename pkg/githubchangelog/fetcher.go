// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package githubchangelog fetches CHANGELOG.md byte content from a target
// GitHub repository at a specific ref via the REST contents API. It is the
// only network boundary the planning step crosses; kept narrow and
// mockable.
//
// Auth model: a bearer token is supplied at construction time (GitHub App
// installation access token or PAT). Empty token means anonymous (60/hr
// rate limit — sufficient for tests, never for production).
package githubchangelog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../../mocks/fetcher.go --fake-name Fetcher . Fetcher

// Fetcher reads CHANGELOG.md bytes from a remote GitHub repo at a ref.
// Implementations MUST be safe for concurrent use. Returned bytes are the
// raw decoded file contents (no base64, no JSON wrapper).
type Fetcher interface {
	Fetch(ctx context.Context, owner, repo, ref string) ([]byte, error)
}

// NewHTTPFetcher constructs a Fetcher backed by net/http against
// api.github.com. token is the bearer token (GitHub App IAT or PAT);
// empty token sends no Authorization header. The internal http.Client
// has a 15-second timeout — operations that exceed this return a wrapped
// context-deadline-exceeded error.
func NewHTTPFetcher(token string) Fetcher {
	return &httpFetcher{
		client:  &http.Client{Timeout: 15 * time.Second},
		token:   token,
		apiBase: "https://api.github.com",
	}
}

// newHTTPFetcherWithBase is an internal constructor used by tests to
// point the fetcher at a test server. Not exported.
func newHTTPFetcherWithBase(token, apiBase string) Fetcher {
	return &httpFetcher{
		client:  &http.Client{Timeout: 15 * time.Second},
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

// Fetch implements Fetcher. Returns wrapped errors on:
//   - empty owner/repo/ref (caller bug; returns "fetch CHANGELOG.md: owner empty" etc.)
//   - request construction failure
//   - HTTP transport failure (timeout, DNS, connection reset)
//   - non-2xx response (returns "fetch CHANGELOG.md: status %d: %s")
//   - JSON decode failure
//   - unsupported encoding (returns "fetch CHANGELOG.md: unsupported encoding %q")
//   - base64 decode failure
func (f *httpFetcher) Fetch(ctx context.Context, owner, repo, ref string) ([]byte, error) {
	if owner == "" {
		return nil, errors.Errorf(ctx, "fetch CHANGELOG.md: owner empty")
	}
	if repo == "" {
		return nil, errors.Errorf(ctx, "fetch CHANGELOG.md: repo empty")
	}
	if ref == "" {
		return nil, errors.Errorf(ctx, "fetch CHANGELOG.md: ref empty")
	}

	// owner/repo are path segments, ref is a query value — escape each so a
	// crafted owner/repo/ref cannot corrupt the URL or inject query params.
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/contents/CHANGELOG.md?ref=%s",
		f.apiBase, url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(ref),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch CHANGELOG.md: build request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch CHANGELOG.md: http %s/%s@%s", owner, repo, ref)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch CHANGELOG.md: read body")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Truncate body for log safety.
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, errors.Errorf(
			ctx,
			"fetch CHANGELOG.md: status %d: %s",
			resp.StatusCode,
			preview,
		)
	}

	glog.V(2).
		Infof("fetch CHANGELOG.md: GET %s/%s@%s status=%d bytes=%d", owner, repo, ref, resp.StatusCode, len(body))

	var cr contentResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch CHANGELOG.md: decode json")
	}
	if cr.Encoding != "base64" {
		return nil, errors.Errorf(
			ctx,
			"fetch CHANGELOG.md: unsupported encoding %q (want base64)",
			cr.Encoding,
		)
	}
	// GitHub embeds literal newlines inside the base64 string; strip them.
	cleaned := strings.NewReplacer("\n", "", "\r", "").Replace(cr.Content)
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "fetch CHANGELOG.md: base64 decode")
	}
	return decoded, nil
}

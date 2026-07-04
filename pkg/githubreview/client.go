// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package githubreview

import (
	"context"
	"encoding/base64"
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
)

// ErrTagNotFound is returned by Client.TagExists on a 404 response.
// Callers use errors.Is(err, ErrTagNotFound) to distinguish 404 (verification
// failure → write ## Review approved:false, return Status:Failed) from
// 5xx / transport errors (wrap and return; controller retries).
var ErrTagNotFound = stderrors.New("githubreview: tag not found")

// errBodyPreviewMax matches githubchangelog (200 bytes) — keeps non-2xx
// response bodies short in error strings to avoid log spam.
const errBodyPreviewMax = 200

//counterfeiter:generate -o ../../mocks/review_client.go --fake-name ReviewClient . Client

// Client is the seam for the three GitHub REST API calls needed by the
// ai_review step. Mock it in tests with a counterfeiter-generated mock.
type Client interface {
	// TagExists calls GET /repos/{owner}/{repo}/git/ref/tags/{tag} and
	// returns (tagSHA, nil) on 200, or ("", ErrTagNotFound) on 404,
	// or ("", wrapped error) on transport / other non-2xx.
	TagExists(ctx context.Context, owner, repo, tag string) (tagSHA string, _ error)

	// ResolveTagCommit calls GET /repos/{owner}/{repo}/git/tags/{sha} and
	// follows annotated tags to their underlying commit SHA. Returns the
	// commit SHA or a wrapped error.
	ResolveTagCommit(ctx context.Context, owner, repo, tagSHA string) (commitSHA string, _ error)

	// FetchChangelog calls GET /repos/{owner}/{repo}/contents/CHANGELOG.md
	// (no ?ref= — relies on API defaulting to the repo's default branch).
	// Returns base64-decoded file bytes or a wrapped error.
	FetchChangelog(ctx context.Context, owner, repo string) ([]byte, error)
}

// NewHTTPClient returns the production client against api.github.com.
func NewHTTPClient(token string) Client {
	return newHTTPClientWithBase(token, "https://api.github.com")
}

// newHTTPClientWithBase is the test seam — package-private so tests via
// export_test.go can point the client at an httptest.Server. Returns the
// Client interface (not *httpClient) so the concrete type stays private
// and future interface changes are a one-step edit. Mirrors
// githubchangelog.newHTTPFetcherWithBase pattern.
func newHTTPClientWithBase(token, apiBase string) Client {
	return &httpClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		token:   token,
		apiBase: apiBase,
	}
}

type httpClient struct {
	client  *http.Client
	token   string
	apiBase string
}

type refResponse struct {
	Ref    string    `json:"ref"`
	Object refObject `json:"object"`
}

type refObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type tagResponse struct {
	SHA    string    `json:"sha"`
	Object tagObject `json:"object"`
}

type tagObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type contentResponse struct {
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// TagExists calls GET /repos/{owner}/{repo}/git/ref/tags/{tag}.
func (c *httpClient) TagExists(ctx context.Context, owner, repo, tag string) (string, error) {
	if owner == "" || repo == "" || tag == "" {
		return "", errors.Errorf(ctx, "TagExists: owner/repo/tag must be non-empty")
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/git/ref/tags/%s",
		c.apiBase, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "TagExists: build request")
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "TagExists: http")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "TagExists: read body")
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrTagNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := string(body)
		if len(preview) > errBodyPreviewMax {
			preview = preview[:errBodyPreviewMax]
		}
		return "", errors.Errorf(ctx, "TagExists: status %d: %s", resp.StatusCode, preview)
	}

	glog.V(2).Infof("TagExists: GET %s status=%d bytes=%d", endpoint, resp.StatusCode, len(body))

	var refResp refResponse
	if err := json.Unmarshal(body, &refResp); err != nil {
		return "", errors.Wrapf(ctx, err, "TagExists: decode json")
	}
	return refResp.Object.SHA, nil
}

// ResolveTagCommit calls GET /repos/{owner}/{repo}/git/tags/{sha} and
// follows annotated tags to their underlying commit SHA.
func (c *httpClient) ResolveTagCommit(
	ctx context.Context,
	owner, repo, tagSHA string,
) (string, error) {
	if owner == "" || repo == "" || tagSHA == "" {
		return "", errors.Errorf(ctx, "ResolveTagCommit: owner/repo/tagSHA must be non-empty")
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/git/tags/%s",
		c.apiBase, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tagSHA),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "ResolveTagCommit: build request")
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "ResolveTagCommit: http")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "ResolveTagCommit: read body")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := string(body)
		if len(preview) > errBodyPreviewMax {
			preview = preview[:errBodyPreviewMax]
		}
		return "", errors.Errorf(ctx, "ResolveTagCommit: status %d: %s", resp.StatusCode, preview)
	}

	glog.V(2).
		Infof("ResolveTagCommit: GET %s status=%d bytes=%d", endpoint, resp.StatusCode, len(body))

	var tagResp tagResponse
	if err := json.Unmarshal(body, &tagResp); err != nil {
		return "", errors.Wrapf(ctx, err, "ResolveTagCommit: decode json")
	}

	// Object.Type == "commit": annotated tag wraps a commit (normal case for
	// github-releaser, which creates non-chained annotated tags via `git tag -a`).
	// Object.Type == "tag": chained annotated tag (tag-of-tag) — theoretically
	// valid in Git but not produced by this release path. Returning Object.SHA
	// in that case would silently hand back another tag SHA mislabeled as a
	// commit SHA, so reject explicitly rather than recurse.
	switch tagResp.Object.Type {
	case "commit":
		return tagResp.Object.SHA, nil
	case "tag":
		return "", errors.Errorf(
			ctx,
			"ResolveTagCommit: chained annotated tag (tag-of-tag) not supported for tag %s",
			tagSHA,
		)
	default:
		return "", errors.Errorf(
			ctx,
			"ResolveTagCommit: unknown tag object type %q for tag %s",
			tagResp.Object.Type,
			tagSHA,
		)
	}
}

// FetchChangelog calls GET /repos/{owner}/{repo}/contents/CHANGELOG.md
// (no ?ref= — relies on API defaulting to the repo's default branch).
func (c *httpClient) FetchChangelog(ctx context.Context, owner, repo string) ([]byte, error) {
	if owner == "" || repo == "" {
		return nil, errors.Errorf(ctx, "FetchChangelog: owner/repo must be non-empty")
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/contents/CHANGELOG.md",
		c.apiBase, url.PathEscape(owner), url.PathEscape(repo),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "FetchChangelog: build request")
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "FetchChangelog: http")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "FetchChangelog: read body")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := string(body)
		if len(preview) > errBodyPreviewMax {
			preview = preview[:errBodyPreviewMax]
		}
		return nil, errors.Errorf(ctx, "FetchChangelog: status %d: %s", resp.StatusCode, preview)
	}

	glog.V(2).
		Infof("FetchChangelog: GET %s status=%d bytes=%d", endpoint, resp.StatusCode, len(body))

	var cr contentResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, errors.Wrapf(ctx, err, "FetchChangelog: decode json")
	}
	if cr.Encoding != "base64" {
		return nil, errors.Errorf(
			ctx,
			"FetchChangelog: unsupported encoding %q (want base64)",
			cr.Encoding,
		)
	}
	cleaned := strings.NewReplacer("\n", "", "\r", "").Replace(cr.Content)
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "FetchChangelog: base64 decode")
	}
	return decoded, nil
}

func (c *httpClient) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

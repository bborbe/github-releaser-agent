// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package githubpullrequest is the narrow GitHub REST client used by the
// protected-branch fallback: resolve the default branch, find an open pull
// request by head branch, and create one pull request.
package githubpullrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// errBodyPreviewMax keeps non-2xx response bodies short in error strings to
// avoid log spam. Mirrors githubreview.
const errBodyPreviewMax = 200

//counterfeiter:generate -o ../../mocks/pull_request_client.go --fake-name PullRequestClient . Client

// Client is the seam for the three GitHub REST API calls the protected-branch
// fallback needs. It is the only non-git write path in this agent.
type Client interface {
	// DefaultBranch returns the repository's default branch name.
	DefaultBranch(ctx context.Context, owner, repo string) (branch string, _ error)

	// FindOpenPullRequest returns the html_url of the open pull request whose
	// head branch is headBranch, and found=false when none exists.
	FindOpenPullRequest(ctx context.Context, owner, repo, headBranch string) (
		pullRequestURL string, found bool, _ error,
	)

	// CreatePullRequest opens one pull request and returns its html_url.
	CreatePullRequest(
		ctx context.Context,
		owner, repo, title, headBranch, baseBranch, body string,
	) (pullRequestURL string, _ error)
}

// NewHTTPClient returns the production client against api.github.com.
func NewHTTPClient(token string) Client {
	return newHTTPClientWithBase(token, "https://api.github.com")
}

// newHTTPClientWithBase is the test seam — package-private so tests via
// export_test.go can point the client at an httptest.Server. Returns the
// Client interface (not *httpClient) so the concrete type stays private.
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

type repoResponse struct {
	DefaultBranch string `json:"default_branch"`
}

type pullRequestResponse struct {
	HTMLURL string `json:"html_url"`
}

type createPullRequestRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// DefaultBranch calls GET /repos/{owner}/{repo} and returns default_branch.
func (c *httpClient) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	if owner == "" || repo == "" {
		return "", errors.Errorf(ctx, "DefaultBranch: owner/repo must be non-empty")
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s",
		c.apiBase, url.PathEscape(owner), url.PathEscape(repo),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "DefaultBranch: build request")
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "DefaultBranch: http")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "DefaultBranch: read body")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.Errorf(
			ctx,
			"DefaultBranch: status %d: %s",
			resp.StatusCode,
			bodyPreview(body),
		)
	}

	glog.V(2).
		Infof("DefaultBranch: GET %s status=%d bytes=%d", endpoint, resp.StatusCode, len(body))

	var repoResp repoResponse
	if err := json.Unmarshal(body, &repoResp); err != nil {
		return "", errors.Wrapf(ctx, err, "DefaultBranch: decode json")
	}
	if repoResp.DefaultBranch == "" {
		return "", errors.Errorf(ctx, "DefaultBranch: empty default_branch for %s/%s", owner, repo)
	}
	return repoResp.DefaultBranch, nil
}

// FindOpenPullRequest calls GET /repos/{owner}/{repo}/pulls?state=open&head=...
// and returns the first matching pull request's html_url. An empty result is
// not an error: it returns ("", false, nil).
func (c *httpClient) FindOpenPullRequest(
	ctx context.Context,
	owner, repo, headBranch string,
) (string, bool, error) {
	if owner == "" || repo == "" || headBranch == "" {
		return "", false, errors.Errorf(
			ctx,
			"FindOpenPullRequest: owner/repo/headBranch must be non-empty",
		)
	}

	query := url.Values{}
	query.Set("state", "open")
	query.Set("head", owner+":"+headBranch)
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/pulls?%s",
		c.apiBase, url.PathEscape(owner), url.PathEscape(repo), query.Encode(),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, errors.Wrapf(ctx, err, "FindOpenPullRequest: build request")
	}
	c.setHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", false, errors.Wrapf(ctx, err, "FindOpenPullRequest: http")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, errors.Wrapf(ctx, err, "FindOpenPullRequest: read body")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, errors.Errorf(
			ctx,
			"FindOpenPullRequest: status %d: %s",
			resp.StatusCode,
			bodyPreview(body),
		)
	}

	glog.V(2).Infof(
		"FindOpenPullRequest: GET %s status=%d bytes=%d",
		endpoint, resp.StatusCode, len(body),
	)

	var pullRequests []pullRequestResponse
	if err := json.Unmarshal(body, &pullRequests); err != nil {
		return "", false, errors.Wrapf(ctx, err, "FindOpenPullRequest: decode json")
	}
	if len(pullRequests) == 0 {
		return "", false, nil
	}
	return pullRequests[0].HTMLURL, true, nil
}

// CreatePullRequest calls POST /repos/{owner}/{repo}/pulls and returns the
// created pull request's html_url.
func (c *httpClient) CreatePullRequest(
	ctx context.Context,
	owner, repo, title, headBranch, baseBranch, body string,
) (string, error) {
	if owner == "" || repo == "" || title == "" || headBranch == "" || baseBranch == "" {
		return "", errors.Errorf(
			ctx,
			"CreatePullRequest: owner/repo/title/head/base must be non-empty",
		)
	}

	payload, err := json.Marshal(createPullRequestRequest{
		Title: title,
		Head:  headBranch,
		Base:  baseBranch,
		Body:  body,
	})
	if err != nil {
		return "", errors.Wrapf(ctx, err, "CreatePullRequest: marshal json")
	}
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/pulls",
		c.apiBase, url.PathEscape(owner), url.PathEscape(repo),
	)
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "CreatePullRequest: build request")
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "CreatePullRequest: http")
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "CreatePullRequest: read body")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.Errorf(
			ctx,
			"CreatePullRequest: status %d: %s",
			resp.StatusCode,
			bodyPreview(respBody),
		)
	}

	glog.V(2).Infof(
		"CreatePullRequest: POST %s status=%d bytes=%d",
		endpoint, resp.StatusCode, len(respBody),
	)

	var prResp pullRequestResponse
	if err := json.Unmarshal(respBody, &prResp); err != nil {
		return "", errors.Wrapf(ctx, err, "CreatePullRequest: decode json")
	}
	if prResp.HTMLURL == "" {
		return "", errors.Errorf(ctx, "CreatePullRequest: empty html_url for %s/%s", owner, repo)
	}
	return prResp.HTMLURL, nil
}

// bodyPreview truncates a non-2xx response body to errBodyPreviewMax bytes.
func bodyPreview(body []byte) string {
	preview := string(body)
	if len(preview) > errBodyPreviewMax {
		preview = preview[:errBodyPreviewMax]
	}
	return preview
}

func (c *httpClient) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

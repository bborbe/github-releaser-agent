// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package githubtags fetches the tag list from a target GitHub repository
// via the REST tags API and returns the highest-semver tag. It is the
// only network boundary the planning step crosses for tag resolution;
// kept narrow and mockable.
//
// Auth model: a bearer token is supplied at construction time (GitHub App
// installation access token or PAT). Empty token means anonymous (60/hr
// rate limit — sufficient for tests, never for production).
package githubtags

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-releaser-agent/pkg/semver"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../../mocks/tags_fetcher.go --fake-name TagsFetcher . TagsFetcher

// TagsFetcher resolves a remote GitHub repo's highest-semver tag.
// Implementations MUST be safe for concurrent use.
//
// LatestSemverTag returns the highest-semver tag string (original
// spelling, e.g. "v0.101.1"). When the repo has zero tags OR none of
// its tags are valid semver, it returns ErrNoTags so the caller can
// fall back to the emit-time snapshot cleanly. All other failures
// (transport, non-2xx, decode) return a wrapped error.
type TagsFetcher interface {
	LatestSemverTag(ctx context.Context, owner, repo string) (string, error)
}

// ErrNoTags signals the repo has no usable semver tag (empty tag list,
// or every tag name is non-semver). Callers use errors.Is(err, ErrNoTags)
// to fall back to the frontmatter snapshot with no warning (spec 001
// no-tags branch). Mirrors pkg/maintainerconfig.ErrFileNotFound and
// pkg/githubreview.ErrTagNotFound (project sentinel convention).
var ErrNoTags = stderrors.New("githubtags: no usable semver tag on remote")

// NewHTTPTagsFetcher constructs a TagsFetcher backed by net/http against
// api.github.com. token is the bearer token (GitHub App IAT or PAT);
// empty token sends no Authorization header. The internal http.Client
// has a 15-second timeout — operations that exceed this return a wrapped
// context-deadline-exceeded error.
func NewHTTPTagsFetcher(token string) TagsFetcher {
	return &httpTagsFetcher{
		client:  &http.Client{Timeout: 15 * time.Second},
		token:   token,
		apiBase: "https://api.github.com",
	}
}

// newHTTPTagsFetcherWithBase is an internal constructor used by tests to
// point the fetcher at a test server. Not exported.
func newHTTPTagsFetcherWithBase(token, apiBase string) TagsFetcher {
	return &httpTagsFetcher{
		client:  &http.Client{Timeout: 15 * time.Second},
		token:   token,
		apiBase: apiBase,
	}
}

type httpTagsFetcher struct {
	client  *http.Client
	token   string
	apiBase string
}

type tagResponse struct {
	Name string `json:"name"`
}

// LatestSemverTag implements TagsFetcher. It paginates through all tag
// pages (GitHub returns tags in refname order, NOT semver order) and
// returns the highest-semver tag across all pages. Returns ErrNoTags
// when no tag in the full list is a valid semver. Returns wrapped errors
// on: empty owner/repo, transport failure, non-2xx response (including
// 404 for absent repos), and JSON decode failure.
func (f *httpTagsFetcher) LatestSemverTag(ctx context.Context, owner, repo string) (string, error) {
	if owner == "" {
		return "", errors.Errorf(ctx, "list tags: owner empty")
	}
	if repo == "" {
		return "", errors.Errorf(ctx, "list tags: repo empty")
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/tags?per_page=100",
		f.apiBase, url.PathEscape(owner), url.PathEscape(repo),
	)

	names := []string{}
	iterations := 0
	for pageURL := endpoint; pageURL != ""; {
		iterations++
		if iterations > 100 {
			return "", errors.Errorf(ctx, "list tags: too many pages")
		}
		pageNames, next, err := f.fetchPage(ctx, pageURL)
		if err != nil {
			return "", err
		}
		names = append(names, pageNames...)
		pageURL = next
	}

	latest, ok := semver.Highest(names)
	if !ok {
		glog.V(2).Infof("list tags: %s/%s no usable semver tag (%d tags)", owner, repo, len(names))
		return "", ErrNoTags
	}
	glog.V(2).Infof("list tags: %s/%s highest=%s (of %d tags)", owner, repo, latest, len(names))
	return latest, nil
}

// fetchPage performs one HTTP GET for a single page of tags. It returns
// the tag names from that page and the next-page URL (from the RFC 5988
// Link header), or "" when no "rel=next" link is present.
func (f *httpTagsFetcher) fetchPage(
	ctx context.Context,
	pageURL string,
) (names []string, nextURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "list tags: build request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "list tags: http %s", pageURL)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "list tags: read body")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, "", errors.Errorf(
			ctx,
			"list tags: status %d: %s",
			resp.StatusCode,
			preview,
		)
	}

	linkHdr := resp.Header.Get("Link")
	next := nextLink(linkHdr)
	glog.V(2).Infof("list tags: GET %s status=%d bytes=%d", pageURL, resp.StatusCode, len(body))
	glog.V(3).Infof("list tags: Link header: %q nextLink=%q", linkHdr, next)

	var tags []tagResponse
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, "", errors.Wrapf(ctx, err, "list tags: decode json")
	}
	for _, tag := range tags {
		names = append(names, tag.Name)
	}

	nextURL = nextLink(resp.Header.Get("Link"))
	return names, nextURL, nil
}

// nextLink parses an RFC 5988 Link header and returns the URL of the
// "rel=next" link, or "" if not present. It performs pure string
// parsing with no I/O.
func nextLink(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	// Link header format: <url>; rel="next", <url>; rel="last", ...
	parts := strings.Split(linkHeader, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		// Each part is "<url>; rel=\"<relValue>\""
		idxURL := strings.Index(part, "<")
		idxRel := strings.Index(part, "rel=\"")
		if idxURL == -1 || idxRel == -1 {
			continue
		}
		urlStart := idxURL + 1
		urlEnd := strings.Index(part, ">")
		if urlEnd == -1 || urlEnd <= urlStart {
			continue
		}
		relStart := idxRel + len("rel=\"")
		relEnd := strings.Index(part[relStart:], "\"")
		if relEnd == -1 {
			continue
		}
		relValue := part[relStart : relStart+relEnd]
		if relValue == "next" {
			return part[urlStart:urlEnd]
		}
	}
	return ""
}

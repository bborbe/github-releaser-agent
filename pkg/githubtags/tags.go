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
type TagsFetcher interface {
	LatestSemverTag(ctx context.Context, owner, repo string) (string, error)

	// CommitSHAForTag returns the COMMIT SHA the named tag points at.
	// GitHub's tags-list endpoint reports commit.sha already dereferenced
	// for annotated tags, so the returned SHA is always a commit, never a
	// tag object (spec 002 § Assumptions).
	//
	// Returns ErrTagNotFound when the listing succeeds but contains no
	// entry whose name equals tag exactly (case-sensitive, no v-prefix
	// normalisation). All other failures (empty args, transport, non-2xx,
	// decode, entry with an empty commit sha) return a wrapped error.
	// Costs one tags-list pagination — never one request per tag.
	CommitSHAForTag(ctx context.Context, owner, repo, tag string) (string, error)

	// CompareCommits returns GitHub's comparison of base...head for
	// owner/repo. Costs one compare call — no pagination, no per-commit
	// request. Returns a wrapped error on empty args, transport failure,
	// non-2xx (including 404 for an unknown ref), and decode failure.
	// BehindBy is nil when the payload does not carry the key.
	CompareCommits(ctx context.Context, owner, repo, base, head string) (CompareResult, error)
}

// CompareResult is the subset of GitHub's compare payload the planning step
// needs. BehindBy is the number of commits HEAD lacks that BASE has, and is
// the only field that decides containment — AheadBy is decoded so the
// decision can be logged with both directions visible, never for control
// flow.
//
// BehindBy is a POINTER so a payload that omits or renames the key is
// distinguishable from a genuine 0. An absent key must never read as
// "contained": that would close a release task with unreleased work on a
// shape change, which is the exact class of defect the containment guard
// exists to remove (spec 006 § Failure Modes row 6).
type CompareResult struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy *int   `json:"behind_by"`
}

// ErrNoTags signals the repo has no usable semver tag (empty tag list,
// or every tag name is non-semver). Callers use errors.Is(err, ErrNoTags)
// to fall back to the frontmatter snapshot with no warning (spec 001
// no-tags branch). Mirrors pkg/maintainerconfig.ErrFileNotFound and
// pkg/githubreview.ErrTagNotFound (project sentinel convention).
var ErrNoTags = stderrors.New("githubtags: no usable semver tag on remote")

// ErrTagNotFound signals that the named tag is absent from the remote's
// tag list (a successful 2xx listing that contains no entry with that
// exact name). Callers use errors.Is(err, ErrTagNotFound) to distinguish
// "tag genuinely does not exist" from a transport/decode failure — spec
// 002 escalates on BOTH, but only the absent case is expected traffic.
// Mirrors pkg/githubreview.ErrTagNotFound and pkg/maintainerconfig.ErrFileNotFound
// (project sentinel convention).
var ErrTagNotFound = stderrors.New("githubtags: tag not found on remote")

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

// tagResponse is one entry of the GitHub tags-list payload. Commit.SHA is
// the COMMIT the tag points at — GitHub dereferences annotated tags on this
// endpoint, so no second request against the tag object is needed
// (spec 002 § Assumptions).
type tagResponse struct {
	Name   string        `json:"name"`
	Commit tagCommitInfo `json:"commit"`
}

// tagCommitInfo is the nested commit object of a tags-list entry.
type tagCommitInfo struct {
	SHA string `json:"sha"`
}

// collectTags paginates the full tags list for owner/repo and returns every
// entry across all pages. GitHub returns tags in refname order (NOT semver
// order) capped at 100 per page, so both lookups must read every page.
// It is the single pagination site for this package — one tags-list
// pagination per call, never one request per tag.
func (f *httpTagsFetcher) collectTags(
	ctx context.Context,
	owner, repo string,
) ([]tagResponse, error) {
	if owner == "" {
		return nil, errors.Errorf(ctx, "list tags: owner empty")
	}
	if repo == "" {
		return nil, errors.Errorf(ctx, "list tags: repo empty")
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/tags?per_page=100",
		f.apiBase, url.PathEscape(owner), url.PathEscape(repo),
	)

	tags := []tagResponse{}
	iterations := 0
	for pageURL := endpoint; pageURL != ""; {
		iterations++
		if iterations > 100 {
			return nil, errors.Errorf(ctx, "list tags: too many pages")
		}
		pageTags, next, err := f.fetchPage(ctx, pageURL)
		if err != nil {
			return nil, err
		}
		tags = append(tags, pageTags...)
		pageURL = next
	}

	return tags, nil
}

// LatestSemverTag implements TagsFetcher. It paginates through all tag
// pages (GitHub returns tags in refname order, NOT semver order) and
// returns the highest-semver tag across all pages. Returns ErrNoTags
// when no tag in the full list is a valid semver. Returns wrapped errors
// on: empty owner/repo, transport failure, non-2xx response (including
// 404 for absent repos), and JSON decode failure.
func (f *httpTagsFetcher) LatestSemverTag(ctx context.Context, owner, repo string) (string, error) {
	tags, err := f.collectTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	latest, ok := semver.Highest(names)
	if !ok {
		glog.V(2).Infof("list tags: %s/%s no usable semver tag (%d tags)", owner, repo, len(names))
		return "", ErrNoTags
	}
	glog.V(2).Infof("list tags: %s/%s highest=%s (of %d tags)", owner, repo, latest, len(names))
	return latest, nil
}

// doGet performs one authenticated GET and returns the body and response
// headers, erroring on transport failure, a read failure, or a non-2xx
// status. It is the single request/response plumbing site for this package,
// shared by fetchPage and CompareCommits so the header set and the error
// wording exist once. op prefixes every message ("list tags", "compare
// commits") so a failure names the endpoint that produced it.
func (f *httpTagsFetcher) doGet(
	ctx context.Context,
	rawURL, op string,
) (body []byte, hdr http.Header, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, errors.Wrapf(ctx, err, "%s: build request", op)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, errors.Wrapf(ctx, err, "%s: http %s", op, rawURL)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, errors.Wrapf(ctx, err, "%s: read body", op)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, nil, errors.Errorf(
			ctx,
			"%s: status %d: %s",
			op,
			resp.StatusCode,
			preview,
		)
	}

	glog.V(2).Infof("%s: GET %s status=%d bytes=%d", op, rawURL, resp.StatusCode, len(body))
	return body, resp.Header, nil
}

// fetchPage performs one HTTP GET for a single page of tags. It returns
// the tag entries from that page and the next-page URL (from the RFC 5988
// Link header), or "" when no "rel=next" link is present.
func (f *httpTagsFetcher) fetchPage(
	ctx context.Context,
	pageURL string,
) (tags []tagResponse, nextURL string, err error) {
	body, hdr, err := f.doGet(ctx, pageURL, "list tags")
	if err != nil {
		return nil, "", err
	}

	linkHdr := hdr.Get("Link")
	next := nextLink(linkHdr)
	glog.V(3).Infof("list tags: Link header: %q nextLink=%q", linkHdr, next)

	var tagEntries []tagResponse
	if err := json.Unmarshal(body, &tagEntries); err != nil {
		return nil, "", errors.Wrapf(ctx, err, "list tags: decode json")
	}

	return tagEntries, next, nil
}

// CompareCommits implements TagsFetcher. base is the task's ref and head is
// the tag's commit; GitHub's compare(base...head) therefore reports
// behind_by = commits head lacks that base has, so behind_by == 0 means the
// tag already contains the ref (spec 006 § Desired Behavior 2).
func (f *httpTagsFetcher) CompareCommits(
	ctx context.Context,
	owner, repo, base, head string,
) (CompareResult, error) {
	if owner == "" {
		return CompareResult{}, errors.Errorf(ctx, "compare commits: owner empty")
	}
	if repo == "" {
		return CompareResult{}, errors.Errorf(ctx, "compare commits: repo empty")
	}
	if base == "" {
		return CompareResult{}, errors.Errorf(ctx, "compare commits: base empty")
	}
	if head == "" {
		return CompareResult{}, errors.Errorf(ctx, "compare commits: head empty")
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/compare/%s...%s",
		f.apiBase,
		url.PathEscape(owner),
		url.PathEscape(repo),
		url.PathEscape(base),
		url.PathEscape(head),
	)

	body, _, err := f.doGet(ctx, endpoint, "compare commits")
	if err != nil {
		return CompareResult{}, err
	}

	var result CompareResult
	if err := json.Unmarshal(body, &result); err != nil {
		return CompareResult{}, errors.Wrapf(ctx, err, "compare commits: decode json")
	}

	behind := "absent"
	if result.BehindBy != nil {
		behind = fmt.Sprintf("%d", *result.BehindBy)
	}
	glog.V(2).Infof(
		"compare commits: %s/%s base=%s head=%s status=%s ahead=%d behind=%s",
		owner, repo, base, head, result.Status, result.AheadBy, behind,
	)
	return result, nil
}

// CommitSHAForTag implements TagsFetcher.
func (f *httpTagsFetcher) CommitSHAForTag(
	ctx context.Context,
	owner, repo, tag string,
) (string, error) {
	if tag == "" {
		return "", errors.Errorf(ctx, "commit sha for tag: tag empty")
	}
	tags, err := f.collectTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	for _, entry := range tags {
		if entry.Name != tag {
			continue
		}
		if entry.Commit.SHA == "" {
			return "", errors.Errorf(ctx, "commit sha for tag: %s has empty commit sha", tag)
		}
		glog.V(2).Infof("list tags: %s/%s tag=%s commit=%s", owner, repo, tag, entry.Commit.SHA)
		return entry.Commit.SHA, nil
	}
	glog.V(2).Infof("list tags: %s/%s tag=%s not found (%d tags)", owner, repo, tag, len(tags))
	return "", ErrTagNotFound
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

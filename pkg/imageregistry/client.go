// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package imageregistry probes a container registry for the existence of an
// image manifest at a given tag. It is used by the ai_review step to record
// whether a release produced a deployable artifact.
package imageregistry

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// manifestAcceptHeader names the manifest media types the registry probe
// accepts. The order mirrors the registry spec's recommendation: index/list
// first (multi-arch), then the single-image manifest types.
const manifestAcceptHeader = "application/vnd.docker.distribution.manifest.list.v2+json" +
	", " + "application/vnd.oci.image.index.v1+json" +
	", " + "application/vnd.docker.distribution.manifest.v2+json" +
	", " + "application/vnd.oci.image.manifest.v1+json"

//counterfeiter:generate -o ../../mocks/image_registry_client.go --fake-name ImageRegistryClient . Client

// Client probes a registry for a manifest at a tag. Implementations MUST be
// safe for concurrent use.
type Client interface {
	// ManifestExists returns true when the registry serves a manifest for
	// imageRef at its tag, false when it answers 404, and a wrapped error
	// for any other status, a failed token exchange, or a transport
	// failure. It never reports an unreachable registry as a missing
	// image.
	ManifestExists(ctx context.Context, imageRef string) (bool, error)
}

// NewClient returns the production client. The image-reference host is
// mapped to the registry-API host per probe.
func NewClient() Client {
	return newClientWithBase("")
}

// newClientWithBase is the test seam — package-private so tests via
// export_test.go can point the client at an httptest.Server. An empty
// baseURL means the production path: map the image-reference host to the
// registry-API host. A non-empty baseURL bypasses the mapping entirely.
func newClientWithBase(baseURL string) Client {
	return &httpClient{
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: baseURL,
	}
}

type httpClient struct {
	client  *http.Client
	baseURL string
}

type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

// ManifestExists implements Client.
func (c *httpClient) ManifestExists(ctx context.Context, imageRef string) (bool, error) {
	host, repository, tag, err := parseImageRef(ctx, imageRef)
	if err != nil {
		return false, err
	}
	endpoint, err := c.endpoint(ctx, host, repository, tag)
	if err != nil {
		return false, err
	}

	status, challenge, err := c.head(ctx, endpoint, "")
	if err != nil {
		return false, err
	}
	if status == http.StatusUnauthorized {
		token, tokenErr := c.fetchToken(ctx, challenge, repository)
		if tokenErr != nil {
			return false, tokenErr
		}
		status, _, err = c.head(ctx, endpoint, token)
		if err != nil {
			return false, err
		}
	}
	return interpretStatus(ctx, status, imageRef)
}

// endpoint builds the manifest URL. When the test seam baseURL is set the
// registry-API host mapping is bypassed; otherwise the image-reference host
// is mapped to its registry-API host.
func (c *httpClient) endpoint(
	ctx context.Context,
	host, repository, tag string,
) (string, error) {
	base := strings.TrimRight(c.baseURL, "/")
	if base == "" {
		apiHost, err := registryAPIHost(host)
		if err != nil {
			return "", errors.Wrapf(ctx, err, "image registry: resolve api host for %s", host)
		}
		base = "https://" + apiHost
	}
	return base + "/v2/" + repository + "/manifests/" + tag, nil
}

// head performs one HEAD request against endpoint and returns the status
// code plus the WWW-Authenticate challenge (empty when absent). A transport
// error is returned wrapped.
func (c *httpClient) head(
	ctx context.Context,
	endpoint, token string,
) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return 0, "", errors.Wrapf(ctx, err, "image registry: build request")
	}
	req.Header.Set("Accept", manifestAcceptHeader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, "", errors.Wrapf(ctx, err, "image registry: http %s", endpoint)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
}

// fetchToken follows the registry's Bearer challenge: it reads realm and
// service from the challenge, requests a pull-scoped token, and returns it.
func (c *httpClient) fetchToken(
	ctx context.Context,
	challenge, repository string,
) (string, error) {
	realm, service, ok := parseBearerChallenge(challenge)
	if !ok {
		return "", errors.Errorf(
			ctx,
			"image registry: 401 without a usable Bearer challenge",
		)
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "image registry: parse token realm %q", realm)
	}
	query := u.Query()
	if service != "" {
		query.Set("service", service)
	}
	query.Set("scope", "repository:"+repository+":pull")
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "image registry: build token request")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "image registry: token http")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "image registry: read token body")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.Errorf(
			ctx,
			"image registry: token status %d",
			resp.StatusCode,
		)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", errors.Wrapf(ctx, err, "image registry: decode token json")
	}
	token := tr.Token
	if token == "" {
		token = tr.AccessToken
	}
	if token == "" {
		return "", errors.Errorf(ctx, "image registry: token response carried no token")
	}
	glog.V(3).Infof("image registry: obtained pull token for repository=%s", repository)
	return token, nil
}

// interpretStatus maps a manifest HEAD status onto the Client contract.
func interpretStatus(ctx context.Context, status int, imageRef string) (bool, error) {
	switch status {
	case http.StatusOK:
		glog.V(2).Infof("image registry: manifest present image=%s", imageRef)
		return true, nil
	case http.StatusNotFound:
		glog.V(2).Infof("image registry: manifest absent image=%s", imageRef)
		return false, nil
	default:
		return false, errors.Errorf(
			ctx,
			"image registry: manifest status %d for %s",
			status,
			imageRef,
		)
	}
}

// parseImageRef splits `<host>/<repository>:<tag>`. The tag separator is
// the last ':' after the last '/', so a host that carries a port is
// tolerated. The host is the first path segment; the repository is
// everything between the first '/' and the tag separator.
func parseImageRef(
	ctx context.Context,
	imageRef string,
) (host, repository, tag string, err error) {
	firstSlash := strings.Index(imageRef, "/")
	lastSlash := strings.LastIndex(imageRef, "/")
	lastColon := strings.LastIndex(imageRef, ":")
	if firstSlash <= 0 || lastSlash <= firstSlash || lastColon <= lastSlash {
		return "", "", "", errors.Errorf(
			ctx,
			"image registry: unparseable image reference %q (want <host>/<repository>:<tag>)",
			imageRef,
		)
	}
	host = imageRef[:firstSlash]
	repository = imageRef[firstSlash+1 : lastColon]
	tag = imageRef[lastColon+1:]
	if host == "" || repository == "" || tag == "" {
		return "", "", "", errors.Errorf(
			ctx,
			"image registry: empty component in image reference %q",
			imageRef,
		)
	}
	return host, repository, tag, nil
}

// parseBearerChallenge extracts realm and service from a
// `Bearer realm="...",service="..."` header. It returns ok=false when the
// header is not a Bearer challenge or carries no realm.
func parseBearerChallenge(header string) (realm, service string, ok bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	for _, part := range strings.Split(header[len(prefix):], ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		value = strings.Trim(value, `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			realm = value
		case "service":
			service = value
		}
	}
	return realm, service, realm != ""
}

// registryAPIHost maps an image-reference host to the host serving the
// registry v2 API. Docker Hub's website host (docker.io) is not the API
// host (registry-1.docker.io); the other supported hosts are identity
// mappings. Any other host is an error — silently probing the wrong
// registry would turn an unknown destination into a false absence.
func registryAPIHost(host string) (string, error) {
	switch host {
	case "docker.io":
		return "registry-1.docker.io", nil
	case "ghcr.io":
		return "ghcr.io", nil
	case "docker.prod.nuke.benjamin-borbe.de:443":
		return "docker.prod.nuke.benjamin-borbe.de:443", nil
	default:
		return "", stderrors.New("image registry: unsupported image host " + host)
	}
}

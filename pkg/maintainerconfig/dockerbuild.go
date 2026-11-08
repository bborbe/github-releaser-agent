// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintainerconfig

import (
	"context"

	"github.com/bborbe/errors"
	"gopkg.in/yaml.v3"
)

// dockerBuildBlock is the `dockerBuild:` namespace of .maintainer.yaml. The
// shared schema pinned by this repo has no dockerBuild field and drops it on
// decode, so it is parsed here from the raw bytes. AutoBuild is a pointer so
// an absent key is distinguishable from an explicit false.
type dockerBuildBlock struct {
	AutoBuild    *bool    `yaml:"autoBuild"`
	Destinations []string `yaml:"destinations"`
}

type dockerBuildRoot struct {
	DockerBuild *dockerBuildBlock `yaml:"dockerBuild"`
}

// ParseDockerBuild reads only the `dockerBuild:` keys from the raw
// .maintainer.yaml bytes and reports whether an image is expected for a
// release.
//
//   - expected=true and imageHost set: autoBuild is true and at least one
//     destination is configured. imageHost is the image-reference host of
//     the PRIMARY (first) destination — the fleet's build arm publishes one
//     destination per Job.
//   - expected=false, destinationsEmpty=false: no dockerBuild block, or
//     autoBuild absent/false. The repo never opted in.
//   - expected=false, destinationsEmpty=true: autoBuild is true but
//     destinations is empty. This is a config fault the caller must be able
//     to tell apart from a repo that never opted in.
//
// An unknown destination class is an error: silently mapping it would turn a
// typo into a false image absence.
func ParseDockerBuild(
	ctx context.Context,
	raw []byte,
) (expected bool, imageHost string, destinationsEmpty bool, err error) {
	var root dockerBuildRoot
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return false, "", false, errors.Wrapf(ctx, err, "parse dockerBuild config")
	}
	if root.DockerBuild == nil || root.DockerBuild.AutoBuild == nil ||
		!*root.DockerBuild.AutoBuild {
		return false, "", false, nil
	}
	if len(root.DockerBuild.Destinations) == 0 {
		return false, "", true, nil
	}
	host, err := imageHostForDestination(ctx, root.DockerBuild.Destinations[0])
	if err != nil {
		return false, "", false, err
	}
	return true, host, false, nil
}

// imageHostForDestination maps a destination class to the image-reference
// host it publishes to. The registry-API host mapping lives in
// pkg/imageregistry — this function owns only the class → image-reference
// host step.
func imageHostForDestination(ctx context.Context, destination string) (string, error) {
	switch destination {
	case "dockerhub":
		return "docker.io", nil
	case "ghcr":
		return "ghcr.io", nil
	case "internal":
		return "docker.prod.nuke.benjamin-borbe.de:443", nil
	default:
		return "", errors.Errorf(ctx, "unknown dockerBuild destination class %q", destination)
	}
}

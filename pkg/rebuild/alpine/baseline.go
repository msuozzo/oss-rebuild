// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"slices"

	alpinereg "github.com/google/oss-rebuild/pkg/registry/alpine"
)

// alwaysInstall is installed in every build environment, as on every
// official builder: alpine-sdk for abuild and git, pigz because the
// builders compress with it, and alpine-base and ifupdown-ng because
// official logs install install_if companions that need openrc and
// ifupdown-ng, which the builders' image does not pull in.
var alwaysInstall = []string{"alpine-sdk", "pigz", "alpine-base", "ifupdown-ng"}

// baselines are the packages each official builder has installed apart
// from a build's own dependencies. Builders are hand-maintained and no list
// of their packages is published, so these are inferred from their logs:
// packages some build needed that its log did not install, sampled from
// each builder's recent logs and checked against its 3,000 most recent in
// September 2026. A builder not listed gets only alwaysInstall.
//
// NOTE: build-3-24-aarch64 gained 21 packages around 2026-05-08, when its
// package count moved from 103 to 124, of which libffi-dev, libxml2 and
// xz-libs are visible to builds.
var baselines = map[string][]string{
	"build-3-24-x86_64": {
		"alpine-release", "apk-tools", "binutils", "build-base", "busybox", "file", "git", "pigz", "rsync", "tar",
	},
	"build-3-24-aarch64": {
		"alpine-release", "apk-tools", "binutils", "build-base", "busybox", "file", "gcc", "git", "gmp-dev",
		"libffi-dev", "libxml2", "linux-headers", "perl", "pigz", "rsync", "tar", "xz-libs",
	},
	"build-edge-x86_64": {
		"abuild", "build-base", "busybox", "file", "git", "musl-dev", "openssl", "pkgconf", "rsync", "tar",
	},
}

// Baseline returns the packages installed on the builder of r besides a
// build's dependencies and alwaysInstall.
func Baseline(r alpinereg.Repository) []string {
	return slices.Clone(baselines[r.Builder()])
}

// repositories are the repositories an official builder of branch has
// configured.
func repositories(branch string) []string {
	if branch == "edge" {
		return []string{"main", "community", "testing"}
	}
	return []string{"main", "community"}
}

// dependencyRepos are the repositories a package of repo may depend on.
// Alpine's policy, enforced by aports CI, limits them to repo and those
// built before it: main for main, main and community for community, and all
// three for testing.
func dependencyRepos(branch, repo string) []string {
	var out []string
	for _, r := range repositories(branch) {
		out = append(out, r)
		if r == repo {
			return out
		}
	}
	return []string{repo}
}

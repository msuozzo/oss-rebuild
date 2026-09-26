// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package alpine rebuilds Alpine Linux packages.
//
// A target names one package file of one repository:
//
//	Package:  <branch>/<repo>/<arch>/<name>, e.g. v3.24/main/x86_64/zlib
//	Version:  <pkgver>-r<pkgrel>, e.g. 1.3.2-r0
//	Artifact: <name>-<version>.apk
//
// Alpine publishes no record of a build's environment and its mirrors keep
// only the newest packages. Inference reconstructs the official builder's
// environment at the build's start from a snapshot archive (see
// pkg/registry/alpine/snapshot) and the build's official log, and the build
// installs it through timewarp.
package alpine

import (
	"context"
	"io"
	"strings"

	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	alpinereg "github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/google/oss-rebuild/pkg/registry/alpine/snapshot"
	"github.com/pkg/errors"
)

// AportsRepo is the repository of Alpine's build definitions. It is
// GitHub's mirror of gitlab.alpinelinux.org/alpine/aports, whose anti-bot
// gate rejects requests from cloud IP ranges.
const AportsRepo = "https://github.com/alpinelinux/aports"

// Rebuilder rebuilds Alpine packages.
type Rebuilder struct{}

var _ rebuild.Rebuilder = Rebuilder{}

// ParsePackage splits a target's package into its repository and name.
func ParsePackage(pkg string) (alpinereg.Repository, string, error) {
	i := strings.LastIndex(pkg, "/")
	if i < 0 {
		return alpinereg.Repository{}, "", errors.Errorf("package %q is not <branch>/<repo>/<arch>/<name>", pkg)
	}
	repo, err := alpinereg.ParseRepository(pkg[:i])
	if err != nil || pkg[i+1:] == "" {
		return alpinereg.Repository{}, "", errors.Errorf("package %q is not <branch>/<repo>/<arch>/<name>", pkg)
	}
	return repo, pkg[i+1:], nil
}

// BaseImage is the image a target builds in: the official image of its
// branch, on which Alpine's builders are based.
func BaseImage(t rebuild.Target) (string, error) {
	repo, _, err := ParsePackage(t.Package)
	if err != nil {
		return "", err
	}
	if repo.Branch == "edge" {
		return "docker.io/library/alpine:edge", nil
	}
	v, ok := strings.CutPrefix(repo.Branch, "v")
	if !ok {
		return "", errors.Errorf("unknown branch %q", repo.Branch)
	}
	return "docker.io/library/alpine:" + v, nil
}

// InferRepo returns the aports repository.
func (Rebuilder) InferRepo(context.Context, rebuild.Target, rebuild.RegistryMux) (string, error) {
	return AportsRepo, nil
}

// CloneRepo does not clone aports, which is too large to clone for one
// package. The build fetches the one commit it needs.
func (Rebuilder) CloneRepo(_ context.Context, _ rebuild.Target, repo string, _ *gitx.RepositoryOptions) (rebuild.RepoConfig, error) {
	return rebuild.RepoConfig{URI: repo}, nil
}

// UsesTimewarp reports that builds install their dependencies through
// timewarp.
func (Rebuilder) UsesTimewarp(rebuild.Input) bool { return true }

// Upstream fetches the published package from the snapshot archive, which
// keeps it after the mirrors drop it.
func (Rebuilder) Upstream(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (io.ReadCloser, error) {
	repo, _, err := ParsePackage(t.Package)
	if err != nil {
		return nil, err
	}
	resp, err := mux.Alpine.Open(ctx, snapshot.PackageObject(repo, t.Artifact))
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// UpstreamURL is the published package's URL in the snapshot archive.
func (Rebuilder) UpstreamURL(_ context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	repo, _, err := ParsePackage(t.Package)
	if err != nil {
		return "", err
	}
	return mux.Alpine.URL.JoinPath(snapshot.PackageObject(repo, t.Artifact)).String(), nil
}

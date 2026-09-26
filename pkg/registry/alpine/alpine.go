// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package alpine fetches from Alpine Linux package mirrors and the official
// build log host.
//
// Alpine mirrors keep only the current version of each package: a builder
// uploads a repository's new packages and its new APKINDEX.tar.gz after each
// build pass, and superseded files are deleted. Each (branch, arch) pair has
// one official builder whose logs are kept at build.alpinelinux.org.
package alpine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/pkg/errors"
)

var (
	// CDNMirror is the mirror clients use. It serves the origin's files with
	// a cache delay and may keep serving a deleted file from cache.
	CDNMirror = urlx.MustParse("https://dl-cdn.alpinelinux.org/alpine/")
	// OriginMirror is the mirror builders upload to. It has an index the
	// moment the upload finishes.
	OriginMirror = urlx.MustParse("https://dl-master.alpinelinux.org/alpine/")
	// BuildLogHost serves the official builders' logs.
	BuildLogHost = urlx.MustParse("https://build.alpinelinux.org/buildlogs/")
)

// Repository identifies one package repository on a mirror.
type Repository struct {
	Branch string // "edge" or a release branch such as "v3.24"
	Repo   string // "main", "community" or "testing"
	Arch   string // "x86_64", "aarch64", ...
}

func (r Repository) String() string { return r.Branch + "/" + r.Repo + "/" + r.Arch }

// ParseRepository parses "<branch>/<repo>/<arch>".
func ParseRepository(s string) (Repository, error) {
	p := strings.Split(s, "/")
	if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
		return Repository{}, errors.Errorf("not branch/repo/arch: %q", s)
	}
	return Repository{Branch: p[0], Repo: p[1], Arch: p[2]}, nil
}

// IndexPath is the path of the repository's index below a mirror root.
func (r Repository) IndexPath() string { return r.String() + "/APKINDEX.tar.gz" }

// PackagePath is the path of a package file below a mirror root.
func (r Repository) PackagePath(file string) string { return r.String() + "/" + file }

// Builder is the hostname of the repository's official builder, which also
// names its log directory.
func (r Repository) Builder() string {
	if r.Branch == "edge" {
		return "build-edge-" + r.Arch
	}
	return "build-" + strings.ReplaceAll(strings.TrimPrefix(r.Branch, "v"), ".", "-") + "-" + r.Arch
}

// LogPath is the path of a build log below BuildLogHost.
func LogPath(builder, repo, origin, version string) string {
	return fmt.Sprintf("%s/%s/%s/%s-%s.log", builder, repo, origin, origin, version)
}

// StatusError is an unexpected HTTP response status.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: %d %s", e.URL, e.Code, http.StatusText(e.Code))
}

// IsNotFound reports whether err is an HTTP 404 or 410.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone)
}

// Mirror fetches from one mirror.
type Mirror struct {
	Client httpx.BasicClient
	URL    *url.URL
}

// IndexResponse is the result of a conditional index fetch.
type IndexResponse struct {
	Body         []byte // nil when NotModified
	ETag         string
	LastModified string
	NotModified  bool
}

// Index fetches a repository's APKINDEX.tar.gz. When etag or lastModified
// are set, the request is conditional and an unchanged index reports
// NotModified.
func (m Mirror) Index(ctx context.Context, r Repository, etag, lastModified string) (*IndexResponse, error) {
	u := m.URL.JoinPath(r.IndexPath()).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "fetching %s", u)
	}
	defer resp.Body.Close()
	ir := &IndexResponse{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	switch resp.StatusCode {
	case http.StatusNotModified:
		ir.NotModified = true
		return ir, nil
	case http.StatusOK:
		ir.Body, err = io.ReadAll(resp.Body)
		return ir, errors.Wrapf(err, "reading %s", u)
	}
	return nil, &StatusError{URL: u, Code: resp.StatusCode}
}

// Package opens a package file for streaming.
func (m Mirror) Package(ctx context.Context, r Repository, file string) (io.ReadCloser, error) {
	u := m.URL.JoinPath(r.PackagePath(file)).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "fetching %s", u)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &StatusError{URL: u, Code: resp.StatusCode}
	}
	return resp.Body, nil
}

// BuildLogs fetches official build logs.
type BuildLogs struct {
	Client httpx.BasicClient
	URL    *url.URL
}

// Get fetches the log of one origin build.
func (b BuildLogs) Get(ctx context.Context, builder, repo, origin, version string) ([]byte, error) {
	u := b.URL.JoinPath(LogPath(builder, repo, origin, version)).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "fetching %s", u)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{URL: u, Code: resp.StatusCode}
	}
	text, err := io.ReadAll(resp.Body)
	return text, errors.Wrapf(err, "reading %s", u)
}

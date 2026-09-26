// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/oss-rebuild/internal/gcsx"
	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/pkg/errors"
)

// DefaultBucket is the GCS bucket of the archive OSS Rebuild publishes.
const DefaultBucket = "google-rebuild-alpine-snapshots"

// Archive reads a snapshot archive over HTTP.
type Archive struct {
	Client httpx.BasicClient
	URL    *url.URL // the archive root
}

// GCSArchive is the archive held in a GCS bucket.
func GCSArchive(client httpx.BasicClient, bucket string) Archive {
	return Archive{Client: client, URL: urlx.MustParse(gcsx.HTTPURL(bucket, ""))}
}

// Bucket returns the GCS bucket holding the archive, which timewarp
// requires to serve it.
func (a Archive) Bucket() (string, error) {
	bucket, rest, _ := strings.Cut(strings.TrimPrefix(a.URL.Path, "/"), "/")
	if a.URL.Host != "storage.googleapis.com" || bucket == "" || rest != "" {
		return "", errors.Errorf("not a GCS bucket root: %s", a.URL)
	}
	return bucket, nil
}

// Open fetches an object for streaming. A missing object is an
// alpine.StatusError for which alpine.IsNotFound holds.
func (a Archive) Open(ctx context.Context, object string) (*http.Response, error) {
	u := a.URL.JoinPath(object).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "fetching %s", u)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &alpine.StatusError{URL: u, Code: resp.StatusCode}
	}
	return resp, nil
}

// Uploads fetches the upload table.
func (a Archive) Uploads(ctx context.Context) (Uploads, error) {
	resp, err := a.Open(ctx, UploadsObject)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ParseUploads(resp.Body)
}

// Read fetches a whole object.
func (a Archive) Read(ctx context.Context, object string) ([]byte, error) {
	resp, err := a.Open(ctx, object)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, errors.Wrapf(err, "reading %s", object)
}

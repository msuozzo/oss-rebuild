// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/gcsx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/google/oss-rebuild/pkg/registry/alpine/snapshot"
	"github.com/pkg/errors"
)

// handleAlpine serves an Alpine repository as it was at t from a snapshot
// archive held in a GCS bucket (see pkg/registry/alpine/snapshot). The
// bucket is the first path element, so an apk repository line reads:
//
//	http://alpine:<RFC3339>@localhost:8081/<bucket>/<branch>/<repo>
//
// and apk requests /<bucket>/<branch>/<repo>/<arch>/<file>. The index is
// the newest upload of the repository at or before t. Packages are served
// from their archived path, since apk checks each against the index.
func (h Handler) handleAlpine(rw http.ResponseWriter, r *http.Request, t *time.Time) error {
	parts := strings.Split(strings.Trim(path.Clean(r.URL.Path), "/"), "/")
	if len(parts) != 5 {
		return herror{errors.New("expected /<bucket>/<branch>/<repo>/<arch>/<file>"), http.StatusNotFound}
	}
	bucket, file := parts[0], parts[4]
	repo := alpine.Repository{Branch: parts[1], Repo: parts[2], Arch: parts[3]}
	archive := snapshot.Archive{Client: h.Client, URL: urlx.MustParse(gcsx.HTTPURL(bucket, ""))}
	var object string
	switch {
	case file == "APKINDEX.tar.gz":
		uploads, err := archive.Uploads(r.Context())
		if err != nil {
			return herror{errors.Wrap(err, "fetching uploads"), http.StatusBadGateway}
		}
		up, ok := uploads.AtOrBefore(repo, *t)
		if !ok {
			return herror{errors.Errorf("no upload of %s at or before %s", repo, t.Format(time.RFC3339)), http.StatusNotFound}
		}
		object = snapshot.BlobObject(up.SHA256)
	case strings.HasSuffix(file, ".apk"):
		object = snapshot.PackageObject(repo, file)
	default:
		return herror{errors.Errorf("unsupported file %s", file), http.StatusNotFound}
	}
	resp, err := archive.Open(r.Context(), object)
	if alpine.IsNotFound(err) {
		return herror{err, http.StatusNotFound}
	}
	if err != nil {
		return herror{err, http.StatusBadGateway}
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Content-Length", "Last-Modified"} {
		if v := resp.Header.Get(k); v != "" {
			rw.Header().Set(k, v)
		}
	}
	rw.WriteHeader(http.StatusOK)
	if _, err := io.Copy(rw, resp.Body); err != nil {
		log.Printf("error proxying %s: %v", object, err)
	}
	return nil
}

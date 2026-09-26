// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package snapshot defines the public layout of an Alpine snapshot archive.
//
// Alpine mirrors keep only the newest version of each package, so a build
// cannot be repeated against the repository state it saw. An archive keeps
// every index a mirror published (an "upload") and every package and
// official build log those indexes referenced, so that any past repository
// state can be served again. Below the archive root:
//
//	uploads.v1.jsonl                       the upload table (Uploads)
//	blobs/sha256/<hex>                     APKINDEX.tar.gz files, by SHA-256
//	apk/<branch>/<repo>/<arch>/<file>      packages, by mirror file name
//	buildlogs/<builder>/<repo>/<origin>/<origin>-<version>.log
//
// Build logs are stored gzip-compressed with Content-Encoding: gzip.
//
// Package paths reuse mirror file names, so a repository at any time T is
// the index of the newest upload at or before T plus a path rewrite for its
// packages. NOTE: Alpine has, rarely, replaced a package file without
// bumping its release. The archive keeps the first file at the package
// path and such a replacement only under blobs/sha256/.
package snapshot

import (
	"encoding/json"
	"io"
	"time"

	"github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/pkg/errors"
)

// Object names below the archive root.
const (
	UploadsObject = "uploads.v1.jsonl"
	blobPrefix    = "blobs/sha256/"
	packagePrefix = "apk/"
	logPrefix     = "buildlogs/"
)

// BlobObject is the name of a blob by its SHA-256 hex digest.
func BlobObject(sha256 string) string { return blobPrefix + sha256 }

// PackageObject is the name of a package file.
func PackageObject(r alpine.Repository, file string) string {
	return packagePrefix + r.PackagePath(file)
}

// LogObject is the name of a build log.
func LogObject(builder, repo, origin, version string) string {
	return logPrefix + alpine.LogPath(builder, repo, origin, version)
}

// Uploads is the upload table: every index the archive observed. It is
// stored as JSON Lines ordered by LastModified, so that recent uploads can
// be read from its end. A new format gets a new object name.
type Uploads []Upload

// ParseUploads parses the stored upload table.
func ParseUploads(r io.Reader) (Uploads, error) {
	var u Uploads
	for d := json.NewDecoder(r); d.More(); {
		var up Upload
		if err := d.Decode(&up); err != nil {
			return nil, errors.Wrap(err, "decoding uploads")
		}
		u = append(u, up)
	}
	return u, nil
}

// Upload is one observed index.
type Upload struct {
	Branch string `json:"branch"`
	Repo   string `json:"repo"`
	Arch   string `json:"arch"`
	// LastModified is the index's Last-Modified in Unix seconds, or the
	// observation time when the mirror sent none. It places the upload in
	// time and identifies it within its repository.
	LastModified int64  `json:"last_modified"`
	ObservedAt   int64  `json:"observed_at"`
	SHA256       string `json:"sha256"` // the index's blob, see BlobObject
	Size         int64  `json:"size"`
	NPackages    int64  `json:"n_packages"`
	// Description is the index's DESCRIPTION: the builder's `git describe`
	// of its aports checkout when it wrote the index.
	Description string `json:"description,omitempty"`
}

// Repository is the upload's repository.
func (u Upload) Repository() alpine.Repository {
	return alpine.Repository{Branch: u.Branch, Repo: u.Repo, Arch: u.Arch}
}

// Time is the upload's LastModified as a time.
func (u Upload) Time() time.Time { return time.Unix(u.LastModified, 0).UTC() }

// AtOrBefore returns the newest upload of r with LastModified at or before t.
func (u Uploads) AtOrBefore(r alpine.Repository, t time.Time) (Upload, bool) {
	var best Upload
	var found bool
	for _, up := range u {
		if up.Repository() == r && up.LastModified <= t.Unix() && (!found || up.LastModified > best.LastModified) {
			best, found = up, true
		}
	}
	return best, found
}

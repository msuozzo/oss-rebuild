// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"regexp"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
)

// AllApkStabilizers is the list of all available Alpine apk stabilizers.
var AllApkStabilizers = []Stabilizer{
	StableApkExcludeSignature,
	StableApkPkgInfoDataHash,
}

// StableApkExcludeSignature removes the signature entry (.SIGN.RSA.<key> or
// .SIGN.RSA256.<key>), made with the Alpine builder's private key.
// See https://wiki.alpinelinux.org/wiki/Apk_spec.
var StableApkExcludeSignature = Stabilizer{
	Name: "apk-exclude-signature",
}.WithConstraints(AtDepth(0)).WithFn(TarArchiveFn(func(ta *archive.TarArchive) {
	ta.Files = slices.DeleteFunc(ta.Files, func(e *archive.TarEntry) bool {
		return strings.HasPrefix(e.Name, ".SIGN.")
	})
}))

var apkDataHashRe = regexp.MustCompile(`(?m)^datahash = [0-9a-f]+\n`)

// StableApkPkgInfoDataHash removes the datahash from .PKGINFO. It is the
// SHA-256 of the compressed data member, so it varies with compression
// that stabilization otherwise discounts.
var StableApkPkgInfoDataHash = Stabilizer{
	Name: "apk-pkginfo-datahash",
}.WithConstraints(AtDepth(0)).WithFn(TarEntryFn(func(e *archive.TarEntry) {
	if e.Name == ".PKGINFO" {
		e.Body = apkDataHashRe.ReplaceAll(e.Body, nil)
		e.Size = int64(len(e.Body))
	}
}))

// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Alpine Package Identifier Encoding
//
// A target's package is "<branch>/<repo>/<arch>/<name>", e.g.
// "v3.24/community/x86_64/py3-foo". Package names use lowercase letters,
// digits and "+-._", so '/' is the only separator to encode.
//
// Filesystem/GCS Encoding:
//   - Replaces '/' with '~' (tilde)
//   - Example: "v3.24/main/x86_64/zlib" → "v3.24~main~x86_64~zlib"
//
// Firestore Encoding:
//   - Replaces '/' with '!' (exclamation mark)
//   - Example: "v3.24/main/x86_64/zlib" → "v3.24!main!x86_64!zlib"

var filesystemEncoder = &rebuild.TargetEncoder{
	Package:  rebuild.MapTransform(map[rune]rune{'/': '~'}),
	Version:  rebuild.IdentityTransform,
	Artifact: rebuild.IdentityTransform,
}

var firestoreEncoder = &rebuild.TargetEncoder{
	Package:  rebuild.MapTransform(map[rune]rune{'/': '!'}),
	Version:  rebuild.IdentityTransform,
	Artifact: rebuild.IdentityTransform,
}

func init() {
	rebuild.RegisterEncoder(rebuild.Alpine, rebuild.FilesystemTargetEncoding, filesystemEncoder)
	rebuild.RegisterEncoder(rebuild.Alpine, rebuild.FirestoreTargetEncoding, firestoreEncoder)
}

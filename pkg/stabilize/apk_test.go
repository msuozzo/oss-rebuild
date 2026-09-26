// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"bytes"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/diffr/diffrtest"
)

func TestStableApk(t *testing.T) {
	const pkginfo = "pkgname = zlib\npkgver = 1.3.2-r0\ndatahash = 0123abcd\ncommit = abc\n"
	tests := []struct {
		Name       string
		Stabilizer Stabilizer
		Input      []archive.TarEntry
		Want       []archive.TarEntry
	}{
		{
			Name:       "ExcludeSignature",
			Stabilizer: StableApkExcludeSignature,
			Input: []archive.TarEntry{
				{Header: &tar.Header{Name: ".SIGN.RSA.alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub", Typeflag: tar.TypeReg}, Body: []byte("sig")},
				{Header: &tar.Header{Name: ".PKGINFO", Typeflag: tar.TypeReg}, Body: []byte(pkginfo)},
				{Header: &tar.Header{Name: "lib/libz.so.1", Typeflag: tar.TypeReg}, Body: []byte("lib")},
			},
			Want: []archive.TarEntry{
				{Header: &tar.Header{Name: ".PKGINFO", Typeflag: tar.TypeReg}, Body: []byte(pkginfo)},
				{Header: &tar.Header{Name: "lib/libz.so.1", Typeflag: tar.TypeReg}, Body: []byte("lib")},
			},
		},
		{
			Name:       "PkgInfoDataHash",
			Stabilizer: StableApkPkgInfoDataHash,
			Input: []archive.TarEntry{
				{Header: &tar.Header{Name: ".PKGINFO", Typeflag: tar.TypeReg}, Body: []byte(pkginfo)},
				{Header: &tar.Header{Name: "usr/share/datahash", Typeflag: tar.TypeReg}, Body: []byte("datahash = 0123abcd\n")},
			},
			Want: []archive.TarEntry{
				{Header: &tar.Header{Name: ".PKGINFO", Typeflag: tar.TypeReg}, Body: []byte("pkgname = zlib\npkgver = 1.3.2-r0\ncommit = abc\n")},
				{Header: &tar.Header{Name: "usr/share/datahash", Typeflag: tar.TypeReg}, Body: []byte("datahash = 0123abcd\n")},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			input := must(archivetest.TarFile(tc.Input))
			want := must(archivetest.TarFile(tc.Want))
			var got bytes.Buffer
			orDie(StabilizeTar(
				tar.NewReader(bytes.NewReader(input.Bytes())),
				tar.NewWriter(&got),
				NewContext(archive.TarGzFormat).WithStabilizers([]Stabilizer{tc.Stabilizer}),
			))
			if diff := diffrtest.Diff(t, want.Bytes(), got.Bytes()); diff != "" {
				t.Errorf("StabilizeTar() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

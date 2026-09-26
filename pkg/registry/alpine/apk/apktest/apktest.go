// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package apktest builds apk packages and APKINDEX archives for tests.
package apktest

import (
	"archive/tar"
	"bytes"
	"fmt"
	"strings"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk"
)

// Package is a package of one data file with payload.
type Package struct {
	Name, Version string
	Bytes         []byte
	Checksum      string // APKINDEX C field
	DataHash      string
}

// NewPackage builds a package. Extra .PKGINFO lines follow the generated ones.
func NewPackage(name, version string, payload []byte, pkginfo ...string) Package {
	data := tgz("usr/share/"+name, string(payload))
	dh := apk.DataHash(data)
	info := append([]string{"pkgname = " + name, "pkgver = " + version, "datahash = " + dh}, pkginfo...)
	ctrl := tgz(".PKGINFO", strings.Join(info, "\n")+"\n")
	sig := tgz(".SIGN.RSA.test.rsa.pub", "signature")
	return Package{Name: name, Version: version, Bytes: bytes.Join([][]byte{sig, ctrl, data}, nil), Checksum: apk.ControlChecksum(ctrl), DataHash: dh}
}

// Filename is the package's mirror file name.
func (p Package) Filename() string { return apk.Filename(p.Name, p.Version) }

// Entry is one APKINDEX record.
type Entry struct {
	Package   Package
	Origin    string
	BuildDate int64
}

// Index builds an APKINDEX.tar.gz with the given DESCRIPTION.
func Index(description string, entries ...Entry) []byte {
	var text strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&text, "C:%s\nP:%s\nV:%s\no:%s\nt:%d\n\n", e.Package.Checksum, e.Package.Name, e.Package.Version, e.Origin, e.BuildDate)
	}
	return tgz("DESCRIPTION", description, "APKINDEX", text.String())
}

// tgz builds a tar.gz of alternating file names and contents.
func tgz(files ...string) []byte {
	var entries []archive.TarEntry
	for i := 0; i+1 < len(files); i += 2 {
		entries = append(entries, archive.TarEntry{Header: &tar.Header{Name: files[i], Typeflag: tar.TypeReg, Mode: 0o644}, Body: []byte(files[i+1])})
	}
	buf, err := archivetest.TgzFile(entries)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

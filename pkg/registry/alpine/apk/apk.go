// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package apk parses the formats of the Alpine Package Keeper (apk) v2:
// package files and APKINDEX archives.
//
// A v2 package is three concatenated gzip members: a signature, a control
// segment holding .PKGINFO, and the data. abuild cuts the tar end-of-archive
// blocks from the first two so that the concatenation reads as one tar
// stream. An APKINDEX entry identifies a package by the SHA-1 of its
// compressed control member, and .PKGINFO pins the data member by its
// SHA-256 (datahash). See https://wiki.alpinelinux.org/wiki/Apk_spec.
package apk

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"io"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// Record is one APKINDEX entry keyed by its single-letter field name.
// See https://wiki.alpinelinux.org/wiki/Apk_spec#APKINDEX_Format.
type Record map[string]string

// Name is the package name (P).
func (r Record) Name() string { return r["P"] }

// Version is the package version, pkgver-rpkgrel (V).
func (r Record) Version() string { return r["V"] }

// Checksum is the control member's checksum (C), "Q1" + base64(SHA-1).
func (r Record) Checksum() string { return r["C"] }

// Origin is the name of the APKBUILD the package was built from (o).
func (r Record) Origin() string { return r["o"] }

// BuildDate is the package's build time in Unix seconds (t), or zero.
func (r Record) BuildDate() int64 {
	t, _ := strconv.ParseInt(r["t"], 10, 64)
	return t
}

// Filename is the mirror file name of a package.
func Filename(name, version string) string { return name + "-" + version + ".apk" }

// SplitNameVersion splits "<name>-<pkgver>-r<pkgrel>" into name and version.
// version is empty when s has no pkgrel.
func SplitNameVersion(s string) (name, version string) {
	i := strings.LastIndex(s, "-r")
	if i < 0 {
		return s, ""
	}
	j := strings.LastIndex(s[:i], "-")
	if j < 0 {
		return s, ""
	}
	return s[:j], s[j+1:]
}

// Index is a parsed APKINDEX.tar.gz.
type Index struct {
	Description string // the builder's `git describe` of aports when it wrote the index
	Records     []Record
}

// ParseIndex parses an APKINDEX.tar.gz.
func ParseIndex(r io.Reader) (*Index, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, errors.Wrap(err, "opening index")
	}
	idx := &Index{}
	var text []byte
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.Wrap(err, "reading index")
		}
		switch h.Name {
		case "DESCRIPTION":
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, errors.Wrap(err, "reading DESCRIPTION")
			}
			idx.Description = strings.TrimSpace(string(b))
		case "APKINDEX":
			if text, err = io.ReadAll(tr); err != nil {
				return nil, errors.Wrap(err, "reading APKINDEX")
			}
		}
	}
	for block := range strings.SplitSeq(string(text), "\n\n") {
		rec := Record{}
		for line := range strings.SplitSeq(block, "\n") {
			if len(line) > 1 && line[1] == ':' {
				rec[line[:1]] = line[2:]
			}
		}
		if rec.Name() != "" {
			idx.Records = append(idx.Records, rec)
		}
	}
	return idx, nil
}

// PkgInfo is a parsed .PKGINFO.
type PkgInfo struct {
	Fields map[string][]string
}

// Get returns the first value of a field.
func (p *PkgInfo) Get(k string) string {
	if v := p.Fields[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// ParsePkgInfo parses .PKGINFO text.
func ParsePkgInfo(text string) *PkgInfo {
	p := &PkgInfo{Fields: map[string][]string{}}
	for line := range strings.SplitSeq(text, "\n") {
		if k, v, ok := strings.Cut(line, " = "); ok && !strings.HasPrefix(line, "#") {
			p.Fields[k] = append(p.Fields[k], v)
		}
	}
	return p
}

// ControlChecksum is the APKINDEX checksum of a compressed control member.
func ControlChecksum(compressedControl []byte) string {
	sum := sha1.Sum(compressedControl)
	return "Q1" + base64.StdEncoding.EncodeToString(sum[:])
}

// DataHash is the .PKGINFO datahash of a compressed data member.
func DataHash(compressedData []byte) string {
	sum := sha256.Sum256(compressedData)
	return hex.EncodeToString(sum[:])
}

// Verify reads a package and checks that its control member matches the
// index checksum and its data member matches the datahash in .PKGINFO. It
// streams the data member, so it works for packages larger than memory.
func Verify(r io.Reader, checksum string) error {
	p := newPackageReader(r)
	ctl := sha1.New()
	info, err := p.pkgInfo(ctl)
	if err != nil {
		return err
	}
	data := sha256.New()
	d, err := p.next(data)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, d); err != nil {
		return errors.Wrap(err, "reading data member")
	}
	p.mr.switchTo(nil)
	if _, err := p.mr.br.Peek(1); err != io.EOF {
		return errors.New("expected 3 gzip members, got more")
	}
	if got := "Q1" + base64.StdEncoding.EncodeToString(ctl.Sum(nil)); got != checksum {
		return errors.Errorf("control checksum %s does not match index %s", got, checksum)
	}
	if dh, declared := hex.EncodeToString(data.Sum(nil)), info.Get("datahash"); declared != "" && declared != dh {
		return errors.Errorf("datahash %s does not match declared %s", dh, declared)
	}
	return nil
}

// packageReader reads a package's gzip members in turn.
type packageReader struct {
	mr *memberReader
	zr gzip.Reader
	n  int // members started
}

func newPackageReader(r io.Reader) *packageReader {
	return &packageReader{mr: &memberReader{br: bufio.NewReaderSize(r, 1<<20)}}
}

// next starts the next member, hashing its compressed bytes with h (if
// not nil), and returns its contents.
func (p *packageReader) next(h hash.Hash) (io.Reader, error) {
	// The header is part of the member, so the hash switches first.
	p.mr.switchTo(h)
	if err := p.zr.Reset(p.mr); err == io.EOF {
		return nil, errors.Errorf("expected 3 gzip members, got %d", p.n)
	} else if err != nil {
		return nil, errors.Wrapf(err, "reading gzip member %d", p.n+1)
	}
	p.zr.Multistream(false)
	p.n++
	return &p.zr, nil
}

// pkgInfo skips the signature member and reads .PKGINFO from the control
// member, hashing the control member's compressed bytes with h.
func (p *packageReader) pkgInfo(h hash.Hash) (*PkgInfo, error) {
	sig, err := p.next(nil)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, sig); err != nil {
		return nil, errors.Wrap(err, "reading signature member")
	}
	ctl, err := p.next(h)
	if err != nil {
		return nil, err
	}
	control, err := io.ReadAll(ctl)
	if err != nil {
		return nil, errors.Wrap(err, "reading control member")
	}
	pkginfo, err := TarFile(control, ".PKGINFO")
	if err != nil {
		return nil, errors.Wrap(err, "reading control")
	}
	return ParsePkgInfo(string(pkginfo)), nil
}

// ReadPkgInfo reads a package's .PKGINFO. It reads only the signature and
// control members, so the data member need not be fetched.
func ReadPkgInfo(r io.Reader) (*PkgInfo, error) { return newPackageReader(r).pkgInfo(nil) }

// TarFile returns the content of the named entry of a tar stream.
func TarFile(raw []byte, name string) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.Errorf("%s not found in tar", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == name {
			return io.ReadAll(tr)
		}
	}
}

// memberReader feeds a gzip.Reader. Because it implements io.ByteReader,
// compress/gzip consumes exactly one member's bytes and no further, which
// lets each member be hashed separately.
type memberReader struct {
	br  *bufio.Reader
	h   hash.Hash
	buf []byte // consumed bytes not yet hashed
}

func (m *memberReader) ReadByte() (byte, error) {
	c, err := m.br.ReadByte()
	if err == nil && m.h != nil {
		m.buf = append(m.buf, c)
		if len(m.buf) >= 64<<10 {
			m.flush()
		}
	}
	return c, err
}

func (m *memberReader) Read(p []byte) (int, error) {
	n, err := m.br.Read(p)
	if n > 0 && m.h != nil {
		m.flush()
		m.h.Write(p[:n])
	}
	return n, err
}

func (m *memberReader) flush() {
	if len(m.buf) > 0 {
		m.h.Write(m.buf)
		m.buf = m.buf[:0]
	}
}

func (m *memberReader) switchTo(h hash.Hash) {
	if m.h != nil {
		m.flush()
	}
	m.buf = m.buf[:0]
	m.h = h
}

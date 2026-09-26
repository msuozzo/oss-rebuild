// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apk_test

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk/apktest"
)

func TestVerify(t *testing.T) {
	p := apktest.NewPackage("x", "1.0-r0", []byte("hello world"))
	other := apktest.NewPackage("x", "1.0-r0", []byte("goodbye"))
	tests := []struct {
		Name     string
		Bytes    []byte
		Checksum string
		WantErr  bool
	}{
		{Name: "Match", Bytes: p.Bytes, Checksum: p.Checksum},
		{Name: "ChecksumMismatch", Bytes: p.Bytes, Checksum: other.Checksum, WantErr: true},
		{Name: "Truncated", Bytes: p.Bytes[:len(p.Bytes)/2], Checksum: p.Checksum, WantErr: true},
		{Name: "TrailingData", Bytes: append(bytes.Clone(p.Bytes), 0), Checksum: p.Checksum, WantErr: true},
		{Name: "TwoMembers", Bytes: p.Bytes[:bytes.LastIndex(p.Bytes, []byte{0x1f, 0x8b, 8})], Checksum: p.Checksum, WantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			if err := apk.Verify(bytes.NewReader(tc.Bytes), tc.Checksum); (err != nil) != tc.WantErr {
				t.Errorf("Verify() = %v, want error %v", err, tc.WantErr)
			}
		})
	}
}

func TestParseIndex(t *testing.T) {
	a := apktest.NewPackage("a", "1.0-r0", []byte("a"))
	b := apktest.NewPackage("b-doc", "2.0_rc1-r3", []byte("b"))
	idx, err := apk.ParseIndex(bytes.NewReader(apktest.Index("v20260805-2885-g740f5577b9f",
		apktest.Entry{Package: a, Origin: "a", BuildDate: 100},
		apktest.Entry{Package: b, Origin: "b", BuildDate: 200},
	)))
	if err != nil {
		t.Fatalf("ParseIndex() = %v", err)
	}
	type rec struct {
		Name, Version, Checksum, Origin string
		BuildDate                       int64
	}
	var got []rec
	for _, r := range idx.Records {
		got = append(got, rec{r.Name(), r.Version(), r.Checksum(), r.Origin(), r.BuildDate()})
	}
	want := []rec{
		{"a", "1.0-r0", a.Checksum, "a", 100},
		{"b-doc", "2.0_rc1-r3", b.Checksum, "b", 200},
	}
	if idx.Description != "v20260805-2885-g740f5577b9f" {
		t.Errorf("Description = %q", idx.Description)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Records mismatch (-want +got):\n%s", diff)
	}
}

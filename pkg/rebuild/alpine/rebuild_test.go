// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	alpinereg "github.com/google/oss-rebuild/pkg/registry/alpine"
)

func TestParsePackage(t *testing.T) {
	tests := []struct {
		Name     string
		Package  string
		WantRepo alpinereg.Repository
		WantName string
		WantErr  bool
	}{
		{Name: "Release", Package: "v3.24/main/x86_64/zlib", WantRepo: alpinereg.Repository{Branch: "v3.24", Repo: "main", Arch: "x86_64"}, WantName: "zlib"},
		{Name: "HyphenatedName", Package: "edge/community/aarch64/py3-foo-bar", WantRepo: alpinereg.Repository{Branch: "edge", Repo: "community", Arch: "aarch64"}, WantName: "py3-foo-bar"},
		{Name: "NameOnly", Package: "zlib", WantErr: true},
		{Name: "MissingArch", Package: "v3.24/main/zlib", WantErr: true},
		{Name: "EmptyName", Package: "v3.24/main/x86_64/", WantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			repo, name, err := ParsePackage(tc.Package)
			if (err != nil) != tc.WantErr {
				t.Fatalf("ParsePackage() error = %v, want error %v", err, tc.WantErr)
			}
			if repo != tc.WantRepo || name != tc.WantName {
				t.Errorf("ParsePackage() = (%v, %q), want (%v, %q)", repo, name, tc.WantRepo, tc.WantName)
			}
		})
	}
}

func TestBaseImage(t *testing.T) {
	tests := []struct {
		Name    string
		Package string
		Want    string
		WantErr bool
	}{
		{Name: "Release", Package: "v3.24/main/x86_64/zlib", Want: "docker.io/library/alpine:3.24"},
		{Name: "Edge", Package: "edge/community/x86_64/zlib", Want: "docker.io/library/alpine:edge"},
		{Name: "UnknownBranch", Package: "latest-stable/main/x86_64/zlib", WantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := BaseImage(rebuild.Target{Ecosystem: rebuild.Alpine, Package: tc.Package})
			if (err != nil) != tc.WantErr {
				t.Fatalf("BaseImage() error = %v, want error %v", err, tc.WantErr)
			}
			if got != tc.Want {
				t.Errorf("BaseImage() = %q, want %q", got, tc.Want)
			}
		})
	}
}

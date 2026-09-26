// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/urlx"
)

func TestBuilderName(t *testing.T) {
	tests := []struct{ Name, Branch, Arch, Want string }{
		{Name: "Edge", Branch: "edge", Arch: "x86_64", Want: "build-edge-x86_64"},
		{Name: "Release", Branch: "v3.24", Arch: "aarch64", Want: "build-3-24-aarch64"},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			if got := (Repository{Branch: tc.Branch, Repo: "main", Arch: tc.Arch}).Builder(); got != tc.Want {
				t.Errorf("Builder() = %s, want %s", got, tc.Want)
			}
		})
	}
}

func TestIndexConditional(t *testing.T) {
	mtime := time.Date(2026, 9, 25, 15, 28, 5, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpine/v3.24/main/x86_64/APKINDEX.tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		http.ServeContent(w, r, "APKINDEX.tar.gz", mtime, bytes.NewReader([]byte("index")))
	}))
	defer srv.Close()
	m := Mirror{Client: srv.Client(), URL: urlx.MustParse(srv.URL + "/alpine/")}
	repo := Repository{Branch: "v3.24", Repo: "main", Arch: "x86_64"}
	first, err := m.Index(context.Background(), repo, "", "")
	if err != nil {
		t.Fatalf("Index() = %v", err)
	}
	if string(first.Body) != "index" || first.ETag != `"abc"` || first.NotModified {
		t.Errorf("Index() = %+v", first)
	}
	second, err := m.Index(context.Background(), repo, first.ETag, first.LastModified)
	if err != nil {
		t.Fatalf("Index() = %v", err)
	}
	if !second.NotModified || second.Body != nil {
		t.Errorf("conditional Index() = %+v, want NotModified", second)
	}
	if _, err := m.Index(context.Background(), Repository{Branch: "edge", Repo: "main", Arch: "x86_64"}, "", ""); !IsNotFound(err) {
		t.Errorf("Index() of a missing repository = %v, want not found", err)
	}
}

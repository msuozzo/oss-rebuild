// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	alpinereg "github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk/apktest"
	"github.com/google/oss-rebuild/pkg/registry/alpine/snapshot"
)

func TestParseLog(t *testing.T) {
	got := parseLog(textwrap.Dedent(`
		>>> ocaml-cmdliner: Building community/ocaml-cmdliner 1.3.0-r5 (using abuild 3.17.0-r0) started Wed, 24 Sep 2026 10:00:00 +0000
		>>> ocaml-cmdliner: Installing for build: build-base ocaml ocaml-findlib-dev>=1.9
		(1/3) Installing ocaml-runtime (4.14.3-r1)
		(2/3) Installing ocaml (4.14.3-r1)
		(3/3) Installing .makedepends-ocaml-cmdliner (20260924.100001)
		(1/1) Upgrading musl (1.2.6-r2 -> 1.2.6-r3)
		(1/2) Purging .makedepends-ocaml-cmdliner (20260924.100001)
		(2/2) Purging ocaml (4.14.3-r1)
		`))
	want := &buildLog{
		Started:   time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
		Requested: []string{"build-base", "ocaml", "ocaml-findlib-dev>=1.9"},
		Installed: []string{"ocaml-runtime-4.14.3-r1", "ocaml-4.14.3-r1", "musl-1.2.6-r3"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("parseLog() mismatch (-want +got):\n%s", diff)
	}
}

// toServer sends every request to a test server, keeping its path.
type toServer struct{ url *url.URL }

func (s toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = s.url.Scheme, s.url.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestInfer(t *testing.T) {
	start := time.Date(2026, 9, 25, 15, 20, 35, 0, time.UTC)
	musl := apktest.NewPackage("musl", "1.2.6-r3", []byte("musl"))
	libb := apktest.NewPackage("lib-b", "1.0-r0", []byte("lib-b 1.0"))
	libbNew := apktest.NewPackage("lib-b", "1.1-r0", []byte("lib-b 1.1"))
	kiota := apktest.NewPackage("kiota", "1.32.5-r0", []byte("kiota"),
		"origin = kiota", "commit = 0403b844be5baff0dbfcae3a98194230c6862bff", "builddate = 1790349605", "packager = Buildozer <alpine-devel@lists.alpinelinux.org>")
	objects := map[string][]byte{
		"blobs/sha256/main0": apktest.Index("v20260805-1-gaaaaaaa", apktest.Entry{Package: musl, Origin: "musl"}),
		"blobs/sha256/comm0": apktest.Index("v20260805-2-gbbbbbbb", apktest.Entry{Package: libb, Origin: "lib-b"}),
		// The builder built lib-b 1.1 in the same pass, before kiota.
		"blobs/sha256/comm1": apktest.Index("v20260805-2885-g740f5577b9f",
			apktest.Entry{Package: libbNew, Origin: "lib-b"}, apktest.Entry{Package: kiota, Origin: "kiota"}),
		"apk/v3.24/community/x86_64/kiota-1.32.5-r0.apk": kiota.Bytes,
		"buildlogs/build-3-24-x86_64/community/kiota/kiota-1.32.5-r0.log": []byte(fmt.Sprintf(
			">>> kiota: Building community/kiota 1.32.5-r0 (using abuild 3.17.0-r0) started %s\n"+
				">>> kiota: Installing for build: build-base lib-b\n"+
				"(1/2) Installing lib-b (1.1-r0)\n"+
				"(2/2) Installing .makedepends-kiota (20260925.152040)\n"+
				"(1/1) Upgrading musl (1.2.6-r2 -> 1.2.6-r3)\n", start.Format(time.RFC1123Z))),
	}
	uploads := snapshot.Uploads{
		{Branch: "v3.24", Repo: "main", Arch: "x86_64", LastModified: start.Add(-2 * time.Hour).Unix(), SHA256: "main0"},
		{Branch: "v3.24", Repo: "community", Arch: "x86_64", LastModified: start.Add(-time.Hour).Unix(), SHA256: "comm0"},
		{Branch: "v3.24", Repo: "community", Arch: "x86_64", LastModified: start.Add(30 * time.Minute).Unix(), SHA256: "comm1", Description: "v20260805-2885-g740f5577b9f"},
	}
	var table bytes.Buffer
	for _, up := range uploads {
		json.NewEncoder(&table).Encode(up)
	}
	objects[snapshot.UploadsObject] = table.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/alpinelinux/aports/commit/740f5577b9f.patch":
			fmt.Fprintln(w, "From 740f5577b9f976333fefa4b32b3292c0a382941b Mon Sep 17 00:00:00 2001")
		case len(r.URL.Path) > len("/archive-bucket/") && objects[r.URL.Path[len("/archive-bucket/"):]] != nil:
			w.Write(objects[r.URL.Path[len("/archive-bucket/"):]])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: toServer{urlx.MustParse(srv.URL)}}
	src := Sources{
		Archive:   snapshot.GCSArchive(client, "archive-bucket"),
		BuildLogs: alpinereg.BuildLogs{Client: client, URL: alpinereg.BuildLogHost},
		GitHub:    client,
	}
	target := rebuild.Target{Ecosystem: rebuild.Alpine, Package: "v3.24/community/x86_64/kiota", Version: "1.32.5-r0", Artifact: "kiota-1.32.5-r0.apk"}
	got, err := Infer(context.Background(), target, src)
	if err != nil {
		t.Fatalf("Infer() = %v", err)
	}
	want := &Abuild{
		Archive: "archive-bucket",
		Branch:  "v3.24",
		Repo:    "community",
		Arch:    "x86_64",
		Origin:  "kiota",
		Builder: "build-3-24-x86_64",
		Repositories: []UploadRef{
			{Repo: "main", Time: start.Add(-2 * time.Hour)},
			{Repo: "community", Time: start.Add(-time.Hour)},
		},
		Overrides:       []Override{{Name: "lib-b", Version: "1.1-r0", Repo: "community", Upload: start.Add(30 * time.Minute)}},
		Baseline:        baselines["build-3-24-x86_64"],
		Requested:       []string{"build-base", "lib-b"},
		AportsCommit:    "740f5577b9f976333fefa4b32b3292c0a382941b",
		Commit:          "0403b844be5baff0dbfcae3a98194230c6862bff",
		SourceDateEpoch: 1790349605,
		Packager:        "Buildozer <alpine-devel@lists.alpinelinux.org>",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Infer() mismatch (-want +got):\n%s", diff)
	}
	t.Run("UnarchivedDependency", func(t *testing.T) {
		delete(objects, "blobs/sha256/comm1")
		objects["blobs/sha256/comm1"] = apktest.Index("v20260805-2885-g740f5577b9f", apktest.Entry{Package: kiota, Origin: "kiota"})
		if _, err := Infer(context.Background(), target, src); err == nil {
			t.Error("Infer() with lib-b 1.1 in no upload succeeded, want error")
		}
	})
}

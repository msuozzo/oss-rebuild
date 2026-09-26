// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package timewarp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/httpx/httpxtest"
)

func TestHandleAlpine(t *testing.T) {
	const uploads = `{"branch": "v3.24", "repo": "main", "arch": "x86_64", "last_modified": 1790341551, "sha256": "aaa"}
{"branch": "v3.24", "repo": "community", "arch": "x86_64", "last_modified": 1790345000, "sha256": "ccc"}
{"branch": "v3.24", "repo": "main", "arch": "x86_64", "last_modified": 1790349605, "sha256": "bbb"}
`
	ok := func(body string) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: io.NopCloser(bytes.NewBufferString(body))}
	}
	notFound := &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString(""))}
	tests := []struct {
		Name       string
		URL        string
		Time       string
		Calls      []httpxtest.Call
		WantStatus int
		WantBody   string
	}{
		{
			Name: "IndexBetweenUploads",
			URL:  "http://localhost:8081/archive-bucket/v3.24/main/x86_64/APKINDEX.tar.gz",
			Time: "2026-09-25T13:30:00Z",
			Calls: []httpxtest.Call{
				{Method: "GET", URL: "https://storage.googleapis.com/archive-bucket/uploads.v1.jsonl", Response: ok(uploads)},
				{Method: "GET", URL: "https://storage.googleapis.com/archive-bucket/blobs/sha256/aaa", Response: ok("index aaa")},
			},
			WantStatus: http.StatusOK,
			WantBody:   "index aaa",
		},
		{
			Name: "IndexAtUpload",
			URL:  "http://localhost:8081/archive-bucket/v3.24/main/x86_64/APKINDEX.tar.gz",
			Time: "2026-09-25T15:20:05Z",
			Calls: []httpxtest.Call{
				{Method: "GET", URL: "https://storage.googleapis.com/archive-bucket/uploads.v1.jsonl", Response: ok(uploads)},
				{Method: "GET", URL: "https://storage.googleapis.com/archive-bucket/blobs/sha256/bbb", Response: ok("index bbb")},
			},
			WantStatus: http.StatusOK,
			WantBody:   "index bbb",
		},
		{
			Name: "IndexBeforeArchive",
			URL:  "http://localhost:8081/archive-bucket/v3.24/main/x86_64/APKINDEX.tar.gz",
			Time: "2026-09-01T00:00:00Z",
			Calls: []httpxtest.Call{
				{Method: "GET", URL: "https://storage.googleapis.com/archive-bucket/uploads.v1.jsonl", Response: ok(uploads)},
			},
			WantStatus: http.StatusNotFound,
		},
		{
			Name: "Package",
			URL:  "http://localhost:8081/archive-bucket/v3.24/main/x86_64/libstdc++-15.2.0-r5.apk",
			Time: "2026-09-25T13:30:00Z",
			Calls: []httpxtest.Call{
				{Method: "GET", URL: "https://storage.googleapis.com/archive-bucket/apk/v3.24/main/x86_64/libstdc++-15.2.0-r5.apk", Response: ok("package")},
			},
			WantStatus: http.StatusOK,
			WantBody:   "package",
		},
		{
			Name: "PackageMissing",
			URL:  "http://localhost:8081/archive-bucket/v3.24/main/x86_64/zlib-9.9-r0.apk",
			Time: "2026-09-25T13:30:00Z",
			Calls: []httpxtest.Call{
				{Method: "GET", URL: "https://storage.googleapis.com/archive-bucket/apk/v3.24/main/x86_64/zlib-9.9-r0.apk", Response: notFound},
			},
			WantStatus: http.StatusNotFound,
		},
		{
			Name:       "UnsupportedPath",
			URL:        "http://localhost:8081/archive-bucket/v3.24/main/APKINDEX.tar.gz",
			Time:       "2026-09-25T13:30:00Z",
			WantStatus: http.StatusNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			client := &httpxtest.MockClient{Calls: tc.Calls, URLValidator: httpxtest.NewURLValidator(t)}
			req := httptest.NewRequest("GET", tc.URL, nil)
			req.SetBasicAuth("alpine", tc.Time)
			rr := httptest.NewRecorder()
			Handler{Client: client}.ServeHTTP(rr, req)
			if rr.Code != tc.WantStatus {
				t.Errorf("status = %d, want %d (%s)", rr.Code, tc.WantStatus, rr.Body.String())
			}
			if tc.WantStatus == http.StatusOK {
				if diff := cmp.Diff(tc.WantBody, rr.Body.String()); diff != "" {
					t.Errorf("body mismatch (-want +got):\n%s", diff)
				}
			}
			if client.CallCount() != len(tc.Calls) {
				t.Errorf("made %d upstream calls, want %d", client.CallCount(), len(tc.Calls))
			}
		})
	}
}

// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcsx

import (
	"net/http"
	"testing"

	"golang.org/x/oauth2"
)

type headerRecorder struct{ auth []string }

func (r *headerRecorder) Do(req *http.Request) (*http.Response, error) {
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestAuthClient(t *testing.T) {
	for _, tc := range []struct {
		URL      string
		WantAuth string
	}{
		{URL: HTTPURL("bucket", "object"), WantAuth: "Bearer tok"},
		{URL: "https://bucket.storage.googleapis.com/object", WantAuth: ""},
		{URL: "https://build.alpinelinux.org/buildlogs/x.log", WantAuth: ""},
	} {
		t.Run(tc.URL, func(t *testing.T) {
			rec := &headerRecorder{}
			c := &AuthClient{BasicClient: rec, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})}
			req, _ := http.NewRequest(http.MethodGet, tc.URL, nil)
			if _, err := c.Do(req); err != nil {
				t.Fatal(err)
			}
			if rec.auth[0] != tc.WantAuth {
				t.Errorf("Authorization = %q, want %q", rec.auth[0], tc.WantAuth)
			}
			if req.Header.Get("Authorization") != "" {
				t.Error("the caller's request was modified")
			}
		})
	}
}

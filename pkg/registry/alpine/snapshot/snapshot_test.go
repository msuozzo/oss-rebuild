// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/registry/alpine"
)

func TestAtOrBefore(t *testing.T) {
	main := alpine.Repository{Branch: "edge", Repo: "main", Arch: "x86_64"}
	community := alpine.Repository{Branch: "edge", Repo: "community", Arch: "x86_64"}
	u, err := ParseUploads(strings.NewReader(`{"branch":"edge","repo":"community","arch":"x86_64","last_modified":100,"sha256":"c100"}
{"branch":"edge","repo":"main","arch":"x86_64","last_modified":150,"sha256":"m150"}
{"branch":"edge","repo":"community","arch":"x86_64","last_modified":200,"sha256":"c200"}
`))
	if err != nil {
		t.Fatalf("ParseUploads() = %v", err)
	}
	tests := []struct {
		Name      string
		Repo      alpine.Repository
		At        int64
		WantSHA   string
		WantFound bool
	}{
		{Name: "Exact", Repo: community, At: 200, WantSHA: "c200", WantFound: true},
		{Name: "Between", Repo: community, At: 299, WantSHA: "c200", WantFound: true},
		{Name: "TooEarly", Repo: community, At: 99},
		{Name: "OtherRepo", Repo: main, At: 299, WantSHA: "m150", WantFound: true},
	}
	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			got, found := u.AtOrBefore(tc.Repo, time.Unix(tc.At, 0))
			if found != tc.WantFound || got.SHA256 != tc.WantSHA {
				t.Errorf("AtOrBefore() = (%q, %v), want (%q, %v)", got.SHA256, found, tc.WantSHA, tc.WantFound)
			}
		})
	}
}

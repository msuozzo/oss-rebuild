// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"strings"
	"testing"

	"github.com/google/oss-rebuild/pkg/scheduler"
)

func TestRankProminence(t *testing.T) {
	got := rankProminence([]scheduler.ProminenceRecord{
		{Ecosystem: "npm", Package: "obscure", P: 0.1},
		{Ecosystem: "cratesio", Package: "ripgrep", P: 0.9},
		{Ecosystem: "npm", Package: "lodash", P: 1.0},
		{Ecosystem: "npm", Package: "", P: 0.5},
	})
	if len(got) != 3 {
		t.Fatalf("got %d ranked, want 3 (records missing a package are dropped)", len(got))
	}
	if got[0].Package != "ripgrep" {
		t.Errorf("ecosystems should be emitted in sorted order, got %q first", got[0].Package)
	}
	// ripgrep tops cratesio on 0.9 while lodash needs 1.0 to top npm: the
	// quantile is per-ecosystem, so raw p never competes across registries.
	if got[0].QProm != 1.0 {
		t.Errorf("cratesio top quantile = %v, want 1", got[0].QProm)
	}
	if got[1].Package != "lodash" || got[1].QProm != 1.0 {
		t.Errorf("npm top = %q at %v, want lodash at 1", got[1].Package, got[1].QProm)
	}
	if got[2].QProm != 0.5 {
		t.Errorf("npm second quantile = %v, want 0.5", got[2].QProm)
	}
}

func TestPromptNamesEcosystemInEnglish(t *testing.T) {
	// The model reasons about "a Rust crate" far better than about "cratesio".
	if got := promptFor([]pkgRef{{"cratesio", "serde"}}); !strings.Contains(got, "ecosystem=Rust") {
		t.Errorf("prompt should name the ecosystem in English, got:\n%s", got)
	}
	if got := ecoEnglish("unknown-eco"); got != "unknown-eco" {
		t.Errorf("unknown ecosystem should pass through, got %q", got)
	}
}

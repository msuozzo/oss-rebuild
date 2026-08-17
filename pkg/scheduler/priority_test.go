// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestPercentile(t *testing.T) {
	for _, tc := range []struct {
		name string
		rank int
		n    int
		want float64
	}{
		{"TopOfTen", 0, 10, 1.0},
		{"LastOfTen", 9, 10, 0.1},
		{"MedianOfFour", 1, 4, 0.75},
		{"SoleMember", 0, 1, 1.0},
		{"EmptyPopulation", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Percentile(tc.rank, tc.n)); diff != "" {
				t.Errorf("Percentile(%d, %d) mismatch (-want +got):\n%s", tc.rank, tc.n, diff)
			}
		})
	}
}

func TestRankBand(t *testing.T) {
	for _, tc := range []struct {
		name string
		rank int
		want string
	}{
		{"FirstRank", 0, "top100"},
		{"LastOfTop100", 99, "top100"},
		{"FirstOfTop1k", 100, "top1k"},
		{"LastOfTop1k", 999, "top1k"},
		{"Longtail", 1000, "longtail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, RankBand(tc.rank)); diff != "" {
				t.Errorf("RankBand(%d) mismatch (-want +got):\n%s", tc.rank, diff)
			}
		})
	}
}

func TestDocumentIDsEscapeSeparators(t *testing.T) {
	// Scoped npm names contain "/", which Firestore forbids in document IDs.
	if diff := cmp.Diff("npm!@babel%2Fcore", PackageID("npm", "@babel/core")); diff != "" {
		t.Errorf("PackageID mismatch (-want +got):\n%s", diff)
	}
	id := TargetID(rebuild.Target{Ecosystem: rebuild.NPM, Package: "@scope/name", Version: "1.0.0", Artifact: "a.tgz"})
	if strings.Contains(id, "/") {
		t.Errorf("TargetID must not contain '/': %q", id)
	}
	if got, want := strings.Count(id, "!"), 3; got != want {
		t.Errorf("TargetID has %d separators, want %d: %q", got, want, id)
	}
	// A name containing the separator must not produce another pair's ID.
	if PackageID("npm", "a!b") == PackageID("npm", "a")+"!b" {
		t.Error("PackageID collides with its own separator")
	}
}

func TestRescore(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Priority
		want float64
	}{
		{"BothSignals", Priority{QCrit: 0.8, QProm: 0.6}, 0.7},
		// A package carrying one signal scores at most half, so it ranks below
		// anything both signals agree on but still above the floor.
		{"CriticalityOnly", Priority{QCrit: 0.8}, 0.4},
		{"ProminenceOnly", Priority{QProm: 0.8}, 0.4},
		{"NoSignals", Priority{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.in
			p.Rescore()
			if diff := cmp.Diff(tc.want, p.Score); diff != "" {
				t.Errorf("Score mismatch (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("LeafAppOutranksObscureLibrary", func(t *testing.T) {
		// The case prominence exists for: an application has no dependents at
		// all, yet must still outrank a library that has a few.
		leafApp, obscureLibrary := Priority{QProm: 1.0}, Priority{QCrit: 0.2}
		leafApp.Rescore()
		obscureLibrary.Rescore()
		if leafApp.Score <= obscureLibrary.Score {
			t.Errorf("leaf app (%v) should outrank an obscure library (%v)", leafApp.Score, obscureLibrary.Score)
		}
	})
}

func TestScoreWithSubstitutesCriticality(t *testing.T) {
	// Scoring a version swaps in that version's criticality while keeping
	// prominence, which is per-package and identical across versions.
	p := Priority{QCrit: 0.2, QProm: 0.6}
	if got, want := p.ScoreWith(1.0), 0.8; got != want {
		t.Errorf("ScoreWith(1.0) = %v, want %v", got, want)
	}
	if p.Score != 0 {
		t.Error("ScoreWith must not mutate the package's own Score")
	}
}

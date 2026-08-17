// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

func TestScoreVersionsPrefersVersionCriticality(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	crit := db.NewMemoryVersionCriticalities()
	for v, q := range map[string]float64{"4.17.21": 1.0, "4.0.0": 0.01} {
		if err := crit.Upsert(ctx, scheduler.VersionCriticality{
			Ecosystem: "npm", Package: "lodash", Version: v, QCrit: q,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := enqueueConfig{Ecosystem: "npm", Package: "lodash", FreshnessK: 3, FreshnessTauHours: 120}
	priority := scheduler.Priority{QCrit: 0.5, QProm: 0.9}
	// Both are old enough that freshness has decayed to nothing, so what
	// separates them is how many packages actually resolve to each.
	got := scoreVersions(ctx, crit, priority, []versionInfo{
		{Version: "4.0.0", Published: now.AddDate(-1, 0, 0)},
		{Version: "4.17.21", Published: now.AddDate(0, -6, 0)},
	}, cfg, now)

	if len(got) != 2 {
		t.Fatalf("got %d targets, want 2", len(got))
	}
	if got[0].Version != "4.17.21" {
		t.Errorf("first admitted = %q, want the widely-depended-upon 4.17.21", got[0].Version)
	}
	// Each version is scored against its own criticality, with the package's
	// prominence carried through unchanged.
	if want := priority.ScoreWith(1.0); got[0].Score != want {
		t.Errorf("score = %v, want %v from the version quantile 1.0", got[0].Score, want)
	}
	if want := priority.ScoreWith(0.01); got[1].Score != want {
		t.Errorf("score = %v, want %v from the version quantile 0.01", got[1].Score, want)
	}
}

func TestScoreVersionsLetsFreshnessOverrideCriticality(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	crit := db.NewMemoryVersionCriticalities()
	for v, q := range map[string]float64{"4.17.21": 1.0, "5.0.0-rc1": 0.01} {
		if err := crit.Upsert(ctx, scheduler.VersionCriticality{
			Ecosystem: "npm", Package: "lodash", Version: v, QCrit: q,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := enqueueConfig{Ecosystem: "npm", Package: "lodash", FreshnessK: 3, FreshnessTauHours: 120}
	got := scoreVersions(ctx, crit, scheduler.Priority{QCrit: 0.5, QProm: 0.9}, []versionInfo{
		{Version: "4.17.21", Published: now.AddDate(-1, 0, 0)},
		{Version: "5.0.0-rc1", Published: now.Add(-time.Hour)},
	}, cfg, now)

	// An hours-old release outranks a year-old one that far more packages
	// depend on. That is deliberate: the freshness multiplier spans 1 to 1+k,
	// so it is allowed to overturn a score gap, trading completeness for
	// getting new releases covered quickly. Lower --freshness-k to weaken it.
	if got[0].Version != "5.0.0-rc1" {
		t.Errorf("first admitted = %q, want the brand-new 5.0.0-rc1", got[0].Version)
	}
	if got[0].Score >= got[1].Score {
		t.Error("the fresh version should win on freshness despite a lower score")
	}
}

func TestScoreVersionsFallsBackToPackageCriticality(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := enqueueConfig{Ecosystem: "npm", Package: "obscure", FreshnessK: 3, FreshnessTauHours: 120}
	// No version criticality at all, so every version inherits the package's
	// and recency is left to order the back catalogue.
	priority := scheduler.Priority{QCrit: 0.7}
	got := scoreVersions(ctx, db.NewMemoryVersionCriticalities(), priority, []versionInfo{
		{Version: "1.0.0", Published: now.AddDate(-2, 0, 0)},
		{Version: "2.0.0", Published: now.Add(-time.Hour)},
	}, cfg, now)

	if got[0].Version != "2.0.0" {
		t.Errorf("first admitted = %q, want the freshest 2.0.0", got[0].Version)
	}
	for _, lt := range got {
		if want := priority.ScoreWith(priority.QCrit); lt.Score != want {
			t.Errorf("%s score = %v, want the package's own %v", lt.Version, lt.Score, want)
		}
		if lt.State != scheduler.StateQueued || scheduler.Tier(lt.NextTier) != scheduler.TierInference {
			t.Errorf("%s enqueued as %v at %v, want queued at T1", lt.Version, lt.State, scheduler.Tier(lt.NextTier))
		}
	}
}

func TestScoreVersionsAdmitsByDispatchOrder(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	crit := db.NewMemoryVersionCriticalities()
	for v, q := range map[string]float64{"1.0.0": 0.9, "2.0.0": 0.1} {
		if err := crit.Upsert(ctx, scheduler.VersionCriticality{
			Ecosystem: "npm", Package: "p", Version: v, QCrit: q,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := enqueueConfig{Ecosystem: "npm", Package: "p", FreshnessK: 3, FreshnessTauHours: 120}
	got := scoreVersions(ctx, crit, scheduler.Priority{}, []versionInfo{
		{Version: "1.0.0", Published: now.AddDate(0, -6, 0)},
		{Version: "2.0.0", Published: now.AddDate(0, -6, 0)},
	}, cfg, now)

	// Equal age, so criticality decides. Admission uses the same ordering the
	// queue is drained by, so a --max-versions of 1 keeps the right one.
	if got[0].Version != "1.0.0" {
		t.Errorf("first admitted = %q, want 1.0.0", got[0].Version)
	}
	if got[0].DispatchOrder() <= got[1].DispatchOrder() {
		t.Error("results must be ordered by descending DispatchOrder")
	}
}

func TestScoreVersionsRequiresNoCriticalityStoreEntry(t *testing.T) {
	// A lookup miss must not error out the whole enqueue.
	ctx := context.Background()
	cfg := enqueueConfig{Ecosystem: "npm", Package: "p", FreshnessK: 3, FreshnessTauHours: 120}
	got := scoreVersions(ctx, db.NewMemoryVersionCriticalities(), scheduler.Priority{}, []versionInfo{
		{Version: "1.0.0"},
	}, cfg, time.Now().UTC())
	if len(got) != 1 {
		t.Fatalf("got %d targets, want 1", len(got))
	}
	if got[0].Target().Ecosystem != rebuild.NPM {
		t.Errorf("ecosystem = %q, want npm", got[0].Target().Ecosystem)
	}
}

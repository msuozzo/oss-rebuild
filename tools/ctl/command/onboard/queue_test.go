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
	// The widely-resolved version is a year old, the fresh one has no
	// dependents at all. Ranking on recency alone would invert these.
	if err := crit.Upsert(ctx, scheduler.VersionCriticality{
		Ecosystem: "npm", Package: "lodash", Version: "4.17.21", QCrit: 1.0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := crit.Upsert(ctx, scheduler.VersionCriticality{
		Ecosystem: "npm", Package: "lodash", Version: "5.0.0-rc1", QCrit: 0.01,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := enqueueConfig{Ecosystem: "npm", Package: "lodash", FreshnessK: 3, FreshnessTauHours: 120}
	got := scoreVersions(ctx, crit, scheduler.Priority{QCrit: 0.5}, []versionInfo{
		{Version: "4.17.21", Published: now.AddDate(-1, 0, 0)},
		{Version: "5.0.0-rc1", Published: now.Add(-time.Hour)},
	}, cfg, now)

	if len(got) != 2 {
		t.Fatalf("got %d targets, want 2", len(got))
	}
	if got[0].Version != "4.17.21" {
		t.Errorf("first admitted = %q, want the widely-depended-upon 4.17.21", got[0].Version)
	}
	if got[0].Score != 1.0 || got[1].Score != 0.01 {
		t.Errorf("scores = %v, %v; want the version quantiles 1 and 0.01", got[0].Score, got[1].Score)
	}
}

func TestScoreVersionsFallsBackToPackageCriticality(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := enqueueConfig{Ecosystem: "npm", Package: "obscure", FreshnessK: 3, FreshnessTauHours: 120}
	// No version criticality at all, so every version inherits the package's
	// and recency is left to order the back catalogue.
	got := scoreVersions(ctx, db.NewMemoryVersionCriticalities(), scheduler.Priority{QCrit: 0.7}, []versionInfo{
		{Version: "1.0.0", Published: now.AddDate(-2, 0, 0)},
		{Version: "2.0.0", Published: now.Add(-time.Hour)},
	}, cfg, now)

	if got[0].Version != "2.0.0" {
		t.Errorf("first admitted = %q, want the freshest 2.0.0", got[0].Version)
	}
	for _, lt := range got {
		if lt.Score != 0.7 {
			t.Errorf("%s score = %v, want the package quantile 0.7", lt.Version, lt.Score)
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

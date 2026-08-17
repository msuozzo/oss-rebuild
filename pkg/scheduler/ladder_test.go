// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestTierString(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Tier
		want string
	}{
		{"Replay", TierReplay, "T0"},
		{"Inference", TierInference, "T1"},
		{"Agent", TierAgent, "T2"},
		{"Manual", TierManual, "T3"},
		{"OutOfRange", Tier(9), "T?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.in.String()); diff != "" {
				t.Errorf("Tier.String mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFreshness(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		published time.Time
		tauHours  float64
		want      float64
	}{
		// An unknown publish date must not boost or penalize, so it lands at
		// the multiplicative identity.
		{"UnknownPublishDate", time.Time{}, 120, 1},
		{"NonPositiveTau", now, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Freshness(tc.published, now, 3, tc.tauHours)); diff != "" {
				t.Errorf("Freshness mismatch (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("DecaysWithAge", func(t *testing.T) {
		fresh := Freshness(now.Add(-time.Hour), now, 3, 120)
		old := Freshness(now.Add(-30*24*time.Hour), now, 3, 120)
		if !(fresh > old && old >= 1) {
			t.Errorf("expected fresh (%v) > old (%v) >= 1", fresh, old)
		}
	})
	t.Run("FutureDateDoesNotAmplify", func(t *testing.T) {
		// Registry clock skew can report a publish time slightly ahead of now.
		// Negative age must clamp rather than run the exponential above 1+k.
		if got, want := Freshness(now.Add(time.Hour), now, 3, 120), 4.0; got != want {
			t.Errorf("Freshness = %v, want %v", got, want)
		}
	})
}

func TestDispatchOrder(t *testing.T) {
	// A fresh release of a mid-tier package can outrank a stale version of a
	// critical one. That mobility is the point of keeping the terms separate.
	freshMidTier := LadderTarget{Score: 0.30, Freshness: 4}
	staleCritical := LadderTarget{Score: 0.99, Freshness: 1}
	if freshMidTier.DispatchOrder() <= staleCritical.DispatchOrder() {
		t.Errorf("fresh mid-tier (%v) should outrank stale critical (%v)",
			freshMidTier.DispatchOrder(), staleCritical.DispatchOrder())
	}
	freshCritical := LadderTarget{Score: 0.99, Freshness: 4}
	if freshCritical.DispatchOrder() <= freshMidTier.DispatchOrder() {
		t.Error("a fresh critical package must still outrank a fresh mid-tier one")
	}
}

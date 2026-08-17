// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

// TestMaybeTimeout covers the requeue of dispatches that never reported back.
func TestMaybeTimeout(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		cfg          roundConfig
		dispatchedAt time.Time
		want         bool
	}{
		{"WedgedPastTimeout", roundConfig{InflightTimeout: time.Hour}, now.Add(-2 * time.Hour), true},
		{"StillWithinTimeout", roundConfig{InflightTimeout: time.Hour}, now.Add(-time.Minute), false},
		{"TimeoutDisabled", roundConfig{}, now.Add(-100 * time.Hour), false},
		// Without a dispatch time there is no age to compare, and requeueing
		// on that basis would loop.
		{"NeverDispatched", roundConfig{InflightTimeout: time.Hour}, time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &round{cfg: tc.cfg, now: now}
			target := scheduler.LadderTarget{State: scheduler.StateInFlight, DispatchedAt: tc.dispatchedAt}
			if diff := cmp.Diff(tc.want, r.maybeTimeout(&target)); diff != "" {
				t.Errorf("maybeTimeout mismatch (-want +got):\n%s", diff)
			}
			if tc.want && target.State != scheduler.StateQueued {
				t.Errorf("timed-out target State = %v, want queued", target.State)
			}
		})
	}
}

// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestTick(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		in         LadderTarget
		outcome    Outcome
		maxRetries int
		want       LadderTarget
	}{
		{
			name:       "AttestedIsDone",
			in:         LadderTarget{NextTier: int(TierInference), State: StateInFlight},
			outcome:    OutcomeAttested,
			maxRetries: 3,
			want:       LadderTarget{NextTier: int(TierInference), State: StateDone, Outcome: OutcomeAttested, Updated: now},
		},
		{
			name:       "InferenceFailureEscalatesToAgent",
			in:         LadderTarget{NextTier: int(TierInference), State: StateInFlight},
			outcome:    OutcomeFailure,
			maxRetries: 3,
			want:       LadderTarget{NextTier: int(TierAgent), State: StateQueued, Outcome: OutcomeFailure, Updated: now},
		},
		{
			// Nothing more expensive exists above the agent tier.
			name:       "AgentFailureParks",
			in:         LadderTarget{NextTier: int(TierAgent), State: StateInFlight},
			outcome:    OutcomeFailure,
			maxRetries: 3,
			want: LadderTarget{NextTier: int(TierManual), State: StateParked, Outcome: OutcomeFailure,
				ParkReason: "agent tier exhausted", Updated: now},
		},
		{
			// Transient failures say nothing about the package, so they must
			// not buy it a more expensive tier.
			name:       "TransientRetriesSameTier",
			in:         LadderTarget{NextTier: int(TierAgent), State: StateInFlight},
			outcome:    OutcomeTransient,
			maxRetries: 3,
			want:       LadderTarget{NextTier: int(TierAgent), State: StateQueued, Outcome: OutcomeTransient, Retries: 1, Updated: now},
		},
		{
			name:       "TransientParksAfterMaxRetries",
			in:         LadderTarget{NextTier: int(TierAgent), State: StateInFlight, Retries: 2},
			outcome:    OutcomeTransient,
			maxRetries: 3,
			want: LadderTarget{NextTier: int(TierManual), State: StateParked, Outcome: OutcomeTransient,
				Retries: 3, ParkReason: "persistent transient failures", Updated: now},
		},
		{
			name:       "UnboundedRetriesNeverPark",
			in:         LadderTarget{NextTier: int(TierAgent), State: StateInFlight, Retries: 99},
			outcome:    OutcomeTransient,
			maxRetries: 0,
			want:       LadderTarget{NextTier: int(TierAgent), State: StateQueued, Outcome: OutcomeTransient, Retries: 100, Updated: now},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Tick(tc.in, tc.outcome, tc.maxRetries, now)); diff != "" {
				t.Errorf("Tick mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import "time"

// Tick advances one target's state machine given the outcome of its last
// attempt. Attested finishes it, transient retries the same tier, and failure
// escalates a tier or parks the target at T3.
//
// Transient retries are bounded because a target that keeps hitting them is
// indistinguishable from one that is wedged, and either way it would absorb
// dispatch capacity indefinitely. maxRetries <= 0 disables that bound.
func Tick(t LadderTarget, outcome Outcome, maxRetries int, now time.Time) LadderTarget {
	t.Outcome = outcome
	t.Updated = now
	switch outcome {
	case OutcomeAttested:
		t.State = StateDone
	case OutcomeTransient:
		t.Retries++
		if maxRetries > 0 && t.Retries >= maxRetries {
			t.State = StateParked
			t.NextTier = int(TierManual)
			t.ParkReason = "persistent transient failures"
			return t
		}
		t.State = StateQueued
	case OutcomeFailure:
		// Escalation stops at the agent tier. Beyond it there is nothing more
		// expensive to try, so the target is parked for a human.
		if t.NextTier < int(TierAgent) {
			t.NextTier++
			t.State = StateQueued
		} else {
			t.NextTier = int(TierManual)
			t.State = StateParked
			t.ParkReason = "agent tier exhausted"
		}
	}
	return t
}

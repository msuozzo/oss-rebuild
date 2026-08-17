// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"math"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Tier is a rung on the escalation ladder. Each rung is more expensive than
// the last. Escalation past the cheap tiers is what the budget rations.
type Tier int

const (
	// TierReplay (T0) re-runs a known/promoted strategy with no inference.
	// Reserved for sibling fan-out (deferred). The MVP enqueues at T1.
	TierReplay Tier = iota
	// TierInference (T1) runs heuristic inference plus a build.
	TierInference
	// TierAgent (T2) runs the multi-iteration LLM agent. Rationed.
	TierAgent
	// TierManual (T3) is out of the automated system: parked for triage.
	TierManual
)

func (t Tier) String() string {
	switch t {
	case TierReplay:
		return "T0"
	case TierInference:
		return "T1"
	case TierAgent:
		return "T2"
	case TierManual:
		return "T3"
	default:
		return "T?"
	}
}

// Outcome is the classification of a single attempt. Exactly one applies.
type Outcome string

const (
	OutcomePending   Outcome = ""          // no terminal result yet
	OutcomeAttested  Outcome = "ATTESTED"  // reproduced, an attestation exists
	OutcomeTransient Outcome = "TRANSIENT" // throttle or infra flake, retry same tier
	OutcomeFailure   Outcome = "FAILURE"   // ran and failed, escalate or park
)

// TargetState tracks where a target sits in the dispatch workflow.
type TargetState string

const (
	StateQueued   TargetState = "QUEUED"   // eligible for dispatch at NextTier
	StateInFlight TargetState = "INFLIGHT" // dispatched, awaiting outcome
	StateParked   TargetState = "PARKED"   // T3 triage, no further automated spend
	StateDone     TargetState = "DONE"     // attested
)

// LadderTarget is the queue-state document for one package version.
// Document ID is TargetID(Target()).
type LadderTarget struct {
	Ecosystem string `firestore:"ecosystem,omitempty"`
	Package   string `firestore:"package,omitempty"`
	Version   string `firestore:"version,omitempty"`
	Artifact  string `firestore:"artifact,omitempty"`

	NextTier int         `firestore:"next_tier"` // Tier as int (T1 by default)
	State    TargetState `firestore:"state,omitempty"`
	Outcome  Outcome     `firestore:"outcome,omitempty"`
	Attempts int         `firestore:"attempts,omitempty"`
	Retries  int         `firestore:"retries,omitempty"` // same-tier transient retries

	LastRunID   string `firestore:"last_run_id,omitempty"`
	LastSession string `firestore:"last_session,omitempty"`
	Repo        string `firestore:"repo,omitempty"` // discovered source repo (for jumbo routing)
	ParkReason  string `firestore:"park_reason,omitempty"`

	// Score is the package's priority specialized to this version, copied at
	// enqueue. Freshness is a per-version recency boost. Neither orders the
	// queue on its own, so they are stored apart and combined by DispatchOrder.
	Score     float64   `firestore:"score,omitempty"`
	Freshness float64   `firestore:"freshness,omitempty"`
	Published time.Time `firestore:"published,omitzero"`

	DispatchedAt time.Time `firestore:"dispatched_at,omitzero"`
	Updated      time.Time `firestore:"updated,omitzero"`
}

// DispatchOrder is the queue position of a target, highest first. Importance
// and recency multiply rather than add so that a stale version of a critical
// package and a fresh version of an unimportant one both stay ranked below a
// fresh version of a critical one.
func (t LadderTarget) DispatchOrder() float64 { return t.Score * t.Freshness }

func (t LadderTarget) Target() rebuild.Target {
	return rebuild.Target{
		Ecosystem: rebuild.Ecosystem(t.Ecosystem),
		Package:   t.Package,
		Version:   t.Version,
		Artifact:  t.Artifact,
	}
}

// Freshness returns the recency boost for a target: 1 + k*exp(-age/tau), so new
// releases spike and decay into the backlog. tauHours must be > 0.
func Freshness(published, now time.Time, k, tauHours float64) float64 {
	if published.IsZero() || tauHours <= 0 {
		return 1
	}
	age := now.Sub(published).Hours()
	if age < 0 {
		age = 0
	}
	return 1 + k*math.Exp(-age/tauHours)
}

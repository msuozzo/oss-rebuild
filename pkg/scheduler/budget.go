// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import "time"

// LadderBudget is the single per-period ledger for the rationed T2 tier.
// Builds (T0/T1) are not rationed given ample build throughput, only agent
// spend is. Document ID is BudgetDocID.
type LadderBudget struct {
	PeriodStart time.Time `firestore:"period_start,omitzero"`
	TokenCap    int64     `firestore:"token_cap,omitempty"`
	TokenSpent  int64     `firestore:"token_spent,omitempty"`
	IterCap     int       `firestore:"iter_cap,omitempty"`
	IterSpent   int       `firestore:"iter_spent,omitempty"`
	Updated     time.Time `firestore:"updated,omitzero"`
}

// BudgetDocID is the fixed key of the current budget period document.
const BudgetDocID = "current"

// TokenRemaining reports the tokens left in the current period (never negative).
func (b LadderBudget) TokenRemaining() int64 {
	if b.TokenCap <= 0 {
		return 0
	}
	if r := b.TokenCap - b.TokenSpent; r > 0 {
		return r
	}
	return 0
}

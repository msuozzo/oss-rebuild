// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

const ladderBudgetCollection = "scheduler_budget"

// LadderBudgets persists the per-period ledger for the rationed agent tier.
// There is exactly one document, so the key is ignored.
type LadderBudgets = Resource[scheduler.LadderBudget, string]

func ladderBudgetKey(string) []string {
	return []string{ladderBudgetCollection, scheduler.BudgetDocID}
}

func ladderBudgetPath(scheduler.LadderBudget) []string { return ladderBudgetKey(scheduler.BudgetDocID) }

// NewFirestoreLadderBudget returns the Firestore-backed per-period T2 ledger.
func NewFirestoreLadderBudget(c *firestore.Client) LadderBudgets {
	return &firestoreResource[scheduler.LadderBudget, string]{client: c, pathFor: ladderBudgetPath, pathForKey: ladderBudgetKey}
}

// NewMemoryLadderBudget returns an in-memory T2 ledger for tests.
func NewMemoryLadderBudget() LadderBudgets {
	return &memoryResource[scheduler.LadderBudget, string]{data: map[string]scheduler.LadderBudget{}, pathFor: ladderBudgetPath, pathForKey: ladderBudgetKey}
}

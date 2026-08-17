// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

const ladderTargetsCollection = "scheduler_targets"

// LadderTargets persists the queue state of each onboarding target.
type LadderTargets = Resource[scheduler.LadderTarget, rebuild.Target]

func ladderTargetKey(t rebuild.Target) []string {
	return []string{ladderTargetsCollection, scheduler.TargetID(t)}
}

func ladderTargetPath(t scheduler.LadderTarget) []string { return ladderTargetKey(t.Target()) }

// NewFirestoreLadderTargets returns a Firestore-backed queue-state store.
func NewFirestoreLadderTargets(c *firestore.Client) LadderTargets {
	return &firestoreResource[scheduler.LadderTarget, rebuild.Target]{client: c, pathFor: ladderTargetPath, pathForKey: ladderTargetKey}
}

// NewMemoryLadderTargets returns an in-memory queue-state store for tests.
func NewMemoryLadderTargets() LadderTargets {
	return &memoryResource[scheduler.LadderTarget, rebuild.Target]{data: map[string]scheduler.LadderTarget{}, pathFor: ladderTargetPath, pathForKey: ladderTargetKey}
}

// ListLadderTargets returns every queue-state document. The onboarded set is
// small enough that a full scan is acceptable. Readers sort and filter in
// memory, which keeps the collection free of composite indexes.
func ListLadderTargets(ctx context.Context, c *firestore.Client) ([]scheduler.LadderTarget, error) {
	return listCollection[scheduler.LadderTarget](ctx, c, ladderTargetsCollection)
}

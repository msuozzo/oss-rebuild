// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"google.golang.org/api/iterator"
)

const priorityCollection = "package_priority"

// PriorityKey identifies a package's materialized rebuild priority.
type PriorityKey struct{ Ecosystem, Package string }

// Priorities persists the fused per-package rebuild priority.
type Priorities = Resource[scheduler.Priority, PriorityKey]

func priorityKeyPath(k PriorityKey) []string {
	return []string{priorityCollection, scheduler.PackageID(k.Ecosystem, k.Package)}
}

func priorityPath(p scheduler.Priority) []string {
	return priorityKeyPath(PriorityKey{Ecosystem: p.Ecosystem, Package: p.Package})
}

// NewFirestorePriorities returns a Firestore-backed priority store.
func NewFirestorePriorities(c *firestore.Client) Priorities {
	return &firestoreResource[scheduler.Priority, PriorityKey]{client: c, pathFor: priorityPath, pathForKey: priorityKeyPath}
}

// NewMemoryPriorities returns an in-memory priority store for tests.
func NewMemoryPriorities() Priorities {
	return &memoryResource[scheduler.Priority, PriorityKey]{data: map[string]scheduler.Priority{}, pathFor: priorityPath, pathForKey: priorityKeyPath}
}

// ListPriorities returns every priority document. Callers rank and filter in
// memory: the collection is bounded by the per-ecosystem export cap, so a full
// scan avoids needing composite indexes.
func ListPriorities(ctx context.Context, c *firestore.Client) ([]scheduler.Priority, error) {
	return listCollection[scheduler.Priority](ctx, c, priorityCollection)
}

const versionCriticalityCollection = "version_criticality"

// VersionCriticalities persists per-version blast radius. It is keyed by
// target rather than by package, and is read one version at a time, so it is
// never listed whole.
type VersionCriticalities = Resource[scheduler.VersionCriticality, rebuild.Target]

func versionCriticalityKeyPath(t rebuild.Target) []string {
	return []string{versionCriticalityCollection, scheduler.TargetID(t)}
}

func versionCriticalityPath(v scheduler.VersionCriticality) []string {
	return versionCriticalityKeyPath(v.Target())
}

// NewFirestoreVersionCriticalities returns a Firestore-backed store.
func NewFirestoreVersionCriticalities(c *firestore.Client) VersionCriticalities {
	return &firestoreResource[scheduler.VersionCriticality, rebuild.Target]{client: c, pathFor: versionCriticalityPath, pathForKey: versionCriticalityKeyPath}
}

// NewMemoryVersionCriticalities returns an in-memory store for tests.
func NewMemoryVersionCriticalities() VersionCriticalities {
	return &memoryResource[scheduler.VersionCriticality, rebuild.Target]{data: map[string]scheduler.VersionCriticality{}, pathFor: versionCriticalityPath, pathForKey: versionCriticalityKeyPath}
}

func listCollection[T any](ctx context.Context, c *firestore.Client, collection string) ([]T, error) {
	it := c.Collection(collection).Documents(ctx)
	defer it.Stop()
	var out []T
	for {
		snap, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var v T
		if err := snap.DataTo(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

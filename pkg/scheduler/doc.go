// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scheduler holds the data model for onboarding packages into
// oss-rebuild coverage: which packages are worth rebuilding, in what order,
// and how much may be spent trying.
//
// Priority ranks packages. It is the materialized per-package score, fusing
// independent signals of package importance, each produced by its own offline
// job under `ctl onboard priority`, and each stored as a per-ecosystem quantile
// so that heavy tails and incomparable registry scales stay out of the
// arithmetic. Criticality is the graph signal: how many other packages depend
// on this one, measured both per package and per version. Prominence is the
// awareness signal: how well known the package is by name, which is what
// carries the leaf applications no dependency graph can rank.
//
// LadderTarget tracks one package version's progress. Targets climb tiers
// T0..T3, each more expensive than the last, ordered by DispatchOrder, which
// combines the score with recency so that importance and freshness both count.
// Tick advances one target given the outcome of its last attempt. Only the
// agent tier is rationed, against the per-period LadderBudget, because build
// throughput is ample but LLM spend is not.
//
// This package holds only pure types and functions. Persistence lives in
// internal/db and the jobs that drive it live in tools/ctl/command/onboard.
package scheduler

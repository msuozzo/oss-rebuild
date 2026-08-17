// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package scheduler holds the data model for rebuild prioritization: which
// packages are worth spending rebuild capacity on, and in what order.
//
// Priority is the materialized per-package score. It fuses independent
// signals of package importance, each produced by its own offline job under
// `ctl onboard priority`, and each stored as a per-ecosystem quantile so that
// heavy tails and incomparable registry scales stay out of the arithmetic.
// Criticality is the graph signal: how many other packages depend on this one.
//
// This package holds only pure types and functions. Persistence lives in
// internal/db and the jobs that populate it live in tools/ctl/command/onboard.
package scheduler

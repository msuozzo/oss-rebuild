// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// EcosystemSystem maps an oss-rebuild ecosystem to its deps.dev BigQuery
// `System` value (the enum used in bigquery-public-data.deps_dev_v1).
// Ecosystems absent here have no deps.dev dependency-graph coverage and so
// carry no criticality signal.
var EcosystemSystem = map[rebuild.Ecosystem]string{
	rebuild.NPM:      "NPM",
	rebuild.PyPI:     "PYPI",
	rebuild.CratesIO: "CARGO",
	rebuild.RubyGems: "RUBYGEMS",
	rebuild.Maven:    "MAVEN",
}

// CriticalityRecord is one row of a criticality export: the number of distinct
// packages that depend on this package, resolved against the latest deps.dev
// dependency-graph snapshot. Version is empty for package-level rows and set
// for version-level ones, which the same export carries together.
//
// This is deliberately not the OSSF Criticality Score, which scores repository
// activity (contributor counts, commit cadence, issue velocity). Criticality
// here measures blast radius only, because that is the one importance signal
// published uniformly across every ecosystem deps.dev covers. Download counts
// and repository statistics would serve a similar role but are not available
// through all registries.
type CriticalityRecord struct {
	Ecosystem  string `json:"ecosystem"`
	Package    string `json:"package"`
	Version    string `json:"version,omitempty"`
	Dependents int64  `json:"dependents"`
}

// VersionCriticality is the blast radius of one package version: how many
// distinct packages resolve to that exact version. Document ID is TargetID for
// (ecosystem, package, version, "").
//
// Version granularity matters because the most-depended-upon version is rarely
// the newest. Lockfiles pin, semver ranges settle on a stable release, and old
// majors keep their dependents for years. Ranking a package's versions by its
// package-level criticality would leave recency as the only differentiator,
// which is close to backwards for blast radius.
type VersionCriticality struct {
	Ecosystem  string    `firestore:"ecosystem,omitempty"`
	Package    string    `firestore:"package,omitempty"`
	Version    string    `firestore:"version,omitempty"`
	Dependents int64     `firestore:"dependents,omitempty"` // packages resolving to this version
	QCrit      float64   `firestore:"q_crit,omitempty"`     // per-ecosystem quantile of Dependents, in [0,1]
	Updated    time.Time `firestore:"updated,omitzero"`
}

func (v VersionCriticality) Target() rebuild.Target {
	return rebuild.Target{Ecosystem: rebuild.Ecosystem(v.Ecosystem), Package: v.Package, Version: v.Version}
}

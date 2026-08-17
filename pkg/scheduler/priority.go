// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import "time"

// Priority is the materialized rebuild priority of a package, the document the
// onboarding commands and the dashboard read. Document ID is PackageID.
//
// Each signal is stored both raw, so the score stays legible, and as a
// per-ecosystem quantile, which is what the score consumes. Score is
// materialized by Rescore, called by whichever `ctl onboard priority` job last
// touched the document, so readers never recompute it.
type Priority struct {
	Ecosystem  string    `firestore:"ecosystem,omitempty"`
	Package    string    `firestore:"package,omitempty"`
	Dependents int64     `firestore:"dependents,omitempty"` // distinct reverse-dependent packages
	QCrit      float64   `firestore:"q_crit,omitempty"`     // per-ecosystem quantile of Dependents, in [0,1]
	Score      float64   `firestore:"score,omitempty"`      // materialized by Rescore
	Band       string    `firestore:"band,omitempty"`       // per-ecosystem rank band
	Updated    time.Time `firestore:"updated,omitzero"`
}

// Rescore recomputes Score from the signals currently set on p.
func (p *Priority) Rescore() { p.Score = p.ScoreWith(p.QCrit) }

// ScoreWith is the score this package would carry if its criticality quantile
// were qCrit instead of its own. Scoring one version uses it to substitute that
// version's criticality while keeping the package's other signals, which are
// per-package and identical across versions.
func (p Priority) ScoreWith(qCrit float64) float64 { return qCrit }

// RankBand returns the coverage band for a 0-based descending rank within an
// ecosystem (0 = most important). Bands exist so coverage can be reported
// against the part of the ecosystem that matters most.
func RankBand(rank int) string {
	switch {
	case rank < 100:
		return "top100"
	case rank < 1000:
		return "top1k"
	default:
		return "longtail"
	}
}

// Percentile maps a 0-based descending rank within a population of n onto
// (0,1], where the top-ranked entry scores highest. n <= 0 yields 0.
func Percentile(rank, n int) float64 {
	if n <= 0 {
		return 0
	}
	return float64(n-rank) / float64(n)
}

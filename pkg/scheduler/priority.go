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
	P          float64   `firestore:"p,omitempty"`          // raw elicited prominence, in [0,1]
	QProm      float64   `firestore:"q_prom,omitempty"`     // per-ecosystem quantile of P, in [0,1]
	Model      string    `firestore:"model,omitempty"`      // prominence scoring model and horizon tag
	Score      float64   `firestore:"score,omitempty"`      // materialized by Rescore
	Band       string    `firestore:"band,omitempty"`       // per-ecosystem rank band
	Updated    time.Time `firestore:"updated,omitzero"`
}

// Rescore recomputes Score from the signals currently set on p.
func (p *Priority) Rescore() { p.Score = p.ScoreWith(p.QCrit) }

// ScoreWith is the score this package would carry if its criticality quantile
// were qCrit instead of its own: an equal-weight average of the two signals.
// Scoring one version uses it to substitute that version's criticality while
// keeping prominence, which is a per-package property and identical across
// versions.
//
// The two signals cover each other's blind spots rather than corroborating
// each other. Criticality sees blast radius, including plumbing nobody has
// heard of, and correctly sinks deprecated names that are still famous.
// Prominence sees public awareness, including the leaf applications that no
// dependency graph can rank. Equal weight is the neutral prior: there is no
// calibration data that would justify favoring one, and a missing signal
// contributes 0, so a package with only one scores at most half.
func (p Priority) ScoreWith(qCrit float64) float64 { return 0.5*qCrit + 0.5*p.QProm }

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

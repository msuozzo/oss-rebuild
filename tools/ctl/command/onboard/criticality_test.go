// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/scheduler"
)

func TestRankByEcosystem(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// npm's counts dwarf rubygems', so a global rank would put rack below every
	// npm package. Quantiles are what make the two comparable. Package and
	// version rows rank against their own populations, never against each other.
	prios, vers := rankByEcosystem([]scheduler.CriticalityRecord{
		{Ecosystem: "npm", Package: "rarely-used", Dependents: 1},
		{Ecosystem: "rubygems", Package: "rack", Dependents: 50},
		{Ecosystem: "npm", Package: "lodash", Dependents: 9000},
		{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Dependents: 8000},
		{Ecosystem: "npm", Package: "lodash", Version: "3.10.1", Dependents: 400},
		{Ecosystem: "", Package: "dropped-no-ecosystem", Dependents: 5},
		{Ecosystem: "npm", Package: "", Dependents: 5},
	}, now)

	wantPrios := []scheduler.Priority{
		{Ecosystem: "npm", Package: "lodash", Dependents: 9000, QCrit: 1.0, Band: "top100", Updated: now},
		{Ecosystem: "npm", Package: "rarely-used", Dependents: 1, QCrit: 0.5, Band: "top100", Updated: now},
		{Ecosystem: "rubygems", Package: "rack", Dependents: 50, QCrit: 1.0, Band: "top100", Updated: now},
	}
	if diff := cmp.Diff(wantPrios, prios); diff != "" {
		t.Errorf("package priorities mismatch (-want +got):\n%s", diff)
	}
	wantVers := []scheduler.VersionCriticality{
		{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Dependents: 8000, QCrit: 1.0, Updated: now},
		{Ecosystem: "npm", Package: "lodash", Version: "3.10.1", Dependents: 400, QCrit: 0.5, Updated: now},
	}
	if diff := cmp.Diff(wantVers, vers); diff != "" {
		t.Errorf("version criticalities mismatch (-want +got):\n%s", diff)
	}
}

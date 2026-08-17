// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"math"
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	rows := []testRow{
		{Name: "lodash", Ecosystem: "npm", Category: "prominent-real", ExpectedHigh: true},
		{Name: "react", Ecosystem: "npm", Category: "prominent-real", ExpectedHigh: true},
		{Name: "left-pad-ish", Ecosystem: "npm", Category: "obscure-real"},
		{Name: "totally-made-up", Ecosystem: "npm", Category: "nonexistent"},
		{Name: "wheel", Ecosystem: "pypi", Category: "word-confounder", ExpectedHigh: true},
		{Name: "spatula", Ecosystem: "pypi", Category: "word-confounder"},
	}
	perfect := map[pkgRef]float64{
		{"npm", "lodash"}: 1.0, {"npm", "react"}: 0.9,
		{"npm", "left-pad-ish"}: 0.2, {"npm", "totally-made-up"}: 0.0,
		{"pypi", "wheel"}: 0.8, {"pypi", "spatula"}: 0.1,
	}
	res := evaluate(rows, perfect)
	if res.AUC != 1.0 {
		t.Errorf("AUC = %v, want 1 on a perfectly ordered set", res.AUC)
	}
	if res.FPR != 0 {
		t.Errorf("FPR = %v, want 0", res.FPR)
	}
	if res.Disambig != 1.0 {
		t.Errorf("Disambig = %v, want 1", res.Disambig)
	}
	if !res.Pass() {
		t.Error("a perfectly ordered set should pass the gate")
	}

	// A fake scoring as high as the famous packages must trip the FPR gate
	// even though it barely moves the main AUC.
	hallucinating := map[pkgRef]float64{}
	for k, v := range perfect {
		hallucinating[k] = v
	}
	hallucinating[pkgRef{"npm", "totally-made-up"}] = 1.0
	if res := evaluate(rows, hallucinating); res.FPR != 1.0 || res.Pass() {
		t.Errorf("a fake scored famous should fail the gate: FPR = %v, pass = %v", res.FPR, res.Pass())
	}

	// An unscored package reads as obscure rather than poisoning the metrics.
	unscored := map[pkgRef]float64{{"npm", "lodash"}: math.NaN()}
	if got := evaluate(rows, unscored); math.IsNaN(got.AUC) {
		t.Error("NaN scores should read as 0, not propagate into the AUC")
	}
}

func TestParseTestsetSkipsCommentsAndHeader(t *testing.T) {
	rows, err := parseTestset(strings.NewReader(
		"name,ecosystem,category,expected_high,notes\n" +
			"# a comment\n" +
			"\n" +
			"lodash,npm,prominent-real,TRUE,famous\n" +
			"spatula,pypi,word-confounder,false,\n" +
			"truncated,row\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if !rows[0].ExpectedHigh || rows[1].ExpectedHigh {
		t.Errorf("expected_high parsed wrong: %+v", rows)
	}
}

func TestEmbeddedTestsetParses(t *testing.T) {
	rows, err := parseTestset(strings.NewReader(defaultTestset))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 50 {
		t.Fatalf("embedded testset has only %d rows", len(rows))
	}
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Category]++
	}
	for _, c := range []string{"prominent-real", "obscure-real", "nonexistent", "word-confounder"} {
		if seen[c] == 0 {
			t.Errorf("embedded testset has no %q rows, so that gate would be vacuous", c)
		}
	}
}

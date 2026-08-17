// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"bufio"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// defaultTestset is the held-out confounder set: famous packages, long-tail
// real ones, names that collide with common English words, and names that do
// not exist at all. Embedded so the gate runs from any working directory.
//
//go:embed testdata/confounder_testset.csv
var defaultTestset string

// Gate thresholds. The main AUC is the metric of record for a rebuild queue:
// it asks whether the signal orders packages correctly at all. The other two
// are trust-oriented, defending against a fake name scoring famous, which only
// matters if the score ever gates something an attacker wants. A fake reaches
// the scorer only if injected into the corpus, and a real registry export
// never contains one, so the worst case here is a rebuild that fails to
// resolve. They are enforced anyway, because a regression in either is
// evidence the rubric has drifted.
const (
	minAUC      = 0.90
	maxFPR      = 0.02
	minDisambig = 0.95
)

// testRow is one labeled confounder.
type testRow struct {
	Name         string
	Ecosystem    string
	Category     string
	ExpectedHigh bool
}

func parseTestset(r io.Reader) ([]testRow, error) {
	var rows []testRow
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "name,") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		rows = append(rows, testRow{
			Name:         parts[0],
			Ecosystem:    parts[1],
			Category:     parts[2],
			ExpectedHigh: strings.EqualFold(strings.TrimSpace(parts[3]), "TRUE"),
		})
	}
	return rows, sc.Err()
}

// auc is ROC-AUC via a tie-aware Mann-Whitney. The testset is ~100 rows, so
// the quadratic form is fine and avoids a sort. NaN if either class is empty.
func auc(scores, labels []float64) float64 {
	var pos, neg []float64
	for i, s := range scores {
		if labels[i] == 1 {
			pos = append(pos, s)
		} else {
			neg = append(neg, s)
		}
	}
	if len(pos) == 0 || len(neg) == 0 {
		return math.NaN()
	}
	var wins float64
	for _, a := range pos {
		for _, b := range neg {
			switch {
			case a > b:
				wins++
			case a == b:
				wins += 0.5
			}
		}
	}
	return wins / float64(len(pos)*len(neg))
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// evalResult holds the three gate metrics.
type evalResult struct {
	AUC       float64
	FPR       float64
	Disambig  float64
	Threshold float64
}

func (r evalResult) Pass() bool {
	return r.AUC >= minAUC && r.FPR <= maxFPR && (r.Disambig >= minDisambig || math.IsNaN(r.Disambig))
}

// evaluate scores the testset. A failed elicitation reads as obscure here,
// which is the conservative direction for every metric.
func evaluate(rows []testRow, score map[pkgRef]float64) evalResult {
	s := func(r testRow) float64 {
		if v := score[pkgRef{r.Ecosystem, r.Name}]; !math.IsNaN(v) {
			return v
		}
		return 0
	}
	var mainS, mainL, prom, non, wcS, wcL []float64
	for _, r := range rows {
		switch r.Category {
		case "prominent-real":
			mainS, mainL = append(mainS, s(r)), append(mainL, 1)
			prom = append(prom, s(r))
		case "obscure-real":
			mainS, mainL = append(mainS, s(r)), append(mainL, 0)
		case "nonexistent":
			mainS, mainL = append(mainS, s(r)), append(mainL, 0)
			non = append(non, s(r))
		case "word-confounder":
			wcS = append(wcS, s(r))
			if r.ExpectedHigh {
				wcL = append(wcL, 1)
			} else {
				wcL = append(wcL, 0)
			}
		}
	}
	out := evalResult{AUC: auc(mainS, mainL), FPR: math.NaN(), Disambig: math.NaN(), Threshold: median(prom)}
	if len(non) > 0 {
		var over int
		for _, x := range non {
			if x >= out.Threshold {
				over++
			}
		}
		out.FPR = float64(over) / float64(len(non))
	}
	if len(wcS) > 0 {
		out.Disambig = auc(wcS, wcL)
	}
	return out
}

type evalConfig struct {
	Project  string
	Location string
	Model    string
	Testset  string
	Batch    int
	Dump     bool
}

func (c evalConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	return nil
}

func evalHandler(ctx context.Context, cfg evalConfig, deps *Deps) (*act.NoOutput, error) {
	raw := defaultTestset
	if cfg.Testset != "" {
		b, err := os.ReadFile(cfg.Testset)
		if err != nil {
			return nil, errors.Wrap(err, "reading testset")
		}
		raw = string(b)
	}
	rows, err := parseTestset(strings.NewReader(raw))
	if err != nil {
		return nil, errors.Wrap(err, "parsing testset")
	}
	client, err := newGenAIClient(ctx, cfg.Project, cfg.Location)
	if err != nil {
		return nil, err
	}
	items := make([]pkgRef, len(rows))
	for i, r := range rows {
		items[i] = pkgRef{r.Ecosystem, r.Name}
	}
	ps := elicit(ctx, client, cfg.Model, prominenceGenConfig(), items, cfg.Batch, deps.IO.Err)
	score := map[pkgRef]float64{}
	for i, r := range rows {
		score[pkgRef{r.Ecosystem, r.Name}] = ps[i]
	}
	res := evaluate(rows, score)
	if cfg.Dump {
		dumpScores(rows, score, res.Threshold, deps.IO.Out)
	}
	out := deps.IO.Out
	fmt.Fprintf(out, "\nAUC (prominent vs obscure and nonexistent): %.3f   (min %.2f)\n", res.AUC, minAUC)
	fmt.Fprintf(out, "nonexistent FPR at median-prominent thresh: %.3f   (max %.2f)\n", res.FPR, maxFPR)
	fmt.Fprintf(out, "word-confounder disambiguation AUC        : %.3f   (min %.2f)\n\n", res.Disambig, minDisambig)
	if !res.Pass() {
		return nil, errors.New("prominence eval did not meet the thresholds")
	}
	fmt.Fprintln(out, "eval PASSED")
	return &act.NoOutput{}, nil
}

// dumpScores prints every row ranked by score, flagging the ones responsible
// for a failed gate: a fake that scored famous, or a word confounder ranked
// against its expected direction.
func dumpScores(rows []testRow, score map[pkgRef]float64, threshold float64, out io.Writer) {
	sorted := append([]testRow(nil), rows...)
	sort.SliceStable(sorted, func(a, b int) bool {
		return score[pkgRef{sorted[a].Ecosystem, sorted[a].Name}] > score[pkgRef{sorted[b].Ecosystem, sorted[b].Name}]
	})
	fmt.Fprintf(out, "\nper-item scores (median-prominent threshold = %.2f):\n", threshold)
	fmt.Fprintf(out, "%-5s  %-9s  %-24s  %-16s  %s\n", "p", "eco", "name", "category", "flag")
	for _, r := range sorted {
		p := score[pkgRef{r.Ecosystem, r.Name}]
		var flag string
		switch {
		case r.Category == "nonexistent" && p >= threshold:
			flag = "<< fake scored famous"
		case r.Category == "word-confounder" && !r.ExpectedHigh && p >= threshold:
			flag = "<< word over-ranked"
		case r.Category == "word-confounder" && r.ExpectedHigh && p < threshold:
			flag = "<< real word-package under-ranked"
		case r.Category == "prominent-real" && p < threshold:
			flag = "(prominent below median)"
		}
		fmt.Fprintf(out, "%-5.2f  %-9s  %-24s  %-16s  %s\n", p, r.Ecosystem, r.Name, r.Category, flag)
	}
	fmt.Fprintln(out)
}

func evalCommand() *cobra.Command {
	cfg := evalConfig{}
	cmd := &cobra.Command{
		Use:   "eval --project <project> [--testset <file>] [--dump]",
		Short: "Validate the prominence scorer against the held-out confounder set",
		Long: `Validate the prominence scorer against the held-out confounder set.

Scores a labeled set of famous packages, long-tail real ones, names that
collide with common English words, and names that do not exist, then checks
three metrics. Run this after changing the rubric or the model.

The main AUC is the metric of record: it asks whether the signal orders
packages correctly. The false-positive and disambiguation gates defend against
a fake name scoring famous, which matters far less for a rebuild queue than it
would if the score gated trust, but a regression in either is evidence the
rubric has drifted.`,
		Args: cobra.NoArgs,
		RunE: cli.RunE(&cfg, cli.SkipArgs[evalConfig], InitDeps, evalHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project for Vertex AI")
	set.StringVar(&cfg.Location, "location", "global", "Vertex AI location")
	set.StringVar(&cfg.Model, "model", llm.GeminiFlash, "Gemini model id served via Vertex")
	set.StringVar(&cfg.Testset, "testset", "", "path to a labeled confounder CSV (default: the embedded set)")
	set.IntVar(&cfg.Batch, "batch", 15, "packages per model call")
	set.BoolVar(&cfg.Dump, "dump", false, "print per-item scores, flagging the rows responsible for a failed gate")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

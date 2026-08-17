// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/genai"
)

// pkgRef identifies a package within an ecosystem. Comparable, so it doubles
// as a map key.
type pkgRef struct{ eco, pkg string }

// ecoName maps an ecosystem id (ours, plus deps.dev's cargo alias) to the
// English name used in the prompt, since the model reasons about "a Rust
// crate" far better than about "cratesio". Unknown ids pass through.
var ecoName = map[string]string{
	"pypi": "Python", "npm": "JavaScript", "cratesio": "Rust", "cargo": "Rust",
	"rubygems": "Ruby", "maven": "Java", "go": "Go",
}

func ecoEnglish(eco string) string {
	if n, ok := ecoName[strings.ToLower(eco)]; ok {
		return n
	}
	return eco
}

// prominenceRubric anchors each integer to a decade of popularity rank.
// Recognition is log-scaled in reality (each decade holds about ten times more
// packages), so asking for a rank decade gets a far better calibrated answer
// than asking for a 0-100 score, and it makes the model's uncertainty legible:
// a name it cannot place lands at 5, and a name it does not believe exists
// lands at 0.
const prominenceRubric = "Rate how prominent each SOFTWARE PACKAGE is within its own ecosystem's registry, " +
	"judged strictly as the package published under that EXACT name -- not as an English word, brand, " +
	"famous person, or unrelated topic. Estimate the package's popularity RANK in its registry (rank 1 = " +
	"the single most-installed / most-depended-upon package) and map that rank to an integer 0-10 on this " +
	"log scale:\n" +
	"  10 = top ~10 packages in the ecosystem (the handful nearly everything depends on)\n" +
	"   9 = roughly top 100\n" +
	"   8 = roughly top 1,000\n" +
	"   7 = roughly top 10,000\n" +
	"   6 = roughly top 100,000\n" +
	"   5 = a real but long-tail package you can place only vaguely\n" +
	"   1-2 = you have only a faint sense a package by that exact name exists\n" +
	"   0 = you do not believe any package exists under that EXACT name\n" +
	"Each step down is about 10x more packages, so most of the registry sits at 5 or below. Do not inflate " +
	"a package for sharing a common word or a famous topic; a plausible-but-unfamiliar name is 0-2, not high."

// prominenceSchema constrains the model to a JSON array of {prominence:int},
// one per item, so a malformed reply fails loudly instead of being parsed into
// a plausible wrong number.
var prominenceSchema = &genai.Schema{
	Type: genai.TypeArray,
	Items: &genai.Schema{
		Type:       genai.TypeObject,
		Properties: map[string]*genai.Schema{"prominence": {Type: genai.TypeInteger}},
		Required:   []string{"prominence"},
	},
}

type ratingItem struct {
	Prominence float64 `json:"prominence"`
}

func newGenAIClient(ctx context.Context, project, location string) (*genai.Client, error) {
	if location == "" {
		location = "global"
	}
	c, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  project,
		Location: location,
	})
	return c, errors.Wrap(err, "creating genai client")
}

func prominenceGenConfig() *genai.GenerateContentConfig {
	return &genai.GenerateContentConfig{
		Temperature:      genai.Ptr(float32(0)),
		ResponseMIMEType: llm.JSONMIMEType,
		ResponseSchema:   prominenceSchema,
	}
}

func promptFor(chunk []pkgRef) string {
	var b strings.Builder
	b.WriteString(prominenceRubric)
	b.WriteString("\n\nReturn ONLY a JSON array, one object per item in order, each {\"prominence\": int}. Items:\n")
	for i, r := range chunk {
		fmt.Fprintf(&b, "%d. name=%q ecosystem=%s\n", i+1, r.pkg, ecoEnglish(r.eco))
	}
	return b.String()
}

// elicitBatch scores one chunk, returning p in [0,1] per item or NaN for a
// missing or malformed entry.
func elicitBatch(ctx context.Context, client *genai.Client, model string, config *genai.GenerateContentConfig, chunk []pkgRef) ([]float64, error) {
	var arr []ratingItem
	if err := llm.GenerateTypedContent(ctx, client, model, config, &arr, genai.NewPartFromText(promptFor(chunk))); err != nil {
		return nil, err
	}
	out := make([]float64, len(chunk))
	for i := range out {
		if i >= len(arr) {
			out[i] = math.NaN()
			continue
		}
		out[i] = math.Max(0, math.Min(10, arr[i].Prominence)) / 10
	}
	return out, nil
}

// elicit scores every item, batch by batch. A failed batch leaves its items
// NaN rather than 0, because a transient error must not brand a real package
// as maximally obscure and skew the quantiles for everything else.
func elicit(ctx context.Context, client *genai.Client, model string, config *genai.GenerateContentConfig, items []pkgRef, batch int, errw io.Writer) []float64 {
	if batch <= 0 {
		batch = 15
	}
	out := make([]float64, len(items))
	for i := range out {
		out[i] = math.NaN()
	}
	for i := 0; i < len(items); i += batch {
		end := min(i+batch, len(items))
		ps, err := elicitBatch(ctx, client, model, config, items[i:end])
		if err != nil {
			fmt.Fprintf(errw, "[%s] batch %d: %v\n", model, i, err)
			continue
		}
		copy(out[i:end], ps)
		fmt.Fprintf(errw, "  %s %d/%d\r", model, end, len(items))
	}
	fmt.Fprintln(errw)
	return out
}

func cacheKey(model string, r pkgRef) string { return model + "|" + r.eco + "|" + r.pkg }

// corpusRecord is one row of the scoring corpus. A criticality export works.
// The date fields feed the horizon rule, and ISO dates sort lexically, so we
// keep the leading 10 characters and compare as strings.
type corpusRecord struct {
	Ecosystem      string `json:"ecosystem"`
	Package        string `json:"package"`
	Registered     string `json:"registered"`
	Created        string `json:"created"`
	Published      string `json:"published"`
	FirstPublished string `json:"first_published"`
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func isoDate(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func loadCorpus(path string) ([]pkgRef, map[pkgRef]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, errors.Wrap(err, "reading corpus")
	}
	var recs []corpusRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		return nil, nil, errors.Wrap(err, "parsing corpus")
	}
	seen := map[pkgRef]bool{}
	var pairs []pkgRef
	registered := map[pkgRef]string{}
	for _, r := range recs {
		if r.Ecosystem == "" || r.Package == "" {
			continue
		}
		k := pkgRef{r.Ecosystem, r.Package}
		if seen[k] {
			continue
		}
		seen[k] = true
		pairs = append(pairs, k)
		if d := firstNonEmpty(r.Registered, r.Created, r.Published, r.FirstPublished); d != "" {
			registered[k] = isoDate(d)
		}
	}
	return pairs, registered, nil
}

func loadCache(path string) (map[string]float64, error) {
	m := map[string]float64{}
	if path == "" {
		return m, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) || len(raw) == 0 {
		return m, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "reading cache")
	}
	return m, errors.Wrap(json.Unmarshal(raw, &m), "parsing cache")
}

func saveCache(path string, m map[string]float64) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return errors.Wrap(os.WriteFile(path, b, 0o644), "writing cache")
}

// ---------------------------------------------------------------------------
// prominence
// ---------------------------------------------------------------------------

type prominenceConfig struct {
	Project  string
	Location string
	Model    string
	Corpus   string
	File     string
	Out      string
	Cache    string
	Horizon  string
	Batch    int
	Load     bool
}

func (c prominenceConfig) Validate() error {
	if c.Corpus == "" && c.File == "" {
		return errors.New("one of corpus or file is required")
	}
	if c.Corpus != "" && c.File != "" {
		return errors.New("corpus and file are mutually exclusive")
	}
	if c.Out == "" && !c.Load {
		return errors.New("at least one of out or load is required")
	}
	if c.Project == "" {
		return errors.New("project is required")
	}
	return nil
}

func prominenceHandler(ctx context.Context, cfg prominenceConfig, deps *Deps) (*act.NoOutput, error) {
	recs, model, err := prominenceRecords(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	if cfg.Out != "" {
		data, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			return nil, errors.Wrap(err, "marshaling records")
		}
		if err := os.WriteFile(cfg.Out, append(data, '\n'), 0o644); err != nil {
			return nil, errors.Wrapf(err, "writing %s", cfg.Out)
		}
		fmt.Fprintf(deps.IO.Out, "wrote %d prominence record(s) to %s\n", len(recs), cfg.Out)
	}
	if cfg.Load {
		fire, err := firestore.NewClient(ctx, cfg.Project)
		if err != nil {
			return nil, errors.Wrap(err, "creating firestore client")
		}
		defer fire.Close()
		store := db.NewFirestorePriorities(fire)
		now := time.Now().UTC()
		var loaded int
		for _, r := range rankProminence(recs) {
			err := upsertMerged(ctx, store, r.Ecosystem, r.Package, now, func(cur *scheduler.Priority) {
				cur.P, cur.QProm, cur.Model = r.P, r.QProm, model
			})
			if err != nil {
				fmt.Fprintf(deps.IO.Err, "load %s/%s: %v\n", r.Ecosystem, r.Package, err)
				continue
			}
			loaded++
		}
		fmt.Fprintf(deps.IO.Out, "loaded %d of %d priority document(s)\n", loaded, len(recs))
	}
	return &act.NoOutput{}, nil
}

// prominenceRecords produces the records to write, either by reading a
// previously scored export or by scoring the corpus. It also returns the model
// tag to stamp on each document.
func prominenceRecords(ctx context.Context, cfg prominenceConfig, deps *Deps) ([]scheduler.ProminenceRecord, string, error) {
	if cfg.File != "" {
		raw, err := os.ReadFile(cfg.File)
		if err != nil {
			return nil, "", errors.Wrap(err, "reading prominence file")
		}
		var recs []scheduler.ProminenceRecord
		if err := json.Unmarshal(raw, &recs); err != nil {
			return nil, "", errors.Wrap(err, "parsing prominence file")
		}
		return recs, cfg.Model, nil
	}
	pairs, registered, err := loadCorpus(cfg.Corpus)
	if err != nil {
		return nil, "", err
	}
	// Horizon rule: a package registered after H cannot be something the model
	// knows, so floor it rather than trust a score inherited from a similar
	// name. Floored packages skip the API entirely, and the floor is recomputed
	// every run rather than cached, so advancing H never leaves one behind.
	horizon := isoDate(cfg.Horizon)
	floored := map[pkgRef]bool{}
	if horizon != "" {
		for _, r := range pairs {
			if d, ok := registered[r]; ok && d > horizon {
				floored[r] = true
			}
		}
	}
	cache, err := loadCache(cfg.Cache)
	if err != nil {
		return nil, "", err
	}
	var todo []pkgRef
	for _, r := range pairs {
		if _, cached := cache[cacheKey(cfg.Model, r)]; !floored[r] && !cached {
			todo = append(todo, r)
		}
	}
	fmt.Fprintf(deps.IO.Err, "%d package(s), %d post-horizon (floored), %d to score with %s\n",
		len(pairs), len(floored), len(todo), cfg.Model)
	if len(todo) > 0 {
		client, err := newGenAIClient(ctx, cfg.Project, cfg.Location)
		if err != nil {
			return nil, "", err
		}
		ps := elicit(ctx, client, cfg.Model, prominenceGenConfig(), todo, cfg.Batch, deps.IO.Err)
		for i, r := range todo {
			// Cache only real scores. A failed batch stays uncached and is
			// retried on the next run.
			if !math.IsNaN(ps[i]) {
				cache[cacheKey(cfg.Model, r)] = ps[i]
			}
		}
		if err := saveCache(cfg.Cache, cache); err != nil {
			return nil, "", err
		}
	}
	var recs []scheduler.ProminenceRecord
	for _, r := range pairs {
		switch v, ok := cache[cacheKey(cfg.Model, r)]; {
		case floored[r]:
			recs = append(recs, scheduler.ProminenceRecord{Ecosystem: r.eco, Package: r.pkg, P: 0})
		case ok:
			recs = append(recs, scheduler.ProminenceRecord{Ecosystem: r.eco, Package: r.pkg, P: v})
		}
	}
	if n := len(pairs) - len(recs); n > 0 {
		fmt.Fprintf(deps.IO.Err, "%d package(s) left unscored by failed batches, omitted\n", n)
	}
	model := cfg.Model
	if horizon != "" {
		model = cfg.Model + "@H" + horizon
	}
	return recs, model, nil
}

// rankedProminence is a prominence record with its per-ecosystem quantile.
type rankedProminence struct {
	scheduler.ProminenceRecord
	QProm float64
}

// rankProminence orders records within each ecosystem by descending p and
// attaches the resulting quantile, which is what the score consumes. Packages
// absent from the export keep whatever quantile they already had, or none.
func rankProminence(recs []scheduler.ProminenceRecord) []rankedProminence {
	byEco := map[string][]scheduler.ProminenceRecord{}
	for _, r := range recs {
		if r.Ecosystem != "" && r.Package != "" {
			byEco[r.Ecosystem] = append(byEco[r.Ecosystem], r)
		}
	}
	ecos := make([]string, 0, len(byEco))
	for eco := range byEco {
		ecos = append(ecos, eco)
	}
	sort.Strings(ecos)
	var out []rankedProminence
	for _, eco := range ecos {
		ranked := byEco[eco]
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].P > ranked[j].P })
		for i, r := range ranked {
			out = append(out, rankedProminence{ProminenceRecord: r, QProm: scheduler.Percentile(i, len(ranked))})
		}
	}
	return out
}

func prominenceCommand() *cobra.Command {
	cfg := prominenceConfig{}
	cmd := &cobra.Command{
		Use:   "prominence --project <project> (--corpus <file> | --file <file>) [--out <file>] [--load]",
		Short: "Rank packages by LLM-elicited public notability (leaf and application coverage)",
		Long: `Rank packages by LLM-elicited public notability.

Asks Gemini once per package, under a decade-anchored log-rank rubric, how well
known the package is under that exact name, then ranks the answers into
per-ecosystem quantiles.

This exists because criticality is structurally blind to leaves: an application
has no reverse-dependents no matter how widely it is installed. Download counts
and repository statistics would cover the same gap but are not published
uniformly across registries.

Packages registered after the model's knowledge horizon are floored to zero and
ride the criticality term instead, since the model cannot know an artifact
published after its training data. Re-measure the horizon per model generation.

Scoring is cached by model and package, so reruns only pay for new packages.`,
		Args: cobra.NoArgs,
		RunE: cli.RunE(&cfg, cli.SkipArgs[prominenceConfig], InitDeps, prominenceHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project for Vertex AI, and for Firestore when loading")
	set.StringVar(&cfg.Location, "location", "global", "Vertex AI location")
	set.StringVar(&cfg.Model, "model", llm.GeminiFlash, "Gemini model id served via Vertex")
	set.StringVar(&cfg.Corpus, "corpus", "", "JSON array of {ecosystem, package, [registered]} to score; a criticality export works")
	set.StringVar(&cfg.File, "file", "", "load an already-scored export instead of calling the model")
	set.StringVar(&cfg.Out, "out", "", "write the scored JSON export to this file")
	set.StringVar(&cfg.Cache, "cache", "", "JSON score cache keyed by model, ecosystem, and package")
	set.StringVar(&cfg.Horizon, "horizon", "", "knowledge-horizon date YYYY-MM-DD; packages registered after it are floored")
	set.IntVar(&cfg.Batch, "batch", 15, "packages per model call")
	set.BoolVar(&cfg.Load, "load", false, "load the ranked result into Firestore")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

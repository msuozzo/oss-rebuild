// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type criticalityConfig struct {
	Project        string
	BillingProject string
	Ecosystems     string
	Top            int
	TopVersions    int
	Out            string
	Load           bool
}

func (c criticalityConfig) Validate() error {
	if c.Out == "" && !c.Load {
		return errors.New("at least one of out or load is required")
	}
	if c.Load && c.Project == "" {
		return errors.New("project is required with load")
	}
	if c.billingProject() == "" {
		return errors.New("billing-project is required when project is unset")
	}
	ecos := c.ecosystems()
	if len(ecos) == 0 {
		return errors.New("at least one ecosystem is required")
	}
	for _, name := range ecos {
		if _, ok := scheduler.EcosystemSystem[rebuild.Ecosystem(name)]; !ok {
			return errors.Errorf("ecosystem %q has no deps.dev dependency-graph coverage", name)
		}
	}
	return nil
}

// billingProject is the project that runs and is billed for the BigQuery jobs.
// deps.dev is public data, so this need not be the project holding our own
// Firestore data, but defaulting to it keeps the common case to one flag.
func (c criticalityConfig) billingProject() string {
	if c.BillingProject != "" {
		return c.BillingProject
	}
	return c.Project
}

func (c criticalityConfig) ecosystems() []string {
	var out []string
	for _, name := range strings.Split(c.Ecosystems, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func criticalityHandler(ctx context.Context, cfg criticalityConfig, deps *Deps) (*act.NoOutput, error) {
	client, err := bigquery.NewClient(ctx, cfg.billingProject(), option.WithQuotaProject(cfg.billingProject()))
	if err != nil {
		return nil, errors.Wrap(err, "creating bigquery client")
	}
	defer client.Close()
	snap, err := latestSnapshot(ctx, client)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(deps.IO.Err, "deps.dev snapshot %s\n", snap.Format(time.RFC3339))
	var recs []scheduler.CriticalityRecord
	for _, name := range cfg.ecosystems() {
		system := scheduler.EcosystemSystem[rebuild.Ecosystem(name)]
		pkgs, err := packageCriticality(ctx, client, name, system, snap, cfg.Top)
		if err != nil {
			return nil, errors.Wrapf(err, "querying %s package criticality", name)
		}
		vers, err := versionCriticality(ctx, client, name, system, snap, cfg.TopVersions)
		if err != nil {
			return nil, errors.Wrapf(err, "querying %s version criticality", name)
		}
		fmt.Fprintf(deps.IO.Err, "[%s] %d package(s), %d version(s)\n", name, len(pkgs), len(vers))
		recs = append(recs, pkgs...)
		recs = append(recs, vers...)
	}
	if cfg.Out != "" {
		data, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			return nil, errors.Wrap(err, "marshaling records")
		}
		if err := os.WriteFile(cfg.Out, append(data, '\n'), 0o644); err != nil {
			return nil, errors.Wrapf(err, "writing %s", cfg.Out)
		}
		fmt.Fprintf(deps.IO.Out, "wrote %d criticality record(s) to %s\n", len(recs), cfg.Out)
	}
	if cfg.Load {
		fire, err := firestore.NewClient(ctx, cfg.Project)
		if err != nil {
			return nil, errors.Wrap(err, "creating firestore client")
		}
		defer fire.Close()
		now := time.Now().UTC()
		prios, vers := rankByEcosystem(recs, now)
		priorities := db.NewFirestorePriorities(fire)
		var loaded int
		for _, p := range prios {
			if err := priorities.Upsert(ctx, p); err != nil {
				fmt.Fprintf(deps.IO.Err, "load %s/%s: %v\n", p.Ecosystem, p.Package, err)
				continue
			}
			loaded++
		}
		fmt.Fprintf(deps.IO.Out, "loaded %d of %d priority document(s)\n", loaded, len(prios))
		criticalities := db.NewFirestoreVersionCriticalities(fire)
		loaded = 0
		for _, v := range vers {
			if err := criticalities.Upsert(ctx, v); err != nil {
				fmt.Fprintf(deps.IO.Err, "load %s/%s@%s: %v\n", v.Ecosystem, v.Package, v.Version, err)
				continue
			}
			loaded++
		}
		fmt.Fprintf(deps.IO.Out, "loaded %d of %d version criticality document(s)\n", loaded, len(vers))
	}
	return &act.NoOutput{}, nil
}

// rankByEcosystem ranks each granularity within its ecosystem by descending
// dependent count. Ranking is per-ecosystem because raw counts are not
// comparable across registries: npm's graph is denser than RubyGems' by an
// order of magnitude, so a global rank would bury every gem below the npm long
// tail. Package and version rows are ranked against their own populations, so
// a version quantile says "busy for a version of anything in this ecosystem".
//
// Neither result carries a score. Only the loaders can set one, since they
// alone see the package's other signals.
func rankByEcosystem(recs []scheduler.CriticalityRecord, now time.Time) ([]scheduler.Priority, []scheduler.VersionCriticality) {
	pkgsByEco := map[string][]scheduler.CriticalityRecord{}
	versByEco := map[string][]scheduler.CriticalityRecord{}
	for _, r := range recs {
		if r.Ecosystem == "" || r.Package == "" {
			continue
		}
		if r.Version == "" {
			pkgsByEco[r.Ecosystem] = append(pkgsByEco[r.Ecosystem], r)
		} else {
			versByEco[r.Ecosystem] = append(versByEco[r.Ecosystem], r)
		}
	}
	var prios []scheduler.Priority
	for _, eco := range sortedKeys(pkgsByEco) {
		ranked := byDependents(pkgsByEco[eco])
		for i, r := range ranked {
			prios = append(prios, scheduler.Priority{
				Ecosystem:  eco,
				Package:    r.Package,
				Dependents: r.Dependents,
				QCrit:      scheduler.Percentile(i, len(ranked)),
				Band:       scheduler.RankBand(i),
				Updated:    now,
			})
		}
	}
	var vers []scheduler.VersionCriticality
	for _, eco := range sortedKeys(versByEco) {
		ranked := byDependents(versByEco[eco])
		for i, r := range ranked {
			vers = append(vers, scheduler.VersionCriticality{
				Ecosystem:  eco,
				Package:    r.Package,
				Version:    r.Version,
				Dependents: r.Dependents,
				QCrit:      scheduler.Percentile(i, len(ranked)),
				Updated:    now,
			})
		}
	}
	return prios, vers
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func byDependents(recs []scheduler.CriticalityRecord) []scheduler.CriticalityRecord {
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Dependents > recs[j].Dependents })
	return recs
}

const depsDevDataset = "`bigquery-public-data.deps_dev_v1"

// latestSnapshot reads the most recent deps.dev snapshot time. Pinning queries
// to it as a TIMESTAMP parameter lets BigQuery prune to a single partition.
func latestSnapshot(ctx context.Context, client *bigquery.Client) (time.Time, error) {
	it, err := runQuery(ctx, client.Query("SELECT MAX(Time) AS Time FROM "+depsDevDataset+".Snapshots`"))
	if err != nil {
		return time.Time{}, errors.Wrap(err, "reading deps.dev snapshots")
	}
	var row struct{ Time time.Time }
	if err := it.Next(&row); err != nil {
		return time.Time{}, errors.Wrap(err, "reading latest snapshot")
	}
	if row.Time.IsZero() {
		return time.Time{}, errors.New("no deps.dev snapshot found")
	}
	return row.Time, nil
}

// packageCriticality counts, for each package, the distinct packages that
// depend on it at the given snapshot. Self-edges are excluded, as are npm's
// ">"-prefixed pseudo-packages, which are range placeholders rather than real
// publications and would otherwise register as dependents.
func packageCriticality(ctx context.Context, client *bigquery.Client, ecosystem, system string, snap time.Time, top int) ([]scheduler.CriticalityRecord, error) {
	q := client.Query("SELECT T.`To`.Name AS Package, COUNT(DISTINCT T.`From`.Name) AS Dependents\n" +
		"FROM " + depsDevDataset + ".DependencyGraphEdges` T\n" +
		"WHERE T.System = @system AND T.SnapshotAt = @snap\n" +
		"  AND T.`From`.Name != T.`To`.Name\n" +
		"  AND T.`From`.Name NOT LIKE '>%' AND T.`To`.Name NOT LIKE '>%'\n" +
		"GROUP BY Package\n" +
		"ORDER BY Dependents DESC\n" +
		"LIMIT @top")
	q.Parameters = []bigquery.QueryParameter{
		{Name: "system", Value: system},
		{Name: "snap", Value: snap},
		{Name: "top", Value: top},
	}
	it, err := runQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []scheduler.CriticalityRecord
	for {
		var row struct {
			Package    string
			Dependents int64
		}
		switch err := it.Next(&row); err {
		case nil:
			out = append(out, scheduler.CriticalityRecord{Ecosystem: ecosystem, Package: row.Package, Dependents: row.Dependents})
		case iterator.Done:
			return out, nil
		default:
			return nil, errors.Wrap(err, "iterating results")
		}
	}
}

// versionCriticality counts, for each exact version, the distinct packages
// that resolve to it. Same table and snapshot as packageCriticality, grouped
// one column finer. Self-edges cannot occur at version granularity in the same
// way, but the pseudo-package filter still applies.
func versionCriticality(ctx context.Context, client *bigquery.Client, ecosystem, system string, snap time.Time, top int) ([]scheduler.CriticalityRecord, error) {
	q := client.Query("SELECT T.`To`.Name AS Package, T.`To`.Version AS Version, COUNT(DISTINCT T.`From`.Name) AS Dependents\n" +
		"FROM " + depsDevDataset + ".DependencyGraphEdges` T\n" +
		"WHERE T.System = @system AND T.SnapshotAt = @snap\n" +
		"  AND T.`From`.Name != T.`To`.Name\n" +
		"  AND T.`From`.Name NOT LIKE '>%' AND T.`To`.Name NOT LIKE '>%'\n" +
		"  AND T.`To`.Version != ''\n" +
		"GROUP BY Package, Version\n" +
		"ORDER BY Dependents DESC\n" +
		"LIMIT @top")
	q.Parameters = []bigquery.QueryParameter{
		{Name: "system", Value: system},
		{Name: "snap", Value: snap},
		{Name: "top", Value: top},
	}
	it, err := runQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []scheduler.CriticalityRecord
	for {
		var row struct {
			Package    string
			Version    string
			Dependents int64
		}
		switch err := it.Next(&row); err {
		case nil:
			out = append(out, scheduler.CriticalityRecord{Ecosystem: ecosystem, Package: row.Package, Version: row.Version, Dependents: row.Dependents})
		case iterator.Done:
			return out, nil
		default:
			return nil, errors.Wrap(err, "iterating results")
		}
	}
}

func runQuery(ctx context.Context, q *bigquery.Query) (*bigquery.RowIterator, error) {
	job, err := q.Run(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "running query")
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "waiting for query")
	}
	if err := status.Err(); err != nil {
		return nil, errors.Wrap(err, "query failed")
	}
	it, err := job.Read(ctx)
	return it, errors.Wrap(err, "reading results")
}

func criticalityCommand() *cobra.Command {
	cfg := criticalityConfig{}
	cmd := &cobra.Command{
		Use:   "criticality [--project <project>] [--out <file>] [--load]",
		Short: "Rank packages by distinct reverse-dependents (deps.dev graph)",
		Long: `Rank packages by distinct reverse-dependents (deps.dev graph).

Counts how many distinct packages depend on each package, and separately on
each exact version, at the latest deps.dev snapshot, then ranks both into
per-ecosystem quantiles.

Version granularity matters because the most-depended-upon version is rarely
the newest: lockfiles pin, semver ranges settle, and old majors keep their
dependents for years. Without it, recency would be the only thing separating a
package's versions from each other.

This is distinct from the OSSF Criticality Score, which scores repository
activity such as contributor counts, commit cadence, and issue velocity.
Criticality here measures blast radius only, because that is the one signal
deps.dev publishes uniformly for every ecosystem it covers.

Reads public data only, so it needs no BigQuery write access.`,
		Args: cobra.NoArgs,
		RunE: cli.RunE(&cfg, cli.SkipArgs[criticalityConfig], InitDeps, criticalityHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.BillingProject, "billing-project", "", "GCP project that runs and is billed for the BigQuery jobs (default: project)")
	set.StringVar(&cfg.Ecosystems, "ecosystems", "npm,pypi,cratesio,rubygems,maven", "comma-separated ecosystems to rank")
	set.IntVar(&cfg.Top, "top", 5000, "max packages to keep per ecosystem, by dependent count")
	set.IntVar(&cfg.TopVersions, "top-versions", 20000, "max versions to keep per ecosystem, by dependent count")
	set.StringVar(&cfg.Out, "out", "", "write the JSON export to this file")
	set.BoolVar(&cfg.Load, "load", false, "load the ranked result into Firestore")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

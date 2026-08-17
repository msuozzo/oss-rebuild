// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// enqueue
// ---------------------------------------------------------------------------

type enqueueConfig struct {
	Project           string
	Ecosystem         string
	Package           string
	MaxVersions       int
	FreshnessK        float64
	FreshnessTauHours float64
}

func (c enqueueConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.Ecosystem == "" {
		return errors.New("ecosystem is required")
	}
	if c.Package == "" {
		return errors.New("package is required")
	}
	return nil
}

type versionInfo struct {
	Version   string
	Published time.Time
}

func enqueueHandler(ctx context.Context, cfg enqueueConfig, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	targets := db.NewFirestoreLadderTargets(fire)
	mux := meta.NewRegistryMux(http.DefaultClient)
	eco := rebuild.Ecosystem(cfg.Ecosystem)
	versions, err := enumerateVersions(ctx, mux, eco, cfg.Package)
	if err != nil {
		return nil, errors.Wrap(err, "enumerating versions")
	}
	// The score is materialized by `ctl onboard priority`, so enqueue only
	// reads it. A package with no priority document enqueues at zero and is
	// ordered by freshness alone until the next priority load reaches it.
	var priority scheduler.Priority
	if p, err := db.NewFirestorePriorities(fire).Get(ctx, db.PriorityKey{Ecosystem: cfg.Ecosystem, Package: cfg.Package}); err == nil {
		priority = p
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, errors.Wrap(err, "reading package priority")
	}
	now := time.Now().UTC()
	scored := scoreVersions(ctx, db.NewFirestoreVersionCriticalities(fire), priority, versions, cfg, now)
	if cfg.MaxVersions > 0 && len(scored) > cfg.MaxVersions {
		scored = scored[:cfg.MaxVersions]
	}

	var enqueued, skipped int
	for _, lt := range scored {
		t := rebuild.Target{Ecosystem: eco, Package: cfg.Package, Version: lt.Version}
		art, err := meta.GuessArtifact(ctx, t, mux)
		if err != nil {
			fmt.Fprintf(deps.IO.Err, "skip %s@%s: resolving artifact: %v\n", cfg.Package, lt.Version, err)
			continue
		}
		lt.Artifact = art
		switch err := targets.Insert(ctx, lt); err {
		case nil:
			enqueued++
		case db.ErrAlreadyExists:
			skipped++
		default:
			fmt.Fprintf(deps.IO.Err, "enqueue %s@%s: %v\n", cfg.Package, lt.Version, err)
		}
	}
	fmt.Fprintf(deps.IO.Out, "enqueued %d version(s) of %s (%s) at T1; %d already tracked\n", enqueued, cfg.Package, cfg.Ecosystem, skipped)
	return &act.NoOutput{}, nil
}

// scoreVersions builds a queue document per version, ordered by DispatchOrder
// so that --max-versions admits the versions most worth rebuilding rather than
// merely the newest. Admission uses the same ordering the queue is drained by,
// so a version that would never reach the front never enters.
//
// Each version is scored against its own criticality where deps.dev knows it,
// falling back to the package's. The fallback degrades to ranking a package's
// back catalogue by recency alone, which is the right default: absent evidence
// about individual versions, prefer rebuilding more of a prominent package.
func scoreVersions(ctx context.Context, crit db.VersionCriticalities, priority scheduler.Priority, versions []versionInfo, cfg enqueueConfig, now time.Time) []scheduler.LadderTarget {
	out := make([]scheduler.LadderTarget, 0, len(versions))
	for _, v := range versions {
		qCrit := priority.QCrit
		vc, err := crit.Get(ctx, rebuild.Target{Ecosystem: rebuild.Ecosystem(cfg.Ecosystem), Package: cfg.Package, Version: v.Version})
		if err == nil {
			qCrit = vc.QCrit
		}
		out = append(out, scheduler.LadderTarget{
			Ecosystem: cfg.Ecosystem, Package: cfg.Package, Version: v.Version,
			NextTier:  int(scheduler.TierInference),
			State:     scheduler.StateQueued,
			Score:     priority.ScoreWith(qCrit),
			Published: v.Published,
			Freshness: scheduler.Freshness(v.Published, now, cfg.FreshnessK, cfg.FreshnessTauHours),
			Updated:   now,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DispatchOrder() > out[j].DispatchOrder() })
	return out
}

func enumerateVersions(ctx context.Context, mux rebuild.RegistryMux, eco rebuild.Ecosystem, pkg string) ([]versionInfo, error) {
	var out []versionInfo
	switch eco {
	case rebuild.NPM:
		p, err := mux.NPM.Package(ctx, pkg)
		if err != nil {
			return nil, err
		}
		for ver := range p.Versions {
			out = append(out, versionInfo{Version: ver, Published: p.UploadTimes[ver]})
		}
	case rebuild.PyPI:
		proj, err := mux.PyPI.Project(ctx, pkg)
		if err != nil {
			return nil, err
		}
		for ver, arts := range proj.Releases {
			var pub time.Time
			for _, a := range arts {
				if a.UploadTime.After(pub) {
					pub = a.UploadTime
				}
			}
			out = append(out, versionInfo{Version: ver, Published: pub})
		}
	case rebuild.CratesIO:
		crate, err := mux.CratesIO.Crate(ctx, pkg)
		if err != nil {
			return nil, err
		}
		for _, v := range crate.Versions {
			if v.Yanked {
				continue
			}
			out = append(out, versionInfo{Version: v.Version, Published: v.Created})
		}
	case rebuild.RubyGems:
		vs, err := mux.RubyGems.Versions(ctx, pkg)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if v.Prerelease {
				continue
			}
			out = append(out, versionInfo{Version: v.Number, Published: v.CreatedAt})
		}
	default:
		return nil, errors.Errorf("unsupported ecosystem %q for enqueue", eco)
	}
	return out, nil
}

func enqueueCommand() *cobra.Command {
	cfg := enqueueConfig{}
	cmd := &cobra.Command{
		Use:   "enqueue --project <project> --ecosystem <eco> --package <name> [--max-versions N]",
		Short: "Enqueue all versions of a package at T1",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[enqueueConfig], InitDeps, enqueueHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.Ecosystem, "ecosystem", "", "the ecosystem (npm, pypi, cratesio, rubygems)")
	set.StringVar(&cfg.Package, "package", "", "the package name")
	set.IntVar(&cfg.MaxVersions, "max-versions", 10, "cap the versions enqueued, highest dispatch order first; 0 = all")
	set.Float64Var(&cfg.FreshnessK, "freshness-k", 3, "freshness boost coefficient k in 1+k*exp(-age/tau)")
	set.Float64Var(&cfg.FreshnessTauHours, "freshness-tau-hours", 120, "freshness decay constant tau in hours")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

type statusConfig struct {
	Project string
}

func (c statusConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	return nil
}

func statusHandler(ctx context.Context, cfg statusConfig, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	all, err := db.ListLadderTargets(ctx, fire)
	if err != nil {
		return nil, errors.Wrap(err, "listing targets")
	}
	byState := map[scheduler.TargetState]int{}
	byTier := map[string]int{}
	var attested int
	for _, t := range all {
		byState[t.State]++
		byTier[scheduler.Tier(t.NextTier).String()]++
		if t.Outcome == scheduler.OutcomeAttested || t.State == scheduler.StateDone {
			attested++
		}
	}
	out := deps.IO.Out
	fmt.Fprintf(out, "targets: %d\n", len(all))
	fmt.Fprintf(out, "  by state: queued=%d inflight=%d done=%d parked=%d\n",
		byState[scheduler.StateQueued], byState[scheduler.StateInFlight], byState[scheduler.StateDone], byState[scheduler.StateParked])
	fmt.Fprintf(out, "  by next tier: T0=%d T1=%d T2=%d T3=%d\n", byTier["T0"], byTier["T1"], byTier["T2"], byTier["T3"])
	if len(all) > 0 {
		fmt.Fprintf(out, "coverage (attested/total): %d/%d = %.1f%%\n", attested, len(all), 100*float64(attested)/float64(len(all)))
	}
	return &act.NoOutput{}, nil
}

func statusCommand() *cobra.Command {
	cfg := statusConfig{}
	cmd := &cobra.Command{
		Use:   "status --project <project>",
		Short: "Print queue state and coverage",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[statusConfig], InitDeps, statusHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

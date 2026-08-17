// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"flag"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/oauth"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// apiClient builds an HTTP client (Cloud Run-authorized when the endpoint is
// on run.app) and the parsed base URL.
func apiClient(ctx context.Context, endpoint string) (*http.Client, *url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, nil, errors.Wrap(err, "parsing API endpoint")
	}
	if strings.Contains(u.Host, "run.app") {
		u.Scheme = "https"
		client, err := oauth.AuthorizedUserIDClient(ctx)
		if err != nil {
			return nil, nil, errors.Wrap(err, "creating authorized HTTP client")
		}
		return client, u, nil
	}
	return http.DefaultClient, u, nil
}

// ---------------------------------------------------------------------------
// round
// ---------------------------------------------------------------------------

type roundConfig struct {
	Project         string
	API             string
	Batch           int
	MaxT2           int
	PerPackage      int
	AgentIterations int
	TokenCap        int64
	IterCap         int
	Period          time.Duration
	InflightTimeout time.Duration
	MaxRetries      int
	JumboBytes      int64
}

func (c roundConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.API == "" {
		return errors.New("api is required")
	}
	return nil
}

var errBudget = errors.New("t2 budget exhausted")

type round struct {
	io      cli.IO
	fire    *firestore.Client
	dex     *rundex.FirestoreClient
	targets db.LadderTargets
	budgets db.LadderBudgets
	repo    db.RepoMetrics
	create  api.StubFn[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]]
	agent   api.StubFn[schema.AgentCreateRequest, schema.AgentCreateResponse]
	mux     rebuild.RegistryMux
	cfg     roundConfig
	now     time.Time
}

func roundHandler(ctx context.Context, cfg roundConfig, deps *Deps) (*act.NoOutput, error) {
	fire, err := firestore.NewClient(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore client")
	}
	defer fire.Close()
	dex, err := rundex.NewFirestore(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating rundex client")
	}
	client, apiURL, err := apiClient(ctx, cfg.API)
	if err != nil {
		return nil, err
	}
	r := &round{
		io:      deps.IO,
		fire:    fire,
		dex:     dex,
		targets: db.NewFirestoreLadderTargets(fire),
		budgets: db.NewFirestoreLadderBudget(fire),
		repo:    db.NewFirestoreRepoMetrics(fire),
		create:  api.Stub[schema.RebuildPackageRequest, longrunning.Operation[schema.Verdict]](client, apiURL.JoinPath("rebuild", "op", "create")),
		agent:   api.Stub[schema.AgentCreateRequest, schema.AgentCreateResponse](client, apiURL.JoinPath("agent")),
		mux:     meta.NewRegistryMux(http.DefaultClient),
		cfg:     cfg,
		now:     time.Now().UTC(),
	}
	return &act.NoOutput{}, r.run(ctx)
}

func (r *round) run(ctx context.Context) error {
	budget, err := r.loadBudget(ctx)
	if err != nil {
		return errors.Wrap(err, "loading budget")
	}
	all, err := db.ListLadderTargets(ctx, r.fire)
	if err != nil {
		return errors.Wrap(err, "listing targets")
	}

	// Phase 1: observe in-flight dispatches and transition them.
	var observed int
	for i := range all {
		if all[i].State != scheduler.StateInFlight {
			continue
		}
		if r.observe(ctx, &all[i], &budget) {
			r.save(ctx, all[i])
			observed++
		}
	}

	// Phase 2: dispatch queued targets, score-ordered, batch- and budget-bounded.
	var qi []int
	for i := range all {
		if all[i].State == scheduler.StateQueued {
			qi = append(qi, i)
		}
	}
	sort.SliceStable(qi, func(a, b int) bool {
		return all[qi[a]].DispatchOrder() > all[qi[b]].DispatchOrder()
	})
	// A per-package cap walks alongside the global order. Without it a package
	// that releases often puts dozens of equally fresh versions at the head of
	// the queue and takes the whole round, every round, until it drains.
	var dispatched, t2disp int
	perPackage := map[string]int{}
	for _, i := range qi {
		if r.cfg.Batch > 0 && dispatched >= r.cfg.Batch {
			break
		}
		pkg := scheduler.PackageID(all[i].Ecosystem, all[i].Package)
		if r.cfg.PerPackage > 0 && perPackage[pkg] >= r.cfg.PerPackage {
			continue
		}
		isT2 := scheduler.Tier(all[i].NextTier) == scheduler.TierAgent
		if isT2 && r.cfg.MaxT2 > 0 && t2disp >= r.cfg.MaxT2 {
			continue
		}
		ok, wasT2, err := r.dispatch(ctx, &all[i], &budget)
		switch {
		case errors.Is(err, errBudget):
			continue
		case err != nil:
			t := all[i].Target()
			fmt.Fprintf(r.io.Err, "dispatch %s@%s (%s): %v\n", t.Package, t.Version, scheduler.Tier(all[i].NextTier), err)
			continue
		}
		if ok {
			r.save(ctx, all[i])
			dispatched++
			perPackage[pkg]++
			if wasT2 {
				t2disp++
			}
		}
	}

	budget.Updated = r.now
	if err := r.budgets.Upsert(ctx, budget); err != nil {
		fmt.Fprintf(r.io.Err, "persisting budget: %v\n", err)
	}
	fmt.Fprintf(r.io.Out, "round: observed %d, dispatched %d (%d at T2); T2 tokens %d/%d, sessions %d/%d\n",
		observed, dispatched, t2disp, budget.TokenSpent, budget.TokenCap, budget.IterSpent, budget.IterCap)
	return nil
}

// observe resolves an in-flight target's outcome. Returns true when the target
// changed and should be persisted.
func (r *round) observe(ctx context.Context, t *scheduler.LadderTarget, budget *scheduler.LadderBudget) bool {
	if scheduler.Tier(t.NextTier) == scheduler.TierAgent && t.LastSession != "" {
		snap, err := r.fire.Collection("agent_sessions").Doc(t.LastSession).Get(ctx)
		if err != nil {
			return r.maybeTimeout(t)
		}
		var s schema.AgentSession
		if err := snap.DataTo(&s); err != nil {
			return r.maybeTimeout(t)
		}
		if s.Status != schema.AgentSessionStatusCompleted {
			return r.maybeTimeout(t)
		}
		budget.TokenSpent += s.TotalTokens
		budget.IterSpent++
		*t = scheduler.Tick(*t, scheduler.ClassifySession(s.StopReason), r.cfg.MaxRetries, r.now)
		return true
	}
	rb, err := r.dex.FetchAttempt(ctx, t.Target(), t.LastRunID)
	if err != nil {
		return r.maybeTimeout(t)
	}
	if !scheduler.IsTerminal(rb.Status) {
		return r.maybeTimeout(t)
	}
	if repo := scheduler.RepoFromStrategy(rb.Strategy); repo != "" {
		t.Repo = repo
	}
	*t = scheduler.Tick(*t, scheduler.ClassifyRebuild(rb.Status, rb.Message), r.cfg.MaxRetries, r.now)
	return true
}

// maybeTimeout requeues a target whose dispatch has been in flight past the
// configured timeout, meaning a lost or wedged job. Resuming one is out of
// scope for the MVP.
func (r *round) maybeTimeout(t *scheduler.LadderTarget) bool {
	if r.cfg.InflightTimeout <= 0 || t.DispatchedAt.IsZero() {
		return false
	}
	if r.now.Sub(t.DispatchedAt) <= r.cfg.InflightTimeout {
		return false
	}
	t.State = scheduler.StateQueued
	t.Outcome = scheduler.OutcomeTransient
	t.Retries++
	t.Updated = r.now
	return true
}

// dispatch launches the target's NextTier. Returns (dispatched, wasT2, err).
func (r *round) dispatch(ctx context.Context, t *scheduler.LadderTarget, budget *scheduler.LadderBudget) (bool, bool, error) {
	tier := scheduler.Tier(t.NextTier)
	runID := r.now.Format(time.RFC3339Nano) + "-" + shortHash(scheduler.TargetID(t.Target()))
	switch tier {
	case scheduler.TierReplay, scheduler.TierInference:
		if _, err := r.create(ctx, schema.RebuildPackageRequest{
			Ecosystem:         rebuild.Ecosystem(t.Ecosystem),
			Package:           t.Package,
			Version:           t.Version,
			Artifact:          t.Artifact,
			ID:                runID,
			ExecutionHint:     schema.ExtendedExecution,
			SizeHint:          r.sizeHint(ctx, t),
			UseRepoDefinition: tier == scheduler.TierReplay,
		}); err != nil {
			return false, false, err
		}
		t.LastRunID = runID
	case scheduler.TierAgent:
		if budget.IterCap > 0 && budget.IterSpent >= budget.IterCap {
			return false, true, errBudget
		}
		if budget.TokenCap > 0 && budget.TokenSpent >= budget.TokenCap {
			return false, true, errBudget
		}
		if t.Artifact == "" {
			art, err := meta.GuessArtifact(ctx, t.Target(), r.mux)
			if err != nil {
				return false, true, errors.Wrap(err, "resolving artifact")
			}
			t.Artifact = art
		}
		resp, err := r.agent(ctx, schema.AgentCreateRequest{
			Target:        t.Target(),
			RunID:         runID,
			MaxIterations: r.cfg.AgentIterations,
		})
		if err != nil {
			return false, true, err
		}
		t.LastSession = resp.SessionID
		t.LastRunID = runID
	default:
		return false, false, nil
	}
	t.State = scheduler.StateInFlight
	t.Outcome = scheduler.OutcomePending
	t.DispatchedAt = r.now
	t.Attempts++
	return true, tier == scheduler.TierAgent, nil
}

// sizeHint routes builds with large known repos to the jumbo pool. The repo is
// known only after a prior attempt records its strategy, so first attempts
// fall back to the service's own sizing.
func (r *round) sizeHint(ctx context.Context, t *scheduler.LadderTarget) schema.SizeHint {
	if t.Repo == "" {
		return schema.UnspecifiedSize
	}
	rm, err := r.repo.Get(ctx, t.Repo)
	if err != nil {
		return schema.UnspecifiedSize
	}
	return scheduler.SizeHintForBytes(rm.Bytes, r.cfg.JumboBytes)
}

func (r *round) loadBudget(ctx context.Context) (scheduler.LadderBudget, error) {
	b, err := r.budgets.Get(ctx, scheduler.BudgetDocID)
	if errors.Is(err, db.ErrNotFound) {
		b = scheduler.LadderBudget{PeriodStart: r.now}
	} else if err != nil {
		return b, err
	}
	if r.cfg.TokenCap > 0 {
		b.TokenCap = r.cfg.TokenCap
	}
	if r.cfg.IterCap > 0 {
		b.IterCap = r.cfg.IterCap
	}
	if r.cfg.Period > 0 && !b.PeriodStart.IsZero() && r.now.Sub(b.PeriodStart) > r.cfg.Period {
		b.PeriodStart = r.now
		b.TokenSpent = 0
		b.IterSpent = 0
	}
	return b, nil
}

func (r *round) save(ctx context.Context, t scheduler.LadderTarget) {
	if err := r.targets.Upsert(ctx, t); err != nil {
		fmt.Fprintf(r.io.Err, "persisting %s: %v\n", scheduler.TargetID(t.Target()), err)
	}
}

func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

func roundCommand() *cobra.Command {
	cfg := roundConfig{}
	cmd := &cobra.Command{
		Use:   "round --project <project> --api <URI> [--batch N] [--max-t2 N] [--per-package N]",
		Short: "Run one round: observe in-flight, dispatch queued, escalate or park",
		Args:  cobra.NoArgs,
		RunE:  cli.RunE(&cfg, cli.SkipArgs[roundConfig], InitDeps, roundHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project holding the onboarding Firestore data")
	set.StringVar(&cfg.API, "api", "", "OSS Rebuild API endpoint URI")
	set.IntVar(&cfg.Batch, "batch", 50, "max dispatches this round; 0 = unbounded")
	set.IntVar(&cfg.MaxT2, "max-t2", 5, "max T2 (agent) dispatches this round")
	set.IntVar(&cfg.PerPackage, "per-package", 5, "max dispatches per package this round, so no one package takes the whole round")
	set.IntVar(&cfg.AgentIterations, "agent-iterations", 5, "max agent iterations per T2 session")
	set.Int64Var(&cfg.TokenCap, "token-cap", 0, "T2 LLM token cap per period; 0 = keep existing")
	set.IntVar(&cfg.IterCap, "iter-cap", 0, "T2 agent-session cap per period; 0 = keep existing")
	set.DurationVar(&cfg.Period, "period", 24*time.Hour, "budget period length")
	set.DurationVar(&cfg.InflightTimeout, "inflight-timeout", 24*time.Hour, "requeue dispatches stuck in flight longer than this")
	set.IntVar(&cfg.MaxRetries, "max-retries", 5, "max same-tier transient retries before parking")
	set.Int64Var(&cfg.JumboBytes, "jumbo-bytes", scheduler.DefaultJumboRepoBytes, "repo size (bytes) at/above which builds route to the jumbo pool")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

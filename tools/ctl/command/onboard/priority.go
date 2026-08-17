// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"context"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// priorityCommand groups the offline jobs that rank onboarding candidates.
// Each subcommand owns one signal of package importance: it computes that
// signal, ranks it into per-ecosystem quantiles, and materializes the fused
// score onto the package's priority document. They run on their own cadence,
// independently of each other and of the rounds that spend against them.
func priorityCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "priority",
		Short: "Compute and load the signals that rank onboarding candidates",
	}
	cmd.AddCommand(criticalityCommand(), prominenceCommand(), evalCommand())
	return cmd
}

// upsertMerged applies one signal to a package's priority document and
// rematerializes its score. Reading before writing is what lets the signals
// land independently: whichever job runs second must leave the other's
// contribution intact.
func upsertMerged(ctx context.Context, store db.Priorities, ecosystem, pkg string, now time.Time, apply func(*scheduler.Priority)) error {
	key := db.PriorityKey{Ecosystem: ecosystem, Package: pkg}
	p, err := store.Get(ctx, key)
	switch {
	case errors.Is(err, db.ErrNotFound):
		p = scheduler.Priority{Ecosystem: ecosystem, Package: pkg}
	case err != nil:
		return err
	}
	apply(&p)
	p.Rescore()
	p.Updated = now
	return store.Upsert(ctx, p)
}

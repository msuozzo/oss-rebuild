// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import "github.com/spf13/cobra"

// priorityCommand groups the offline jobs that rank onboarding candidates.
// Each subcommand owns one signal of package importance: it computes that
// signal, ranks it into per-ecosystem quantiles, and materializes the fused
// score onto the package's priority document. They run on their own cadence,
// independently of the rounds that spend against them.
func priorityCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "priority",
		Short: "Compute and load the signals that rank onboarding candidates",
	}
	cmd.AddCommand(criticalityCommand())
	return cmd
}

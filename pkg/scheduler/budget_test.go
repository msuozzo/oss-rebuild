// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTokenRemaining(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   LadderBudget
		want int64
	}{
		{"UnderCap", LadderBudget{TokenCap: 100, TokenSpent: 40}, 60},
		{"Overspent", LadderBudget{TokenCap: 100, TokenSpent: 140}, 0},
		{"NoCapConfigured", LadderBudget{TokenSpent: 40}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.in.TokenRemaining()); diff != "" {
				t.Errorf("TokenRemaining mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

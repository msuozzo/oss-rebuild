// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestClassifyRebuild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  schema.RebuildStatus
		message string
		want    Outcome
	}{
		{"Success", schema.RebuildStatusSuccess, "", OutcomeAttested},
		{"EmptyMessage", schema.RebuildStatusUnspecified, "", OutcomeAttested},
		// A prior run already produced the attestation, so the work is done.
		{"AlreadyExists", schema.RebuildStatusError, "attestation AlreadyExists", OutcomeAttested},
		// The one outcome that genuinely means "not reproducible here".
		{"ContentMismatch", schema.RebuildStatusFailure, "rebuild content mismatch", OutcomeFailure},
		{"InfraFlake", schema.RebuildStatusError, "read tcp: connection reset by peer", OutcomeTransient},
		{"Cancelled", schema.RebuildStatusCancelled, "context canceled", OutcomeTransient},
		{"GenericError", schema.RebuildStatusError, "no such package", OutcomeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, ClassifyRebuild(tc.status, tc.message)); diff != "" {
				t.Errorf("ClassifyRebuild(%v, %q) mismatch (-want +got):\n%s", tc.status, tc.message, diff)
			}
		})
	}
}

func TestClassifySession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		want   Outcome
	}{
		{"Success", schema.AgentCompleteReasonSuccess, OutcomeAttested},
		// Throttling says nothing about the package, so it must not escalate.
		{"Throttled", schema.AgentCompleteReasonThrottled, OutcomeTransient},
		{"Failed", schema.AgentCompleteReasonFailed, OutcomeFailure},
		{"Error", schema.AgentCompleteReasonError, OutcomeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, ClassifySession(tc.reason)); diff != "" {
				t.Errorf("ClassifySession(%q) mismatch (-want +got):\n%s", tc.reason, diff)
			}
		})
	}
}

func TestSizeHintForBytes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bytes     int64
		threshold int64
		want      schema.SizeHint
	}{
		{"BelowThreshold", 10, 100, schema.ShrimpSize},
		{"AtThreshold", 100, 100, schema.JumboSize},
		{"DefaultThreshold", DefaultJumboRepoBytes + 1, 0, schema.JumboSize},
		// An unmeasured repo must not be assumed large.
		{"UnknownSize", 0, 100, schema.ShrimpSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, SizeHintForBytes(tc.bytes, tc.threshold)); diff != "" {
				t.Errorf("SizeHintForBytes(%d, %d) mismatch (-want +got):\n%s", tc.bytes, tc.threshold, diff)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status schema.RebuildStatus
		want   bool
	}{
		{"Success", schema.RebuildStatusSuccess, true},
		{"Failure", schema.RebuildStatusFailure, true},
		{"Cancelled", schema.RebuildStatusCancelled, true},
		{"Unspecified", schema.RebuildStatusUnspecified, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, IsTerminal(tc.status)); diff != "" {
				t.Errorf("IsTerminal(%v) mismatch (-want +got):\n%s", tc.status, diff)
			}
		})
	}
}

// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"reflect"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// transientBuildSignatures are message fragments that indicate a build ended on
// infrastructure flakiness rather than a genuine reproduction failure. Kept
// deliberately small (the MVP punts the full failure taxonomy). These mirror
// the transient cases already recognized in rundex.cleanVerdict.
var transientBuildSignatures = []string{
	"connection reset by peer",
	"connection to gateway failed",
	"crates.io connection failed",
	"context deadline exceeded",
	"i/o timeout",
	"internal error",
	"try again",
}

// cachedSignatures indicate a prior successful attestation already exists, which
// we treat as an attested outcome.
var cachedSignatures = []string{"alreadyexists", "already exists", "cached"}

// ClassifyRebuild maps a T0/T1 rebuild attempt's status and message onto a
// ladder outcome. Attested reproductions and cache hits are Attested. Infra
// flakes and cancellations are Transient, retried at the same tier. Everything
// else, notably "rebuild content mismatch", is a Failure that escalates or parks.
func ClassifyRebuild(status schema.RebuildStatus, message string) Outcome {
	msg := strings.ToLower(message)
	for _, s := range cachedSignatures {
		if strings.Contains(msg, s) {
			return OutcomeAttested
		}
	}
	switch status {
	case schema.RebuildStatusSuccess:
		return OutcomeAttested
	case schema.RebuildStatusCancelled:
		return OutcomeTransient
	}
	if message == "" {
		return OutcomeAttested
	}
	for _, s := range transientBuildSignatures {
		if strings.Contains(msg, s) {
			return OutcomeTransient
		}
	}
	return OutcomeFailure
}

// ClassifySession maps a T2 agent session's completion reason onto an outcome.
// Trust only the agent's build-confirmed SUCCESS. A THROTTLED session is
// transient and retried at the same tier, while FAILED and ERROR are genuine
// failures.
func ClassifySession(stopReason string) Outcome {
	switch stopReason {
	case schema.AgentCompleteReasonSuccess:
		return OutcomeAttested
	case schema.AgentCompleteReasonThrottled:
		return OutcomeTransient
	default:
		return OutcomeFailure
	}
}

// IsTerminal reports whether a rebuild status is final, and so safe to read
// as a completed dispatch.
func IsTerminal(status schema.RebuildStatus) bool {
	switch status {
	case schema.RebuildStatusSuccess, schema.RebuildStatusFailure,
		schema.RebuildStatusError, schema.RebuildStatusCancelled:
		return true
	default:
		return false
	}
}

// DefaultJumboRepoBytes is the packed-object-store size at or above which a
// package's builds are routed to the jumbo pool.
const DefaultJumboRepoBytes int64 = 500 * 1024 * 1024 // 500 MiB

// SizeHintForBytes picks a build pool from a repo's measured size. threshold<=0
// uses DefaultJumboRepoBytes. bytes<=0 (unknown size) yields the small pool.
func SizeHintForBytes(bytes, threshold int64) schema.SizeHint {
	if threshold <= 0 {
		threshold = DefaultJumboRepoBytes
	}
	if bytes >= threshold {
		return schema.JumboSize
	}
	return schema.ShrimpSize
}

// RepoFromStrategy best-effort extracts the source repository URL from a
// resolved strategy's embedded Location. Returns "" when the strategy has no
// Location. Used to look up RepoMetrics for jumbo routing on re-dispatch.
func RepoFromStrategy(oneof schema.StrategyOneOf) string {
	s, err := oneof.Strategy()
	if err != nil || s == nil {
		return ""
	}
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName("Location")
	if !f.IsValid() || !f.CanInterface() {
		return ""
	}
	if loc, ok := f.Interface().(rebuild.Location); ok {
		return loc.Repo
	}
	return ""
}

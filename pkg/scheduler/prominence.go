// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

// ProminenceRecord is one row of a prominence export: the LLM-elicited public
// notability p of a package, in [0,1]. Prominence is a per-package property,
// not a per-version one.
//
// p measures how well known a package is under that exact name, which is a
// proxy for public awareness rather than a measure of quality or trust. It
// exists because criticality is structurally blind to leaves: an application
// has no dependents no matter how many people install it. Downloads and
// repository statistics would cover the same gap, but no equivalent is
// published across every registry we rebuild.
//
// A package registered after the scoring model's knowledge horizon is floored
// to 0: the model cannot know an artifact published after its training data,
// so any score would be inherited from a similarly named neighbor. Floored and
// unscored packages simply ride the criticality term. See PROMINENCE.md in
// tools/ctl/command/onboard for the full rationale.
type ProminenceRecord struct {
	Ecosystem string  `json:"ecosystem"`
	Package   string  `json:"package"`
	P         float64 `json:"p"`
}

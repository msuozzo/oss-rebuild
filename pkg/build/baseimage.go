// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"github.com/google/oss-rebuild/pkg/rebuild/alpine"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type BaseImageConfig struct {
	Default    string                       `json:"default"`
	Ecosystems map[rebuild.Ecosystem]string `json:"ecosystems"`
}

func (c BaseImageConfig) SelectFor(input rebuild.Input, req rebuild.RequiredEnv) string {
	if req.BaseImage != "" {
		return req.BaseImage
	}
	if img, ok := c.Ecosystems[input.Target.Ecosystem]; ok {
		return img
	}
	// Alpine builds need the image of their target's branch.
	if input.Target.Ecosystem == rebuild.Alpine {
		if img, err := alpine.BaseImage(input.Target); err == nil {
			return img
		}
	}
	return c.Default
}

func DefaultBaseImageConfig() BaseImageConfig {
	return BaseImageConfig{
		Default: "docker.io/library/alpine:3.21",
		Ecosystems: map[rebuild.Ecosystem]string{
			rebuild.Debian:   "docker.io/library/debian:stable-20251103-slim",
			rebuild.Maven:    "docker.io/library/debian:stable-20251103-slim",
			rebuild.OCI:      "docker.io/library/docker:29.5-dind-rootless",
			rebuild.RubyGems: "docker.io/library/debian:stable-20251103-slim",
		},
	}
}

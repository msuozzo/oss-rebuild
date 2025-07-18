// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package commandreg

import (
	"context"
	"fmt"

	"github.com/google/oss-rebuild/tools/ctl/rundex"
)

type RebuildCmd struct {
	Short       string
	Hotkey      rune
	Func        func(context.Context, rundex.Rebuild)
	DisabledMsg func() string
}

func (c RebuildCmd) IsDisabled() bool {
	if c.DisabledMsg == nil {
		return false
	}
	return c.DisabledMsg() != ""
}

type RebuildGroupCmd struct {
	Short       string
	Func        func(context.Context, []rundex.Rebuild)
	DisabledMsg func() string
}

func (c RebuildGroupCmd) IsDisabled() bool {
	if c.DisabledMsg == nil {
		return false
	}
	return c.DisabledMsg() != ""
}

type BenchmarkCmd struct {
	Short       string
	Func        func(context.Context, string)
	DisabledMsg func() string
}

func (c BenchmarkCmd) IsDisabled() bool {
	if c.DisabledMsg == nil {
		return false
	}
	return c.DisabledMsg() != ""
}

type GlobalCmd struct {
	Short       string
	Hotkey      rune
	Func        func(context.Context)
	DisabledMsg func() string
}

func (c GlobalCmd) IsDisabled() bool {
	if c.DisabledMsg == nil {
		return false
	}
	return c.DisabledMsg() != ""
}

type Registry struct {
	rebuildCmds      []RebuildCmd
	rebuildGroupCmds []RebuildGroupCmd
	benchmarkCmds    []BenchmarkCmd
	globalCmds       []GlobalCmd
}

func (reg *Registry) AddGlobals(cmds ...GlobalCmd) error {
	old := reg.globalCmds
	reg.globalCmds = append(reg.globalCmds, cmds...)
	err := reg.Validate()
	if err != nil {
		reg.globalCmds = old
		return err
	}
	return nil
}

func (reg *Registry) AddRebuildGroups(cmds ...RebuildGroupCmd) error {
	old := reg.rebuildGroupCmds
	reg.rebuildGroupCmds = append(reg.rebuildGroupCmds, cmds...)
	err := reg.Validate()
	if err != nil {
		reg.rebuildGroupCmds = old
		return err
	}
	return nil
}

func (reg *Registry) AddRebuilds(cmds ...RebuildCmd) error {
	old := reg.rebuildCmds
	reg.rebuildCmds = append(reg.rebuildCmds, cmds...)
	err := reg.Validate()
	if err != nil {
		reg.rebuildCmds = old
		return err
	}
	return nil
}

func (reg *Registry) AddBenchmarks(cmds ...BenchmarkCmd) error {
	old := reg.benchmarkCmds
	reg.benchmarkCmds = append(reg.benchmarkCmds, cmds...)
	err := reg.Validate()
	if err != nil {
		reg.benchmarkCmds = old
		return err
	}
	return nil
}

func (reg *Registry) Validate() error {
	hotkeys := make(map[rune]bool)
	for _, cmd := range reg.rebuildCmds {
		if cmd.Hotkey != 0 {
			if hotkeys[cmd.Hotkey] {
				return fmt.Errorf("duplicate hotkey: %c (%s)", cmd.Hotkey, cmd.Short)
			}
			hotkeys[cmd.Hotkey] = true
		}
	}
	for _, cmd := range reg.globalCmds {
		if cmd.Hotkey != 0 {
			if hotkeys[cmd.Hotkey] {
				return fmt.Errorf("duplicate hotkey: %c (%s)", cmd.Hotkey, cmd.Short)
			}
			hotkeys[cmd.Hotkey] = true
		}
	}
	return nil
}

func (reg *Registry) RebuildCommands() []RebuildCmd {
	return reg.rebuildCmds
}

func (reg *Registry) RebuildGroupCommands() []RebuildGroupCmd {
	return reg.rebuildGroupCmds
}

func (reg *Registry) BenchmarkCommands() []BenchmarkCmd {
	return reg.benchmarkCmds
}

func (reg *Registry) GlobalCommands() []GlobalCmd {
	return reg.globalCmds
}

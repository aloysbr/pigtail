package main

// Removal gate: 2026-12-01 (sub_x000000000000035).

import (
	"errors"

	"github.com/spf13/cobra"
)

// retiredWebAliasCmd is the former name of `pigtail web`, kept as a hidden
// command that refuses and points at the new one. A Cobra alias would keep
// the old name as a second live entry point; a refusal turns the reflex into
// a hint and runs nothing: no client, no socket, no login code.
func retiredWebAliasCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "cockpit",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("pigtail cockpit is now pigtail web: run pigtail web")
		},
	}
}

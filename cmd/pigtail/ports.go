package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func portsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ports",
		Short: "Repair a workspace's ports (registry vs .env.local)",
	}
	cmd.AddCommand(portsResyncCmd(), portsReassignCmd())
	return cmd
}

func portsResyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resync [workspace_id]",
		Short: "Copy the ports of .env.local into the registry",
		Long: `Make the registry agree with .env.local, which is what "pigtail dev" launches.

Refuses a port another live workspace is registered with, or that another
worktree's server is listening on: use "pigtail ports reassign" then.
Without an argument, the workspace is inferred from the current directory.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPortsPatch(args, pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync})
		},
	}
}

func portsReassignCmd() *cobra.Command {
	var kinds []string
	cmd := &cobra.Command{
		Use:   "reassign [workspace_id] [--kind dev|storybook|…]",
		Short: "Give a workspace a fresh free port, in .env.local and the registry",
		Long: `Allocate a free port (same rules as "pigtail new"), rewrite its line in
.env.local and record it. Refuses while that server is running: "pigtail stop"
first. Without --kind, every port kind of the project is reassigned.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPortsPatch(args, pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: kinds})
		},
	}
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "port kind to reassign (repeatable); default: all")
	return cmd
}

func runPortsPatch(args []string, req pigtail.PatchPortsRequest) error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	var wsID string
	if len(args) == 1 {
		wsID = args[0]
	} else {
		id, err := resolveWorkspaceFromCwd(cx, c)
		if err != nil {
			return err
		}
		wsID = id
	}
	resp, err := c.PatchWorkspacePorts(cx, wsID, req)
	if err != nil {
		return err
	}
	printPortChanges(os.Stdout, req.Mode, resp)
	return nil
}

func printPortChanges(w io.Writer, mode string, resp *pigtail.PatchPortsResponse) {
	if len(resp.Changed) == 0 {
		fmt.Fprintf(w, "✓ %s ports already match .env.local\n", resp.WorkspaceID)
		return
	}
	if mode == pigtail.PortsModeReassign {
		fmt.Fprintf(w, "✓ %s reassigned\n", resp.WorkspaceID)
	} else {
		fmt.Fprintf(w, "✓ %s ports resynced from .env.local\n", resp.WorkspaceID)
	}
	for _, ch := range resp.Changed {
		fmt.Fprintf(w, "  %s  %d → %d\n", ch.Kind, ch.From, ch.To)
	}
	if mode == pigtail.PortsModeReassign {
		fmt.Fprintln(w, "  restart its server: pigtail dev")
	}
}

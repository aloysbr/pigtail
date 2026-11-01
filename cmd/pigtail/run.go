package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runCmd spawns an additional agent inside an existing workspace, without
// creating a worktree. This enables the multi-agent shared-worktree model:
// several agents working in one worktree reviewed on one dev server.
func runCmd() *cobra.Command {
	var profileID, agentKind, planPath, specPath string
	var planFirst, yolo, overMargin bool
	cmd := &cobra.Command{
		Use:   "run <workspace_id> <intent>",
		Short: "Spawn an agent inside an existing workspace (shared worktree)",
		Long: `Spawn an additional agent session inside an existing workspace without
creating a new worktree. Multiple agents can share one worktree (and one
dev server) — the orchestrator partitions subjects by file zones.

When another agent is already live in the workspace and no --plan is given,
the new agent's prompt is prefixed with the shared-worktree no-commit
discipline (the orchestrator owns all commits).

Examples:
  pigtail run ws_a1b2c3 "refactor the auth helper"
  pigtail run ws_a1b2c3 --plan spec.md "implement the reviewed plan"`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			workspaceID := args[0]
			intent := strings.Join(args[1:], " ")
			// --spec is an alias for --plan; last-set wins.
			if planPath == "" {
				planPath = specPath
			}
			req := pigtail.RunRequest{
				Intent:     intent,
				ProfileID:  profileID,
				AgentKind:  agentKind,
				OverMargin: overMargin,
			}
			if planPath != "" {
				abs, err := filepath.Abs(planPath)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(abs)
				if err != nil {
					return fmt.Errorf("read plan %s: %w", planPath, err)
				}
				req.PlanContent = string(data)
				req.PlanFilename = filepath.Base(abs)
			}
			if c.Flags().Changed("plan-first") {
				req.PlanFirst = &planFirst
			}
			if yolo {
				f := false
				req.PlanFirst = &f
			}
			client := newClient()
			cx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			resp, err := client.RunAgent(cx, workspaceID, req)
			if err != nil {
				return launchMarginExit(err)
			}
			printLaunchMargin(c.ErrOrStderr(), resp.Margin)
			fmt.Fprintf(c.OutOrStdout(), "✓ agent %s started in workspace %s\n", resp.AgentSessionID, resp.WorkspaceID)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileID, "profile", "", "profile_id (Claude account/config)")
	cmd.Flags().StringVar(&agentKind, "agent", "", "agent kind (claude, codex)")
	cmd.Flags().BoolVar(&overMargin, "over-margin", false, "launch even when every account of the profile's scope is over the usage margin (decision forced; refused otherwise, exit 3)")
	cmd.Flags().StringVar(&planPath, "plan", "", "path to a reviewed plan/spec to hand the agent")
	cmd.Flags().StringVar(&specPath, "spec", "", "alias for --plan")
	cmd.Flags().BoolVar(&planFirst, "plan-first", false, "force the agent to start in plan mode")
	cmd.Flags().BoolVar(&yolo, "yolo", false, "force autonomous mode (no plan mode)")
	return cmd
}

package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// hooksRunTimeout: the daemon bounds the hooks by devserver.HooksTimeout; the
// client waits that long plus a margin. A var so tests shorten it.
var hooksRunTimeout = devserver.HooksTimeout + time.Minute

func hooksCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "hooks", Short: "Post-create hooks of a workspace"}
	cmd.AddCommand(&cobra.Command{
		Use:   "run [workspace_id]",
		Short: "Run a workspace's post-create hooks again and record the outcome",
		Long: `Run the post-create hooks of pigtail.yaml again in a workspace, through the
daemon (same budget as at creation), and record the outcome shown by
pigtail list. Use it once the cause of a failed hook is fixed: session spawn
accepts the worktree again when the hooks pass.

With no argument, the workspace is inferred from the current directory.

Examples:
  pigtail hooks run              # the cwd's workspace
  pigtail hooks run ws_a1b2c3    # a specific workspace`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			cx, cancel := context.WithTimeout(context.Background(), hooksRunTimeout)
			defer cancel()
			var id string
			if len(args) == 1 {
				id = args[0]
			} else {
				resolved, err := resolveWorkspaceFromCwd(cx, c)
				if err != nil {
					return err
				}
				id = resolved
			}
			ws, err := c.RunWorkspaceHooks(cx, id)
			if err != nil {
				return err
			}
			return reportHooksRun(cmd.OutOrStdout(), ws)
		},
	})
	return cmd
}

// reportHooksRun prints the recorded outcome; a failed hook is an error (exit 1).
func reportHooksRun(w io.Writer, ws *pigtail.Workspace) error {
	h := ws.Hooks
	if h == nil {
		return fmt.Errorf("workspace %s: no hooks outcome recorded", ws.ID)
	}
	switch h.Status {
	case pigtail.HooksOK:
		fmt.Fprintf(w, "✓ hooks of %s: ok\n  log: %s\n", ws.ID, h.Log)
		return nil
	case pigtail.HooksFailed:
		return fmt.Errorf("workspace %s: post-create hook %q failed (%s) — log: %s", ws.ID, h.Hook, h.Error, h.Log)
	}
	return fmt.Errorf("workspace %s: hooks still %s — log: %s", ws.ID, h.Status, h.Log)
}

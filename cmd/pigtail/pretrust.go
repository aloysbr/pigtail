package main

import (
	"context"
	"time"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// preTrustFor pre-accepts Claude's folder trust for a launch in cwd under
// configDir, for a pigtail workspace only (D2): cwd's worktree root must be a
// Workspace path.
func preTrustFor(wss []pigtail.Workspace, configDir, cwd string) claudeconfig.Result {
	wt, err := gitbackend.WorktreeRoot(cwd)
	if err != nil {
		return claudeconfig.Result{Outcome: claudeconfig.NotEligible, Reason: cwd + " is not in a git worktree"}
	}
	for _, ws := range wss {
		if sameDir(ws.Path, wt) {
			return claudeconfig.PreTrustWorkspace(configDir, wt)
		}
	}
	return claudeconfig.Result{Outcome: claudeconfig.NotEligible, Reason: wt + " is not a pigtail workspace"}
}

// preTrustLaunch reads the workspaces from the daemon, then runs preTrustFor.
// A daemon that cannot answer is a failure, reported, never fatal.
func preTrustLaunch(ctx context.Context, c *pigtail.Client, configDir, cwd string) claudeconfig.Result {
	cx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	wss, err := c.Workspaces(cx)
	if err != nil {
		return claudeconfig.Result{Outcome: claudeconfig.Failed, Reason: "list pigtail workspaces: " + err.Error()}
	}
	return preTrustFor(wss, configDir, cwd)
}

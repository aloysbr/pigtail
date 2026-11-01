package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func adoptCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Register existing worktrees with the daemon",
		Long: `Scans the project container for git worktrees that are not yet tracked
by the daemon and registers them as workspaces.

Run from anywhere inside the project. Each worktree gets a workspace
entry with its branch, ports (from .env.local), and a "ready_to_review"
state. No agent is spawned.

Examples:
  pigtail adopt             # adopt all untracked worktrees
  pigtail adopt --dry-run   # show what would be adopted`,
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx, err := devserver.ResolveContext(cwd)
			if err != nil {
				return err
			}
			return runAdopt(ctx, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be adopted without registering")
	return cmd
}

func runAdopt(dctx *devserver.Context, dryRun bool) error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()

	// Non-destructive GC first: drop rows whose worktree no longer exists
	// (e.g. left behind by a manual `git worktree move`) so adopt's path
	// de-dup does not skip a relocated worktree. Skipped on dry-run.
	if !dryRun {
		if gc, err := c.GCWorkspaces(cx, false); err == nil && len(gc.DeletedIDs) > 0 {
			for _, id := range gc.DeletedIDs {
				fmt.Printf("  🧹 gc: removed orphaned workspace %s\n", id)
			}
		}
	}

	existing, err := c.Workspaces(cx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	tracked := map[string]bool{}
	for _, ws := range existing {
		tracked[ws.Path] = true
	}

	mainDir := filepath.Join(dctx.ContainerDir, dctx.Config.MainWorktree)
	out, err := exec.Command("git", "-C", mainDir, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree list: %w: %s", err, out)
	}

	projectID := "prj_" + sanitize(dctx.Config.Name)
	var adopted int

	var wtPath, branch string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			wtPath = strings.TrimPrefix(line, "worktree ")
			branch = ""
		case strings.HasPrefix(line, "branch refs/heads/"):
			branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "":
			if wtPath == "" || branch == "" {
				continue
			}
			if tracked[wtPath] {
				continue
			}
			// Skip main worktree — it's not a workspace.
			wtName := filepath.Base(wtPath)
			if wtName == dctx.Config.MainWorktree {
				wtPath, branch = "", ""
				continue
			}

			ports := readWorktreePorts(wtPath, dctx.Config)

			if dryRun {
				fmt.Printf("  would adopt: %s (branch %s, ports %v)\n", wtName, branch, ports)
			} else {
				ws := pigtail.Workspace{
					ID:          "ws_" + shortAdoptID(),
					ProjectID:   projectID,
					Intent:      branch,
					State:       pigtail.StateReadyToReview,
					Path:        wtPath,
					Ports:       ports,
					BackendKind: "git",
					BackendData: map[string]interface{}{
						"branch":      branch,
						"base_branch": "main",
					},
					CreatedAt:      time.Now().UTC(),
					LastActivityAt: time.Now().UTC(),
				}
				cx2, cancel2 := ctx()
				if _, err := c.AdoptWorkspace(cx2, ws); err != nil {
					fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", wtName, err)
					cancel2()
					continue
				}
				cancel2()
				fmt.Printf("  ✓ %s → %s (branch %s)\n", wtName, ws.ID, branch)
			}
			adopted++
			wtPath, branch = "", ""
		}
	}

	if adopted == 0 {
		fmt.Println("no untracked worktrees to adopt")
	} else if dryRun {
		fmt.Printf("\n%d worktree(s) would be adopted (dry run)\n", adopted)
	} else {
		fmt.Printf("\n✓ adopted %d worktree(s)\n", adopted)
	}
	return nil
}

func readWorktreePorts(wtPath string, cfg *pigtail.ProjectConfig) map[string]int {
	ports := map[string]int{}
	for kind, spec := range cfg.Ports {
		envVar := spec.Env
		if envVar == "" {
			envVar = pigtail.DefaultPortEnv(kind)
		}
		port, err := devserver.ReadPort(wtPath, envVar)
		if err == nil {
			ports[kind] = port
		}
	}
	return ports
}

func shortAdoptID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

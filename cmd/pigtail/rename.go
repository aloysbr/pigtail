package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func renameCmd() *cobra.Command {
	var force, renameBranch bool
	cmd := &cobra.Command{
		Use:     "rename [workspace_id] <new-slug>",
		Aliases: []string{"mv"},
		Short:   "Rename a workspace's worktree directory (and optionally its branch)",
		Long: `Relocate a workspace's worktree to a new slug under the project root.

With one argument, the workspace is inferred from the current directory.
With two arguments, the first is an explicit workspace id (ws_…).

The workspace id, state, agent sessions, tmux session names, and ports are
left unchanged. Pass --branch to also rename the git branch to the new slug.

Refuses to rename a workspace with a live agent; refuses a running dev
server unless --force (which stops it first).

Examples:
  pigtail rename auth-refactor              # rename the cwd's workspace
  pigtail rename ws_a1b2c3 auth-refactor    # rename a specific workspace
  pigtail rename auth-refactor --branch     # also rename the branch
  pigtail mv auth-refactor --force          # stop a running dev server first`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			c := newClient()
			cx, cancel := ctx()
			defer cancel()

			var wsID, slug string
			switch len(args) {
			case 2:
				wsID, slug = args[0], args[1]
			case 1:
				slug = args[0]
				resolved, err := resolveWorkspaceFromCwd(cx, c)
				if err != nil {
					return err
				}
				wsID = resolved
			}

			resp, err := c.RenameWorkspace(cx, wsID, pigtail.RenameRequest{
				NewSlug:      slug,
				Force:        force,
				RenameBranch: renameBranch,
			})
			if err != nil {
				if hint := daemonDesyncHint(cx, c, err); hint != "" {
					fmt.Fprintln(os.Stderr, hint)
				}
				return err
			}
			fmt.Printf("✓ renamed %s\n  %s → %s\n", resp.WorkspaceID, resp.OldPath, resp.NewPath)
			if resp.NewBranch != "" {
				fmt.Printf("  branch → %s\n", resp.NewBranch)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "stop a running dev server before renaming")
	cmd.Flags().BoolVar(&renameBranch, "branch", false, "also rename the git branch to the new slug")
	return cmd
}

func gcCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove workspace rows whose worktree no longer exists",
		Long: `Garbage-collect orphaned workspaces — rows whose worktree directory is
gone (e.g. after a manual 'git worktree move'). Workspaces with a live
agent are never collected.

Examples:
  pigtail gc            # delete orphaned workspace rows
  pigtail gc --dry-run  # list what would be deleted`,
		RunE: func(_ *cobra.Command, _ []string) error {
			c := newClient()
			cx, cancel := ctx()
			defer cancel()
			resp, err := c.GCWorkspaces(cx, dryRun)
			if err != nil {
				return err
			}
			if len(resp.DeletedIDs) == 0 {
				fmt.Println("no orphaned workspaces")
				return nil
			}
			verb := "deleted"
			if resp.DryRun {
				verb = "would delete"
			}
			for _, id := range resp.DeletedIDs {
				fmt.Printf("  %s %s\n", verb, id)
			}
			fmt.Printf("\n%s %d workspace(s)\n", verb, len(resp.DeletedIDs))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list orphans without deleting")
	return cmd
}

// resolveWorkspaceFromCwd resolves the workspace whose worktree contains the
// current directory by matching its worktree root against the daemon's
// workspace paths.
// errNotInWorktree: the directory is not inside a git worktree.
var errNotInWorktree = errors.New("not inside a git worktree")

// workspaceForDir finds the workspace whose worktree contains dir: the git
// worktree root of dir, matched with sameDir (Abs + EvalSymlinks), so a
// subdirectory or a symlinked path finds it too. (nil, root, nil) when no
// workspace matches; errNotInWorktree (wrapping git's diagnostic) when dir is
// not in a git worktree.
func workspaceForDir(cx context.Context, c *pigtail.Client, dir string) (*pigtail.Workspace, string, error) {
	root, err := gitbackend.WorktreeRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", errNotInWorktree, err)
	}
	all, err := c.Workspaces(cx)
	if err != nil {
		return nil, root, fmt.Errorf("list workspaces: %w", err)
	}
	for i := range all {
		if sameDir(all[i].Path, root) {
			return &all[i], root, nil
		}
	}
	return nil, root, nil
}

func resolveWorkspaceFromCwd(cx context.Context, c *pigtail.Client) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	ws, root, err := workspaceForDir(cx, c, cwd)
	if err != nil {
		return "", err
	}
	if ws == nil {
		return "", fmt.Errorf("no workspace matches the current worktree (%s); pass an explicit workspace id", root)
	}
	return ws.ID, nil
}

func sameDir(a, b string) bool {
	ca, _ := filepath.Abs(a)
	cb, _ := filepath.Abs(b)
	if ra, err := filepath.EvalSymlinks(ca); err == nil {
		ca = ra
	}
	if rb, err := filepath.EvalSymlinks(cb); err == nil {
		cb = rb
	}
	return filepath.Clean(ca) == filepath.Clean(cb)
}

// daemonDesyncHint returns a hint message when the rename failed in a way
// that suggests the running daemon is older than this CLI (it does not know
// the /rename route yet). Empty string when no such evidence.
func daemonDesyncHint(cx context.Context, c *pigtail.Client, callErr error) string {
	var apiErr *pigtail.APIError
	notFoundish := errors.As(callErr, &apiErr) &&
		(apiErr.Code == pigtail.ErrNotFound || apiErr.Code == pigtail.ErrWorkspaceNotFound)
	httpNotFound := strings.Contains(callErr.Error(), "HTTP 404") || strings.Contains(callErr.Error(), "HTTP 405")
	if !notFoundish && !httpNotFound {
		return ""
	}
	daemonVer, _, verr := c.Version(cx)
	if verr == nil && daemonVer != "" && daemonVer != Version {
		return fmt.Sprintf(
			"hint: the running daemon (%s) is older than this CLI (%s) and may not support `rename` — restart pigtaild (`pigtail teardown && pigtail setup`, or relaunch the launchd service)",
			daemonVer, Version)
	}
	return ""
}

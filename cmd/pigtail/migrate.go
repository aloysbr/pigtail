package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func migrateCmd() *cobra.Command {
	var mainName string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a sibling-layout project to the nested container layout",
		Long: `Converts a project from the old sibling layout (worktrees next to the
main repo) to the new nested layout (worktrees inside a container directory).

Run this from the main worktree (the directory containing .git). The command:

  1. Creates a container directory at ../<dirname> (moving the main worktree
     into <container>/<main-worktree>/).
  2. Moves all sibling worktrees into the container.
  3. Runs git worktree repair to fix internal paths.
  4. Writes pigtail.yaml in the container.

Example — before:
  projects/
  ├── my-app/                    ← you are here (main repo)
  ├── my-app-feat-auth/          ← sibling worktree
  └── my-app-fix-bug/            ← sibling worktree

After:
  projects/
  └── my-app/                    ← container
      ├── main/                  ← main worktree (moved)
      ├── feat-auth/             ← worktree (moved + renamed)
      ├── fix-bug/               ← worktree (moved + renamed)
      └── pigtail.yaml`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigrate(mainName, dryRun)
		},
	}
	cmd.Flags().StringVar(&mainName, "main-worktree", "main", "name for the main worktree subdirectory")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would happen without making changes")
	return cmd
}

func runMigrate(mainName string, dryRun bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Must be run from a git repo (the main worktree).
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err != nil {
		return fmt.Errorf("%s is not a git repository — run migrate from the main worktree", cwd)
	}

	repoName := filepath.Base(cwd)
	parentDir := filepath.Dir(cwd)

	// Discover sibling worktrees via git worktree list.
	out, err := exec.Command("git", "-C", cwd, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree list: %w: %s", err, out)
	}
	siblings := discoverSiblings(string(out), cwd, repoName, parentDir)

	// Plan the moves.
	containerDir := filepath.Join(parentDir, repoName+"-pigtail-migrate-tmp")
	finalContainerDir := filepath.Join(parentDir, repoName)
	mainDest := filepath.Join(containerDir, mainName)

	fmt.Println("Migration plan:")
	fmt.Printf("  container: %s\n", finalContainerDir)
	fmt.Printf("  main worktree: %s → %s/%s/\n", cwd, repoName, mainName)
	for _, s := range siblings {
		fmt.Printf("  worktree: %s → %s/%s/\n", s.path, repoName, s.shortName)
	}
	fmt.Println()

	if dryRun {
		fmt.Println("(dry run — no changes made)")
		return nil
	}

	// 1. Create temp container.
	fmt.Println("Creating container...")
	if err := os.MkdirAll(containerDir, 0o750); err != nil {
		return fmt.Errorf("mkdir container: %w", err)
	}

	// 2. Move main worktree into container.
	fmt.Printf("Moving main worktree → %s/\n", mainName)
	if err := os.Rename(cwd, mainDest); err != nil {
		return fmt.Errorf("move main worktree: %w", err)
	}

	// 3. Move sibling worktrees into container with shorter names.
	for _, s := range siblings {
		dest := filepath.Join(containerDir, s.shortName)
		fmt.Printf("Moving %s → %s/\n", filepath.Base(s.path), s.shortName)
		if err := os.Rename(s.path, dest); err != nil {
			fmt.Fprintf(os.Stderr, "  warn: could not move %s: %v\n", s.path, err)
		}
	}

	// 4. Rename temp container to final name.
	if err := os.Rename(containerDir, finalContainerDir); err != nil {
		return fmt.Errorf("rename container: %w (temp dir left at %s)", err, containerDir)
	}

	// 5. Repair worktree paths. We must pass the new worktree directories
	// as arguments so git can fix the gitdir pointers in both directions.
	mainFinal := filepath.Join(finalContainerDir, mainName)
	fmt.Println("Running git worktree repair...")
	repairArgs := []string{"-C", mainFinal, "worktree", "repair"}
	for _, s := range siblings {
		repairArgs = append(repairArgs, filepath.Join(finalContainerDir, s.shortName))
	}
	repairCmd := exec.Command("git", repairArgs...)
	repairCmd.Stdout = os.Stdout
	repairCmd.Stderr = os.Stderr
	if err := repairCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warn: git worktree repair: %v\n", err)
		fmt.Fprintf(os.Stderr, "  You may need to run it manually from %s\n", mainFinal)
	}

	// 6. Move extra files from parent that belonged to the old layout.
	moveIfExists(parentDir, finalContainerDir, "wt-manager.json")
	moveIfExists(parentDir, finalContainerDir, ".workspaces.json")

	fmt.Printf("\n✓ Migration complete.\n")
	fmt.Printf("  cd %s\n", finalContainerDir)
	fmt.Printf("  pigtail init    # to write pigtail.yaml and register with daemon\n")
	return nil
}

type siblingWorktree struct {
	path      string
	shortName string
}

func discoverSiblings(porcelain, mainPath, repoName, parentDir string) []siblingWorktree {
	var siblings []siblingWorktree

	for _, line := range strings.Split(porcelain, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		wtPath := strings.TrimPrefix(line, "worktree ")
		if wtPath == mainPath {
			continue
		}
		if filepath.Dir(wtPath) != parentDir {
			continue
		}
		base := filepath.Base(wtPath)
		short := shortenWorktreeName(base, repoName)
		siblings = append(siblings, siblingWorktree{path: wtPath, shortName: short})
	}
	return siblings
}

// shortenWorktreeName strips known prefixes to produce a compact directory name.
// Given repo "my-app" and worktree "my-app-feat-auth", returns "feat-auth".
// Also handles wt-manager convention where the prefix differs from the repo
// name (e.g. repo "big-monorepo", worktree "big-feat-cockpit").
func shortenWorktreeName(wtBase, repoName string) string {
	// Try exact prefix first: "my-app-"
	if strings.HasPrefix(wtBase, repoName+"-") {
		return strings.TrimPrefix(wtBase, repoName+"-")
	}
	// Try common prefix heuristic: find the longest shared prefix that ends
	// at a "-" boundary. This handles cases like repo="big-monorepo"
	// with worktrees named "big-feat-xyz".
	parts := strings.Split(repoName, "-")
	for i := len(parts); i > 0; i-- {
		prefix := strings.Join(parts[:i], "-") + "-"
		if strings.HasPrefix(wtBase, prefix) {
			return strings.TrimPrefix(wtBase, prefix)
		}
	}
	return wtBase
}

func moveIfExists(srcDir, dstDir, name string) {
	src := filepath.Join(srcDir, name)
	if _, err := os.Stat(src); err == nil {
		dst := filepath.Join(dstDir, name)
		if err := os.Rename(src, dst); err == nil {
			fmt.Printf("Moved %s into container\n", name)
		}
	}
}

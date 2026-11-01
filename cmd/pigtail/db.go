package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
)

// This is the one command group that reaches under the daemon instead of
// talking to it (architecture.md §10). It has to: it only ever runs while the
// daemon is stopped, and it refuses to run when the daemon answers. The db tool
// lives in internal/dbtool and is reached through `pigtaild -db-tool`, so that
// this binary does not link the SQLite driver.

func dbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Inspect and restore the daemon's database backups",
	}
	cmd.AddCommand(dbListCmd(), dbRestoreCmd())
	return cmd
}

func dbListCmd() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the database and its backup generations",
		Long: `Shows the live database and each backup generation, with its age,
size, integrity check result and row counts.

Read-only: the generations are opened read-only and nothing is modified.
Use it before you need it — a generation that fails its integrity check is
better discovered now than during a restore.`,
		RunE: func(c *cobra.Command, _ []string) error {
			return runDB(c, "list", dbPath)
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "database path (default: ~/.config/pigtail/pigtail.db)")
	return cmd
}

// runDB resolves the caller's view from the cell, then hands the command to
// the db tool. The view is resolved here, never in the db tool: the cell is the
// CLI's identity (CLAUDE.md § Subject scope).
func runDB(c *cobra.Command, sub, dbPath string, extra ...string) error {
	view, err := dbView()
	if err != nil {
		return err
	}
	return runDBTool(c, dbToolArgs(sub, view, dbPath, extra...))
}

// dbToolArgs is the db tool's argv after -db-tool.
func dbToolArgs(sub string, view scope.Scope, dbPath string, extra ...string) []string {
	args := []string{sub, "-view", string(view)}
	if dbPath != "" {
		args = append(args, "-db", dbPath)
	}
	return append(args, extra...)
}

// runDBTool runs the db tool in pigtaild (`pigtaild -db-tool …`), which links
// the SQLite driver so that this binary does not. The terminal is inherited:
// restore's prompt and its TTY test see the user's terminal.
var runDBTool = func(c *cobra.Command, argv []string) error {
	bin, err := pigtaildPathFn()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, append([]string{"-db-tool"}, argv...)...)
	var stderrSeen bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, &stderrSeen)
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee) && ee.ExitCode() == 2 && strings.Contains(stderrSeen.String(), "flag provided but not defined"):
		// Go's flag package on an unknown -db-tool. The text, not the code: a
		// panic of the db tool also exits 2. stderrSeen tees the child's stderr.
		return withExitCode(1, fmt.Errorf("%s does not know -db-tool: it is older than this pigtail — run `make install` (it installs pigtail and pigtaild together)", bin))
	case errors.As(err, &ee):
		c.SilenceErrors = true // the db tool printed "Error: …" already
		code := ee.ExitCode()
		if code < 1 {
			code = 1 // killed by a signal
		}
		return withExitCode(code, fmt.Errorf("pigtail db %s: pigtaild exited %d", argv[0], ee.ExitCode()))
	default:
		return fmt.Errorf("run %s: %w", bin, err)
	}
}

// pigtaildPathFn locates pigtaild; a var for tests.
var pigtaildPathFn = func() (string, error) {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	return pigtaildPathFrom(self, exec.LookPath)
}

// pigtaildPathFrom prefers the pigtaild installed beside this binary (make
// install puts both in one directory; a symlinked pigtail is resolved first),
// then PATH.
func pigtaildPathFrom(self string, lookPath func(string) (string, error)) (string, error) {
	if self != "" {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		sib := filepath.Join(filepath.Dir(self), "pigtaild")
		if fi, err := os.Stat(sib); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return sib, nil
		}
	}
	if p, err := lookPath("pigtaild"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("pigtail db runs in pigtaild, which is neither beside %s nor on PATH — run `make install`", self)
}

// dbView is the scope the subject counts are shown for: the calling Claude
// cell's (callerScope), the same identity the daemon's subject routes use. A
// non-master cell is shown its own scope's subjects only.
func dbView() (scope.Scope, error) {
	sc, err := callerScope()
	if err != nil {
		return "", err
	}
	return scope.FromHeader(sc)
}

// daemonIsUp reports whether a daemon answers. A package var so tests can
// exercise both branches without standing up a socket.
var daemonIsUp = func() bool {
	cx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return newClient().Health(cx) == nil
}

func dbRestoreCmd() *cobra.Command {
	var dbPath string
	var generation int
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Replace the database with one of its backup generations",
		Long: `Replaces the live database with a backup generation.

The daemon must be stopped: it holds the database open. The command refuses
to run while it answers.

Nothing is destroyed. The live database is moved aside to
pigtail.db.pre-restore-<timestamp>, and the generation is copied rather than
moved, so both a second attempt and a change of mind stay possible.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if daemonIsUp() {
				return fmt.Errorf("%s", daemonRunningMessage())
			}
			extra := []string{"-generation", strconv.Itoa(generation)}
			if dryRun {
				extra = append(extra, "-dry-run")
			}
			if yes {
				extra = append(extra, "-yes")
			}
			return runDB(c, "restore", dbPath, extra...)
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "database path (default: ~/.config/pigtail/pigtail.db)")
	cmd.Flags().IntVar(&generation, "generation", 0, "which generation to restore (0 = newest)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would happen without making changes")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// daemonRunningMessage names the literal commands, because this is read by
// someone whose database has just broken.
func daemonRunningMessage() string {
	return fmt.Sprintf(`the daemon is running and holds the database open.

Stop it, restore, then start it again:

  launchctl bootout gui/$(id -u)/%s
  pigtail db restore --generation N --yes
  launchctl bootstrap gui/$(id -u) %s

If you started pigtaild by hand rather than through launchd, stop that
process instead.`, setup.Label(), setup.PlistPath())
}

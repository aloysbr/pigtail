// Command pigtail is the CLI and TUI client for the pigtail daemon.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/buildinfo"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/tui"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Version of this binary. Stamped at build time by the Makefile
// (-ldflags -X main.Version); "dev" is an unstamped `go build`.
var Version = "dev"

var (
	flagSocket string
	flagHTTP   string
	flagToken  string
)

func main() {
	// Deliberate stops made by this CLI are announced to the daemon so its
	// death detector does not report them. Wired here and only here: pigtaild
	// links internal/devserver too (watchdog kill path) and must keep the
	// package default no-op — it marks its own stops in-process.
	devserver.SetStopNotifier(notifyDaemonServerStopped)
	// The scope table, from the dir the daemon reads it from (setup.ConfigDir,
	// beside the cell table): before any command parses a scope.
	loadScopeTable(setup.ConfigDir(), os.Stderr)
	// settings.yaml (a bad file is the daemon's log line, not the CLI's): how
	// notices and pilot texts name the human, and the user's own commands the
	// hooks and the help name (none by default).
	us, _ := usersettings.Load(setup.ConfigDir())
	maintenance.HumanName = us.Human.Name
	maintenance.FreezeAlsoRefuses = us.Maintenance.FreezeAlsoRefuses
	cmuxcheck.HumanName = maintenance.HumanName
	waittype.Configure(us.WaitCommands())

	if err := newRootCmd().Execute(); err != nil {
		os.Exit(exitCode(err))
	}
}

// newRootCmd builds the whole command tree. Split out of main so tests can
// drive the real CLI end-to-end (cobra parsing, arg validation, RunE) instead
// of a hand-rolled stand-in.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "pigtail",
		Short: "Supervise N AI agents working in parallel",
		Long: `pigtail is the CLI/TUI client for the pigtaild daemon.

Run pigtaild in the background (or via launchd) and use this command to
dispatch agents, list workspaces, and attach to running sessions.

With no subcommand, opens the triage TUI.`,
		// The TUI subscribes to GET /events, which filters subject-bearing
		// events by the caller's scope.
		Annotations: scopedRead(),
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			return tui.Run(c, resolveLocalProjectID())
		},
	}

	root.PersistentFlags().StringVar(&flagSocket, "socket", "", "Unix socket path (default: ~/.config/pigtail/pigtail.sock)")
	root.PersistentFlags().StringVar(&flagHTTP, "http", "", "HTTP loopback address (overrides socket if set)")
	root.PersistentFlags().StringVar(&flagToken, "token", "", "HTTP bearer token (default: read from ~/.config/pigtail/token)")
	root.PersistentFlags().StringVar(&flagScope, "scope", "", "subject view: a scope name (or alias) or all narrows subject list/next, theme list and board; never changes the Claude cell's own scope (a non-master cell accepts only its own scope; outside any cell, the master scope or all speak for the master, else the default scope; env "+envScope+")")

	root.AddCommand(versionCmd())
	root.AddCommand(skillsCmd())
	root.AddCommand(initCmd())
	root.AddCommand(dispatchCmd())
	root.AddCommand(newCmd())
	root.AddCommand(runCmd())
	root.AddCommand(listCmd())
	root.AddCommand(attachCmd())
	root.AddCommand(cleanCmd())
	root.AddCommand(replyCmd())
	root.AddCommand(devCmd())
	root.AddCommand(startCmd())
	root.AddCommand(sbCmd())
	root.AddCommand(stopServerCmd())
	root.AddCommand(adoptCmd())
	root.AddCommand(renameCmd())
	root.AddCommand(hooksCmd())
	root.AddCommand(gcCmd())
	root.AddCommand(themeCmd())
	root.AddCommand(subjectCmd())
	root.AddCommand(captureCmd())
	root.AddCommand(focusCmd())
	root.AddCommand(handoffCmd())
	root.AddCommand(boardCmd())
	root.AddCommand(webCmd(), retiredWebAliasCmd())
	root.AddCommand(serveCmd())
	root.AddCommand(serversCmd())
	root.AddCommand(batteryCmd())
	root.AddCommand(lidCmd())
	root.AddCommand(purgeCmd())
	root.AddCommand(slotsCmd())
	root.AddCommand(testCmd())
	root.AddCommand(admitCmd())
	root.AddCommand(hookCmd())
	root.AddCommand(migrateCmd())
	root.AddCommand(dbCmd())
	root.AddCommand(staffingCmd())
	root.AddCommand(setupCmd(root))
	root.AddCommand(teardownCmd())
	root.AddCommand(sessionCmd())
	root.AddCommand(sdkCmd())
	root.AddCommand(msgCmd())
	root.AddCommand(waitCmd())
	root.AddCommand(cmuxCmd())
	root.AddCommand(journalCmd())
	root.AddCommand(alertsCmd())
	root.AddCommand(roleCmd())
	root.AddCommand(daemonCmd())
	root.AddCommand(maintenanceCmd())
	root.AddCommand(runtimeCmd())
	root.AddCommand(portsCmd())
	root.AddCommand(usageCmd())

	// Last, so it covers every command added above: a failure from inside a
	// RunE is an operational failure, not a misuse, and must print its error
	// without a usage dump burying it. See silenceUsageOnRunE.
	silenceUsageOnRunE(root)

	return root
}

// --- client construction --------------------------------------------------

func newClient() *pigtail.Client {
	if flagHTTP != "" {
		token := flagToken
		if token == "" {
			token = readTokenFile()
		}
		return pigtail.NewHTTPClient(flagHTTP, token).WithSession(os.Getenv("CLAUDE_CODE_SESSION_ID"))
	}
	return pigtail.NewSocketClient(socketPath()).WithSession(os.Getenv("CLAUDE_CODE_SESSION_ID"))
}

// socketPath is the daemon socket this CLI talks to: --socket if given, else
// $PIGTAIL_SOCKET, else the client's own default (~/.config/pigtail).
//
// The environment variable exists so a command that re-enters pigtail —
// `pigtail admit -- npm test` running a script that calls pigtail again, or
// anything under a test daemon — reaches the same daemon without every caller
// having to thread --socket through. The Node CPU-slot client already reads
// it (clients/node/client.mjs), so this makes the two agree.
func socketPath() string {
	if flagSocket != "" {
		return flagSocket
	}
	return os.Getenv("PIGTAIL_SOCKET")
}

// --- deliberate-stop notification ------------------------------------------

// stopNotifyTimeout bounds the whole notification (workspace lookup + mark).
// Short on purpose: a stop must never wait on the daemon.
const stopNotifyTimeout = 2 * time.Second

// workspaceCache memoises the workspace list for the lifetime of one CLI
// invocation. `pigtail stop --all` stops many servers across many worktrees;
// looking the list up once keeps that a single daemon round-trip.
var workspaceCache struct {
	once sync.Once
	list []pigtail.Workspace
	err  error
}

// notifyDaemonServerStopped tells the daemon that a dev server this CLI just
// stopped went away on purpose. internal/devserver reasons in worktree
// directories, not workspaces, so the workspace is resolved here — the CLI is
// the only layer that has both the daemon and the on-disk view.
//
// Entirely best-effort: no daemon, no matching workspace, or a failing call
// all mean "send nothing". The cost of silence is one false death reported,
// never a real one missed.
func notifyDaemonServerStopped(worktreeDir, serverName string) {
	if worktreeDir == "" || serverName == "" {
		return
	}
	cx, cancel := context.WithTimeout(context.Background(), stopNotifyTimeout)
	defer cancel()
	c := newClient()
	workspaceCache.once.Do(func() {
		workspaceCache.list, workspaceCache.err = c.Workspaces(cx)
	})
	if workspaceCache.err != nil {
		return
	}
	if id, ok := workspaceIDForDir(workspaceCache.list, worktreeDir); ok {
		_ = c.MarkServerStopped(cx, id, serverName)
	}
}

// workspaceIDForDir finds the workspace whose on-disk path IS the given
// worktree directory. Pure, so the resolution can be tested without a daemon.
//
// It matches on the full path, never on the directory's base name: the
// workspace list spans every project the daemon knows, and `main` is the main
// worktree of every pigtail project (cfg.MainWorktree). With two projects
// registered — the ordinary case — a base-name match returns whichever project
// the daemon listed first, so `pigtail stop` in projectA/main would mark
// projectB/main's server as deliberately stopped. Two failures at once: the
// real stop is never marked and comes back as a ✝ death on the next sweep,
// and the homonym gets a spurious 60s mark under which a genuine death of ITS
// server would be swallowed.
func workspaceIDForDir(workspaces []pigtail.Workspace, worktreeDir string) (string, bool) {
	want := canonicalPath(worktreeDir)
	for _, ws := range workspaces {
		if canonicalPath(ws.Path) == want {
			return ws.ID, true
		}
	}
	return "", false
}

// canonicalPath normalises a path for comparison: symlinks resolved when the
// path exists (on macOS /tmp is a symlink to /private/tmp, and the daemon and
// the CLI need not have spelled it the same way), plain lexical cleaning
// otherwise — a worktree may legitimately have been deleted by the time its
// stop is announced.
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

func readTokenFile() string {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".config", "pigtail", "token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// --- version --------------------------------------------------------------

func versionCmd() *cobra.Command {
	return versionCmdWith(func() (pigtail.VersionInfo, error) {
		cx, cancel := ctx()
		defer cancel()
		return newClient().VersionInfo(cx)
	})
}

// versionCmdWith prints this client's build identity, or with --daemon the
// daemon's (GET /version, through fetch); an unreachable daemon exits 1 with
// the cause on stderr.
func versionCmdWith(fetch func() (pigtail.VersionInfo, error)) *cobra.Command {
	var daemon bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the client's version and build (branch, commit, dirty); --daemon: the daemon's",
		// A RunE failure is the daemon unreachable, not a misuse: no usage dump.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !daemon {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Line("pigtail", Version))
				return nil
			}
			vi, err := fetch()
			if err != nil {
				return fmt.Errorf("daemon version: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(),
				buildinfo.Format("pigtaild", vi.DaemonVersion, vi.DaemonBranch, vi.DaemonCommit, vi.DaemonDirty))
			return nil
		},
	}
	cmd.Flags().BoolVar(&daemon, "daemon", false, "print the running daemon's version and build (GET /version)")
	return cmd
}

// --- init -----------------------------------------------------------------

func initCmd() *cobra.Command {
	var mainWorktree string
	var yes bool
	var base string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a pigtail project",
		Long: `Initialize a pigtail project. Can be run from:

  1. A container directory (already has pigtail.yaml or worktree children)
     → writes pigtail.yaml and registers the project.

  2. A git repository (has .git)
     → creates a container layout by moving the repo into a "main/"
     subdirectory, then writes pigtail.yaml and registers. Existing
     sibling worktrees are moved in and renamed automatically.

Restructuring a git repo asks first (default: no); without a terminal it
refuses unless --yes is given. base_ref is detected from the repo (origin's
default branch, else the current branch); --base overrides it.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			// Case 1: already a container (pigtail.yaml exists).
			if _, err := os.Stat(filepath.Join(cwd, pigtail.ProjectConfigFilename)); err == nil {
				return registerContainer(cwd)
			}

			// Case 2: inside a git repo → restructure into container layout.
			if _, err := os.Stat(filepath.Join(cwd, ".git")); err == nil {
				return initFromGitRepo(cwd, mainWorktree, yes, base)
			}

			// Case 3: empty/new directory → write config and register.
			return initFreshContainer(cwd)
		},
	}
	cmd.Flags().StringVar(&mainWorktree, "main-worktree", "main", "name for the main worktree subdirectory")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept the restructuring without asking (required when stdin is not a terminal)")
	cmd.Flags().StringVar(&base, "base", "", "base ref new branches start from (default: detected from the repo)")
	return cmd
}

// initFromGitRepo restructures a git repo into a container layout, then registers.
// baseOverride, when set, wins over the detected base_ref.
func initFromGitRepo(repoDir, mainName string, skipConfirm bool, baseOverride string) error {
	repoName := filepath.Base(repoDir)
	parentDir := filepath.Dir(repoDir)

	// Nothing is moved before consent and a usable base ref are settled.
	baseRef := baseOverride
	if baseRef == "" {
		var err error
		if baseRef, err = detectBaseRef(repoDir); err != nil {
			return err
		}
	}
	if !skipConfirm && !initIsTTY() {
		return fmt.Errorf("pigtail init would move %s into %s/%s/ — rerun with --yes to accept", repoDir, repoDir, mainName)
	}

	// Discover sibling worktrees.
	out, err := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree list: %w: %s", err, out)
	}
	siblings := discoverSiblings(string(out), repoDir, repoName, parentDir)

	fmt.Printf("Detected git repo: %s\n\n", repoDir)
	fmt.Printf("pigtail will restructure into a container layout:\n")
	fmt.Printf("  %s/              ← container\n", repoName)
	fmt.Printf("  ├── %s/            ← main worktree (your repo)\n", mainName)
	for _, s := range siblings {
		fmt.Printf("  ├── %s/\n", s.shortName)
	}
	fmt.Printf("  └── pigtail.yaml\n\n")

	if !skipConfirm {
		fmt.Print("Proceed? [y/N] ")
		var answer string
		_, _ = fmt.Fscanln(initStdin, &answer)
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Use a temp name to avoid collision (repo dir has the same name as container).
	tmpContainer := filepath.Join(parentDir, repoName+"-pigtail-init-tmp")
	if err := os.MkdirAll(tmpContainer, 0o750); err != nil {
		return fmt.Errorf("mkdir container: %w", err)
	}

	// Move main repo into container.
	mainDest := filepath.Join(tmpContainer, mainName)
	fmt.Printf("Moving repo → %s/\n", mainName)
	if err := os.Rename(repoDir, mainDest); err != nil {
		_ = os.Remove(tmpContainer)
		return fmt.Errorf("move repo: %w", err)
	}

	// Move sibling worktrees.
	for _, s := range siblings {
		dest := filepath.Join(tmpContainer, s.shortName)
		fmt.Printf("Moving %s → %s/\n", filepath.Base(s.path), s.shortName)
		if err := os.Rename(s.path, dest); err != nil {
			fmt.Fprintf(os.Stderr, "  warn: could not move %s: %v\n", s.path, err)
		}
	}

	// Rename temp to final.
	finalContainer := filepath.Join(parentDir, repoName)
	if err := os.Rename(tmpContainer, finalContainer); err != nil {
		return fmt.Errorf("rename container: %w (temp left at %s)", err, tmpContainer)
	}

	// Repair worktree paths.
	mainFinal := filepath.Join(finalContainer, mainName)
	fmt.Println("Running git worktree repair...")
	repairArgs := []string{"-C", mainFinal, "worktree", "repair"}
	for _, s := range siblings {
		repairArgs = append(repairArgs, filepath.Join(finalContainer, s.shortName))
	}
	repairCmd := exec.Command("git", repairArgs...)
	repairCmd.Stdout = os.Stdout
	repairCmd.Stderr = os.Stderr
	if err := repairCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warn: git worktree repair: %v\n", err)
	}

	// Move legacy config files.
	moveIfExists(parentDir, finalContainer, "wt-manager.json")
	moveIfExists(parentDir, finalContainer, ".workspaces.json")

	// Write pigtail.yaml and register.
	fmt.Println()
	if err := pigtail.WriteDefaultProjectConfig(finalContainer, repoName); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := setBaseRef(finalContainer, baseRef); err != nil {
		return fmt.Errorf("set base_ref: %w", err)
	}
	fmt.Printf("✓ created %s/pigtail.yaml (base_ref: %s)\n", repoName, baseRef)

	return registerContainer(finalContainer)
}

// initFreshContainer writes pigtail.yaml in a new/empty directory and registers.
func initFreshContainer(dir string) error {
	if err := pigtail.WriteDefaultProjectConfig(dir, filepath.Base(dir)); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	fmt.Printf("✓ created %s\n", filepath.Join(dir, pigtail.ProjectConfigFilename))
	return registerContainer(dir)
}

// registerContainer loads pigtail.yaml from dir and registers the project with the daemon.
func registerContainer(dir string) error {
	cfg, err := pigtail.LoadProjectConfig(dir)
	if err != nil {
		return err
	}

	// Check main worktree exists.
	mainWT := filepath.Join(dir, cfg.MainWorktree)
	if _, err := os.Stat(mainWT); err != nil {
		fmt.Fprintf(os.Stderr, "warn: main worktree %s/ does not exist yet\n", cfg.MainWorktree)
		fmt.Fprintf(os.Stderr, "  Create it with: git clone <url> %s\n\n", cfg.MainWorktree)
	}

	ranges := make(map[string]pigtail.PortRange, len(cfg.Ports))
	envs := make(map[string]string, len(cfg.Ports))
	for kind, spec := range cfg.Ports {
		ranges[kind] = spec.Range
		if spec.Env != "" && spec.Env != pigtail.DefaultPortEnv(kind) {
			envs[kind] = spec.Env
		}
	}
	if len(envs) == 0 {
		envs = nil
	}
	proj := pigtail.Project{
		ID:             "prj_" + sanitize(cfg.Name),
		Name:           cfg.Name,
		RootPath:       dir,
		MainWorktree:   cfg.MainWorktree,
		DefaultBackend: cfg.Backend,
		PortRanges:     ranges,
		PortEnvs:       envs,
		Defaults:       cfg.Defaults,
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	if _, err := c.CreateProject(cx, proj); err != nil {
		var apiErr *pigtail.APIError
		if errors.As(err, &apiErr) {
			fmt.Fprintf(os.Stderr, "warn: register project failed (%s) — daemon may not yet support /projects\n", apiErr.Code)
			return nil
		}
		fmt.Fprintf(os.Stderr, "warn: register project failed: %v\n", err)
		return nil
	}
	fmt.Printf("✓ registered project %s with daemon\n", proj.ID)
	fmt.Printf("  container: %s\n", dir)
	fmt.Printf("  main worktree: %s/\n", cfg.MainWorktree)
	return nil
}

// --- dispatch -------------------------------------------------------------

func dispatchCmd() *cobra.Command {
	var projectID, recipeID, profileID, agentKind, branch, baseRef, planPath, specPath string
	var planFirst, yolo, force, noFetch, overMargin bool
	cmd := &cobra.Command{
		Use:   "dispatch <intent>",
		Short: "Dispatch an agent on a new workspace",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			intent := strings.Join(args, " ")
			if projectID == "" {
				projectID = resolveLocalProjectID()
			}
			// --spec is an alias for --plan; last-set wins.
			if planPath == "" {
				planPath = specPath
			}
			req := pigtail.DispatchRequest{
				ProjectID:      projectID,
				Intent:         intent,
				RecipeID:       recipeID,
				ProfileID:      profileID,
				AgentKind:      agentKind,
				BranchOverride: branch,
				BaseRef:        baseRef,
				NoFetch:        noFetch,
				Force:          force,
				OverMargin:     overMargin,
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
			resp, err := client.Dispatch(cx, req)
			if err != nil {
				return launchMarginExit(err)
			}
			printLaunchMargin(c.ErrOrStderr(), resp.Margin)
			fmt.Fprintf(c.OutOrStdout(), "✓ dispatched · workspace %s · agent %s\n", resp.WorkspaceID, resp.AgentSessionID)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project_id (defaults to prj_<cwd basename>)")
	cmd.Flags().StringVar(&recipeID, "recipe", "", "recipe_id")
	cmd.Flags().StringVar(&profileID, "profile", "", "profile_id (Claude account/config)")
	cmd.Flags().StringVar(&agentKind, "agent", "", "agent kind (claude, codex)")
	cmd.Flags().StringVar(&branch, "branch", "", "branch override (skip slug derivation)")
	cmd.Flags().StringVar(&baseRef, "base", "", "base ref to branch from (default: project base_ref; a remote-tracking base like origin/main is auto-fetched first)")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip the auto-fetch of the base ref's remote before branching")
	cmd.Flags().StringVar(&planPath, "plan", "", "path to a reviewed plan/spec to hand the agent")
	cmd.Flags().StringVar(&specPath, "spec", "", "alias for --plan")
	cmd.Flags().BoolVar(&planFirst, "plan-first", false, "force the agent to start in plan mode")
	cmd.Flags().BoolVar(&yolo, "yolo", false, "force autonomous mode (no plan mode)")
	cmd.Flags().BoolVar(&force, "force", false, "bypass the anti-collision guard")
	cmd.Flags().BoolVar(&overMargin, "over-margin", false, "launch even when every account of the profile's scope is over the usage margin (decision forced; refused otherwise, exit 3)")
	return cmd
}

// --- list -----------------------------------------------------------------

func listCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workspaces",
		RunE: func(_ *cobra.Command, _ []string) error {
			c := newClient()
			cx, cancel := ctx()
			defer cancel()
			out, err := c.Workspaces(cx)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			// Best-effort, like the backup line of `pigtail servers`: a daemon
			// that cannot answer GET /servers leaves the list without markers.
			var conflicts []pigtail.PortConflictView
			if srv, err := c.Servers(cx); err == nil {
				conflicts = srv.PortConflicts
			}
			printWorkspaces(os.Stdout, out, conflicts)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// printWorkspaces prints the workspace table. A workspace with a port
// conflict is marked ⚠port in STATE — the marker only: the fix is printed by
// `pigtail servers`.
func printWorkspaces(w io.Writer, out []pigtail.Workspace, conflicts []pigtail.PortConflictView) {
	if len(out) == 0 {
		fmt.Fprintln(w, "no workspaces")
		return
	}
	portConflict := map[string]bool{}
	for _, c := range conflicts {
		portConflict[c.WorkspaceID] = true
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tID\tBRANCH\tAGENT\tAGE\tINTENT")
	for _, ws := range out {
		branch := ws.DisplayRef
		agentKind := "-"
		if len(ws.AgentSessions) > 0 {
			agentKind = ws.AgentSessions[0].AgentKind
		}
		age := humaneDuration(time.Since(ws.LastActivityAt))
		intent := truncate(ws.Intent, 40)
		state := string(ws.State)
		if recentRunaway(ws) {
			state += " ⚠runaway"
		}
		if portConflict[ws.ID] {
			state += " ⚠port"
		}
		state += hooksMarker(ws.Hooks)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", state, ws.ID, branch, agentKind, age, intent)
	}
	tw.Flush()
}

// recentRunaway reports whether the watchdog killed a runaway dev server on
// this workspace within the last hour — recent enough to warrant a marker
// in the list output.
func recentRunaway(ws pigtail.Workspace) bool {
	return ws.LastRunawayAt != nil && time.Since(*ws.LastRunawayAt) < time.Hour
}

func humaneDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// --- attach ---------------------------------------------------------------

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <workspace_id>",
		Short: "Attach to the tmux session of a workspace's agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id := args[0]
			c := newClient()
			cx, cancel := ctx()
			defer cancel()
			out, err := c.Workspaces(cx)
			if err != nil {
				return err
			}
			for _, ws := range out {
				if ws.ID != id {
					continue
				}
				if len(ws.AgentSessions) == 0 {
					return fmt.Errorf("workspace %s has no agent sessions", id)
				}
				name := ws.AgentSessions[0].TmuxSessionName
				if name == "" {
					return fmt.Errorf("agent has no tmux_session_name")
				}
				tmux, err := exec.LookPath("tmux")
				if err != nil {
					fmt.Printf("tmux not in PATH — run:\n  tmux attach -t %s\n", name)
					return nil
				}
				attachCmd := exec.Command(tmux, "attach", "-t", name)
				attachCmd.Stdin, attachCmd.Stdout, attachCmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				return attachCmd.Run()
			}
			return fmt.Errorf("workspace %s not found", id)
		},
	}
}

// --- reply ----------------------------------------------------------------

func replyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reply <workspace_id_or_agent_id> [content...]",
		Short: "Send input to a running agent (avoids tmux attach)",
		Long: `Forward text to an agent's tmux pane as if you had typed it there.
If <id> is a workspace id, the (single) running agent in that workspace
is used; if it's an agent_session_id (agt_…), that session is used
directly. Content can be passed as remaining args, or piped on stdin.

Examples:
  pigtail reply ws_a1b2c3 "go ahead"
  echo "use the auth helper instead" | pigtail reply ws_a1b2c3`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			target := args[0]
			content := strings.Join(args[1:], " ")
			if content == "" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				content = strings.TrimRight(string(b), "\n")
			}
			if content == "" {
				return fmt.Errorf("no content to send (pass as args or pipe on stdin)")
			}

			c := newClient()
			cx, cancel := ctx()
			defer cancel()

			agentID := target
			if strings.HasPrefix(target, "ws_") {
				ws, err := c.Workspaces(cx)
				if err != nil {
					return err
				}
				var found *pigtail.AgentSession
				for _, w := range ws {
					if w.ID != target {
						continue
					}
					active := []pigtail.AgentSession{}
					for i := range w.AgentSessions {
						st := w.AgentSessions[i].State
						if st == pigtail.AgentStateRunning || st == pigtail.AgentStatePausedInput {
							active = append(active, w.AgentSessions[i])
						}
					}
					if len(active) == 0 {
						return fmt.Errorf("workspace %s has no active agent", target)
					}
					if len(active) > 1 {
						ids := make([]string, len(active))
						for i := range active {
							ids[i] = active[i].ID
						}
						return fmt.Errorf("workspace %s has %d active agents — pass one of: %s",
							target, len(active), strings.Join(ids, ", "))
					}
					found = &active[0]
					break
				}
				if found == nil {
					return fmt.Errorf("workspace %s not found", target)
				}
				agentID = found.ID
			}

			return c.ReplyAgent(cx, agentID, pigtail.ReplyRequest{Content: content})
		},
	}
	return cmd
}

// --- clean ----------------------------------------------------------------

// cleanTimeout is the per-target budget for a teardown. Removing a worktree
// with a populated node_modules is minutes of unlink() calls, so the shared
// 10s ctx() helper is far too short here — `new`/`run`/`dispatch` already
// depart from it for the same reason. Overridable with --timeout, and a
// package var so tests can shrink it.
var cleanTimeout = 10 * time.Minute

// printCleanOutcome reports a finished teardown. A teardown that left a
// branch behind unexpectedly has to say so here: the daemon log is not where
// the user is looking, and a bare "✓ cleaned" reads as "everything is gone"
// (issue #53). The dev servers it stopped first are named too.
func printCleanOutcome(w io.Writer, id string, out *pigtail.CleanupResponse) {
	fmt.Fprintf(w, "✓ cleaned %s\n", id)
	if out == nil {
		return
	}
	for _, s := range out.ServersStopped {
		pids := make([]string, len(s.PIDs))
		for i, p := range s.PIDs {
			pids[i] = strconv.Itoa(p)
		}
		fmt.Fprintf(w, "  ○ stopped %s (:%d, pid %s)\n", strings.Join(s.Kinds, "+"), s.Port, strings.Join(pids, ","))
	}
	if out.BranchKept != "" {
		fmt.Fprintf(w, "  ⚠ branch %s kept: %s\n", out.BranchKept, out.BranchKeptReason)
	}
}

func cleanCmd() *cobra.Command {
	var force, keepBranch, shipped, merged, dryRun bool
	timeout := cleanTimeout
	cmd := &cobra.Command{
		Use:   "clean [workspace_id...]",
		Short: "Remove a workspace (worktree + branch + ports)",
		Long: `Tear down one or more workspaces. By default, refuses to clean a
workspace with uncommitted changes or a running agent — pass --force to
override.

Dev servers running inside the worktree (and their child processes) are
stopped first. If one cannot be stopped, or its owner cannot be established,
the clean refuses and removes nothing — the error names the port and pid, and
--force does not lift it.

Examples:
  pigtail clean ws_a1b2c3            # clean one by id
  pigtail clean --shipped            # clean every workspace in 'shipped' state
  pigtail clean --merged             # clean every workspace whose branch is merged into its base
  pigtail clean ws_a1b2c3 --force    # ignore safety guards
  pigtail clean --merged --dry-run   # list what would be cleaned`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newClient()
			// Listing workspaces is a fast read; the 10s default is fine. The
			// containment scan and the teardowns below each get their own,
			// much larger budget.
			listCx, cancelList := ctx()
			defer cancelList()

			var targets []string
			reasons := map[string]string{} // id -> why it was selected
			selected := map[string]bool{}
			if shipped || merged {
				all, err := c.Workspaces(listCx)
				if err != nil {
					return err
				}
				var rest []pigtail.Workspace
				for _, w := range all {
					if shipped && w.State == pigtail.StateShipped {
						targets = append(targets, w.ID)
						reasons[w.ID] = "shipped"
						selected[w.ID] = true
						continue
					}
					rest = append(rest, w)
				}
				if merged {
					// The containment scan runs before the first DELETE, so it
					// needs a budget of its own: --timeout only bounds each
					// teardown and is powerless over this phase (issue #54).
					scanCx, cancelScan := context.WithTimeout(context.Background(), mergedScanTimeout)
					sel := selectMerged(scanCx, c, rest)
					cancelScan()
					sel.report(cmd.OutOrStdout())
					for _, v := range sel.Selected {
						if selected[v.ID] {
							continue
						}
						selected[v.ID] = true
						targets = append(targets, v.ID)
						reasons[v.ID] = v.Reason
					}
				}
			}
			targets = append(targets, args...)
			if len(targets) == 0 {
				if !shipped && !merged {
					return fmt.Errorf("no workspaces to clean (pass an id, --shipped, or --merged)")
				}
				// A selector was given and matched nothing. That is an answer,
				// not a usage error — and the sweep above has already said how
				// many workspaces it could not classify, so "nothing selected"
				// is no longer indistinguishable from "nothing to clean".
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to clean")
				return nil
			}
			if dryRun {
				fmt.Printf("would clean %d workspace(s):\n", len(targets))
				for _, id := range targets {
					if r := reasons[id]; r != "" {
						fmt.Printf("  %s (%s)\n", id, r)
					} else {
						fmt.Printf("  %s\n", id)
					}
				}
				return nil
			}
			var firstErr error
			for _, id := range targets {
				// One budget per target: a slow teardown must not eat the
				// budget of the next workspace in a --merged/--shipped sweep.
				fmt.Printf("⏳ cleaning %s…\n", id)
				cx, cancel := context.WithTimeout(context.Background(), timeout)
				out, err := c.CleanupWorkspace(cx, id, pigtail.CleanupRequest{
					Force:      force,
					KeepBranch: keepBranch,
				})
				cancel() // not deferred: we're in a loop
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						err = fmt.Errorf("%w (gave up after %s; the daemon keeps tearing down in the background — check with `pigtail list`)", err, timeout)
					}
					fmt.Fprintf(os.Stderr, "✗ %s: %v\n", id, err)
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				printCleanOutcome(os.Stdout, id, out)
			}
			return firstErr
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip safety guards (uncommitted changes, running agent); never the dev-server stop")
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "keep the branch around even if safely deletable")
	cmd.Flags().BoolVar(&shipped, "shipped", false, "include every workspace in 'shipped' state")
	cmd.Flags().BoolVar(&merged, "merged", false, "include every workspace whose work is already in its base (or whose delivery is merged)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list targets without cleaning")
	cmd.Flags().DurationVar(&timeout, "timeout", timeout, "per-workspace budget for the teardown")
	return cmd
}

// The containment check that used to live here (branchMerged) moved to
// internal/backend/git/merged.go, behind the backend.MergeInspector
// capability, and is reached through the daemon's preflight endpoint — see
// clean_merged.go. It shelled out to git from the CLI (architecture.md §8 and
// §10) and ran without a deadline; both are fixed there, along with the
// multi-commit squash blindness that made --merged select almost nothing
// (issue #54).

// --- setup ----------------------------------------------------------------

// setupOptions are what `pigtail setup` was asked to write; a nil field means
// the option was absent and the plist keeps its value.
type setupOptions struct {
	folders, folderSort *bool
	idlePause           *int
	sessionBudget       *bool // true: the standard session budget; false: the gate off (ceiling 0)
	// SDK sessions (pigtaild -sdk-host, -sdk-node, -sdk-launch): machine-specific
	// paths and an opt-in, written only when asked, never on by default.
	sdkHost, sdkNode *string
	sdkLaunch        *bool
}

// runSetupFn is what `pigtail setup` runs; a test swaps it to drive the cobra
// command without launchctl nor the daemon wait (TestSetupCmd_cmuxFolderSort).
var runSetupFn = runSetup

func setupCmd(root *cobra.Command) *cobra.Command {
	var folders, folderSort, sessionBudget, sdkLaunch bool
	var idleClose, idlePause int
	var sdkHost, sdkNode string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install launchd service, zsh completions, and verify daemon",
		Long: `One-time setup: checks prerequisites, installs a launchd plist so
pigtaild starts at login, installs zsh completions, and verifies the
daemon is running. Safe to re-run — every step is idempotent.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var f, s *bool // absent: the plist keeps its value
			if cmd.Flags().Changed("cmux-folders") {
				f = &folders
			}
			if cmd.Flags().Changed("cmux-folder-sort") {
				s = &folderSort
			}
			var ic *int
			for _, name := range []string{"idle-session-close-seconds", "idle-session-pause-seconds"} {
				if !cmd.Flags().Changed(name) {
					continue
				}
				v := idleClose
				if name == "idle-session-pause-seconds" {
					v = idlePause
				}
				if v < 0 {
					return fmt.Errorf("--%s: must be >= 0 (0 = off)", name)
				}
				if ic != nil && *ic != v {
					return fmt.Errorf("--idle-session-pause-seconds and --idle-session-close-seconds disagree")
				}
				ic = &v
			}
			var sb *bool
			if cmd.Flags().Changed("session-budget") {
				sb = &sessionBudget
			}
			o := setupOptions{folders: f, folderSort: s, idlePause: ic, sessionBudget: sb}
			if cmd.Flags().Changed("sdk-host") {
				o.sdkHost = &sdkHost
			}
			if cmd.Flags().Changed("sdk-node") {
				o.sdkNode = &sdkNode
			}
			if cmd.Flags().Changed("sdk-launch") {
				o.sdkLaunch = &sdkLaunch
			}
			return runSetupFn(root, o)
		},
	}
	cmd.Flags().BoolVar(&folders, "cmux-folders", false,
		"write pigtaild --cmux-folders (folder arrangement of the cmux sidebar; acts only while the sidebar loop is on); =false writes it off")
	cmd.Flags().BoolVar(&folderSort, "cmux-folder-sort", true,
		"write pigtaild --cmux-folder-sort; =false files sessions in their folders without sorting (an order set by hand stays, pigtail's own adds go to the bottom)")
	cmd.Flags().BoolVar(&sessionBudget, "session-budget", false,
		"write the session budget into the plist: floor 6, ceiling 20, window 5m, min gap 40s, compressor 16 MiB/s, available-memory floor 6 GiB (the gate is off until you do; the sampler runs from install); =false turns the gate off (ceiling 0) and keeps the other values")
	cmd.Flags().IntVar(&idlePause, "idle-session-pause-seconds", 0,
		"write pigtaild --idle-session-pause-seconds (stop a Claude session idle at least this long; its tab offers to relaunch it); 0 turns it off")
	cmd.Flags().StringVar(&sdkHost, "sdk-host", "",
		"write pigtaild --sdk-host (the clients/sdkhost directory); with --sdk-node it turns the SDK session routes on (read and answer); an empty value removes it")
	cmd.Flags().StringVar(&sdkNode, "sdk-node", "",
		"write pigtaild --sdk-node (the node binary that runs the SDK host, Node 22 or later); an empty value removes it")
	cmd.Flags().BoolVar(&sdkLaunch, "sdk-launch", false,
		"write pigtaild --sdk-launch (opt-in: lets a subject launch a real Claude session held by the SDK; off unless you ask); =false turns it off")
	cmd.Flags().IntVar(&idleClose, "idle-session-close-seconds", 0, "the old name of --idle-session-pause-seconds")
	_ = cmd.Flags().MarkHidden("idle-session-close-seconds")
	_ = cmd.Flags().MarkDeprecated("idle-session-close-seconds", "use --idle-session-pause-seconds")
	return cmd
}

// installLaunchdPlist writes (or rewrites) the launchd plist for configDir.
// It carries forward any tuning flags found in a plist already at
// setup.PlistPath() — RAM/battery/eviction thresholds, the off-site
// backup path, … — rather than resetting them to the deployment defaults
// on every `pigtail setup` re-run. When the existing plist cannot be fully
// understood (malformed XML, a truncated flag, one this code does not
// recognise), it returns that error and writes nothing: losing a hand-tuned
// setting to a silent parse bug would be worse than the regression this
// function exists to close.
//
// Split out of runSetup so it can be exercised directly in tests without
// the surrounding prerequisite checks, daemon wait, and launchctl call.
//
// folders, when set, is written as --cmux-folders after the existing plist's
// tuning is carried (nil keeps what the plist has); folderSort, when set, is
// written as --cmux-folder-sort the same way, and idlePause as
// --idle-session-pause-seconds, and sessionBudget, when set, as the standard
// session budget flags (true) or a ceiling of 0 (false).
func installLaunchdPlist(configDir string, folders, folderSort *bool, idlePause *int, sessionBudget *bool) error {
	return installLaunchdPlistOpts(configDir, setupOptions{folders: folders, folderSort: folderSort, idlePause: idlePause, sessionBudget: sessionBudget})
}

// installLaunchdPlistOpts is installLaunchdPlist with every option of `pigtail
// setup`, the SDK ones included: sdkHost and sdkNode, when set, replace the
// path the plist carries ("" removes it), sdkLaunch, when set, turns the
// opt-in on or off; nil keeps what the plist has, and a plist with none of the
// three stays without them.
func installLaunchdPlistOpts(configDir string, o setupOptions) error {
	folders, folderSort, idlePause, sessionBudget := o.folders, o.folderSort, o.idlePause, o.sessionBudget
	params := setup.PlistParams{
		PigtaildPath: setup.ResolvePigtaildPath(),
		ConfigDir:    configDir,
		Path:         os.Getenv("PATH"),
	}
	params, err := setup.PreserveTuning(setup.PlistPath(), params)
	if err != nil {
		return fmt.Errorf("read existing plist at %s: %w", setup.PlistPath(), err)
	}
	if folders != nil {
		params.CmuxFolders = folders
	}
	if folderSort != nil {
		params.CmuxFolderSort = folderSort
	}
	if idlePause != nil {
		params.IdleSessionPauseSeconds = idlePause
	}
	if sessionBudget != nil {
		if *sessionBudget {
			params = params.WithTuningFrom(setup.SessionBudgetParams())
		} else {
			params.SessionCeiling = setup.IntPtr(0)
		}
	}
	if o.sdkHost != nil {
		params.SDKHost = *o.sdkHost
	}
	if o.sdkNode != nil {
		params.SDKNode = *o.sdkNode
	}
	if o.sdkLaunch != nil {
		params.SDKLaunch = *o.sdkLaunch
	}
	if err := params.ValidateSDK(); err != nil {
		return err
	}
	return setup.InstallPlist(params)
}

func runSetup(root *cobra.Command, o setupOptions) error {
	// 1. Prerequisites
	fmt.Println("Checking prerequisites...")
	prereqs := setup.CheckPrereqs()
	allFound := true
	for _, p := range prereqs {
		if p.Found {
			fmt.Printf("  ✓ %s (%s)\n", p.Name, p.Path)
		} else {
			fmt.Printf("  ✗ %s — %s\n", p.Name, p.HelpMsg)
			allFound = false
		}
	}
	if !allFound {
		return fmt.Errorf("missing prerequisites — install them and re-run pigtail setup")
	}

	// 2. Config dir
	configDir, err := setup.EnsureConfigDir()
	if err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	fmt.Printf("  ✓ config dir %s\n", configDir)

	// 3. Launchd plist (macOS only)
	if runtime.GOOS == "darwin" {
		fmt.Println("\nInstalling launchd service...")
		if err := installLaunchdPlistOpts(configDir, o); err != nil {
			return fmt.Errorf("install plist: %w", err)
		}
		fmt.Printf("  ✓ plist written to %s\n", setup.PlistPath())

		if err := setup.LoadLaunchd(); err != nil {
			fmt.Printf("  ✗ launchctl bootstrap failed: %v\n", err)
			fmt.Println("    Try: launchctl bootstrap gui/$(id -u) " + setup.PlistPath())
		} else {
			fmt.Println("  ✓ launchd service loaded")
		}

		if err := setup.InstallWatchdogStep(setup.ExecRunner, setup.ResolvePigtailPath(), configDir); err != nil {
			fmt.Printf("  ✗ watchdog install failed: %v\n", err)
		} else {
			fmt.Println("  ✓ watchdog agent loaded (sh.pigtail.watchdog)")
		}
	} else {
		fmt.Println("\nNot macOS — skipping launchd. Example systemd unit:")
		fmt.Println("  [Unit]")
		fmt.Println("  Description=pigtail daemon")
		fmt.Println("  [Service]")
		fmt.Printf("  ExecStart=%s\n", setup.ResolvePigtaildPath())
		fmt.Println("  Restart=always")
		fmt.Println("  [Install]")
		fmt.Println("  WantedBy=default.target")
	}

	// 4. Verify daemon
	fmt.Println("\nVerifying daemon...")
	socketPath := filepath.Join(configDir, "pigtail.sock")
	if err := setup.WaitForDaemon(socketPath, 5, 500*time.Millisecond); err != nil {
		fmt.Printf("  ✗ daemon not responding: %v\n", err)
		fmt.Println("    Check logs: tail -f " + filepath.Join(configDir, "daemon.log"))
	} else {
		fmt.Println("  ✓ pigtaild is running")
	}

	// 5. Zsh completions
	fmt.Println("\nInstalling zsh completions...")
	if err := setup.InstallZshCompletions(configDir, root); err != nil {
		fmt.Printf("  ✗ completions: %v\n", err)
	} else {
		fmt.Println("  ✓ completions written")
		home, _ := os.UserHomeDir()
		zshrc := filepath.Join(home, ".zshrc")
		needsUpdate, _ := setup.ZshrcNeedsUpdate(zshrc, configDir)
		if needsUpdate {
			if err := setup.AppendToZshrc(zshrc, configDir); err != nil {
				fmt.Printf("  ✗ zshrc update: %v\n", err)
			} else {
				fmt.Println("  ✓ zshrc updated (restart shell to activate)")
			}
		} else {
			fmt.Println("  ✓ zshrc already configured")
		}
	}

	// 6. Summary
	fmt.Println("\n--- Setup complete ---")
	fmt.Println("Next: cd into a git repo and run `pigtail init`")
	return nil
}

// --- teardown -------------------------------------------------------------

func teardownCmd() *cobra.Command {
	var cleanShell bool
	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove launchd service and optionally clean shell config",
		Long: `Reverse of pigtail setup: stops the daemon, removes the launchd plist.
Pass --clean-shell to also remove the zsh completion fpath entry from ~/.zshrc.
Data in ~/.config/pigtail/ is preserved.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runTeardown(cleanShell)
		},
	}
	cmd.Flags().BoolVar(&cleanShell, "clean-shell", false, "also remove completions from ~/.zshrc")
	return cmd
}

func runTeardown(cleanShell bool) error {
	if runtime.GOOS == "darwin" {
		fmt.Println("Stopping launchd service...")
		if err := setup.UnloadLaunchd(); err != nil {
			fmt.Printf("  ✗ launchctl bootout: %v (may already be unloaded)\n", err)
		} else {
			fmt.Println("  ✓ service stopped")
		}

		if err := setup.TeardownWatchdogStep(setup.ExecRunner); err != nil {
			fmt.Printf("  ✗ watchdog teardown: %v\n", err)
		} else {
			fmt.Println("  ✓ watchdog agent removed")
		}

		fmt.Println("Removing plist...")
		if err := setup.RemovePlist(); err != nil {
			if os.IsNotExist(err) {
				fmt.Println("  ✓ plist already absent")
			} else {
				fmt.Printf("  ✗ remove plist: %v\n", err)
			}
		} else {
			fmt.Println("  ✓ plist removed")
		}
	}

	if cleanShell {
		fmt.Println("Cleaning shell config...")
		home, _ := os.UserHomeDir()
		zshrc := filepath.Join(home, ".zshrc")
		if err := setup.RemoveFromZshrc(zshrc); err != nil {
			fmt.Printf("  ✗ zshrc cleanup: %v\n", err)
		} else {
			fmt.Println("  ✓ completions removed from zshrc")
		}
		configDir := setup.ConfigDir()
		compFile := filepath.Join(configDir, "completions", "_pigtail")
		_ = os.Remove(compFile)
	}

	fmt.Println("\n--- Teardown complete ---")
	fmt.Println("Data in ~/.config/pigtail/ preserved. Remove manually if needed:")
	fmt.Println("  rm -rf ~/.config/pigtail/")
	return nil
}

// --- helpers --------------------------------------------------------------

// resolveLocalProjectID returns the project_id implied by the cwd. It
// reads pigtail.yaml's `name` field when present; otherwise it falls
// back to the cwd's basename.
func resolveLocalProjectID() string {
	cwd, _ := os.Getwd()
	if cfg, err := pigtail.LoadProjectConfig(cwd); err == nil {
		return "prj_" + sanitize(cfg.Name)
	}
	return "prj_" + sanitize(filepath.Base(cwd))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// hooksMarker marks a workspace whose post-create hooks failed (naming the
// hook) or still run. Unknown (nil, a workspace from before migration 0023)
// and ok print nothing.
func hooksMarker(h *pigtail.HooksResult) string {
	if h == nil {
		return ""
	}
	switch h.Status {
	case pigtail.HooksFailed:
		if h.Hook == "" {
			return " ⚠hooks"
		}
		return " ⚠hooks:" + h.Hook
	case pigtail.HooksRunning:
		return " …hooks"
	}
	return ""
}

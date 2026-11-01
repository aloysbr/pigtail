package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// startServer routes a dev-server launch. Inside a cmux session it spawns a
// foreground sidecar surface (so cmux surfaces the branch/PR/port badges);
// otherwise, or with --foreground / --no-cmux, it uses the historical
// foreground/background path. The cmux path itself falls back to the background
// daemon if the cmux CLI is unavailable, so the user always gets a server.
func startServer(ctx *devserver.Context, worktree, server string, foreground, noCmux bool) error {
	if !foreground && !noCmux && cmux.InCmux() {
		return devserver.StartInCmux(ctx, worktree, server)
	}
	return devserver.Start(ctx, worktree, server, foreground)
}

// resolveTargetContext resolves a start/stop invocation to a concrete
// (context, worktree, server). Local project context wins; when the cwd is
// outside any project AND a target was given, fall back to the daemon's
// project registry so port/worktree targets work from any terminal.
func resolveTargetContext(args []string) (*devserver.Context, string, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", "", err
	}
	// A workspace id names its worktree through the daemon, from any cwd.
	if len(args) > 0 && isWorkspaceID(args[0]) {
		return resolveWorkspaceTarget(args[0])
	}
	var target devserver.Target
	if len(args) > 0 {
		target = devserver.ParseTarget(args[0])
	}
	ctx, localErr := devserver.ResolveContext(cwd)
	if localErr == nil {
		wt, srv, err := target.Resolve(ctx)
		return ctx, wt, srv, err
	}
	if len(args) == 0 {
		return nil, "", "", fmt.Errorf("%w — pass a target (port, worktree, worktree:server) to address a registered project", localErr)
	}
	projects, err := newClient().Projects(context.Background())
	if err != nil {
		return nil, "", "", fmt.Errorf("%w (daemon fallback failed: %w)", localErr, err)
	}
	return devserver.ResolveGlobal(projects, target)
}

// workspaceIDRe is a workspace id: the pigtail.PrefixWorkspace and hex digits.
// A prefix test alone would swallow a static server named "ws_docs".
var workspaceIDRe = regexp.MustCompile(`^` + pigtail.PrefixWorkspace + `[0-9a-f]+$`)

// isWorkspaceID reports whether arg is a workspace id, with or without a
// ":server" suffix ("ws_cf8935", "ws_cf8935:storybook").
func isWorkspaceID(arg string) bool {
	id, _, _ := strings.Cut(arg, ":")
	return workspaceIDRe.MatchString(id)
}

// lookupWorkspaceFn resolves a workspace id through the daemon. The client is
// built at call time, never captured at init, so the invocation's socket
// flags and environment apply. A var for tests.
var lookupWorkspaceFn = func(id string) (*pigtail.Workspace, error) {
	cx, cancel := ctx()
	defer cancel()
	return newClient().Workspace(cx, id)
}

// resolveWorkspaceTarget turns "ws_<id>[:server]" into a (context, worktree,
// server) through the daemon: the workspace's path is its worktree.
func resolveWorkspaceTarget(arg string) (*devserver.Context, string, string, error) {
	id, srv, _ := strings.Cut(arg, ":")
	ws, err := lookupWorkspaceFn(id)
	if err != nil {
		var api *pigtail.APIError
		if errors.As(err, &api) && api.Code == pigtail.ErrWorkspaceNotFound {
			return nil, "", "", fmt.Errorf("no workspace %s (see \"pigtail list\")", id)
		}
		return nil, "", "", fmt.Errorf("resolve %s through the daemon: %w", id, err)
	}
	dctx, err := devserver.ResolveContextFromPath(ws.Path)
	if err != nil {
		return nil, "", "", fmt.Errorf("workspace %s (%s) is not inside a pigtail project: %w", id, ws.Path, err)
	}
	// Stop/Start rebuild the directory from container + name: a nested layout
	// would address another directory.
	if filepath.Join(dctx.ContainerDir, dctx.WorktreeName) != filepath.Clean(ws.Path) {
		return nil, "", "", fmt.Errorf("workspace %s (%s) is not a direct worktree of its project container %s", id, ws.Path, dctx.ContainerDir)
	}
	wt, server, err := devserver.Target{Worktree: dctx.WorktreeName, Server: srv}.Resolve(dctx)
	if err != nil {
		return nil, "", "", err
	}
	return dctx, wt, server, nil
}

// noTargetArgs refuses a positional argument for a command that acts on the
// current worktree only: silently ignoring it started the wrong server.
func noTargetArgs(name string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("pigtail %s takes no argument (got %q) — it starts a server of the current worktree; to address another one: pigtail start <port|worktree|ws_id>[:server]", name, args[0])
		}
		return nil
	}
}

// isBareName reports whether a positional target is a plain name (not a port,
// not a worktree:server pair). Only bare names are eligible for the static
// server fallback — worktree-addressed targets (port / worktree:server) must
// never silently route to a static server.
func isBareName(arg string) bool {
	if arg == "" {
		return false
	}
	if _, err := strconv.Atoi(arg); err == nil {
		return false // numeric → port target
	}
	if strings.Contains(arg, ":") {
		return false // worktree:server target
	}
	return true
}

// lookupStaticServer asks the daemon whether a static server named `name`
// exists, returning it (or nil if not found). The error is non-nil only when
// the daemon itself is unreachable; a missing name yields (nil, nil).
func lookupStaticServer(name string) (*pigtail.StaticServerView, error) {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	servers, err := c.ListStaticServers(cx)
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if servers[i].Name == name {
			return &servers[i], nil
		}
	}
	return nil, nil
}

// staticServerNames returns the registered static-server names for shell
// completion. Best-effort: a daemon error yields no completions.
func staticServerNames() []string {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	servers, err := c.ListStaticServers(cx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name)
	}
	return names
}

func devCmd() *cobra.Command {
	var foreground bool
	var noCmux bool
	var serverName string
	cmd := &cobra.Command{
		Use:   "dev",
		Args:  noTargetArgs("dev"),
		Short: "Start the dev server for the current worktree",
		Long: `Reads the port from .env.local, checks it is free, and launches the
dev server. Refuses to start if the port is already taken.

Inside a cmux session the server runs in a foreground cmux sidecar surface so
cmux surfaces the worktree's branch/PR/port badges; pass --no-cmux to force the
background daemon instead. Outside cmux it daemonizes as before.

By default starts the "dev" server. Use --server to start a different one
(e.g. --server storybook), or use the "pigtail sb" shorthand.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx, err := devserver.ResolveContext(cwd)
			if err != nil {
				return err
			}
			fmt.Printf("→ %s/%s\n", ctx.Config.Name, ctx.WorktreeName)
			return startServer(ctx, ctx.WorktreeName, serverName, foreground, noCmux)
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", false, "run in foreground (don't daemonize)")
	cmd.Flags().BoolVar(&noCmux, "no-cmux", false, "force the background daemon even inside a cmux session")
	cmd.Flags().StringVar(&serverName, "server", "dev", "server name from pigtail.yaml servers section")
	return cmd
}

func sbCmd() *cobra.Command {
	var foreground bool
	var noCmux bool
	cmd := &cobra.Command{
		Use:   "sb",
		Args:  noTargetArgs("sb"),
		Short: "Start storybook for the current worktree (alias for dev --server storybook)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx, err := devserver.ResolveContext(cwd)
			if err != nil {
				return err
			}
			fmt.Printf("→ %s/%s\n", ctx.Config.Name, ctx.WorktreeName)
			return startServer(ctx, ctx.WorktreeName, "storybook", foreground, noCmux)
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", false, "run in foreground (don't daemonize)")
	cmd.Flags().BoolVar(&noCmux, "no-cmux", false, "force the background daemon even inside a cmux session")
	return cmd
}

func startCmd() *cobra.Command {
	var foreground bool
	var noCmux bool
	var serverName string
	cmd := &cobra.Command{
		Use:   "start [port|worktree|ws_id][:server]",
		Short: "Start a dev server by port, worktree, or worktree:server",
		Long: `Start a dev server addressed by a port number, a worktree name, or an
explicit worktree:server pair. A workspace id (ws_…) is resolved through the
daemon to its worktree. Works from anywhere: inside a project the local
pigtail.yaml wins; outside, targets are resolved against the daemon's registered
projects.

With no argument, starts the default "dev" server of the current worktree
(same as "pigtail dev").

Examples:
  pigtail start                     # dev server, current worktree
  pigtail start 6010                # whichever server owns port 6010
  pigtail start feat-auth           # dev server of feat-auth
  pigtail start feat-auth:storybook # storybook of feat-auth
  pigtail start ws_cf8935:storybook # storybook of workspace ws_cf8935`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeStartStopTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, wt, srv, err := resolveTargetContext(args)
			if err != nil {
				// Worktree-first: only when a bare name resolves to no worktree
				// do we fall through to a daemon-registered static server.
				if len(args) > 0 && isBareName(args[0]) && !isWorkspaceID(args[0]) {
					if ss, lookErr := lookupStaticServer(args[0]); lookErr == nil {
						if ss != nil {
							return startStaticServer(args[0])
						}
						return fmt.Errorf("no worktree or static server named %q (%w)", args[0], err)
					}
				}
				return err
			}
			// A target that names a server (port or worktree:server) wins over
			// --server; refuse the combination instead of silently dropping it.
			if srv != "" && cmd.Flags().Changed("server") && serverName != srv {
				return fmt.Errorf("target names server %q but --server=%q; remove one", srv, serverName)
			}
			if srv == "" {
				srv = serverName // --server flag, defaults to "dev"
			}
			fmt.Printf("→ %s/%s\n", ctx.Config.Name, wt)
			return startServer(ctx, wt, srv, foreground, noCmux)
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", false, "run in foreground (don't daemonize)")
	cmd.Flags().BoolVar(&noCmux, "no-cmux", false, "force the background daemon even inside a cmux session")
	cmd.Flags().StringVar(&serverName, "server", "dev", "server to start (conflicts with a target that names one)")
	return cmd
}

// startStaticServer starts a daemon-registered static server by name and
// prints its URL.
func startStaticServer(name string) error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	ss, err := c.StartStaticServer(cx, name)
	if err != nil {
		return err
	}
	port := ss.RunningPort
	if port == 0 {
		port = ss.Port
	}
	fmt.Printf("→ static server %s\n", ss.Name)
	fmt.Printf("● running on http://127.0.0.1:%d/\n", port)
	return nil
}

// stopStaticServer stops a daemon-registered static server by name.
func stopStaticServer(name string) error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	if err := c.StopStaticServer(cx, name); err != nil {
		return err
	}
	fmt.Printf("○ stopped static server %s\n", name)
	return nil
}

// stopAllStaticServers stops every running static server and prints the names
// stopped, folding them into the `stop --all` output. A daemon error is
// returned so the caller can surface it, but it does not undo the worktree
// servers already stopped.
func stopAllStaticServers() error {
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	stopped, err := c.StopAllStaticServers(cx)
	if err != nil {
		return err
	}
	for _, name := range stopped {
		fmt.Printf("○ stopped static server %s\n", name)
	}
	return nil
}

// completeStartStopTargets supplies shell completions for start/stop: worktree
// names and worktree:server pairs (via the daemon's project registry) plus the
// registered static-server names. Best-effort — failures yield no completions.
func completeStartStopTargets(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	cwd, err := os.Getwd()
	if err == nil {
		if ctx, ctxErr := devserver.ResolveContext(cwd); ctxErr == nil {
			if entries, dirErr := os.ReadDir(ctx.ContainerDir); dirErr == nil {
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					if _, statErr := os.Stat(ctx.ContainerDir + "/" + e.Name() + "/.env.local"); statErr == nil {
						out = append(out, e.Name())
					}
				}
			}
		}
	}
	out = append(out, staticServerNames()...)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// isTTYFn reports whether stdin is an interactive terminal. A package-level
// var (not a direct os.Stdin.Stat() call buried in the logic) so tests can
// override it to exercise both the interactive and non-interactive paths of
// `stop --everywhere` without a real TTY.
var isTTYFn = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// requireForceForEverywhere refuses a machine-wide stop from a non-interactive
// shell unless --force is explicit. Agent sessions run without a TTY: this
// turns a silent fratricide into a clear, harmless error.
func requireForceForEverywhere(isTTY, force bool) error {
	if force || isTTY {
		return nil
	}
	return fmt.Errorf("--everywhere stops every dev server on this machine, including other sessions' — " +
		"re-run with --force to confirm, or use --idle (only genuinely idle servers) or --all (your servers and orphans)")
}

// confirmEverywhere asks for interactive y/N confirmation before a
// --everywhere stop that was not passed --force. Returns false on anything
// but an explicit yes, including a read error.
func confirmEverywhere() bool {
	fmt.Fprint(os.Stdout, "This stops every dev server on this machine, including other sessions' — continue? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// idleGraceNoOptDefVal is what pflag assigns --idle when it appears bare
// (no duration argument). "0s" parses to a zero grace, which the daemon
// treats identically to an omitted grace_seconds: fall back to its
// configured idle-purge grace, refusing outright if that is also disabled.
const idleGraceNoOptDefVal = "0s"

// resolveIdleDuration reconciles a pflag quirk: an optional-value flag like
// --idle never consumes a following bare token as its own value (that
// requires --idle=1m or the flag's NoOptDefVal for a bare --idle) — so the
// natural, documented space-separated form (`--idle 1m`) instead leaves "1m"
// sitting in the positional args.
//
// Reinterpreting that leftover as the duration is only safe in the genuinely
// ambiguous case: a bare --idle, recognisable because idle still holds
// idleGraceNoOptDefVal (no explicit value was ever assigned to the flag).
// The instant --idle carries an explicit value of its own (--idle=1m), a
// residual positional can only be a real stop target — and must be a hard
// mutual-exclusion error, not a candidate duration. Silently discarding the
// explicit "1m" and reinterpreting the target as a *different* grace would
// run the sweep at the wrong duration without a word: exactly the class of
// damage this command exists to prevent (found in code review, since a
// target shaped like a duration, e.g. a stray token, made the mix-up land
// with no error at all — see task-7-report.md).
func resolveIdleDuration(idle string, args []string) (string, error) {
	if idle != idleGraceNoOptDefVal {
		// --idle already has an explicit value; any leftover positional is a
		// real target, never a duration candidate.
		if len(args) > 0 {
			return "", fmt.Errorf("--idle and a positional target are mutually exclusive")
		}
		return idle, nil
	}
	switch len(args) {
	case 0:
		return idle, nil // truly bare --idle: use the daemon's configured grace
	case 1:
		return args[0], nil // space-separated: --idle DURATION
	default:
		return "", fmt.Errorf("--idle takes at most one duration")
	}
}

// stopIdleServers implements `stop --idle [duration]`: it asks the daemon to
// stop only genuinely idle dev servers and prints every server's outcome,
// including the ones it spared and why. This is the deliberate inverse of
// the daemon's silent idle-purge/battery/cap evictions — the whole point of
// --idle is to never be quiet about what it left running.
func stopIdleServers(idleFlag string) error {
	var graceSeconds int
	if idleFlag != "" {
		d, err := time.ParseDuration(idleFlag)
		if err != nil {
			return fmt.Errorf("invalid --idle duration %q: %w", idleFlag, err)
		}
		graceSeconds = int(d.Seconds())
	}
	c := newClient()
	cx, cancel := ctx()
	defer cancel()
	resp, err := c.StopIdleServers(cx, graceSeconds)
	if err != nil {
		return err
	}
	for _, s := range resp.Servers {
		idle := (time.Duration(s.IdleSeconds) * time.Second).Round(time.Second)
		if s.Stopped {
			fmt.Printf("○ stopped %s:%s (:%d, idle %s)\n", s.WorkspaceID, s.Server, s.Port, idle)
		} else {
			fmt.Printf("· kept %s:%s (:%d, idle %s) — %s\n", s.WorkspaceID, s.Server, s.Port, idle, s.Reason)
		}
	}
	return nil
}

func stopServerCmd() *cobra.Command {
	var all bool
	var everywhere bool
	var force bool
	var idle string
	var serverName string
	cmd := &cobra.Command{
		Use:   "stop [port|worktree|ws_id][:server]",
		Short: "Stop dev servers by port, worktree, or worktree:server",
		Long: `Stop dev servers addressed by a port number, a worktree name, or an
explicit worktree:server pair. A workspace id (ws_…) is resolved through the
daemon to its worktree. Works from anywhere: inside a project the local
pigtail.yaml wins; outside, targets are resolved against the daemon's registered
projects.

With no argument, stops all servers of the current worktree.

--all and --everywhere both sweep every worktree, but bound who they touch:
--all stops this worktree's own servers and servers nobody claims, and
spares every other worktree's — a worktree identity can't be interrogated,
so it is always treated as alive. --everywhere stops every server regardless
of owner, including other worktrees' and sessions' — it refuses to run
outside a TTY unless --force is given, and announces both what it reclaims
and what it seizes.

Claiming takes a session's identity from, in order: CMUX_WORKSPACE_ID (set by
cmux), $PIGTAIL_OWNER (exported yourself), PIGTAIL_OWNER recorded in the
worktree's .env.local (written by "pigtail new" — this is the stable case),
then a POSIX session id as sid:<n>@<host> (fragile: an AI agent's command
runner detaches each invocation it runs, so this identity is typically dead
within a second; it works as intended for an interactive terminal or a tmux
pane). Only a same-host sid:<n>@<host> whose session has since ended is ever
reclaimed by --all — printed when it happens; every other identity,
including any other worktree's, is opaque and always treated as alive. A
worktree that predates this scheme has no PIGTAIL_OWNER in its .env.local;
add one by hand to give it a stable identity. Static servers (pigtail serve)
have no owner at all: --all stops every running one, whoever started it.

--idle is different in kind: instead of stopping indiscriminately, it stops
only servers that are genuinely idle (CPU-quiet for at least the given
duration, or the daemon's configured idle-purge grace when no duration is
given), never touches pinned servers, and reports every server's outcome —
including the ones it spared and why. Use this instead of --all/--everywhere
when the goal is freeing RAM from forgotten servers, not stopping work in
progress. Note its reach: it runs inside the daemon, over every server the
daemon knows, in every project and every session — ownership plays no part.
That is deliberate (a server nobody has touched for an hour is a forgotten
server whoever started it), but it does mean --idle is machine-wide, not
session-scoped. Mutually exclusive with --all, --everywhere, and a positional
target.

Examples:
  pigtail stop                      # all servers, current worktree
  pigtail stop 3030                 # whichever server owns port 3030
  pigtail stop feat-auth            # all servers of feat-auth
  pigtail stop feat-auth:storybook  # storybook of feat-auth
  pigtail stop ws_cf8935            # all servers of workspace ws_cf8935
  pigtail stop --server storybook   # storybook of the current worktree
  pigtail stop --all                # this worktree's servers and orphans, sparing other worktrees
  pigtail stop --everywhere --force # every server, every worktree, no exceptions
  pigtail stop --idle               # idle servers, using the daemon's configured grace
  pigtail stop --idle 1h            # servers idle at least 1h`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeStartStopTargets,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("idle") {
				resolved, err := resolveIdleDuration(idle, args)
				if err != nil {
					return err
				}
				return stopIdleServers(resolved)
			}
			if (all || everywhere) && len(args) > 0 {
				return fmt.Errorf("--all/--everywhere and a positional target are mutually exclusive")
			}
			if all || everywhere {
				scope := devserver.ScopeOwned
				if everywhere {
					if err := requireForceForEverywhere(isTTYFn(), force); err != nil {
						return err
					}
					if !force && !confirmEverywhere() {
						fmt.Fprintln(os.Stdout, "aborted")
						return nil
					}
					scope = devserver.ScopeEverywhere
				}
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				// Worktree-local stop needs a project context. Static servers are
				// daemon-global, so when the cwd is outside any pigtail project we
				// skip the local sweep but still stop the static servers.
				ctx, err := devserver.ResolveContext(cwd)
				switch {
				case errors.Is(err, devserver.ErrNotInProject):
					// no local servers to enumerate here — fall through to statics
				case err != nil:
					return err
				default:
					if stopErr := devserver.StopAll(ctx, scope); stopErr != nil {
						return stopErr
					}
				}
				// Also stop every daemon-registered static server. Best-effort:
				// a daemon error here must not mask the worktree-server stop.
				return stopAllStaticServers()
			}
			ctx, wt, srv, err := resolveTargetContext(args)
			if err != nil {
				// Worktree-first: a bare name that matches no worktree falls
				// through to a daemon-registered static server.
				if len(args) > 0 && isBareName(args[0]) && !isWorkspaceID(args[0]) {
					if ss, lookErr := lookupStaticServer(args[0]); lookErr == nil {
						if ss != nil {
							return stopStaticServer(args[0])
						}
						return fmt.Errorf("no worktree or static server named %q (%w)", args[0], err)
					}
				}
				return err
			}
			// A target that names a server (port or worktree:server) wins over
			// --server; refuse the combination instead of silently dropping it.
			if srv != "" && cmd.Flags().Changed("server") && serverName != srv {
				return fmt.Errorf("target names server %q but --server=%q; remove one", srv, serverName)
			}
			if srv == "" {
				srv = serverName // --server flag, defaults to "" (= all servers)
			}
			fmt.Printf("→ %s/%s\n", ctx.Config.Name, wt)
			return devserver.Stop(ctx, wt, srv)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "stop this worktree's servers, unclaimed ones, and every static server, across every worktree — sparing every other worktree's dev servers")
	cmd.Flags().BoolVar(&everywhere, "everywhere", false, "stop every server in every worktree, whoever started it (needs --force outside a TTY)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the interactive confirmation for --everywhere")
	cmd.Flags().StringVar(&idle, "idle", "",
		"stop only servers idle at least this long (e.g. 1h); bare --idle uses the daemon's configured idle-purge grace; never stops pinned servers; machine-wide — every project and session the daemon knows")
	cmd.Flags().Lookup("idle").NoOptDefVal = idleGraceNoOptDefVal
	cmd.Flags().StringVar(&serverName, "server", "", "stop only this server (conflicts with a target that names one)")
	cmd.MarkFlagsMutuallyExclusive("all", "everywhere", "idle", "server")
	return cmd
}

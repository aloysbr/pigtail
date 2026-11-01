package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type (
	tickMsg        time.Time
	workspacesMsg  []pigtail.Workspace
	errMsg         error
	portStatusMsg  map[string]portInfo
	actionDoneMsg  string
	streamReadyMsg struct{ ch <-chan pigtail.Event }
	streamEventMsg pigtail.Event
	streamClosed   struct{}
	openStreamMsg  struct{}
)

type preflightMsg struct {
	data *cleanPreflight
}

type preflightErrMsg struct {
	err error
}

type cleanPreflight struct {
	wsID     string
	branch   string
	clean    bool
	ahead    int
	behind   int
	merged   bool
	dirty    []string
	commits  []string
	delivery *deliveryInfo
	verdict  string
}

type deliveryInfo struct {
	url      string
	state    string
	ciStatus string
}

type portInfo struct {
	DevAlive bool
	SBAlive  bool
	DevPort  int
	SBPort   int
}

func loadWorkspaces(c *pigtail.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := c.Workspaces(ctx)
		if err != nil {
			return errMsg(err)
		}
		return workspacesMsg(out)
	}
}

// configOf is the project config governing a worktree, nil when none resolves.
func configOf(path string) *pigtail.ProjectConfig {
	ctx, err := devserver.ResolveContextFromPath(path)
	if err != nil || ctx == nil {
		return nil
	}
	return ctx.Config
}

// ownedListeners probes a port and splits its listeners between the
// worktree's own processes and the others. A seam: tests swap it.
var ownedListeners = devserver.OwnedListeners

func checkPorts(ws []pigtail.Workspace) tea.Cmd {
	return func() tea.Msg {
		result := make(map[string]portInfo, len(ws))
		for _, w := range ws {
			var info portInfo
			// The port .env.local names (what `pigtail dev` launches), else
			// the registry's; alive only when a listener runs in the worktree.
			ports := devserver.WorkspacePorts(w.Path, configOf(w.Path), w.Ports)
			if p, ok := ports["dev"]; ok {
				info.DevPort = p
				own, _ := ownedListeners(p, w.Path)
				info.DevAlive = len(own.Owned) > 0
			}
			if p, ok := ports["storybook"]; ok {
				info.SBPort = p
				own, _ := ownedListeners(p, w.Path)
				info.SBAlive = len(own.Owned) > 0
			}
			result[w.ID] = info
		}
		return portStatusMsg(result)
	}
}

func openStream(c *pigtail.Client) tea.Cmd {
	return func() tea.Msg {
		ch, err := c.Events(context.Background(), pigtail.EventsRequest{
			Types: []string{"workspace.*", "agent.*"},
		})
		if err != nil {
			return errMsg(err)
		}
		return streamReadyMsg{ch: ch}
	}
}

func readNext(ch <-chan pigtail.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return streamClosed{}
		}
		return streamEventMsg(e)
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func portTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return portTickMsg{} })
}

type portTickMsg struct{}

func execTmuxAttach(sessionName string) tea.Cmd {
	c := exec.Command("tmux", "attach", "-t", sessionName)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return errMsg(fmt.Errorf("tmux attach: %w", err))
		}
		return actionDoneMsg("attached")
	})
}

func execGitDiff(path string) tea.Cmd {
	c := exec.Command("sh", "-c", fmt.Sprintf("cd %q && git diff | less -R", path))
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return errMsg(fmt.Errorf("git diff: %w", err))
		}
		return actionDoneMsg("diff viewed")
	})
}

// cleanTimeout mirrors the CLI's `pigtail clean --timeout` default. Removing
// a worktree with a populated node_modules is minutes of unlink() calls, and
// giving up early does not stop the daemon — it only makes the TUI report a
// failure for a teardown that is still running.
const cleanTimeout = 10 * time.Minute

func execClean(client *pigtail.Client, wsID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cleanTimeout)
		defer cancel()
		out, err := client.CleanupWorkspace(ctx, wsID, pigtail.CleanupRequest{Force: true})
		if err != nil {
			return errMsg(fmt.Errorf("clean %s: %w", wsID, err))
		}
		// Same honesty as the CLI (issue #53): a branch left behind must not
		// hide inside a bare "cleaned". The status line is one row, so it
		// names the branch and leaves the full reason to the daemon log.
		if out != nil && out.BranchKept != "" {
			return actionDoneMsg(fmt.Sprintf("cleaned %s — branch %s kept", wsID, out.BranchKept))
		}
		return actionDoneMsg(fmt.Sprintf("cleaned %s", wsID))
	}
}

func fetchPreflight(c *pigtail.Client, wsID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := c.Preflight(ctx, wsID)
		if err != nil {
			return preflightErrMsg{err: err}
		}
		pf := &cleanPreflight{
			wsID:    resp.WorkspaceID,
			branch:  resp.Branch,
			clean:   resp.Git.Clean,
			ahead:   resp.Git.Ahead,
			behind:  resp.Git.Behind,
			merged:  resp.Git.Merged,
			dirty:   resp.Git.ModifiedFiles,
			commits: resp.Git.Commits,
			verdict: resp.Verdict,
		}
		if resp.Delivery != nil {
			pf.delivery = &deliveryInfo{
				url:      resp.Delivery.URL,
				state:    resp.Delivery.State,
				ciStatus: resp.Delivery.CIStatus,
			}
		}
		return preflightMsg{data: pf}
	}
}

func execDispatch(client *pigtail.Client, projectID, intent string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		resp, err := client.Dispatch(ctx, pigtail.DispatchRequest{ProjectID: projectID, Intent: intent})
		if err != nil {
			return errMsg(fmt.Errorf("dispatch: %w", err))
		}
		return actionDoneMsg(fmt.Sprintf("dispatched %s", resp.WorkspaceID))
	}
}

func execRestartDaemon() tea.Cmd {
	return func() tea.Msg {
		_ = exec.Command("pkill", "-USR1", "pigtaild").Run()
		time.Sleep(500 * time.Millisecond)
		// Check if daemon is still alive, if not, try to restart via launchd.
		if err := exec.Command("pgrep", "pigtaild").Run(); err != nil {
			_ = exec.Command("pkill", "pigtaild").Run()
			time.Sleep(500 * time.Millisecond)
			home, _ := os.UserHomeDir()
			plist := home + "/Library/LaunchAgents/sh.pigtail.daemon.plist"
			_ = exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist).Run()
			time.Sleep(2 * time.Second)
		}
		return actionDoneMsg("daemon restarted")
	}
}

// notifyServerStoppedTimeout bounds the deliberate-stop notification. Short:
// the TUI is interactive and must not stall on an unreachable daemon.
const notifyServerStoppedTimeout = 2 * time.Second

// notifyServerStopped tells the daemon a dev server stop was deliberate, so
// its death detector does not report it. Silent and best-effort: the cost of
// failing is one false death reported, never a real one missed.
func notifyServerStopped(client *pigtail.Client, wsID, serverName string) {
	if client == nil || wsID == "" || serverName == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyServerStoppedTimeout)
	defer cancel()
	_ = client.MarkServerStopped(ctx, wsID, serverName)
}

func execToggleServer(client *pigtail.Client, wsPath, serverName, wsID string) tea.Cmd {
	return func() tea.Msg {
		dctx, err := devserver.ResolveContextFromPath(wsPath)
		if err != nil {
			return errMsg(fmt.Errorf("%s: %w", serverName, err))
		}
		spec, ok := dctx.Config.Servers[serverName]
		if !ok {
			return errMsg(fmt.Errorf("unknown server %q", serverName))
		}
		ps, ok := dctx.Config.Ports[spec.PortKind]
		if !ok {
			return errMsg(fmt.Errorf("no port config for %q", spec.PortKind))
		}
		envVar := ps.Env
		if envVar == "" {
			envVar = pigtail.DefaultPortEnv(spec.PortKind)
		}
		port, err := devserver.ReadPort(dctx.WorktreeDir, envVar)
		if err != nil {
			return errMsg(err)
		}

		own, _ := ownedListeners(port, dctx.WorktreeDir)
		busy, pids := len(own.Owned) > 0, own.Owned
		if own.HeldElsewhere() {
			// Held by a process running elsewhere (another worktree's
			// server), or by one whose owner the cwd probe could not
			// determine. Neither killed nor doubled by a second start.
			retry := ""
			if own.Unknown() {
				retry = "; retry"
			}
			return actionDoneMsg(fmt.Sprintf("%s (:%d) — %s — not stopped%s",
				serverName, port, own.Describe(dctx.ContainerDir), retry))
		}
		if busy {
			// Announce the stop before killing. This path kills PIDs directly
			// instead of going through devserver.Stop — deliberately: Stop
			// prints to stdout (which would corrupt the TUI's own rendering)
			// and purges the Turbopack dev cache, which is wrong for a toggle
			// the user is about to flip back on. So the notification is made
			// explicitly here, straight to the daemon: the TUI already holds
			// the workspace ID, so it needs none of the CLI's path-to-workspace
			// resolution. Best-effort — a stop must never fail on the daemon.
			// Every name sharing the port is announced: the kill takes the
			// port's listeners, so a twin (a large monorepo's `web` next to `dev`) goes
			// down too and must not come back as a death.
			names := devserver.NamesSharingPort(dctx.Config, serverName)
			for _, name := range names {
				notifyServerStopped(client, wsID, name)
			}
			devserver.KillPIDs(pids)
			return actionDoneMsg(fmt.Sprintf("%s stopped (:%d)", strings.Join(names, "+"), port))
		}

		// Source .env.local to get ALL project env vars, then start via nohup.
		// The log goes to pigtail's state dir, not the worktree — same path the
		// CLI's own background launcher uses, resolved by the same function.
		if _, err := devserver.EnsureStateDir(dctx.WorktreeDir); err != nil {
			return errMsg(err)
		}
		logFile := devserver.LogPath(dctx.WorktreeDir, serverName)
		shell := fmt.Sprintf("cd %q && set -a && . .env.local && set +a && nohup %s >>%q 2>&1 &",
			dctx.WorktreeDir, spec.Command, logFile)
		cmd := exec.Command("sh", "-c", shell)
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Run(); err != nil {
			return errMsg(err)
		}
		// Poll until the server binds the port, then open browser.
		url := fmt.Sprintf("http://localhost:%d", port)
		for i := 0; i < 30; i++ {
			time.Sleep(time.Second)
			busy, _, _ := devserver.IsPortBusy(port)
			if busy {
				_ = exec.Command("open", url).Run()
				return actionDoneMsg(fmt.Sprintf("%s ready (:%d)", serverName, port))
			}
		}
		return actionDoneMsg(fmt.Sprintf("%s started (:%d) — slow to bind", serverName, port))
	}
}

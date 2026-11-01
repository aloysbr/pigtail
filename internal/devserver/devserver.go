// Package devserver manages dev server processes (start, stop, status) for
// pigtail worktrees. All operations are local — no daemon RPC.
package devserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/envlocal"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ErrNotInProject is returned when the cwd is not inside a pigtail project
// (no pigtail.yaml found while walking up). Callers that operate on
// daemon-global resources (e.g. static servers) can tolerate this and skip
// the worktree-local part of their work.
var ErrNotInProject = errors.New("not inside a pigtail project")

// Package-level seams, indirected through vars so tests can override the
// external effects (cmux spawn, port probing, background daemon, sleeping)
// and capture printed output without touching real processes or sockets.
var (
	spawnSidecar              = cmux.SpawnForegroundSidecar
	portBusy                  = IsPortBusy
	runBackgroundFn           = runBackground
	killPIDsFn                = KillPIDs
	processCwds               = ProcessCwds
	sleepFn                   = time.Sleep
	out             io.Writer = os.Stdout
)

// notifyStopped tells the daemon a stop was deliberate, so its death detector
// does not report it. A package-level seam: internal/devserver must not depend
// on the daemon, and the daemon itself calls Stop (watchdog kill path), where
// this must stay a no-op — it marks its own stops directly, in-process, and
// must never work to itself over HTTP. Only the CLI wires a real notifier, via
// SetStopNotifier.
//
// The first argument is the worktree's ABSOLUTE directory, not its name. A
// name cannot identify a workspace: "main" is the main worktree of every
// pigtail project, so on a machine with two registered projects a name-based
// lookup marks whichever one the daemon happens to list first — and the
// deliberate stop the user just made resurfaces as a death, the exact false
// positive this whole surface exists to avoid.
var notifyStopped = func(_, _ string) {}

// SetStopNotifier installs the deliberate-stop notifier. Called by cmd/pigtail
// at startup; never by pigtaild. A nil function restores the no-op default
// rather than panicking on the next stop.
func SetStopNotifier(f func(worktreeDir, serverName string)) {
	if f == nil {
		notifyStopped = func(_, _ string) {}
		return
	}
	notifyStopped = f
}

// Context holds the resolved project and worktree for a dev server operation.
type Context struct {
	ContainerDir string
	WorktreeDir  string
	WorktreeName string
	Config       *pigtail.ProjectConfig
}

// RunningServer describes a server whose port is currently in use.
type RunningServer struct {
	WorktreeName string
	ServerName   string
	Port         int
	PIDs         []int
}

// ResolveContext walks up from cwd to find pigtail.yaml, then determines
// which worktree the caller is in.
func ResolveContext(cwd string) (*Context, error) {
	containerDir, err := findContainer(cwd)
	if err != nil {
		return nil, err
	}
	cfg, err := pigtail.LoadProjectConfig(containerDir)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	wtName := resolveWorktreeName(cwd, containerDir, cfg.MainWorktree)
	wtDir := filepath.Join(containerDir, wtName)

	return &Context{
		ContainerDir: containerDir,
		WorktreeDir:  wtDir,
		WorktreeName: wtName,
		Config:       cfg,
	}, nil
}

// Start launches a dev server in the background (or foreground) for the
// given worktree (not necessarily the caller's cwd).
func Start(ctx *Context, worktreeName, serverName string, foreground bool) error {
	spec, ok := ctx.Config.Servers[serverName]
	if !ok {
		return fmt.Errorf("unknown server %q (available: %s)", serverName, strings.Join(serverNames(ctx.Config), ", "))
	}

	envVar, ok := envVarFor(ctx.Config, spec)
	if !ok {
		return fmt.Errorf("server %q references port kind %q not found in ports config", serverName, spec.PortKind)
	}

	wtDir := filepath.Join(ctx.ContainerDir, worktreeName)
	port, err := readPortForStart(wtDir, envVar)
	if err != nil {
		return fmt.Errorf("read port: %w", err)
	}

	busy, pids, _ := portBusy(port)
	if busy {
		skip, err := checkOwnListener(ctx, wtDir, serverName, spec.PortKind, port, pids)
		if err != nil {
			return err
		}
		if skip {
			return nil
		}
	}

	if foreground {
		// runForeground blocks for the process's entire lifetime, so unlike the
		// background/cmux paths (where "launched" and "still running" are
		// separate moments, and an orphaned marker is a real risk on a failed
		// launch), here the call IS the run: writing just before it and
		// clearing right after it returns makes the marker's lifetime exactly
		// match the process's, whether it exits cleanly or with an error.
		if err := writeOwner(wtDir, serverName, OwnerID(wtDir)); err != nil {
			fmt.Fprintf(out, "  ⚠ could not record server owner: %v\n", err)
		}
		err := runForeground(wtDir, spec.Command, port)
		clearOwner(wtDir, serverName)
		return err
	}
	if err := runBackgroundFn(wtDir, serverName, spec.Command, port); err != nil {
		return err
	}
	if err := writeOwner(wtDir, serverName, OwnerID(wtDir)); err != nil {
		fmt.Fprintf(out, "  ⚠ could not record server owner: %v\n", err)
	}
	return nil
}

// Sidecar liveness timing. The boot timeout rides out a lazily-started PTY; the
// settle window then re-confirms the port keeps its listener before we call the
// server live, since a heavy dev server can be reaped just after the first
// port-up under CPU contention. The free grace bounds how long we wait for a
// dying sidecar to release the port before the daemon takes over.
// Package vars (not consts) so tests can shrink them: the liveness loops poll
// against a wall-clock deadline, so leaving these at production values would
// make a portBusy-driven unit test busy-spin for the full 30s/4s window.
var (
	sidecarBootTimeout    = 30 * time.Second
	sidecarSettleWindow   = 4 * time.Second
	sidecarSettleInterval = 1 * time.Second
	sidecarFreeGrace      = 2 * time.Second
)

// StartInCmux launches a dev server inside a cmux sidecar surface anchored on
// the worktree, so cmux discovers the running process and lights the workspace's
// branch/PR/port badges — which a daemonized background server (reparented out
// of any pane's tty tree) never triggers.
//
// It resolves the worktree/port/command exactly like Start and honours the same
// idempotent "already running" short-circuit. Liveness is checked honestly, and
// it auto-falls back to the reliable background daemon on any of three
// conditions, so the caller is never left with a dead-but-"live" server:
//
//  1. the cmux spawn itself fails (CLI missing, new-surface error);
//  2. the port never comes up within the boot timeout;
//  3. the port comes up but drops again during the settle window — a re-poll
//     over sidecarSettleWindow that catches a sidecar reaped under CPU
//     contention just after the first port-up.
//
// The "● live" line is printed only after the port has survived the whole settle
// window. Before falling back, it waits briefly for the port to free; a port
// re-bound in the meantime is left running and no second copy is started.
func StartInCmux(ctx *Context, worktreeName, serverName string) error {
	spec, ok := ctx.Config.Servers[serverName]
	if !ok {
		return fmt.Errorf("unknown server %q (available: %s)", serverName, strings.Join(serverNames(ctx.Config), ", "))
	}

	envVar, ok := envVarFor(ctx.Config, spec)
	if !ok {
		return fmt.Errorf("server %q references port kind %q not found in ports config", serverName, spec.PortKind)
	}

	wtDir := filepath.Join(ctx.ContainerDir, worktreeName)
	port, err := readPortForStart(wtDir, envVar)
	if err != nil {
		return fmt.Errorf("read port: %w", err)
	}

	if busy, pids, _ := portBusy(port); busy {
		skip, err := checkOwnListener(ctx, wtDir, serverName, spec.PortKind, port, pids)
		if err != nil {
			return err
		}
		if skip {
			return nil
		}
	}

	wrappedCmd := wrapCommand(wtDir, spec.Command)

	if err := spawnSidecar(cmux.WorkspaceID(), wtDir, wrappedCmd); err != nil {
		fmt.Fprintf(out, "⚠ cmux sidecar unavailable (%v) — falling back to background daemon\n", err)
		return fallbackToDaemon(wtDir, serverName, spec.Command, port)
	}

	fmt.Fprintf(out, "✓ %s starting on :%d in a cmux sidecar\n", serverName, port)
	fmt.Fprintf(out, "  url: http://localhost:%d\n", port)

	if !waitPortBusy(port, sidecarBootTimeout) {
		fmt.Fprintf(out, "⚠ %s never came up on :%d in the cmux sidecar — falling back to background daemon\n", serverName, port)
		return fallbackToDaemon(wtDir, serverName, spec.Command, port)
	}

	if !waitPortStable(port, sidecarSettleWindow, sidecarSettleInterval) {
		fmt.Fprintf(out, "⚠ %s came up then died in the cmux sidecar (port :%d dropped) — falling back to background daemon\n", serverName, port)
		fmt.Fprintf(out, "  (cmux badges lost, but the daemon is reliable and logs to %s)\n", LogPath(wtDir, serverName))
		return fallbackToDaemon(wtDir, serverName, spec.Command, port)
	}

	fmt.Fprintf(out, "● %s live on :%d\n", serverName, port)
	if err := writeOwner(wtDir, serverName, OwnerID(wtDir)); err != nil {
		fmt.Fprintf(out, "  ⚠ could not record server owner: %v\n", err)
	}
	return nil
}

// waitPortBusy polls the port until it has a listener or the timeout elapses.
// Used to ride out a sidecar PTY that starts its shell lazily.
func waitPortBusy(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if busy, _, _ := portBusy(port); busy {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		sleepFn(500 * time.Millisecond)
	}
}

// waitPortStable re-checks that a port keeps a listener across the whole
// settle window. Returns true only if no probe over [0, settle] sees the port
// free; a single drop returns false immediately. Honest survival check: the
// first port-up can be a transient the sidecar loses under CPU contention.
//
// A probe that failed (lsof error) is not a drop. It says nothing about the
// port, and reading it as "free" would declare a live sidecar dead and start a
// second copy of the server next to it.
func waitPortStable(port int, settle, interval time.Duration) bool {
	deadline := time.Now().Add(settle)
	for {
		if busy, _, err := portBusy(port); err == nil && !busy {
			return false
		}
		if !time.Now().Before(deadline) {
			return true
		}
		sleepFn(interval)
	}
}

// waitPortFree waits up to grace for a port to have no listener, and never
// kills. Returns whether the port was seen free, and otherwise the pids holding
// it at the last probe.
//
// It used to kill whatever still held the port once the grace ran out. But by
// then the sidecar has already been judged dead — its port dropped, or never
// came up — so a listener present at the end of the grace bound the port during
// it: a sidecar that restarted, or a twin server on the same port. Killing it
// took down a healthy server to make room for a copy of itself. A probe that
// fails (lsof error) proves nothing either way and is retried; one still
// failing at the deadline is reported as not free, with no holders.
func waitPortFree(port int, grace, interval time.Duration) (free bool, holders []int) {
	deadline := time.Now().Add(grace)
	for {
		busy, pids, err := portBusy(port)
		if err == nil && !busy {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			if err != nil {
				return false, nil
			}
			return false, pids
		}
		sleepFn(interval)
	}
}

// Stop kills servers for a worktree. serverName == "" stops them all
// (the historical behavior); a non-empty name stops just that server. An
// explicit target like this is an explicit intention, so it is never scoped
// by ownership — that only applies to the blanket sweep in StopAll.
func Stop(ctx *Context, worktreeName, serverName string) error {
	servers, err := serversToStop(ctx.Config, serverName)
	if err != nil {
		return err
	}
	wtDir := filepath.Join(ctx.ContainerDir, worktreeName)
	probed := probeServers(ctx, wtDir, servers)
	stopServers(wtDir, probed)
	// Reclaim the Next.js/Turbopack dev cache: it is the heaviest disk hog
	// across parallel worktrees (often >10 GB each) and `next dev` rebuilds it
	// on the next start, so purging an idle/stopped worktree is safe. Only the
	// dev server writes it, so stopping just storybook must not purge.
	if shouldPurgeNextCache(probed) {
		purgeNextDevCache(wtDir)
	}
	return nil
}

// probedServer is one configured server of a worktree with its port state
// sampled once.
type probedServer struct {
	name string
	spec pigtail.ServerSpec
	port int
	busy bool  // an owned pid listens (a foreign-only port is not busy)
	pids []int // owned pids only: the ones a stop may kill
	// own classifies the port's listeners. When no owned pid listens and
	// the port is held — by another worktree, or by an owner the cwd probe
	// could not determine — nothing is killed and the output says which.
	own Ownership
}

// probeServers resolves each server's port from .env.local and probes it once.
// IsPortBusy shells out to lsof, and a blanket stop needs the same answer
// twice — once to decide whether ownership protects the server, once to know
// what to kill — so it is sampled here and carried, not asked for again.
//
// Servers whose port kind is missing from the ports config, or whose port is
// absent from .env.local, are dropped: there is nothing to stop and nothing to
// protect. Sorted by name so a sweep's output is stable rather than following
// Go's randomised map order.
func probeServers(ctx *Context, wtDir string, servers map[string]pigtail.ServerSpec) []probedServer {
	probed := make([]probedServer, 0, len(servers))
	for name, spec := range servers {
		envVar, ok := envVarFor(ctx.Config, spec)
		if !ok {
			continue
		}
		port, err := ReadPort(wtDir, envVar)
		if err != nil {
			continue
		}
		own, _ := OwnedListeners(port, wtDir)
		probed = append(probed, probedServer{name: name, spec: spec, port: port, busy: len(own.Owned) > 0, pids: own.Owned, own: own})
	}
	sort.Slice(probed, func(i, j int) bool { return probed[i].name < probed[j].name })
	return probed
}

// stopServers kills whichever of the already-probed servers hold their port,
// in one worktree. It is the single body shared by Stop (an explicit, unscoped
// target) and the scope-filtered sweep in StopAll — the .next/dev/cache purge
// guard (shouldPurgeNextCache) stays the caller's job so it is not duplicated
// here.
//
// It acts per port, not per name. A project may list one process under several
// names (a large monorepo: `dev` and `web`); the kill takes the port's listeners, so it
// takes every name at once. Each name is announced to the daemon and has its
// owner marker cleared, the process is killed once, and the report says the
// twin went down with it instead of pretending only one server stopped. Returns
// the number of processes stopped.
func stopServers(wtDir string, probed []probedServer) int {
	stopped := 0
	for _, group := range groupProbedByPort(probed) {
		var pids []int
		busy := false
		var held *Ownership
		for i, p := range group {
			if p.busy {
				busy = true
				pids = appendUniquePIDs(pids, p.pids)
			}
			if held == nil && p.own.HeldElsewhere() {
				held = &group[i].own
			}
		}
		if !busy && held != nil {
			// Held, but not by a process known to run in this worktree:
			// another worktree's server, or an owner the cwd probe could not
			// determine. Not killed either way — say which, and never call an
			// unknown owner another worktree.
			retry := ""
			if held.Unknown() {
				retry = "; retry"
			}
			fmt.Fprintf(out, "  · kept %s (:%d) — %s, not stopped%s\n",
				group[0].name, group[0].port, held.Describe(filepath.Dir(wtDir)), retry)
		}
		if !busy {
			// The server is already gone — crashed, OOM-killed, killed by hand,
			// or reaped by one of the daemon's own paths — outside of this
			// call. Its marker must not outlive it: a stale marker would make a
			// later scoped sweep wrongly spare a dead server (nothing to spare),
			// and worse, would hand a foreign "owner" to whatever next process
			// happens to bind the same port.
			for _, p := range group {
				clearOwner(wtDir, p.name)
			}
			continue
		}
		// Announce the stop BEFORE killing, not after: the daemon's death
		// detector runs on its own sweep clock, and a server that releases its
		// port faster than the notification lands is reported as a death. That
		// race is not theoretical — it fired on the first end-to-end run
		// (2026-07-27, 2s sweep interval). Marking first costs nothing: an
		// unfulfilled mark simply expires.
		for _, p := range group {
			notifyStopped(wtDir, p.name)
		}
		killPIDsFn(withDescendants(pids))
		for _, p := range group {
			clearOwner(wtDir, p.name)
		}
		line := fmt.Sprintf("  ✓ %s (:%d)", group[0].name, group[0].port)
		if len(group) > 1 {
			twins := make([]string, 0, len(group)-1)
			for _, p := range group[1:] {
				twins = append(twins, p.name)
			}
			line += fmt.Sprintf(" — also stopped %s (same port)", strings.Join(twins, ", "))
		}
		fmt.Fprintln(out, line)
		stopped++
	}
	if stopped == 0 {
		fmt.Fprintf(out, "  (no servers running)\n")
	}
	return stopped
}

// groupProbedByPort partitions probed servers into one group per port,
// preserving the input order (probeServers sorts by name) inside and across
// groups. A non-positive port is not an identity and stands alone. Pure.
func groupProbedByPort(probed []probedServer) [][]probedServer {
	var groups [][]probedServer
	at := map[int]int{}
	for _, p := range probed {
		if p.port > 0 {
			if i, ok := at[p.port]; ok {
				groups[i] = append(groups[i], p)
				continue
			}
			at[p.port] = len(groups)
		}
		groups = append(groups, []probedServer{p})
	}
	return groups
}

// appendUniquePIDs appends the pids not already in dst.
func appendUniquePIDs(dst, pids []int) []int {
	for _, pid := range pids {
		seen := false
		for _, d := range dst {
			if d == pid {
				seen = true
				break
			}
		}
		if !seen {
			dst = append(dst, pid)
		}
	}
	return dst
}

// serversToStop returns the servers matching the filter ("" = all). A named
// server expands to every name sharing its port: the stop kills the port's
// listeners, so those servers go down with it and must be treated as stopped.
func serversToStop(cfg *pigtail.ProjectConfig, serverName string) (map[string]pigtail.ServerSpec, error) {
	if serverName == "" {
		return cfg.Servers, nil
	}
	if _, ok := cfg.Servers[serverName]; !ok {
		return nil, fmt.Errorf("unknown server %q (available: %s)", serverName, strings.Join(serverNames(cfg), ", "))
	}
	out := map[string]pigtail.ServerSpec{}
	for _, name := range NamesSharingPort(cfg, serverName) {
		out[name] = cfg.Servers[name]
	}
	return out, nil
}

// NamesSharingPort returns every configured server name bound to the same port
// as name — name included, sorted. Servers share a port when their port kinds
// resolve to the same port variable (a large monorepo lists `dev` and `web` on the dev
// port): one process, several names. An unknown name yields nil; a name whose
// port kind is missing from the ports config shares with nobody. Pure.
func NamesSharingPort(cfg *pigtail.ProjectConfig, name string) []string {
	spec, ok := cfg.Servers[name]
	if !ok {
		return nil
	}
	envVar, ok := envVarFor(cfg, spec)
	if !ok {
		return []string{name}
	}
	var out []string
	for other, otherSpec := range cfg.Servers {
		if v, ok := envVarFor(cfg, otherSpec); ok && v == envVar {
			out = append(out, other)
		}
	}
	sort.Strings(out)
	return out
}

// shouldPurgeNextCache reports whether the given set of servers actually
// being acted on includes a Next.js dev server (PortKind == "dev"), which
// writes .next/dev/cache. It takes the already-filtered server set — Stop's
// serversToStop result, or a scoped sweep's in-scope subset — rather than
// re-deriving it from the full project config, so a worktree whose dev server
// a scoped stop leaves running (someone else's session) never has its cache
// purged just because the project has a dev-kind server configured somewhere:
// that would be the same kind of cross-session damage this package exists to
// stop.
func shouldPurgeNextCache(probed []probedServer) bool {
	for _, p := range probed {
		if p.spec.PortKind == "dev" {
			return true
		}
	}
	return false
}

// purgeNextDevCache removes the Next.js dev cache (.next/dev/cache, written by
// Turbopack under `next dev`) for a worktree. Next recreates it on the next
// `next dev`, so this is safe once the dev servers are stopped. We deliberately
// keep .next/static and .next/server (build output) intact. Retries briefly to
// ride out transient file locks (antivirus, lingering handles).
func purgeNextDevCache(wtDir string) {
	cacheDir := filepath.Join(wtDir, ".next", "dev", "cache")
	info, err := os.Stat(cacheDir)
	if err != nil {
		return // nothing to purge
	}
	var freed int64
	if info.IsDir() {
		freed = dirSize(cacheDir)
	}
	var rmErr error
	for i := 0; i < 3; i++ {
		if rmErr = os.RemoveAll(cacheDir); rmErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if rmErr != nil {
		fmt.Printf("  ⚠ could not purge .next/dev/cache: %v\n", rmErr)
		return
	}
	if freed > 0 {
		fmt.Printf("  🧹 purged .next/dev/cache (%s)\n", humanBytes(freed))
	}
}

// dirSize sums the sizes of all regular files under path (best-effort).
func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// humanBytes renders a byte count as a compact human-readable string.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Scope bounds a blanket stop across every worktree in a container.
type Scope int

const (
	// ScopeOwned stops this session's servers and unclaimed orphans. The
	// default: a blanket stop used to be a machine-wide guillotine — one agent
	// session freeing memory would kill every other session's dev servers,
	// silently and with no trace in the daemon log.
	ScopeOwned Scope = iota
	// ScopeEverywhere stops every server, whoever started it.
	ScopeEverywhere
)

// shouldStopInScope reports whether a worktree's server is in scope for a
// blanket stop. Pure — alive is computed by the caller, which already does
// the marker I/O — so the safety property (another *live* session's server is
// spared) stays testable without starting a single process.
func shouldStopInScope(scope Scope, owner, me string, alive bool) bool {
	if scope == ScopeEverywhere {
		return true
	}
	if ownedByMe(owner, me) {
		return true
	}
	// An owner whose session is gone is a forgotten server, not someone
	// else's work: reclaiming it is what keeps --all a tidying tool once
	// every session carries an identity.
	return !alive
}

// skippedServer names a server a scoped stop left running, and who owns it —
// used to report what a blanket stop kept alive and why.
type skippedServer struct {
	name  string
	owner string
}

// triageScope splits a worktree's already-probed servers into the ones a
// blanket stop at this scope may touch and the ones it must spare.
//
// Ownership only protects what is RUNNING. A server whose port is free has
// nothing left to protect, so its marker is not consulted at all — it falls in
// scope, and stopServers clears the stale marker on its !busy branch.
//
// That single guard is what keeps the marker from fossilising. Six kill paths
// leave a marker behind (`stop --idle`, the daemon's RAM/battery/idle-purge/cap
// evictions, the menu bar, `rename --force`, the TUI's off toggle), and a
// session with no identity cannot overwrite one on the next start. Honouring a
// marker whose server is gone therefore made the damage permanent: `pigtail
// stop --all` would spare — forever — a server the caller had just started
// themselves, and because that worktree then contributed nothing to the
// in-scope set, its .next/dev/cache (>10 GB) silently stopped being purged.
// Adding clearOwner to six call sites would fix today's six and miss the
// seventh; refusing to let a free port claim protection cannot be forgotten.
//
// reclaimed and seized are both "in scope AND owned by someone else", split
// by whether that someone else's session is still alive. reclaimed is a dead
// owner — a forgotten server, the tidying `stop --all` exists for. seized is
// a LIVE owner taken only because scope == ScopeEverywhere: the unguarded,
// consequential kill, and the one that must never be the one that stays
// silent. Distinguishing them is what lets the caller word a live seizure
// differently from a dead-owner reclaim (finding 2 of the 2026-07-27 final
// review: --everywhere used to announce only the small kill).
func triageScope(probed []probedServer, wtDir string, scope Scope, me string) (inScope []probedServer, skipped, reclaimed, seized []skippedServer) {
	for _, p := range probed {
		if !p.busy {
			inScope = append(inScope, p)
			continue
		}
		owner := ReadOwner(wtDir, p.name)
		alive := OwnerAlive(owner)
		if !shouldStopInScope(scope, owner, me, alive) {
			skipped = append(skipped, skippedServer{name: p.name, owner: owner})
			continue
		}
		if owner != "" && owner != me {
			if alive {
				if scope == ScopeEverywhere {
					seized = append(seized, skippedServer{name: p.name, owner: owner})
				}
			} else {
				reclaimed = append(reclaimed, skippedServer{name: p.name, owner: owner})
			}
		}
		inScope = append(inScope, p)
	}
	return inScope, skipped, reclaimed, seized
}

// stopScoped is Stop filtered by ownership scope: it stops one worktree's
// in-scope servers via the same stopServers body Stop uses, and applies the
// same shouldPurgeNextCache/purgeNextDevCache guard — scoped to that in-scope
// subset, so a worktree whose dev server is spared for another session never
// has its cache purged.
func stopScoped(wtDir string, inScope []probedServer) int {
	stopped := stopServers(wtDir, inScope)
	if shouldPurgeNextCache(inScope) {
		purgeNextDevCache(wtDir)
	}
	return stopped
}

// StopAll stops servers across all worktrees, bounded by scope. See Scope for
// why ScopeOwned is the safe default.
func StopAll(ctx *Context, scope Scope) error {
	entries, err := os.ReadDir(ctx.ContainerDir)
	if err != nil {
		return err
	}
	// ctx.WorktreeDir is the caller's own worktree — ResolveContext always
	// sets it from cwd — never the worktree currently being swept (that one
	// is wtDir, below). "Who am I" must answer for the worktree this session
	// is running from, not for whichever one the loop happens to be on.
	me := OwnerID(ctx.WorktreeDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wtDir := filepath.Join(ctx.ContainerDir, e.Name())
		if _, err := os.Stat(filepath.Join(wtDir, ".env.local")); err != nil {
			continue
		}
		inScope, skipped, reclaimed, seized := triageScope(probeServers(ctx, wtDir, ctx.Config.Servers), wtDir, scope, me)
		fmt.Fprintf(out, "→ %s\n", e.Name())
		for _, s := range skipped {
			fmt.Fprintf(out, "  · kept %s (owned by %s)\n", s.name, s.owner)
		}
		// A sweep just accused of killing without leaving a trace must say
		// what it takes and why: reclaiming is invisible otherwise, since the
		// marker is cleared on the way out.
		for _, s := range reclaimed {
			fmt.Fprintf(out, "  ↻ reclaimed %s (owner %s — session gone)\n", s.name, s.owner)
		}
		// A live seizure is the more consequential kill of the two — someone
		// else's server, still running — so --everywhere must not let it pass
		// in silence while a dead-owner reclaim next door gets a line. Worded
		// so it cannot be mistaken for "session gone": it isn't.
		for _, s := range seized {
			fmt.Fprintf(out, "  ⚠ took %s from another LIVE session (owner %s, --everywhere)\n", s.name, s.owner)
		}
		if len(inScope) == 0 && len(skipped) > 0 {
			continue // every server here belongs to another session
		}
		stopScoped(wtDir, inScope)
	}
	return nil
}

// ListRunning scans all worktrees for running servers.
func ListRunning(ctx *Context) ([]RunningServer, error) {
	var result []RunningServer
	entries, err := os.ReadDir(ctx.ContainerDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wtDir := filepath.Join(ctx.ContainerDir, e.Name())
		for name, spec := range ctx.Config.Servers {
			envVar, ok := envVarFor(ctx.Config, spec)
			if !ok {
				continue
			}
			port, err := ReadPort(wtDir, envVar)
			if err != nil {
				continue
			}
			busy, pids, _ := IsPortBusy(port)
			if busy {
				result = append(result, RunningServer{
					WorktreeName: e.Name(),
					ServerName:   name,
					Port:         port,
					PIDs:         pids,
				})
			}
		}
	}
	return result, nil
}

// envVarFor returns the env var carrying the port for a server spec.
// Returns false when the server's port kind has no entry in the ports config.
func envVarFor(cfg *pigtail.ProjectConfig, spec pigtail.ServerSpec) (string, bool) {
	portSpec, ok := cfg.Ports[spec.PortKind]
	if !ok {
		return "", false
	}
	if portSpec.Env != "" {
		return portSpec.Env, true
	}
	return pigtail.DefaultPortEnv(spec.PortKind), true
}

// ReadPort reads a port number from .env.local in the given directory.
func ReadPort(dir string, envVar string) (int, error) {
	port, _, err := readPort(dir, envVar)
	return port, err
}

// readPortForStart resolves the port of a server about to be started, and is
// the one place that reports a .env.local assigning it more than once.
//
// Such files are real: the pre-fix mergeEnvLocal appended a port line whenever
// it failed to recognise the one already there (`export PORT=…`), leaving a
// worktree whose file states two ports. Resolving it silently — last one wins,
// like the shell — is correct but leaves a user looking at an unexpected port
// with nothing to go on.
//
// Deliberately NOT in ReadPort: probeServers (every `stop --idle`, which runs
// inside the daemon) and ListRunning (GET /servers, polled by the menu bar)
// call it for every server of every worktree of every project. The same line
// would repeat forever in a process with no user attached. A start is a
// discrete thing a person just asked for.
func readPortForStart(dir string, envVar string) (int, error) {
	port, assignments, err := readPort(dir, envVar)
	if err != nil {
		return 0, err
	}
	if assignments > 1 {
		fmt.Fprintf(out, "⚠ %s is assigned %d times in %s — using :%d, the last one, like the shell that sources it. "+
			"Usually an inherited line left behind: delete the stale one.\n",
			envVar, assignments, filepath.Join(dir, ".env.local"), port)
	}
	return port, nil
}

// readPort resolves the port and reports how many times .env.local assigns it,
// so a caller can warn about a contradictory file without reading it twice.
func readPort(dir string, envVar string) (port, assignments int, err error) {
	path := filepath.Join(dir, ".env.local")
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("no .env.local in %s", dir)
	}
	// envlocal reads the file the way the shell that sources it does, so a
	// hand-edited `export PORT=…`, `PORT = 3000` or `PORT=3000 # dev` resolves
	// to the port the dev server will actually bind. Getting this wrong is not
	// a cosmetic parse failure: the caller falls back to 3000, which is the
	// collision `pigtail dev` exists to prevent.
	val, assignments := envlocal.LookupN(b, envVar)
	if assignments == 0 {
		return 0, 0, fmt.Errorf("%s not found in %s", envVar, path)
	}
	port, err = strconv.Atoi(val)
	if err != nil {
		return 0, assignments, fmt.Errorf("invalid %s value in %s: %q", envVar, path, val)
	}
	return port, assignments, nil
}

// IsPortBusy reports whether a port has a TCP listener, and which processes
// hold it. Every caller wants the *server*: the "already running" guard in
// Start, the stop path (which kills what it gets back), the watchdog's
// escalation, the rename guard, and the RAM monitor's process-identity check.
//
// `-sTCP:LISTEN` is what makes that true. Without it lsof matches a socket by
// *either* endpoint, so a browser tab open on the dev server comes back as a
// process holding the port — and is then killed by the stop path, or read by
// the RAM monitor as the server having restarted.
//
// The flag order is load-bearing, and the shorter forms are not equivalent:
//
//	lsof -ti :P                 works, but matches clients too (the bug)
//	lsof -ti -sTCP:LISTEN :P    errors: -i takes no argument here, so :P is
//	                            read as a *file name* ("status error on :P")
//	lsof -ti -sTCP:LISTEN -i :P dumps every listener on the machine: the
//	                            argument-less -i selects all internet files,
//	                            OR'd with the second -i
//	lsof -t -sTCP:LISTEN -i :P  correct
//
// A UDP socket bound to the same port is out of scope: the state filter only
// constrains TCP, and no pigtail-managed server is UDP.
func IsPortBusy(port int) (bool, []int, error) {
	out, err := exec.Command("lsof", "-t", "-sTCP:LISTEN", "-i", fmt.Sprintf(":%d", port)).CombinedOutput()
	return portProbeResult(port, out, err)
}

// portProbeResult interprets one lsof invocation.
//
// lsof exits 1 when nothing matched. That is the ordinary "port is free"
// answer, not a failure, and it is the reason the error used to be dropped
// wholesale. But the same nil error was also returned when lsof could not run
// at all — absent from PATH, EMFILE under load, EPERM — and "the probe did not
// happen" then reached every caller as "the port is free".
//
// The consequence is not cosmetic. rammon watches for a listener that was
// there and is gone, and records a death when it disappears; a PATH that
// briefly loses lsof therefore produced a volley of deaths for servers that
// never stopped. rammon has always had an `unknown` branch written for exactly
// this — "an lsof failure is not evidence of a free port" — which could never
// run while the error was nil. This is what wakes it up.
//
// Callers that probe to decide whether to act (the watchdog, `pigtail stop`)
// already read `err != nil` the same way as "not busy" and are unaffected.
func portProbeResult(port int, out []byte, err error) (bool, []int, error) {
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
				return false, nil, fmt.Errorf("lsof :%d: %w: %s", port, err, trimmed)
			}
			return false, nil, fmt.Errorf("lsof :%d: %w", port, err)
		}
		// Exit 1: no match. The port is free, and that is an answer.
		return false, nil, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var pids []int
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		pid, err := strconv.Atoi(l)
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return len(pids) > 0, pids, nil
}

// ResolveContextFromPath resolves the project context from a known worktree
// path (used by the TUI which is not necessarily inside the worktree).
func ResolveContextFromPath(wtPath string) (*Context, error) {
	return ResolveContext(wtPath)
}

func findContainer(cwd string) (string, error) {
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, pigtail.ProjectConfigFilename)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w (no %s found)", ErrNotInProject, pigtail.ProjectConfigFilename)
		}
		dir = parent
	}
}

func resolveWorktreeName(cwd, containerDir, mainWorktree string) string {
	rel, err := filepath.Rel(containerDir, cwd)
	if err != nil || rel == "." {
		return mainWorktree
	}
	parts := strings.SplitN(rel, string(os.PathSeparator), 2)
	return parts[0]
}

// wrapCommand builds the shell snippet a server runs: source .env.local (so
// PORT and friends are exported) then exec the server command. Shared by the
// foreground, background, and cmux-sidecar launch paths so they stay in sync.
//
// The .env.local is referenced by ABSOLUTE path on purpose. The cmux sidecar
// runs an interactive zsh, where `. .env.local` (a bare name with no slash) is
// looked up along $PATH rather than the cwd, failing with "no such file or
// directory". An absolute path resolves correctly in bash and zsh alike,
// independent of the shell's cwd. Sourcing is guarded by a file test so a
// missing .env.local skips cleanly instead of aborting the whole command.
func wrapCommand(dir, command string) string {
	envFile := shellQuote(filepath.Join(dir, ".env.local"))
	return fmt.Sprintf("set -a; [ -f %s ] && . %s; set +a; %s", envFile, envFile, command)
}

// shellQuote single-quotes a string for safe interpolation into a shell command
// line (paths may contain spaces).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runForeground(dir, command string, port int) error {
	wrappedCmd := wrapCommand(dir, command)
	cmd := exec.Command("sh", "-c", wrappedCmd)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	fmt.Printf("Starting on :%d (foreground)...\n", port)
	return cmd.Run()
}

// fallbackToDaemon starts the reliable background daemon once the sidecar's
// port is free. Idempotent: a free port goes straight to runBackground.
//
// If the port is still held after the grace, something bound it while the
// sidecar was being given up on — a restarted sidecar, or a twin server on the
// same port — and it is left alone: no kill, no second copy that could only
// fail to bind, and no owner marker for a server this call did not start. The
// warning names the pids and the command that stops them if they are stale.
func fallbackToDaemon(wtDir, serverName, command string, port int) error {
	if free, holders := waitPortFree(port, sidecarFreeGrace, sidecarSettleInterval); !free {
		if len(holders) > 0 {
			fmt.Fprintf(out, "  ⚠ :%d re-bound by pids %v — leaving it running, not starting a second copy (pigtail stop --server %s if stale)\n",
				port, holders, serverName)
		} else {
			fmt.Fprintf(out, "  ⚠ :%d could not be probed (lsof failed) — not starting a second copy (pigtail stop --server %s, then retry, if nothing runs)\n",
				port, serverName)
		}
		return nil
	}
	if err := runBackgroundFn(wtDir, serverName, command, port); err != nil {
		return err
	}
	// The sidecar died (or never came up); the daemon has taken over the same
	// server, so re-record ownership under the daemon-backed path too.
	if err := writeOwner(wtDir, serverName, OwnerID(wtDir)); err != nil {
		fmt.Fprintf(out, "  ⚠ could not record server owner: %v\n", err)
	}
	return nil
}

// LogPath is where a background dev server's output goes, in pigtail's state
// directory rather than at the worktree root. Pure — the directory is created
// by EnsureStateDir, which every writer calls first.
//
// Exported because the TUI starts its own background server (it must not go
// through Stop/Start, which print to stdout and would corrupt its rendering)
// and has to name the same file. The path used to be rebuilt by hand there,
// with its own fmt.Sprintf — two spellings of one convention, which is how a
// convention drifts.
func LogPath(wtDir, serverName string) string {
	return stateFile(wtDir, serverName, ".log")
}

func runBackground(dir, serverName, command string, port int) error {
	if _, err := EnsureStateDir(dir); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	logFile := LogPath(dir, serverName)

	// Rotate previous log.
	if _, err := os.Stat(logFile); err == nil {
		_ = os.Rename(logFile, logFile+".prev")
	}

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}

	wrappedCmd := wrapCommand(dir, command)
	cmd := exec.Command("sh", "-c", wrappedCmd)
	cmd.Dir = dir
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return fmt.Errorf("start: %w", err)
	}
	_ = f.Close()

	fmt.Fprintf(out, "✓ %s started on :%d (pid %d)\n", serverName, port, cmd.Process.Pid)
	fmt.Fprintf(out, "  log: %s\n", logFile)
	fmt.Fprintf(out, "  url: http://localhost:%d\n", port)
	return nil
}

// hookTimeout bounds a single post-create hook.
//
// The default hook is `npm install`, which on a cold cache over a slow link
// genuinely takes minutes — so the bound has to be generous enough not to fire
// on a legitimately slow install. What it exists to stop is the other case: an
// unreachable registry or a lock that never releases, where the hook does not
// take long, it takes forever.
const hookTimeout = 10 * time.Minute

// hookKillGrace is how long a cancelled hook has to exit after SIGTERM before
// it is killed outright.
const hookKillGrace = 15 * time.Second

// HooksTimeout bounds the whole post-create sequence, every hook together.
// The hooks run detached from the request that created the workspace (a
// client that gives up no longer kills them), so this is the only bound left;
// clients size their own deadline on it (NewWorkspaceTimeout).
const HooksTimeout = 10 * time.Minute

// NewWorkspaceTimeout is the budget of POST /workspaces/new — shared by
// pigtail new and session spawn --new-worktree, and the base of the session
// budget's pending-lease expiry. The hooks no longer die with the client, but
// the client still waits for their verdict: the bound covers the hooks' own
// (HooksTimeout) plus the worktree creation.
const NewWorkspaceTimeout = HooksTimeout + 5*time.Minute

// HookError is a hook that stopped the sequence: failed, timed out or
// cancelled. The hooks after it did not run.
type HookError struct {
	Hook string
	Err  error
}

func (e *HookError) Error() string { return e.Err.Error() }
func (e *HookError) Unwrap() error { return e.Err }

// RunHooks executes post-create hooks in the given worktree directory.
// Each hook runs sequentially; if a hook has IfFile set, it is skipped
// when that file does not exist in the worktree.
//
// Three things it is careful about, each of which was wrong before:
//
//   - **Bounded.** A hook used to run with no deadline at all, on a goroutine
//     serving an HTTP request. `npm install` against an unreachable registry
//     therefore pinned that goroutine for the life of the daemon.
//   - **Killed as a group.** The hook is `sh -c "npm install"`, so killing the
//     command kills the shell and leaves npm reparented to init, still running,
//     still holding its lock. Setpgid plus a signal to -pgid takes the tree.
//   - **Logged per worktree.** The output used to go to the daemon's own
//     stdout, which under launchd is ~/.config/pigtail/daemon.log — an
//     unrotated file that had every npm install of every workspace in it. It
//     now lands beside that worktree's server logs, under the state root
//     (see StateDir), and rotates to .prev like they do.
//
// ctx cancellation is honoured: ctx is the daemon's own (cancelled when it
// stops, or at HooksTimeout for the whole sequence), no longer the client's —
// a client that gives up does not stop the hooks. A hook cancelled that way is
// released as a group rather than orphaned, and leaves a partial install,
// which `npm install` is designed to resume — unlike an orphan, which nothing
// cleans up.
func RunHooks(ctx context.Context, worktreeDir string, hooks []pigtail.HookSpec) error {
	if len(hooks) == 0 {
		return nil
	}
	logFile, logPath, err := openHookLog(worktreeDir)
	if err != nil {
		// Not fatal: a hook that cannot be logged is still worth running.
		// Falling back to the daemon's stdout is what this change moves away
		// from, so it goes to io.Discard and the error is surfaced instead.
		fmt.Printf("  hooks: cannot open log (%v); output discarded\n", err)
	}
	defer func() {
		if logFile != nil {
			_ = logFile.Close()
		}
	}()

	sink := io.Discard // io.Discard is declared as io.Writer, so this stays assignable
	if logFile != nil {
		sink = logFile
	}

	for _, h := range hooks {
		if h.IfFile != "" {
			if _, err := os.Stat(filepath.Join(worktreeDir, h.IfFile)); err != nil {
				continue
			}
		}
		fmt.Printf("  hook %s: %s (log: %s)\n", h.Name, h.Command, logPath)
		fmt.Fprintf(sink, "\n=== hook %s: %s ===\n", h.Name, h.Command)

		if err := runHook(ctx, worktreeDir, h, sink); err != nil {
			return &HookError{Hook: h.Name, Err: err}
		}
	}
	return nil
}

// runHook runs one hook under its own deadline, killing the whole process
// group if it overruns.
func runHook(ctx context.Context, worktreeDir string, h pigtail.HookSpec, sink io.Writer) error {
	hctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(hctx, "sh", "-c", h.Command) //nolint:gosec // hooks are the project's own pigtail.yaml
	cmd.Dir = worktreeDir
	cmd.Stdout = sink
	cmd.Stderr = sink
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Without this, CommandContext kills `sh` and npm carries on as an orphan.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
			return syscall.Kill(-pgid, syscall.SIGTERM)
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = hookKillGrace

	err := cmd.Run()
	if err == nil {
		return nil
	}
	// Report why it stopped: "npm install failed" and "npm install was still
	// going after ten minutes" call for different reactions.
	if errors.Is(hctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("hook %s timed out after %s", h.Name, hookTimeout)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("hook %s cancelled: %w", h.Name, ctx.Err())
	}
	return fmt.Errorf("hook %s failed: %w", h.Name, err)
}

// openHookLog opens the worktree's hook log, rotating the previous one. It
// returns a nil file (and an error) when the state dir cannot be prepared.
func openHookLog(worktreeDir string) (*os.File, string, error) {
	if _, err := EnsureStateDir(worktreeDir); err != nil {
		return nil, "", fmt.Errorf("state dir: %w", err)
	}
	path := LogPath(worktreeDir, "hooks")
	if _, err := os.Stat(path); err == nil {
		_ = os.Rename(path, path+".prev")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, path, fmt.Errorf("open hook log: %w", err)
	}
	return f, path, nil
}

// KillPIDs sends SIGTERM then SIGKILL to the given PIDs.
func KillPIDs(pids []int) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	time.Sleep(400 * time.Millisecond)
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

package devserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// DefaultStopGrace is how long a teardown waits, after the kill returned, for
// the ports to be released and the killed processes to be gone.
const DefaultStopGrace = 5 * time.Second

// verifyPoll is the pause between two verification rounds.
const verifyPoll = 100 * time.Millisecond

// StopDeps are the seams of StopWorktreeServers; a nil field is the real
// function. Tests inject fakes, so no real process is ever signalled.
type StopDeps struct {
	PortBusy func(port int) (bool, []int, error)
	Cwds     func(pids []int) (map[int]string, error)
	Parents  func() (map[int]int, error)
	Kill     func(pids []int)
	Alive    func(pid int) bool
	Sleep    func(time.Duration)
	Now      func() time.Time
	Grace    time.Duration // 0 = DefaultStopGrace
}

func (d StopDeps) withDefaults() StopDeps {
	if d.PortBusy == nil {
		d.PortBusy = IsPortBusy
	}
	if d.Cwds == nil {
		d.Cwds = ProcessCwds
	}
	if d.Parents == nil {
		d.Parents = ProcessParents
	}
	if d.Kill == nil {
		d.Kill = KillPIDs
	}
	if d.Alive == nil {
		d.Alive = ProcessAlive
	}
	if d.Sleep == nil {
		d.Sleep = time.Sleep
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Grace <= 0 {
		d.Grace = DefaultStopGrace
	}
	return d
}

// ServerStopFailure is one port whose server could not be stopped, or whose
// owner could not be established.
type ServerStopFailure struct {
	Kinds  []string `json:"kinds"`
	Port   int      `json:"port"`
	PIDs   []int    `json:"pids"`
	Reason string   `json:"reason"`
}

// ServersRunningError: at least one dev server of the worktree is (or may
// be) still running. The caller removes nothing.
type ServersRunningError struct{ Failures []ServerStopFailure }

func (e *ServersRunningError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		s := fmt.Sprintf("%s :%d", strings.Join(f.Kinds, "+"), f.Port)
		if len(f.PIDs) > 0 {
			s += fmt.Sprintf(" (pid %s)", joinPIDs(f.PIDs))
		}
		parts = append(parts, s+": "+f.Reason)
	}
	return "dev server still running — " + strings.Join(parts, "; ")
}

// portGroup is one port and the kinds that share it.
type portGroup struct {
	port  int
	kinds []string
}

func groupPorts(ports map[string]int) []portGroup {
	by := map[int][]string{}
	for kind, port := range ports {
		if port > 0 {
			by[port] = append(by[port], kind)
		}
	}
	groups := make([]portGroup, 0, len(by))
	for port, kinds := range by {
		sort.Strings(kinds)
		groups = append(groups, portGroup{port: port, kinds: kinds})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].port < groups[j].port })
	return groups
}

// inspect reads one port: the pids of the worktree's own listeners, or a
// failure when the port's holders cannot be attributed. A listener proven to
// run in another worktree (cwd known, outside wtDir) is not a failure and not
// a root. Nothing here is skippable: no probe error, unknown owner or
// unreadable cwd is ever waved through, --force included.
func inspect(g portGroup, wtDir string, d StopDeps) (owned []int, fail *ServerStopFailure) {
	failure := func(pids []int, reason string) *ServerStopFailure {
		return &ServerStopFailure{Kinds: g.kinds, Port: g.port, PIDs: pids, Reason: reason}
	}
	busy, pids, err := d.PortBusy(g.port)
	if err != nil {
		return nil, failure(nil, fmt.Sprintf("port probe failed: %v", err))
	}
	if !busy {
		return nil, nil
	}
	own := ClassifyListeners(pids, wtDir, d.Cwds)
	if own.Unknown() {
		return nil, failure(own.PIDs, own.Describe(filepath.Dir(wtDir)))
	}
	if blind := unreadableCwd(own, wtDir); len(blind) > 0 {
		return nil, failure(blind, fmt.Sprintf("owner unknown (no readable cwd; pid %s)", joinPIDs(blind)))
	}
	return own.Owned, nil
}

// unreadableCwd lists the foreign listeners whose cwd proves nothing: lsof had
// none, or the kernel reports a deleted directory while the worktree itself
// is gone (a vite started in a removed worktree).
func unreadableCwd(o Ownership, wtDir string) []int {
	_, statErr := os.Stat(wtDir)
	gone := errors.Is(statErr, os.ErrNotExist)
	var blind []int
	for _, p := range o.foreignPIDs() {
		c := o.Foreign[p]
		if c == "" || (gone && strings.HasSuffix(c, " (deleted)")) {
			blind = append(blind, p)
		}
	}
	return blind
}

// StopWorktreeServers stops every dev server listening on ports from inside
// wtDir, with its descendant processes, and proves them gone.
//
// Three phases, the order is the contract:
//  1. Probe every port, kill nothing. Any port that cannot be attributed
//     (probe error, unknown owner, unreadable cwd) refuses the whole call.
//  2. Kill once: the owned listeners and their descendants, from one process
//     table snapshot taken before the kill.
//  3. Verify until the grace elapsed (counted after the kill returned, with at
//     least one round): no owned listener on the port, every killed pid dead.
//
// A server that survives, or is re-bound by a supervisor, is a
// *ServersRunningError. There is no allow-unknown mode: a forced clean
// releases nothing here.
func StopWorktreeServers(wtDir string, ports map[string]int, deps StopDeps) ([]pigtail.StoppedServer, error) {
	d := deps.withDefaults()

	// Phase 1.
	type target struct {
		group portGroup
		roots []int
	}
	var targets []target
	var failures []ServerStopFailure
	for _, g := range groupPorts(ports) {
		roots, fail := inspect(g, wtDir, d)
		switch {
		case fail != nil:
			failures = append(failures, *fail)
		case len(roots) > 0:
			targets = append(targets, target{group: g, roots: roots})
		}
	}
	if len(failures) > 0 {
		return nil, &ServersRunningError{Failures: failures}
	}
	if len(targets) == 0 {
		return nil, nil
	}

	// Phase 2.
	parents, perr := d.Parents()
	if perr != nil {
		parents = nil // roots only
	}
	trees := make([][]int, len(targets))
	var all []int
	for i, t := range targets {
		tree := append(append([]int(nil), t.roots...), Descendants(parents, t.roots)...)
		sort.Ints(tree)
		trees[i] = tree
		all = appendUniquePIDs(all, tree)
	}
	sort.Ints(all)
	d.Kill(all)

	// Phase 3.
	deadline := d.Now().Add(d.Grace)
	for {
		var left []ServerStopFailure
		for i, t := range targets {
			left = append(left, verify(t.group, trees[i], wtDir, d)...)
		}
		if len(left) == 0 {
			break
		}
		remaining := deadline.Sub(d.Now())
		if remaining <= 0 {
			return nil, &ServersRunningError{Failures: left}
		}
		d.Sleep(min(verifyPoll, remaining))
	}
	stops := make([]pigtail.StoppedServer, len(targets))
	for i, t := range targets {
		stops[i] = pigtail.StoppedServer{Kinds: t.group.kinds, Port: t.group.port, PIDs: trees[i]}
	}
	return stops, nil
}

// verify is one verification round for a killed port.
func verify(g portGroup, tree []int, wtDir string, d StopDeps) []ServerStopFailure {
	inTree := map[int]bool{}
	for _, p := range tree {
		inTree[p] = true
	}
	var out []ServerStopFailure
	fail := func(pids []int, reason string) {
		out = append(out, ServerStopFailure{Kinds: g.kinds, Port: g.port, PIDs: pids, Reason: reason})
	}
	owned, f := inspect(g, wtDir, d)
	reported := map[int]bool{}
	if f != nil {
		out = append(out, *f)
	}
	var still, rebound []int
	for _, p := range owned {
		reported[p] = true
		if inTree[p] {
			still = append(still, p)
		} else {
			rebound = append(rebound, p)
		}
	}
	if len(still) > 0 {
		fail(still, "still listening after SIGKILL")
	}
	for _, p := range rebound {
		fail([]int{p}, fmt.Sprintf("re-bound by pid %d after the stop (a supervisor restarted it?)", p))
	}
	var live []int
	for _, p := range tree {
		if !reported[p] && d.Alive(p) {
			live = append(live, p)
		}
	}
	if len(live) > 0 {
		fail(live, "process still alive after SIGKILL")
	}
	return out
}

// ProcessAlive reports whether pid is a live process: signal 0 answers
// (EPERM still means it exists), and a zombie (ps stat Z) is dead — it holds
// no port and only waits to be reaped.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), procTreeTimeout)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true // the signal said alive; ps could not say otherwise
	}
	return !isZombieStat(parseStat(raw))
}

func parseStat(raw []byte) string {
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func isZombieStat(stat string) bool { return strings.HasPrefix(stat, "Z") }

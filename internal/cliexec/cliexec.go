// Package cliexec runs a child command the way a thin wrapper must: same
// stdio, signals relayed, and the child's own exit status handed back — so
// that `pigtail test -- …` and `pigtail admit -- …` are indistinguishable,
// to a shell or an agent, from the command they wrap.
package cliexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// relayed are the signals forwarded to the child. Catching them also keeps
// this process alive long enough to clean up after the child: Go's default
// disposition would kill it mid-syscall, before any defer runs.
var relayed = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// Opts tunes RunWith. The zero value is Run's historical behaviour: the child
// stays in this process's group and signals reach it alone.
type Opts struct {
	// Group runs the child in its own process group; relayed signals then
	// reach the whole group, and the stop procedure (see stopTree) applies.
	Group bool
	// MaxRun, when positive, stops the child's tree once it has run that long
	// (the run then exits 124). Grace is how long a stopped tree has to
	// honour SIGTERM before the pids captured at the stop get SIGKILL
	// (default 10 s). PollEvery paces the Orphaned probe (default 2 s).
	MaxRun, Grace, PollEvery time.Duration
	// Orphaned, when non-nil, is polled every PollEvery; true stops the tree.
	Orphaned func() bool
	// Tree lists the descendants of root (root excluded); nil means "the
	// process group only".
	Tree func(root int) []int
	// OnStop is told why the tree was stopped ("orphaned" or "ceiling"),
	// right after the TERM went out. It is never called for the leftovers
	// of a child that exited on its own.
	OnStop func(reason string)
}

// defaultGrace is how long a stopped tree has before SIGKILL.
const defaultGrace = 10 * time.Second

// kill is a seam: tests record what would be signalled.
var kill = syscall.Kill

// beforeLoop is a test seam: called once, right before the select loop, with
// the channel that carries the child's exit.
var beforeLoop = func(<-chan error) {}

// groupMembers lists the pids whose process group is pgid, by `ps`. A failed
// `ps` gives none: the stop then relies on the group TERM alone.
var groupMembers = func(pgid int) []int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,pgid=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		p, e1 := strconv.Atoi(f[0])
		g, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil && g == pgid && p > 1 {
			pids = append(pids, p)
		}
	}
	return pids
}

// SignalablePgid reports whether a process group may be signalled as a
// whole: never 0 or 1 (kill(-1) is "everything", kill(0) "my own group"),
// never negative, never this process's own group.
func SignalablePgid(pgid int) bool {
	return pgid > 1 && pgid != syscall.Getpgrp()
}

// Run starts cmd (whose Stdin/Stdout/Stderr, Dir and Env the caller has
// already set), relays signals to it until it exits, and returns its exit
// code — 128+signal when a signal killed it, the shell's convention.
//
// err is non-nil only when the child could not be started, or Wait failed
// for a reason other than the child's own exit status.
func Run(cmd *exec.Cmd) (int, error) { return RunWith(cmd, Opts{}) }

// RunWith is Run with Opts.
func RunWith(cmd *exec.Cmd, o Opts) (int, error) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, relayed...)
	defer signal.Stop(sigCh)

	if o.Group {
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Setpgid = true
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start: %w", err)
	}
	pid := cmd.Process.Pid
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// relay sends sig to the child — to its whole group in Group mode.
	relay := func(sig os.Signal) {
		if s, ok := sig.(syscall.Signal); ok && o.Group && SignalablePgid(pid) {
			_ = kill(-pid, s)
			return
		}
		_ = cmd.Process.Signal(sig)
	}

	var (
		stopped  <-chan struct{}
		ceiling  bool // the ceiling stopped the tree: the run exits 124
		maxRunCh <-chan time.Time
		pollCh   <-chan time.Time
	)
	if o.MaxRun > 0 {
		t := time.NewTimer(o.MaxRun)
		defer t.Stop()
		maxRunCh = t.C
	}
	if o.Orphaned != nil {
		every := o.PollEvery
		if every <= 0 {
			every = 2 * time.Second
		}
		t := time.NewTicker(every)
		defer t.Stop()
		pollCh = t.C
	}
	// stop ends the tree once; the TERM is out before OnStop is called, so a
	// slow OnStop (a journal write) is off the critical path.
	stop := func(reason string) {
		if stopped != nil {
			return
		}
		var signalled bool
		stopped, signalled = stopTree(pid, pid, o)
		if signalled {
			ceiling = reason == "ceiling"
			if o.OnStop != nil {
				o.OnStop(reason)
			}
		}
	}
	beforeLoop(waitCh)
	for {
		select {
		case err := <-waitCh:
			code, cerr := exitCode(err)
			if o.Group && stopped == nil && kill(-pid, 0) == nil {
				// The child is gone but its group is not: leftovers. The child is
				// already reaped, so a ppid walk finds nothing: capture the group's
				// members by listing, before the TERM, so that a leftover that
				// ignores it still gets the KILL after the grace. Root 0: the
				// reaped pid is not a victim (it may be reused by a stranger).
				// The members' own descendants (a setsid'd helper) come from
				// the caller's Tree, walked from each live member.
				lo := o
				lo.Tree = func(int) []int {
					members := groupMembers(pid)
					all := append([]int(nil), members...)
					if o.Tree != nil {
						for _, m := range members {
							if m > 1 {
								all = append(all, o.Tree(m)...)
							}
						}
					}
					return all
				}
				stopped, _ = stopTree(0, pid, lo)
			}
			if stopped != nil {
				<-stopped
			}
			if ceiling && cerr == nil {
				code = 124
			}
			return code, cerr
		case sig := <-sigCh:
			// Every signal, not just the first: a child that traps SIGINT and
			// hangs must still receive the second Ctrl+C, which Notify above
			// took away from this process's default handling.
			relay(sig)
		case <-maxRunCh:
			maxRunCh = nil
			// select picks at random between ready cases: a child that has
			// already exited keeps its own code over the ceiling.
			select {
			case err := <-waitCh:
				waitCh <- err
				continue
			default:
			}
			stop("ceiling")
		case <-pollCh:
			// An exited child is not "orphaned": same re-check as the ceiling.
			select {
			case err := <-waitCh:
				waitCh <- err
				continue
			default:
			}
			if stopped == nil && o.Orphaned() {
				pollCh = nil
				stop("orphaned")
			}
		}
	}
}

// stopTree is the one way a tree is stopped. It captures the tree of root by
// ppid (root plus o.Tree(root)), sends SIGTERM to pgid and to every distinct
// pgid of the tree — a chromium that called setsid is in none of the
// child's groups — then, after the grace, SIGKILL to the captured pids still
// there with the pgid they had. The returned channel closes when that is
// over (early if every captured pid is gone); signalled reports that at
// least one TERM reached a group.
//
// It never signals a pgid that is not SignalablePgid, pid <= 1 nor itself.
func stopTree(root, pgid int, o Opts) (done <-chan struct{}, signalled bool) {
	grace := o.Grace
	if grace <= 0 {
		grace = defaultGrace
	}
	type victim struct{ pid, pgid int }
	var victims []victim
	pids := []int{root}
	if o.Tree != nil {
		pids = append(pids, o.Tree(root)...)
	}
	seen := map[int]bool{}
	for _, p := range pids {
		if p <= 1 || p == os.Getpid() || seen[p] {
			continue
		}
		seen[p] = true
		pg, err := syscall.Getpgid(p)
		if err != nil || !SignalablePgid(pg) {
			continue
		}
		victims = append(victims, victim{p, pg})
	}
	groups := map[int]bool{}
	if SignalablePgid(pgid) {
		groups[pgid] = true
	}
	for _, v := range victims {
		groups[v.pgid] = true
	}
	for g := range groups {
		if kill(-g, syscall.SIGTERM) == nil {
			signalled = true
		}
	}

	ch := make(chan struct{})
	go func() {
		defer close(ch)
		present := func() []victim {
			var left []victim
			for _, v := range victims {
				if kill(v.pid, 0) != nil {
					continue
				}
				if pg, err := syscall.Getpgid(v.pid); err == nil && pg == v.pgid {
					left = append(left, v)
				}
			}
			return left
		}
		deadline := time.Now().Add(grace)
		for time.Now().Before(deadline) {
			if len(present()) == 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		for _, v := range present() {
			_ = kill(v.pid, syscall.SIGKILL)
		}
	}()
	return ch, signalled
}

// exitCode maps Wait's error onto a shell exit status.
func exitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, fmt.Errorf("wait: %w", err)
	}
	// A child killed by a signal has no normal exit code (ExitCode reports
	// -1, which os.Exit would wrap to 255): report 128+signal instead.
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return ee.ExitCode(), nil
}

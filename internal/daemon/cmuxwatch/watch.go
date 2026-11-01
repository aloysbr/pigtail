// Package cmuxwatch runs `pigtail cmux check` once after each cmux relaunch
// (subject sub_x000000000000043): the sentinel pid changing is the relaunch.
// In pilot or fix mode, one second pass follows on the rows left busy
// (subject sub_x000000000000006, D6).
package cmuxwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Values of pigtaild --cmux-check-after-relaunch.
const (
	ModeOff    = "off"
	ModeReport = "report" // inventory; the defects show as the sidebar pill (D13 of the previous subject)
	ModePilot  = "pilot"  // D3: repair the pilot's session only, report the rest
	ModeFix    = "fix"    // also repairs every session — after an acceptance (D3)
	// DefaultMode: D3 (2026-10-09) — the pilot only. Without a pilot it is
	// a report (the check's note says so).
	DefaultMode = ModePilot
)

// ParseMode validates a flag value; anything else refuses to start pigtaild.
func ParseMode(s string) (string, error) {
	switch s {
	case ModeOff, ModeReport, ModePilot, ModeFix:
		return s, nil
	}
	return "", fmt.Errorf("--cmux-check-after-relaunch: %q is not off, report, pilot or fix", s)
}

type Config struct {
	Mode       string        // off | report | pilot | fix
	Poll       time.Duration // 30 s
	Settle     time.Duration // 90 s: cmux resumes its sessions first
	Retries    int           // exit 2 (could not look): retry, Settle apart
	StatePath  string        // last cmux pid checked; survives a daemon restart
	SecondPass time.Duration // D6: one more pass on the rows left busy, this long after the first; 0 = none
}

type Deps struct {
	Sentinel func() (int, error)
	// Run runs `pigtail <args>` (the check), returning its exit code.
	Run func(ctx context.Context, args []string) (exit int, err error)
	// Args is the check's argv for a mode — surfaces for the second pass
	// (D6); the log line before each run names it.
	Args func(mode string, surfaces []string) []string
	Logf func(string, ...any)
	// SidebarOn reports whether the cmux sidebar loop runs: the defects show
	// only as its pill (D13). nil = unknown, nothing logged (É19).
	SidebarOn func() bool
	// BusyLeft (D6): the surfaces the last report of launch cmuxPID left busy
	// that this mode repairs (cmuxcheck.Report.BusyLeft). nil = no second pass.
	BusyLeft func(cmuxPID int, pilotOnly bool) ([]string, error)
	// Notice (D2, sub_x000000000000037): called once the last pass of
	// launch cmuxPID is done (pilot and fix modes) — failure "" = read the
	// report; else why the launch was not checked. nil = none.
	Notice func(cmuxPID int, failure string)
}

// FailSecondPass is the Notice failure of a second pass that did not run to
// its end; the detail stays in the journal.
const FailSecondPass = "seconde passe ratée"

// secondPass (D6): the one pass planned on the rows left busy — in memory
// only, forgotten by a daemon restart or a new cmux pid.
type secondPass struct {
	pid      int
	at       time.Time
	surfaces []string
}

type Watcher struct {
	c        Config
	d        Deps
	known    bool // a last-cmux-pid file was read (É1)
	done     int  // cmux pid already checked
	seenPID  int
	seenAt   time.Time
	attempts int
	nextAt   time.Time
	second   secondPass
}

func New(c Config, d Deps) *Watcher {
	w := &Watcher{c: c, d: d}
	if b, err := os.ReadFile(c.StatePath); err == nil {
		w.done, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		w.known = true
	}
	return w
}

// record writes the cmux pid checked (or adopted), surviving a daemon restart.
func (w *Watcher) record(pid int) {
	w.done, w.known = pid, true
	_ = os.MkdirAll(filepath.Dir(w.c.StatePath), 0o700)
	if werr := os.WriteFile(w.c.StatePath, []byte(strconv.Itoa(pid)), 0o600); werr != nil {
		w.d.Logf("cmuxwatch: state not written: %v", werr)
	}
}

// Step is one poll: a new cmux pid starts the settle clock; once settled the
// check runs, and the pid is recorded when it could look (exit 0 or 1) or
// after Retries attempts (logged as given up). A killed check (exit < 0) or
// an error is retried; a cancelled context records nothing.
func (w *Watcher) Step(ctx context.Context, now time.Time) {
	if w.c.Mode == ModeOff {
		return
	}
	pid, err := w.d.Sentinel()
	if err != nil || pid <= 0 {
		return // a read error or cmux down keeps a planned second pass (revue 1 #2)
	}
	if w.second.pid != 0 && pid != w.second.pid {
		w.d.Logf("cmuxwatch: second pass for cmux pid %d dropped — cmux pid now %d", w.second.pid, pid)
		w.second = secondPass{}
	}
	if w.second.pid != 0 && !now.Before(w.second.at) {
		w.runSecond(ctx)
		return
	}
	if pid == w.done {
		return
	}
	if !w.known {
		// É1: first start, no last-cmux-pid — nothing says this cmux was just
		// relaunched. Adopt it, no check.
		w.record(pid)
		w.d.Logf("cmuxwatch: first start, cmux pid %d adopted — no check", pid)
		return
	}
	if pid != w.seenPID {
		w.seenPID, w.seenAt, w.attempts, w.nextAt = pid, now, 0, now.Add(w.c.Settle)
		w.d.Logf("cmuxwatch: cmux pid %d — check (%s) in %s", pid, w.c.Mode, w.c.Settle)
	}
	if now.Before(w.nextAt) {
		return
	}
	w.attempts++
	args := w.d.Args(w.c.Mode, nil)
	w.d.Logf("cmuxwatch: cmux pid %d → check (%s) attempt %d: running pigtail %s", pid, w.c.Mode, w.attempts, strings.Join(args, " "))
	exit, err := w.d.Run(ctx, args)
	if ctx.Err() != nil {
		// pigtaild is stopping: the check was cut, it looked at nothing —
		// never recorded, the next daemon start checks this launch.
		w.d.Logf("cmuxwatch: cmux pid %d → check (%s) attempt %d interrupted (%v) — not recorded", pid, w.c.Mode, w.attempts, ctx.Err())
		return
	}
	if err == nil && exit < 0 { // killed by a signal: no exit code, nothing checked
		err = fmt.Errorf("check killed (exit %d)", exit)
	}
	if err != nil {
		w.d.Logf("cmuxwatch: cmux pid %d → check (%s) attempt %d failed: %v — not recorded as checked", pid, w.c.Mode, w.attempts, err)
	} else {
		w.d.Logf("cmuxwatch: cmux pid %d → check (%s) attempt %d: exit %d", pid, w.c.Mode, w.attempts, exit)
	}
	if err == nil && exit == 1 && w.d.SidebarOn != nil && !w.d.SidebarOn() {
		// D14 (decided 2026-10-08): the check still runs; the pill is the only
		// signal (D13), so say nobody sees it. Exit 1 means a row that would
		// have a pill (D17: reported-only rows exit 0).
		w.d.Logf("cmuxwatch: sidebar off — no pill for the defects: pigtail cmux check --last")
	}
	looked := err == nil && (exit == 0 || exit == 1)
	if looked || w.attempts >= w.c.Retries {
		if !looked {
			w.d.Logf("cmuxwatch: cmux pid %d — gave up after %d attempts, this launch was not checked: pigtail cmux check", pid, w.attempts)
		}
		w.record(pid)
		if !looked {
			w.notice(pid, fmt.Sprintf("abandon après %d essais", w.attempts))
		} else {
			w.planSecond(pid, now)
			if w.second.pid == 0 {
				w.notice(pid, "")
			}
		}
		return
	}
	w.nextAt = now.Add(w.c.Settle)
}

// planSecond (D6): rows left busy that this mode repairs, after a pass
// that looked — one more pass on their surfaces SecondPass later. Pilot and
// fix modes only: report mode repairs nothing (revue 1 #5). Not persisted:
// a daemon restart forgets it.
func (w *Watcher) planSecond(pid int, now time.Time) {
	if w.d.BusyLeft == nil || w.c.SecondPass <= 0 || (w.c.Mode != ModePilot && w.c.Mode != ModeFix) {
		return
	}
	surfaces, err := w.d.BusyLeft(pid, w.c.Mode == ModePilot)
	if err != nil {
		w.d.Logf("cmuxwatch: cmux pid %d — rows left busy unreadable (%v): no second pass", pid, err)
		return
	}
	if len(surfaces) == 0 {
		return
	}
	w.second = secondPass{pid: pid, at: now.Add(w.c.SecondPass), surfaces: surfaces}
	w.d.Logf("cmuxwatch: cmux pid %d — %d rows left busy, second pass in %s", pid, len(surfaces), w.c.SecondPass)
}

// runSecond runs the one second pass; never retried. Like the first pass,
// a cancelled context means pigtaild is stopping: the pass looked at
// nothing, and the restart forgets it (T-g).
func (w *Watcher) runSecond(ctx context.Context) {
	pid, args := w.second.pid, w.d.Args(w.c.Mode, w.second.surfaces)
	w.second = secondPass{}
	w.d.Logf("cmuxwatch: cmux pid %d → second pass (%s): running pigtail %s", pid, w.c.Mode, strings.Join(args, " "))
	exit, err := w.d.Run(ctx, args)
	if ctx.Err() != nil {
		w.d.Logf("cmuxwatch: cmux pid %d → second pass interrupted (%v) — forgotten", pid, ctx.Err())
		return
	}
	if err == nil && exit < 0 {
		err = fmt.Errorf("check killed (exit %d)", exit)
	}
	if err != nil {
		w.d.Logf("cmuxwatch: cmux pid %d → second pass failed: %v — not retried", pid, err)
		w.notice(pid, FailSecondPass)
		return
	}
	w.d.Logf("cmuxwatch: cmux pid %d → second pass exit %d", pid, exit)
	w.notice(pid, "")
}

// notice: repairing modes only — report mode repairs nothing, the pill says it.
func (w *Watcher) notice(pid int, failure string) {
	if w.d.Notice == nil || (w.c.Mode != ModePilot && w.c.Mode != ModeFix) {
		return
	}
	w.d.Notice(pid, failure)
}

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.c.Poll)
	defer t.Stop()
	for {
		w.Step(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

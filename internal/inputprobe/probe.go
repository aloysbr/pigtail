// Package inputprobe tells whether a cmux tab's keyboard input still reaches
// its claude process (sub_x000000000000024, D3, amended 2026-10-27): a
// terminal status query (DSR, ESC [ 5 n) is written to the tty of claude's
// pid; the terminal emulator answers (ESC [ 0 n) on claude's input, the path
// keystrokes also take, and claude's unix syscall counter is read before and
// after. A live input makes claude react, a dead one never does. The only
// byte sequence ever written is Query, only to the pid's own tty; no cmux
// call, no keystroke, no signal.
package inputprobe

import (
	"errors"
	"fmt"
	"time"
)

// Query is the terminal status request (DSR 5), the only sequence the probe
// ever writes. The emulator answers it as ESC [ 0 n on the session's input.
const Query = "\x1b[5n"

// Verdict of a probe.
type Verdict string

const (
	VerdictAlive   Verdict = "alive"
	VerdictDead    Verdict = "dead"
	VerdictUnknown Verdict = "unknown"
)

// Result of a probe: the verdict, the counter rise of each completed trial,
// and why (for the log, never shown to another scope).
type Result struct {
	Verdict Verdict
	Deltas  []uint64
	Why     string
}

// Target is one session to probe.
type Target struct {
	Surface   string
	PID       int
	SessionID string
	TTY       string // "/dev/" + the tty column of ps for PID; re-read by TTYOf before each write
}

// TTYInfo is what TTYOf knows of a claude's terminal.
type TTYInfo struct {
	TTY        string // "/dev/ttysNNN"
	Foreground bool   // claude alone reads its tty (TTYOf's rule)
	Why        string // why Foreground is false
}

// Deps are the probe's seams. A nil one means nothing is written (Unknown).
type Deps struct {
	Counter func(pid int) (uint64, error)    // pti_syscalls_unix
	TTYOf   func(pid int) (TTYInfo, error)   // re-read right before each write
	Write   func(tty string, b []byte) error // WriteTTY
	Sleep   func(time.Duration)
}

// Params of a probe (plan task 9). One trial is two windows on claude's
// counter: a baseline (no write) then, if the baseline was calm, the query's
// window. Counted trials are those whose baseline was calm; of them a trial
// is reactive when the query's window rose by at least Threshold.
type Params struct {
	Window, Gap  time.Duration
	Threshold    uint64
	Counted      int // counted trials the verdict is read on
	MaxAttempts  int // trials tried at most (noisy baselines are not counted)
	AliveAtLeast int // reactive counted trials that prove an alive input
	DeadAtMost   int // reactive counted trials a dead input can show (noise)
}

// DefaultParams: window 80 ms, gap 400 ms, threshold 7 (the minimum of 20
// windows measured after ESC [ 5 n, 2026-10-27), 10 counted trials in at
// most 18 attempts, alive from 8 reactive, dead from 5 silent. The exact
// error rates, from the measured noise, are in the plan (task 9) and held
// by TestDefaultParamsMeetTargets; a change goes through the plan.
func DefaultParams() Params {
	return Params{Window: 80 * time.Millisecond, Gap: 400 * time.Millisecond, Threshold: 7,
		Counted: 10, MaxAttempts: 18, AliveAtLeast: 8, DeadAtMost: 5}
}

func (p Params) coherent() bool {
	return p.Threshold > 0 && p.Counted >= 6 && p.DeadAtMost < p.AliveAtLeast &&
		p.AliveAtLeast <= p.Counted && p.MaxAttempts >= p.Counted
}

// Probe runs up to p.MaxAttempts trials on t. After each trial, in this
// order, with a reactive and f silent counted trials: a >= AliveAtLeast is
// Alive; f >= Counted-DeadAtMost is Dead (decided as soon as the counted
// trials could no longer exceed DeadAtMost reactive ones); a > DeadAtMost
// and f > Counted-AliveAtLeast is Unknown ("between bands"); the last
// attempt reached is Unknown ("not settled"). Any doubt (unreadable tty or
// counter, a counter going backwards, claude no longer alone on its tty, a
// failed write, incoherent parameters) is Unknown, never Dead.
func Probe(d Deps, p Params, t Target) Result {
	if !p.coherent() {
		return Result{Verdict: VerdictUnknown, Why: "incoherent parameters"}
	}
	if d.TTYOf == nil || d.Write == nil || d.Counter == nil {
		return Result{Verdict: VerdictUnknown, Why: "probe seams missing"}
	}
	if t.TTY == "" {
		return Result{Verdict: VerdictUnknown, Why: "no tty"}
	}
	sleep := d.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	var res Result
	a, f := 0, 0
	for n := 1; n <= p.MaxAttempts; n++ {
		if n > 1 {
			sleep(p.Gap)
		}
		delta, counted, why := trial(d, p, t, sleep)
		if why != "" {
			res.Verdict, res.Why = VerdictUnknown, fmt.Sprintf("trial %d: %s", n, why)
			return res
		}
		if counted {
			res.Deltas = append(res.Deltas, delta)
			if delta >= p.Threshold {
				a++
			} else {
				f++
			}
		}
		switch {
		case a >= p.AliveAtLeast:
			res.Verdict, res.Why = VerdictAlive, fmt.Sprintf("%d reactive of %d counted trials", a, a+f)
		case f >= p.Counted-p.DeadAtMost:
			res.Verdict, res.Why = VerdictDead, fmt.Sprintf("%d silent of %d counted trials", f, a+f)
		case a > p.DeadAtMost && f > p.Counted-p.AliveAtLeast:
			res.Verdict, res.Why = VerdictUnknown, fmt.Sprintf("between bands: %d reactive, %d silent", a, f)
		case n == p.MaxAttempts:
			res.Verdict, res.Why = VerdictUnknown, fmt.Sprintf("not settled after %d attempts: %d reactive, %d silent", n, a, f)
		default:
			continue
		}
		return res
	}
	return res
}

// trial runs one attempt. A noisy baseline (or claude's own output pending
// on the tty) writes nothing and is not counted. Else the query goes out and
// the second window, which starts only after TTYOf and the write's own
// reading of the counter, is the counted rise. why is set when the attempt
// proves nothing at all.
func trial(d Deps, p Params, t Target, sleep func(time.Duration)) (delta uint64, counted bool, why string) {
	c0, err := d.Counter(t.PID)
	if err != nil {
		return 0, false, "counter: " + err.Error()
	}
	sleep(p.Window)
	c1, err := d.Counter(t.PID)
	switch {
	case err != nil:
		return 0, false, "counter: " + err.Error()
	case c1 < c0:
		return 0, false, "counter went backwards"
	case c1-c0 >= p.Threshold:
		return 0, false, "" // noisy baseline: nothing written
	}
	info, err := d.TTYOf(t.PID)
	switch {
	case err != nil:
		return 0, false, "tty unreadable: " + err.Error()
	case info.TTY != t.TTY:
		return 0, false, "tty changed"
	case !info.Foreground:
		return 0, false, "claude is not alone on its tty: " + info.Why
	}
	// TTYOf's ps and fd reads take tens of ms: the query's window starts here.
	c1p, err := d.Counter(t.PID)
	if err != nil {
		return 0, false, "counter: " + err.Error()
	}
	if err := d.Write(t.TTY, []byte(Query)); err != nil {
		if errors.Is(err, ErrOutputPending) {
			return 0, false, "" // claude was writing: its baseline was not calm after all
		}
		return 0, false, "write: " + err.Error()
	}
	sleep(p.Window)
	c2, err := d.Counter(t.PID)
	switch {
	case err != nil:
		return 0, false, "counter: " + err.Error()
	case c2 < c1p:
		return 0, false, "counter went backwards"
	}
	return c2 - c1p, true, ""
}

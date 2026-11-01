// Package daemonwatch is the pure state machine of the pigtaild watchdog: one
// run probes the daemon, and a daemon that stays wedged (alive, not
// answering) is kicked, with an intensity cap. Nothing here execs or dials:
// the probe, the kick and the reports are injected.
package daemonwatch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Result is one probe's verdict.
type Result int

const (
	Healthy  Result = iota // answered 200
	Down                   // no process: launchd restarts it
	Starting               // a process that is still coming up
	Wedged                 // alive and not answering
)

const (
	wedgedToKick   = 3
	maxKicksPerHr  = 3
	kickWindow     = time.Hour
	defaultKickTTL = 30 * time.Second
	// maxWedgedGap is the longest gap between two wedged probes that still
	// counts as consecutive (the LaunchAgent runs every 60 s).
	maxWedgedGap = 150 * time.Second
)

// Deps is everything a run touches.
type Deps struct {
	// Probe detail: "timeout", "503 failing=[…]", "refused while pid 123 alive".
	Probe       func(ctx context.Context) (Result, string)
	Kick        func(ctx context.Context) error // ctx bounded by KickTimeout
	KickTimeout time.Duration                   // 0 → 30 s
	// Post journals the events; called only after a Healthy probe.
	Post        func([]Event) error
	Maintenance func() (bool, error)
	// RuntimeClaim: a live runtime claim is held (the caller treats an expired
	// one as not held).
	RuntimeClaim func() (bool, error)
	Load         func() (State, error)
	Save         func(State) error
	Now          func() time.Time
	Logf         func(string, ...any)
}

// Run is one watchdog run. It returns an error only when the state cannot be
// saved; every probe outcome is a nil return (launchd must not throttle it).
func Run(ctx context.Context, d Deps) error {
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	now := d.Now()
	st, err := d.Load()
	if err != nil {
		d.Logf("daemonwatch: state unreadable, reset: %v", err)
		st = State{}
	}
	st.LastRun = now

	if reason := skipReason(d); reason != "" {
		st.LastSkip = reason
		st.Failures = 0 // a skipped run breaks the streak: probes must be consecutive
		d.Logf("daemonwatch: skip: %s", reason)
		return d.Save(st)
	}

	res, detail := d.Probe(ctx)
	switch res {
	case Healthy:
		st.Failures, st.GaveUp, st.LastSkip = 0, nil, ""
		if len(st.Unreported) > 0 {
			if err := d.Post(st.Unreported); err != nil {
				d.Logf("daemonwatch: report kept: %v", err)
			} else {
				st.Unreported = nil
			}
		}
	case Down:
		st.Failures = 0
	case Starting:
		st.Failures = 0
	case Wedged:
		if st.Failures > 0 && now.Sub(st.LastWedged) > maxWedgedGap {
			st.Failures = 0 // the streak stalled (sleep, long gap): start over
		}
		st.Failures++
		st.LastWedged = now
		if st.Failures >= wedgedToKick {
			st = onWedged(ctx, d, st, now, detail)
		}
	}
	return d.Save(st)
}

func skipReason(d Deps) string {
	if d.Maintenance != nil {
		on, err := d.Maintenance()
		if err != nil {
			return fmt.Sprintf("maintenance unreadable: %v", err)
		}
		if on {
			return "maintenance on"
		}
	}
	if d.RuntimeClaim != nil {
		held, err := d.RuntimeClaim()
		if err != nil {
			return fmt.Sprintf("runtime claim unreadable: %v", err)
		}
		if held {
			return "runtime claim held"
		}
	}
	return ""
}

func onWedged(ctx context.Context, d Deps, st State, now time.Time, detail string) State {
	var recent []time.Time
	for _, k := range st.Kicks {
		if now.Sub(k) < kickWindow {
			recent = append(recent, k)
		}
	}
	st.Kicks = recent
	if len(st.Kicks) >= maxKicksPerHr {
		if st.GaveUp == nil {
			t := now
			st.GaveUp = &t
			st.Unreported = append(st.Unreported, Event{Kind: "gave-up", At: now,
				Detail: fmt.Sprintf("%d kicks within 1h, last: %s", len(st.Kicks), detail)})
			d.Logf("daemonwatch: gave up after %d kicks within 1h", len(st.Kicks))
		}
		return st
	}
	ttl := d.KickTimeout
	if ttl <= 0 {
		ttl = defaultKickTTL
	}
	kctx, cancel := context.WithTimeout(ctx, ttl)
	defer cancel()
	err := d.Kick(kctx)
	if err == nil && kctx.Err() != nil {
		err = kctx.Err()
	}
	out := detail
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			out = fmt.Sprintf("%s (failed: kick timed out (may have taken effect))", detail)
		} else {
			out = fmt.Sprintf("%s (failed: %v)", detail, err)
		}
	}
	st.Kicks = append(st.Kicks, now) // whatever its outcome
	st.Unreported = append(st.Unreported, Event{Kind: "kick", At: now, Detail: out})
	st.Failures = 0
	return st
}

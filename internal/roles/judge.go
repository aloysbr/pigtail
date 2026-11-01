package roles

import (
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// State is a role's verdict.
type State string

const (
	StateOK      State = "ok"
	StateSilent  State = "silent"
	StateStuck   State = "stuck"
	StateAbsent  State = "absent"
	StateUnknown State = "unknown"
)

// SessionObs is one live session of the role's subject.
type SessionObs struct {
	SessionID     string
	Native        string // "busy" | "idle" | "waiting"
	StatusSince   time.Time
	ActivityMtime time.Time // claudesession.ActivityMtime (StatusSince when the live session has no transcript yet)
}

// Observation is what the collector saw of a role's subject.
type Observation struct {
	Live []SessionObs
	Loop claudesession.LoopState
	Err  error
}

// Verdict is the judge's answer.
type Verdict struct {
	State    State
	Deadline time.Time // current deadline (always set unless unknown)
	Episode  time.Time // key, alerting states only
	Reason   string
}

// Unknown is the verdict of a read error or a panic: never an alert.
func Unknown(reason string) Verdict { return Verdict{State: StateUnknown, Reason: reason} }

// Alerting reports silent, stuck or absent.
func (v Verdict) Alerting() bool {
	return v.State == StateSilent || v.State == StateStuck || v.State == StateAbsent
}

// Deadline derives the role's deadline from the loop state, and says whether
// the role is wakeup-driven (an arm or a fire seen): a human turn then moves
// nothing.
func Deadline(r pigtail.Role, l claudesession.LoopState) (time.Time, bool) {
	switch {
	case !l.ArmAt.IsZero() && l.LoopFireAt.IsZero():
		return l.ScheduledFor, true
	case !l.FireAt.IsZero():
		return l.FireAt.Add(r.Cadence), true
	case !l.TurnAt.IsZero():
		return l.TurnAt.Add(r.Cadence), false
	}
	return r.CreatedAt.Add(r.Cadence), false
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// hhmm is local time, like `pigtail role status`.
func hhmm(t time.Time) string { return t.Local().Format("15:04") }

// Judge turns an observation into a verdict. Standby is never read: it is not
// an excuse.
func Judge(r pigtail.Role, o Observation, now time.Time) Verdict {
	v := judge(r, o, now)
	// Journal ids carry milliseconds (AlertID): a key finer than that never
	// matches the closed keys read back from them.
	v.Episode = v.Episode.Truncate(time.Millisecond)
	return v
}

func judge(r pigtail.Role, o Observation, now time.Time) Verdict {
	if o.Err != nil {
		return Unknown(o.Err.Error())
	}
	d, driven := Deadline(r, o.Loop)
	v := Verdict{State: StateOK, Deadline: d}
	if len(o.Live) == 0 {
		if now.After(d.Add(r.Grace)) {
			v.State, v.Episode = StateAbsent, maxTime(d, r.CreatedAt)
			v.Reason = fmt.Sprintf("no live session, due %s", hhmm(d))
			return v
		}
		v.Reason = fmt.Sprintf("no session, due %s", hhmm(d))
		return v
	}
	var busy bool
	var p time.Time
	for _, s := range o.Live {
		if s.Native == "busy" {
			busy = true
			p = maxTime(p, s.ActivityMtime)
		}
	}
	if busy {
		if now.Sub(p) > r.Stall {
			v.State, v.Episode = StateStuck, maxTime(p, r.CreatedAt)
			v.Reason = fmt.Sprintf("busy, no write since %s", hhmm(p))
			return v
		}
		v.Reason = "busy"
		return v
	}
	if now.After(d.Add(r.Grace)) {
		v.State, v.Episode = StateSilent, maxTime(d, r.CreatedAt)
		switch {
		case waitingSince(o.Live) != nil:
			v.Reason = fmt.Sprintf("waiting for you since %s", hhmm(*waitingSince(o.Live)))
		case driven:
			v.Reason = fmt.Sprintf("wakeup due %s never fired", hhmm(d))
		default:
			v.Reason = fmt.Sprintf("no turn since %s", hhmm(d.Add(-r.Cadence)))
		}
		return v
	}
	v.Reason = fmt.Sprintf("idle, due %s", hhmm(d))
	return v
}

func waitingSince(live []SessionObs) *time.Time {
	for _, s := range live {
		if s.Native == "waiting" {
			t := s.StatusSince
			return &t
		}
	}
	return nil
}

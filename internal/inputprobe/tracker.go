package inputprobe

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// ConfirmAfter: a session whose first Dead awaits its confirmation is due
// again this soon (not at the rotation): Record dates a verdict from the
// start of a tick and two ticks can fall less than 60 s apart.
const ConfirmAfter = 50 * time.Second

// ProofWindow: a dead state is shown only if a session answered the probe
// (an Alive verdict, at or after the first Dead of the dead one) within this
// window: an emulator that stopped answering DSR would otherwise make every
// idle tab look dead.
const ProofWindow = 30 * time.Minute

// dueSlack: the rotation compares with every - dueSlack, so a 5 min rotation
// is not pushed to 6 by ticks that fall a few seconds short.
const dueSlack = 10 * time.Second

// Tracker keeps, per session, when it was last probed and whether its input
// is dead — the daemon's loop state (memory only: a restart forgets it). A
// first Dead puts the session on hold (no signal, due again after
// ConfirmAfter); a second Dead in a row, Unknown changing nothing, confirms
// it dead; an Alive cancels the hold. A confirmed state is cleared only by
// two Alive verdicts in a row (a Dead in between starts the count again).
// DeadSurfaces shows a confirmed state only while some session has answered
// (the positive control of the instrument).
type Tracker struct {
	every time.Duration
	mu    sync.Mutex
	state map[string]*trackState

	lastAlive time.Time // latest Alive of any session
}

type trackState struct {
	last      time.Time
	surface   string
	pending   bool // a first Dead awaits its confirmation
	dead      bool // confirmed by two Dead in a row
	firstDead time.Time
	alives    int
}

// NewTracker: each session is due once every `every`.
func NewTracker(every time.Duration) *Tracker {
	return &Tracker{every: every, state: map[string]*trackState{}}
}

// due: caller holds mu.
func (tr *Tracker) due(st *trackState, ok bool, now time.Time) bool {
	if !ok || st.last.IsZero() {
		return true
	}
	if st.pending {
		return now.Sub(st.last) >= ConfirmAfter
	}
	return now.Sub(st.last) >= tr.every-dueSlack
}

// Due: sid was never probed, or its last probe is old enough (the rotation
// less a slack; ConfirmAfter for a session whose first Dead awaits its
// confirmation).
func (tr *Tracker) Due(sid string, now time.Time) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	st, ok := tr.state[sid]
	return tr.due(st, ok, now)
}

// confirming: sid's first Dead awaits its confirmation.
func (tr *Tracker) confirming(sid string) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	st, ok := tr.state[sid]
	return ok && st.pending
}

// lastProbe: the zero time for a session never probed (the most overdue).
func (tr *Tracker) lastProbe(sid string) time.Time {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if st, ok := tr.state[sid]; ok {
		return st.last
	}
	return time.Time{}
}

// Record files a probe of t at now.
func (tr *Tracker) Record(t Target, r Result, now time.Time) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	st, ok := tr.state[t.SessionID]
	if !ok {
		st = &trackState{}
		tr.state[t.SessionID] = st
	}
	st.last, st.surface = now, t.Surface
	switch r.Verdict {
	case VerdictDead:
		st.alives = 0
		switch {
		case st.dead:
		case st.pending:
			st.pending, st.dead = false, true
		default:
			st.pending, st.firstDead = true, now
		}
	case VerdictAlive:
		if now.After(tr.lastAlive) {
			tr.lastAlive = now
		}
		st.pending = false
		if st.dead {
			if st.alives++; st.alives >= 2 {
				st.dead, st.alives = false, 0
			}
		}
	}
}

// Forget drops every session absent from live (all live claude sessions,
// not only the probe's targets: a session busy for a moment keeps its state).
func (tr *Tracker) Forget(live map[string]bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for sid := range tr.state {
		if !live[sid] {
			delete(tr.state, sid)
		}
	}
}

// Dead: is sid's input confirmed dead (two Dead in a row), proven or not?
func (tr *Tracker) Dead(sid string) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	st, ok := tr.state[sid]
	return ok && st.dead
}

// proven: a session answered at or after st's first Dead, within ProofWindow
// of now (the caller's clock, never the latest record: with nothing recorded
// the window must still run out). Caller holds mu.
func (tr *Tracker) proven(st *trackState, now time.Time) bool {
	return !tr.lastAlive.IsZero() && !tr.lastAlive.Before(st.firstDead) && now.Sub(tr.lastAlive) <= ProofWindow
}

// DeadSurfaces lists the surfaces (upper case, sorted) whose input is
// confirmed dead and proven by an answer (the positive control).
func (tr *Tracker) DeadSurfaces(now time.Time) []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []string
	seen := map[string]bool{}
	for _, st := range tr.state {
		s := strings.ToUpper(st.surface)
		if st.dead && s != "" && !seen[s] && tr.proven(st, now) {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// DeadHeld: a dead state is confirmed but not shown, for want of a recent
// answer from any session.
func (tr *Tracker) DeadHeld(now time.Time) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, st := range tr.state {
		if st.dead && !tr.proven(st, now) {
			return true
		}
	}
	return false
}

// Pick returns the targets Pass would probe: at most max of those due, first
// the sessions whose first Dead awaits its confirmation, then the most
// overdue (never probed = most overdue; ties by surface).
func (tr *Tracker) Pick(targets []Target, now time.Time, max int) []Target {
	var due []Target
	for _, t := range targets {
		if t.SessionID != "" && tr.Due(t.SessionID, now) {
			due = append(due, t)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		if ci, cj := tr.confirming(due[i].SessionID), tr.confirming(due[j].SessionID); ci != cj {
			return ci
		}
		a, b := tr.lastProbe(due[i].SessionID), tr.lastProbe(due[j].SessionID)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return due[i].Surface < due[j].Surface
	})
	if max >= 0 && len(due) > max {
		due = due[:max]
	}
	return due
}

// Pass probes, one after the other, at most max of the targets that are
// due (Pick), and records each verdict. It returns the transitions: sessions
// whose confirmed dead state changed.
func Pass(tr *Tracker, targets []Target, probe func(Target) Result, now time.Time, max int) []Transition {
	due := tr.Pick(targets, now, max)
	var out []Transition
	for _, t := range due {
		was := tr.Dead(t.SessionID)
		tr.Record(t, probe(t), now)
		if is := tr.Dead(t.SessionID); is != was {
			out = append(out, Transition{Target: t, Dead: is})
		}
	}
	return out
}

// Transition: a session's input became confirmed dead (Dead) or alive again.
type Transition struct {
	Target Target
	Dead   bool
}

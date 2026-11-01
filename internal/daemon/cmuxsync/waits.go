package cmuxsync

import (
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// WaitGoneConfirm is how long a session must read gone (not alive, no
// SessionEnd), across two observations, before its waits are dropped: one
// unreadable native file must not lose a decision that waits for hours.
const WaitGoneConfirm = 2 * time.Second

// WaitTracker decides when a recorded wait is over (spec §4.8, effacement),
// from the native status the loop polls. The hooks clear a wait sooner
// (UserPromptSubmit, SessionEnd); this is what holds when a hook is missing,
// lost, or the cell is not equipped:
//
//   - automatic: over once the native status leaves "waiting" at a time
//     after the hook recorded it — the dialog was answered. A hook fired
//     just before its dialog opened (status still busy since earlier) does
//     not end it;
//   - declared: over at the next turn. The status movement proposes it — the
//     session handed back (idle) and its status moved again, both after the
//     declaration — and the transcript confirms it: a turn opens with a
//     turn-opening entry (turnRan). A busy flicker with no such entry (a
//     background notification that runs no turn) is not a turn. Without a
//     verdict (transcript unreadable, not covered, no read this pass) the
//     status alone decides: idle, then busy again. A dialog inside the
//     declaring turn (busy → waiting → busy) is not a next turn. When the
//     loop never saw the declaring turn (daemon restarted), a busy newer
//     than the declaration is a candidate too;
//   - a failure told by StopFailure (Limit, Login, API error; A2): over at
//     its own end (a limit's reset), or, but for a login (the next prompt
//     only, D3), when the session runs a turn begun after it (busy, native
//     status newer than the wait). Never on idle: the failure is the idle;
//   - held (a process keeps it: the Mac-hold helper, D4): never over by a turn;
//     over at its Until, or as soon as its holder pid is dead — judged
//     before anything else, so a session in failure or with no native
//     reading still loses it (F13);
//   - all: over when the session is gone, confirmed WaitGoneConfirm apart.
//
// Not safe for concurrent use; the runner owns one.
type WaitTracker struct {
	s map[string]*waitTrack
	// pidAlive tells whether a held wait's holder runs; nil =
	// claudesession.PidAlive.
	pidAlive func(int) bool
	// turnRan tells whether the transcript of s holds a turn-opening entry
	// stamped after its declared wait: known false = a flicker, no turn; nil
	// or known false = no verdict, the status decides.
	turnRan func(s Session) (ran, known bool)
}

type waitTrack struct {
	declSince time.Time // the declaration the flags below are about
	sawBefore bool      // seen in the declaring turn (status older than it)
	sawIdle   bool      // seen handed back since the declaration
	idleAt    time.Time // native status time of that first idle: the trigger only
	sawBusy   bool      // seen busy, with a status time after idleAt
	goneSince time.Time
	seen      bool // touched since the last Sweep
}

// Observe judges s's waits at now and returns the sources to clear, auto
// before declared before held, or nil.
func (wt *WaitTracker) Observe(now time.Time, s Session) []string {
	if !s.Auto.Set() && !s.Declared.Set() && !s.Held.Set() {
		return nil
	}
	if wt.s == nil {
		wt.s = map[string]*waitTrack{}
	}
	tr := wt.s[s.SessionID]
	if tr == nil {
		tr = &waitTrack{}
		wt.s[s.SessionID] = tr
	}
	tr.seen = true
	heldOver := s.Held.Set() && wt.heldOver(now, s.Held)

	if s.Ended {
		return sources(s.Auto.Set(), s.Declared.Set(), s.Held.Set())
	}
	if !s.Alive {
		if tr.goneSince.IsZero() {
			tr.goneSince = now
		}
		if now.Sub(tr.goneSince) >= WaitGoneConfirm {
			return sources(s.Auto.Set(), s.Declared.Set(), s.Held.Set())
		}
		return sources(false, false, heldOver)
	}
	tr.goneSince = time.Time{}
	if s.Auto.Set() && s.Auto.Type.Failure() {
		// A2: a failure ends at its own end (a limit's reset), or — but a
		// login (D3: the next prompt only) — when the session runs a turn
		// begun after it (auto resume, a prompt the hook missed).
		over := waittype.FailureReset(now, s.Auto.Until)
		if !over && s.Auto.Type != waittype.Login && !s.NativeStatusAt.IsZero() {
			over = s.Status == "busy" && s.NativeStatusAt.After(s.Auto.Since)
		}
		return sources(over, s.Declared.Set() && wt.declaredOver(tr, s), heldOver)
	}
	if s.NativeStatusAt.IsZero() {
		return sources(false, false, heldOver) // no native reading: only the held wait is judged
	}

	clearAuto := s.Auto.Set() && s.Status != "waiting" && s.NativeStatusAt.After(s.Auto.Since)
	return sources(clearAuto, s.Declared.Set() && wt.declaredOver(tr, s), heldOver)
}

// heldOver tells whether a held wait is over: its Until passed, or its
// holder is dead (D4). A held wait with no holder pid is judged on Until
// only. The rule is waittype.HeldOver, shared with the idle wake (D38).
func (wt *WaitTracker) heldOver(now time.Time, w Wait) bool {
	alive := wt.pidAlive
	if alive == nil {
		alive = claudesession.PidAlive
	}
	return waittype.HeldOver(now, w.Until, w.HolderPID, alive)
}

// declaredOver tells whether the declared wait of s is over: the next turn.
// The status proposes it (idle after the declaration, then its status moved
// again; or, the loop having missed the declaring turn, a busy newer than the
// declaration), the transcript confirms it (turnRan); no verdict falls back
// to the status alone (idle, then busy seen). No native reading: not over.
func (wt *WaitTracker) declaredOver(tr *waitTrack, s Session) bool {
	if s.NativeStatusAt.IsZero() {
		return false
	}
	if !tr.declSince.Equal(s.Declared.Since) {
		*tr = waitTrack{declSince: s.Declared.Since, goneSince: tr.goneSince, seen: tr.seen}
	}
	if !s.NativeStatusAt.After(s.Declared.Since) {
		tr.sawBefore = true
		return false
	}
	candidate := false
	switch {
	case s.Status == "idle" && !tr.sawIdle:
		tr.sawIdle, tr.idleAt = true, s.NativeStatusAt
	case tr.sawIdle && (s.NativeStatusAt.After(tr.idleAt) || s.Status == "busy"):
		candidate = true // any status: a short real turn may be idle again already
		if s.Status == "busy" {
			tr.sawBusy = true
		}
	}
	restart := !tr.sawBefore && s.Status == "busy"
	if restart {
		candidate = true
	}
	if !candidate {
		return false
	}
	if wt.turnRan != nil {
		if ran, known := wt.turnRan(s); known {
			if !ran && restart && !tr.sawIdle {
				// The restart's busy is not confirmed yet (a pills-only pass, or
				// the declaring turn itself): count it as the handing back, so
				// the next status movement is a candidate in its own right.
				tr.sawIdle, tr.idleAt = true, s.Declared.Since
			}
			return ran
		}
	}
	return tr.sawBusy || restart
}

// Sweep forgets the sessions not observed since the last sweep, and those
// absent from keep (nil keep = only the unobserved).
func (wt *WaitTracker) Sweep(keep map[string]bool) {
	for id, tr := range wt.s {
		if !tr.seen || (keep != nil && !keep[id]) {
			delete(wt.s, id)
			continue
		}
		tr.seen = false
	}
}

func sources(auto, declared, held bool) []string {
	var out []string
	if auto {
		out = append(out, pigtail.WaitSourceAuto)
	}
	if declared {
		out = append(out, pigtail.WaitSourceDeclared)
	}
	if held {
		out = append(out, pigtail.WaitSourceHeld)
	}
	return out
}

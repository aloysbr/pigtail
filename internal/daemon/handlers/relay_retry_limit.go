package handlers

// Resume at a usage limit's reset (sub_x000000000000013, D6): a session whose
// automatic limit wait is still standing LimitResumeGrace after its Until —
// no account took it, the rotation was refused or impossible, Claude Code did
// not resume by itself — gets "continue" typed once per pid and per reset,
// under the wake's guards with the post-reset screen rule (the stop lines are
// tolerated, the auto-resume markers still refuse). The markers are journal
// rows limit-resume-sent|native|held, id limit-resume:<s>:<pid>:<untilUnix>:….
// Any marker lifts the idle pause's hold (relay.Candidate.LimitResumePending).

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	limitKindSent   = "limit-resume-sent"
	limitKindNative = "limit-resume-native"
	limitKindHeld   = "limit-resume-held"
)

func limitIDPrefix(s string, pid int, until time.Time) string {
	return "limit-resume:" + s + ":" + strconv.Itoa(pid) + ":" + strconv.FormatInt(until.Unix(), 10) + ":"
}

func limitHeldID(s string, pid int, until time.Time, why string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(why))
	return fmt.Sprintf("%sheld:%x", limitIDPrefix(s, pid, until), h.Sum32())
}

// limitResumePending is true while the reset's resume owes the session a
// decision: a limit wait with its end, the session not in Standby, and no
// limit-resume marker (sent, native or held) for this pid and this reset. A
// journal read error holds too (fail closed; the hold is bounded).
func (rl *Relay) limitResumePending(p pigtail.PeerSession, c relay.Candidate) bool {
	if c.Standby {
		return false
	}
	w, ok := autoWait(c.Waits, "limit")
	if !ok || w.Until == nil {
		return false
	}
	rows, err := rl.listJournalRows(store.JournalFilter{Scope: sessionScope(p), IDPrefix: limitIDPrefix(p.SessionID, p.PID, *w.Until), Limit: 1})
	return err != nil || len(rows) == 0
}

// limitHeld journals a refusal of the resume, once per reason and reset.
func (rl *Relay) limitHeld(p pigtail.PeerSession, until time.Time, why string, now time.Time) {
	rl.retryNote(p.SessionID, "limit resume: "+why)
	summary := fmt.Sprintf("%s: limit · resume at reset · held: %s", wakeShort(p.SessionID), why)
	if _, err := rl.retryJournal(p, limitKindHeld, limitHeldID(p.SessionID, p.PID, until, why), summary, now); err != nil {
		rl.retryNote(p.SessionID, "limit resume: held marker not written: "+err.Error())
	}
}

// resumeLimit handles one session whose limit wait outlived its reset by the
// grace. Every exit that types nothing leaves a limit-resume-held row, except
// a resume already done (sent, native), a session typed into this sweep, an
// unreadable journal, and the two global holds (daemon start-up grace, a cmux
// check typing), which are logged only. A per-session cmux read-screen error
// does leave a held row.
// False stops the sweep.
func (rl *Relay) resumeLimit(p pigtail.PeerSession, w pigtail.SessionWait, now time.Time, sw *wakeSweep) bool {
	s := p.SessionID
	until := *w.Until
	prefix := limitIDPrefix(s, p.PID, until)
	done, err := rl.listJournalRows(store.JournalFilter{Scope: sessionScope(p),
		IDs: []string{prefix + "sent", prefix + "native"}, Limit: 1})
	if err != nil {
		rl.retryNote(s, "limit resume: journal unreadable: "+err.Error())
		return true
	}
	if len(done) > 0 {
		return true // once per pid and per reset
	}
	if sw.typed[s] {
		rl.retryNote(s, "limit resume: typed into during this sweep")
		return true
	}
	c, err := rl.candidate(p)
	if err != nil {
		rl.limitHeld(p, until, "waits unreadable", now)
		return true
	}
	switch {
	case c.SurfaceID == "":
		rl.limitHeld(p, until, "no cmux surface", now)
		return true
	case !c.NativeOK:
		rl.limitHeld(p, until, "native state unreadable", now)
		return true
	case !c.Alive:
		rl.limitHeld(p, until, "pid not alive", now)
		return true
	}
	// Claude Code resumed by itself: the native status moved since the reset
	// (it is in milliseconds), or a turn succeeded since the stop.
	succeeded, err := rl.svc.Store.TurnSucceededSince(s, w.Since)
	if err != nil {
		rl.limitHeld(p, until, "turns unreadable", now)
		return true
	}
	if c.Native.StatusUpdatedAt >= until.UnixMilli() || succeeded {
		summary := fmt.Sprintf("%s: limit · resume at reset · native resume", wakeShort(s))
		if _, err := rl.retryJournal(p, limitKindNative, prefix+"native", summary, now); err != nil {
			rl.retryNote(s, "limit resume: native marker not written: "+err.Error())
		}
		return true
	}
	if ok, why := relay.RetryAllowed(c); !ok {
		rl.limitHeld(p, until, why, now)
		return true
	}
	e, ok := rl.cfg.Wake.LastAPIError(p.ConfigDir, s)
	if !ok {
		rl.limitHeld(p, until, "transcript unreadable", now)
		return true
	}
	if e.Error != "rate_limit" || e.ResetsAt.Unix() != until.Unix() {
		rl.limitHeld(p, until, "transcript no longer says this limit", now)
		return true
	}

	wk := rl.cfg.Wake
	marked := false
	noter := &retryNoter{rl: rl, screen: relay.ScreenAllowsAfterLimitReset, onHeld: func(why string) { rl.limitHeld(p, until, why, now) }}
	beforeSend := func() error {
		sw.focusDone = true
		sw.focused, sw.focusErr = wk.Focused()
		if sw.focusErr != nil {
			rl.svc.logf("retry: %s: cmux identify: %v", wakeShort(s), sw.focusErr)
			return errTypeStop
		}
		if strings.EqualFold(sw.focused, c.SurfaceID) {
			return fmt.Errorf("pane has keyboard focus")
		}
		n, ok := wk.ReadNative(p.ConfigDir, p.PID)
		if !ok || n.SessionID != s || n.Status != "idle" || n.StatusUpdatedAt != c.Native.StatusUpdatedAt {
			return fmt.Errorf("left idle during the checks")
		}
		summary := fmt.Sprintf("%s: limit · resume at reset · typed", wakeShort(s))
		created, err := rl.retryJournal(p, limitKindSent, prefix+"sent", summary, now)
		if err != nil {
			return fmt.Errorf("journal write failed: %w", err)
		}
		if !created {
			return fmt.Errorf("marker already written")
		}
		marked = true
		return nil
	}
	_, out := rl.typeGuarded(s, relay.ContinueLine, c, p, now, sw, noter, nil, nil, nil, beforeSend)
	switch out {
	case typeStop:
		return false
	case typeSent:
		rl.retryClearNote(s)
		rl.svc.logf("retry: typed %s in %s for %s (limit reset)", relay.ContinueLine, c.SurfaceID, wakeShort(s))
	default:
		if marked {
			// The send failed after the marker: never retyped for this reset.
			rl.limitHeld(p, until, retrySendFailed, now)
		}
	}
	return true
}

package handlers

// Standby compaction (sub_x000000000000041, Tâche 4): a session cannot start
// its own compaction, so pigtaild types /compact in the pane of a session in
// Standby — once, under the wake's guards (typeGuarded), in the same sweep and
// with the same cmux calls (relay_wake.go). Success is not the send: the
// session must leave idle on the pid it was sent to (its native status time
// moves, P17: a /compact moves it). Not proven after compactProofWait →
// resent, up to maxCompactTries; then, or when our /compact is still in the
// input, the compaction is given up and reported. The Standby stays either
// way: parking a session does not depend on its compaction.

import (
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// compactProofWait: how long a sent /compact may stay unproven before a
	// new send (or the failure).
	compactProofWait = 2 * time.Minute
	// maxCompactTries: sends of one Standby's /compact, at most.
	maxCompactTries = 3
)

// sweepIdle is one relay sweep (Run): the idle wake, then the compaction of
// the sessions in Standby. A running cmux check holds both (T-f): /compact
// is typed in a pane like the wake line. The auto-retry (relay_retry.go) types
// "continue" in a session stopped on an API error, between the compaction and
// the wrap. The idle pause (relay_pause.go) comes
// last, from the same listing. The cmux listing and the focus are
// read once for both (wakeSweep).
func (rl *Relay) sweepIdle() {
	if rl.cfg.Wake == nil {
		return
	}
	if held := rl.cfg.Wake.CheckRunning != nil && rl.cfg.Wake.CheckRunning(); rl.noteCheckHeld(held) {
		return
	}
	now := rl.cfg.Now()
	sw := &wakeSweep{ls: newListing(rl.cfg.Wake.CmuxSessions), typed: map[string]bool{}}
	if rl.wakeIdle(now, sw) && rl.compactStandby(now, sw) && rl.retryStopped(now, sw) && rl.wrapIdle(now, sw) {
		rl.pauseIdle(now, sw)
	}
}

// compactStandby gives each Standby still to compact its turn; a Standby
// compacted or given up is never typed in again (D24: a resume included).
// False when a cmux answer every session needs failed (typeStop): the idle
// close, which needs the same answers, waits for the next sweep.
func (rl *Relay) compactStandby(now time.Time, sw *wakeSweep) bool {
	rows, err := rl.svc.Store.ListStandby()
	if err != nil {
		rl.svc.logf("wake: standby rows unreadable: %v", err)
		return true
	}
	ids := make([]string, 0, len(rows))
	for _, sb := range rows {
		ids = append(ids, sb.SessionID)
	}
	defer rl.forgetCompacts(ids)
	for _, sb := range rows {
		if sb.CompactedAt != nil || sb.CompactFailedAt != nil {
			continue
		}
		if !rl.compactOne(sb, now, sw) {
			return false // a cmux answer every session needs failed: next sweep
		}
	}
	return true
}

// compactOne: the proof of an earlier send first, then a guarded send. False
// stops the sweep.
func (rl *Relay) compactOne(sb pigtail.SessionStandby, now time.Time, sw *wakeSweep) bool {
	s := sb.SessionID
	p, err := rl.svc.Store.GetPeer(s)
	if err != nil || p == nil || p.EndedAt != nil {
		rl.compactNote(s, "unknown or ended") // a resume finds its row (D24)
		return true
	}
	c, err := rl.candidate(*p)
	if err != nil {
		rl.compactNote(s, "waits unreadable: "+err.Error())
		return true
	}
	sent := sb.CompactSentAt != nil
	samePID := sent && p.PID == sb.CompactPID
	proofDue := samePID && now.Sub(*sb.CompactSentAt) >= compactProofWait
	if samePID && c.NativeOK && c.Native.StatusUpdatedAt > sb.CompactStatusAt {
		if err := rl.svc.Store.MarkStandbyCompacted(s, now); err != nil {
			rl.svc.logf("wake: %s standby: compacted, not recorded: %v", wakeShort(s), err)
			return true
		}
		rl.svc.logf("wake: %s standby: compacted", wakeShort(s))
		return true
	}
	if samePID && !proofDue {
		return true // the session has compactProofWait to leave idle
	}
	// Not proven: on the same pid after compactProofWait, or the process was
	// relaunched (D24: the send was lost with it, never a proof).
	if sent && sb.CompactTries >= maxCompactTries {
		why := "the session never left idle"
		if !samePID {
			why = "relaunched"
		}
		rl.compactFailed(*p, sb.CompactTries, why, now)
		return true
	}
	if ok, why := relay.CompactAllowed(c); !ok {
		rl.compactNote(s, why)
		return true
	}
	left := false
	var onScreen func(string) bool
	if proofDue {
		// Our /compact still in the input (the Enter swallowed): given up at
		// once, never erased (D9).
		onScreen = func(screen string) bool {
			draft, found := claudesession.InputDraft(screen)
			left = found && strings.TrimSpace(draft) == relay.CompactLine
			return !left
		}
	}
	n, out := rl.typeGuarded(s, relay.CompactLine, c, *p, now, sw, compactNoter{rl}, onScreen, nil, nil, nil) // no admission: /compact only shrinks a Standby session
	if left {
		rl.compactFailed(*p, sb.CompactTries, relay.CompactLine+" left in the input", now)
		return true
	}
	switch out {
	case typeStop:
		return false
	case typeRefused:
		return true
	}
	if err := rl.svc.Store.MarkStandbyCompactSent(s, now, n.StatusUpdatedAt, p.PID); err != nil {
		rl.svc.logf("wake: %s standby: /compact typed, not recorded: %v", wakeShort(s), err)
	}
	rl.wakeMu.Lock()
	delete(rl.compactReason, s)
	rl.wakeMu.Unlock()
	rl.svc.logf("wake: typed %s in %s for %s (standby, send %d)", relay.CompactLine, c.SurfaceID, wakeShort(s), sb.CompactTries+1)
	return true
}

// compactFailed gives the compaction up: recorded, one line, one event with
// the session's scope. No more send for this Standby.
func (rl *Relay) compactFailed(p pigtail.PeerSession, tries int, why string, now time.Time) {
	s := p.SessionID
	if err := rl.svc.Store.MarkStandbyCompactFailed(s, now); err != nil {
		rl.svc.logf("wake: %s standby: failure not recorded: %v", wakeShort(s), err)
	}
	rl.svc.logf("wake: %s standby: %s not proven after %d sends (%s)", wakeShort(s), relay.CompactLine, tries, why)
	if rl.svc.Bus == nil {
		return
	}
	rl.svc.Bus.Publish(pigtail.Event{Type: pigtail.EventSessionStandbyCompactFailed, Scope: string(sessionScope(p)), Data: map[string]interface{}{
		"session_id": s, "cmux_workspace_id": p.CmuxWorkspaceID, "tries": tries, "reason": why,
	}})
}

// compactNote logs a compaction refusal once per change of reason (écart 3):
// its own memory, never the wake's lastReason.
func (rl *Relay) compactNote(s, reason string) {
	rl.compactOnce(s, "held: "+reason, fmt.Sprintf("wake: %s standby: held: %s", wakeShort(s), reason))
}

func (rl *Relay) compactOnce(s, key, line string) {
	rl.wakeMu.Lock()
	same := rl.compactReason[s] == key
	rl.compactReason[s] = key
	rl.wakeMu.Unlock()
	if !same {
		rl.svc.logf("%s", line)
	}
}

// forgetCompacts drops the refusal memory of sessions no longer in Standby.
func (rl *Relay) forgetCompacts(ids []string) {
	keep := make(map[string]bool, len(ids))
	for _, s := range ids {
		keep[s] = true
	}
	rl.wakeMu.Lock()
	defer rl.wakeMu.Unlock()
	for s := range rl.compactReason {
		if !keep[s] {
			delete(rl.compactReason, s)
		}
	}
}

type compactNoter struct{ rl *Relay }

func (n compactNoter) held(s, reason string) { n.rl.compactNote(s, reason) }

func (n compactNoter) readScreen(s string, err error) {
	n.rl.compactOnce(s, "read-screen: "+err.Error(), fmt.Sprintf("wake: %s standby: cmux read-screen: %v", wakeShort(s), err))
}

// watcherHolds: the D26 re-read for the /compact is the Standby's, not the
// watcher's — while the session is in Standby its watcher claims nothing
// (WaitMessages, inStandby), so it never holds the compaction (relay.
// CompactAllowed); the Standby ended (the user's prompt) or unreadable during the
// cmux calls → nothing typed.
func (n compactNoter) watcherHolds(s string) (bool, string) {
	in, why := n.rl.inStandby(s)
	switch {
	case !in:
		return true, "standby ended"
	case why != standbyReason:
		return true, why
	}
	return false, ""
}

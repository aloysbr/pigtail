package handlers

// Idle wrap: a session whose declared wait is done and which has been idle
// past -idle-session-pause-seconds gets the user's done command
// (RelayConfig.DoneCommand, settings.yaml session.done_command) as a bare
// line, once per pid, instead of the idle pause (SIGTERM + Standby). With no
// done command set the idle wrap is off and types nothing. This file holds the
// durable marker (a journal line per session and pid, modelled on the pause's
// give-up); the sweep phase follows in the same file.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// wrapProofWait: how long the done command may leave the session open, with no question
// asked, before the stay-open failure is journalled (followWraps).
const wrapProofWait = 10 * time.Minute

// wrapJournalKindPrefix prefixes the journal kinds idle-wrap-sent and
// idle-wrap-failed.
const wrapJournalKindPrefix = "idle-wrap-"

// wrapJournalID is the deterministic id of one outcome ("sent"|"failed") for
// this session and pid: a second write creates nothing.
func wrapJournalID(session string, pid int, state string) string {
	return "idle-wrap:" + session + ":" + strconv.Itoa(pid) + ":" + state
}

func (rl *Relay) listJournalRows(f store.JournalFilter) ([]pigtail.JournalEntry, error) {
	if rl.listJournal != nil {
		return rl.listJournal(f)
	}
	return rl.svc.Store.ListJournal(f)
}

func (rl *Relay) addJournalRow(e *pigtail.JournalEntry) (bool, error) {
	if rl.addJournal != nil {
		return rl.addJournal(e)
	}
	return rl.svc.Store.AddJournal(e)
}

// wrapJournal writes the marker of an outcome for this session and pid.
// created is false when the row already exists (first writer wins).
func (rl *Relay) wrapJournal(p pigtail.PeerSession, state, summary string, now time.Time) (bool, error) {
	e := pigtail.JournalEntry{ID: wrapJournalID(p.SessionID, p.PID, state), Timestamp: now, Kind: wrapJournalKindPrefix + state,
		Scope: string(sessionScope(p)), SubjectID: p.SubjectID, Summary: summary, Source: "pigtail:session/" + p.SessionID}
	if err := e.Validate(); err != nil {
		return false, err
	}
	return rl.addJournalRow(&e)
}

// wrapMarked reads the markers of this session and pid: sent (with its time)
// and failed. A read error is the caller's to hold on.
func (rl *Relay) wrapMarked(p pigtail.PeerSession) (sent, failed bool, sentAt time.Time, err error) {
	for _, state := range []string{"sent", "failed"} {
		rows, e := rl.listJournalRows(store.JournalFilter{
			Scope: sessionScope(p), Kind: wrapJournalKindPrefix + state, IDs: []string{wrapJournalID(p.SessionID, p.PID, state)}, Limit: 1,
		})
		if e != nil {
			return false, false, time.Time{}, e
		}
		if len(rows) == 0 {
			continue
		}
		if state == "sent" {
			sent, sentAt = true, rows[0].Timestamp
		} else {
			failed = true
		}
	}
	return sent, failed, sentAt, nil
}

// wrapIdle is the idle wrap's part of a sweep: the oldest idle Done session
// still unmarked gets the done command, under the wake's guards
// (typeGuarded) — one per sweep. False when a cmux answer every session needs
// failed (typeStop): the pause waits for the next sweep.
func (rl *Relay) wrapIdle(now time.Time, sw *wakeSweep) bool {
	threshold := rl.cfg.IdlePause
	if threshold <= 0 || rl.cfg.DoneCommand == "" {
		return true // no done command: nothing to type
	}
	// The seams the wrap reads and types through; the pause reports a missing
	// one (pauseUnwired), the wrap just stays off.
	if w := rl.cfg.Wake; w == nil || w.ReadNative == nil || w.SessionAlive == nil || w.Focused == nil || w.SendLine == nil || w.CmuxSessions == nil || w.ReadScreen == nil {
		return true
	}
	rl.followWraps(now)
	ps, err := rl.svc.Store.ListPeers(false)
	if err != nil {
		rl.svc.logf("wrap: peers unreadable: %v", err)
		return true
	}
	type cand struct {
		p pigtail.PeerSession
		c relay.Candidate
	}
	var cands []cand
	for _, p := range ps {
		s := p.SessionID
		c, err := rl.candidate(p)
		if err != nil {
			rl.wrapNote(s, "waits unreadable: "+err.Error())
			continue
		}
		if ok, why := relay.WrapAllowed(c, threshold); !ok {
			// Only a Done session idle past the threshold is worth a line.
			if pauseIdleLong(c, now, threshold) && relay.HasDeclaredWait(c.Waits, "done") {
				rl.wrapNote(s, why)
			} else {
				rl.wrapClearNote(s)
			}
			continue
		}
		sent, failed, _, err := rl.wrapMarked(p)
		if err != nil {
			rl.wrapNote(s, "journal unreadable: "+err.Error())
			continue
		}
		if sent || failed {
			continue // once per pid, whatever came of it
		}
		cands = append(cands, cand{p, c})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].c.Native.StatusUpdatedAt < cands[j].c.Native.StatusUpdatedAt
	})
	for _, k := range cands {
		switch rl.wrapOne(k.p, k.c, now, sw) {
		case wrapStop:
			return false
		case wrapActed:
			return true // one wrap per sweep
		}
	}
	return true
}

// followWraps journals a done command that left its session open: a sent marker, no
// failed marker, the same pid alive, no question asked (no question wait, no
// native waiting), wrapProofWait elapsed. The pid stays out of the pause (the
// marker holds it) and is never wrapped again. It types and signals nothing.
func (rl *Relay) followWraps(now time.Time) {
	ps, err := rl.svc.Store.ListPeers(false)
	if err != nil {
		return // wrapIdle reports the unreadable list
	}
	for _, p := range ps {
		sent, failed, at, err := rl.wrapMarked(p)
		if err != nil || !sent || failed || now.Sub(at) < wrapProofWait {
			continue
		}
		if p.PID <= 0 || !rl.sessionAlive(p.ConfigDir, p.PID, p.SessionID) {
			continue // gone: the wrap closed it
		}
		c, err := rl.candidate(p)
		if err != nil || (c.NativeOK && c.Native.Status == "waiting") {
			continue
		}
		asked := false
		for _, w := range c.Waits {
			asked = asked || w.Type == "question"
		}
		if asked {
			continue // stopped on a question: the tab stays, no failure
		}
		if relay.HasDeclaredWait(c.Waits, "wrapped") {
			continue // the closing ended on purpose and left the tab open: a normal end
		}
		if created, err := rl.wrapJournal(p, "failed", "stayed open after "+rl.cfg.DoneCommand, now); err != nil {
			rl.wrapOnce(p.SessionID, "unrecorded: "+err.Error(), fmt.Sprintf("wrap: %s: failure not recorded: %v", wakeShort(p.SessionID), err))
		} else if created {
			rl.svc.logf("wrap: %s stayed open after %s (no question): not wrapped again, not paused", wakeShort(p.SessionID), rl.cfg.DoneCommand)
		}
	}
}

type wrapResult int

const (
	wrapNext  wrapResult = iota // held: the next candidate
	wrapActed                   // done command typed, or marked and failed: this sweep's wrap
	wrapStop                    // a cmux answer every session needs failed
)

// wrapOne types the done command into one candidate: the marker is written after every
// guard has passed and right before the send (beforeSend), never before.
func (rl *Relay) wrapOne(p pigtail.PeerSession, c relay.Candidate, now time.Time, sw *wakeSweep) wrapResult {
	s := p.SessionID
	if sw.typed[s] {
		rl.wrapNote(s, "typed into during this sweep")
		return wrapNext
	}
	w := rl.cfg.Wake
	marked := false
	beforeSend := func() error {
		// (a) the focus, fresh: the sweep's cache may date from the wake's read.
		sw.focusDone = true
		sw.focused, sw.focusErr = w.Focused()
		if sw.focusErr != nil {
			rl.svc.logf("wrap: %s: cmux identify: %v", wakeShort(s), sw.focusErr)
			return errTypeStop
		}
		if strings.EqualFold(sw.focused, c.SurfaceID) {
			return fmt.Errorf("left idle during the checks")
		}
		// (b) the native state: still this session, idle, status time unmoved.
		n, ok := w.ReadNative(p.ConfigDir, p.PID)
		if !ok || n.SessionID != s || n.Status != "idle" || n.StatusUpdatedAt != c.Native.StatusUpdatedAt {
			return fmt.Errorf("left idle during the checks")
		}
		// (c) the marker, first: a row already there, or a write error, aborts.
		created, err := rl.wrapJournal(p, "sent", "typed "+rl.cfg.DoneCommand, now)
		if err != nil {
			return fmt.Errorf("journal write failed: %w", err)
		}
		if !created {
			return fmt.Errorf("marker already written")
		}
		marked = true
		return nil
	}
	_, out := rl.typeGuarded(s, rl.cfg.DoneCommand, c, p, now, sw, wrapNoter{rl}, nil, nil, nil, beforeSend)
	switch out {
	case typeStop:
		return wrapStop
	case typeRefused:
		if marked {
			// The send failed after the marker: never retyped, never paused.
			if _, err := rl.wrapJournal(p, "failed", "send failed", now); err != nil {
				rl.svc.logf("wrap: %s: failure not recorded: %v", wakeShort(s), err)
			}
			return wrapActed
		}
		return wrapNext
	}
	rl.wrapClearNote(s)
	rl.svc.logf("wake: typed %s in %s for %s (idle done)", rl.cfg.DoneCommand, c.SurfaceID, wakeShort(s))
	return wrapActed
}

// wrapNote logs a refusal once per change of reason and session: its own
// memory, never the wake's lastReason (forgetWakes clears that one).
func (rl *Relay) wrapNote(s, reason string) {
	rl.wrapOnce(s, "held: "+reason, fmt.Sprintf("wrap: %s held: %s", wakeShort(s), reason))
}

func (rl *Relay) wrapOnce(s, key, line string) {
	rl.wakeMu.Lock()
	same := rl.wrapReason[s] == key
	rl.wrapReason[s] = key
	rl.wakeMu.Unlock()
	if !same {
		rl.svc.logf("%s", line)
	}
}

func (rl *Relay) wrapClearNote(s string) {
	rl.wakeMu.Lock()
	delete(rl.wrapReason, s)
	rl.wakeMu.Unlock()
}

type wrapNoter struct{ rl *Relay }

func (n wrapNoter) held(s, reason string) { n.rl.wrapNote(s, reason) }

func (n wrapNoter) readScreen(s string, err error) {
	n.rl.wrapOnce(s, "read-screen: "+err.Error(), fmt.Sprintf("wrap: %s cmux read-screen: %v", wakeShort(s), err))
}

// watcherHolds is the last re-check before typing (D26), run by paneGuard:
// Standby, readable or not, holds (D16, fail closed); a message pending holds,
// a one-way notice aside: the inbox hook delivers it at the done command's
// prompt, like at any prompt; an open Stop watcher holds (the wrap waits for
// it, like the wake).
func (n wrapNoter) watcherHolds(s string) (bool, string) {
	if in, why := n.rl.inStandby(s); in {
		if why != standbyReason {
			return true, why
		}
		return true, "standby (D16)"
	}
	if c, err := n.rl.svc.Store.CountPendingExceptKind(s, pigtail.MessageKindNotice, n.rl.cfg.Now()); err != nil {
		return true, "pending messages unreadable: " + err.Error()
	} else if c > 0 {
		return true, "messages pending"
	}
	if n.rl.watched(s) {
		return true, "watched"
	}
	return false, ""
}

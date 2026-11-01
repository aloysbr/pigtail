package handlers

// Idle session pause (sub_x000000000000036, D12–D24): at each relay sweep,
// after the wake and the Standby compaction, pigtaild stops by SIGTERM the
// claude process of the oldest session idle past the threshold, once every
// guard has passed (paneGuard and the pause's own checks). The session keeps
// its tab: the `c` launcher, told by the session's .sid, offers « Entrée =
// relancer », and a cmux restart relaunches it (D15·t3). The session enters
// Standby with its compaction settled, so the resumed claude neither gets
// /compact typed nor is woken before the user's prompt.
//
// Every check comes first, with no mutation (sequence A); then the grouped
// mutations, with no cmux call between them (sequence B, D20). Once they have
// begun only two things undo them: the signal refused, or the process
// surviving it (followPauses).

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// pauseExitTimeout: how long the sweep waits for the signalled process
	// to exit before leaving the row stopping for followPauses.
	pauseExitTimeout = 20 * time.Second
	// pauseJournalKindPrefix prefixes the journal kinds session-pause-paused,
	// session-pause-failed and session-pause-resumed.
	pauseJournalKindPrefix = "session-pause-"
)

// pauseIdle is the idle pause's part of a sweep: the oldest idle session that
// passes every check is stopped — one pause per sweep. False when a cmux
// answer every session needs failed.
func (rl *Relay) pauseIdle(now time.Time, sw *wakeSweep) bool {
	if !rl.followPauses(now) {
		return false
	}
	threshold := rl.cfg.IdlePause
	if threshold <= 0 {
		return true
	}
	if missing := rl.pauseUnwired(); missing != "" {
		rl.pauseOnce("*", "held: not wired", "pause: held: not wired ("+missing+" nil)")
		return true
	}
	ps, err := rl.svc.Store.ListPeers(false)
	if err != nil {
		rl.svc.logf("pause: peers unreadable: %v", err)
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
			rl.pauseNote(s, "waits unreadable: "+err.Error())
			continue
		}
		c.LimitResumePending = rl.limitResumePending(p, c)
		c.DoneCommand = rl.cfg.DoneCommand != ""
		if ok, why := relay.PauseAllowed(c, threshold); !ok {
			// Only a session idle past the threshold is worth a line.
			if pauseIdleLong(c, now, threshold) {
				rl.pauseNote(s, why)
			} else {
				rl.pauseClearNote(s)
			}
			continue
		}
		cands = append(cands, cand{p, c})
	}
	// Oldest idle first: the status time is the native file's.
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].c.Native.StatusUpdatedAt < cands[j].c.Native.StatusUpdatedAt
	})
	for _, k := range cands {
		switch rl.pauseOne(k.p, k.c, now, sw) {
		case pauseStop:
			return false
		case pauseActed:
			return true // one pause per sweep
		}
	}
	return true
}

// pauseResult is how one candidate's attempt ended.
type pauseResult int

const (
	pauseNext  pauseResult = iota // held, or the begin failed: the next candidate
	pauseActed                    // a signal was sent (or refused): this sweep's pause
	pauseStop                     // a cmux answer every session needs failed
)

// pauseUnwired names the first nil seam the pause needs ("" = all wired).
// The commits between the pause's arrival and its wiring never pause.
func (rl *Relay) pauseUnwired() string {
	w := rl.cfg.Wake
	switch {
	case w == nil:
		return "Wake"
	case w.Background == nil:
		return "Background"
	case w.SessionAlive == nil:
		return "SessionAlive"
	case w.Signal == nil:
		return "Signal"
	case w.WaitGone == nil:
		return "WaitGone"
	case w.ProcessEnviron == nil:
		return "ProcessEnviron"
	}
	return ""
}

// pauseOne runs sequence A then B for one candidate.
func (rl *Relay) pauseOne(p pigtail.PeerSession, c relay.Candidate, now time.Time, sw *wakeSweep) pauseResult {
	w := rl.cfg.Wake
	s := p.SessionID
	// --- A. Every check, no mutation. ---
	if why := rl.pauseHeldLocal(p); why != "" {
		rl.pauseNote(s, why)
		return pauseNext
	}
	// T-k: a Standby's compaction is settled before a pause.
	sb, err := rl.standbyRow(s)
	if err != nil {
		rl.pauseNote(s, "standby unreadable (compaction check)")
		return pauseNext
	}
	if sb != nil && sb.CompactedAt == nil && sb.CompactFailedAt == nil {
		rl.pauseNote(s, "compaction pending")
		return pauseNext
	}
	if sw.typed[s] {
		rl.pauseNote(s, "typed into during this sweep")
		return pauseNext
	}
	var screen string
	onScreen := func(sc string) bool { screen = sc; return true }
	n, out := rl.paneGuard(s, c, p, now, sw, pauseNoter{rl}, onScreen)
	switch out {
	case typeStop:
		return pauseStop
	case typeRefused:
		return pauseNext
	}
	if n.Status != "idle" {
		rl.pauseNote(s, "left idle during the checks")
		return pauseNext
	}
	// T-j: background work the native status does not show, on the screen
	// paneGuard read (one read-screen per candidate).
	work, err := w.Background(p.ConfigDir, s, p.PID, screen)
	switch {
	case len(work) > 0:
		rl.pauseNote(s, "background work ("+strings.Join(work, ", ")+")")
		return pauseNext
	case err != nil:
		rl.pauseNote(s, "background work unreadable")
		return pauseNext
	}
	env, err := w.ProcessEnviron(p.PID)
	if err != nil {
		rl.pauseNote(s, "environment unreadable")
		return pauseNext
	}
	sidPath, _ := cmux.EnvValue(env, relaunchfile.EnvFile)
	if sidPath != "" {
		if err := relaunchfile.SidPathAllowed(rl.cfg.RelaunchDir, sidPath); err != nil {
			rl.pauseNote(s, "relaunch file outside "+rl.cfg.RelaunchDir)
			return pauseNext
		}
	}
	// The last re-reads, closest to the mutations: the focus first (the
	// sweep's cache is stale after the cmux calls; the one cmux re-read),
	// then, without cmux, the native state and the process.
	sw.focusDone = true
	sw.focused, sw.focusErr = w.Focused()
	if sw.focusErr != nil {
		rl.svc.logf("pause: %s: cmux identify: %v", wakeShort(s), sw.focusErr)
		return pauseStop
	}
	if strings.EqualFold(sw.focused, c.SurfaceID) {
		rl.pauseNote(s, "left idle during the checks")
		return pauseNext
	}
	if n2, ok := w.ReadNative(p.ConfigDir, p.PID); !ok || n2.SessionID != s || n2.Status != "idle" ||
		n2.StatusUpdatedAt != c.Native.StatusUpdatedAt {
		rl.pauseNote(s, "left idle during the checks")
		return pauseNext
	}
	if !w.SessionAlive(p.ConfigDir, p.PID, s) {
		rl.pauseNote(s, "left idle during the checks")
		return pauseNext
	}

	// --- B. The grouped mutations: store and local files only. ---
	row := pigtail.SessionPause{
		SessionID: s, PID: p.PID, SurfaceID: c.SurfaceID, CmuxWorkspaceID: p.CmuxWorkspaceID,
		SidPath: sidPath, State: pigtail.PauseStopping, EnteredStandby: sb == nil, Since: now,
	}
	if err := rl.svc.Store.BeginPause(row); err != nil {
		rl.svc.logf("pause: %s begin failed: %v", wakeShort(s), err)
		return pauseNext
	}
	if sidPath != "" {
		if _, err := relaunchfile.ClaimSid(sidPath, s); err != nil {
			row.SidPath = "" // nothing of ours to withdraw
			rl.pauseUndo(row, "relaunch file not written: "+err.Error(), false)
			return pauseNext
		}
	}
	if row.EnteredStandby {
		// The prio is cmux's own pin (D3 of sub_x000000000000014): the pause
		// neither reads nor moves it.
		err := rl.svc.Store.EnterStandby(s, now)
		if err == nil {
			err = rl.svc.Store.MarkStandbyCompacted(s, now)
		}
		if err != nil {
			rl.pauseUndo(row, "standby not written: "+err.Error(), false)
			return pauseNext
		}
	}
	// The peer ends before the signal: else the sidebar would show Dead
	// while the process exits.
	if err := rl.endPeer(p, now); err != nil {
		rl.pauseUndo(row, "peer not ended: "+err.Error(), false)
		return pauseNext
	}
	if err := rl.svc.Store.MarkSignaled(s, now); err != nil {
		rl.pauseUndo(row, "signal not recorded: "+err.Error(), false)
		return pauseNext
	}
	if err := w.Signal(p.PID); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// Exited on its own since the last re-read: the session moved,
			// nothing was refused (final review M2).
			rl.pauseUndo(row, "process gone before the signal", false)
			return pauseNext
		}
		rl.pauseUndo(row, "SIGTERM refused: "+err.Error(), true)
		return pauseActed
	}
	rl.refreshStandbyMarker(s)
	if row.EnteredStandby {
		rl.publishStandby(p, true)
	}
	if !w.WaitGone(p.PID, pauseExitTimeout) {
		// Still running: the row stays stopping and the .sid kept;
		// followPauses judges it.
		return pauseActed
	}
	rl.pauseDone(p, row, now.Sub(time.UnixMilli(c.Native.StatusUpdatedAt)), now)
	return pauseActed
}

// pauseDone records the process gone: the row becomes paused, one log line,
// the journal line and the event.
func (rl *Relay) pauseDone(p pigtail.PeerSession, row pigtail.SessionPause, idle time.Duration, now time.Time) {
	s := p.SessionID
	if err := rl.svc.Store.MarkPaused(s, now); err != nil {
		rl.svc.logf("pause: %s paused, row not updated: %v", wakeShort(s), err)
	}
	rl.pauseClearNote(s)
	launcher := "no"
	if row.SidPath != "" {
		launcher = "yes"
	}
	idleText := "unknown" // a slow exit judged by followPauses
	if idle > 0 {
		idleText = idle.Round(time.Minute).String()
	}
	rl.svc.logf("pause: %s paused (pid %d, idle %s, launcher %s)", wakeShort(s), p.PID, idleText, launcher)
	rl.pauseJournal(p, p.PID, "paused", fmt.Sprintf("session pause %s: pid %d stopped (launcher %s)", wakeShort(s), p.PID, launcher), now)
	rl.publishPause(p, pigtail.EventSessionPauseChanged, map[string]interface{}{"paused": true})
}

// pauseUndo takes back what a pause wrote, from its row alone (a daemon
// restart included): the .sid while it still holds the id, the Standby the
// pause entered, the Prio it ended, the row; then revives the peer when its
// process still runs. failed: a signal was tried — the give-up is logged,
// journalled (it blocks this pid) and published; else only held is logged.
func (rl *Relay) pauseUndo(row pigtail.SessionPause, why string, failed bool) {
	s := row.SessionID
	now := rl.cfg.Now()
	p, err := rl.svc.Store.GetPeer(s)
	if err != nil || p == nil {
		p = &pigtail.PeerSession{SessionID: s, PID: row.PID, CmuxWorkspaceID: row.CmuxWorkspaceID}
	}
	if row.SidPath != "" {
		if err := relaunchfile.ReleaseSid(row.SidPath, s); err != nil {
			rl.svc.logf("pause: %s relaunch file not withdrawn: %v", wakeShort(s), err)
		}
	}
	if row.EnteredStandby {
		if _, err := rl.svc.Store.ClearStandby(s, now); err != nil {
			rl.svc.logf("pause: %s standby not cleared: %v", wakeShort(s), err)
		}
		rl.refreshStandbyMarker(s)
		rl.publishStandby(*p, false)
	}
	if _, err := rl.svc.Store.EndPause(s); err != nil {
		rl.svc.logf("pause: %s row not removed: %v", wakeShort(s), err)
	}
	if p.EndedAt != nil && p.PID == row.PID && p.PID > 0 && rl.sessionAlive(p.ConfigDir, p.PID, s) {
		q := *p
		q.UpdatedAt = now
		q.Source = "snapshot"
		if err := rl.svc.Store.UpsertPeer(&q); err != nil {
			rl.svc.logf("pause: %s peer not revived: %v", wakeShort(s), err)
		} else {
			rl.restoreEndedWaits(s, q.Source)
			rl.refreshWaitMarker(s)
			rl.refreshStandbyMarker(s)
		}
	}
	if !failed {
		rl.pauseNote(s, why)
		return
	}
	rl.pauseOnce(s, "gave up: "+why, fmt.Sprintf("pause: %s gave up: %s", wakeShort(s), why))
	q := *p
	q.PID = row.PID
	rl.pauseJournal(q, row.PID, "failed", fmt.Sprintf("session pause %s failed: %s", wakeShort(s), why), now)
	rl.publishPause(q, pigtail.EventSessionPauseFailed, map[string]interface{}{"reason": why})
}

// publishPause publishes a pause event with the session's scope.
func (rl *Relay) publishPause(p pigtail.PeerSession, t pigtail.EventType, data map[string]interface{}) {
	if rl.svc.Bus == nil {
		return
	}
	data["session_id"] = p.SessionID
	data["cmux_workspace_id"] = p.CmuxWorkspaceID
	rl.svc.Bus.Publish(pigtail.Event{Type: t, Scope: string(sessionScope(p)), Data: data})
}

// pauseIdleLong: the native status time is at least threshold old.
func pauseIdleLong(c relay.Candidate, now time.Time, threshold time.Duration) bool {
	return c.NativeOK && c.Native.StatusUpdatedAt > 0 &&
		now.Sub(time.UnixMilli(c.Native.StatusUpdatedAt)) >= threshold
}

// pauseHeldLocal: the refusals the daemon knows without cmux and
// PauseAllowed does not — a message waiting outside Standby (in Standby the
// inbox is never claimed before the user's prompt, D21; an unreadable Standby
// skips nothing), a give-up for this pid recorded in the journal. "" =
// nothing holds.
func (rl *Relay) pauseHeldLocal(p pigtail.PeerSession) string {
	in, why := rl.inStandby(p.SessionID)
	switch {
	case in && why != standbyReason:
		return "standby unreadable (pending check)"
	case !in:
		if n, err := rl.svc.Store.CountPending(p.SessionID, rl.cfg.Now()); err != nil {
			return "pending messages unreadable: " + err.Error()
		} else if n > 0 {
			return "messages pending"
		}
	}
	if sent, failed, _, err := rl.wrapMarked(p); err != nil {
		return "journal unreadable: " + err.Error()
	} else if sent || failed {
		return "wrapped by the idle wrap"
	}
	given, err := rl.pauseGivenUp(p)
	switch {
	case err != nil:
		return "journal unreadable: " + err.Error()
	case given:
		return "given up earlier for pid " + strconv.Itoa(p.PID)
	}
	return ""
}

// pauseGivenUp reads the durable give-up: the journal line of a failed pause
// of this session on this pid.
func (rl *Relay) pauseGivenUp(p pigtail.PeerSession) (bool, error) {
	rows, err := rl.svc.Store.ListJournal(store.JournalFilter{
		Scope: sessionScope(p), Kind: pauseJournalKindPrefix + "failed", IDs: []string{pauseJournalID(p.SessionID, p.PID, "failed")}, Limit: 1,
	})
	return len(rows) > 0, err
}

func pauseJournalID(session string, pid int, state string) string {
	return "session-pause:" + session + ":" + strconv.Itoa(pid) + ":" + state
}

// pauseJournal writes the deterministic journal line of an outcome
// (paused|failed|resumed) for this session and pid.
func (rl *Relay) pauseJournal(p pigtail.PeerSession, pid int, state, summary string, now time.Time) {
	e := pigtail.JournalEntry{ID: pauseJournalID(p.SessionID, pid, state), Timestamp: now, Kind: pauseJournalKindPrefix + state,
		Scope: string(sessionScope(p)), SubjectID: p.SubjectID, Summary: summary, Source: "pigtail:session/" + p.SessionID}
	if err := e.Validate(); err != nil {
		rl.svc.logf("pause: %s journal line not written: %v", wakeShort(p.SessionID), err)
		return
	}
	if _, err := rl.svc.Store.AddJournal(&e); err != nil {
		rl.svc.logf("pause: %s journal line not written: %v", wakeShort(p.SessionID), err)
	}
}

// sessionAlive is the session-alive reading of the relay: the wake's seam
// when wired, else claudesession.SessionAlive. restoreEndedWaits and
// RecordSnapshot read it too, so a test drives one liveness for all.
func (rl *Relay) sessionAlive(cfg string, pid int, session string) bool {
	if w := rl.cfg.Wake; w != nil && w.SessionAlive != nil {
		return w.SessionAlive(cfg, pid, session)
	}
	return claudesession.SessionAlive(cfg, pid, session)
}

// pauseNote logs a refusal once per change of reason and session.
func (rl *Relay) pauseNote(s, reason string) {
	rl.pauseOnce(s, "held: "+reason, fmt.Sprintf("pause: %s held: %s", wakeShort(s), reason))
}

func (rl *Relay) pauseOnce(s, key, line string) {
	rl.wakeMu.Lock()
	same := rl.pauseReason[s] == key
	rl.pauseReason[s] = key
	rl.wakeMu.Unlock()
	if !same {
		rl.svc.logf("%s", line)
	}
}

// pauseClearNote forgets the last refusal: the session is no longer one.
func (rl *Relay) pauseClearNote(s string) {
	rl.wakeMu.Lock()
	delete(rl.pauseReason, s)
	rl.wakeMu.Unlock()
}

type pauseNoter struct{ rl *Relay }

func (n pauseNoter) held(s, reason string) { n.rl.pauseNote(s, reason) }

func (n pauseNoter) readScreen(s string, err error) {
	n.rl.pauseOnce(s, "read-screen: "+err.Error(), fmt.Sprintf("pause: %s cmux read-screen: %v", wakeShort(s), err))
}

// watcherHolds: the D26 re-read for the pause is not the watcher's (it holds
// nothing here) but a message that arrived during the cmux calls — skipped
// only for a readable Standby (D21), fail closed on an unreadable one.
func (n pauseNoter) watcherHolds(s string) (bool, string) {
	in, why := n.rl.inStandby(s)
	switch {
	case in && why != standbyReason:
		return true, "standby unreadable (recheck)"
	case in:
		return false, ""
	}
	if c, err := n.rl.svc.Store.CountPending(s, n.rl.cfg.Now()); err != nil {
		return true, "pending messages unreadable: " + err.Error()
	} else if c > 0 {
		return true, "messages pending"
	}
	return false, ""
}

// defaultRelaunchDir is the launcher's directory for this daemon.
func defaultRelaunchDir() string { return relaunchfile.Dir(os.Getenv) }

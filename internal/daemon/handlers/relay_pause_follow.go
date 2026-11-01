package handlers

// The follow-up of the idle pause (sub_x000000000000036): the rows a sweep
// left stopping, the peers a snapshot revived, the resume. No cmux call
// (D15·t3): a session cmux relaunches at its restart is an ordinary resume,
// matched by session id, never by surface.

import (
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// pauseSurviveWait: how long a signalled pid may stay alive before the pause
// is given up.
const pauseSurviveWait = 2 * time.Minute

// followPauses runs before any new pause, over every pause row:
//   - stopping, never signalled (the daemon restarted in between): undone,
//     never a give-up;
//   - stopping and signalled: the signalled pid (row.PID, never the peer's
//     current one) gone → paused; alive past pauseSurviveWait → given up;
//   - paused, the peer revived on the same pid that is dead → ended again;
//   - paused, the peer alive on another pid → resumed (a snapshot is the
//     only registration that saw it).
//
// It always returns true: no cmux answer is read.
func (rl *Relay) followPauses(now time.Time) bool {
	rows, err := rl.svc.Store.ListPauses()
	if err != nil {
		rl.svc.logf("pause: rows unreadable: %v", err)
		return true
	}
	for _, row := range rows {
		s := row.SessionID
		p, err := rl.svc.Store.GetPeer(s)
		if err != nil || p == nil {
			p = &pigtail.PeerSession{SessionID: s, PID: row.PID, CmuxWorkspaceID: row.CmuxWorkspaceID}
		}
		switch {
		case row.State == pigtail.PauseStopping && row.SignaledAt == nil:
			rl.pauseUndo(row, "daemon restarted before the signal", false)
		case row.State == pigtail.PauseStopping:
			switch {
			case !rl.sessionAlive(p.ConfigDir, row.PID, s):
				q := *p
				q.PID = row.PID
				rl.pauseDone(q, row, 0, now)
			case now.Sub(*row.SignaledAt) >= pauseSurviveWait:
				rl.pauseUndo(row, fmt.Sprintf("pid %d alive %s after the signal", row.PID, now.Sub(*row.SignaledAt).Round(time.Second)), true)
			}
		case p.EndedAt != nil:
		case p.PID == row.PID:
			if !rl.sessionAlive(p.ConfigDir, row.PID, s) {
				if err := rl.endPeer(*p, now); err != nil {
					rl.svc.logf("pause: %s peer not ended again: %v", wakeShort(s), err)
				}
			}
		case p.PID > 0 && rl.sessionAlive(p.ConfigDir, p.PID, s):
			rl.endPauseResumed(*p)
		}
	}
	return true
}

// resumePause ends the paused row of a session that registers alive: its
// SessionStart hook, or a registration whose process runs and names it. The
// Standby stays until the user's prompt (D13, D24), except a kept answer
// delivered at the registration (D29-C).
func (rl *Relay) resumePause(p pigtail.PeerSession) {
	row, err := rl.svc.Store.GetPause(p.SessionID)
	if err != nil || row == nil || row.State != pigtail.PausePaused {
		return
	}
	if p.Source != "hook" && (p.PID <= 0 || !rl.sessionAlive(p.ConfigDir, p.PID, p.SessionID)) {
		return
	}
	rl.endPauseResumed(p)
}

// endPauseResumed closes the pause of a session seen alive again: the row,
// one log line, the journal line of the new pid, the event.
func (rl *Relay) endPauseResumed(p pigtail.PeerSession) {
	s := p.SessionID
	ended, err := rl.svc.Store.EndPause(s)
	if err != nil {
		rl.svc.logf("pause: %s row not removed at resume: %v", wakeShort(s), err)
		return
	}
	if !ended {
		return
	}
	rl.pauseClearNote(s)
	rl.svc.logf("pause: %s resumed (pid %d)", wakeShort(s), p.PID)
	rl.pauseJournal(p, p.PID, "resumed", fmt.Sprintf("session pause %s: resumed under pid %d", wakeShort(s), p.PID), rl.cfg.Now())
	rl.publishPause(p, pigtail.EventSessionPauseChanged, map[string]interface{}{"paused": false})
}

// endPauseReplaced ends the pause of the sessions whose surface p, by its
// own SessionStart, now holds under another id (like endStandbyReplaced).
func (rl *Relay) endPauseReplaced(p pigtail.PeerSession) {
	ids, err := rl.svc.Store.EndPauseReplacedOnSurface(p.CmuxSurfaceID, p.SessionID)
	if err != nil {
		rl.svc.logf("pause: end pause replaced on %s: %v", p.CmuxSurfaceID, err)
		return
	}
	for _, id := range ids {
		old := pigtail.PeerSession{SessionID: id}
		if got, err := rl.svc.Store.GetPeer(id); err == nil && got != nil {
			old = *got
		}
		rl.pauseClearNote(id)
		rl.svc.logf("pause: %s pause ended: %s took its surface", wakeShort(id), wakeShort(p.SessionID))
		rl.publishPause(old, pigtail.EventSessionPauseChanged, map[string]interface{}{"paused": false})
	}
}

// EndPauseGone ends the pause of a session whose cmux workspace is gone
// (two listings, the sidebar loop's closeMissingPause): the row and the
// event. The relaunch file stays: it is the launcher's, and its tab is gone.
func (rl *Relay) EndPauseGone(session, workspace string) error {
	ended, err := rl.svc.Store.EndPause(session)
	if err != nil || !ended {
		return err
	}
	p := pigtail.PeerSession{SessionID: session, CmuxWorkspaceID: workspace}
	if got, err := rl.svc.Store.GetPeer(session); err == nil && got != nil {
		p = *got
	}
	rl.pauseClearNote(session)
	rl.svc.logf("pause: %s pause ended: workspace %s closed", wakeShort(session), workspace)
	rl.publishPause(p, pigtail.EventSessionPauseChanged, map[string]interface{}{"paused": false})
	return nil
}

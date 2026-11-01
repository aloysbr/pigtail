package handlers

import (
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Wait records and their events (sub_x000000000000026, Task 4). A declared
// wait, or the auto wait of a Claude Code dialog, opens a record beside its
// session_waits row (store.SetSessionWaitRecord); every transition of a record
// is a wait.* event tagged with the wait's scope, whose data is
// {wait_id, session_id, state} — never the reason nor the content.

// waitRetention is how long a closed wait record is kept (G3).
const waitRetention = 30 * 24 * time.Hour

// publishWaitEvent emits one wait.* event for record id of session sessionID,
// tagged with the scope of the wait sc (D28: a wait is its subject's in its
// scope — the emitter's, by construction, at declaration).
func (rl *Relay) publishWaitEvent(t pigtail.EventType, id, sessionID string, sc scope.Scope, state string) {
	if rl.svc.Bus == nil || id == "" {
		return
	}
	rl.svc.Bus.Publish(pigtail.Event{Type: t, Scope: string(sc), Data: map[string]interface{}{
		"wait_id": id, "session_id": sessionID, "state": state,
	}})
}

// publishClosed emits wait.closed (state) for record id, told under the
// session that emitted it and in the record's own scope — re-read: a record
// replaced by another session's declaration (D29-B) is that session's, and
// WaitTransition carries no scope. fallback is the scope used when the
// record cannot be read.
func (rl *Relay) publishClosed(id, sessionID string, fallback scope.Scope, state string) {
	if id == "" {
		return
	}
	sc := fallback
	if rec, err := rl.svc.Store.GetWaitRecord(id); err == nil && rec != nil {
		sc = waitScope(rec)
		if sessionID == "" {
			sessionID = rec.SessionID
		}
	}
	rl.publishWaitEvent(pigtail.EventWaitClosed, id, sessionID, sc, state)
}

// recordWait records w for session p with its validated details (JSON, nil =
// none) and tells it: the marker, session.wait_changed, wait.closed
// (replaced) for the record it replaced and wait.opened for the one it
// opened. The one path of every wait the daemon poses: the POST route, the
// turn end (session_handbacks.go) and the respawn (relay_respawn.go).
func (rl *Relay) recordWait(p pigtail.PeerSession, w pigtail.SessionWait, details []byte) error {
	// The subject is frozen only when the session's scope may read it (D23, the
	// rule of POST /journal): a default-scope session working in a perso subject's
	// worktree records a wait with no subject.
	subjectID := p.SubjectID
	if subjectID != "" && !rl.subjectVisible(sessionScope(p), subjectID) {
		subjectID = ""
	}
	tr, err := rl.svc.Store.SetSessionWaitRecord(p.SessionID, w, store.WaitMeta{
		Scope: string(sessionScope(p)), SubjectID: subjectID, Details: details,
	})
	if err != nil {
		return err
	}
	rl.refreshWaitMarker(p.SessionID)
	rl.publishWait(p, w.Type, w.Source)
	sc := sessionScope(p)
	rl.publishClosed(tr.ReplacedID, tr.ReplacedSessionID, sc, pigtail.WaitStateReplaced)
	rl.publishClosed(tr.AlsoReplacedID, tr.AlsoReplacedSessionID, sc, pigtail.WaitStateReplaced)
	rl.publishWaitEvent(pigtail.EventWaitOpened, tr.OpenedID, p.SessionID, sc, pigtail.WaitStateOpen)
	return nil
}

// publishWaitAnswered tells that the declared wait rec was answered
// (store.AnswerWaitWithMessage dropped its declared row): the emitter's
// marker and session.wait_changed while it is not gone, and wait.answered
// always, under the emitter and in the wait's scope. Called after the commit
// only — a failed answer publishes nothing.
func (rl *Relay) publishWaitAnswered(emitter pigtail.PeerSession, rec *store.WaitRecord) {
	if !rl.emitterGone(emitter) {
		rl.refreshWaitMarker(emitter.SessionID)
		rl.publishWait(emitter, "", pigtail.WaitSourceDeclared)
	}
	rl.publishWaitEvent(pigtail.EventWaitAnswered, rec.ID, rec.SessionID, waitScope(rec), pigtail.WaitStateAnswered)
}

// publishWaitSnoozed tells that the open wait rec was snoozed: wait.snoozed
// only — the session's marker and pill do not change.
func (rl *Relay) publishWaitSnoozed(rec *store.WaitRecord) {
	rl.publishWaitEvent(pigtail.EventWaitSnoozed, rec.ID, rec.SessionID, waitScope(rec), pigtail.WaitStateOpen)
}

// publishWaitsCleared emits wait.closed (cleared) for each record id of the
// session that a clear closed, each in its wait's scope.
func (rl *Relay) publishWaitsCleared(p pigtail.PeerSession, ids []string) {
	for _, id := range ids {
		rl.publishClosed(id, p.SessionID, sessionScope(p), pigtail.WaitStateCleared)
	}
}

// purgeWaits deletes the wait records closed more than waitRetention ago and
// logs each kept answer it purged undelivered. Relay.Run calls it at start
// and at each tick of its sweep.
func (rl *Relay) purgeWaits() {
	before := rl.cfg.Now().Add(-waitRetention)
	kept, err := rl.svc.Store.KeptAnswers()
	if err != nil {
		rl.svc.logf("relay: purge closed waits: kept answers: %v", err)
	}
	n, never, err := rl.svc.Store.PurgeClosedWaitRecords(before)
	if err != nil {
		rl.svc.logf("relay: purge closed waits: %v", err)
		return
	}
	// C8: a kept answer whose subject never had a session again is lost at
	// 30 days, with this line only (D28 freeze: no alert, no close route).
	subjectOf := map[string]string{}
	for _, k := range kept {
		subjectOf[k.ID] = k.SubjectID
	}
	for _, id := range never {
		rl.svc.logf("relay: purged kept answer %s never delivered (subject %s)", id, subjectOf[id])
	}
	if n > 0 {
		rl.svc.logf("relay: purged %d closed waits", n)
	}
}

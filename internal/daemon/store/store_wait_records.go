package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Wait records (migration 0027, sub_x000000000000026) ---------------------
//
// A wait record stands beside the `declared` row of session_waits, and beside
// the `auto` row of a Claude Code dialog (permission, question): a stable id,
// the typed content, its history. D28: an answerable declared wait bound to a
// subject belongs to the subject in its scope (owner, waitOwner); the session
// is its emitter. At most one open record per owner and source, and per
// session and source (the PK of session_waits). Records move in the same
// transaction as their session_waits row.

// ErrWaitClosed: the wait is no longer open (replaced, cleared, answered).
var ErrWaitClosed = errors.New("store: wait is closed")

// ErrWaitAuto: an automatic wait (a Claude Code dialog) is answered in the
// terminal only (D2).
var ErrWaitAuto = errors.New("store: an automatic wait takes no answer")

// ErrWaitDelivered: a kept answer already went to a session (another path
// delivered it first).
var ErrWaitDelivered = errors.New("store: answer already delivered")

// ErrRecipientGone: the session an answer was about to reach has ended.
var ErrRecipientGone = errors.New("store: recipient session ended")

// WaitMeta is what a wait record keeps beyond its session_waits row: the
// session's scope and subject at declaration, and the validated content
// (JSON; empty = none).
type WaitMeta struct {
	Scope, SubjectID string
	Details          []byte
}

// WaitTransition names the records a SetSessionWaitRecord opened and
// replaced, all empty when nothing moved. A declaration replaces at most two
// records: the open one of its owner and source (maybe another session's:
// the subject's wait, D29-B), and the open one of its session and source
// (B2). ReplacedID is the first found, AlsoReplacedID the second when
// distinct; each *SessionID names the replaced record's emitter.
type WaitTransition struct {
	OpenedID                              string
	ReplacedID, ReplacedSessionID         string
	AlsoReplacedID, AlsoReplacedSessionID string
}

// waitOwner is the owner of a wait record — the rule of migration 0027's
// CHECK, its only Go source: the subject in its scope for a declared wait of
// an answerable type bound to a subject (D28), else the session. sc is the
// canonical scope written in the scope column.
func waitOwner(sessionID, source, sc, subjectID string, t waittype.Type) string {
	if source == pigtail.WaitSourceDeclared && subjectID != "" && waittype.Answerable(t) {
		return "subject:" + sc + ":" + subjectID
	}
	return "session:" + sessionID
}

// WaitRecord is a wait record with the scope it was declared in, its owner
// ('subject:<scope>:<id>' or 'session:<id>', D28) and the message its answer
// went as (empty while the answer is kept, D29-C). Where and when it went
// are the embedded Wait.DeliveredTo / Wait.DeliveredAt.
type WaitRecord struct {
	pigtail.Wait
	Scope     string
	Owner     string
	MessageID string
}

// WaitRecordFilter narrows ListOpenWaitRecords and
// ListRecentClosedWaitRecords. Scope scope.Any lists every scope; a wait
// snoozed past Now is left out unless IncludeSnoozed (open records only). A
// zero Now reads the clock. SubjectID and SessionID, when set, keep the
// records of that subject or that emitting session.
type WaitRecordFilter struct {
	Scope          scope.Scope
	Now            time.Time
	IncludeSnoozed bool
	SubjectID      string
	SessionID      string
}

// MaxClosedWaitHistory bounds ListRecentClosedWaitRecords.
const MaxClosedWaitHistory = 5

// waitFilterSQL appends f's scope, subject and session conditions.
func waitFilterSQL(q string, args []any, f WaitRecordFilter) (string, []any) {
	if f.Scope != scope.Any {
		q += ` AND scope = ?`
		args = append(args, string(f.Scope))
	}
	if f.SubjectID != "" {
		q += ` AND subject_id = ?`
		args = append(args, f.SubjectID)
	}
	if f.SessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	return q, args
}

// execer is what both *sql.DB and *sql.Tx offer to write.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// recordsWait reports whether a wait of source and type w opens a record.
func recordsWait(w pigtail.SessionWait) bool {
	switch w.Source {
	case pigtail.WaitSourceDeclared:
		return true
	case pigtail.WaitSourceAuto:
		return waittype.DialogType(waittype.Type(w.Type))
	}
	return false
}

// wrapType reports whether w is a declaration of the end of the session's
// work — done, wrapped (D30) — which never replaces its subject's wait. The
// emitter's own working still does (Review Focus 10 of the plan).
func wrapType(w pigtail.SessionWait) bool {
	return w.Source == pigtail.WaitSourceDeclared && (w.Type == string(waittype.Done) || w.Type == string(waittype.Wrapped))
}

// setWaitRecord moves the records of w's source inside tx (see
// SetSessionWaitRecord).
func setWaitRecord(tx *sql.Tx, sessionID string, w pigtail.SessionWait, meta WaitMeta) (WaitTransition, error) {
	if w.Source != pigtail.WaitSourceDeclared && w.Source != pigtail.WaitSourceAuto {
		return WaitTransition{}, nil
	}
	at := w.Since.UnixMilli()
	sc := ""
	if strings.TrimSpace(meta.Scope) != "" {
		sc = string(scope.Canonical(meta.Scope))
	}
	owner := waitOwner(sessionID, w.Source, sc, meta.SubjectID, waittype.Type(w.Type))
	var openID, openType, openReason, openOwner string
	err := tx.QueryRow(`SELECT id, type, reason, owner FROM wait_records WHERE session_id = ? AND source = ? AND state = 'open'`,
		sessionID, w.Source).Scan(&openID, &openType, &openReason, &openOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WaitTransition{}, err
	}
	if openID != "" && wrapType(w) && strings.HasPrefix(openOwner, "subject:") {
		// The emitter's own closing (its done, the wrapped it declares)
		// never closes its subject's wait: the record stays open for the
		// subject's next session, the row (the pill) reads the new type, and
		// no record opens beside it (one open per session and source).
		return WaitTransition{}, nil
	}
	if openID != "" && w.Source == pigtail.WaitSourceAuto && recordsWait(w) && openType == w.Type && openReason == w.Reason {
		// The same dialog told again (PermissionRequest, then its
		// permission_prompt notification): one record, its details kept
		// unless new ones come.
		if len(meta.Details) > 0 {
			if _, err := tx.Exec(`UPDATE wait_records SET details = ? WHERE id = ?`, string(meta.Details), openID); err != nil {
				return WaitTransition{}, err
			}
		}
		return WaitTransition{}, nil
	}
	// The open record of the owner (maybe another session's, D29-B), then
	// the session's own (B2).
	type replaced struct{ id, session string }
	var gone []replaced
	if recordsWait(w) {
		var id, sess string
		err := tx.QueryRow(`SELECT id, session_id FROM wait_records WHERE owner = ? AND source = ? AND state = 'open'`,
			owner, w.Source).Scan(&id, &sess)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WaitTransition{}, err
		}
		if id != "" {
			gone = append(gone, replaced{id, sess})
		}
	}
	if openID != "" && (len(gone) == 0 || gone[0].id != openID) {
		gone = append(gone, replaced{openID, sessionID})
	}
	var tr WaitTransition
	for i, g := range gone {
		if _, err := tx.Exec(`UPDATE wait_records SET state = 'replaced', closed_at = ?, closed_by = NULL WHERE id = ?`, at, g.id); err != nil {
			return WaitTransition{}, err
		}
		if i == 0 {
			tr.ReplacedID, tr.ReplacedSessionID = g.id, g.session
		} else {
			tr.AlsoReplacedID, tr.AlsoReplacedSessionID = g.id, g.session
		}
	}
	if !recordsWait(w) {
		return tr, nil
	}
	id := newStoreID(pigtail.PrefixWait)
	var details sql.NullString
	if len(meta.Details) > 0 {
		details = sql.NullString{String: string(meta.Details), Valid: true}
	}
	if _, err := tx.Exec(`INSERT INTO wait_records (id, session_id, source, scope, subject_id, type, reason, details, state, opened_at, owner)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
		id, sessionID, w.Source, sc, meta.SubjectID, w.Type, w.Reason, details, at, owner); err != nil {
		return WaitTransition{}, err
	}
	tr.OpenedID = id
	return tr, nil
}

// ClearSessionWaitsClosing is ClearSessionWaits that also closes, as
// cleared, the open record each declared or auto row that went had emitted,
// in one transaction; it returns the ids it closed. With gone (the session
// is gone: the sidebar loop's Ended / !Alive branches) a record its subject
// owns stays open (D29-A) and the answers to a subject's wait it was handed
// but never read are kept again for the subject's next session, as at its
// end (rekeepUnreadTx); the row goes either way.
func (s *Store) ClearSessionWaitsClosing(sessionID string, gone bool, sources ...string) (n int, closed, reheld []string, err error) {
	by := ""
	if gone {
		by = closedBySessionEnd
	}
	return s.clearAt(sessionID, time.Now().UTC(), gone, gone, by, sources...)
}

// ClearSessionWaitsKeepingSubject is the clear of the done command's prompt
// (waittype.DonePrompt): the turn's rows (auto, declared) go and the records
// the session owns close as cleared, but a record its subject owns stays
// open — closing does not answer
// the subject's wait, the subject's next session receives its answer. The
// session lives on: nothing is kept again (reheld is always empty).
func (s *Store) ClearSessionWaitsKeepingSubject(sessionID string) (n int, closed, reheld []string, err error) {
	return s.clearAt(sessionID, time.Now().UTC(), true, false, "", pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared)
}

// clearAt is ClearSessionWaitsClosing at the clock at, in its own
// transaction: keepSubject leaves a record its subject owns open, rekeep
// keeps again the unread answers the session was handed. Never call it
// inside another transaction: the store has one connection (clearTx is the
// body to reuse).
func (s *Store) clearAt(sessionID string, at time.Time, keepSubject, rekeep bool, closedBy string, sources ...string) (int, []string, []string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	n, closed, err := s.clearTx(tx, sessionID, at, keepSubject, closedBy, sources...)
	if err != nil {
		return 0, nil, nil, err
	}
	var reheld []string
	if rekeep {
		if reheld, err = rekeepUnreadTx(tx, sessionID); err != nil {
			return 0, nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, nil, err
	}
	return n, closed, reheld, nil
}

// closedBySessionEnd is wait_records.closed_by of a record closed because its
// session ended or was judged gone (migration 0030); "" = any other cause.
// On a record whose state is still 'open' it means "kept open at a session
// end" (a subject-owned record, D29-A): its session_waits row went with the
// session and RestoreEndedWaits may give it back. NULL on an open record =
// the row went by the done command's prompt on a live session: never restored.
const closedBySessionEnd = "session_end"

// clearTx deletes the session's session_waits rows of sources (none = auto
// and declared) inside tx and, for each declared or auto row that went,
// closes as cleared the open record the session emitted of that source —
// unless keepSubject and its subject owns it. closedBy ("" = NULL) says why. A record is never closed
// without its row going (k == 0 → nothing).
func (s *Store) clearTx(tx *sql.Tx, sessionID string, at time.Time, keepSubject bool, closedBy string, sources ...string) (int, []string, error) {
	if len(sources) == 0 {
		sources = []string{pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared}
	}
	// D9: a stamp lives until the session's next wait write. A clear that is
	// no session end invalidates even when it finds no row; an end path only
	// when a row of the session exists, so the gone clear the loop repeats
	// keeps the stamp its first run set.
	invalidate := closedBy == ""
	if !invalidate {
		var rows int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM session_waits WHERE session_id = ?`, sessionID).Scan(&rows); err != nil {
			return 0, nil, err
		}
		invalidate = rows > 0
	}
	if invalidate {
		if err := invalidateEndStampsTx(tx, sessionID); err != nil {
			return 0, nil, err
		}
	}
	n := 0
	var closed []string
	for _, src := range sources {
		// The type of the row about to go, read before the DELETE: a kept-open
		// record is stamped session_end only when the row was a wait the
		// session can resume (not done, wrapped, working: the work was over or
		// its job dies with the restart).
		var rowType string
		if err := tx.QueryRow(`SELECT type FROM session_waits WHERE session_id = ? AND source = ?`, sessionID, src).Scan(&rowType); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, nil, err
		}
		res, err := tx.Exec(`DELETE FROM session_waits WHERE session_id = ? AND source = ?`, sessionID, src)
		if err != nil {
			return 0, nil, err
		}
		k, err := res.RowsAffected()
		if err != nil {
			return 0, nil, err
		}
		n += int(k)
		if k == 0 || (src != pigtail.WaitSourceDeclared && src != pigtail.WaitSourceAuto) {
			continue
		}
		var id, owner string
		err = tx.QueryRow(`SELECT id, owner FROM wait_records WHERE session_id = ? AND source = ? AND state = 'open'`, sessionID, src).Scan(&id, &owner)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, nil, err
		}
		if keepSubject && strings.HasPrefix(owner, "subject:") {
			// Kept open. A session end stamps it closed_by='session_end' (state
			// stays 'open'): "kept at a session end", what lets
			// RestoreEndedWaits give the row back to a resumed session. The
			// done command's clear (closedBy "") leaves closed_by NULL, so a
			// live session is never given its row back.
			if closedBy != "" && rowType != string(waittype.Done) && rowType != string(waittype.Wrapped) && rowType != string(waittype.Working) {
				if _, err := tx.Exec(`UPDATE wait_records SET closed_by = ? WHERE id = ?`, closedBy, id); err != nil {
					return 0, nil, err
				}
			}
			continue
		}
		if _, err := tx.Exec(`UPDATE wait_records SET state = 'cleared', closed_at = ?, closed_by = NULLIF(?, '') WHERE id = ?`, at.UnixMilli(), closedBy, id); err != nil {
			return 0, nil, err
		}
		closed = append(closed, id)
	}
	return n, closed, nil
}

// EndPeerClosing marks the session ended and drops what it waited for, in
// one transaction: its session_waits rows go, the records it emitted close
// as cleared except its subject's (D29-A), and the answers to a subject's
// wait it was handed but never read are kept again (rekeepUnreadTx). It
// returns the records it closed and those it kept again.
func (s *Store) EndPeerClosing(sessionID string, at time.Time) (closed, reheld []string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE peer_sessions SET ended_at = ? WHERE session_id = ?`, at.UnixMilli(), sessionID); err != nil {
		return nil, nil, err
	}
	_, closed, err = s.clearTx(tx, sessionID, at.UTC(), true, closedBySessionEnd, pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared, pigtail.WaitSourceHeld)
	if err != nil {
		return nil, nil, err
	}
	if reheld, err = rekeepUnreadTx(tx, sessionID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return closed, reheld, nil
}

// rekeepUnreadTx keeps again, inside tx, the answers to a subject's wait
// session was handed but never read (message still pending): the message
// expires and the delivery is reset, for the subject's next session (C3).
// An answer to a wait without a subject is never kept again: its message
// stays pending for a resume under the same id (D24). The end of the session
// (EndPeerClosing), the sidebar loop judging it gone (clearAt) and the
// relay's tick judging a recipient gone (RekeepUnreadAnswers) share it.
func rekeepUnreadTx(tx *sql.Tx, sessionID string) ([]string, error) {
	rows, err := tx.Query(`SELECT r.id, r.message_id FROM wait_records r JOIN messages m ON m.id = r.message_id
		WHERE r.state = 'answered' AND r.delivered_to = ? AND r.message_id IS NOT NULL AND r.owner LIKE 'subject:%'
		AND m.state = ?`, sessionID, pigtail.MessageStatePending)
	if err != nil {
		return nil, err
	}
	type unread struct{ id, msg string }
	var us []unread
	for rows.Next() {
		var u unread
		if err := rows.Scan(&u.id, &u.msg); err != nil {
			_ = rows.Close()
			return nil, err
		}
		us = append(us, u)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var reheld []string
	for _, u := range us {
		if _, err := tx.Exec(`UPDATE messages SET state = ? WHERE id = ?`, pigtail.MessageStateExpired, u.msg); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE wait_records SET message_id = NULL, delivered_to = NULL, delivered_at = NULL WHERE id = ?`, u.id); err != nil {
			return nil, err
		}
		reheld = append(reheld, u.id)
	}
	return reheld, nil
}

// UnreadAnswerRecipients names the sessions handed an answer to a subject's
// wait they have not read yet (message still pending), each once, sorted:
// the relay's tick judges each (D29-C, a crash without SessionEnd).
func (s *Store) UnreadAnswerRecipients() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT r.delivered_to FROM wait_records r JOIN messages m ON m.id = r.message_id
		WHERE r.state = 'answered' AND r.delivered_to IS NOT NULL AND r.message_id IS NOT NULL AND r.owner LIKE 'subject:%'
		AND m.state = ? ORDER BY r.delivered_to`, pigtail.MessageStatePending)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RekeepUnreadAnswers keeps again, in its own transaction, the answers to a
// subject's wait session was handed but never read (rekeepUnreadTx): the
// relay's tick calls it for a recipient it judged gone.
func (s *Store) RekeepUnreadAnswers(sessionID string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	reheld, err := rekeepUnreadTx(tx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return reheld, nil
}

// recipientGone reports, inside q, whether session ended. A session the
// registry does not know is not ended.
func recipientGone(q queryer, session string) (bool, error) {
	var gone bool
	err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM peer_sessions WHERE session_id = ? AND ended_at IS NOT NULL)`, session).Scan(&gone)
	return gone, err
}

// AnswerWaitWithMessage closes the open declared wait id as answered, drops
// its emitter's declared row (when it still is this wait's) and inserts m, in one
// transaction: never an answered wait without its message, never a message
// for a closed wait. A nil m keeps the answer (message_id NULL) for the
// subject's next session (D29-C, DeliverKeptAnswer). m's recipient ended →
// ErrRecipientGone, nothing written (C3). ErrNotFound (unknown id),
// ErrWaitAuto (an automatic wait), ErrWaitClosed.
func (s *Store) AnswerWaitWithMessage(id string, answer []byte, at time.Time, m *pigtail.Message) (*WaitRecord, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if m != nil {
		gone, err := recipientGone(tx, m.ToSession)
		if err != nil {
			return nil, err
		}
		if gone {
			return nil, ErrRecipientGone
		}
	}
	var msgID, to sql.NullString
	var deliveredAt sql.NullInt64
	if m != nil {
		msgID = sql.NullString{String: m.ID, Valid: true}
		to = sql.NullString{String: m.ToSession, Valid: true}
		deliveredAt = sql.NullInt64{Int64: at.UnixMilli(), Valid: true}
	}
	res, err := tx.Exec(`UPDATE wait_records SET state = 'answered', closed_at = ?, closed_by = NULL, answer = ?,
		message_id = ?, delivered_to = ?, delivered_at = ?
		WHERE id = ? AND state = 'open' AND source = 'declared'`, at.UnixMilli(), string(answer), msgID, to, deliveredAt, id)
	if err != nil {
		return nil, err
	}
	if k, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if k == 0 {
		return nil, whyNotOpen(tx, id, true)
	}
	r, err := getWaitRecord(tx, id)
	if err != nil {
		return nil, err
	}
	// Only the row this wait is: after the emitter's closing the row reads
	// done or wrapped, a pill the answer leaves alone.
	if err := invalidateEndStampsTx(tx, r.SessionID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM session_waits WHERE session_id = ? AND source = 'declared' AND type = ?`, r.SessionID, r.Type); err != nil {
		return nil, err
	}
	if m != nil {
		if err := insertMessage(tx, m); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

// KeptAnswers returns the answered waits of a subject whose answer has not
// gone yet (D29-C), oldest answer first.
func (s *Store) KeptAnswers() ([]WaitRecord, error) {
	return s.listWaitRecords(`SELECT ` + waitRecordColumns + ` FROM wait_records
		WHERE state = 'answered' AND message_id IS NULL AND subject_id <> '' ORDER BY closed_at, id`)
}

// DeliverKeptAnswer sends the kept answer id as m, in one transaction: m's
// recipient ended → ErrRecipientGone; the answer already went (or the
// record is not a kept answer) → ErrWaitDelivered, or ErrNotFound; any error
// rolls back — delivered once, never twice.
func (s *Store) DeliverKeptAnswer(id string, m *pigtail.Message, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	gone, err := recipientGone(tx, m.ToSession)
	if err != nil {
		return err
	}
	if gone {
		return ErrRecipientGone
	}
	res, err := tx.Exec(`UPDATE wait_records SET message_id = ?, delivered_to = ?, delivered_at = ?
		WHERE id = ? AND state = 'answered' AND message_id IS NULL`, m.ID, m.ToSession, at.UnixMilli(), id)
	if err != nil {
		return err
	}
	if k, err := res.RowsAffected(); err != nil {
		return err
	} else if k == 0 {
		if _, err := getWaitRecord(tx, id); err != nil {
			return err
		}
		return ErrWaitDelivered
	}
	if err := insertMessage(tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

// whyNotOpen is the error of a write that found no open record id:
// ErrNotFound, ErrWaitAuto (when refuseAuto and the record is automatic),
// else ErrWaitClosed.
func whyNotOpen(q queryer, id string, refuseAuto bool) error {
	var src string
	err := q.QueryRow(`SELECT source FROM wait_records WHERE id = ?`, id).Scan(&src)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	case refuseAuto && src == pigtail.WaitSourceAuto:
		return ErrWaitAuto
	}
	return ErrWaitClosed
}

// SnoozeWait hides the open wait id from ListOpenWaitRecords until until.
// ErrNotFound, ErrWaitClosed.
func (s *Store) SnoozeWait(id string, until time.Time) (*WaitRecord, error) {
	res, err := s.db.Exec(`UPDATE wait_records SET snoozed_until = ? WHERE id = ? AND state = 'open'`, until.UnixMilli(), id)
	if err != nil {
		return nil, err
	}
	if k, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if k == 0 {
		return nil, whyNotOpen(s.db, id, false)
	}
	return getWaitRecord(s.db, id)
}

// PurgeClosedWaitRecords deletes the records closed before before and
// returns how many went, and the ids of the kept answers it purged before
// any session of their subject ran (C8); an open record is never purged.
func (s *Store) PurgeClosedWaitRecords(before time.Time) (purged int, neverDelivered []string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT id FROM wait_records WHERE state = 'answered' AND message_id IS NULL AND subject_id <> ''
		AND closed_at < ? ORDER BY closed_at, id`, before.UnixMilli())
	if err != nil {
		return 0, nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, nil, err
		}
		neverDelivered = append(neverDelivered, id)
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	res, err := tx.Exec(`DELETE FROM wait_records WHERE state <> 'open' AND closed_at < ?`, before.UnixMilli())
	if err != nil {
		return 0, nil, err
	}
	k, err := res.RowsAffected()
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return int(k), neverDelivered, nil
}

const waitRecordColumns = `id, session_id, source, scope, subject_id, type, reason, details, state,
	opened_at, closed_at, snoozed_until, answer, owner, message_id, delivered_to, delivered_at`

// GetWaitRecord returns one record, or ErrNotFound.
func (s *Store) GetWaitRecord(id string) (*WaitRecord, error) {
	return getWaitRecord(s.db, id)
}

func getWaitRecord(q queryer, id string) (*WaitRecord, error) {
	return scanWaitRecord(q.QueryRow(`SELECT `+waitRecordColumns+` FROM wait_records WHERE id = ?`, id))
}

// ListOpenWaitRecords returns the open records matching f, oldest first.
func (s *Store) ListOpenWaitRecords(f WaitRecordFilter) ([]WaitRecord, error) {
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	q, args := waitFilterSQL(`SELECT `+waitRecordColumns+` FROM wait_records WHERE state = 'open'`, nil, f)
	if !f.IncludeSnoozed {
		q += ` AND (snoozed_until IS NULL OR snoozed_until <= ?)`
		args = append(args, now.UnixMilli())
	}
	q += ` ORDER BY opened_at, id`
	return s.listWaitRecords(q, args...)
}

// ListRecentClosedWaitRecords returns up to n (at most
// MaxClosedWaitHistory) closed records matching f — answered, cleared or
// replaced, with their answer — the most recently closed first.
func (s *Store) ListRecentClosedWaitRecords(f WaitRecordFilter, n int) ([]WaitRecord, error) {
	if n <= 0 {
		return nil, nil
	}
	if n > MaxClosedWaitHistory {
		n = MaxClosedWaitHistory
	}
	q, args := waitFilterSQL(`SELECT `+waitRecordColumns+` FROM wait_records WHERE state <> 'open'`, nil, f)
	q += ` ORDER BY closed_at DESC, opened_at DESC, rowid DESC LIMIT ?`
	return s.listWaitRecords(q, append(args, n)...)
}

func (s *Store) listWaitRecords(q string, args ...any) ([]WaitRecord, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WaitRecord
	for rows.Next() {
		r, err := scanWaitRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// OpenWaitRecordIDs returns the ids of the session's open records (one per
// source at most), auto before declared.
func (s *Store) OpenWaitRecordIDs(sessionID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM wait_records WHERE session_id = ? AND state = 'open' ORDER BY source`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func scanWaitRecord(r rowScanner) (*WaitRecord, error) {
	var (
		w                                   WaitRecord
		details, answer, msgID, deliveredTo sql.NullString
		opened                              int64
		closedAt, snoozedUntil, deliveredAt sql.NullInt64
	)
	err := r.Scan(&w.ID, &w.SessionID, &w.Source, &w.Scope, &w.SubjectID, &w.Type, &w.Reason, &details, &w.State,
		&opened, &closedAt, &snoozedUntil, &answer, &w.Owner, &msgID, &deliveredTo, &deliveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w.OpenedAt = time.UnixMilli(opened).UTC()
	if closedAt.Valid {
		t := time.UnixMilli(closedAt.Int64).UTC()
		w.ClosedAt = &t
	}
	if snoozedUntil.Valid {
		t := time.UnixMilli(snoozedUntil.Int64).UTC()
		w.SnoozedUntil = &t
	}
	w.MessageID = msgID.String
	w.DeliveredTo = deliveredTo.String
	if deliveredAt.Valid {
		t := time.UnixMilli(deliveredAt.Int64).UTC()
		w.DeliveredAt = &t
	}
	if details.Valid && details.String != "" {
		var d pigtail.WaitDetails
		if err := json.Unmarshal([]byte(details.String), &d); err != nil {
			return nil, fmt.Errorf("store: wait %s: details: %w", w.ID, err)
		}
		d.Normalize(w.Type) // a row written before the envelope (D28 c)
		w.Details = &d
	}
	if answer.Valid && answer.String != "" {
		var a pigtail.WaitAnswer
		if err := json.Unmarshal([]byte(answer.String), &a); err != nil {
			return nil, fmt.Errorf("store: wait %s: answer: %w", w.ID, err)
		}
		a.Normalize(w.Type)
		w.Answer = &a
	}
	return &w, nil
}

// RestoreEndedWaits gives a session back the declared wait a session end took
// from it, when the same session id registers again (a cmux restart resumes
// `claude --resume <id>`; nothing else restored the wait, so the idle pause
// typed the done command into a session D1 holds). Declared source only: an auto dialog
// died with its process, a held wait is tied to its pid. In one transaction,
// on the session's latest declared record:
//   - cleared by a session end (closed_by) and not done, wrapped or working: the same row
//     reopens (closed_by back to NULL) and its session_waits row returns, its
//     since the record's opened_at — unless a session_waits row or another
//     open record of the owner exists (the session or its subject took over);
//   - still open, kept by D29-A at a session end (its subject owns it,
//     closed_by = session_end) with no session_waits row: the row alone
//     returns and closed_by goes back to NULL. An open record with closed_by
//     NULL (the done command's keep=subject clear) is left alone;
//   - anything else: nothing. A reopened record is no longer "closed by a
//     session end", so a second call restores nothing.
func (s *Store) RestoreEndedWaits(sessionID string) ([]pigtail.SessionWait, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var (
		id, owner, typ, reason, state string
		opened                        int64
		by                            sql.NullString
	)
	err = tx.QueryRow(`SELECT id, owner, type, reason, state, opened_at, closed_by FROM wait_records
		WHERE session_id = ? AND source = ? ORDER BY opened_at DESC, rowid DESC LIMIT 1`,
		sessionID, pigtail.WaitSourceDeclared).Scan(&id, &owner, &typ, &reason, &state, &opened, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// D7 (the user): `working` is never restored — the background job it declared
	// died with the restart, so the session is closable after an idle hour.
	if typ == "done" || typ == "wrapped" || typ == "working" {
		return nil, nil
	}
	reopen := state == pigtail.WaitStateCleared && by.Valid && by.String == closedBySessionEnd
	keptAtEnd := state == pigtail.WaitStateOpen && by.Valid && by.String == closedBySessionEnd
	if !reopen && !keptAtEnd {
		return nil, nil
	}
	var rows int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM session_waits WHERE session_id = ? AND source = ?`,
		sessionID, pigtail.WaitSourceDeclared).Scan(&rows); err != nil {
		return nil, err
	}
	if rows > 0 {
		return nil, nil
	}
	if reopen {
		var other int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM wait_records WHERE owner = ? AND source = ? AND state = 'open'`,
			owner, pigtail.WaitSourceDeclared).Scan(&other); err != nil {
			return nil, err
		}
		if other > 0 {
			return nil, nil
		}
		if _, err := tx.Exec(`UPDATE wait_records SET state = 'open', closed_at = NULL, closed_by = NULL WHERE id = ?`, id); err != nil {
			return nil, err
		}
	} else if _, err := tx.Exec(`UPDATE wait_records SET closed_by = NULL WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO session_waits (session_id, source, type, reason, since) VALUES (?, ?, ?, ?, ?)`,
		sessionID, pigtail.WaitSourceDeclared, typ, reason, opened); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return []pigtail.SessionWait{{
		Type: typ, Reason: reason, Source: pigtail.WaitSourceDeclared, Since: time.UnixMilli(opened).UTC(),
	}}, nil
}

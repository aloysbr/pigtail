package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Session waits (migration 0013) -------------------------------------------
//
// What a Claude session waits for: one automatic, one declared and one held row at
// most per session (primary key session_id + source). The rows say what was told;
// whether a pill shows is the sidebar loop's call, from the native status.

// SetSessionWait records w for the session, replacing the previous wait of
// the same source (the last one told wins). It is SetSessionWaitRecord
// without meta.
func (s *Store) SetSessionWait(sessionID string, w pigtail.SessionWait) error {
	_, err := s.SetSessionWaitRecord(sessionID, w, WaitMeta{})
	return err
}

// SetSessionWaitRecord records w for the session like SetSessionWait and, in
// the same transaction, moves its wait records (migration 0027): a declared
// wait, or the auto wait of a dialog (permission, question), replaces the
// open record of its source and opens its own; the same dialog told again
// (same type and reason) keeps its record, its details replaced only by new
// ones; any other auto wait replaces the open auto record and opens none. The
// open record of the other source is never touched.
func (s *Store) SetSessionWaitRecord(sessionID string, w pigtail.SessionWait, meta WaitMeta) (WaitTransition, error) {
	if sessionID == "" {
		return WaitTransition{}, fmt.Errorf("store: session wait without session id")
	}
	if w.Source != pigtail.WaitSourceAuto && w.Source != pigtail.WaitSourceDeclared && w.Source != pigtail.WaitSourceHeld {
		return WaitTransition{}, fmt.Errorf("store: unknown wait source %q", w.Source)
	}
	if w.Source == pigtail.WaitSourceHeld && w.HolderPID <= 0 {
		return WaitTransition{}, fmt.Errorf("store: held wait without holder pid")
	}
	var holder sql.NullInt64 // migration 0019: the process that holds a held wait
	if w.Source == pigtail.WaitSourceHeld {
		holder = sql.NullInt64{Int64: int64(w.HolderPID), Valid: true}
	}
	if w.Since.IsZero() {
		w.Since = time.Now().UTC()
	}
	var until sql.NullInt64 // migration 0016: when the wait ends on its own
	if w.Until != nil {
		until = sql.NullInt64{Int64: w.Until.UnixMilli(), Valid: true}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return WaitTransition{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := invalidateEndStampsTx(tx, sessionID); err != nil {
		return WaitTransition{}, err
	}
	if _, err := tx.Exec(`
INSERT INTO session_waits (session_id, source, type, reason, since, until_ms, holder_pid) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, source) DO UPDATE SET
	type = excluded.type, reason = excluded.reason, since = excluded.since, until_ms = excluded.until_ms,
	holder_pid = excluded.holder_pid`,
		sessionID, w.Source, w.Type, w.Reason, w.Since.UnixMilli(), until, holder); err != nil {
		return WaitTransition{}, err
	}
	if w.Source == pigtail.WaitSourceDeclared {
		// migration 0017 (D12): the declaration's time outlives the wait row,
		// which the loop may clear before the turn's Stop.
		if _, err := tx.Exec(`
INSERT INTO session_last_declared (session_id, at) VALUES (?, ?)
ON CONFLICT(session_id) DO UPDATE SET at = excluded.at`, sessionID, w.Since.UnixMilli()); err != nil {
			return WaitTransition{}, err
		}
	}
	tr, err := setWaitRecord(tx, sessionID, w, meta)
	if err != nil {
		return WaitTransition{}, err
	}
	if err := tx.Commit(); err != nil {
		return WaitTransition{}, err
	}
	return tr, nil
}

// SetAutoWaitReason rewrites the reason of the session's automatic wait of
// type typ told at since, and nothing else (not since, not until, no wait
// record). True when a row changed: a declared wait, another type or another
// since changes nothing.
func (s *Store) SetAutoWaitReason(sessionID, typ string, since time.Time, reason string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := invalidateEndStampsTx(tx, sessionID); err != nil {
		return false, err
	}
	res, err := tx.Exec(`UPDATE session_waits SET reason = ? WHERE session_id = ? AND source = 'auto' AND type = ? AND since = ?`,
		reason, sessionID, typ, since.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// ClearSessionWaits removes the session's waits of the given sources (none =
// the turn's waits: auto and declared; a held wait is never a turn's) and
// returns how many rows went. The open record of each declared or auto row
// that went is closed as cleared (ClearSessionWaitsClosing).
func (s *Store) ClearSessionWaits(sessionID string, sources ...string) (int, error) {
	n, _, _, err := s.ClearSessionWaitsClosing(sessionID, false, sources...)
	return n, err
}

// ClearGoneSessionWaits is ClearSessionWaits for a session that is gone
// (ended, or its pid dead: the sidebar loop's fallbacks): its rows go, the
// wait its subject owns stays open (D29-A), and an answer to its subject's
// wait it was handed but never read is kept again (D29-C).
func (s *Store) ClearGoneSessionWaits(sessionID string, sources ...string) (int, error) {
	n, _, _, err := s.ClearSessionWaitsClosing(sessionID, true, sources...)
	return n, err
}

// ClearHeldWait removes the session's held wait only if holderPID still holds
// it (a conditional delete: a newer holder's row is left alone). Reports
// whether a row went.
func (s *Store) ClearHeldWait(sessionID string, holderPID int) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM session_waits WHERE session_id = ? AND source = 'held' AND holder_pid = ?`,
		sessionID, holderPID)
	if err != nil {
		return false, err
	}
	k, err := res.RowsAffected()
	return k > 0, err
}

// SessionWaits returns the session's waits, auto before declared before held.
func (s *Store) SessionWaits(sessionID string) ([]pigtail.SessionWait, error) {
	all, err := s.querySessionWaits(sessionWaitsSelect+`
		WHERE w.session_id = ? ORDER BY w.session_id, w.source`, sessionID)
	if err != nil {
		return nil, err
	}
	return all[sessionID], nil
}

// ListSessionWaits returns every recorded wait, by session.
func (s *Store) ListSessionWaits() (map[string][]pigtail.SessionWait, error) {
	return s.querySessionWaits(sessionWaitsSelect + `
		ORDER BY w.session_id, w.source`)
}

// sessionWaitsSelect reads the wait rows with the id of their open record
// (migration 0027): the declared row's, and the auto row's of a dialog.
const sessionWaitsSelect = `SELECT w.session_id, w.source, w.type, w.reason, w.since, w.until_ms, w.holder_pid, COALESCE(r.id, '')
	FROM session_waits w
	LEFT JOIN wait_records r ON r.session_id = w.session_id AND r.source = w.source AND r.state = 'open'`

func (s *Store) querySessionWaits(query string, args ...any) (map[string][]pigtail.SessionWait, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]pigtail.SessionWait{}
	for rows.Next() {
		var (
			id    string
			w     pigtail.SessionWait
			since int64
			until sql.NullInt64
			pid   sql.NullInt64
		)
		if err := rows.Scan(&id, &w.Source, &w.Type, &w.Reason, &since, &until, &pid, &w.ID); err != nil {
			return nil, err
		}
		w.Since = time.UnixMilli(since).UTC()
		if until.Valid {
			u := time.UnixMilli(until.Int64).UTC()
			w.Until = &u
		}
		w.HolderPID = int(pid.Int64)
		out[id] = append(out[id], w)
	}
	return out, rows.Err()
}

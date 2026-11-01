package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Session Standby (migration 0023, sub_x000000000000041) -----------------
//
// One row per session parked by /standby. The row outlives SessionEnd (D24: a
// relaunch resumes the same session id); it goes only by ClearStandby (a
// prompt the user types, `pigtail session standby --clear`), when another
// conversation takes the session's surface, or when its workspace is gone.
// The daemon reads it through one predicate only (handlers.inStandby).

const standbyColumns = `session_id, since, compact_sent_at, compact_status_at, compact_pid, compact_tries,
	compacted_at, compact_failed_at, missing_ticks`

// SetStandby puts the session in Standby since `since` (EnterStandby).
func (s *Store) SetStandby(session string, since time.Time) error {
	return s.EnterStandby(session, since)
}

// EnterStandby puts the session in Standby since `since`. On a row already
// there it starts over: no compaction state, no missing tick; the time already
// spent in Standby is given back to the pending messages first (D28), so none
// is counted twice nor lost. The prio is cmux's pin now (D3 of
// sub_x000000000000014): the CLI unpins before calling, the store has no Prio.
func (s *Store) EnterStandby(session string, since time.Time) error {
	if session == "" {
		return fmt.Errorf("store: standby without session id")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := resumeExpiryTx(tx, session, since); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO session_standby (session_id, since) VALUES (?, ?)
ON CONFLICT(session_id) DO UPDATE SET
	since = excluded.since, compact_sent_at = NULL, compact_status_at = 0, compact_pid = 0,
	compact_tries = 0, compacted_at = NULL, compact_failed_at = NULL, missing_ticks = 0`,
		session, since.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearStandby ends the session's Standby and reports whether a row went.
// now is when it ends: the pending messages' countdown resumes from it
// (D28).
func (s *Store) ClearStandby(session string, now time.Time) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := resumeExpiryTx(tx, session, now); err != nil {
		return false, err
	}
	res, err := tx.Exec(`DELETE FROM session_standby WHERE session_id = ?`, session)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// resumeExpiryTx gives the pending messages of session the time its Standby
// lasted (D28): each expiry moves by now − max(since, created_at), `since`
// read in the same transaction. A message received before the Standby finds
// the time it had left at its start; one received during it, its whole TTL. No
// row, nothing to give back. Called by every exit from Standby, and by a
// SetStandby on a row already there.
func resumeExpiryTx(tx *sql.Tx, session string, now time.Time) error {
	var since int64
	err := tx.QueryRow(`SELECT since FROM session_standby WHERE session_id = ?`, session).Scan(&since)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE messages SET expires_at = expires_at + (? - MAX(?, created_at))
WHERE to_session = ? AND state = 'pending'`, now.UnixMilli(), since, session)
	return err
}

// EndStandbyReplacedOnSurface ends, in one transaction, the Standby of every
// session other than session registered on surface (case-insensitive): a
// different conversation took that surface (D24). It returns their ids. now
// is when they end (D28).
func (s *Store) EndStandbyReplacedOnSurface(surface, session string, now time.Time) ([]string, error) {
	if surface == "" {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`
SELECT sb.session_id FROM session_standby sb JOIN peer_sessions p ON p.session_id = sb.session_id
WHERE p.cmux_surface_id = ? COLLATE NOCASE AND sb.session_id <> ? ORDER BY sb.session_id`, surface, session)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := resumeExpiryTx(tx, id, now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM session_standby WHERE session_id = ?`, id); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}

// GetStandby returns the session's Standby row, or nil, nil when it has none.
func (s *Store) GetStandby(session string) (*pigtail.SessionStandby, error) {
	sb, err := scanStandby(s.db.QueryRow(`SELECT `+standbyColumns+` FROM session_standby WHERE session_id = ?`, session))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sb, err
}

// ListStandby returns every Standby row, by session id.
func (s *Store) ListStandby() ([]pigtail.SessionStandby, error) {
	rows, err := s.db.Query(`SELECT ` + standbyColumns + ` FROM session_standby ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.SessionStandby
	for rows.Next() {
		sb, err := scanStandby(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sb)
	}
	return out, rows.Err()
}

// ListStandbyWorkspaces returns every Standby row with the cmux workspace its
// session registered on (upper case; "" for a session never registered or
// registered without one: no proof of absence is possible then).
func (s *Store) ListStandbyWorkspaces() ([]pigtail.StandbyWorkspace, error) {
	rows, err := s.db.Query(`
SELECT sb.session_id, COALESCE(UPPER(TRIM(p.cmux_workspace_id)), ''), sb.missing_ticks
FROM session_standby sb LEFT JOIN peer_sessions p ON p.session_id = sb.session_id
ORDER BY sb.session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.StandbyWorkspace
	for rows.Next() {
		var w pigtail.StandbyWorkspace
		if err := rows.Scan(&w.SessionID, &w.CmuxWorkspaceID, &w.MissingTicks); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetStandbyMissingTicks records the successful cmux listings in a row
// without the session's workspace (the purge of a closed workspace).
func (s *Store) SetStandbyMissingTicks(session string, n int) error {
	return s.updateStandby(session, `missing_ticks = ?`, n)
}

// MarkStandbyCompactSent records a /compact typed at `at` into the claude
// process pid, with the native statusUpdatedAt read right before it, and
// counts the send.
func (s *Store) MarkStandbyCompactSent(session string, at time.Time, statusUpdatedAt int64, pid int) error {
	return s.updateStandby(session, `compact_sent_at = ?, compact_status_at = ?, compact_pid = ?, compact_tries = compact_tries + 1`,
		at.UnixMilli(), statusUpdatedAt, pid)
}

// MarkStandbyCompacted records the proof of the compaction.
func (s *Store) MarkStandbyCompacted(session string, at time.Time) error {
	return s.updateStandby(session, `compacted_at = ?`, at.UnixMilli())
}

// MarkStandbyCompactFailed records that the compaction was given up.
func (s *Store) MarkStandbyCompactFailed(session string, at time.Time) error {
	return s.updateStandby(session, `compact_failed_at = ?`, at.UnixMilli())
}

func (s *Store) updateStandby(session, set string, args ...any) error {
	res, err := s.db.Exec(`UPDATE session_standby SET `+set+` WHERE session_id = ?`, append(args, session)...) // #nosec G202 -- set is a constant of this file
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanStandby(r rowScanner) (*pigtail.SessionStandby, error) {
	var (
		sb                      pigtail.SessionStandby
		since                   int64
		sent, compacted, failed sql.NullInt64
	)
	if err := r.Scan(&sb.SessionID, &since, &sent, &sb.CompactStatusAt, &sb.CompactPID, &sb.CompactTries,
		&compacted, &failed, &sb.MissingTicks); err != nil {
		return nil, err
	}
	sb.Since = time.UnixMilli(since).UTC()
	sb.CompactSentAt = standbyTime(sent)
	sb.CompactedAt = standbyTime(compacted)
	sb.CompactFailedAt = standbyTime(failed)
	return &sb, nil
}

func standbyTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

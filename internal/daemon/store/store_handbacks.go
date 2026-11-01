package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Turn ends by skill (migration 0017, D11 / D12) ----------------------------
//
// The last skill a session invoked, and one row per turn end keyed by the
// transcript's promptId. A turn counts as handed back without a declaration
// when it neither failed (StopFailure) nor saw a `session wait` since it began.
// A turn ended while the session is in Standby (its session_standby row,
// sub_x000000000000041) is not one: /standby hands back without a wait.

// SetSessionSkill records skill as the session's last one.
func (s *Store) SetSessionSkill(sessionID, skill string, at time.Time) error {
	if sessionID == "" || skill == "" {
		return fmt.Errorf("store: session skill without session or skill")
	}
	_, err := s.db.Exec(`
INSERT INTO session_last_skill (session_id, skill, at) VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET skill = excluded.skill, at = excluded.at`,
		sessionID, skill, at.UnixMilli())
	return err
}

// RecordTurnEnd records the end of turn turnID of the session. A failure
// creates the row or marks it failed; a stop creates it (with the session's
// last skill and whether it declared a wait since turnStart) and never
// overwrites one: two Stops of a turn count once, and the Stop/StopFailure
// order does not matter.
func (s *Store) RecordTurnEnd(sessionID, sc, turnID string, turnStart, at time.Time, event string) error {
	if sessionID == "" || turnID == "" {
		return fmt.Errorf("store: turn end without session or turn")
	}
	switch event {
	case pigtail.TurnEventFailure:
		_, err := s.db.Exec(`
INSERT INTO session_turn_ends (session_id, turn_id, turn_start, at, skill, scope, declared, failed)
VALUES (?, ?, ?, ?, COALESCE((SELECT skill FROM session_last_skill WHERE session_id = ?), ''), ?, 0, 1)
ON CONFLICT(session_id, turn_id) DO UPDATE SET failed = 1`,
			sessionID, turnID, turnStart.UnixMilli(), at.UnixMilli(), sessionID, sc)
		return err
	case pigtail.TurnEventStop:
		_, err := s.db.Exec(`
INSERT INTO session_turn_ends (session_id, turn_id, turn_start, at, skill, scope, declared, failed)
VALUES (?, ?, ?, ?,
	COALESCE((SELECT skill FROM session_last_skill WHERE session_id = ?), ''), ?,
	EXISTS(SELECT 1 FROM session_last_declared WHERE session_id = ? AND at >= ?)
		OR EXISTS(SELECT 1 FROM session_standby WHERE session_id = ?), 0)
ON CONFLICT(session_id, turn_id) DO NOTHING`,
			sessionID, turnID, turnStart.UnixMilli(), at.UnixMilli(), sessionID, sc, sessionID, turnStart.UnixMilli(), sessionID)
		return err
	}
	return fmt.Errorf("store: unknown turn event %q", event)
}

// DeclaredSince tells whether the session declared a wait (`session wait`)
// at or after t: the same test RecordTurnEnd makes for a turn begun at t.
func (s *Store) DeclaredSince(sessionID string, t time.Time) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM session_last_declared WHERE session_id = ? AND at >= ?`,
		sessionID, t.UnixMilli()).Scan(&n)
	return n > 0, err
}

// LatestTurnEnd returns the id and end time of the session's newest turn that
// ended without failing; ok is false when there is none.
func (s *Store) LatestTurnEnd(sessionID string) (turnID string, at time.Time, ok bool, err error) {
	var ms int64
	err = s.db.QueryRow(`SELECT turn_id, at FROM session_turn_ends WHERE session_id = ? AND failed = 0 ORDER BY at DESC LIMIT 1`,
		sessionID).Scan(&turnID, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	return turnID, time.UnixMilli(ms).UTC(), true, nil
}

// TurnSucceededSince tells whether a turn of the session ended without failing
// (a Stop, no StopFailure) after t.
func (s *Store) TurnSucceededSince(sessionID string, t time.Time) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM session_turn_ends WHERE session_id = ? AND failed = 0 AND at > ?`,
		sessionID, t.UnixMilli()).Scan(&n)
	return n > 0, err
}

// HandbacksBySkill counts the turns handed back without a declaration since
// since, in one scope (scope.Any = every scope), by skill.
func (s *Store) HandbacksBySkill(since time.Time, sc scope.Scope) ([]pigtail.SkillHandbacks, error) {
	out := []pigtail.SkillHandbacks{}
	rows, err := s.db.Query(`
SELECT skill, COUNT(*), MAX(at) FROM session_turn_ends
WHERE failed = 0 AND declared = 0 AND at >= ? AND (? = '' OR scope = ?)
GROUP BY skill ORDER BY skill`, since.UnixMilli(), string(sc), string(sc))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h pigtail.SkillHandbacks
		var last int64
		if err := rows.Scan(&h.Skill, &h.Count, &last); err != nil {
			return nil, err
		}
		h.LastAt = time.UnixMilli(last).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// TurnEndStart returns the start time recorded for the session's turn turnID
// (its turn_start), ok false when the turn has no row.
func (s *Store) TurnEndStart(sessionID, turnID string) (time.Time, bool, error) {
	var ms int64
	err := s.db.QueryRow(`SELECT turn_start FROM session_turn_ends WHERE session_id = ? AND turn_id = ?`, sessionID, turnID).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.UnixMilli(ms).UTC(), true, nil
}

// SessionSkillAt returns the session's last skill and when it was set, ok false
// when none was ever recorded. The row is never cleared: callers compare the
// time with the turn they ask about.
func (s *Store) SessionSkillAt(sessionID string) (skill string, at time.Time, ok bool, err error) {
	var ms int64
	err = s.db.QueryRow(`SELECT skill, at FROM session_last_skill WHERE session_id = ?`, sessionID).Scan(&skill, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	return skill, time.UnixMilli(ms).UTC(), true, nil
}

// LatestTurnEndBefore returns when the session's latest non-failed turn ended
// strictly before t, ok false when there is none.
func (s *Store) LatestTurnEndBefore(sessionID string, t time.Time) (time.Time, bool, error) {
	var ms int64
	err := s.db.QueryRow(`SELECT at FROM session_turn_ends WHERE session_id = ? AND failed = 0 AND at < ? ORDER BY at DESC LIMIT 1`,
		sessionID, t.UnixMilli()).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.UnixMilli(ms).UTC(), true, nil
}

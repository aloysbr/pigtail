package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Session pause (migration 0032, sub_x000000000000036) -------------------
//
// One row per idle pause: pigtaild stopped the session's claude process by
// SIGTERM. Begun `stopping` right before the signal, `paused` once the
// process is gone; it goes when the same session registers alive again
// (EndPause), when another session takes its surface
// (EndPauseReplacedOnSurface), or when the pause is undone.

const pauseColumns = `session_id, pid, surface_id, cmux_workspace_id, sid_path, state, entered_standby,
	since, signaled_at, paused_at`

// BeginPause inserts the pause row (state as given, normally stopping); a
// row already there for the session is an error. The cmux workspace id is
// stored in upper case.
func (s *Store) BeginPause(p pigtail.SessionPause) error {
	if p.SessionID == "" {
		return fmt.Errorf("store: pause without session id")
	}
	state := p.State
	if state == "" {
		state = pigtail.PauseStopping
	}
	_, err := s.db.Exec(`INSERT INTO session_pause (`+pauseColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.SessionID, p.PID, p.SurfaceID, strings.ToUpper(strings.TrimSpace(p.CmuxWorkspaceID)), p.SidPath, state,
		boolInt(p.EnteredStandby), p.Since.UnixMilli(),
		pauseMillis(p.SignaledAt), pauseMillis(p.PausedAt))
	return err
}

// MarkSignaled records the SIGTERM about to be sent at `at`.
func (s *Store) MarkSignaled(session string, at time.Time) error {
	return s.updatePause(session, `signaled_at = ?`, at.UnixMilli())
}

// MarkPaused records the process gone at `at`: the row becomes paused.
func (s *Store) MarkPaused(session string, at time.Time) error {
	return s.updatePause(session, `state = 'paused', paused_at = ?`, at.UnixMilli())
}

// EndPause deletes the session's row and reports whether one went.
func (s *Store) EndPause(session string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM session_pause WHERE session_id = ?`, session)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// EndPauseReplacedOnSurface deletes, in one transaction, the pause of every
// session other than session paused on surface (case-insensitive): another
// session took that surface. It returns their ids.
func (s *Store) EndPauseReplacedOnSurface(surface, session string) ([]string, error) {
	if surface == "" {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT session_id FROM session_pause
WHERE surface_id = ? COLLATE NOCASE AND session_id <> ? ORDER BY session_id`, surface, session)
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
		if _, err := tx.Exec(`DELETE FROM session_pause WHERE session_id = ?`, id); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}

// GetPause returns the session's pause row, or nil, nil when it has none.
func (s *Store) GetPause(session string) (*pigtail.SessionPause, error) {
	p, err := scanPause(s.db.QueryRow(`SELECT `+pauseColumns+` FROM session_pause WHERE session_id = ?`, session))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// ListPauses returns every pause row, by session id.
func (s *Store) ListPauses() ([]pigtail.SessionPause, error) {
	rows, err := s.db.Query(`SELECT ` + pauseColumns + ` FROM session_pause ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.SessionPause
	for rows.Next() {
		p, err := scanPause(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) updatePause(session, set string, args ...any) error {
	res, err := s.db.Exec(`UPDATE session_pause SET `+set+` WHERE session_id = ?`, append(args, session)...) // #nosec G202 -- set is a constant of this file
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

func scanPause(r rowScanner) (*pigtail.SessionPause, error) {
	var (
		p                pigtail.SessionPause
		entered          int
		since            int64
		signaled, paused sql.NullInt64
	)
	if err := r.Scan(&p.SessionID, &p.PID, &p.SurfaceID, &p.CmuxWorkspaceID, &p.SidPath, &p.State, &entered,
		&since, &signaled, &paused); err != nil {
		return nil, err
	}
	p.EnteredStandby = entered != 0
	p.Since = time.UnixMilli(since).UTC()
	p.SignaledAt = standbyTime(signaled)
	p.PausedAt = standbyTime(paused)
	return &p, nil
}

func pauseMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

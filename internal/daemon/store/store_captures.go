package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Capture errors the handlers map to a 400 / 409.
var (
	// ErrCaptureOtherTheme: from_subject_id is not in the capture's theme.
	ErrCaptureOtherTheme = errors.New("store: capture and subject are not in the same theme")
	// ErrCapturesMixedThemes: a promote names captures of more than one theme.
	ErrCapturesMixedThemes = errors.New("store: captures to promote are not all in the same theme")
	// ErrCapturePromoted: a promoted capture is history and is never deleted.
	ErrCapturePromoted = errors.New("store: capture already promoted")
	// ErrCaptureClosed: promote needs open captures.
	ErrCaptureClosed = errors.New("store: capture is closed")
)

// CaptureFilter narrows ListCaptures. Scope uses scope.Any for "every scope".
type CaptureFilter struct {
	ThemeID  string
	OpenOnly bool
	Scope    scope.Scope
}

var captureSelect = `SELECT c.id, c.theme_id, c.text, c.from_subject_id, c.from_prev_status, c.promoted_subject_id, c.created_at, c.closed_at, COALESCE(t.scope, '` + string(scope.Master()) + `')
 FROM captures c LEFT JOIN themes t ON t.id = c.theme_id`

func scanCapture(r rowScanner) (*pigtail.Capture, error) {
	var (
		c        pigtail.Capture
		from     sql.NullString
		prev     sql.NullString
		promoted sql.NullString
		created  int64
		closed   sql.NullInt64
	)
	if err := r.Scan(&c.ID, &c.ThemeID, &c.Text, &from, &prev, &promoted, &created, &closed, &c.Scope); err != nil {
		return nil, err
	}
	c.FromSubjectID, c.FromPrevStatus, c.PromotedSubjectID = from.String, prev.String, promoted.String
	c.CreatedAt = time.UnixMilli(created).UTC()
	if closed.Valid {
		t := time.UnixMilli(closed.Int64).UTC()
		c.ClosedAt = &t
	}
	return &c, nil
}

// GetCapture returns a capture by id, or ErrNotFound.
func (s *Store) GetCapture(id string) (*pigtail.Capture, error) {
	c, err := scanCapture(s.db.QueryRow(captureSelect+` WHERE c.id = ?`, id)) //nolint:gosec // G202: constant scope literal
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ListCaptures returns the captures matching f, newest first.
func (s *Store) ListCaptures(f CaptureFilter) ([]pigtail.Capture, error) {
	q := captureSelect + ` WHERE 1=1` //nolint:gosec // G202: constant scope literal
	var args []any
	if f.ThemeID != "" {
		q += ` AND c.theme_id = ?`
		args = append(args, f.ThemeID)
	}
	if f.OpenOnly {
		q += ` AND c.closed_at IS NULL`
	}
	if f.Scope != "" && f.Scope != scope.Any {
		q += ` AND COALESCE(t.scope, '` + string(scope.Master()) + `') = ?`
		args = append(args, string(f.Scope))
	}
	q += ` ORDER BY c.created_at DESC, c.id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pigtail.Capture{}
	for rows.Next() {
		c, err := scanCapture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CountOpenCapturesByTheme counts the captures of a theme not yet promoted.
func (s *Store) CountOpenCapturesByTheme(themeID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM captures WHERE theme_id = ? AND closed_at IS NULL`, themeID).Scan(&n)
	return n, err
}

// AddCapture inserts c. With c.FromSubjectID set it also — in one transaction —
// records the subject's previous status, moves it to "captured" and writes the
// subject_log line (D2): either everything lands or nothing does. logID mints
// that line's id.
func (s *Store) AddCapture(c *pigtail.Capture, logID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var prev sql.NullString
	if c.FromSubjectID != "" {
		var theme, status string
		err := tx.QueryRow(`SELECT theme_id, status FROM subjects WHERE id = ?`, c.FromSubjectID).Scan(&theme, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if theme != c.ThemeID {
			return ErrCaptureOtherTheme
		}
		c.FromPrevStatus = status
		prev = sql.NullString{String: status, Valid: true}
	}
	if _, err := tx.Exec(
		`INSERT INTO captures (id, theme_id, text, from_subject_id, from_prev_status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.ThemeID, c.Text, nullStr(c.FromSubjectID), prev, c.CreatedAt.UnixMilli(),
	); err != nil {
		return err
	}
	if c.FromSubjectID != "" {
		now := c.CreatedAt.UnixMilli()
		if _, err := tx.Exec(`UPDATE subjects SET status = ?, updated_at = ? WHERE id = ?`, pigtail.StatusCaptured, now, c.FromSubjectID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO subject_log (id, subject_id, ts, kind, text) VALUES (?, ?, ?, ?, ?)`,
			logID, c.FromSubjectID, now, pigtail.LogStatus, prev.String+" → "+pigtail.StatusCaptured+" · "+c.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PromoteCaptures creates sub from the open captures ids, all of one theme
// (sub.ThemeID), and closes them: promoted_subject_id and closed_at are set.
// It returns the updated captures.
func (s *Store) PromoteCaptures(ids []string, sub *pigtail.Subject) ([]pigtail.Capture, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("store: no capture to promote")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		var theme string
		var closed sql.NullInt64
		err := tx.QueryRow(`SELECT theme_id, closed_at FROM captures WHERE id = ?`, id).Scan(&theme, &closed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if theme != sub.ThemeID {
			return nil, ErrCapturesMixedThemes
		}
		if closed.Valid {
			return nil, ErrCaptureClosed
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at, source, short_title) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sub.ID, sub.ThemeID, sub.Title, sub.Slug, sub.Status, sub.CreatedAt.UnixMilli(), sub.UpdatedAt.UnixMilli(), sub.Source, nullStr(sub.ShortTitle),
	); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE captures SET promoted_subject_id = ?, closed_at = ? WHERE id = ?`, sub.ID, sub.CreatedAt.UnixMilli(), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]pigtail.Capture, 0, len(ids))
	for _, id := range ids {
		c, err := s.GetCapture(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

// DeleteCapture removes a capture (undo, C13). A promoted capture is refused
// (ErrCapturePromoted). When it replaced a subject that is still "captured",
// the subject gets its from_prev_status back in the same transaction, with a
// subject_log line; the restored subject is returned. A subject changed since
// is left alone (nil subject). logID mints the log line's id.
func (s *Store) DeleteCapture(id, logID string) (*pigtail.Capture, *pigtail.Subject, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := scanCapture(tx.QueryRow(captureSelect+` WHERE c.id = ?`, id)) //nolint:gosec // G202: constant scope literal
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if c.PromotedSubjectID != "" {
		return nil, nil, ErrCapturePromoted
	}
	restored := false
	if c.FromSubjectID != "" && c.FromPrevStatus != "" {
		var status string
		err := tx.QueryRow(`SELECT status FROM subjects WHERE id = ?`, c.FromSubjectID).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		if err == nil && status == pigtail.StatusCaptured {
			now := time.Now().UTC().UnixMilli()
			if _, err := tx.Exec(`UPDATE subjects SET status = ?, updated_at = ? WHERE id = ?`, c.FromPrevStatus, now, c.FromSubjectID); err != nil {
				return nil, nil, err
			}
			if _, err := tx.Exec(`INSERT INTO subject_log (id, subject_id, ts, kind, text) VALUES (?, ?, ?, ?, ?)`,
				logID, c.FromSubjectID, now, pigtail.LogStatus, pigtail.StatusCaptured+" → "+c.FromPrevStatus+" · restored"); err != nil {
				return nil, nil, err
			}
			restored = true
		}
	}
	if _, err := tx.Exec(`DELETE FROM captures WHERE id = ?`, id); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	if !restored {
		return c, nil, nil
	}
	sub, err := s.GetSubject(c.FromSubjectID)
	if err != nil {
		return c, nil, nil
	}
	return c, sub, nil
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/staticsrv"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// OutputFilter narrows ListSubjectOutputs. Limit <= 0 → pigtail.OutputListDefault.
type OutputFilter struct {
	SinceSeq  int64
	Kind      string
	SessionID string
	Limit     int
}

const subjectOutputCols = `seq, id, subject_id, session_id, kind, path, url, key, title, ts, step, meta, purged_at, created_at`

func scanSubjectOutput(r rowScanner) (pigtail.SubjectOutput, error) {
	var (
		o                              pigtail.SubjectOutput
		session, path, url, step, meta sql.NullString
		purged                         sql.NullInt64
	)
	if err := r.Scan(&o.Seq, &o.ID, &o.SubjectID, &session, &o.Kind, &path, &url, &o.Key, &o.Title, &o.TS, &step, &meta, &purged, &o.CreatedAt); err != nil {
		return o, err
	}
	o.SessionID, o.Path, o.URL, o.Step = session.String, path.String, url.String, step.String
	if meta.Valid && meta.String != "" {
		o.Meta = json.RawMessage(meta.String)
	}
	if purged.Valid {
		v := purged.Int64
		o.PurgedAt = &v
	}
	return o, nil
}

func (s *Store) getSubjectOutput(ctx context.Context, subjectID, key string) (pigtail.SubjectOutput, error) {
	o, err := scanSubjectOutput(s.db.QueryRowContext(ctx, `SELECT `+subjectOutputCols+` FROM subject_outputs WHERE subject_id = ? AND key = ?`, subjectID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

// AddSubjectOutput inserts o (ID and Key already set by Normalize) unless a
// row with the same (subject_id, key) exists; it returns the stored row and
// whether it was created. The first writer wins: a re-add changes nothing.
func (s *Store) AddSubjectOutput(ctx context.Context, o pigtail.SubjectOutput) (pigtail.SubjectOutput, bool, error) {
	if o.ID == "" || o.Key == "" || o.SubjectID == "" {
		return o, false, errors.New("store: subject output needs subject_id, id and key")
	}
	if o.CreatedAt == 0 {
		o.CreatedAt = time.Now().UnixMilli()
	}
	var meta any
	if len(o.Meta) > 0 {
		meta = string(o.Meta)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO subject_outputs (id, subject_id, session_id, kind, path, url, key, title, ts, step, meta, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(subject_id, key) DO NOTHING`,
		o.ID, o.SubjectID, nullStr(o.SessionID), o.Kind, nullStr(o.Path), nullStr(o.URL), o.Key, o.Title, o.TS, nullStr(o.Step), meta, o.CreatedAt)
	if err != nil {
		return o, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return o, false, err
	}
	stored, err := s.getSubjectOutput(ctx, o.SubjectID, o.Key)
	if err != nil {
		return o, false, fmt.Errorf("store: subject output %s/%s neither inserted nor found: %w", o.SubjectID, o.Key, err)
	}
	return stored, n == 1, nil
}

// ListSubjectOutputs returns the subject's outputs, purged ones included, in
// seq ASC order, never nil.
func (s *Store) ListSubjectOutputs(ctx context.Context, subjectID string, f OutputFilter) ([]pigtail.SubjectOutput, error) {
	q := `SELECT ` + subjectOutputCols + ` FROM subject_outputs WHERE subject_id = ? AND seq > ?`
	args := []any{subjectID, f.SinceSeq}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.SessionID != "" {
		q += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = pigtail.OutputListDefault
	}
	q += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pigtail.SubjectOutput{}
	for rows.Next() {
		o, err := scanSubjectOutput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PurgeSubjectOutput sets purged_at to atMs if it is not set yet (the first
// date is kept) and returns the row; ErrNotFound for an unknown key.
func (s *Store) PurgeSubjectOutput(ctx context.Context, subjectID, key string, atMs int64) (pigtail.SubjectOutput, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE subject_outputs SET purged_at = ? WHERE subject_id = ? AND key = ? AND purged_at IS NULL`, atMs, subjectID, key); err != nil {
		return pigtail.SubjectOutput{}, err
	}
	return s.getSubjectOutput(ctx, subjectID, key)
}

// SubjectOutputStats counts the subject's non-purged outputs and returns the
// max ts among them (epoch ms, 0 when none).
func (s *Store) SubjectOutputStats(ctx context.Context, subjectID string) (int, int64, error) {
	var (
		n    int
		last int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(ts), 0) FROM subject_outputs WHERE subject_id = ? AND purged_at IS NULL`, subjectID).Scan(&n, &last)
	return n, last, err
}

// ServableOutputs lists, every scope included, the non-purged outputs with a
// path, for the outputs server (staticsrv.NewOutputsHandler). The fingerprint
// moves on every add (MAX(seq)) and every purge (the purged count): one cheap
// query the server runs at each request.
func (s *Store) ServableOutputs() (string, []staticsrv.ServedEntry, error) {
	var maxSeq, purged int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0), COUNT(purged_at) FROM subject_outputs`).Scan(&maxSeq, &purged); err != nil {
		return "", nil, err
	}
	fp := fmt.Sprintf("%d:%d", maxSeq, purged)
	rows, err := s.db.Query(`SELECT kind, path FROM subject_outputs WHERE purged_at IS NULL AND path IS NOT NULL AND path != '' ORDER BY seq ASC`)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	out := []staticsrv.ServedEntry{}
	for rows.Next() {
		var e staticsrv.ServedEntry
		if err := rows.Scan(&e.Kind, &e.Path); err != nil {
			return "", nil, err
		}
		out = append(out, e)
	}
	return fp, out, rows.Err()
}

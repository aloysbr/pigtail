package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ErrFindPicked: the search was already picked for another subject.
var ErrFindPicked = errors.New("store: subject find already picked for another subject")

const findColumns = `id, scope, ts, query, candidates, cmux_workspace_id, picked_subject_id, picked_at`

// CreateSubjectFind writes a search and its journal line j (nil = none) in one
// transaction: both or neither.
func (s *Store) CreateSubjectFind(f *pigtail.SubjectFind, j *pigtail.JournalEntry) error {
	hits := f.Candidates
	if hits == nil {
		hits = []pigtail.SubjectFindHit{}
	}
	cands, err := json.Marshal(hits)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO subject_finds (id, scope, ts, query, candidates, cmux_workspace_id) VALUES (?, ?, ?, ?, ?, ?)`,
		f.ID, f.Scope, f.Timestamp.UnixMilli(), f.Query, string(cands), f.CmuxWorkspaceID); err != nil {
		return err
	}
	if j != nil {
		if _, err := addJournalTx(tx, j); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSubjectFind returns one search, or ErrNotFound.
func (s *Store) GetSubjectFind(id string) (*pigtail.SubjectFind, error) {
	return scanSubjectFind(s.db.QueryRow(`SELECT `+findColumns+` FROM subject_finds WHERE id = ?`, id))
}

// PickSubjectFind records subjectID as the subject search id led to, at at,
// with the journal line journal builds, in one transaction. The same pick
// again changes nothing (Created false, no line, the first PickedAt); another
// subject is ErrFindPicked; an unknown id is ErrNotFound.
func (s *Store) PickSubjectFind(id, subjectID string, at time.Time,
	journal func(pigtail.SubjectFindPick, *pigtail.SubjectFind) *pigtail.JournalEntry) (*pigtail.SubjectFindPick, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	f, err := scanSubjectFind(tx.QueryRow(`SELECT `+findColumns+` FROM subject_finds WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	switch {
	case f.PickedSubjectID == subjectID:
		at = f.PickedAt
	case f.PickedSubjectID != "":
		return nil, ErrFindPicked
	}
	p := pigtail.SubjectFindPick{FindID: id, SubjectID: subjectID, Candidates: len(f.Candidates), Searches: 1, PickedAt: at.UTC()}
	for i, h := range f.Candidates {
		if h.SubjectID == subjectID {
			p.Rank = i + 1
			break
		}
	}
	if f.CmuxWorkspaceID != "" {
		// The searches of this workspace after its previous pick (made before
		// this one), up to this search.
		if err := tx.QueryRow(`SELECT COUNT(*) FROM subject_finds
			WHERE scope = ? AND cmux_workspace_id = ? AND ts <= ?
			  AND ts > COALESCE((SELECT MAX(picked_at) FROM subject_finds
			                     WHERE scope = ? AND cmux_workspace_id = ? AND picked_at IS NOT NULL
			                       AND picked_at < ? AND id <> ?), -1)`,
			f.Scope, f.CmuxWorkspaceID, f.Timestamp.UnixMilli(),
			f.Scope, f.CmuxWorkspaceID, at.UnixMilli(), id).Scan(&p.Searches); err != nil {
			return nil, err
		}
		// Choosing a search older than the workspace's latest pick counts 0 by the
		// query: at least this one.
		p.Searches = max(1, p.Searches)
	}
	if f.PickedSubjectID == subjectID {
		return &p, nil
	}
	res, err := tx.Exec(`UPDATE subject_finds SET picked_subject_id = ?, picked_at = ? WHERE id = ? AND picked_subject_id IS NULL`,
		subjectID, at.UnixMilli(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrFindPicked
	}
	if j := journal(p, f); j != nil {
		if _, err := addJournalTx(tx, j); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	p.Created = true
	return &p, nil
}

// SubjectLastActivity returns, per subject, the ts (epoch ms) of its last
// log line of one of kinds; a subject with none is absent.
func (s *Store) SubjectLastActivity(kinds []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(kinds) == 0 {
		return out, nil
	}
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	q := `SELECT subject_id, MAX(ts) FROM subject_log WHERE kind IN (?` + strings.Repeat(",?", len(kinds)-1) + `) GROUP BY subject_id` //nolint:gosec // G202: fixed fragments only; every value is a bound parameter
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ts int64
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		out[id] = ts
	}
	return out, rows.Err()
}

func scanSubjectFind(r rowScanner) (*pigtail.SubjectFind, error) {
	var (
		f        pigtail.SubjectFind
		ts       int64
		cands    string
		picked   sql.NullString
		pickedAt sql.NullInt64
	)
	if err := r.Scan(&f.ID, &f.Scope, &ts, &f.Query, &cands, &f.CmuxWorkspaceID, &picked, &pickedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	f.Timestamp = time.UnixMilli(ts).UTC()
	if err := json.Unmarshal([]byte(cands), &f.Candidates); err != nil {
		return nil, err
	}
	f.PickedSubjectID = picked.String
	if pickedAt.Valid {
		f.PickedAt = time.UnixMilli(pickedAt.Int64).UTC()
	}
	return &f, nil
}

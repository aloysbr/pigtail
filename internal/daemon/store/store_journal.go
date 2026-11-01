package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- System journal (migration 0014) ------------------------------------------
//
// An index of pointers, transversal to subjects: see 0014_journal.sql. The
// handler validates and scopes; the store only persists and filters.

// JournalFilter narrows ListJournal. Scope is already the caller's allowed
// view (scope.Any = both); zero times and empty strings do not filter.
type JournalFilter struct {
	Since, Until time.Time // Since inclusive, Until exclusive
	Scope        scope.Scope
	Kind         string
	SubjectID    string
	IDs          []string // only rows whose id is one of these; empty = no filter
	IDPrefix     string   // only rows whose id starts with this (no LIKE: "_" is no wildcard)
	Limit        int      // <= 0: pigtail.JournalListDefault
	Offset       int      // rows skipped in the list order, for paging; <= 0: none
}

const journalCols = `id, scope, ts, kind, COALESCE(subject_id, ''), summary, source`

// AddJournal inserts e. created is false when (scope, id) already exists: the
// stored row is then copied into e, unchanged — first writer wins.
func (s *Store) AddJournal(e *pigtail.JournalEntry) (bool, error) {
	n, err := addJournalTx(s.db, e)
	if err != nil {
		return false, err
	}
	if n == 1 {
		return true, nil
	}
	stored, err := s.queryJournal(`SELECT `+journalCols+` FROM journal WHERE scope = ? AND id = ?`, e.Scope, e.ID)
	if err != nil {
		return false, err
	}
	if len(stored) != 1 {
		return false, fmt.Errorf("store: journal %s/%s neither inserted nor found", e.Scope, e.ID)
	}
	*e = stored[0]
	return false, nil
}

// addJournalTx inserts e on db (a connection or a transaction) and returns the
// rows affected: 0 when (scope, id) already exists.
func addJournalTx(db execer, e *pigtail.JournalEntry) (int64, error) {
	var subject any
	if e.SubjectID != "" {
		subject = e.SubjectID
	}
	res, err := db.Exec(`INSERT INTO journal (id, scope, ts, kind, subject_id, summary, source)
VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(scope, id) DO NOTHING`,
		e.ID, e.Scope, e.Timestamp.UnixMilli(), e.Kind, subject, e.Summary, e.Source)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListJournal returns the matching rows, newest first (ts, then id, then
// scope), never nil.
func (s *Store) ListJournal(f JournalFilter) ([]pigtail.JournalEntry, error) {
	q := `SELECT ` + journalCols + ` FROM journal WHERE 1 = 1`
	var args []any
	if !f.Since.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, f.Since.UnixMilli())
	}
	if !f.Until.IsZero() {
		q += ` AND ts < ?`
		args = append(args, f.Until.UnixMilli())
	}
	if f.Scope != scope.Any {
		q += ` AND scope = ?`
		args = append(args, string(f.Scope))
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.SubjectID != "" {
		q += ` AND subject_id = ?`
		args = append(args, f.SubjectID)
	}
	if len(f.IDs) > 0 {
		q += ` AND id IN (?` + strings.Repeat(`, ?`, len(f.IDs)-1) + `)`
		for _, id := range f.IDs {
			args = append(args, id)
		}
	}
	if f.IDPrefix != "" {
		q += ` AND substr(id, 1, ?) = ?`
		args = append(args, len([]rune(f.IDPrefix)), f.IDPrefix)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = pigtail.JournalListDefault
	}
	// A total order (a ts/id tie across two scopes never resolves at random
	// from one page to the next), then the offset.
	q += ` ORDER BY ts DESC, id DESC, scope DESC LIMIT ?`
	args = append(args, limit)
	if f.Offset > 0 {
		q += ` OFFSET ?`
		args = append(args, f.Offset)
	}
	return s.queryJournal(q, args...)
}

func (s *Store) queryJournal(q string, args ...any) ([]pigtail.JournalEntry, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pigtail.JournalEntry{}
	for rows.Next() {
		var (
			e  pigtail.JournalEntry
			ts int64
		)
		if err := rows.Scan(&e.ID, &e.Scope, &ts, &e.Kind, &e.SubjectID, &e.Summary, &e.Source); err != nil {
			return nil, err
		}
		e.Timestamp = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

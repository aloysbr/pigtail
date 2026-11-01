package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrRankChanged: a subject's current rank is not the From a rank change
// expected (someone moved it since the caller read it); nothing is written.
var ErrRankChanged = errors.New("store: rank changed")

// RankChange moves one subject from rank From to rank To; 0 = unranked
// (NULL), as everywhere else.
type RankChange struct {
	ID       string
	From, To int
}

// SetRanks applies changes in one transaction (POST /subjects/ranks): every
// From is checked against the current rank first, then every To is written;
// a stale From is ErrRankChanged and an unknown id ErrNotFound, and then
// nothing is written. updated_at is not touched: a rank move is not an edit
// of the subject (the « most recent » tie-break stays put).
func (s *Store) SetRanks(changes []RankChange) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range changes {
		var cur sql.NullInt64
		err := tx.QueryRow(`SELECT rank FROM subjects WHERE id = ?`, c.ID).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: subject %s", ErrNotFound, c.ID)
		}
		if err != nil {
			return err
		}
		if int(cur.Int64) != c.From {
			return fmt.Errorf("%w: subject %s is at rank %d, not %d", ErrRankChanged, c.ID, cur.Int64, c.From)
		}
	}
	for _, c := range changes {
		if _, err := tx.Exec(`UPDATE subjects SET rank = ? WHERE id = ?`, nullInt(c.To), c.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

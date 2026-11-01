package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Focus spaces (sub_x000000000000007, migration 0025). A focus counts while
// ended_at IS NULL AND (ends_at IS NULL OR ends_at > now): its end is judged
// at read. Sealing writes what the read already judges — ended_at = ends_at,
// end_reason = deadline, one end line per subject — at the first read or
// write after the deadline, unless the seal gate holds it (focus D26).

// ErrFocusNameTaken: an open focus (or, under a freeze, an expired one not
// sealed yet, focus D27) already holds the name.
var ErrFocusNameTaken = errors.New("store: focus name taken")

// ErrFocusEnded: the focus is ended, by hand or by its deadline.
var ErrFocusEnded = errors.New("store: focus already ended")

// ErrFocusMemberTaken: a theme or subject is already in another open focus
// (D2, focus D14). FocusID names that focus, for a caller allowed to read it.
type ErrFocusMemberTaken struct {
	Kind, ID, FocusID string
}

func (e *ErrFocusMemberTaken) Error() string {
	return "store: " + e.Kind + " " + e.ID + " is already in an open focus"
}

// openFocusSQL is the "counts at now" predicate; its one argument is now.
const openFocusSQL = `ended_at IS NULL AND (ends_at IS NULL OR ends_at > ?)`

// SetFocusSealGate installs the freeze gate (focus D26): while held()
// answers true, no read and no write seals an expired focus. nil = never held.
func (s *Store) SetFocusSealGate(held func() bool) {
	if held == nil {
		s.focusSealGate.Store(nil)
		return
	}
	s.focusSealGate.Store(&held)
}

func (s *Store) focusSealsHeld() bool {
	p := s.focusSealGate.Load()
	return p != nil && (*p)()
}

// CreateFocus inserts a focus and its members (AddedAt = now). The caller
// checks names, scopes and deadline; the store refuses a name held by an
// unsealed focus (ErrFocusNameTaken) and a member already in an open focus
// (*ErrFocusMemberTaken). An empty ID is generated.
func (s *Store) CreateFocus(f *pigtail.Focus, now time.Time) error {
	if f.ID == "" {
		f.ID = newStoreID(pigtail.PrefixFocus)
	}
	f.CreatedAt = now
	members := dedupeMembers(f.Members, now)
	return s.focusWrite(now, func(tx *sql.Tx) error {
		for _, m := range members {
			if err := memberFree(tx, f.ID, m, now); err != nil {
				return err
			}
		}
		var endsAt sql.NullInt64
		if f.EndsAt != nil {
			endsAt = sql.NullInt64{Valid: true, Int64: f.EndsAt.UnixMilli()}
		}
		if _, err := tx.Exec(`INSERT INTO focus_spaces (id, name, scope, created_at, ends_at) VALUES (?, ?, ?, ?, ?)`,
			f.ID, f.Name, f.Scope, now.UnixMilli(), endsAt); err != nil {
			if isUniqueViolation(err) {
				return ErrFocusNameTaken
			}
			return err
		}
		for _, m := range members {
			if _, err := tx.Exec(`INSERT INTO focus_members (focus_id, kind, member_id, added_at) VALUES (?, ?, ?, ?)`,
				f.ID, m.Kind, m.ID, now.UnixMilli()); err != nil {
				return err
			}
		}
		f.Members = members
		return nil
	})
}

// AddFocusMember adds a member to an open focus. ErrNotFound for an unknown
// focus, ErrFocusEnded for an ended one, *ErrFocusMemberTaken when another
// open focus holds the member; a member the focus already holds is a no-op.
func (s *Store) AddFocusMember(focusID string, m pigtail.FocusMember, now time.Time) error {
	return s.focusWrite(now, func(tx *sql.Tx) error {
		if err := focusOpenForWrite(tx, focusID, now); err != nil {
			return err
		}
		if err := memberFree(tx, focusID, m, now); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO focus_members (focus_id, kind, member_id, added_at) VALUES (?, ?, ?, ?)`,
			focusID, m.Kind, m.ID, now.UnixMilli())
		return err
	})
}

// RemoveFocusMember removes a member from an open focus: ErrFocusEnded on an
// ended focus, ErrNotFound for an unknown focus or a member it does not hold.
func (s *Store) RemoveFocusMember(focusID, kind, id string, now time.Time) error {
	return s.focusWrite(now, func(tx *sql.Tx) error {
		if err := focusOpenForWrite(tx, focusID, now); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM focus_members WHERE focus_id = ? AND kind = ? AND member_id = ?`, focusID, kind, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// EndFocus ends an open focus by hand (end_reason manual, ended_at = now)
// and writes its end lines dated now. It touches no other focus beyond the
// ordinary seal (none under the gate).
func (s *Store) EndFocus(id string, now time.Time) (*pigtail.Focus, error) {
	var out *pigtail.Focus
	err := s.focusWrite(now, func(tx *sql.Tx) error {
		if err := focusOpenForWrite(tx, id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE focus_spaces SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`,
			now.UnixMilli(), pigtail.FocusEndManual, id); err != nil {
			return err
		}
		f, err := readFocus(tx, id, now)
		if err != nil {
			return err
		}
		if err := writeEndLines(tx, f, now, "by hand"); err != nil {
			return err
		}
		out = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetFocus reads one focus, sealing expired ones first in a transaction of
// its own: a failed seal comes back in sealErr and the read goes on (an
// expired, unsealed focus reads ended by its deadline); err is ErrNotFound or
// a read failure.
func (s *Store) GetFocus(id string, now time.Time) (f *pigtail.Focus, sealErr, err error) {
	sealErr = s.sealForRead(now)
	f, err = readFocus(s.db, id, now)
	return f, sealErr, err
}

// ListFocus reads the focus, oldest first: the open ones only, or every one
// with includeEnded. Sealing as in GetFocus.
func (s *Store) ListFocus(now time.Time, includeEnded bool) (fs []pigtail.Focus, sealErr, err error) {
	sealErr = s.sealForRead(now)
	fs, err = listFocus(s.db, now, includeEnded)
	return fs, sealErr, err
}

// ActiveFocus is ListFocus(now, false): the focus that count at now.
func (s *Store) ActiveFocus(now time.Time) (fs []pigtail.Focus, sealErr, err error) {
	return s.ListFocus(now, false)
}

// --- internals ---------------------------------------------------------------

// focusWrite runs fn in one transaction, after sealing expired focus in the
// same transaction unless the gate holds seals. A failed seal fails the write.
func (s *Store) focusWrite(now time.Time, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if !s.focusSealsHeld() {
		if err := sealExpired(tx, now); err != nil {
			return fmt.Errorf("focus: seal: %w", err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// sealForRead seals in a transaction of its own, unless held.
func (s *Store) sealForRead(now time.Time) error {
	if s.focusSealsHeld() {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("focus: seal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := sealExpired(tx, now); err != nil {
		return fmt.Errorf("focus: seal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("focus: seal: %w", err)
	}
	return nil
}

// sealExpired ends every unsealed focus whose deadline has passed: ended_at
// = ends_at, end_reason = deadline, end lines dated the deadline. Once only:
// the update and the lines share the transaction, and a sealed focus is
// never selected again.
func sealExpired(tx *sql.Tx, now time.Time) error {
	rows, err := tx.Query(`SELECT id FROM focus_spaces WHERE ended_at IS NULL AND ends_at IS NOT NULL AND ends_at <= ? ORDER BY created_at, id`,
		now.UnixMilli())
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE focus_spaces SET ended_at = ends_at, end_reason = ? WHERE id = ? AND ended_at IS NULL`,
			pigtail.FocusEndDeadline, id); err != nil {
			return err
		}
		f, err := readFocus(tx, id, now)
		if err != nil {
			return err
		}
		if err := writeEndLines(tx, f, *f.EndedAt, "deadline"); err != nil {
			return err
		}
	}
	return nil
}

// writeEndLines writes one note per concerned subject (D3, focus D13): the
// subject members, and the subjects not closed of the theme members, once each.
func writeEndLines(tx *sql.Tx, f *pigtail.Focus, ts time.Time, why string) error {
	rows, err := tx.Query(`
		SELECT s.id FROM focus_members m JOIN subjects s ON s.id = m.member_id
		 WHERE m.focus_id = ? AND m.kind = 'subject'
		UNION
		SELECT s.id FROM focus_members m JOIN subjects s ON s.theme_id = m.member_id
		 WHERE m.focus_id = ? AND m.kind = 'theme' AND s.status NOT IN (?, ?, ?, ?)
		ORDER BY 1`, f.ID, f.ID,
		// every closed status (pigtail.IsClosedStatus)
		pigtail.StatusDone, pigtail.StatusParked, pigtail.StatusDropped, pigtail.StatusCaptured)
	if err != nil {
		return err
	}
	var subs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		subs = append(subs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	text := `focus "` + f.Name + `" ended (` + why + `)`
	for _, sub := range subs {
		if _, err := tx.Exec(`INSERT INTO subject_log (id, subject_id, ts, kind, text) VALUES (?, ?, ?, ?, ?)`,
			newStoreID(pigtail.PrefixLog), sub, ts.UnixMilli(), pigtail.LogNote, text); err != nil {
			return err
		}
	}
	return nil
}

// focusOpenForWrite: ErrNotFound for an unknown focus, ErrFocusEnded for one
// ended by hand, sealed, or past its deadline (judged like a read, so the
// answer is the same with or without the gate).
func focusOpenForWrite(tx *sql.Tx, id string, now time.Time) error {
	var ended, ends sql.NullInt64
	err := tx.QueryRow(`SELECT ended_at, ends_at FROM focus_spaces WHERE id = ?`, id).Scan(&ended, &ends)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ended.Valid || (ends.Valid && ends.Int64 <= now.UnixMilli()) {
		return ErrFocusEnded
	}
	return nil
}

// memberFree: *ErrFocusMemberTaken when an open focus other than self holds m.
func memberFree(tx *sql.Tx, self string, m pigtail.FocusMember, now time.Time) error {
	var other string
	err := tx.QueryRow(`SELECT f.id FROM focus_members m JOIN focus_spaces f ON f.id = m.focus_id
		WHERE m.kind = ? AND m.member_id = ? AND f.id <> ? AND f.`+openFocusSQL+` LIMIT 1`,
		m.Kind, m.ID, self, now.UnixMilli()).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &ErrFocusMemberTaken{Kind: m.Kind, ID: m.ID, FocusID: other}
}

func dedupeMembers(in []pigtail.FocusMember, now time.Time) []pigtail.FocusMember {
	out := make([]pigtail.FocusMember, 0, len(in))
	seen := map[[2]string]bool{}
	for _, m := range in {
		k := [2]string{m.Kind, m.ID}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, pigtail.FocusMember{Kind: m.Kind, ID: m.ID, AddedAt: now})
	}
	return out
}

// queryer is what both *sql.DB and *sql.Tx offer for reads.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

const focusColumns = `id, name, scope, created_at, ends_at, ended_at, end_reason`

func scanFocus(r rowScanner, now time.Time) (*pigtail.Focus, error) {
	var (
		f               pigtail.Focus
		created         int64
		endsAt, endedAt sql.NullInt64
	)
	if err := r.Scan(&f.ID, &f.Name, &f.Scope, &created, &endsAt, &endedAt, &f.EndReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	f.CreatedAt = time.UnixMilli(created).UTC()
	if endsAt.Valid {
		v := time.UnixMilli(endsAt.Int64).UTC()
		f.EndsAt = &v
	}
	switch {
	case endedAt.Valid:
		v := time.UnixMilli(endedAt.Int64).UTC()
		f.EndedAt = &v
	case f.EndsAt != nil && !f.EndsAt.After(now):
		// Expired, not sealed yet (seal failed or held): reads ended by its
		// deadline, as the seal will write it.
		v := *f.EndsAt
		f.EndedAt, f.EndReason = &v, pigtail.FocusEndDeadline
	}
	return &f, nil
}

func readFocus(q queryer, id string, now time.Time) (*pigtail.Focus, error) {
	f, err := scanFocus(q.QueryRow(`SELECT `+focusColumns+` FROM focus_spaces WHERE id = ?`, id), now)
	if err != nil {
		return nil, err
	}
	if f.Members, err = readMembers(q, id); err != nil {
		return nil, err
	}
	return f, nil
}

func listFocus(q queryer, now time.Time, includeEnded bool) ([]pigtail.Focus, error) {
	query := `SELECT ` + focusColumns + ` FROM focus_spaces`
	var args []any
	if !includeEnded {
		query += ` WHERE ` + openFocusSQL
		args = append(args, now.UnixMilli())
	}
	query += ` ORDER BY created_at, id`
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var out []pigtail.Focus
	for rows.Next() {
		f, err := scanFocus(rows, now)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Members, err = readMembers(q, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readMembers reads a focus's members in order of addition; Missing marks a
// theme or subject deleted since (no foreign key on member_id).
func readMembers(q queryer, focusID string) ([]pigtail.FocusMember, error) {
	rows, err := q.Query(`
		SELECT m.kind, m.member_id, m.added_at,
		       CASE WHEN m.kind = 'theme' THEN t.id IS NULL ELSE s.id IS NULL END
		  FROM focus_members m
		  LEFT JOIN themes t ON m.kind = 'theme' AND t.id = m.member_id
		  LEFT JOIN subjects s ON m.kind = 'subject' AND s.id = m.member_id
		 WHERE m.focus_id = ?
		 ORDER BY m.added_at, m.rowid`, focusID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pigtail.FocusMember{}
	for rows.Next() {
		var (
			m       pigtail.FocusMember
			added   int64
			missing bool
		)
		if err := rows.Scan(&m.Kind, &m.ID, &added, &missing); err != nil {
			return nil, err
		}
		m.AddedAt, m.Missing = time.UnixMilli(added).UTC(), missing
		out = append(out, m)
	}
	return out, rows.Err()
}

// newStoreID: prefix + 16 hex chars, the daemon's id scheme.
func newStoreID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Roles (migration 0036, role supervision) -------------------------------

const roleCols = `scope, name, subject_id, cadence_ms, grace_ms, stall_ms, created_at, updated_at`

func roleTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// UpsertRole inserts the role or updates it in place: created_at is kept on
// an update (so only rm + add gives a new creation time), updated_at moves.
// The times come from the caller's clock, never time.Now() here.
func (s *Store) UpsertRole(r *pigtail.Role) error {
	_, err := s.db.Exec(`INSERT INTO roles (`+roleCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scope, name) DO UPDATE SET
	subject_id = excluded.subject_id, cadence_ms = excluded.cadence_ms,
	grace_ms = excluded.grace_ms, stall_ms = excluded.stall_ms, updated_at = excluded.updated_at`,
		r.Scope, r.Name, r.SubjectID, r.Cadence.Milliseconds(), r.Grace.Milliseconds(), r.Stall.Milliseconds(),
		roleTime(r.CreatedAt), roleTime(r.UpdatedAt))
	return err
}

func scanRole(r rowScanner) (*pigtail.Role, error) {
	var (
		x                 pigtail.Role
		cad, grace, stall int64
		created, updated  string
	)
	if err := r.Scan(&x.Scope, &x.Name, &x.SubjectID, &cad, &grace, &stall, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	x.Cadence = time.Duration(cad) * time.Millisecond
	x.Grace = time.Duration(grace) * time.Millisecond
	x.Stall = time.Duration(stall) * time.Millisecond
	var err error
	if x.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return nil, err
	}
	if x.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return nil, err
	}
	return &x, nil
}

// GetRole returns one role of a scope, or ErrNotFound.
func (s *Store) GetRole(sc scope.Scope, name string) (*pigtail.Role, error) {
	return scanRole(s.db.QueryRow(`SELECT `+roleCols+` FROM roles WHERE scope = ? AND name = ?`, string(sc), name))
}

// ListRoles lists the roles of one scope (scope.Any = every scope), by scope then name.
func (s *Store) ListRoles(filter scope.Scope) ([]pigtail.Role, error) {
	q := `SELECT ` + roleCols + ` FROM roles`
	var args []any
	if filter != scope.Any {
		q += ` WHERE scope = ?`
		args = append(args, string(filter))
	}
	rows, err := s.db.Query(q+` ORDER BY scope, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pigtail.Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// DeleteRole removes a role, or ErrNotFound.
func (s *Store) DeleteRole(sc scope.Scope, name string) error {
	res, err := s.db.Exec(`DELETE FROM roles WHERE scope = ? AND name = ?`, string(sc), name)
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

// PeersOfSubject lists the sessions of a subject, whatever scope registered
// them: the subject comes from peerSubjectExpr (a live peer's cmux binding
// first). Live rows first, then newest update, at most limit rows.
func (s *Store) PeersOfSubject(subjectID string, includeEnded bool, limit int) ([]pigtail.PeerSession, error) {
	q := `SELECT ` + peerColumns + peerFrom + ` WHERE ` + peerSubjectExpr + ` = ?`
	if !includeEnded {
		q += ` AND p.ended_at IS NULL`
	}
	q += ` ORDER BY (p.ended_at IS NOT NULL), p.updated_at DESC LIMIT ?`
	rows, err := s.db.Query(q, subjectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.PeerSession
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

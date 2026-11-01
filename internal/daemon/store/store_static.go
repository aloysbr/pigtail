package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Static servers -------------------------------------------------------

const staticServerColumns = `id, name, dir, port, command, created_at`

// CreateStaticServer inserts a new static server row.
func (s *Store) CreateStaticServer(ss *pigtail.StaticServer) error {
	if ss.CreatedAt.IsZero() {
		ss.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO static_servers (id, name, dir, port, command, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ss.ID, ss.Name, ss.Dir, ss.Port, nullStr(ss.Command), ss.CreatedAt.UnixMilli(),
	)
	return err
}

// GetStaticServerByName returns a static server by its unique name, or
// ErrNotFound.
func (s *Store) GetStaticServerByName(name string) (*pigtail.StaticServer, error) {
	row := s.db.QueryRow(`SELECT `+staticServerColumns+` FROM static_servers WHERE name = ?`, name)
	return scanStaticServer(row)
}

// ListStaticServers returns all static servers, oldest first.
func (s *Store) ListStaticServers() ([]pigtail.StaticServer, error) {
	rows, err := s.db.Query(`SELECT ` + staticServerColumns + ` FROM static_servers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pigtail.StaticServer
	for rows.Next() {
		ss, err := scanStaticServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ss)
	}
	return out, rows.Err()
}

// DeleteStaticServer removes a static server by id. Returns ErrNotFound when
// no row matched.
func (s *Store) DeleteStaticServer(id string) error {
	res, err := s.db.Exec(`DELETE FROM static_servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanStaticServer(r rowScanner) (*pigtail.StaticServer, error) {
	var (
		ss        pigtail.StaticServer
		command   sql.NullString
		createdAt int64
	)
	err := r.Scan(&ss.ID, &ss.Name, &ss.Dir, &ss.Port, &command, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ss.Command = command.String
	ss.CreatedAt = time.UnixMilli(createdAt).UTC()
	return &ss, nil
}

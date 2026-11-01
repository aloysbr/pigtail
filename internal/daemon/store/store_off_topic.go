package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// ErrOffTopicExists: the workspace is already declared off-topic.
var ErrOffTopicExists = errors.New("store: workspace already declared off-topic")

const offTopicColumns = `cmux_workspace_id, scope, session_id, reason, declared_at, missing_ticks, space`

// CreateOffTopic declares a workspace off-topic; a second declaration is ErrOffTopicExists.
func (s *Store) CreateOffTopic(o *pigtail.WorkspaceOffTopic) error {
	_, err := s.db.Exec(`INSERT INTO workspace_off_topic (`+offTopicColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.CmuxWorkspaceID, o.Scope, o.SessionID, o.Reason, o.DeclaredAt.UnixMilli(), o.MissingTicks, o.Space)
	if err != nil && isUniqueViolation(err) {
		return ErrOffTopicExists
	}
	return err
}

// GetOffTopic returns the declaration of a workspace, or ErrNotFound.
func (s *Store) GetOffTopic(wsID string) (*pigtail.WorkspaceOffTopic, error) {
	return scanOffTopic(s.db.QueryRow(`SELECT `+offTopicColumns+` FROM workspace_off_topic WHERE cmux_workspace_id = ?`, wsID))
}

// ListOffTopic returns every declaration, oldest first.
func (s *Store) ListOffTopic() ([]pigtail.WorkspaceOffTopic, error) {
	rows, err := s.db.Query(`SELECT ` + offTopicColumns + ` FROM workspace_off_topic ORDER BY declared_at, cmux_workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pigtail.WorkspaceOffTopic{}
	for rows.Next() {
		o, err := scanOffTopic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// DeleteOffTopic lifts a declaration; none is ErrNotFound.
func (s *Store) DeleteOffTopic(wsID string) error {
	return s.updateBinding(`DELETE FROM workspace_off_topic WHERE cmux_workspace_id = ?`, wsID)
}

// SetOffTopicMissingTicks records how many listings in a row missed the workspace.
func (s *Store) SetOffTopicMissingTicks(wsID string, n int) error {
	return s.updateBinding(`UPDATE workspace_off_topic SET missing_ticks = ? WHERE cmux_workspace_id = ?`, n, wsID)
}

// SetOffTopicSpace changes the explicit space of a declaration (D6 of
// sub_x000000000000019: redeclaring with another --space moves the workspace);
// none is ErrNotFound.
func (s *Store) SetOffTopicSpace(wsID, space string) error {
	return s.updateBinding(`UPDATE workspace_off_topic SET space = ? WHERE cmux_workspace_id = ?`, space, wsID)
}

func scanOffTopic(r rowScanner) (*pigtail.WorkspaceOffTopic, error) {
	var (
		o  pigtail.WorkspaceOffTopic
		at int64
	)
	if err := r.Scan(&o.CmuxWorkspaceID, &o.Scope, &o.SessionID, &o.Reason, &at, &o.MissingTicks, &o.Space); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	o.DeclaredAt = time.UnixMilli(at).UTC()
	return &o, nil
}

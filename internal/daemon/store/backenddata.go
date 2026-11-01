package store

import (
	"encoding/json"
)

// UpdateWorkspaceBackendData replaces the backend_data column and nothing
// else — not the state, not the path, and above all **not
// last_activity_at**.
//
// That exclusion is the reason this exists rather than reusing
// UpdateWorkspacePath, which bumps the timestamp. The branch reconciliation
// runs on a timer over every workspace, so a bumped timestamp would make every
// workspace look freshly active every few minutes. The menu bar's Start list is
// sorted by last_activity_at and `pigtail list` reads it as recency: a
// background bookkeeping pass must not be able to reorder what the user sees.
//
// The caller passes the whole map, not a patch: backend data is the backend's
// to shape, and a store that merged keys would be making decisions about a
// structure it does not own.
func (s *Store) UpdateWorkspaceBackendData(id string, backendData map[string]any) error {
	bd, err := json.Marshal(backendData)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`UPDATE workspaces SET backend_data = ? WHERE id = ?`, string(bd), id,
	); err != nil {
		return err
	}
	s.invalidateWorkspace(id)
	return nil
}

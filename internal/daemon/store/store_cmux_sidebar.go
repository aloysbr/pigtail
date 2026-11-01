package store

import (
	"fmt"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Sidebar memory of a bound workspace (cmuxsync) ----------------------
//
// The sidebar loop (internal/daemon/cmuxsync) remembers, per bound
// workspace, what the title and description were before it first wrote them
// (prev_*), what it last wrote (applied_*), and who owns each field now. The
// applied text is persisted because it decides, after a daemon restart,
// whether a different title in cmux is a manual rename (spec §4.5).

// CmuxField names one text field of a bound workspace.
type CmuxField string

// The two text fields pigtail may own.
const (
	CmuxFieldTitle       CmuxField = "title"
	CmuxFieldDescription CmuxField = "description"
)

// ListAllCmuxBindings returns every binding, by workspace id.
func (s *Store) ListAllCmuxBindings() ([]pigtail.SubjectCmuxBinding, error) {
	rows, err := s.db.Query(`SELECT ` + cmuxBindingColumns + ` FROM subject_cmux_bindings ORDER BY cmux_workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pigtail.SubjectCmuxBinding{}
	for rows.Next() {
		b, err := scanCmuxBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// CaptureCmuxPrev records what a field showed before pigtail's first write on
// it. It is a no-op once pigtail has applied a text to that field, so the
// name given back on detach is never pigtail's own. custom matters for the
// title only: false means the title was cmux's automatic one, given back by
// clearing the name rather than by renaming.
func (s *Store) CaptureCmuxPrev(wsID string, f CmuxField, prev string, custom bool) error {
	var q string
	var args []any
	switch f {
	case CmuxFieldTitle:
		q = `UPDATE subject_cmux_bindings SET prev_title = ?, prev_title_custom = ?
WHERE cmux_workspace_id = ? AND COALESCE(applied_title, '') = ''`
		args = []any{nullStr(prev), boolInt(custom), wsID}
	case CmuxFieldDescription:
		q = `UPDATE subject_cmux_bindings SET prev_description = ?
WHERE cmux_workspace_id = ? AND COALESCE(applied_description, '') = ''`
		args = []any{nullStr(prev), wsID}
	default:
		return fmt.Errorf("store: unknown cmux field %q", f)
	}
	_, err := s.db.Exec(q, args...)
	return err
}

// SetCmuxApplied records the text pigtail just wrote on a field ("" = none).
func (s *Store) SetCmuxApplied(wsID string, f CmuxField, text string) error {
	col, err := cmuxColumn(f, "applied_title", "applied_description")
	if err != nil {
		return err
	}
	return s.updateBinding(`UPDATE subject_cmux_bindings SET `+col+` = ? WHERE cmux_workspace_id = ?`, nullStr(text), wsID)
}

// SetCmuxOwner records who owns a field: pigtail, or the user after a manual
// rename.
func (s *Store) SetCmuxOwner(wsID string, f CmuxField, owner string) error {
	if owner != pigtail.CmuxOwnerPigtail && owner != pigtail.CmuxOwnerUser {
		return fmt.Errorf("store: unknown cmux owner %q", owner)
	}
	col, err := cmuxColumn(f, "title_owner", "description_owner")
	if err != nil {
		return err
	}
	return s.updateBinding(`UPDATE subject_cmux_bindings SET `+col+` = ? WHERE cmux_workspace_id = ?`, owner, wsID)
}

// ReleaseCmuxText forgets the sidebar memory once pigtail has given the
// workspace's title and description back (sidebar switched off): the next
// write starts from a fresh capture.
func (s *Store) ReleaseCmuxText(wsID string) error {
	return s.updateBinding(`UPDATE subject_cmux_bindings SET prev_title = NULL, prev_title_custom = 0,
	prev_description = NULL, applied_title = NULL, applied_description = NULL WHERE cmux_workspace_id = ?`, wsID)
}

// SetCmuxMissingTicks records how many consecutive listings missed the
// workspace.
func (s *Store) SetCmuxMissingTicks(wsID string, n int) error {
	return s.updateBinding(`UPDATE subject_cmux_bindings SET missing_ticks = ? WHERE cmux_workspace_id = ?`, n, wsID)
}

// ForgetCmuxWorkspace drops the binding of a workspace cmux closed, whatever
// its subject. Only the sidebar loop calls it; the API unbinds through
// DeleteCmuxBinding, scoped to one subject.
func (s *Store) ForgetCmuxWorkspace(wsID string) error {
	return s.updateBinding(`DELETE FROM subject_cmux_bindings WHERE cmux_workspace_id = ?`, wsID)
}

func (s *Store) updateBinding(q string, args ...any) error {
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// cmuxColumn maps a field onto one of two fixed column names: never a value
// from outside reaches the SQL text.
func cmuxColumn(f CmuxField, title, description string) (string, error) {
	switch f {
	case CmuxFieldTitle:
		return title, nil
	case CmuxFieldDescription:
		return description, nil
	}
	return "", fmt.Errorf("store: unknown cmux field %q", f)
}

package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// --- Subject ↔ cmux workspace bindings (migration 0012) -------------------

// ErrCmuxBound is returned when a cmux workspace is already bound to another
// subject than the one the write expected. The first link wins; only an
// explicit replace moves it.
var ErrCmuxBound = errors.New("store: cmux workspace already bound")

const cmuxBindingColumns = `cmux_workspace_id, subject_id, source, bound_at,
	COALESCE(prev_title, ''), prev_title_custom, COALESCE(prev_description, ''),
	COALESCE(applied_title, ''), COALESCE(applied_description, ''),
	title_owner, description_owner, missing_ticks`

// CreateCmuxBinding binds a workspace that no subject holds yet. A workspace
// already bound (to any subject) is ErrCmuxBound: the primary key decides, so
// two concurrent first links cannot both win.
func (s *Store) CreateCmuxBinding(b *pigtail.SubjectCmuxBinding) error {
	if b.TitleOwner == "" {
		b.TitleOwner = pigtail.CmuxOwnerPigtail
	}
	if b.DescriptionOwner == "" {
		b.DescriptionOwner = pigtail.CmuxOwnerPigtail
	}
	_, err := s.db.Exec(`INSERT INTO subject_cmux_bindings (cmux_workspace_id, subject_id, source, bound_at,
	prev_title, prev_title_custom, prev_description, applied_title, applied_description,
	title_owner, description_owner, missing_ticks)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.CmuxWorkspaceID, b.SubjectID, b.Source, b.BoundAt.UnixMilli(),
		nullStr(b.PrevTitle), boolInt(b.PrevTitleCustom), nullStr(b.PrevDescription),
		nullStr(b.AppliedTitle), nullStr(b.AppliedDescription),
		b.TitleOwner, b.DescriptionOwner, b.MissingTicks)
	if err != nil && isUniqueViolation(err) {
		return ErrCmuxBound
	}
	return err
}

// ReplaceCmuxBinding moves a bound workspace from subject `from` to subject
// `to`, only if it is still bound to `from` (compare-and-swap: the caller
// authorised the move against that subject). Otherwise ErrCmuxBound. The
// sidebar memory (previous and applied titles, owners) is kept: it describes
// the workspace, not the subject.
func (s *Store) ReplaceCmuxBinding(wsID, from, to, source string, at time.Time) error {
	res, err := s.db.Exec(`UPDATE subject_cmux_bindings SET subject_id = ?, source = ?, bound_at = ?, missing_ticks = 0
WHERE cmux_workspace_id = ? AND subject_id = ?`, to, source, at.UnixMilli(), wsID, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCmuxBound
	}
	return nil
}

// GetCmuxBinding returns the binding of a workspace, or ErrNotFound.
func (s *Store) GetCmuxBinding(wsID string) (*pigtail.SubjectCmuxBinding, error) {
	return scanCmuxBinding(s.db.QueryRow(`SELECT `+cmuxBindingColumns+` FROM subject_cmux_bindings WHERE cmux_workspace_id = ?`, wsID))
}

// ListCmuxBindings returns the workspaces bound to a subject, oldest link
// first.
func (s *Store) ListCmuxBindings(subjectID string) ([]pigtail.SubjectCmuxBinding, error) {
	rows, err := s.db.Query(`SELECT `+cmuxBindingColumns+` FROM subject_cmux_bindings WHERE subject_id = ?
ORDER BY bound_at, cmux_workspace_id`, subjectID)
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

// DeleteCmuxBinding removes the binding of wsID to subjectID. A workspace not
// bound to that subject is ErrNotFound, so a route scoped to one subject can
// never unbind another's workspace.
func (s *Store) DeleteCmuxBinding(subjectID, wsID string) error {
	res, err := s.db.Exec(`DELETE FROM subject_cmux_bindings WHERE cmux_workspace_id = ? AND subject_id = ?`, wsID, subjectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetCmuxOwners hands a bound workspace's title and description back to
// pigtail (retitle) — the title is pigtail's since D5: only the description
// changes hands. A field the user had taken also forgets the text pigtail
// last posed: the sidebar loop then treats it as never written, captures the
// user's name as the one to give back, and renames again. A field pigtail
// still owns keeps its applied text. Unknown workspace: ErrNotFound.
func (s *Store) ResetCmuxOwners(wsID string) error {
	res, err := s.db.Exec(`UPDATE subject_cmux_bindings SET
	applied_title       = CASE WHEN title_owner = 'user' THEN NULL ELSE applied_title END,
	applied_description = CASE WHEN description_owner = 'user' THEN NULL ELSE applied_description END,
	title_owner = 'pigtail', description_owner = 'pigtail'
WHERE cmux_workspace_id = ?`, wsID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanCmuxBinding(r rowScanner) (*pigtail.SubjectCmuxBinding, error) {
	var (
		b      pigtail.SubjectCmuxBinding
		bound  int64
		custom int
	)
	err := r.Scan(&b.CmuxWorkspaceID, &b.SubjectID, &b.Source, &bound,
		&b.PrevTitle, &custom, &b.PrevDescription, &b.AppliedTitle, &b.AppliedDescription,
		&b.TitleOwner, &b.DescriptionOwner, &b.MissingTicks)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	b.BoundAt = time.UnixMilli(bound).UTC()
	b.PrevTitleCustom = custom != 0
	return &b, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// isUniqueViolation recognises SQLite's primary-key / UNIQUE failure.
func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "PRIMARY KEY")
}

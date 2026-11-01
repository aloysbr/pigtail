package store

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/suggest"
)

// likeEscaper makes a needle literal inside LIKE … ESCAPE '\'.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// SuggestLogLines returns the log entries of the given kinds whose text
// contains one of the needles (LIKE '%n%', one query); internal/suggest's
// regexes judge them afterwards. No needle or no kind → nothing, no query.
func (s *Store) SuggestLogLines(needles []string, kinds []string) ([]suggest.LogLine, error) {
	var likes []string
	var args []any
	for _, n := range needles {
		if n == "" {
			continue
		}
		likes = append(likes, `text LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscaper.Replace(n)+"%")
	}
	if len(likes) == 0 || len(kinds) == 0 {
		return nil, nil
	}
	for _, k := range kinds {
		args = append(args, k)
	}
	where := strings.Join(likes, " OR ")
	marks := "?" + strings.Repeat(",?", len(kinds)-1)
	q := `SELECT subject_id, kind, text FROM subject_log WHERE (` + where + `) AND kind IN (` + marks + `) ORDER BY ts` //nolint:gosec // G202: fixed fragments only; every value is a bound parameter
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []suggest.LogLine
	for rows.Next() {
		var l suggest.LogLine
		if err := rows.Scan(&l.SubjectID, &l.Kind, &l.Text); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SuggestLinkedWorktrees returns the subject → worktree path pairs linked by a
// phase (subject_phases.workspace_id → workspaces.path); a dangling link is
// skipped.
func (s *Store) SuggestLinkedWorktrees() ([]suggest.Linked, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT p.subject_id, w.path FROM subject_phases p
		 JOIN workspaces w ON w.id = p.workspace_id
		 WHERE p.workspace_id IS NOT NULL AND p.workspace_id != '' AND w.path != ''
		 ORDER BY p.subject_id, w.path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []suggest.Linked
	for rows.Next() {
		var l suggest.Linked
		if err := rows.Scan(&l.SubjectID, &l.Path); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

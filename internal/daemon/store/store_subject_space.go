package store

import (
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Writes of subjects.space (migration 0033, D5 of sub_x000000000000033). Only
// two paths write it: a hand drag of a session into a space folder, and the
// clear on a theme change (D8, in UpdateSubject). Both leave a subject_log note
// and a journal line in the same transaction (D7).

const journalSummaryRunes = 280

// spaceDragText is the one-line text of a drag: ” renders as the theme's.
func spaceDragText(from, to, theme string) string {
	if from == "" {
		from = "(aucun)" // a theme with no space
	}
	if to == "" {
		to = "(theme: " + theme + ")"
	}
	return "space dragged: " + from + " → " + to
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// SetSubjectSpaceByDrag sets a subject's own space (” = its theme's) after a
// hand drag, and traces it (subject_log note + journal line) in the same
// transaction. from is the effective space it leaves, for the text. changed is
// false when the space already was space (nothing is written); an unknown
// subject is ErrNotFound.
func (s *Store) SetSubjectSpaceByDrag(subjectID, space, from string, at time.Time) (changed bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	sub, err := scanSubject(tx.QueryRow(subjectSelect()+` WHERE s.id = ?`, subjectID)) //nolint:gosec // G202: orphanScopeSQL is a validated identifier
	if err != nil {
		return false, err
	}
	if sub.Space == space {
		return false, nil
	}
	var themeSpace string
	if err := tx.QueryRow(`SELECT space FROM themes WHERE id = ?`, sub.ThemeID).Scan(&themeSpace); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE subjects SET space = ? WHERE id = ?`, space, subjectID); err != nil {
		return false, err
	}
	text := spaceDragText(from, space, themeSpace)
	if err := traceSubjectSpace(tx, sub, pigtail.JournalKindSubjectSpaceDrag, text, at); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// traceSubjectSpace writes the subject_log note and the journal line of a
// subjects.space change, on tx.
func traceSubjectSpace(tx *sql.Tx, sub *pigtail.Subject, kind, text string, at time.Time) error {
	if _, err := tx.Exec(`INSERT INTO subject_log (id, subject_id, ts, kind, text) VALUES (?, ?, ?, ?, ?)`,
		newStoreID(pigtail.PrefixLog), sub.ID, at.UnixMilli(), pigtail.LogNote, text); err != nil {
		return err
	}
	_, err := addJournalTx(tx, &pigtail.JournalEntry{
		ID:        kind + ":" + sub.ID + ":" + at.UTC().Format("20060102T150405.000"),
		Timestamp: at, Kind: kind, Scope: sub.Scope, SubjectID: sub.ID,
		Summary: clipRunes(pigtail.ShortTitle(*sub)+" — "+text, journalSummaryRunes), Source: "pigtail:space",
	})
	return err
}

// SetOffTopicSpaceByDrag rewrites the explicit space of an off-topic
// declaration after a hand drag, with a journal line (kind
// workspace-off-topic-space-drag, scope of the declaration) in the same
// transaction. from is the space it leaves. changed is false when it already was
// space; an absent row is ErrNotFound.
func (s *Store) SetOffTopicSpaceByDrag(wsID, space, from string, at time.Time) (changed bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var scope, cur string
	if err := tx.QueryRow(`SELECT scope, space FROM workspace_off_topic WHERE cmux_workspace_id = ?`, wsID).Scan(&scope, &cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if cur == space {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE workspace_off_topic SET space = ? WHERE cmux_workspace_id = ?`, space, wsID); err != nil {
		return false, err
	}
	short := wsID
	if len(short) > 8 {
		short = short[:8]
	}
	text := spaceDragText(from, space, "")
	if _, err := addJournalTx(tx, &pigtail.JournalEntry{
		ID:        pigtail.JournalKindOffTopicSpaceDrag + ":" + wsID + ":" + at.UTC().Format("20060102T150405.000"),
		Timestamp: at, Kind: pigtail.JournalKindOffTopicSpaceDrag, Scope: scope,
		Summary: clipRunes("workspace cmux "+short+" (off-topic) — "+text, journalSummaryRunes), Source: "pigtail:space",
	}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

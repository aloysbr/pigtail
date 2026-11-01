package store

import (
	"regexp"
	"testing"
)

// A freshly migrated database carries no private vocabulary: the schema
// (tables, comments inside CREATE statements, indexes, triggers) names no
// organisation, scope or vault root. The scope set lives in the scope table,
// never in SQL.
func TestFreshSchemaIsNeutral(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	banned := regexp.MustCompile(`(?i)talk|perso|alice|arboretum|duetto`)
	n := 0
	for rows.Next() {
		var typ, name, sqlText string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			t.Fatal(err)
		}
		n++
		if m := banned.FindString(name + "\n" + sqlText); m != "" {
			t.Errorf("sqlite_master %s %s mentions %q", typ, name, m)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 20 {
		t.Fatalf("only %d schema objects read: the instrument read nothing", n)
	}
}

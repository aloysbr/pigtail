package pigtail

import (
	"strings"
	"testing"
)

func validEntry() JournalEntry {
	return JournalEntry{ID: "session-end:sub_x:2026-10-03:plan:t1", Kind: "session-end",
		Summary: "Journal · build/tooling · step plan (t1) · plan written", Source: "vault:Perso/Areas/x/index.md"}
}

func TestJournalEntryValidate(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*JournalEntry)
		wantErr string // "" = valid
	}{
		{"valid", func(*JournalEntry) {}, ""},
		{"trimmed", func(e *JournalEntry) { e.Summary = "  x  "; e.Kind = " note " }, ""},
		{"no id", func(e *JournalEntry) { e.ID = " " }, "id is required"},
		{"id with space", func(e *JournalEntry) { e.ID = "a b" }, "id must be"},
		{"id too long", func(e *JournalEntry) { e.ID = strings.Repeat("a", 201) }, "id must be"},
		{"bad kind", func(e *JournalEntry) { e.Kind = "Session End" }, "kind"},
		{"empty kind", func(e *JournalEntry) { e.Kind = "" }, "kind"},
		{"empty summary", func(e *JournalEntry) { e.Summary = "" }, "summary is required"},
		{"multi-line summary", func(e *JournalEntry) { e.Summary = "a\nb" }, "one line"},
		{"cr summary", func(e *JournalEntry) { e.Summary = "a\rb" }, "one line"},
		{"280 runes", func(e *JournalEntry) { e.Summary = strings.Repeat("é", 280) }, ""},
		{"281 runes", func(e *JournalEntry) { e.Summary = strings.Repeat("é", 281) }, "at most 280"},
		{"invalid utf8", func(e *JournalEntry) { e.Summary = "\xff" }, "UTF-8"},
		{"no source", func(e *JournalEntry) { e.Source = "" }, "source is required"},
		{"source without scheme", func(e *JournalEntry) { e.Source = "Perso/Areas/x.md" }, "scheme"},
		{"multi-line source", func(e *JournalEntry) { e.Source = "vault:a\nb" }, "scheme"},
		{"source too long", func(e *JournalEntry) { e.Source = "https://x/" + strings.Repeat("a", 1020) }, "scheme"},
		{"source with spaces ok", func(e *JournalEntry) { e.Source = "vault:Globex/Areas/Meetings/a b.md" }, ""},
		{"globex scope", func(e *JournalEntry) { e.Scope = "globex" }, ""},
		{"third scope", func(e *JournalEntry) { e.Scope = "acme" }, ""},
		{"no scope", func(e *JournalEntry) { e.Scope = "" }, ""},
		{"scope with a space", func(e *JournalEntry) { e.Scope = "Globex Corp" }, "must be a scope name"},
		{"scope with a slash", func(e *JournalEntry) { e.Scope = "a/b" }, "must be a scope name"},
		{"gx scope", func(e *JournalEntry) { e.Scope = "gx" }, ""},
		{"subject with space", func(e *JournalEntry) { e.SubjectID = "sub x" }, "subject_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validEntry()
			c.mut(&e)
			err := e.Validate()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestJournalEntryValidateTrims(t *testing.T) {
	e := validEntry()
	e.Summary, e.Kind = "  x  ", " note "
	if err := e.Validate(); err != nil || e.Summary != "x" || e.Kind != "note" {
		t.Fatalf("got %+v, %v", e, err)
	}
}

package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runJournal runs `pigtail journal <args>` against s with stdin, returning stdout.
func runJournal(t *testing.T, s *stubDaemon, stdin string, args ...string) (string, error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	var out string
	var err error
	withStubDaemon(t, s, func() {
		stdout := os.Stdout
		rd, wr, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		os.Stdout = wr
		cmd := journalCmd()
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader(stdin))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
		_ = wr.Close()
		os.Stdout = stdout
		b, _ := io.ReadAll(rd)
		out = string(b)
	})
	return out, err
}

func journalRows(t *testing.T, st *store.Store) []pigtail.JournalEntry {
	t.Helper()
	out, err := st.ListJournal(store.JournalFilter{Scope: scope.Any})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJournalCLIAddTwice(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	args := []string{"add", "--id", "gate-fired:g1", "--kind", "gate-fired", "--subject", "sub_p",
		"--summary", "gate g1 fired — v=1", "--source", "file:~/gates.jsonl#g1"}
	out, err := runJournal(t, s, "", args...)
	if err != nil || !strings.Contains(out, "✓ journal ← gate-fired:g1") {
		t.Fatalf("first: %q %v", out, err)
	}
	out, err = runJournal(t, s, "", args...)
	if err != nil || !strings.Contains(out, "already present") {
		t.Fatalf("second: %q %v", out, err)
	}
	if rows := journalRows(t, st); len(rows) != 1 || rows[0].Scope != "perso" {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestJournalCLIPersoOnWorkNeedsCrossScope(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	args := []string{"add", "--id", "x", "--kind", "note", "--subject", "sub_t", "--summary", "s", "--source", "vault:Globex/x.md"}
	_, err := runJournal(t, s, "", args...)
	wantScopeMismatch(t, "perso journal add on work", err)
	if _, err := runJournal(t, s, "", append(args, "--cross-scope")...); err != nil {
		t.Fatalf("with --cross-scope: %v", err)
	}
	if rows := journalRows(t, st); len(rows) != 1 || rows[0].Scope != "globex" {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestJournalCLIWorkCellNeverListsPerso(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	s, st := realSubjectDaemon(t)
	for _, e := range []*pigtail.JournalEntry{
		{ID: "t", Scope: "globex", Timestamp: time.UnixMilli(2), Kind: "note", Summary: "work row", Source: "a:b"},
		{ID: "p", Scope: "perso", Timestamp: time.UnixMilli(3), Kind: "note", Summary: "perso row", Source: "a:b"},
	} {
		if _, err := st.AddJournal(e); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runJournal(t, s, "", "list", "--json")
	if err != nil || !strings.Contains(out, "work row") || strings.Contains(out, "perso row") {
		t.Fatalf("work list: %q %v", out, err)
	}
}

func TestJournalCLIListText(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "a", Scope: "perso", Timestamp: time.Now().Add(-time.Hour),
		Kind: "session-end", SubjectID: "sub_p", Summary: "plan written", Source: "vault:Perso/x.md"}); err != nil {
		t.Fatal(err)
	}
	out, err := runJournal(t, s, "", "list", "--since", "24h")
	if err != nil || !strings.Contains(out, "perso  session-end  sub_p  plan written  → vault:Perso/x.md") {
		t.Fatalf("list text: %q %v", out, err)
	}
	out, err = runJournal(t, s, "", "list", "--since", "2d", "--until", "1h", "--kind", "meeting")
	if err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("filtered list: %q %v", out, err)
	}
}

func TestJournalCLIImport(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	in := strings.Join([]string{
		`{"id":"a","ts":"2026-10-01T10:00:00Z","kind":"note","summary":"one","source":"a:b"}`,
		``,
		`{"id":"a","ts":"2026-10-01T10:00:00Z","kind":"note","summary":"one","source":"a:b"}`,
		`{"id":"b","kind":"note","summary":"two\nlines","source":"a:b"}`,
		`not json`,
		`{"id":"c","kind":"note","scope":"globex","summary":"work row","source":"a:b"}`,
	}, "\n")
	out, err := runJournal(t, s, in, "import")
	if err == nil || !strings.Contains(out, "journal import: 1 created, 1 already present, 3 refused") {
		t.Fatalf("import: %q %v", out, err)
	}
	if _, err := runJournal(t, s, `{"id":"c","kind":"note","scope":"globex","summary":"work row","source":"a:b"}`, "import", "--cross-scope"); err != nil {
		t.Fatalf("import --cross-scope: %v", err)
	}
	if rows := journalRows(t, st); len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
}

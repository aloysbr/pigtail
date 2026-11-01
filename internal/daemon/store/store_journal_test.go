package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func jEntry(id, sc string, ts int64, kind, subj string) *pigtail.JournalEntry {
	return &pigtail.JournalEntry{ID: id, Scope: sc, Timestamp: time.UnixMilli(ts).UTC(), Kind: kind,
		SubjectID: subj, Summary: "summary " + id, Source: "pigtail:test/" + id}
}

func TestJournalAddIdempotent(t *testing.T) {
	st := openMem(t)
	e := jEntry("a", "globex", 10, "note", "")
	if created, err := st.AddJournal(e); err != nil || !created {
		t.Fatalf("first add: %v %v", created, err)
	}
	again := jEntry("a", "globex", 99, "other", "")
	again.Summary = "changed"
	created, err := st.AddJournal(again)
	if err != nil || created {
		t.Fatalf("second add: created=%v err=%v", created, err)
	}
	if again.Summary != "summary a" || again.Kind != "note" || again.Timestamp.UnixMilli() != 10 {
		t.Fatalf("conflict must echo the stored row, got %+v", again)
	}
	t.Run("same id in the other scope is another row", func(t *testing.T) {
		if created, err := st.AddJournal(jEntry("a", "perso", 11, "note", "")); err != nil || !created {
			t.Fatalf("perso add of a work id: %v %v", created, err)
		}
		all, _ := st.ListJournal(JournalFilter{Scope: scope.Any})
		if len(all) != 2 {
			t.Fatalf("rows = %d, want 2", len(all))
		}
	})
}

func TestJournalListFilters(t *testing.T) {
	st := openMem(t)
	for _, e := range []*pigtail.JournalEntry{
		jEntry("t1", "globex", 100, "delivery", "sub_t"),
		jEntry("t2", "globex", 200, "session-end", "sub_t"),
		jEntry("p1", "perso", 150, "delivery", ""),
		jEntry("p2", "perso", 300, "meeting", "sub_p"),
		jEntry("p0", "perso", 300, "meeting", ""), // same ts as p2: id breaks the tie
	} {
		if _, err := st.AddJournal(e); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(f JournalFilter) string {
		t.Helper()
		out, err := st.ListJournal(f)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, e := range out {
			s = append(s, e.ID)
		}
		return strings.Join(s, ",")
	}
	cases := map[string]struct {
		f    JournalFilter
		want string
	}{
		"all newest first": {JournalFilter{Scope: scope.Any}, "p2,p0,t2,p1,t1"},
		"work only":        {JournalFilter{Scope: scope.Scope("globex")}, "t2,t1"},
		"since inclusive":  {JournalFilter{Scope: scope.Any, Since: time.UnixMilli(200)}, "p2,p0,t2"},
		"until exclusive":  {JournalFilter{Scope: scope.Any, Until: time.UnixMilli(200)}, "p1,t1"},
		"kind":             {JournalFilter{Scope: scope.Any, Kind: "delivery"}, "p1,t1"},
		"subject":          {JournalFilter{Scope: scope.Any, SubjectID: "sub_t"}, "t2,t1"},
		"limit":            {JournalFilter{Scope: scope.Any, Limit: 2}, "p2,p0"},
		"work never perso": {JournalFilter{Scope: scope.Scope("globex"), SubjectID: "sub_p"}, ""},
	}
	for name, c := range cases {
		if got := ids(c.f); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	if out, _ := st.ListJournal(JournalFilter{Scope: scope.Scope("globex"), Kind: "none"}); out == nil {
		t.Error("an empty result must be [] not nil (JSON null)")
	}
}

func TestJournalCheckConstraints(t *testing.T) {
	st := openMem(t)
	for name, e := range map[string]*pigtail.JournalEntry{
		"malformed scope": {ID: "x", Scope: "Two Words", Kind: "k", Summary: "s", Source: "a:b"},
		"empty kind":      {ID: "x", Scope: "globex", Kind: "", Summary: "s", Source: "a:b"},
		"multi-line":      {ID: "x", Scope: "globex", Kind: "k", Summary: "a\nb", Source: "a:b"},
		"too long":        {ID: "x", Scope: "globex", Kind: "k", Summary: strings.Repeat("é", 281), Source: "a:b"},
		"empty source":    {ID: "x", Scope: "globex", Kind: "k", Summary: "s", Source: ""},
		"empty summary":   {ID: "x", Scope: "globex", Kind: "k", Summary: "", Source: "a:b"},
	} {
		if _, err := st.AddJournal(e); err == nil {
			t.Errorf("%s: the database accepted it", name)
		}
	}
}

func TestJournalMigrationLeavesSubjectLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db := migrateBefore(t, path, "0014")
	if _, err := db.Exec(`INSERT INTO subject_log (id, subject_id, ts, kind, text) VALUES
		('log_1', 'sub_a', 5, 'delivery', 'aloysbr/pigtail#1 merged · x'),
		('log_2', 'sub_a', 6, 'handoff', 'long content')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logs, err := st.ListLog("sub_a", time.Time{})
	if err != nil || len(logs) != 2 || logs[0].Text != "long content" || logs[1].Kind != "delivery" {
		t.Fatalf("subject_log changed: %+v %v", logs, err)
	}
	if out, err := st.ListJournal(JournalFilter{Scope: scope.Any}); err != nil || len(out) != 0 {
		t.Fatalf("journal after migration: %+v %v", out, err)
	}
}

// The alert read asks for given ids only (Task 7): the filter keeps exactly
// them, in every scope the scope filter lets through, newest first.
func TestJournalListByIDs(t *testing.T) {
	st := openMem(t)
	for _, e := range []*pigtail.JournalEntry{
		jEntry("a", "perso", 100, "alert-resolved", ""), jEntry("b", "perso", 200, "alert-resolved", ""),
		jEntry("c", "perso", 300, "alert-resolved", ""), jEntry("a", "globex", 400, "alert-resolved", ""),
	} {
		if _, err := st.AddJournal(e); err != nil {
			t.Fatal(err)
		}
	}
	got := func(f JournalFilter) string {
		t.Helper()
		out, err := st.ListJournal(f)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, e := range out {
			s = append(s, e.Scope+"/"+e.ID)
		}
		return strings.Join(s, ",")
	}
	if g := got(JournalFilter{Scope: scope.Any, IDs: []string{"a", "c", "zz"}, Limit: 10}); g != "globex/a,perso/c,perso/a" {
		t.Fatalf("ids a,c,zz = %q", g)
	}
	if g := got(JournalFilter{Scope: scope.Scope("perso"), IDs: []string{"a"}}); g != "perso/a" {
		t.Fatalf("perso a = %q", g)
	}
	if g := got(JournalFilter{Scope: scope.Any}); strings.Count(g, ",") != 3 {
		t.Fatalf("no IDs = no filter, got %q", g)
	}
}

// Offset pages the list order (ts DESC, id DESC, scope DESC) with no gap and
// no repeat, a tie across scopes included (Task 7, the alert read).
func TestJournalListOffsetPages(t *testing.T) {
	st := openMem(t)
	for _, e := range []*pigtail.JournalEntry{
		jEntry("a", "perso", 100, "k", ""), jEntry("a", "globex", 100, "k", ""),
		jEntry("b", "perso", 200, "k", ""), jEntry("c", "perso", 300, "k", ""), jEntry("d", "perso", 400, "other", ""),
	} {
		if _, err := st.AddJournal(e); err != nil {
			t.Fatal(err)
		}
	}
	var s []string
	for off := 0; ; off += 2 {
		out, err := st.ListJournal(JournalFilter{Scope: scope.Any, Kind: "k", Limit: 2, Offset: off})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range out {
			s = append(s, e.Scope+"/"+e.ID)
		}
		if len(out) < 2 {
			break
		}
	}
	if g := strings.Join(s, ","); g != "perso/c,perso/b,perso/a,globex/a" {
		t.Fatalf("pages = %q", g)
	}
}

func TestJournalListByIDPrefix(t *testing.T) {
	st := openMem(t)
	for i, id := range []string{"api-retry:s1:7:100:1", "api-retry:s1:8:100:1", "api-retry:s1_7:100:1"} {
		if _, err := st.AddJournal(jEntry(id, "perso", int64(10+i), "api-retry-sent", "")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListJournal(JournalFilter{Scope: scope.Any, IDPrefix: "api-retry:s1:7:"})
	if err != nil || len(got) != 1 || got[0].ID != "api-retry:s1:7:100:1" {
		t.Fatalf("prefix s1:7: %+v %v", got, err)
	}
	// "_" is no wildcard: s1_7 is not matched by the prefix "api-retry:s1:".
	got, err = st.ListJournal(JournalFilter{Scope: scope.Any, IDPrefix: "api-retry:s1:"})
	if err != nil || len(got) != 2 || got[0].ID != "api-retry:s1:8:100:1" {
		t.Fatalf("prefix s1: (newest first) %+v %v", got, err)
	}
	// "_" and "%" in the prefix are literal: "s_1:" must not match "sx1:".
	for i, id := range []string{"api-retry:sx1:7:100:1", "api-retry:s_1:7:100:1", "api-retry:s%1:7:100:1", "api-retry:sabc1:7:100:1"} {
		if _, err := st.AddJournal(jEntry(id, "perso", int64(20+i), "api-retry-sent", "")); err != nil {
			t.Fatal(err)
		}
	}
	for prefix, want := range map[string]string{"api-retry:s_1:": "api-retry:s_1:7:100:1", "api-retry:s%1:": "api-retry:s%1:7:100:1"} {
		got, err = st.ListJournal(JournalFilter{Scope: scope.Any, IDPrefix: prefix})
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Fatalf("prefix %q: %+v %v, want only %s", prefix, got, err, want)
		}
	}
}

package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func jBody(id, sc, subj string) pigtail.JournalEntry {
	return pigtail.JournalEntry{ID: id, Scope: sc, SubjectID: subj, Kind: "note",
		Summary: "line " + id, Source: "pigtail:test/" + id}
}

func TestJournalAddValidation(t *testing.T) {
	t.Parallel()
	r, st := scopedFixture(t)
	bad := map[string]pigtail.JournalEntry{
		"no id":      {Kind: "note", Summary: "s", Source: "a:b"},
		"multi-line": {ID: "x", Kind: "note", Summary: "a\nb", Source: "a:b"},
		"281 runes":  {ID: "x", Kind: "note", Summary: strings.Repeat("é", 281), Source: "a:b"},
		"no scheme":  {ID: "x", Kind: "note", Summary: "s", Source: "Perso/x.md"},
		"bad scope":  {ID: "x", Kind: "note", Summary: "s", Source: "a:b", Scope: "work"},
		"bad kind":   {ID: "x", Kind: "Note", Summary: "s", Source: "a:b"},
	}
	for name, body := range bad {
		rec := doAs(t, r, perso, "POST", "/journal", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), string(pigtail.ErrInvalidRequest)) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if out, _ := st.ListJournal(store.JournalFilter{Scope: scope.Any}); len(out) != 0 {
		t.Fatalf("refused rows stored: %+v", out)
	}
	if rec := doAs(t, r, perso, "POST", "/journal", "not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("non-JSON body: %d", rec.Code)
	}
}

func TestJournalAddScopeMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		c         caller
		body      pigtail.JournalEntry
		code      int
		wantScope string
		msg       string // for a refusal: a fragment its body must contain
	}{
		{"no header writes work", none, jBody("a", "", ""), 201, "globex", ""},
		{"work writes work", work, jBody("b", "gx", ""), 201, "globex", ""},
		{"work cannot write perso", work, jBody("c", "perso", ""), 400, "", "not a declared scope"}, // D19
		{"work opt-in does not widen", workCross, jBody("c2", "perso", ""), 400, "", "not a declared scope"},
		{"perso defaults to perso", perso, jBody("d", "", ""), 201, "perso", ""},
		{"perso writing work needs opt-in", perso, jBody("e", "gx", ""), 403, "", ""},
		{"perso writes work with opt-in", persoCross, jBody("f", "gx", ""), 201, "globex", ""},
		{"work on a perso subject = unknown", work, jBody("g", "", "sub_p"), 404, "", ""},
		{"work on an unknown subject", work, jBody("h", "", "sub_nope"), 404, "", ""},
		{"subject sets the scope", work, jBody("i", "", "sub_t"), 201, "globex", ""},
		{"perso on a work subject needs opt-in", perso, jBody("j", "", "sub_t"), 403, "", ""},
		{"perso on a work subject with opt-in", persoCross, jBody("k", "", "sub_t"), 201, "globex", ""},
		{"scope differs from the subject's", perso, jBody("l", "gx", "sub_p"), 400, "", "differs"},
		{"bad scope", work, jBody("m", "nope", ""), 400, "", "nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := scopedFixture(t)
			rec := doAs(t, r, c.c, "POST", "/journal", c.body)
			if rec.Code != c.code {
				t.Fatalf("%s: %d %s", c.c, rec.Code, rec.Body)
			}
			if c.msg != "" && !strings.Contains(rec.Body.String(), c.msg) {
				t.Fatalf("%s: refusal %s does not say %q", c.c, rec.Body, c.msg)
			}
			if c.code != 201 {
				return
			}
			var res pigtail.JournalAddResult
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.Created || res.Entry.Scope != c.wantScope || res.Entry.Timestamp.IsZero() {
				t.Fatalf("result %+v %v", res, err)
			}
		})
	}
	// The perso-subject answer is byte-identical to the unknown-subject one.
	r, _ := scopedFixture(t)
	a := doAs(t, r, work, "POST", "/journal", jBody("m", "", "sub_p")).Body.String()
	b := doAs(t, r, work, "POST", "/journal", jBody("m", "", "sub_zz")).Body.String()
	if a != b {
		t.Fatalf("perso subject distinguishable from unknown:\n%s\n%s", a, b)
	}
}

func TestJournalAddIdempotentHTTP(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	if rec := doAs(t, r, perso, "POST", "/journal", jBody("x", "", "")); rec.Code != 201 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	second := jBody("x", "", "")
	second.Summary = "changed"
	rec := doAs(t, r, perso, "POST", "/journal", second)
	var res pigtail.JournalAddResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || res.Created || res.Entry.Summary != "line x" {
		t.Fatalf("second: %d %+v", rec.Code, res)
	}
}

func TestJournalListScope(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	for i, b := range []struct {
		c caller
		e pigtail.JournalEntry
	}{{work, jBody("t1", "", "sub_t")}, {perso, jBody("p1", "", "sub_p")}, {perso, jBody("p2", "", "")}} {
		b.e.Timestamp = time.UnixMilli(int64(10 + i)) // explicit: the expected order must not depend on the clock
		if rec := doAs(t, r, b.c, "POST", "/journal", b.e); rec.Code != 201 {
			t.Fatalf("seed %s: %d %s", b.e.ID, rec.Code, rec.Body)
		}
	}
	list := func(c caller, q string) (int, string) {
		rec := doAs(t, r, c, "GET", "/journal"+q, nil)
		var out []pigtail.JournalEntry
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		var ids []string
		for _, e := range out {
			ids = append(ids, e.ID)
		}
		return rec.Code, strings.Join(ids, ",")
	}
	for _, c := range []struct {
		c    caller
		q    string
		code int
		want string
	}{
		{none, "", 200, "t1"},
		{work, "?subject=sub_p", 200, ""},
		{work, "?scope=perso", 400, ""}, // D19
		{work, "?scope=all", 403, ""},
		{perso, "", 200, "p2,p1,t1"},
		{perso, "?scope=gx", 200, "t1"},
		{perso, "?limit=0", 400, ""},
		{perso, "?limit=1001", 400, ""},
		{perso, "?since=abc", 400, ""},
		{perso, "?since=5&until=5", 400, ""},
	} {
		code, got := list(c.c, c.q)
		if code != c.code || (code == 200 && got != c.want) {
			t.Errorf("%s GET /journal%s = %d %q, want %d %q", c.c, c.q, code, got, c.code, c.want)
		}
	}
}

// A third scope writes itself; the alias work opens nothing for it.
func TestJournalAddThirdScope(t *testing.T) {
	scopetest.Three(t)
	r, _ := scopedFixture(t)
	acme := caller{scope: "acme"}
	if rec := doAs(t, r, acme, "POST", "/journal", jBody("a", "acme", "")); rec.Code != 201 {
		t.Fatalf("acme writes acme = %d %s", rec.Code, rec.Body)
	}
	// D19: the alias of a scope acme cannot read is an unknown name to it.
	workRec := doAs(t, r, acme, "POST", "/journal", jBody("b", "gx", ""))
	nopeRec := doAs(t, r, acme, "POST", "/journal", jBody("b", "nope", ""))
	if workRec.Code != 400 || strings.ReplaceAll(workRec.Body.String(), "gx", "<N>") != strings.ReplaceAll(nopeRec.Body.String(), "nope", "<N>") {
		t.Fatalf("acme writes work = %d %s, want the 400 of an unknown name (%s)", workRec.Code, workRec.Body, nopeRec.Body)
	}
}

// The --cross-scope hint is for the master caller only: the opt-in never
// widens a non-master caller (scope.CanWrite), so it must not be suggested.
func TestJournalWriteRefusalHint(t *testing.T) {
	scopetest.Three(t)
	master, other := scope.Master(), scope.Scope("acme")
	if m := journalWriteRefusal(master, "globex"); !strings.Contains(m, "--cross-scope") {
		t.Fatalf("master refusal lost its hint: %q", m)
	}
	if m := journalWriteRefusal(other, "globex"); strings.Contains(m, "--cross-scope") {
		t.Fatalf("non-master refusal suggests --cross-scope: %q", m)
	}
	// End to end: the master's 403 carries the hint.
	r, _ := scopedFixture(t)
	if rec := doAs(t, r, perso, "POST", "/journal", jBody("hint", "gx", "")); rec.Code != 403 || !strings.Contains(rec.Body.String(), "--cross-scope") {
		t.Fatalf("master 403 = %d %s", rec.Code, rec.Body)
	}
}

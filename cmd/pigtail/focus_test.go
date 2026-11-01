package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // Europe/Paris for TestParseUntil, whatever the machine has

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestParseUntil(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, loc)
	rfc := time.Date(2026, 10, 20, 7, 30, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"3h":                   now.Add(3 * time.Hour),
		"2d":                   now.Add(48 * time.Hour),
		"2026-10-16":           time.Date(2026, 10, 16, 22, 0, 0, 0, time.UTC), // focus D25: the local midnight after the day
		"2026-10-25":           time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC), // winter time that night: never midnight + 24h
		"2026-10-16 18:00":     time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC),
		"2026-10-20T07:30:00Z": rfc,
	} {
		got, err := parseUntil(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseUntil(%q) = %v, %v; want %v", in, got.UTC(), err, want.UTC())
		}
	}
	if !time.Date(2026, 10, 17, 0, 0, 0, 0, loc).Equal(time.Date(2026, 10, 16, 22, 0, 0, 0, time.UTC)) {
		t.Fatal("fixture: Paris midnight of the 17th is 22:00Z")
	}
	_, err = parseUntil("yesterday", now)
	const want = `--until "yesterday": want a duration (3h, 2d), a date (2026-10-16), a date and time (2026-10-16 18:00) or RFC 3339`
	if err == nil || err.Error() != want {
		t.Fatalf("yesterday = %v, want %q", err, want)
	}
}

// focusRequest is one call the stub daemon saw.
type focusRequest struct {
	method, path, cross string
	body                map[string]any
}

// focusStub serves /focus like the daemon, from a fixed list.
type focusStub struct {
	mu   sync.Mutex
	reqs []focusRequest
	list []pigtail.Focus
	note string
}

func (s *focusStub) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.reqs = append(s.reqs, focusRequest{r.Method, r.URL.Path, r.Header.Get(pigtail.HeaderCrossScope), body})
	s.mu.Unlock()
	find := func(id string) pigtail.Focus {
		for _, f := range s.list {
			if f.ID == id {
				return f
			}
		}
		return pigtail.Focus{ID: id, Name: "?"}
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/focus/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/focus":
		writeData(w, http.StatusOK, map[string]any{"focus": s.list, "note": s.note})
	case r.Method == http.MethodPost && r.URL.Path == "/focus":
		var req struct {
			Name    string                `json:"name"`
			EndsAt  *time.Time            `json:"ends_at"`
			Members []pigtail.FocusMember `json:"members"`
		}
		_ = json.Unmarshal(raw, &req)
		writeData(w, http.StatusCreated, pigtail.Focus{ID: "fcs_new", Name: req.Name, Scope: "perso", EndsAt: req.EndsAt, Members: req.Members, Note: s.note})
	case len(parts) == 2 && parts[1] == "end":
		f := find(parts[0])
		at := time.Now()
		f.EndedAt, f.EndReason = &at, pigtail.FocusEndManual
		writeData(w, http.StatusOK, f)
	default:
		writeData(w, http.StatusOK, find(parts[0]))
	}
}

func runFocus(t *testing.T, s *focusStub, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	var err error
	withStubDaemon(t, &stubDaemon{srv: srv}, func() {
		cmd := focusCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	return out.String(), err
}

func localStamp(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

func TestFocusCreateSendsMembersAndUntil(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s := &focusStub{}
	before := time.Now()
	if _, err := runFocus(t, s, "create", "Deep work", "--theme", "thm_a", "--subject", "sub_b", "--subject", "sub_c", "--until", "3h"); err != nil {
		t.Fatal(err)
	}
	if len(s.reqs) != 1 || s.reqs[0].method != http.MethodPost || s.reqs[0].path != "/focus" {
		t.Fatalf("requests = %+v", s.reqs)
	}
	b := s.reqs[0].body
	if b["name"] != "Deep work" {
		t.Fatalf("name = %v", b["name"])
	}
	ms, _ := json.Marshal(b["members"])
	if string(ms) != `[{"added_at":"0001-01-01T00:00:00Z","id":"thm_a","kind":"theme"},{"added_at":"0001-01-01T00:00:00Z","id":"sub_b","kind":"subject"},{"added_at":"0001-01-01T00:00:00Z","id":"sub_c","kind":"subject"}]` {
		t.Fatalf("members = %s", ms)
	}
	ends, err := time.Parse(time.RFC3339Nano, b["ends_at"].(string))
	if err != nil || ends.Before(before.Add(3*time.Hour-time.Second)) || ends.After(time.Now().Add(3*time.Hour)) {
		t.Fatalf("ends_at = %v, %v", b["ends_at"], err)
	}
	// An unreadable --until stops before any call.
	s2 := &focusStub{}
	if _, err := runFocus(t, s2, "create", "X", "--until", "soon"); err == nil || len(s2.reqs) != 0 {
		t.Fatalf("bad --until: err=%v, %d calls", err, len(s2.reqs))
	}
}

func TestFocusResolveByName(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s := &focusStub{list: []pigtail.Focus{{ID: "fcs_1", Name: "Deep", Scope: "perso", Members: []pigtail.FocusMember{}}}}
	if _, err := runFocus(t, s, "end", "Deep"); err != nil {
		t.Fatal(err)
	}
	last := s.reqs[len(s.reqs)-1]
	if last.method != http.MethodPost || last.path != "/focus/fcs_1/end" {
		t.Fatalf("requests = %+v", s.reqs)
	}
	s.reqs = nil
	if _, err := runFocus(t, s, "end", "fcs_9"); err != nil || s.reqs[0].path != "/focus/fcs_9/end" {
		t.Fatalf("by id: %v, %+v", err, s.reqs)
	}
	if _, err := runFocus(t, s, "end", "Nope"); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("unknown name: %v", err)
	}
}

func TestFocusLsPrintsNote(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s := &focusStub{note: "folders: idle (pigtail cmux sidebar on, --cmux-folders)"}
	out, err := runFocus(t, s, "ls")
	if err != nil || !strings.Contains(out, "folders: idle (pigtail cmux sidebar on, --cmux-folders)\n") {
		t.Fatalf("ls = %q, %v", out, err)
	}
}

func TestFocusOutputsInEnglish(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	ends := time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	s := &focusStub{
		note: "folders: several windows",
		list: []pigtail.Focus{
			{ID: "fcs_1", Name: "Deep", Scope: "perso", EndsAt: &ends, Members: []pigtail.FocusMember{
				{Kind: "theme", ID: "thm_a"}, {Kind: "subject", ID: "sub_b"}, {Kind: "theme", ID: "thm_gone", Missing: true}}},
			{ID: "fcs_2", Name: "Open", Scope: "perso", Members: []pigtail.FocusMember{}},
			{ID: "fcs_3", Name: "Old", Scope: "perso", EndsAt: &ended, EndedAt: &ended, EndReason: "deadline", Members: []pigtail.FocusMember{}},
			{ID: "fcs_4", Name: "Hand", Scope: "perso", EndedAt: &ended, EndReason: "manual", Members: []pigtail.FocusMember{}},
		},
	}
	out, err := runFocus(t, s, "ls", "--all")
	want := "Deep  fcs_1  ends " + localStamp(ends) + "  2 theme(s), 1 subject(s)\n" +
		"  theme thm_gone (not found)\n" +
		"Open  fcs_2  ends manually  0 theme(s), 0 subject(s)\n" +
		"Old  fcs_3  ended (deadline) " + localStamp(ended) + "\n" +
		"Hand  fcs_4  ended (by hand) " + localStamp(ended) + "\n" +
		"folders: several windows\n"
	if err != nil || out != want {
		t.Fatalf("ls --all =\n%s\nwant\n%s(err %v)", out, want, err)
	}
	if _, err := runFocus(t, s, "ls"); err != nil || s.reqs[len(s.reqs)-1].path != "/focus" {
		t.Fatalf("ls: %v", err)
	}

	s.note = ""
	out, err = runFocus(t, s, "create", "Deep work", "--theme", "thm_a", "--subject", "sub_b")
	want = "✓ created fcs_new  Deep work  perso\n" +
		"  ends: manually\n" +
		"  members: thm_a, sub_b\n" +
		"  folder: shows at the next pass while cmux is not focused, once a member has an open session\n"
	if err != nil || out != want {
		t.Fatalf("create =\n%s\nwant\n%s(err %v)", out, want, err)
	}
	s.note = "folders: idle (pigtail cmux sidebar on, --cmux-folders)"
	out, _ = runFocus(t, s, "create", "Noted")
	if !strings.HasSuffix(out, "  note: folders: idle (pigtail cmux sidebar on, --cmux-folders)\n") {
		t.Fatalf("create with note = %q", out)
	}

	out, err = runFocus(t, s, "end", "fcs_1")
	if err != nil || out != "✓ ended fcs_1  Deep (by hand)\n" {
		t.Fatalf("end = %q, %v", out, err)
	}
	out, err = runFocus(t, s, "add", "fcs_1", "--subject", "sub_z")
	if err != nil || out != "✓ updated fcs_1  Deep\n  members: thm_a, sub_b, thm_gone\n" {
		t.Fatalf("add = %q, %v", out, err)
	}
}

func TestFocusCommandsDeclareAccess(t *testing.T) {
	t.Parallel()
	want := map[string]string{"create": "write", "add": "write", "rm": "write", "end": "write", "ls": "read"}
	seen := 0
	for _, c := range focusCmd().Commands() {
		name := c.Name()
		access, ok := want[name]
		if !ok {
			continue
		}
		seen++
		if c.Annotations[scopeAccessKey] != access {
			t.Errorf("%s: access = %q, want %q", name, c.Annotations[scopeAccessKey], access)
		}
		if hasFlag := c.Flags().Lookup("cross-scope") != nil; hasFlag != (access == "write") {
			t.Errorf("%s: --cross-scope declared = %v", name, hasFlag)
		}
	}
	if seen != len(want) {
		t.Fatalf("found %d of %d focus subcommands", seen, len(want))
	}
}

func TestFocusWritesSendCrossScopeOnlyWithFlag(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	for _, args := range [][]string{
		{"add", "fcs_1", "--theme", "thm_a"},
		{"rm", "fcs_1", "--theme", "thm_a"},
		{"end", "fcs_1"},
	} {
		for _, cross := range []bool{false, true} {
			s := &focusStub{list: []pigtail.Focus{{ID: "fcs_1", Name: "Deep", Members: []pigtail.FocusMember{}}}}
			a := append([]string{}, args...)
			if cross {
				a = append(a, "--cross-scope")
			}
			if _, err := runFocus(t, s, a...); err != nil {
				t.Fatalf("%v: %v", a, err)
			}
			want := ""
			if cross {
				want = pigtail.CrossScopeWrite
			}
			for _, r := range s.reqs {
				if r.method != http.MethodGet && r.cross != want {
					t.Errorf("%v: %s %s cross header = %q, want %q", a, r.method, r.path, r.cross, want)
				}
			}
		}
	}
	s := &focusStub{}
	if _, err := runFocus(t, s, "create", "X", "--cross-scope"); err != nil || s.reqs[0].cross != "" {
		t.Fatalf("create --cross-scope sent %+v (%v); the flag is ignored", s.reqs, err)
	}
}

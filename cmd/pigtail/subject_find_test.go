package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const findPaneWS = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"

func seedFindSubjects(t *testing.T, st *store.Store) {
	t.Helper()
	now := time.UnixMilli(1).UTC()
	for _, s := range []pigtail.Subject{
		{ID: "sub_n1", ThemeID: "thm_t", Title: "Notifications push", ShortTitle: "Notif push", Slug: "n1", Status: pigtail.StatusBuilding, CreatedAt: now, UpdatedAt: now},
		{ID: "sub_n2", ThemeID: "thm_t", Title: "Notifications email", Slug: "n2", Status: pigtail.StatusDone, CreatedAt: now, UpdatedAt: now},
	} {
		s := s
		if err := st.CreateSubject(&s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AppendLog(&pigtail.SubjectLogEntry{ID: "log_n1", SubjectID: "sub_n1", Timestamp: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), Kind: "phase", Text: "x"}); err != nil {
		t.Fatal(err)
	}
}

func decodeFind(t *testing.T, out string) pigtail.SubjectFindResult {
	t.Helper()
	var res pigtail.SubjectFindResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout is not one SubjectFindResult: %v\n%s", err, out)
	}
	return res
}

func TestSubjectFindJSON(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	seedFindSubjects(t, st)
	t.Setenv(cmux.EnvWorkspaceID, strings.ToLower(findPaneWS))
	out, err := runSubject(t, s, "find", "notif", "--json")
	if err != nil {
		t.Fatal(err)
	}
	res := decodeFind(t, out)
	if len(res.Candidates) != 2 || res.Candidates[0].SubjectID != "sub_n1" || res.Candidates[0].Theme != "T" || res.FindID == "" {
		t.Fatalf("got %+v", res)
	}
	if f, err := st.GetSubjectFind(res.FindID); err != nil || f.CmuxWorkspaceID != findPaneWS || f.Query != "notif" {
		t.Fatalf("row = %+v, %v (the pane's workspace must be sent)", f, err)
	}
	// Several words join into one query.
	if out, err = runSubject(t, s, "find", "notifications", "email", "--json"); err != nil || decodeFind(t, out).Query != "notifications email" {
		t.Fatalf("words: %v %s", err, out)
	}
	// A query with no searchable word is an error (exit 1), not an empty list.
	if _, err := runSubject(t, s, "find", "le", "de", "--json"); err == nil || !strings.Contains(err.Error(), "no searchable word") {
		t.Fatalf("stop words: %v", err)
	}
}

func TestSubjectFindPlain(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	seedFindSubjects(t, st)
	out, err := runSubject(t, s, "find", "notif")
	if err != nil {
		t.Fatal(err)
	}
	want := "sub_n1\tbuilding\tNotif push\tT\t2026-10-28\nsub_n2\tdone\tNotifications email\tT\t-\n"
	if out != want {
		t.Fatalf("plain = %q, want %q", out, want)
	}
}

func TestSubjectFindLimitFlag(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	seedFindSubjects(t, st)
	for _, l := range []string{"0", "21"} {
		if _, err := runSubject(t, s, "find", "notif", "--limit", l); err == nil || !strings.Contains(err.Error(), "--limit must be 1..20") {
			t.Errorf("--limit %s: %v", l, err)
		}
	}
	out, err := runSubject(t, s, "find", "notif", "--limit", "1", "--json")
	if err != nil || len(decodeFind(t, out).Candidates) != 1 {
		t.Fatalf("--limit 1: %v %s", err, out)
	}
}

func TestSubjectPick(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	seedFindSubjects(t, st)
	out, _ := runSubject(t, s, "find", "notif", "--json")
	fid := decodeFind(t, out).FindID
	out, err := runSubject(t, s, "pick", fid, "sub_n2")
	if err != nil || out != "✓ "+fid+" → sub_n2 (rank 2/2, 1 searches)\n" {
		t.Fatalf("pick: %v %q", err, out)
	}
	if out, err = runSubject(t, s, "pick", fid, "sub_n2"); err != nil || !strings.HasPrefix(out, "= ") {
		t.Fatalf("same pick: %v %q", err, out)
	}
	if _, err = runSubject(t, s, "pick", fid, "sub_n1"); err == nil || !strings.Contains(err.Error(), "already picked") {
		t.Fatalf("other subject: %v", err)
	}
}

func TestSubjectFindOldDaemon(t *testing.T) {
	asWorkCell(t)
	r := chi.NewRouter()
	r.Post("/subjects/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"NOT_FOUND","message":"no such subject"},"meta":{"request_id":"r1"}}`)
	})
	s := &stubDaemon{srv: httptest.NewServer(r)}
	t.Cleanup(s.srv.Close)
	want := "this pigtail daemon has no subject find (restart it after make install)"
	if _, err := runSubject(t, s, "find", "notif", "--json"); err == nil || err.Error() != want {
		t.Fatalf("find on an old daemon: %v", err)
	}
	if _, err := runSubject(t, s, "pick", "sf_0000000000000000", "sub_x"); err == nil || err.Error() != want {
		t.Fatalf("pick on an old daemon: %v", err)
	}
}

func TestSubjectAttachFind(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	seedFindSubjects(t, st)
	out, _ := runSubject(t, s, "find", "notif", "--json")
	fid := decodeFind(t, out).FindID
	if _, err := runSubject(t, s, "attach", "sub_n1", "--workspace", findPaneWS, "--find", fid); err != nil {
		t.Fatal(err)
	}
	if f, _ := st.GetSubjectFind(fid); f.PickedSubjectID != "sub_n1" {
		t.Fatalf("pick not recorded: %+v", f)
	}
}

func TestFindFooter(t *testing.T) {
	cands := []pigtail.SubjectFindCandidate{{SubjectID: "sub_a"}, {SubjectID: "sub_b"}}
	for name, c := range map[string]struct {
		res  pigtail.SubjectFindResult
		want string
	}{
		"candidates":           {pigtail.SubjectFindResult{FindID: "sf_1", Candidates: cands, Total: 2}, "find sf_1: 2 candidates — link: pigtail subject attach --find sf_1 <subject_id>"},
		"candidates elsewhere": {pigtail.SubjectFindResult{FindID: "sf_1", Candidates: cands, Total: 2, Elsewhere: 1}, "find sf_1: 2 candidates (1 elsewhere) — link: pigtail subject attach --find sf_1 <subject_id>"},
		"none":                 {pigtail.SubjectFindResult{FindID: "sf_1", Candidates: []pigtail.SubjectFindCandidate{}}, "find sf_1: no candidate"},
		"none elsewhere":       {pigtail.SubjectFindResult{FindID: "sf_1", Candidates: []pigtail.SubjectFindCandidate{}, Elsewhere: 2}, "find sf_1: no candidate (2 elsewhere)"},
	} {
		if got := findFooter(&c.res); got != c.want {
			t.Errorf("%s: footer = %q, want %q", name, got, c.want)
		}
	}
}

// An old daemon decodes the attach body without knowing find_id: the link is
// made, the pick is not — the CLI must say so.
func TestSubjectAttachFindOldDaemon(t *testing.T) {
	asWorkCell(t)
	r := chi.NewRouter()
	r.Post("/subjects/{id}/cmux-workspaces", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// The real daemon wraps every answer in {"data": …} (client.go c.do).
		_, _ = io.WriteString(w, `{"data":{"cmux_workspace_id":"`+findPaneWS+`","subject_id":"sub_x","source":"attach","bound_at":"2026-10-29T10:00:00Z","title_owner":"pigtail","description_owner":"pigtail"}}`)
	})
	s := &stubDaemon{srv: httptest.NewServer(r)}
	t.Cleanup(s.srv.Close)
	_, err := runSubject(t, s, "attach", "sub_x", "--workspace", findPaneWS, "--find", "sf_0000000000000000")
	if err == nil || !strings.Contains(err.Error(), "the daemon ignored --find") || !strings.Contains(err.Error(), "pigtail subject pick sf_0000000000000000 sub_x") {
		t.Fatalf("old daemon attach --find: %v", err)
	}
}

// Review round 3: the plain output of a pick outside the candidates, and --json.
func TestSubjectPickRankZeroAndJSON(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	seedFindSubjects(t, st)
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_far", ThemeID: "thm_t", Title: "Unrelated board", Slug: "far", Status: pigtail.StatusBuilding, CreatedAt: time.UnixMilli(1).UTC(), UpdatedAt: time.UnixMilli(1).UTC()}); err != nil {
		t.Fatal(err)
	}
	out, _ := runSubject(t, s, "find", "notif", "--json")
	fid := decodeFind(t, out).FindID
	out, err := runSubject(t, s, "pick", fid, "sub_far")
	if err != nil || out != "✓ "+fid+" → sub_far (not among the 2 candidates, 1 searches)\n" {
		t.Fatalf("rank 0: %v %q", err, out)
	}
	out, _ = runSubject(t, s, "find", "notif", "--json")
	fid = decodeFind(t, out).FindID
	out, err = runSubject(t, s, "pick", fid, "sub_n1", "--json")
	var p pigtail.SubjectFindPick
	if err != nil || json.Unmarshal([]byte(out), &p) != nil || p.SubjectID != "sub_n1" || p.Rank != 1 || p.Candidates != 2 || !p.Created {
		t.Fatalf("--json: %v %q %+v", err, out, p)
	}
}

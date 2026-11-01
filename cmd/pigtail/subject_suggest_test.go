package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// suggestOwner9225 seeds a work subject owning #9225 on realSubjectDaemon's store.
func suggestOwner9225(t *testing.T, st *store.Store) {
	t.Helper()
	now := time.UnixMilli(1).UTC()
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_owner", ThemeID: "thm_t", Title: "Wind vane housing", Slug: "owner", Status: pigtail.StatusBuilding, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog(&pigtail.SubjectLogEntry{ID: "log_o1", SubjectID: "sub_owner", Timestamp: now, Kind: "delivery", Text: "globex/firmware#9225 merged"}); err != nil {
		t.Fatal(err)
	}
}

// asWorkCell runs the CLI as a work cell, outside any cmux pane and repo.
func asWorkCell(t *testing.T) {
	t.Helper()
	withCells(t, func(work, _ string) string { return work })
	t.Setenv(cmux.EnvWorkspaceID, "")
	t.Chdir(t.TempDir())
}

func decodeSuggest(t *testing.T, out string) pigtail.SuggestResult {
	t.Helper()
	var res pigtail.SuggestResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stdout is not one SuggestResult: %v\n%s", err, out)
	}
	return res
}

func TestSubjectSuggestJSON(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	suggestOwner9225(t, st)
	out, err := runSubject(t, s, "suggest", "--text", "fix #9225", "--json")
	if err != nil {
		t.Fatal(err)
	}
	res := decodeSuggest(t, out)
	if res.Auto != "sub_owner" || len(res.Candidates) != 1 || !res.Candidates[0].Writable {
		t.Fatalf("got %+v, want auto sub_owner", res)
	}
	// No candidate: still one object, exit 0.
	out, err = runSubject(t, s, "suggest", "--text", "nothing here", "--json")
	if err != nil || !strings.Contains(out, `"candidates":[]`) {
		t.Fatalf("empty: %v %s", err, out)
	}
	// Plain output: one tab-separated line per candidate.
	out, err = runSubject(t, s, "suggest", "--text", "fix #9225")
	if err != nil || out != "strong\tpr\tsub_owner\tWind vane housing\tglobex/firmware#9225\n" {
		t.Fatalf("plain: %v %q", err, out)
	}
}

func TestSubjectSuggestStdin(t *testing.T) {
	asWorkCell(t)
	s, st := realSubjectDaemon(t)
	suggestOwner9225(t, st)
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = rd
	t.Cleanup(func() { os.Stdin = prev })
	_, _ = io.WriteString(wr, "please fix #9225 today\n")
	_ = wr.Close()
	out, err := runSubject(t, s, "suggest", "--text", "-", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if res := decodeSuggest(t, out); res.Auto != "sub_owner" {
		t.Fatalf("stdin: %+v", res)
	}
}

// suggestRecorder serves POST /subjects/suggest and records the body sent.
func suggestRecorder(t *testing.T) (*stubDaemon, *pigtail.SuggestRequest) {
	t.Helper()
	var got pigtail.SuggestRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/subjects/suggest", func(w http.ResponseWriter, r *http.Request) {
		got = pigtail.SuggestRequest{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeData(w, http.StatusOK, pigtail.SuggestResult{Candidates: []pigtail.SuggestCandidate{}})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	return s, &got
}

func TestSubjectSuggestWorkspaceRef(t *testing.T) {
	asWorkCell(t)
	const ws = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"
	prev := resolveCmuxRef
	resolveCmuxRef = func(ref string) (string, error) {
		if ref == "workspace:3" {
			return ws, nil
		}
		return "", errors.New("no such ref")
	}
	t.Cleanup(func() { resolveCmuxRef = prev })
	s, got := suggestRecorder(t)

	if _, err := runSubject(t, s, "suggest", "--text", "x", "--workspace", "workspace:3", "--json"); err != nil || got.WorkspaceID != ws {
		t.Fatalf("workspace:3: %v, sent %q", err, got.WorkspaceID)
	}
	if _, err := runSubject(t, s, "suggest", "--text", "x", "--workspace", "workspace:9", "--json"); err == nil {
		t.Fatal("an unresolved --workspace must fail")
	}
	t.Setenv(cmux.EnvWorkspaceID, strings.ToLower(ws))
	if _, err := runSubject(t, s, "suggest", "--text", "x", "--json"); err != nil || got.WorkspaceID != ws {
		t.Fatalf("env lower case: %v, sent %q", err, got.WorkspaceID)
	}
	t.Setenv(cmux.EnvWorkspaceID, "pas-un-uuid")
	if _, err := runSubject(t, s, "suggest", "--text", "x", "--json"); err != nil || got.WorkspaceID != "" {
		t.Fatalf("invalid env: %v, sent %q", err, got.WorkspaceID)
	}
	// --cwd is sent cleaned and absolute; outside a repo, no branch nor repo.
	dir := t.TempDir()
	if _, err := runSubject(t, s, "suggest", "--text", "x", "--cwd", dir+"/./", "--json"); err != nil {
		t.Fatal(err)
	}
	if got.Cwd == "" || strings.Contains(got.Cwd, "/./") || got.Branch != "" || got.Repo != "" {
		t.Fatalf("cwd sent: %+v", got)
	}
}

func TestSubjectSuggestOldDaemon(t *testing.T) {
	asWorkCell(t)
	serve := func(status int, body string) *stubDaemon {
		r := chi.NewRouter()
		r.Post("/subjects/{id}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		})
		s := &stubDaemon{srv: httptest.NewServer(r)}
		t.Cleanup(s.srv.Close)
		return s
	}
	s := serve(http.StatusNotFound, `{"error":{"code":"NOT_FOUND","message":"no such subject"},"meta":{"request_id":"r1"}}`)
	_, err := runSubject(t, s, "suggest", "--text", "fix #9225", "--json")
	if err == nil || err.Error() != "this pigtail daemon has no subject suggest (restart it after make install)" {
		t.Fatalf("old daemon: %v", err)
	}
	// A bare chi 404/405 (no envelope) reads the same.
	bare := &stubDaemon{srv: httptest.NewServer(chi.NewRouter())}
	t.Cleanup(bare.srv.Close)
	if _, err := runSubject(t, bare, "suggest", "--text", "x", "--json"); err == nil || !strings.Contains(err.Error(), "has no subject suggest") {
		t.Fatalf("bare 404: %v", err)
	}
	// A freeze on the old daemon is an ordinary error.
	s = serve(http.StatusLocked, `{"error":{"code":"MAINTENANCE_FROZEN","message":"pigtail is frozen for maintenance"},"meta":{"request_id":"r2"}}`)
	if _, err := runSubject(t, s, "suggest", "--text", "x", "--json"); err == nil || strings.Contains(err.Error(), "has no subject suggest") {
		t.Fatalf("frozen: %v", err)
	}
}

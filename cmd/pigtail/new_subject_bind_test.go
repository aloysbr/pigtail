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

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const newBindWS = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"

// bindCall is one request the bind daemon saw.
type bindCall struct {
	method, path, scope string
	body                map[string]any
}

// newBindDaemon is newWorkspaceDaemon (new_mainrefresh_test.go) extended to
// the subject routes pigtail new --subject calls: the linked worktree
// (existing = "" → 404), the phases and the cmux link (attachStatus).
func newBindDaemon(t *testing.T, existing string, attachStatus int) (*stubDaemon, func() []bindCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []bindCall
	record := func(r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		calls = append(calls, bindCall{r.Method, r.URL.Path, r.Header.Get(pigtail.HeaderScope), body})
		mu.Unlock()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces/new", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeData(w, http.StatusCreated, pigtail.Workspace{ID: "ws_new", ProjectID: "prj_1", Path: wantPath, DisplayRef: "feat/x", Ports: map[string]int{}})
	})
	mux.HandleFunc("/subjects/sub_1/worktree", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if existing == "" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "NOT_FOUND", "message": "no worktree"}})
			return
		}
		writeData(w, http.StatusOK, pigtail.Workspace{ID: "ws_old", Path: existing})
	})
	mux.HandleFunc("/subjects/sub_1/phases", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeData(w, http.StatusCreated, pigtail.SubjectPhase{ID: "phs_1", SubjectID: "sub_1"})
	})
	mux.HandleFunc("/subjects/sub_1/cmux-workspaces", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if attachStatus == http.StatusConflict {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "CONFLICT", "message": "bound to another subject"}})
			return
		}
		writeData(w, http.StatusCreated, pigtail.SubjectCmuxBinding{CmuxWorkspaceID: newBindWS, SubjectID: "sub_1"})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	return s, func() []bindCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]bindCall(nil), calls...)
	}
}

func runNewBind(t *testing.T, s *stubDaemon, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	withStubDaemon(t, s, func() {
		cmd := newCmd()
		cmd.SetArgs(append([]string{"an intent", "--project", "prj_1"}, args...))
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("pigtail new: %v", err)
		}
	})
	return out.String(), errOut.String()
}

func callsTo(calls []bindCall, method, suffix string) []bindCall {
	var out []bindCall
	for _, c := range calls {
		if c.method == method && strings.HasSuffix(c.path, suffix) {
			out = append(out, c)
		}
	}
	return out
}

func asWorkPane(t *testing.T, ws string) {
	t.Helper()
	withCells(t, func(work, _ string) string { return work })
	t.Setenv(cmux.EnvWorkspaceID, ws)
}

func TestNewSubjectReuseBinds(t *testing.T) {
	asWorkPane(t, strings.ToLower(newBindWS))
	existing := t.TempDir()
	s, calls := newBindDaemon(t, existing, http.StatusCreated)
	stdout, stderr := runNewBind(t, s, "--subject", "sub_1", "--reuse")
	if stdout != existing+"\n" {
		t.Fatalf("stdout = %q, want the path alone", stdout)
	}
	links := callsTo(calls(), "POST", "/cmux-workspaces")
	if len(links) != 1 || links[0].body["cmux_workspace_id"] != newBindWS || links[0].body["source"] != pigtail.CmuxSourcePtnew {
		t.Fatalf("links = %+v", links)
	}
	if len(callsTo(calls(), "POST", "/phases")) != 0 {
		t.Fatal("a reused worktree is already linked: no phase")
	}
	if !strings.Contains(stderr, "✓ cmux workspace bound to subject sub_1") {
		t.Fatalf("stderr = %q", stderr)
	}
	// --quiet cuts the success message, not the link.
	s, calls = newBindDaemon(t, existing, http.StatusCreated)
	if _, stderr := runNewBind(t, s, "--subject", "sub_1", "--reuse", "--quiet"); stderr != "" || len(callsTo(calls(), "POST", "/cmux-workspaces")) != 1 {
		t.Fatalf("--quiet: stderr %q", stderr)
	}
}

func TestNewSubjectCreateBindsPhaseAndWorkspace(t *testing.T) {
	asWorkPane(t, newBindWS)
	s, calls := newBindDaemon(t, "", http.StatusCreated)
	stdout, _ := runNewBind(t, s, "--subject", "sub_1", "--quiet")
	if stdout != wantPath+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	all := calls()
	phases, links := callsTo(all, "POST", "/phases"), callsTo(all, "POST", "/cmux-workspaces")
	if len(phases) != 1 || phases[0].body["workspace_id"] != "ws_new" || phases[0].body["kind"] != pigtail.PhaseImplement || phases[0].body["state"] != pigtail.PhaseActive {
		t.Fatalf("phases = %+v", phases)
	}
	if len(links) != 1 {
		t.Fatalf("links = %+v", links)
	}
	iPhase, iLink := -1, -1
	for i, c := range all {
		switch {
		case c.method == "POST" && strings.HasSuffix(c.path, "/phases"):
			iPhase = i
		case c.method == "POST" && strings.HasSuffix(c.path, "/cmux-workspaces"):
			iLink = i
		}
	}
	if iPhase < 0 || iLink < iPhase {
		t.Fatalf("phase must come before the link: %+v", all)
	}
}

func TestNewSubjectForceNewBinds(t *testing.T) {
	asWorkPane(t, newBindWS)
	s, calls := newBindDaemon(t, t.TempDir(), http.StatusCreated)
	runNewBind(t, s, "--subject", "sub_1", "--force-new", "--quiet")
	all := calls()
	phases, links := callsTo(all, "POST", "/phases"), callsTo(all, "POST", "/cmux-workspaces")
	if len(phases) != 1 || len(links) != 1 {
		t.Fatalf("calls = %+v", all)
	}
	for _, c := range append(phases, links...) {
		if c.scope == "" {
			t.Fatalf("no scope header on %s %s", c.method, c.path)
		}
	}
}

func TestNewSubjectNoCmux(t *testing.T) {
	for _, ws := range []string{"", "pas-un-uuid"} {
		t.Run("CMUX="+ws, func(t *testing.T) {
			asWorkPane(t, ws)
			s, calls := newBindDaemon(t, "", http.StatusCreated)
			stdout, stderr := runNewBind(t, s, "--subject", "sub_1", "--quiet")
			if stdout != wantPath+"\n" {
				t.Fatalf("stdout = %q", stdout)
			}
			all := calls()
			if p := callsTo(all, "POST", "/phases"); len(p) != 1 || p[0].body["workspace_id"] != "ws_new" {
				t.Fatalf("phases = %+v", p)
			}
			if l := callsTo(all, "POST", "/cmux-workspaces"); len(l) != 0 {
				t.Fatalf("a link without a cmux UUID: %+v", l)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q", stderr)
			}
		})
	}
}

func TestNewSubjectBindConflict(t *testing.T) {
	asWorkPane(t, newBindWS)
	s, _ := newBindDaemon(t, "", http.StatusConflict)
	stdout, stderr := runNewBind(t, s, "--subject", "sub_1", "--quiet")
	if stdout != wantPath+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	want := "warning: cmux workspace already bound to another subject — not replaced (pigtail subject attach sub_1 --replace)\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

// A stale $CMUX_WORKSPACE_ID (cmux answers caller: null): the link is not
// written, one warning, no attach proposal (it would be refused too), and the
// command keeps its output and exit code.
func TestNewSubjectStaleCmuxWorkspaceNotBound(t *testing.T) {
	asWorkPane(t, newBindWS)
	fakeCmuxCLI(t, identifyScript(identifyUnknown))
	s, calls := newBindDaemon(t, "", http.StatusCreated)
	stdout, stderr := runNewBind(t, s, "--subject", "sub_1", "--quiet")
	if stdout != wantPath+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	all := calls()
	if n := len(callsTo(all, "POST", "/phases")); n != 1 {
		t.Fatalf("phases = %d, want 1", n)
	}
	if n := len(callsTo(all, "POST", "/cmux-workspaces")); n != 0 {
		t.Fatalf("a stale id was linked (%d POSTs)", n)
	}
	if want := "warning: " + cmuxStaleMessage(newBindWS, true) + "\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

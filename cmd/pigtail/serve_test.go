package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// stubDaemon is a minimal HTTP daemon that records the last create request and
// serves canned static-server responses in the {"data":...} envelope.
type stubDaemon struct {
	srv         *httptest.Server
	lastCreate  pigtail.CreateStaticServerRequest
	servers     []pigtail.StaticServerView
	deletedName string
	startedName string // last name hit on .../{name}/start
	stoppedName string // last name hit on .../{name}/stop
	stopAllHit  bool   // /static-servers/stop-all was called
	// workspaces serves GET /workspaces/{id} (404 WORKSPACE_NOT_FOUND otherwise).
	workspaces map[string]pigtail.Workspace
	// paths records every request realSubjectDaemonWith serves, as
	// "METHOD /path", in order (under pathsMu: handlers run concurrently).
	paths   []string
	pathsMu sync.Mutex
}

func newStubDaemon(t *testing.T) *stubDaemon {
	t.Helper()
	s := &stubDaemon{}
	mux := http.NewServeMux()
	mux.HandleFunc("/static-servers", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &s.lastCreate)
			ss := pigtail.StaticServer{
				ID:   "ss_test",
				Name: s.lastCreate.Name,
				Dir:  s.lastCreate.Dir,
				Port: 18080,
			}
			if s.lastCreate.Port != 0 {
				ss.Port = s.lastCreate.Port
			}
			writeData(w, http.StatusCreated, ss)
		case http.MethodGet:
			writeData(w, http.StatusOK, s.servers)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/static-servers/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/static-servers/")
		// DELETE /static-servers/{name}
		if r.Method == http.MethodDelete {
			s.deletedName = rest
			writeData(w, http.StatusOK, map[string]any{"name": s.deletedName, "id": "ss_test"})
			return
		}
		if r.Method == http.MethodPost {
			switch {
			case rest == "stop-all":
				s.stopAllHit = true
				writeData(w, http.StatusOK, map[string]any{"stopped": []string{"docs"}})
				return
			case strings.HasSuffix(rest, "/start"):
				name := strings.TrimSuffix(rest, "/start")
				s.startedName = name
				view := s.viewFor(name)
				view.Running = true
				view.RunningPort = view.Port
				writeData(w, http.StatusOK, view)
				return
			case strings.HasSuffix(rest, "/stop"):
				s.stoppedName = strings.TrimSuffix(rest, "/stop")
				writeData(w, http.StatusOK, map[string]any{"name": s.stoppedName})
				return
			}
		}
		http.Error(w, "method", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/workspaces/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/workspaces/")
		if ws, ok := s.workspaces[id]; ok && r.Method == http.MethodGet {
			writeData(w, http.StatusOK, ws)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(pigtail.ErrorResponse{Error: pigtail.APIError{Code: pigtail.ErrWorkspaceNotFound, Message: "no such workspace"}})
	})
	// /projects lets out-of-project target resolution reach a clean
	// "no worktree" verdict (empty registry) so the static fallback engages.
	mux.HandleFunc("/projects", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, []pigtail.Project{})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// viewFor returns the registered view for name, or a synthetic one on a
// default port so /start always has something to echo back.
func (s *stubDaemon) viewFor(name string) pigtail.StaticServerView {
	for _, v := range s.servers {
		if v.Name == name {
			return v
		}
	}
	return pigtail.StaticServerView{StaticServer: pigtail.StaticServer{Name: name, Port: 18080}}
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": map[string]any{}})
}

// withStubDaemon points the package-global client flags at the stub for the
// duration of fn, restoring them afterwards.
func withStubDaemon(t *testing.T, s *stubDaemon, fn func()) {
	t.Helper()
	testutil.SerialOnly(t) // swaps flagHTTP
	prevHTTP, prevSocket := flagHTTP, flagSocket
	// httptest URL is like http://127.0.0.1:PORT — newClient wants a bare addr.
	flagHTTP = strings.TrimPrefix(s.srv.URL, "http://")
	flagSocket = ""
	t.Cleanup(func() { flagHTTP, flagSocket = prevHTTP, prevSocket })
	fn()
}

func TestServeAddResolvesRelativeDirToAbsolute(t *testing.T) {
	s := newStubDaemon(t)
	withStubDaemon(t, s, func() {
		cmd := serveCmd()
		cmd.SetArgs([]string{"add", "docs", "."})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("serve add: %v", err)
		}
	})
	if s.lastCreate.Name != "docs" {
		t.Errorf("name = %q, want docs", s.lastCreate.Name)
	}
	if !filepath.IsAbs(s.lastCreate.Dir) {
		t.Errorf("dir = %q, want absolute", s.lastCreate.Dir)
	}
}

func TestServeAddPassesPort(t *testing.T) {
	s := newStubDaemon(t)
	withStubDaemon(t, s, func() {
		cmd := serveCmd()
		cmd.SetArgs([]string{"add", "docs", ".", "--port", "9123"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("serve add: %v", err)
		}
	})
	if s.lastCreate.Port != 9123 {
		t.Errorf("port = %d, want 9123", s.lastCreate.Port)
	}
}

func TestServeAddRequiresTwoArgs(t *testing.T) {
	t.Parallel()
	cmd := serveCmd()
	cmd.SetArgs([]string{"add", "onlyname"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when dir is missing")
	}
}

func TestServeRmCallsDelete(t *testing.T) {
	s := newStubDaemon(t)
	withStubDaemon(t, s, func() {
		cmd := serveCmd()
		cmd.SetArgs([]string{"rm", "docs"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("serve rm: %v", err)
		}
	})
	if s.deletedName != "docs" {
		t.Errorf("deleted = %q, want docs", s.deletedName)
	}
}

func TestServeLsRendersTable(t *testing.T) {
	s := newStubDaemon(t)
	s.servers = []pigtail.StaticServerView{
		{StaticServer: pigtail.StaticServer{Name: "docs", Dir: "/tmp/docs", Port: 18080}, Running: true, RunningPort: 18080},
		{StaticServer: pigtail.StaticServer{Name: "assets", Dir: "/tmp/assets", Port: 18081}},
	}
	withStubDaemon(t, s, func() {
		cmd := serveCmd()
		cmd.SetArgs([]string{"ls"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("serve ls: %v", err)
		}
	})
}

package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/staticsrv"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// freeLoopbackAddr returns a 127.0.0.1 address whose port was just free.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// waitOutputsBase polls the bound outputs address the daemon publishes.
func waitOutputsBase(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b := srv.outputsBase(); b != "" {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

func seedOutputsSubject(t *testing.T, st *store.Store) string {
	t.Helper()
	now := time.Now()
	sc := string(scope.Active().Default())
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_out", Name: "Out", Kind: "ongoing", Scope: sc, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_out", ThemeID: "thm_out", Title: "Outputs", Slug: "outputs", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return "sub_out"
}

func postOutput(t *testing.T, srv *Server, subjectID string, o pigtail.SubjectOutput) {
	t.Helper()
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(o)
	req := httptest.NewRequest(http.MethodPost, "/subjects/"+subjectID+"/outputs", &buf)
	req.RemoteAddr = "" // the unix socket
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST output = %d %s", rec.Code, rec.Body.String())
	}
}

func getURL(t *testing.T, u string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(u) //nolint:gosec // loopback test URL
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestOutputsListenerServesRegisteredMockupAfterRestart(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	dir := t.TempDir()
	files, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mock := filepath.Join(files, "proj", "mockup", "t1", "index.html")
	stray := filepath.Join(files, "other", "secret.html")
	for p, body := range map[string]string{mock: "<h1>mock t1</h1>", stray: "STRAY"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(dir, "p.db")
	httpAddr, outAddr := freeLoopbackAddr(t), freeLoopbackAddr(t)
	start := func(seed bool) (*Server, func()) {
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		srv, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: httpAddr, OutputsAddr: outAddr}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if seed {
			id := seedOutputsSubject(t, st)
			postOutput(t, srv, id, pigtail.SubjectOutput{Kind: "mockup", Path: mock})
		}
		stop := startDaemon(t, srv)
		return srv, func() { stop(); _ = st.Close() }
	}

	srv, stop := start(true)
	base := waitOutputsBase(t, srv)
	if base != "http://"+outAddr {
		stop()
		t.Fatalf("outputsBase = %q; want http://%s", base, outAddr)
	}
	code, body, _ := getURL(t, base+staticsrv.ServedPath(mock))
	if code != http.StatusOK || !strings.Contains(body, "mock t1") {
		stop()
		t.Fatalf("registered mockup = %d %q; want 200", code, body)
	}
	if code, _, _ := getURL(t, base+staticsrv.ServedPath(stray)); code != http.StatusNotFound {
		stop()
		t.Fatalf("unregistered file = %d; want 404", code)
	}
	stop()

	srv, stop = start(false)
	defer stop()
	base = waitOutputsBase(t, srv)
	if code, body, _ := getURL(t, base+staticsrv.ServedPath(mock)); code != http.StatusOK || !strings.Contains(body, "mock t1") {
		t.Fatalf("after restart = %d %q; want 200", code, body)
	}
	// The other listener does not serve that path.
	if code, body, _ := getURL(t, "http://"+httpAddr+staticsrv.ServedPath(mock)); code == http.StatusOK && strings.Contains(body, "mock t1") {
		t.Fatalf("the HTTP API listener served the mockup")
	}
}

func TestOutputsListenerRefusesSameAddrAsHTTP(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	_, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: "127.0.0.1:7802", OutputsAddr: "127.0.0.1:7802"}, st, events.New(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "outputs-http") {
		t.Fatalf("New with the same address = %v; want an outputs-http error", err)
	}
}

func TestOutputsListenerRefusesNonLoopback(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	for _, addr := range []string{"0.0.0.0:7802", ":7802", "192.168.1.10:7802", "example.com:7802", "nonsense"} {
		_, err := New(Config{SocketPath: testSocketPath(t), OutputsAddr: addr}, st, events.New(), nil, nil)
		if err == nil {
			t.Errorf("New with outputs %q = nil; want a refusal", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if err := ValidateOutputsAddr(addr, ""); err != nil {
			t.Errorf("ValidateOutputsAddr(%q) = %v; want ok", addr, err)
		}
	}
}

// logCapture collects log lines for the test's duration.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func captureLog(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := log.Writer()
	log.SetOutput(c)
	t.Cleanup(func() { log.SetOutput(prev) })
	return c
}

func TestOutputsListenerBindFailureIsNonFatal(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	logs := captureLog(t)
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t), OutputsAddr: held.Addr().String()}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := startDaemon(t, srv)
	defer stop()
	// The daemon is up: its socket answers.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c, err := net.Dial("unix", srv.cfg.SocketPath); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never came up after the outputs bind failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	testutil.Eventually(t, 3*time.Second, "the failed outputs bind is logged", func() bool {
		return strings.Contains(logs.String(), "outputs:")
	})
	if b := srv.outputsBase(); b != "" {
		t.Fatalf("outputsBase = %q after a failed bind; want empty", b)
	}
	if n := strings.Count(logs.String(), "outputs:"); n != 1 {
		t.Fatalf("log has %d outputs lines; want 1:\n%s", n, logs.String())
	}

	// A bind that works hands back the address really bound.
	st2, _ := store.Open(":memory:")
	defer st2.Close()
	srv2, err := New(Config{SocketPath: testSocketPath(t), OutputsAddr: "127.0.0.1:0"}, st2, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop2 := startDaemon(t, srv2)
	defer stop2()
	b := waitOutputsBase(t, srv2)
	if b == "" || strings.HasSuffix(b, ":0") || !strings.HasPrefix(b, "http://127.0.0.1:") {
		t.Fatalf("outputsBase = %q; want the bound http://127.0.0.1:<port>", b)
	}
}

func TestOutputsListenerOffWhenHTTPEmpty(t *testing.T) {
	for _, c := range []struct {
		http, flag string
		explicit   bool
		want       string
	}{
		{"127.0.0.1:7800", "", false, DefaultOutputsAddr},
		{"127.0.0.1:7800", "", true, ""},
		{"127.0.0.1:7800", "127.0.0.1:7802", true, "127.0.0.1:7802"},
		{"", "", false, ""},
		{"", "", true, ""},
		{"", "127.0.0.1:7802", true, "127.0.0.1:7802"},
	} {
		if got := ResolveOutputsAddr(c.http, c.flag, c.explicit); got != c.want {
			t.Errorf("ResolveOutputsAddr(%q, %q, %v) = %q; want %q", c.http, c.flag, c.explicit, got, c.want)
		}
	}
	// A daemon with HTTP off and no outputs address binds nothing.
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := startDaemon(t, srv)
	defer stop()
	time.Sleep(100 * time.Millisecond)
	if b := srv.outputsBase(); b != "" {
		t.Fatalf("outputsBase = %q with the listener off; want empty", b)
	}
}

func TestOutputsListenerFrameAncestorsNoneWithoutHTTP(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	files, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mock := filepath.Join(files, "p", "t1", "index.html")
	if err := os.MkdirAll(filepath.Dir(mock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mock, []byte("M"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t), OutputsAddr: freeLoopbackAddr(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	postOutput(t, srv, seedOutputsSubject(t, st), pigtail.SubjectOutput{Kind: "mockup", Path: mock})
	stop := startDaemon(t, srv)
	defer stop()
	base := waitOutputsBase(t, srv)
	code, _, hdr := getURL(t, base+staticsrv.ServedPath(mock))
	if code != http.StatusOK {
		t.Fatalf("GET = %d; want 200", code)
	}
	if got := hdr.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Fatalf("CSP = %q; want frame-ancestors 'none'", got)
	}
}

// getRouterJSON reads a socket route of srv into v.
func getRouterJSON(t *testing.T, srv *Server, path string, v any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "" // the unix socket
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
	}
	env := struct {
		Data any `json:"data"`
	}{Data: v}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

// TestServedURLOnlyForRowsTheServerOpens: served_url comes from the outputs
// server's own index — a row it refuses (an html in $HOME, a report in a
// folder holding .git) carries none, in the listing and in the groups, while
// a round it serves does, and that URL answers 200.
func TestServedURLOnlyForRowsTheServerOpens(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	files, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(files, "home")
	t.Setenv("HOME", home)
	inHome := filepath.Join(home, "index.html")
	inRepo := filepath.Join(files, "repo", "report.html")
	round := filepath.Join(files, "proj", "mockup", "t1", "index.html")
	for _, p := range []string{inHome, inRepo, round} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("<h1>x</h1>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(files, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t), HTTPAddr: freeLoopbackAddr(t), OutputsAddr: freeLoopbackAddr(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := seedOutputsSubject(t, st)
	postOutput(t, srv, id, pigtail.SubjectOutput{Kind: "html", Path: inHome})
	postOutput(t, srv, id, pigtail.SubjectOutput{Kind: "report", Path: inRepo})
	postOutput(t, srv, id, pigtail.SubjectOutput{Kind: "mockup", Path: round})
	stop := startDaemon(t, srv)
	defer stop()
	if waitOutputsBase(t, srv) == "" {
		t.Fatal("outputs listener not bound")
	}

	served := map[string]string{}
	var outs []pigtail.SubjectOutput
	getRouterJSON(t, srv, "/subjects/"+id+"/outputs", &outs)
	for _, o := range outs {
		served[o.Path] = o.ServedURL
	}
	var groups []pigtail.OutputGroup
	getRouterJSON(t, srv, "/subjects/"+id+"/outputs/groups", &groups)
	inGroups := map[string]string{}
	for _, g := range groups {
		for _, tr := range g.Tours {
			inGroups[tr.Path] = tr.ServedURL
		}
	}
	for _, p := range []string{inHome, inRepo} {
		if served[p] != "" {
			t.Errorf("listing: refused %s carries served_url %q; want none", p, served[p])
		}
		if inGroups[p] != "" {
			t.Errorf("groups: refused %s carries served_url %q; want none", p, inGroups[p])
		}
	}
	if served[round] == "" || inGroups[round] != served[round] {
		t.Fatalf("round served_url: listing %q, groups %q; want the same non-empty URL", served[round], inGroups[round])
	}
	if code, _, _ := getURL(t, served[round]); code != http.StatusOK {
		t.Fatalf("GET %s = %d; want 200", served[round], code)
	}
}

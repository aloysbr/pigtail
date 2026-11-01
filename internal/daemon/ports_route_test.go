package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// portFileBackend is fakeBackend with a .env.local, PORT=<dev>. Pointer
// receivers throughout: fakeBackend's methods are on *fakeBackend, so only
// *portFileBackend satisfies backend.Backend.
type portFileBackend struct{ fakeBackend }

func (*portFileBackend) ReadPorts(ws *pigtail.Workspace, kinds []string, envs map[string]string) (map[string]int, error) {
	b, err := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	if err != nil {
		return nil, err
	}
	var p int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(b)), "PORT=%d", &p)
	return map[string]int{"dev": p}, nil
}

func (*portFileBackend) WritePorts(ws *pigtail.Workspace, p map[string]int, _ map[string]string) (func() error, error) {
	path := filepath.Join(ws.Path, ".env.local")
	prev, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(fmt.Sprintf("PORT=%d\n", p["dev"])), 0o644); err != nil {
		return nil, err
	}
	return func() error { return os.WriteFile(path, prev, 0o644) }, nil
}

var (
	_ backend.Backend  = (*portFileBackend)(nil)
	_ backend.PortFile = (*portFileBackend)(nil)
)

type portsFixture struct {
	srv   *Server
	st    *store.Store
	alloc *ports.Allocator
	ws    string // path of ws_a
}

func newPortsFixture(t *testing.T, envPort, dbPort int) portsFixture {
	t.Helper()
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	alloc := ports.New(st, ports.WithBindProbe(func(int) bool { return true }), ports.WithEnvSources(StoreEnvSources(st)))
	// No real lsof in route tests: nothing listens, unless a test swaps these.
	srv, err := New(Config{SocketPath: testSocketPath(t), Allocator: alloc,
		PortBusy:    func(int) (bool, []int, error) { return false, nil, nil },
		ProcessCwds: func([]int) (map[int]string, error) { return map[int]string{}, nil },
	}, st, events.New(), &portFileBackend{}, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := st.CreateProject(&pigtail.Project{ID: "prj", Name: "p", RootPath: root, DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {39500, 39599}}}); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(root, "a")
	_ = os.MkdirAll(a, 0o755)
	_ = os.WriteFile(filepath.Join(a, ".env.local"), []byte(fmt.Sprintf("PORT=%d\n", envPort)), 0o644)
	if err := st.CreateWorkspace(&pigtail.Workspace{ID: "ws_a", ProjectID: "prj", Path: a,
		Ports: map[string]int{"dev": dbPort}, State: pigtail.StateBuilding, BackendKind: "fake"}); err != nil {
		t.Fatal(err)
	}
	return portsFixture{srv: srv, st: st, alloc: alloc, ws: a}
}

func patchPorts(t *testing.T, srv *Server, id string, req pigtail.PatchPortsRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	srv.buildRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/workspaces/"+id+"/ports", strings.NewReader(string(body))))
	return rec
}

func TestPortsResyncCopiesEnvLocalIntoTheRegistry(t *testing.T) {
	f := newPortsFixture(t, 39513, 39500)
	rec := patchPorts(t, f.srv, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	ws, _ := f.st.GetWorkspace("ws_a")
	if ws.Ports["dev"] != 39513 {
		t.Fatalf("registry %v, want dev=39513", ws.Ports)
	}
}

func TestPortsResyncRefusesAPortAnotherWorkspaceHolds(t *testing.T) {
	f := newPortsFixture(t, 39513, 39500)
	other := filepath.Join(filepath.Dir(f.ws), "b")
	_ = os.MkdirAll(other, 0o755)
	if err := f.st.CreateWorkspace(&pigtail.Workspace{ID: "ws_b", ProjectID: "prj", Path: other,
		Ports: map[string]int{"dev": 39513}, State: pigtail.StateBuilding, BackendKind: "fake"}); err != nil {
		t.Fatal(err)
	}
	rec := patchPorts(t, f.srv, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ws_b") {
		t.Fatalf("status %d body %s, want 409 naming ws_b", rec.Code, rec.Body)
	}
	if ws, _ := f.st.GetWorkspace("ws_a"); ws.Ports["dev"] != 39500 {
		t.Fatalf("registry changed on a refusal: %v", ws.Ports)
	}
}

func TestPortsReassignAllocatesWritesAndPersists(t *testing.T) {
	f := newPortsFixture(t, 39500, 39500)
	rec := patchPorts(t, f.srv, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: []string{"dev"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	ws, _ := f.st.GetWorkspace("ws_a")
	b, _ := os.ReadFile(filepath.Join(f.ws, ".env.local"))
	if ws.Ports["dev"] == 39500 || !strings.Contains(string(b), fmt.Sprintf("PORT=%d", ws.Ports["dev"])) {
		t.Fatalf("registry %v, file %q", ws.Ports, b)
	}
	if f.alloc.Pending("dev")[ws.Ports["dev"]] {
		t.Fatal("reassigned port left pending")
	}
}

// resync applies allocation's "taken" rules: a port another live
// workspace's .env.local names, or one pending in the shared allocator, is
// refused like a registered one.
func TestPortsResyncRefusesAPortNamedElsewhereOrPending(t *testing.T) {
	f := newPortsFixture(t, 39513, 39500)
	other := filepath.Join(filepath.Dir(f.ws), "b")
	_ = os.MkdirAll(other, 0o755)
	_ = os.WriteFile(filepath.Join(other, ".env.local"), []byte("PORT=39513\n"), 0o644)
	if err := f.st.CreateWorkspace(&pigtail.Workspace{ID: "ws_b", ProjectID: "prj", Path: other,
		Ports: map[string]int{"dev": 39550}, State: pigtail.StateBuilding, BackendKind: "fake"}); err != nil {
		t.Fatal(err)
	}
	rec := patchPorts(t, f.srv, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "env_local") {
		t.Fatalf("named in ws_b's .env.local: status %d body %s", rec.Code, rec.Body)
	}

	g := newPortsFixture(t, 39520, 39500)
	if err := g.alloc.Reserve("dev", 39520, g.ws, 39500); err != nil { // in flight (as if another create held it); self = ws_a so its own .env.local does not refuse first
		t.Fatal(err)
	}
	rec = patchPorts(t, g.srv, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "pending") {
		t.Fatalf("pending port: status %d body %s", rec.Code, rec.Body)
	}
	if ws, _ := g.st.GetWorkspace("ws_a"); ws.Ports["dev"] != 39500 {
		t.Fatalf("registry changed on a refusal: %v", ws.Ports)
	}
}

func TestPortsResyncConfirmsItsReservation(t *testing.T) {
	f := newPortsFixture(t, 39513, 39500)
	if rec := patchPorts(t, f.srv, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync}); rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(f.alloc.Pending("dev")) != 0 {
		t.Fatalf("reservation left pending: %v", f.alloc.Pending("dev"))
	}
}

func TestPortsReassignRejectsUnknownKind(t *testing.T) {
	f := newPortsFixture(t, 39500, 39500)
	if rec := patchPorts(t, f.srv, "ws_a", pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: []string{"nope"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

// listening is a scripted machine shared by rammon and the ports handlers:
// port → (pid, cwd). Stopping a server removes its entry.
type listening map[int]struct {
	pid int
	cwd string
}

func (l listening) busy(port int) (bool, []int, error) {
	if e, ok := l[port]; ok {
		return true, []int{e.pid}, nil
	}
	return false, nil, nil
}

func (l listening) cwds(pids []int) (map[int]string, error) {
	out := map[int]string{}
	for _, e := range l {
		out[e.pid] = e.cwd
	}
	return out, nil
}

// wireMachine points the fixture's handlers and a fresh RAM monitor at l.
// Servers are read from .env.local (PORT=) against the registry, as
// defaultServersFor does.
func wireMachine(f portsFixture, l listening) {
	f.srv.cfg.PortBusy, f.srv.cfg.ProcessCwds = l.busy, l.cwds
	f.srv.ramMonitor = rammon.New(events.New(), f.st,
		rammon.WithLogger(func(string, ...any) {}),
		rammon.WithPortPIDs(l.busy), rammon.WithCwds(l.cwds),
		rammon.WithServersFunc(func(ws pigtail.Workspace) []rammon.ServerProc {
			b, _ := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
			var p int
			_, _ = fmt.Sscanf(strings.TrimSpace(string(b)), "PORT=%d", &p)
			if p == 0 {
				p = ws.Ports["dev"]
			}
			return []rammon.ServerProc{{Name: "dev", Kind: "dev", Port: p, DBPort: ws.Ports["dev"]}}
		}))
}

func getServers(t *testing.T, srv *Server) pigtail.ServersResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.buildRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/servers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /servers: %d %s", rec.Code, rec.Body)
	}
	var env struct {
		Data pigtail.ServersResponse `json:"data"` // the daemon's response envelope (writeJSON)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func fixOf(t *testing.T, resp pigtail.ServersResponse, ws, reason string) string {
	t.Helper()
	for _, c := range resp.PortConflicts {
		if c.WorkspaceID == ws && c.Reason == reason {
			return c.Fix
		}
	}
	t.Fatalf("no %s conflict for %s in %+v", reason, ws, resp.PortConflicts)
	return ""
}

// runFix replays a fix the way a shell would run it, each " && " step onto
// its route: ports commands → PATCH /workspaces/{id}/ports, the sub-shell
// "(cd X && pigtail stop --server S …)" → stop(X, S) for each S, the cd
// scoped to the sub-shell. Any step that fails, or that this test cannot
// replay, fails the test: a fix must run as shown. (Test paths are plain
// words: quoteWord leaves them unquoted.)
func runFix(t *testing.T, srv *Server, fix string, stop func(dir, server string)) {
	t.Helper()
	var dir string
	for _, step := range strings.Split(fix, " && ") {
		opens, closes := strings.HasPrefix(step, "("), strings.HasSuffix(step, ")")
		w := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(step, "("), ")"))
		switch {
		case opens && len(w) == 2 && w[0] == "cd":
			dir = w[1]
		case len(w) == 4 && w[0] == "pigtail" && w[1] == "stop" && w[2] == "--server" && dir != "":
			stop(dir, w[3])
			if closes {
				dir = "" // the sub-shell ends: the cd does not outlive it
			}
		case len(w) == 4 && w[0] == "pigtail" && w[1] == "ports" && w[2] == "resync":
			if rec := patchPorts(t, srv, w[3], pigtail.PatchPortsRequest{Mode: pigtail.PortsModeResync}); rec.Code != http.StatusOK {
				t.Fatalf("fix %q, step %q: %d %s", fix, step, rec.Code, rec.Body)
			}
		case len(w) >= 6 && w[0] == "pigtail" && w[1] == "ports" && w[2] == "reassign" && len(w[4:])%2 == 0:
			var kinds []string
			for i := 4; i < len(w); i += 2 {
				if w[i] != "--kind" {
					t.Fatalf("fix %q, step %q: unexpected %q", fix, step, w[i])
				}
				kinds = append(kinds, w[i+1])
			}
			if rec := patchPorts(t, srv, w[3], pigtail.PatchPortsRequest{Mode: pigtail.PortsModeReassign, Kinds: kinds}); rec.Code != http.StatusOK {
				t.Fatalf("fix %q, step %q: %d %s", fix, step, rec.Code, rec.Body)
			}
		default:
			t.Fatalf("fix %q, step %q: not replayable", fix, step)
		}
	}
}

// (a) W (ws_a): .env.local 39513, registry 39500, its server on 39513.
// H (ws_h): registered and named 39513, and sees W's listener there. The
// fix shown for W, replayed step by step against the routes, must succeed
// and leave no conflict.
func TestPortConflictFixRunsAsShownOrdered(t *testing.T) {
	f := newPortsFixture(t, 39513, 39500)
	h := filepath.Join(filepath.Dir(f.ws), "h")
	_ = os.MkdirAll(h, 0o755)
	_ = os.WriteFile(filepath.Join(h, ".env.local"), []byte("PORT=39513\n"), 0o644)
	if err := f.st.CreateWorkspace(&pigtail.Workspace{ID: "ws_h", ProjectID: "prj", Path: h,
		Ports: map[string]int{"dev": 39513}, State: pigtail.StateBuilding, BackendKind: "fake"}); err != nil {
		t.Fatal(err)
	}
	l := listening{39513: {pid: 4242, cwd: filepath.Join(f.ws, "apps", "web")}}
	wireMachine(f, l)

	resp := getServers(t, f.srv)
	fix := fixOf(t, resp, "ws_a", "env_diverges")
	hFix := fixOf(t, resp, "ws_h", "foreign_listener")
	// Replay W's fix first: on the base it is a bare resync, refused (409).
	runFix(t, f.srv, fix, func(string, string) { t.Fatalf("fix %q stops W's server: (a) needs no stop", fix) })
	if hFix != fix {
		t.Fatalf("one port, two fixes: W %q, H %q", fix, hFix)
	}

	if ws, _ := f.st.GetWorkspace("ws_a"); ws.Ports["dev"] != 39513 {
		t.Fatalf("W's registry %v, want dev=39513", ws.Ports)
	}
	if ws, _ := f.st.GetWorkspace("ws_h"); ws.Ports["dev"] == 39513 {
		t.Fatalf("H still registers 39513: %v", ws.Ports)
	}
	if after := getServers(t, f.srv); len(after.PortConflicts) != 0 {
		t.Fatalf("conflicts left after the fix: %+v", after.PortConflicts)
	}
}

// (b) 39513 is registered by a row whose directory is gone: W cannot
// recover it. W's server runs there, so the fix stops it, then reassigns.
func TestPortConflictFixRunsAsShownDeadRow(t *testing.T) {
	f := newPortsFixture(t, 39513, 39500)
	if err := f.st.CreateWorkspace(&pigtail.Workspace{ID: "ws_dead", ProjectID: "prj",
		Path: filepath.Join(filepath.Dir(f.ws), "gone"), Ports: map[string]int{"dev": 39513},
		State: pigtail.StateBuilding, BackendKind: "fake"}); err != nil {
		t.Fatal(err)
	}
	l := listening{39513: {pid: 4242, cwd: f.ws}}
	wireMachine(f, l)

	fix := fixOf(t, getServers(t, f.srv), "ws_a", "env_diverges")
	stopped := false
	runFix(t, f.srv, fix, func(dir, server string) {
		if dir != f.ws || server != "dev" {
			t.Fatalf("fix %q stops %s in %s, want dev in %s", fix, server, dir, f.ws)
		}
		delete(l, 39513)
		stopped = true
	})
	if !stopped {
		t.Fatalf("fix %q reassigns under W's running server", fix)
	}
	ws, _ := f.st.GetWorkspace("ws_a")
	b, _ := os.ReadFile(filepath.Join(f.ws, ".env.local"))
	if ws.Ports["dev"] == 39513 || ws.Ports["dev"] == 39500 || !strings.Contains(string(b), fmt.Sprintf("PORT=%d", ws.Ports["dev"])) {
		t.Fatalf("W after the fix: registry %v, .env.local %q", ws.Ports, b)
	}
}

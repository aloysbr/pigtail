package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/admission"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxsync"
	"github.com/aloysbr/pigtail/internal/daemon/cpuslots"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/focuspri"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/daemon/store/storetest"
	"github.com/aloysbr/pigtail/internal/sysmem"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// logSink collects the resolver's log lines.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *logSink) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func newFocusServer(t *testing.T, cfg Config, st *store.Store) *Server {
	t.Helper()
	cfg.SocketPath = testSocketPath(t)
	s, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A persisting seal failure is one summary line, never "recovered" meanwhile.
func TestFocusPriSealFailurePersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pigtail.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := newFocusServer(t, Config{DBPath: dbPath}, st)
	deps := s.focusPriDeps()
	sink := &logSink{}
	deps.Logf = sink.logf
	r := focuspri.New(deps, 5*time.Second, 30*time.Second)

	restore := storetest.FailFocusSeal(t, dbPath)
	past := time.Now().Add(-time.Hour)
	ends := past.Add(30 * time.Minute)
	due := pigtail.Focus{Name: "Due one", Scope: "perso", EndsAt: &ends}
	if err := st.CreateFocus(&due, past); err != nil {
		t.Fatal(err)
	}
	r.Refresh()
	r.Refresh()
	if sink.count("focuspri: refresh:") != 1 || sink.count("recovered") != 0 {
		t.Fatalf("lines = %q, want exactly one summary and no recovered", sink.lines)
	}
	restore()
	r.Refresh()
	if sink.count("recovered") != 1 {
		t.Fatalf("lines = %q, want one recovered", sink.lines)
	}
}

func TestFocusPriDepsTranslateNotFound(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s := newFocusServer(t, Config{}, st)
	deps := s.focusPriDeps()
	if v, err := deps.SubjectForWorkspace("ws_without_phase"); v != "" || err != nil {
		t.Fatalf("SubjectForWorkspace = %q, %v, want absence", v, err)
	}
	if v, err := deps.ThemeOfSubject("sub_unknown"); v != "" || err != nil {
		t.Fatalf("ThemeOfSubject = %q, %v, want absence", v, err)
	}
	_ = st.Close()
	if _, err := deps.SubjectForWorkspace("ws"); err == nil {
		t.Fatal("a closed store must be an error, not absence")
	}
	if _, err := deps.ThemeOfSubject("sub"); err == nil {
		t.Fatal("a closed store must be an error, not absence")
	}
}

// The runner always exists: with the loop off its pinned set is nil, so the
// pinned half resolves to non-focus and logs nothing.
func TestPinnedWiringNilWhenLoopOff(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s := newFocusServer(t, Config{CmuxSidebar: false}, st)
	deps := s.focusPriDeps()
	if deps.Pinned() != nil {
		t.Fatal("loop off: Pinned must be nil")
	}
	sink := &logSink{}
	deps.Logf = sink.logf
	r := focuspri.New(deps, 5*time.Second, 30*time.Second)
	r.Refresh()
	if r.IsFocus(focuspri.Caller{CmuxWorkspaceID: "AAAAAAAA-0000-4000-8000-000000000001"}) || len(sink.lines) != 0 {
		t.Fatalf("lines = %q", sink.lines)
	}
}

// simSidebar is a simulated cmux: one window, one pinned and one plain
// workspace. It never touches the real cmux.
type simSidebar struct{ pinnedID, plainID string }

func (simSidebar) Rename(string, string) error                   { return nil }
func (simSidebar) ClearName(string) error                        { return nil }
func (simSidebar) SetDescription(string, string) error           { return nil }
func (simSidebar) ClearDescription(string) error                 { return nil }
func (simSidebar) SetStatus(string, cmux.StatusEntry) error      { return nil }
func (simSidebar) ClearStatus(string, string) error              { return nil }
func (simSidebar) ListStatus(string) ([]cmux.StatusEntry, error) { return nil, nil }
func (simSidebar) ListWindows() ([]cmux.Window, error)           { return []cmux.Window{{ID: "WIN-1"}}, nil }
func (simSidebar) ListSurfaces(string) (map[string][]string, error) {
	return map[string][]string{}, nil
}
func (x simSidebar) ListWorkspaces(string) ([]cmux.WorkspaceInfo, error) {
	return []cmux.WorkspaceInfo{{ID: x.pinnedID, Title: "pinned", Pinned: true}, {ID: x.plainID, Title: "plain"}}, nil
}

// Criterion 4, pinned half, with the loop ON through the daemon wiring.
func TestPinnedWorkspaceFocusThroughLoopOn(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	st, _ := store.Open(":memory:")
	defer st.Close()
	s := newFocusServer(t, Config{CmuxSidebar: true, CmuxFolders: false}, st)
	pinned := "AAAAAAAA-0000-4000-8000-000000000001"
	deps := s.cmuxSyncDeps()
	deps.Sidebar = simSidebar{pinnedID: pinned, plainID: "BBBBBBBB-0000-4000-8000-000000000002"}
	deps.RefreshPeers = nil
	s.cmuxSync = cmuxsync.New(s.cmuxSyncConfig(), deps)
	s.cmuxSync.Step()

	r := focuspri.New(s.focusPriDeps(), 5*time.Second, 30*time.Second)
	r.Refresh()
	if !r.IsFocus(focuspri.Caller{CmuxWorkspaceID: strings.ToLower(pinned)}) {
		t.Fatal("a pinned workspace seen by the running loop must be focus")
	}
	if r.IsFocus(focuspri.Caller{CmuxWorkspaceID: "BBBBBBBB-0000-4000-8000-000000000002"}) {
		t.Fatal("an unpinned workspace must not be")
	}
}

type roomySampler struct{}

func (roomySampler) ReadMemory() (sysmem.Memory, error) {
	return sysmem.Memory{Available: 60 << 30, Pressure: 1, Total: 64 << 30}, nil
}
func (roomySampler) Procs() (map[int]sysmem.Proc, error) { return map[int]sysmem.Proc{}, nil }

// recordingFocus wraps the real resolver and counts its true answers.
type recordingFocus struct {
	inner *focuspri.Resolver
	trues atomic.Int32
}

func (f *recordingFocus) IsFocus(c focuspri.Caller) bool {
	ok := f.inner.IsFocus(c)
	if ok {
		f.trues.Add(1)
	}
	return ok
}

// The hot path (admission requests) never reaches a store read: every dep of
// the real wiring is counted, and only Refresh moves the counters.
func TestHotPathNeverCallsActiveFocus(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s := newFocusServer(t, Config{}, st)
	deps := s.focusPriDeps()
	var focusN, pinnedN, wsN, subjN, peersN, themeN atomic.Int32
	read, ws, subj, peers, theme := deps.ReadFocus, deps.Workspaces, deps.SubjectForWorkspace, deps.Peers, deps.ThemeOfSubject
	deps.ReadFocus = func() ([]pigtail.Focus, error, error) { focusN.Add(1); return read() }
	deps.Pinned = func() map[string]bool {
		pinnedN.Add(1)
		return map[string]bool{"AAAAAAAA-0000-4000-8000-000000000001": true}
	}
	deps.Workspaces = func() ([]pigtail.Workspace, error) { wsN.Add(1); return ws() }
	deps.SubjectForWorkspace = func(id string) (string, error) { subjN.Add(1); return subj(id) }
	deps.Peers = func() ([]pigtail.PeerSession, error) { peersN.Add(1); return peers() }
	deps.ThemeOfSubject = func(id string) (string, error) { themeN.Add(1); return theme(id) }
	resolver := focuspri.New(deps, 5*time.Second, 30*time.Second)
	rec := &recordingFocus{inner: resolver}

	const refreshes, requests = 2, 5
	for i := 0; i < refreshes; i++ {
		resolver.Refresh()
	}
	c := admission.New(events.New(),
		admission.WithConfig(admission.Config{MaxJobs: 10, FloorBytes: 6 << 30, MaxHold: time.Hour}),
		admission.WithSampler(roomySampler{}), admission.WithLogger(func(string, ...any) {}))
	r := chi.NewRouter()
	handlers.RegisterAdmissionRoutes(r, &handlers.Services{Admission: c, FocusPri: rec}, serverResponder{})
	srv := httptest.NewServer(r)
	defer srv.Close()
	for i := 0; i < requests; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		body := `{"group":"g","class":"tsc","cmux_workspace_id":"aaaaaaaa-0000-4000-8000-000000000001"}`
		req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/admission/leases", strings.NewReader(body))
		resp, err := srv.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		line, err := bufio.NewReader(resp.Body).ReadBytes('\n')
		var env struct {
			Data pigtail.AdmissionUpdate `json:"data"`
		}
		if err != nil || json.Unmarshal(line, &env) != nil || env.Data.State != pigtail.AdmissionGranted {
			cancel()
			t.Fatalf("lease line %q: %v", line, err)
		}
		cancel()
		resp.Body.Close()
	}
	if rec.trues.Load() != requests {
		t.Fatalf("resolved true %d times, want %d: the requests must reach the resolver", rec.trues.Load(), requests)
	}
	for name, n := range map[string]int32{"ReadFocus": focusN.Load(), "Pinned": pinnedN.Load(), "Workspaces": wsN.Load(), "Peers": peersN.Load()} {
		if n != refreshes {
			t.Errorf("%s called %d times, want %d (once per refresh, none per request)", name, n, refreshes)
		}
	}
}

// The daemon rotates cpu-slots-history.jsonl once, at startup, past its cap.
func TestServerRotatesCPUSlotHistoryAtStartup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pigtail.db")
	hist := filepath.Join(dir, "cpu-slots-history.jsonl")
	f, err := os.Create(hist)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(cpuslots.HistoryMaxBytes + 1); err != nil { // sparse: no disk used
		t.Fatal(err)
	}
	_ = f.Close()
	st, _ := store.Open(":memory:")
	defer st.Close()
	s := newFocusServer(t, Config{DBPath: dbPath, CPUSlots: 2}, st)
	if _, err := os.Stat(hist + ".1"); err != nil {
		t.Fatalf("the oversized history must have been rotated to .1: %v", err)
	}
	if _, err := os.Stat(hist); !os.IsNotExist(err) {
		t.Fatalf("the live file must be gone until the next append: %v", err)
	}
	g, err := s.cpuSlots.Acquire(context.Background(), "g", "l")
	if err != nil {
		t.Fatalf("the arbiter must be on (CPUSlots is set): %v", err)
	}
	s.cpuSlots.Release(g)
	// Read the live file itself: the rotated .1 is a sparse blob here.
	data, err := os.ReadFile(hist)
	if err != nil || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("the daemon's arbiter must append one history line: %q %v", data, err)
	}
}

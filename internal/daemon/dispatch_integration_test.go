package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/agent"
	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeBackend creates a deterministic workspace without touching git.
type fakeBackend struct {
	counter int

	// moveErr / renameErr inject failures into Move / RenameBranch for
	// partial-failure tests. movedTo / renamedTo record the last call.
	moveErr   error
	renameErr error
	movedTo   string
	renamedTo string

	// path, when set, is the Path Create returns (default /tmp/fake).
	path string
}

func (f *fakeBackend) Kind() string           { return "fake" }
func (f *fakeBackend) SupportsDelivery() bool { return false }
func (f *fakeBackend) Snapshot(_ context.Context, _ *pigtail.Workspace, _ string) (backend.SnapshotID, error) {
	return "", nil
}
func (f *fakeBackend) Diff(_ context.Context, _ *pigtail.Workspace, _, _ *backend.SnapshotID) (backend.Diff, error) {
	return backend.Diff{}, nil
}
func (f *fakeBackend) Status(_ context.Context, _ *pigtail.Workspace) (backend.Status, error) {
	return backend.Status{Clean: true}, nil
}
func (f *fakeBackend) Cleanup(_ context.Context, _ *pigtail.Workspace, _ backend.CleanupOptions) (backend.CleanupOutcome, error) {
	return backend.CleanupOutcome{}, nil
}
func (f *fakeBackend) Move(_ context.Context, ws *pigtail.Workspace, newPath string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	f.movedTo = newPath
	ws.Path = newPath
	return nil
}
func (f *fakeBackend) RenameBranch(_ context.Context, _ *pigtail.Workspace, newBranch string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	f.renamedTo = newBranch
	return nil
}
func (f *fakeBackend) DisplayRef(ws *pigtail.Workspace) string {
	if ws == nil {
		return ""
	}
	if branch, _ := ws.BackendData["branch"].(string); branch != "" {
		return branch
	}
	return ws.Intent
}
func (f *fakeBackend) Create(_ context.Context, p *pigtail.Project, intent backend.CreateIntent) (*pigtail.Workspace, error) {
	f.counter++
	// Mirror GitBackend's base-ref resolution so tests can assert the fallback
	// chain (explicit BaseRef → project Defaults.BaseRef → "main").
	base := intent.BaseRef
	if base == "" && p.Defaults != nil {
		base = p.Defaults.BaseRef
	}
	if base == "" {
		base = "main"
	}
	path := "/tmp/fake"
	if f.path != "" {
		path = f.path
	}
	return &pigtail.Workspace{
		ID:          "ws_fake",
		ProjectID:   p.ID,
		Intent:      intent.Intent,
		State:       pigtail.StateBuilding,
		Path:        path,
		Ports:       map[string]int{"dev": 3000 + f.counter},
		BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "fake/" + intent.Intent, "base_branch": base},
	}, nil
}

type fakeAgent struct{}

func (fakeAgent) Kind() string { return "fake" }
func (fakeAgent) Spawn(_ context.Context, ws *pigtail.Workspace, in agent.SpawnIntent) (*pigtail.AgentSession, error) {
	// Each spawn gets a unique id so multiple sessions per workspace are
	// distinguishable (mirrors the real tmux backend's per-agent id).
	id := "agt_" + randHex()
	return &pigtail.AgentSession{
		ID:              id,
		WorkspaceID:     ws.ID,
		AgentKind:       "claude",
		Intent:          in.Intent,
		State:           pigtail.AgentStateRunning,
		TmuxSessionName: "pigtail-" + ws.ID + "-" + id,
		StartedAt:       time.Now(),
		LastEventAt:     time.Now(),
	}, nil
}

// randHex returns a short random hex string for unique fake agent ids.
func randHex() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
func (fakeAgent) Attach(_ context.Context, _ *pigtail.AgentSession) (agent.AttachInfo, error) {
	return agent.AttachInfo{}, nil
}
func (fakeAgent) Pause(_ context.Context, _ *pigtail.AgentSession) error  { return nil }
func (fakeAgent) Resume(_ context.Context, _ *pigtail.AgentSession) error { return nil }
func (fakeAgent) Kill(_ context.Context, _ *pigtail.AgentSession) error   { return nil }
func (fakeAgent) Reply(_ context.Context, _ *pigtail.AgentSession, _ string) error {
	return nil
}
func (fakeAgent) Status(_ context.Context, _ *pigtail.AgentSession) (pigtail.AgentState, error) {
	return pigtail.AgentStateRunning, nil
}

func TestDispatchEndToEnd(t *testing.T) {
	_ = exec.Command // silence unused if no git
	dir := t.TempDir()
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()

	srv, err := New(Config{SocketPath: testSocketPath(t)},
		st, bus, &fakeBackend{}, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	defer startDaemon(t, srv)()

	// Seed a project
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_test", Name: "test", RootPath: dir, DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}); err != nil {
		t.Fatal(err)
	}

	// wait for socket
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", srv.cfg.SocketPath); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", srv.cfg.SocketPath)
		},
	}}

	// Subscribe to events first
	sub := bus.Subscribe()
	defer sub.Close()

	body, _ := json.Marshal(pigtail.DispatchRequest{ProjectID: "prj_test", Intent: "say hello"})
	resp, err := httpc.Post("http://pigtail/dispatch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(buf))
	}
	var env struct {
		Data pigtail.DispatchResponse `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Data.WorkspaceID == "" || env.Data.AgentSessionID == "" {
		t.Fatalf("dispatch response empty: %+v", env)
	}

	// Verify events were emitted
	gotWS, gotAgt := false, false
	deadline2 := time.After(2 * time.Second)
loop:
	for !gotWS || !gotAgt {
		select {
		case e := <-sub.C:
			switch e.Type {
			case pigtail.EventWorkspaceCreated:
				gotWS = true
			case pigtail.EventAgentStarted:
				gotAgt = true
			}
		case <-deadline2:
			break loop
		}
	}
	if !gotWS || !gotAgt {
		t.Fatalf("missing events: ws=%v agt=%v", gotWS, gotAgt)
	}

	// Verify GET /workspaces returns it
	listResp, err := httpc.Get("http://pigtail/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var listEnv struct {
		Data []pigtail.Workspace `json:"data"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listEnv); err != nil {
		t.Fatal(err)
	}
	if len(listEnv.Data) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(listEnv.Data))
	}
	if len(listEnv.Data[0].AgentSessions) != 1 {
		t.Fatalf("expected workspace to carry 1 agent session, got %d", len(listEnv.Data[0].AgentSessions))
	}
}

func TestNewWorkspaceNoAgent(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()

	srv, err := New(Config{SocketPath: testSocketPath(t)},
		st, bus, &fakeBackend{}, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	defer startDaemon(t, srv)()

	// Seed a project
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_new", Name: "new-test", RootPath: dir, DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}); err != nil {
		t.Fatal(err)
	}

	// wait for socket
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", srv.cfg.SocketPath); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", srv.cfg.SocketPath)
		},
	}}

	body, _ := json.Marshal(pigtail.NewWorkspaceRequest{ProjectID: "prj_new", Intent: "build palette"})
	resp, err := httpc.Post("http://pigtail/workspaces/new", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(buf))
	}

	var env struct {
		Data pigtail.Workspace `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	ws := env.Data
	if ws.ID == "" {
		t.Fatalf("expected non-empty workspace ID, got: %+v", ws)
	}

	// Assert NO agent session was created
	sessions, _ := st.ListAgentSessionsByWorkspace(ws.ID)
	if len(sessions) != 0 {
		t.Fatalf("expected 0 agent sessions, got %d", len(sessions))
	}
}

func TestDispatchAntiCollision(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()

	srv, err := New(Config{SocketPath: testSocketPath(t)},
		st, bus, &fakeBackend{}, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	defer startDaemon(t, srv)()

	// Seed a project
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_coll", Name: "collision-test", RootPath: dir, DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}); err != nil {
		t.Fatal(err)
	}

	// wait for socket
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", srv.cfg.SocketPath); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	httpc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", srv.cfg.SocketPath)
		},
	}}

	body, _ := json.Marshal(pigtail.DispatchRequest{ProjectID: "prj_coll", Intent: "first dispatch"})

	// First dispatch — must succeed with 201.
	resp1, err := httpc.Post("http://pigtail/dispatch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != 201 {
		buf, _ := io.ReadAll(resp1.Body)
		t.Fatalf("first dispatch: status=%d body=%s", resp1.StatusCode, string(buf))
	}
	io.Copy(io.Discard, resp1.Body) //nolint:errcheck

	// Second dispatch with same body — must return 409 CONFLICT.
	body2, _ := json.Marshal(pigtail.DispatchRequest{ProjectID: "prj_coll", Intent: "first dispatch"})
	resp2, err := httpc.Post("http://pigtail/dispatch", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 409 {
		buf, _ := io.ReadAll(resp2.Body)
		t.Fatalf("second dispatch: expected 409, got status=%d body=%s", resp2.StatusCode, string(buf))
	}
	var errEnv struct {
		Error pigtail.APIError `json:"error"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&errEnv); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errEnv.Error.Code != pigtail.ErrConflict {
		t.Fatalf("expected error code %q, got %q", pigtail.ErrConflict, errEnv.Error.Code)
	}
}

// startTestServer spins up a daemon with the given backend over a unix socket
// and returns an http.Client wired to that socket plus the store and backend.
func startTestServer(t *testing.T, be backend.Backend) (*http.Client, *store.Store, *events.Bus) {
	t.Helper()
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { st.Close() })
	bus := events.New()

	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, be, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(startDaemon(t, srv))
	return unixHTTPClient(t, srv.cfg.SocketPath), st, bus
}

// unixHTTPClient waits for the daemon socket and returns a client dialing it.
func unixHTTPClient(t *testing.T, socketPath string) *http.Client {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", socketPath); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}}
}

// postJSON posts body as JSON and returns the status code + raw body.
func postJSON(t *testing.T, httpc *http.Client, path string, body any) (int, []byte) {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := httpc.Post("http://pigtail"+path, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func seedWorkspace(t *testing.T, st *store.Store, ws *pigtail.Workspace) {
	t.Helper()
	if _, err := st.GetProject(ws.ProjectID); err != nil {
		if err := st.CreateProject(&pigtail.Project{
			ID: ws.ProjectID, Name: ws.ProjectID, RootPath: filepath.Dir(ws.Path),
			DefaultBackend: "fake", PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
}

func TestRenameWorkspace(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old-slug")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}

	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)

	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_ren", ProjectID: "prj_ren", Intent: "x", State: pigtail.StateReadyToReview,
		Path: oldPath, Ports: map[string]int{"dev": 39998}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/old-slug", "base_branch": "main"},
	})
	// Seed a terminal agent session — must survive the rename untouched.
	if err := st.CreateAgentSession(&pigtail.AgentSession{
		ID: "agt_done", WorkspaceID: "ws_ren", AgentKind: "claude",
		State: pigtail.AgentStateDone, TmuxSessionName: "pigtail-ws_ren-agt_done",
	}); err != nil {
		t.Fatal(err)
	}

	code, raw := postJSON(t, httpc, "/workspaces/ws_ren/rename", pigtail.RenameRequest{NewSlug: "new-slug"})
	if code != 200 {
		t.Fatalf("rename status=%d body=%s", code, raw)
	}

	got, err := st.GetWorkspace("ws_ren")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "new-slug")
	if got.Path != wantPath {
		t.Errorf("path = %q, want %q", got.Path, wantPath)
	}
	if be.movedTo != wantPath {
		t.Errorf("backend Move target = %q, want %q", be.movedTo, wantPath)
	}
	// Invariants: id, state, branch, sessions, tmux name all unchanged.
	if got.ID != "ws_ren" {
		t.Errorf("id changed to %q", got.ID)
	}
	if got.State != pigtail.StateReadyToReview {
		t.Errorf("state changed to %q", got.State)
	}
	if got.BackendData["branch"] != "feat/old-slug" {
		t.Errorf("branch changed without --branch: %v", got.BackendData["branch"])
	}
	if len(got.AgentSessions) != 1 || got.AgentSessions[0].ID != "agt_done" {
		t.Fatalf("agent session lost: %+v", got.AgentSessions)
	}
	if got.AgentSessions[0].TmuxSessionName != "pigtail-ws_ren-agt_done" {
		t.Errorf("tmux session name changed: %q", got.AgentSessions[0].TmuxSessionName)
	}
}

func TestRenameWithBranch(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_b", ProjectID: "prj_b", Intent: "x", State: pigtail.StateBuilding,
		Path: oldPath, Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/old", "base_branch": "main"},
	})

	code, raw := postJSON(t, httpc, "/workspaces/ws_b/rename",
		pigtail.RenameRequest{NewSlug: "newname", RenameBranch: true})
	if code != 200 {
		t.Fatalf("rename status=%d body=%s", code, raw)
	}
	if be.renamedTo != "newname" {
		t.Errorf("backend RenameBranch target = %q, want newname", be.renamedTo)
	}
	got, _ := st.GetWorkspace("ws_b")
	if got.BackendData["branch"] != "newname" {
		t.Errorf("branch in store = %v, want newname", got.BackendData["branch"])
	}
	if got.BackendData["base_branch"] != "main" {
		t.Errorf("base_branch lost: %v", got.BackendData)
	}
}

func TestRenameRefusesLiveAgent(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "live")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_live", ProjectID: "prj_live", Intent: "x", State: pigtail.StateBuilding,
		Path: oldPath, Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/live"},
	})
	if err := st.CreateAgentSession(&pigtail.AgentSession{
		ID: "agt_live", WorkspaceID: "ws_live", AgentKind: "claude",
		State: pigtail.AgentStateRunning, TmuxSessionName: "pigtail-ws_live-agt_live",
	}); err != nil {
		t.Fatal(err)
	}

	// Even with force, a live agent blocks the rename.
	code, raw := postJSON(t, httpc, "/workspaces/ws_live/rename",
		pigtail.RenameRequest{NewSlug: "renamed", Force: true})
	if code != 409 {
		t.Fatalf("expected 409, got %d body=%s", code, raw)
	}
	if be.movedTo != "" {
		t.Errorf("backend Move should not have been called, got %q", be.movedTo)
	}
}

func TestRenameRefusesCollision(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Destination dir already exists on disk.
	if err := os.MkdirAll(filepath.Join(root, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_a", ProjectID: "prj_c", Intent: "x", State: pigtail.StateBuilding,
		Path: filepath.Join(root, "a"), Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/a"},
	})

	code, raw := postJSON(t, httpc, "/workspaces/ws_a/rename", pigtail.RenameRequest{NewSlug: "taken"})
	if code != 409 {
		t.Fatalf("expected 409 on dir collision, got %d body=%s", code, raw)
	}

	// Slug with a path separator is rejected up front.
	code, _ = postJSON(t, httpc, "/workspaces/ws_a/rename", pigtail.RenameRequest{NewSlug: "a/b"})
	if code != 400 {
		t.Fatalf("expected 400 on bad slug, got %d", code)
	}
}

// TestRenamePartialFailureBranch covers the documented partial-failure path:
// the worktree move succeeds but the subsequent branch rename fails. The
// handler must report 422 (not silently swallow it) and surface old/new
// paths so the operator knows the dir moved while the branch did not.
func TestRenamePartialFailureBranch(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "src")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{renameErr: errors.New("branch rename boom")}
	httpc, st, _ := startTestServer(t, be)
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_pf", ProjectID: "prj_pf", Intent: "x", State: pigtail.StateBuilding,
		Path: oldPath, Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/src"},
	})

	code, raw := postJSON(t, httpc, "/workspaces/ws_pf/rename",
		pigtail.RenameRequest{NewSlug: "dst", RenameBranch: true})
	if code != 422 {
		t.Fatalf("expected 422 on branch-rename failure, got %d body=%s", code, raw)
	}
	// The move did happen (fake backend recorded it) before the branch step.
	if be.movedTo != filepath.Join(root, "dst") {
		t.Errorf("move should have run before the branch step, movedTo=%q", be.movedTo)
	}
	// The persisted store row must NOT have been updated, since the branch
	// rename failed before UpdateWorkspacePath was reached. (We read the
	// committed list rather than GetWorkspace, whose cache holds the same
	// pointer the fake backend mutated in-place.)
	all, _ := st.ListWorkspaces("")
	for _, w := range all {
		if w.ID == "ws_pf" && w.Path != oldPath {
			t.Errorf("persisted path should be unchanged on partial failure, got %q", w.Path)
		}
	}
}

// decodeRun decodes a RunResponse from a /run reply.
func decodeRun(t *testing.T, raw []byte) pigtail.RunResponse {
	t.Helper()
	var env struct {
		Data pigtail.RunResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode run response: %v (raw=%s)", err, raw)
	}
	return env.Data
}

// TestRunTwoAgentsSameWorkspace covers the core feature: two `run` calls into
// one existing workspace yield two distinct agent sessions (distinct ids and
// tmux names) and never touch the workspace's state or ports.
func TestRunTwoAgentsSameWorkspace(t *testing.T) {
	root := t.TempDir()
	wsPath := filepath.Join(root, "shared")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_shared", ProjectID: "prj_run", Intent: "x", State: pigtail.StateBuilding,
		Path: wsPath, Ports: map[string]int{"dev": 4321}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/shared"},
	})

	code, raw := postJSON(t, httpc, "/workspaces/ws_shared/run", pigtail.RunRequest{Intent: "subject A"})
	if code != 201 {
		t.Fatalf("first run status=%d body=%s", code, raw)
	}
	first := decodeRun(t, raw)

	code, raw = postJSON(t, httpc, "/workspaces/ws_shared/run", pigtail.RunRequest{Intent: "subject B"})
	if code != 201 {
		t.Fatalf("second run status=%d body=%s", code, raw)
	}
	second := decodeRun(t, raw)

	if first.AgentSessionID == second.AgentSessionID {
		t.Fatalf("expected distinct agent session ids, both = %s", first.AgentSessionID)
	}

	sessions, _ := st.ListAgentSessionsByWorkspace("ws_shared")
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions on ws_shared, got %d", len(sessions))
	}
	if sessions[0].TmuxSessionName == sessions[1].TmuxSessionName {
		t.Fatalf("expected distinct tmux session names, both = %s", sessions[0].TmuxSessionName)
	}

	// Workspace row untouched.
	ws, _ := st.GetWorkspace("ws_shared")
	if ws.State != pigtail.StateBuilding {
		t.Errorf("state changed to %q, want building", ws.State)
	}
	if ws.Ports["dev"] != 4321 {
		t.Errorf("ports changed: %v", ws.Ports)
	}
}

// TestRunNotFound and a vanished-path workspace.
func TestRunNotFound(t *testing.T) {
	be := &fakeBackend{}
	httpc, _, _ := startTestServer(t, be)
	code, raw := postJSON(t, httpc, "/workspaces/ws_nope/run", pigtail.RunRequest{Intent: "x"})
	if code != 404 {
		t.Fatalf("expected 404 for unknown workspace, got %d body=%s", code, raw)
	}
}

func TestRunPathGone(t *testing.T) {
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	// Path that never exists on disk.
	gone := filepath.Join(t.TempDir(), "vanished")
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_gone", ProjectID: "prj_gone", Intent: "x", State: pigtail.StateBuilding,
		Path: gone, Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/gone"},
	})
	code, raw := postJSON(t, httpc, "/workspaces/ws_gone/run", pigtail.RunRequest{Intent: "x"})
	if code != 422 {
		t.Fatalf("expected 422 for vanished path, got %d body=%s", code, raw)
	}
}

// TestRunNoCommitPreamble: the preamble is absent on the first agent, present
// on the second (a live agent already exists), and absent when a plan is given.
func TestRunNoCommitPreamble(t *testing.T) {
	root := t.TempDir()
	wsPath := filepath.Join(root, "pre")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_pre", ProjectID: "prj_pre", Intent: "x", State: pigtail.StateBuilding,
		Path: wsPath, Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/pre"},
	})

	const preamble = "Do NOT run git commit/add"

	// First agent: no live peer → no preamble.
	code, _ := postJSON(t, httpc, "/workspaces/ws_pre/run", pigtail.RunRequest{Intent: "first subject"})
	if code != 201 {
		t.Fatalf("first run code=%d", code)
	}
	// Second agent: a live peer exists → preamble injected.
	code, _ = postJSON(t, httpc, "/workspaces/ws_pre/run", pigtail.RunRequest{Intent: "second subject"})
	if code != 201 {
		t.Fatalf("second run code=%d", code)
	}
	// Third agent: live peers exist but a plan is provided → no preamble.
	code, _ = postJSON(t, httpc, "/workspaces/ws_pre/run",
		pigtail.RunRequest{Intent: "third subject", PlanContent: "do the thing"})
	if code != 201 {
		t.Fatalf("third run code=%d", code)
	}

	sessions, _ := st.ListAgentSessionsByWorkspace("ws_pre")
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}
	// Sessions are ordered by started_at; map by intent suffix to assert.
	var firstIntent, secondIntent, thirdIntent string
	for _, s := range sessions {
		switch {
		case strings.Contains(s.Intent, "first subject"):
			firstIntent = s.Intent
		case strings.Contains(s.Intent, "second subject"):
			secondIntent = s.Intent
		case strings.Contains(s.Intent, "third subject"):
			thirdIntent = s.Intent
		}
	}
	if strings.Contains(firstIntent, preamble) {
		t.Errorf("first agent should NOT carry preamble: %q", firstIntent)
	}
	if !strings.Contains(secondIntent, preamble) {
		t.Errorf("second agent SHOULD carry preamble: %q", secondIntent)
	}
	if strings.Contains(thirdIntent, preamble) {
		t.Errorf("third agent (with plan) should NOT carry preamble: %q", thirdIntent)
	}
}

// TestIntentPersistedOnDispatchAndRun: the spawn intent is recorded on the
// agent_sessions row for both dispatch and run.
func TestIntentPersistedOnDispatchAndRun(t *testing.T) {
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_int", Name: "intent", RootPath: t.TempDir(), DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}); err != nil {
		t.Fatal(err)
	}

	// dispatch creates ws_fake with one session carrying the intent.
	code, raw := postJSON(t, httpc, "/dispatch",
		pigtail.DispatchRequest{ProjectID: "prj_int", Intent: "dispatched subject"})
	if code != 201 {
		t.Fatalf("dispatch code=%d body=%s", code, raw)
	}
	sessions, _ := st.ListAgentSessionsByWorkspace("ws_fake")
	if len(sessions) != 1 || sessions[0].Intent != "dispatched subject" {
		t.Fatalf("dispatch intent not persisted: %+v", sessions)
	}

	// run a second agent into the same workspace (it exists on disk: /tmp/fake).
	if _, err := os.Stat("/tmp/fake"); err != nil {
		_ = os.MkdirAll("/tmp/fake", 0o755)
	}
	code, raw = postJSON(t, httpc, "/workspaces/ws_fake/run",
		pigtail.RunRequest{Intent: "ran subject"})
	if code != 201 {
		t.Fatalf("run code=%d body=%s", code, raw)
	}
	sessions, _ = st.ListAgentSessionsByWorkspace("ws_fake")
	found := false
	for _, s := range sessions {
		if strings.Contains(s.Intent, "ran subject") {
			found = true
		}
	}
	if !found {
		t.Fatalf("run intent not persisted: %+v", sessions)
	}
}

// TestDispatchBaseRef checks that DispatchRequest.BaseRef reaches the backend
// and is reflected in the resulting workspace's BackendData["base_branch"],
// and that an empty BaseRef falls back to the project's Defaults.BaseRef.
func TestDispatchBaseRef(t *testing.T) {
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_base", Name: "base-test", RootPath: t.TempDir(), DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
		Defaults:   &pigtail.ProjectDefaults{BaseRef: "develop"},
	}); err != nil {
		t.Fatal(err)
	}

	// Explicit base_ref wins.
	code, raw := postJSON(t, httpc, "/dispatch",
		pigtail.DispatchRequest{ProjectID: "prj_base", Intent: "on a feature base", BaseRef: "feat/project-tasks"})
	if code != 201 {
		t.Fatalf("dispatch status=%d body=%s", code, raw)
	}
	ws, err := st.GetWorkspace("ws_fake")
	if err != nil {
		t.Fatal(err)
	}
	if got := ws.BackendData["base_branch"]; got != "feat/project-tasks" {
		t.Fatalf("base_branch = %v, want feat/project-tasks", got)
	}
}

// TestNewWorkspaceBaseRefFallback checks that an empty BaseRef on /workspaces/new
// falls back to the project's Defaults.BaseRef.
func TestNewWorkspaceBaseRefFallback(t *testing.T) {
	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_nb", Name: "new-base", RootPath: t.TempDir(), DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
		Defaults:   &pigtail.ProjectDefaults{BaseRef: "develop"},
	}); err != nil {
		t.Fatal(err)
	}

	code, raw := postJSON(t, httpc, "/workspaces/new",
		pigtail.NewWorkspaceRequest{ProjectID: "prj_nb", Intent: "no explicit base"})
	if code != 201 {
		t.Fatalf("new status=%d body=%s", code, raw)
	}
	ws, err := st.GetWorkspace("ws_fake")
	if err != nil {
		t.Fatal(err)
	}
	if got := ws.BackendData["base_branch"]; got != "develop" {
		t.Fatalf("base_branch = %v, want develop (project default)", got)
	}
}

func TestGCWorkspaces(t *testing.T) {
	root := t.TempDir()
	alive := filepath.Join(root, "alive")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(root, "dead") // never created on disk

	be := &fakeBackend{}
	httpc, st, _ := startTestServer(t, be)
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_alive", ProjectID: "prj_gc", Intent: "x", State: pigtail.StateBuilding,
		Path: alive, Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/alive"},
	})
	seedWorkspace(t, st, &pigtail.Workspace{
		ID: "ws_dead", ProjectID: "prj_gc", Intent: "x", State: pigtail.StateBuilding,
		Path: dead, Ports: map[string]int{}, BackendKind: "fake",
		BackendData: map[string]interface{}{"branch": "feat/dead"},
	})
	// dead workspace has a terminal agent session that must be cleaned too.
	if err := st.CreateAgentSession(&pigtail.AgentSession{
		ID: "agt_dead", WorkspaceID: "ws_dead", AgentKind: "claude",
		State: pigtail.AgentStateDone,
	}); err != nil {
		t.Fatal(err)
	}

	// Dry-run: lists the dead one, deletes nothing.
	code, raw := postJSON(t, httpc, "/workspaces/gc", pigtail.GCRequest{DryRun: true})
	if code != 200 {
		t.Fatalf("gc dry-run status=%d body=%s", code, raw)
	}
	var dryEnv struct {
		Data pigtail.GCResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &dryEnv); err != nil {
		t.Fatal(err)
	}
	if len(dryEnv.Data.DeletedIDs) != 1 || dryEnv.Data.DeletedIDs[0] != "ws_dead" {
		t.Fatalf("dry-run candidates = %v, want [ws_dead]", dryEnv.Data.DeletedIDs)
	}
	if _, err := st.GetWorkspace("ws_dead"); err != nil {
		t.Fatalf("dry-run must not delete ws_dead: %v", err)
	}

	// Real run: deletes the dead one + its sessions, keeps the alive one.
	code, raw = postJSON(t, httpc, "/workspaces/gc", pigtail.GCRequest{})
	if code != 200 {
		t.Fatalf("gc status=%d body=%s", code, raw)
	}
	if _, err := st.GetWorkspace("ws_dead"); err == nil {
		t.Fatal("ws_dead should have been deleted")
	}
	sessions, _ := st.ListAgentSessionsByWorkspace("ws_dead")
	if len(sessions) != 0 {
		t.Fatalf("ws_dead sessions not cleaned: %d", len(sessions))
	}
	if _, err := st.GetWorkspace("ws_alive"); err != nil {
		t.Fatalf("ws_alive should survive: %v", err)
	}
}

// allocBackend is fakeBackend whose ports come from the daemon's allocator,
// the way the git backend's do.
type allocBackend struct {
	fakeBackend
	alloc *ports.Allocator
}

func (b *allocBackend) Create(ctx context.Context, p *pigtail.Project, in backend.CreateIntent) (*pigtail.Workspace, error) {
	ws, err := b.fakeBackend.Create(ctx, p, in)
	if err != nil {
		return nil, err
	}
	port, err := b.alloc.Allocate(p.PortRanges, "dev")
	if err != nil {
		return nil, err
	}
	ws.Ports = map[string]int{"dev": port}
	return ws, nil
}

func TestNewWorkspaceConfirmsPortsAfterPersist(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	alloc := ports.New(st, ports.WithBindProbe(func(int) bool { return true }))
	be := &allocBackend{alloc: alloc}
	srv, err := New(Config{SocketPath: testSocketPath(t), Allocator: alloc}, st, events.New(), be, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(startDaemon(t, srv))
	httpc := unixHTTPClient(t, srv.cfg.SocketPath)
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_p", Name: "p", RootPath: t.TempDir(), DefaultBackend: "fake",
		PortRanges: map[string]pigtail.PortRange{"dev": {39400, 39499}},
	}); err != nil {
		t.Fatal(err)
	}
	code, raw := postJSON(t, httpc, "/workspaces/new", pigtail.NewWorkspaceRequest{ProjectID: "prj_p", Intent: "x"})
	if code != 201 {
		t.Fatalf("status=%d body=%s", code, raw)
	}
	ws, err := st.GetWorkspace("ws_fake")
	if err != nil {
		t.Fatal(err)
	}
	if alloc.Pending("dev")[ws.Ports["dev"]] {
		t.Fatal("port still pending after the row was persisted")
	}
	if srv.allocator != alloc {
		t.Fatal("the daemon built its own allocator instead of the configured one")
	}
}

// A dispatch refused by the anti-collision guard gives its port back. The
// first dispatch persists ws_fake with a live agent; the second one is
// refused (409) after Backend.Create allocated a port.
func TestDispatchAntiCollisionReleasesItsPort(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	alloc := ports.New(st, ports.WithBindProbe(func(int) bool { return true }))
	srv, err := New(Config{SocketPath: testSocketPath(t), Allocator: alloc}, st, events.New(), &allocBackend{alloc: alloc}, fakeAgent{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(startDaemon(t, srv))
	httpc := unixHTTPClient(t, srv.cfg.SocketPath)
	rng := map[string]pigtail.PortRange{"dev": {39450, 39499}}
	if err := st.CreateProject(&pigtail.Project{
		ID: "prj_c", Name: "c", RootPath: t.TempDir(), DefaultBackend: "fake", PortRanges: rng,
	}); err != nil {
		t.Fatal(err)
	}
	req := pigtail.DispatchRequest{ProjectID: "prj_c", Intent: "same"}
	if code, raw := postJSON(t, httpc, "/dispatch", req); code != 201 {
		t.Fatalf("first dispatch: status=%d body=%s", code, raw)
	}
	if code, raw := postJSON(t, httpc, "/dispatch", req); code != 409 {
		t.Fatalf("second dispatch: status=%d body=%s, want 409", code, raw)
	}
	if p := alloc.Pending("dev"); len(p) != 0 {
		t.Fatalf("port still pending after a refused dispatch: %v", p)
	}
	// 39450 is ws_fake's registered port; 39451 was allocated then refused.
	if p, err := alloc.Allocate(rng, "dev"); err != nil || p != 39451 {
		t.Fatalf("next allocation got %d (%v), want the released 39451", p, err)
	}
}

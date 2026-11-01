package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// wiringCmux is a simulated cmux for the server's launcher: one window, the
// tabs NewWorkspace makes, and every spec it was handed. Never the real cmux.
type wiringCmux struct {
	mu    sync.Mutex
	tabs  []cmux.WorkspaceInfo
	specs []cmux.WorkspaceSpec
}

const wiringWindow = "AAAAAAAA-0000-4000-8000-000000000001"

func (f *wiringCmux) wire(d *launch.Deps) {
	d.Windows = func() ([]cmux.Window, error) { return []cmux.Window{{ID: wiringWindow}}, nil }
	d.Workspaces = func(string) ([]cmux.WorkspaceInfo, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]cmux.WorkspaceInfo(nil), f.tabs...), nil
	}
	d.Groups = func(string) ([]cmux.Group, error) { return nil, nil }
	d.NewWorkspace = func(s cmux.WorkspaceSpec) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.specs = append(f.specs, s)
		n := len(f.tabs) + 1
		ref := fmt.Sprintf("workspace:%d", n)
		f.tabs = append(f.tabs, cmux.WorkspaceInfo{ID: fmt.Sprintf("BBBBBBBB-0000-4000-8000-%012d", n), Ref: ref, Title: s.Name})
		return ref, nil
	}
}

func (f *wiringCmux) lastSpec(t *testing.T) cmux.WorkspaceSpec {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.specs) == 0 {
		t.Fatal("NewWorkspace was never called")
	}
	return f.specs[len(f.specs)-1]
}

// launchServer is a daemon on socketPath ("" = the default path) whose
// launcher runs on simulated cmux, ps and cells, with a subject of the
// master scope.
func launchServer(t *testing.T, socketPath string) (*Server, *wiringCmux, http.Handler) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	master := string(scope.Master())
	now := time.Now()
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_l", Name: "L", Kind: "ongoing", Scope: master, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_l", ThemeID: "thm_l", Title: "Launch me", ShortTitle: "Launch me", Slug: "l", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f := &wiringCmux{}
	cellDir, launchDir := t.TempDir(), t.TempDir()
	reg := filepath.Join(t.TempDir(), "spawns.jsonl")
	cfg := Config{SocketPath: socketPath, LaunchOverrides: func(d *launch.Deps) {
		f.wire(d)
		d.Registry = spawnreg.Open(reg)
		d.Cell = func(string) (claudeprofile.Cell, error) {
			return claudeprofile.Cell{ID: "mp", ConfigDir: cellDir, Scope: master}, nil
		}
		d.LaunchDir = func(string) (string, bool) { return launchDir, true }
		d.Mode = func(string) (string, error) { return "auto", nil }
		d.Procs = func() ([]claudesession.Proc, error) { return nil, nil }
		d.Environ = func(int) (string, error) { return "", nil }
		d.PollEvery = 5 * time.Millisecond
		d.Logf = t.Logf
	}}
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The launcher runs on a context the test ends, as Server.Run's would:
	// a confirmation still polling stops at cleanup.
	ctx, cancel := context.WithCancel(context.Background())
	if srv.launcher != nil {
		srv.launcher.Start(ctx)
	}
	t.Cleanup(func() {
		cancel()
		if srv.launcher != nil {
			srv.launcher.Wait()
		}
	})
	return srv, f, srv.router
}

func launchServe(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = ""
	req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServerWiresLauncher(t *testing.T) {
	sock := testSocketPath(t)
	srv, f, h := launchServer(t, sock)
	if srv.launcher == nil {
		t.Fatal("no launcher wired")
	}
	rec := launchServe(t, h, http.MethodPost, "/subjects/sub_l/launch", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST launch = %d %s", rec.Code, rec.Body)
	}
	got := decodeLaunch(t, rec)
	if got.State != pigtail.LaunchPending || got.WorkspaceID == "" || got.Origin != "api" {
		t.Fatalf("launch = %+v", got)
	}
	spec := f.lastSpec(t)
	if !containsEnv(spec.Env, "PIGTAIL_SOCKET="+sock) {
		t.Fatalf("spec env %v lacks PIGTAIL_SOCKET=%s", spec.Env, sock)
	}
	if !containsEnv(spec.Env, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("spec env %v lacks the cell", spec.Env)
	}

	get := func() pigtail.SessionLaunch {
		t.Helper()
		rec := launchServe(t, h, http.MethodGet, "/launches/"+got.LaunchID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET launch = %d %s", rec.Code, rec.Body)
		}
		return decodeLaunch(t, rec)
	}
	if s := get(); s.State != pigtail.LaunchPending {
		t.Fatalf("before registration: %+v", s)
	}

	peer := pigtail.PeersRequest{Peers: []pigtail.PeerSession{{SessionID: "s-launched", PID: 4242, CmuxWorkspaceID: got.WorkspaceID, Scope: string(scope.Master())}}}
	if rec := launchServe(t, h, http.MethodPost, "/peers", peer); rec.Code != http.StatusOK {
		t.Fatalf("POST /peers = %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := get()
		if s.State == pigtail.LaunchLaunched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never launched after registration: %+v", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServerLauncherDefaultSocketHasNoPigtailSocket(t *testing.T) {
	_, f, h := launchServer(t, "")
	if rec := launchServe(t, h, http.MethodPost, "/subjects/sub_l/launch", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("POST launch = %d %s", rec.Code, rec.Body)
	}
	for _, kv := range f.lastSpec(t).Env {
		if strings.HasPrefix(kv, "PIGTAIL_SOCKET=") {
			t.Fatalf("default socket: env carries %s", kv)
		}
	}
}

func containsEnv(env []string, prefix string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// decodeLaunch reads a SessionLaunch from the daemon's envelope ({"data": …}).
func decodeLaunch(t *testing.T, rec *httptest.ResponseRecorder) pigtail.SessionLaunch {
	t.Helper()
	var env struct {
		Data pigtail.SessionLaunch `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return env.Data
}

// CA3: a registry that cannot be read refuses a TUI launch (fail-safe).
func TestSDKLiveUnreadableRegistryReadsAsLive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sdk_bad"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sdk_bad", "meta.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, live := sdkLive(&sdksession.Registry{Dir: dir})("sub_x")
	if !live || !strings.Contains(id, "unreadable") {
		t.Fatalf("sdkLive = %q %v, want live with a reason", id, live)
	}
}

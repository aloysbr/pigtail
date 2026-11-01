package daemon

import (
	"context"
	"fmt"
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
	"github.com/aloysbr/pigtail/internal/wsmarker"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestServerWiresWorkspaceMarkers breaks if the markers leave daemonMaintenanceDir,
// if buildRouter stops wiring Services.SyncWorkspaceMarkers, or if the tick stops recreating them.
func TestServerWiresWorkspaceMarkers(t *testing.T) {
	md := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", md) // the hook's rule: this dir wins over the socket's
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(md, wsmarker.DirName, probeCmuxWS)
	q := httptest.NewRequest(http.MethodPost, "/cmux-workspaces/"+probeCmuxWS+"/off-topic", strings.NewReader(`{"space":"Inbox"}`))
	q.Header.Set(pigtail.HeaderScope, "perso")
	rec := httptest.NewRecorder()
	s.buildRouter().ServeHTTP(rec, q)
	if rec.Code != http.StatusCreated {
		t.Fatalf("declare = %d %s", rec.Code, rec.Body)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "off-topic\n" {
		t.Fatalf("marker after declare (wiring lost in buildRouter?) = %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.cfg.SocketPath), wsmarker.DirName)); !os.IsNotExist(err) {
		t.Fatal("markers written next to the socket despite PIGTAIL_MAINTENANCE_DIR")
	}
	_ = os.RemoveAll(filepath.Join(md, wsmarker.DirName)) // so the wait below sees the loop's own first sync
	ctx, cancel := context.WithCancel(context.Background())
	s.markerEvery = 20 * time.Millisecond
	done := make(chan struct{})
	go func() { s.runWorkspaceMarkers(ctx); close(done) }()
	// Registered after `defer st.Close()`, so it runs first (LIFO), and before t.TempDir's cleanup
	// (t.Cleanup runs after every defer): the loop has returned before the store closes and md goes.
	// Also runs on t.Fatal (runtime.Goexit runs deferred calls).
	defer func() { cancel(); <-done }()
	waitMarker(t, marker, "initial sync of runWorkspaceMarkers did not write the marker")
	_ = os.RemoveAll(filepath.Join(md, wsmarker.DirName)) // the first sync is spent: only a tick can recreate it now
	waitMarker(t, marker, "deleted marker dir not recreated by the tick")
}

// waitMarker polls for path for up to 2 s.
func waitMarker(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWorkspaceMarkersConcurrent: routes, tick and sidebar loop call syncWorkspaceMarkers at once;
// red without markerMu (max in flight > 1), green with it.
func TestWorkspaceMarkersConcurrent(t *testing.T) {
	md := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", md)
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const (
		offWS   = "F8290864-F5B5-4B3F-9B93-0B027295DECC" // declared only
		boundWS = "0D7E5B1C-3A44-4F0E-9D11-3C1E2B9A7F01" // bound to a perso subject only
		bothWS  = "0D7E5B1C-3A44-4F0E-9D11-3C1E2B9A7F02" // bound and declared (only the store allows it; the route answers 409): bound wins
	)
	now := time.Now().UTC()
	if err := st.CreateTheme(&pigtail.Theme{ID: "thm_p", Name: "P", Kind: "area", VaultLink: "Perso/Areas/p", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubject(&pigtail.Subject{ID: "sub_p", ThemeID: "thm_p", Title: "perso subject", Slug: "p", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []string{boundWS, bothWS} {
		if err := st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: "sub_p", Source: "attach", BoundAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []string{offWS, bothWS} {
		if err := st.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: ws, Scope: "perso", DeclaredAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu                    sync.Mutex
		inFlight, maxInFlight int
	)
	s.markerTestHook = func() { // called inside the critical section, between the DB read and wsmarker.Sync
		mu.Lock()
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond) // widen the window: without markerMu, 16 goroutines overlap here
		mu.Lock()
		inFlight--
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				s.syncWorkspaceMarkers()
			}
		}()
	}
	wg.Wait()
	if maxInFlight != 1 {
		t.Fatalf("max concurrent syncWorkspaceMarkers = %d, want 1 (markerMu missing?)", maxInFlight)
	}
	dir := filepath.Join(md, wsmarker.DirName)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("markers = %v, want exactly %s, %s, %s (no *.tmp left)", entries, offWS, boundWS, bothWS)
	}
	// Review Focus 1: a link to a perso subject has its marker (a work cell's hook stays silent too); bound wins over off-topic.
	for ws, want := range map[string]string{offWS: "off-topic\n", boundWS: "bound\n", bothWS: "bound\n"} {
		if got, err := os.ReadFile(filepath.Join(dir, ws)); err != nil || string(got) != want {
			t.Fatalf("marker %s = %q %v, want %q", ws, got, err, want)
		}
	}
}

// TestWorkspaceMarkersLogOncePerError: a write that keeps failing the same way logs one line, not one per tick (§ Pannes).
func TestWorkspaceMarkersLogOncePerError(t *testing.T) {
	md := t.TempDir()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", md)
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(md, "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.markerDir = filepath.Join(blocker, wsmarker.DirName) // MkdirAll fails with the same message every time
	var logged []string
	s.markerLogf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	s.syncWorkspaceMarkers()
	s.syncWorkspaceMarkers()
	if len(logged) != 1 || !strings.Contains(logged[0], "wsmarker: sync ") {
		t.Fatalf("wsmarker log lines = %d %q, want 1 per distinct error", len(logged), logged)
	}
}

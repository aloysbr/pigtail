package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func endPeerSession(t *testing.T, f *relayFixture, id string) {
	t.Helper()
	rec := doJSON(t, f.r, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: id}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("end = %d", rec.Code)
	}
}

// D1: a cmux restart ends the session (SessionEnd) and resumes it under the
// same id; the declared wait comes back with its marker.
func TestRelaunchRestoresDeclaredWait(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex"})
	declareWait(t, f, "s1", "arbitrage")
	endPeerSession(t, f, "s1")
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 || f.waitMarker("s1") {
		t.Fatalf("end kept the wait: %+v marker=%v", ws, f.waitMarker("s1"))
	}
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex", Source: "hook"})
	ws, _ := f.st.SessionWaits("s1")
	if len(ws) != 1 || ws[0].Type != "arbitrage" || ws[0].Source != pigtail.WaitSourceDeclared {
		t.Fatalf("waits after the relaunch = %+v", ws)
	}
	if !f.waitMarker("s1") {
		t.Fatal("wait marker not back: the next prompt could not clear the wait")
	}
}

// A snapshot of a session that ended, whose process is gone, restores nothing
// (it would flap: restored, then judged gone and cleared).
func TestSnapshotOfDeadSessionRestoresNothing(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex"})
	declareWait(t, f, "s1", "review")
	endPeerSession(t, f, "s1")
	cfg := t.TempDir() // no sessions/<pid>.json: not alive
	if err := f.rl.RecordSnapshot([]pigtail.PeerSession{{SessionID: "s1", Scope: "globex", PID: os.Getpid(), ConfigDir: cfg}}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 || f.waitMarker("s1") {
		t.Fatalf("a dead session got its wait back: %+v marker=%v", ws, f.waitMarker("s1"))
	}
}

// The sidebar tracker judging the session gone clears its wait without any
// hook; the resumed session, seen alive by a snapshot, gets it back.
func TestGoneClearThenResumeRestores(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex"})
	declareWait(t, f, "s1", "review")
	if err := f.rl.ClearWaitsKeeping("s1", true, pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("gone clear kept the wait: %+v", ws)
	}
	cfg := t.TempDir()
	pid := os.Getpid()
	if err := os.MkdirAll(filepath.Join(cfg, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"pid":` + strconv.Itoa(pid) + `,"sessionId":"s1","cwd":"/w"}`)
	if err := os.WriteFile(filepath.Join(cfg, "sessions", strconv.Itoa(pid)+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.rl.RecordSnapshot([]pigtail.PeerSession{{SessionID: "s1", Scope: "globex", PID: pid, ConfigDir: cfg}}); err != nil {
		t.Fatal(err)
	}
	ws, _ := f.st.SessionWaits("s1")
	if len(ws) != 1 || ws[0].Type != "review" || !f.waitMarker("s1") {
		t.Fatalf("waits after resume = %+v marker=%v", ws, f.waitMarker("s1"))
	}
}

// aliveSession writes the Claude Code session file a snapshot reads to judge a
// process alive (this test process stands for it) and returns the config dir.
func aliveSession(t *testing.T, id string) (cfg string, pid int) {
	t.Helper()
	cfg, pid = t.TempDir(), os.Getpid()
	if err := os.MkdirAll(filepath.Join(cfg, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"pid":` + strconv.Itoa(pid) + `,"sessionId":"` + id + `","cwd":"/w"}`)
	if err := os.WriteFile(filepath.Join(cfg, "sessions", strconv.Itoa(pid)+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg, pid
}

func drainWaitEvents(sub *events.Subscription) (changed, opened int) {
	for {
		select {
		case e := <-sub.C:
			switch e.Type {
			case pigtail.EventSessionWaitChanged:
				changed++
			case pigtail.EventWaitOpened:
				opened++
			}
		default:
			return changed, opened
		}
	}
}

// A restore is told like a declaration: session.wait_changed, wait.opened and
// its log line (the sidebar and the board learn of it, the user can read why).
func TestRestorePublishesEventsAndLogs(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	var mu sync.Mutex
	var logs []string
	f.rl.svc.Logf = func(format string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, a...))
		mu.Unlock()
	}
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionWaitChanged), string(pigtail.EventWaitOpened))
	defer sub.Close()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex"})
	declareWait(t, f, "s1", "arbitrage")
	endPeerSession(t, f, "s1")
	drainWaitEvents(sub) // the declaration and the end are not under test
	mu.Lock()
	logs = nil
	mu.Unlock()

	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex", Source: "hook"})
	changed, opened := drainWaitEvents(sub)
	if changed != 1 || opened != 1 {
		t.Fatalf("events after the restore: session.wait_changed=%d wait.opened=%d, want 1 and 1", changed, opened)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, l := range logs {
		if strings.Contains(l, "restored arbitrage (declared) after session end") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no restore log line in %q", logs)
	}
}

// The window between SessionEnd and the process's death: the peer is ended,
// its pid still runs and names it, so a snapshot takes it for a resumed
// session. Defined outcome: exactly one restore (one wait.opened); further
// snapshots add none; once the process is gone and the sidebar loop clears the
// wait, dead snapshots restore nothing — no flapping.
func TestSnapshotInSessionEndWindowRestoresOnce(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionWaitChanged), string(pigtail.EventWaitOpened))
	defer sub.Close()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex"})
	declareWait(t, f, "s1", "review")
	endPeerSession(t, f, "s1")
	drainWaitEvents(sub)

	cfg, pid := aliveSession(t, "s1")
	snap := []pigtail.PeerSession{{SessionID: "s1", Scope: "globex", PID: pid, ConfigDir: cfg}}
	for i := 0; i < 3; i++ {
		if err := f.rl.RecordSnapshot(snap); err != nil {
			t.Fatal(err)
		}
	}
	if _, opened := drainWaitEvents(sub); opened != 1 {
		t.Fatalf("wait.opened over 3 snapshots in the window = %d, want exactly 1", opened)
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 1 {
		t.Fatalf("waits in the window = %+v, want the restored one", ws)
	}

	// The process dies; the sidebar loop judges the session gone.
	if err := os.RemoveAll(filepath.Join(cfg, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := f.rl.ClearWaitsKeeping("s1", true, pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	drainWaitEvents(sub)
	for i := 0; i < 2; i++ {
		if err := f.rl.RecordSnapshot(snap); err != nil {
			t.Fatal(err)
		}
	}
	if changed, opened := drainWaitEvents(sub); changed != 0 || opened != 0 {
		t.Fatalf("events from dead snapshots after the clear: changed=%d opened=%d, want none", changed, opened)
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("wait flapped back after the process died: %+v", ws)
	}
}

// D7: a `working` wait does not come back after a session end, so the resumed
// session carries no wait and the idle pause may take it after the threshold.
func TestRelaunchDoesNotRestoreWorking(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex"})
	declareWait(t, f, "s1", "working")
	endPeerSession(t, f, "s1")
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex", Source: "hook"})
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 || f.waitMarker("s1") {
		t.Fatalf("working came back after the restart: %+v marker=%v", ws, f.waitMarker("s1"))
	}
	// No wait left: relay.PauseAllowed ("idle 2h") lets the idle pause take it.
}

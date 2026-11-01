package handlers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Idle session pause (sub_x000000000000036): every test goes through
// sweepIdle, the one call site.

// idleSince moves the native status time of pid to d before the fixture's now.
func (f *relayFixture) idleSince(fw *fakeWake, pid int, d time.Duration) {
	n := fw.natives[pid]
	n.StatusUpdatedAt = f.clock().Add(-d).UnixMilli()
	fw.natives[pid] = n
}

// pauseEnv is the environment of a claude started by the `c` launcher.
func pauseEnv(dir string) string {
	return "claude CLAUDE_RELAUNCH_FILE=" + filepath.Join(dir, "_dev_ttys001.sid") + " CLAUDE_RELAUNCH_PROTO=1"
}

// idlePauseFixture: s-alpha (pid 7, surf) idle for 3 h, unfocused, input
// empty, no message pending, started by the launcher; the idle pause on at
// one hour.
func idlePauseFixture(t *testing.T) (*relayFixture, *fakeWake, *wakeLog) {
	t.Helper()
	f, fw, lg := newWakeFixture(t)
	f.rl.cfg.IdlePause = time.Hour
	f.rl.cfg.RelaunchDir = t.TempDir()
	fw.environ = map[int]string{7: pauseEnv(f.rl.cfg.RelaunchDir), 8: pauseEnv(f.rl.cfg.RelaunchDir)}
	f.wakePeers(t, alphaPeer())
	f.idleSince(fw, 7, 3*time.Hour)
	fw.resetCounts()
	return f, fw, lg
}

// twoIdle adds s-gamma (pid 8, surf2): s-gamma idle 3 h, s-alpha 2 h.
func twoIdle(t *testing.T, f *relayFixture, fw *fakeWake, ps ...pigtail.PeerSession) {
	t.Helper()
	fw.natives[8] = claudesession.Native{SessionID: "s-gamma", Status: "idle", Kind: "interactive"}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
	if len(ps) == 0 {
		ps = []pigtail.PeerSession{alphaPeer(), gammaPeer()}
	}
	f.peers(t, ps...)
	f.idleSince(fw, 7, 2*time.Hour)
	f.idleSince(fw, 8, 3*time.Hour)
	fw.resetCounts()
}

// pauseJournal returns the journal rows of one session-pause kind.
func pauseJournalRows(t *testing.T, f *relayFixture, kind string) []pigtail.JournalEntry {
	t.Helper()
	rows, err := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func drainEvents(sub *events.Subscription) []pigtail.Event {
	var out []pigtail.Event
	for {
		select {
		case e := <-sub.C:
			out = append(out, e)
		default:
			return out
		}
	}
}

func sidFile(f *relayFixture) string { return filepath.Join(f.rl.cfg.RelaunchDir, "_dev_ttys001.sid") }

func pauseRow(t *testing.T, f *relayFixture, s string) *pigtail.SessionPause {
	t.Helper()
	r, err := f.st.GetPause(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func peerOf(t *testing.T, f *relayFixture, s string) *pigtail.PeerSession {
	t.Helper()
	p, err := f.st.GetPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// assertHeld: no signal, no pause row, no .sid, the hold logged.
func assertHeld(t *testing.T, f *relayFixture, fw *fakeWake, lg *wakeLog, s, reason string) {
	t.Helper()
	if len(fw.signals) != 0 {
		t.Fatalf("signalled %v; log:\n%s", fw.signals, lg)
	}
	if r := pauseRow(t, f, s); r != nil {
		t.Fatalf("pause row %+v", r)
	}
	if _, err := os.Stat(sidFile(f)); !os.IsNotExist(err) {
		t.Fatalf(".sid written: %v", err)
	}
	if lg.count("pause: "+s+" held: "+reason) < 1 {
		t.Fatalf("hold %q not logged:\n%s", reason, lg)
	}
}

func TestPauseStopsOldestIdleSession(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	twoIdle(t, f, fw)
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.signals, []int{8}) {
		t.Fatalf("signals = %v, want [8]", fw.signals)
	}
	fw.environ[7] = "claude"
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.signals, []int{8, 7}) {
		t.Fatalf("signals = %v, want [8 7]", fw.signals)
	}
}

// D20: the re-reads, then the store writes in order, then the signal; no
// re-read after BeginPause (errata 4: the order is the store's state read
// from the fakes).
func TestPauseOrderChecksThenMutationsThenSignal(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	twoIdle(t, f, fw)
	var order []string
	fw.onAlive = func(pid int) {
		if pid != 8 {
			return
		}
		r, _ := f.st.GetPause("s-gamma")
		p, _ := f.st.GetPeer("s-gamma")
		order = append(order, "alive")
		if r != nil || p.EndedAt != nil {
			t.Errorf("SessionAlive after the mutations began: row %+v, ended %v", r, p.EndedAt)
		}
	}
	fw.onFocus = func() { order = append(order, "focus") }
	fw.onSignal = func(pid int) {
		order = append(order, "signal")
		r, _ := f.st.GetPause("s-gamma")
		if r == nil || r.State != pigtail.PauseStopping || r.SignaledAt == nil {
			t.Errorf("row at the signal = %+v", r)
		}
		if b, err := os.ReadFile(sidFile(f)); err != nil || string(b) != "s-gamma\n" {
			t.Errorf(".sid at the signal = %q, %v", b, err)
		}
		if p, _ := f.st.GetPeer("s-gamma"); p.EndedAt == nil {
			t.Error("peer not ended at the signal")
		}
		if in, _ := f.rl.inStandby("s-gamma"); !in {
			t.Error("not in Standby at the signal")
		}
		if sb, _ := f.st.GetStandby("s-gamma"); sb == nil || sb.CompactedAt == nil {
			t.Errorf("compaction not settled at the signal: %+v", sb)
		}
	}
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.signals, []int{8}) {
		t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
	}
	// candidate's SessionAlive, then the re-reads (focus, alive), then the signal.
	if n := len(order); n < 3 || order[n-1] != "signal" || order[n-2] != "alive" || order[n-3] != "focus" {
		t.Fatalf("order = %v", order)
	}
}

func TestPauseIncludesStandby(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	t0 := f.now.Add(-2 * time.Hour).Truncate(time.Millisecond)
	if err := f.st.SetStandby("s-alpha", t0); err != nil {
		t.Fatal(err)
	}
	compactedAlready(t, f, "s-alpha")
	f.rl.sweepIdle()
	if len(fw.signals) != 1 {
		t.Fatalf("signals = %v", fw.signals)
	}
	sb, _ := f.st.GetStandby("s-alpha")
	if sb == nil || !sb.Since.Equal(t0) {
		t.Fatalf("standby = %+v, want since %v", sb, t0)
	}
	if r := pauseRow(t, f, "s-alpha"); r == nil || r.EnteredStandby {
		t.Fatalf("row = %+v", r)
	}
}

func TestPauseRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, reason string
		set          func(t *testing.T, f *relayFixture, fw *fakeWake)
	}{
		{"draft", "", func(t *testing.T, _ *relayFixture, fw *fakeWake) { fw.screen = screenFixture(t, "screen-draft.txt") }},
		{"usage limit", "usage limit wait (not idle)", func(_ *testing.T, _ *relayFixture, fw *fakeWake) {
			fw.screen = strings.TrimRight(fw.screen, "\n") + "\n⏺ Usage limit reached · continuing automatically at 11:40pm · esc to cancel\n"
		}},
		{"focused", "", func(_ *testing.T, _ *relayFixture, fw *fakeWake) { fw.focused = surf }},
		{"pending", "messages pending", func(t *testing.T, f *relayFixture, fw *fakeWake) { f.sendTo(t, fw, "s-alpha") }},
		{"shell", "background task running (status shell)", func(_ *testing.T, _ *relayFixture, fw *fakeWake) {
			n := fw.natives[7]
			n.Status = "shell"
			fw.natives[7] = n
		}},
		{"frozen", "", func(_ *testing.T, _ *relayFixture, fw *fakeWake) { fw.frozen = map[string]bool{"s-alpha": true} }},
		{"review", "wait review (declared)", func(t *testing.T, f *relayFixture, _ *fakeWake) { declareWait(t, f, "s-alpha", "review") }},
		{"mac held alive", "", func(t *testing.T, f *relayFixture, _ *fakeWake) {
			rec := doJSON(t, f.r, "POST", "/peers/s-alpha/wait", pigtail.SessionWaitRequest{Type: "mac", Source: "held", HolderPID: 4242})
			if rec.Code != 200 {
				t.Fatalf("mac = %d %s", rec.Code, rec.Body.String())
			}
		}},
		{"standby unreadable", "standby unreadable: disk", func(_ *testing.T, f *relayFixture, _ *fakeWake) {
			f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) { return nil, errors.New("disk") }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, fw, lg := idlePauseFixture(t)
			tc.set(t, f, fw)
			f.rl.sweepIdle()
			assertHeld(t, f, fw, lg, "s-alpha", tc.reason)
		})
	}
}

// D21: in Standby a pending message does not hold the pause; outside it does.
func TestPauseMessagesPendingStandby(t *testing.T) {
	t.Parallel()
	t.Run("in standby", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		if err := f.st.SetStandby("s-alpha", f.now); err != nil {
			t.Fatal(err)
		}
		compactedAlready(t, f, "s-alpha")
		f.sendTo(t, fw, "s-alpha")
		f.rl.sweepIdle()
		if len(fw.signals) != 1 {
			t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
		}
		if n, _ := f.st.CountPending("s-alpha", f.now); n != 1 {
			t.Fatalf("pending = %d, want the message still queued", n)
		}
	})
	t.Run("outside standby", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		f.sendTo(t, fw, "s-alpha")
		f.rl.sweepIdle()
		assertHeld(t, f, fw, lg, "s-alpha", "messages pending")
	})
	// (i) the Standby readable for the candidacies (the wake's, the
	// compaction's, the wrap's, the pause's), unreadable from the sixth read (the wrap reads each peer's Standby too)
	// (pauseHeldLocal's): held there, before any cmux call.
	t.Run("unreadable at the pending check", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		if err := f.st.SetStandby("s-alpha", f.now); err != nil {
			t.Fatal(err)
		}
		compactedAlready(t, f, "s-alpha")
		f.sendTo(t, fw, "s-alpha")
		calls := 0
		f.rl.getStandby = func(s string) (*pigtail.SessionStandby, error) {
			calls++
			if calls >= 6 {
				return nil, errors.New("disk")
			}
			return f.st.GetStandby(s)
		}
		f.rl.sweepIdle()
		assertHeld(t, f, fw, lg, "s-alpha", "standby unreadable (pending check)")
		if fw.screenCalls != 0 || calls != 6 {
			t.Fatalf("screen calls %d, getStandby calls %d; want 0 and 6", fw.screenCalls, calls)
		}
	})
	// (ii) unreadable only from the screen read on: held by the D26 re-read.
	t.Run("unreadable at the recheck", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		if err := f.st.SetStandby("s-alpha", f.now); err != nil {
			t.Fatal(err)
		}
		compactedAlready(t, f, "s-alpha")
		f.sendTo(t, fw, "s-alpha")
		broken := false
		f.rl.getStandby = func(s string) (*pigtail.SessionStandby, error) {
			if broken {
				return nil, errors.New("disk")
			}
			return f.st.GetStandby(s)
		}
		fw.onScreen = func() { broken = true }
		f.rl.sweepIdle()
		assertHeld(t, f, fw, lg, "s-alpha", "standby unreadable (recheck)")
		if fw.screenCalls != 1 {
			t.Fatalf("screen calls = %d, want 1", fw.screenCalls)
		}
	})
}

// An idle declared done session is left to the idle wrap: never signalled.
func TestPauseIgnoresWatcherAndDoneWait(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	f.rl.register("s-alpha")
	declareWait(t, f, "s-alpha", "done")
	f.rl.sweepIdle()
	if len(fw.signals) != 0 {
		t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
	}
	if r := pauseRow(t, f, "s-alpha"); r != nil {
		t.Fatalf("pause row = %+v", r)
	}
}

// A pid carrying an idle-wrap marker is never paused afterwards (D2), nor when
// the journal cannot be read.
func TestPauseHoldsAWrapMarkedPid(t *testing.T) {
	t.Parallel()
	t.Run("sent", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		if _, err := f.rl.wrapJournal(*peerOf(t, f, "s-alpha"), "sent", "typed "+testDoneCommand, f.clock()); err != nil {
			t.Fatal(err)
		}
		f.rl.sweepIdle()
		assertHeld(t, f, fw, lg, "s-alpha", "wrapped by the idle wrap")
	})
	t.Run("unreadable journal", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		f.rl.listJournal = func(store.JournalFilter) ([]pigtail.JournalEntry, error) { return nil, errors.New("disk") }
		f.rl.sweepIdle()
		if len(fw.signals) != 0 {
			t.Fatalf("signalled %v; log:\n%s", fw.signals, lg)
		}
		if lg.count("pause: s-alpha held: journal unreadable") < 1 {
			t.Fatalf("hold not logged:\n%s", lg)
		}
	})
}

// The pause still ignores an open watcher (D1: wrapped stays pausable).
func TestPauseIgnoresWatcherWrappedWait(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	f.rl.register("s-alpha")
	declareWait(t, f, "s-alpha", "wrapped")
	f.rl.sweepIdle()
	if len(fw.signals) != 1 {
		t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
	}
}

// Review Focus 1: a turn during the cmux calls holds the pause.
func TestPauseRechecksNativeBeforeKill(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		move func(n *claudesession.Native)
	}{
		{"status time", func(n *claudesession.Native) { n.StatusUpdatedAt++ }},
		{"session id", func(n *claudesession.Native) { n.SessionID = "s-other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fw, lg := idlePauseFixture(t)
			fw.onScreen = func() {
				n := fw.natives[7]
				tc.move(&n)
				fw.natives[7] = n
			}
			f.rl.sweepIdle()
			assertHeld(t, f, fw, lg, "s-alpha", "left idle during the checks")
		})
	}
}

func TestPauseWithoutLauncher(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	fw.environ[7] = "claude HOME=/Users/x"
	f.rl.sweepIdle()
	if len(fw.signals) != 1 {
		t.Fatalf("signals = %v", fw.signals)
	}
	if es, _ := os.ReadDir(f.rl.cfg.RelaunchDir); len(es) != 0 {
		t.Fatalf("files in the relaunch dir: %v", es)
	}
	if r := pauseRow(t, f, "s-alpha"); r == nil || r.SidPath != "" {
		t.Fatalf("row = %+v", r)
	}
}

func TestPauseRefusesUnreadableEnvAndForeignPath(t *testing.T) {
	t.Parallel()
	t.Run("unreadable", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		fw.environErr = errors.New("ps")
		f.rl.sweepIdle()
		assertHeld(t, f, fw, lg, "s-alpha", "environment unreadable")
	})
	t.Run("foreign", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		fw.environ[7] = "claude CLAUDE_RELAUNCH_FILE=/tmp/x.sid"
		f.rl.sweepIdle()
		assertHeld(t, f, fw, lg, "s-alpha", "relaunch file outside "+f.rl.cfg.RelaunchDir)
	})
}

func TestPauseSuccess(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	g := gammaPeer()
	g.WorkspaceID = "ws-pigtail-1"
	g.CmuxWorkspaceID = "aa11bb22-0000-4000-8000-000000000001"
	twoIdle(t, f, fw, alphaPeer(), g)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionPauseChanged))
	f.rl.sweepIdle()
	r := pauseRow(t, f, "s-gamma")
	if r == nil || r.State != pigtail.PausePaused || r.PausedAt == nil || r.PID != 8 || r.SurfaceID != surf2 {
		t.Fatalf("row = %+v; log:\n%s", r, lg)
	}
	if r.CmuxWorkspaceID != strings.ToUpper(g.CmuxWorkspaceID) {
		t.Fatalf("row cmux workspace = %q, want the cmux id, not %q", r.CmuxWorkspaceID, g.WorkspaceID)
	}
	if len(fw.sent)+len(fw.compacts) != 0 {
		t.Fatalf("cmux writes: %v %v", fw.sent, fw.compacts)
	}
	rows := pauseJournalRows(t, f, "session-pause-paused")
	if len(rows) != 1 || rows[0].ID != "session-pause:s-gamma:8:paused" || rows[0].Scope != string(sessionScope(gammaPeer())) || rows[0].Scope == "gx" {
		t.Fatalf("journal = %+v", rows)
	}
	evs := drainEvents(sub)
	if len(evs) != 1 || evs[0].Data["paused"] != true || evs[0].Data["session_id"] != "s-gamma" ||
		evs[0].Data["cmux_workspace_id"] != g.CmuxWorkspaceID {
		t.Fatalf("events = %+v", evs)
	}
	if lg.count("pause: s-gamma paused (pid 8, idle 3h0m0s, launcher yes)") != 1 {
		t.Fatalf("log:\n%s", lg)
	}
}

func TestPauseFailureUndoes(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionPauseFailed))
	fw.signalErr = errors.New("EPERM")
	f.rl.sweepIdle()
	if _, err := os.Stat(sidFile(f)); !os.IsNotExist(err) {
		t.Fatalf(".sid kept: %v", err)
	}
	if in, _ := f.rl.inStandby("s-alpha"); in {
		t.Fatal("standby kept")
	}
	if r := pauseRow(t, f, "s-alpha"); r != nil {
		t.Fatalf("row kept: %+v", r)
	}
	if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 1 || rows[0].ID != "session-pause:s-alpha:7:failed" {
		t.Fatalf("journal = %+v", rows)
	}
	if evs := drainEvents(sub); len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
	fw.signalErr = nil
	f.rl.sweepIdle()
	if len(fw.signals) != 0 || lg.count("pause: s-alpha held: given up earlier for pid 7") != 1 {
		t.Fatalf("signals %v; log:\n%s", fw.signals, lg)
	}
}

func TestPauseSlowExitStaysStopping(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	fw.stayAlive = true
	f.rl.sweepIdle()
	r := pauseRow(t, f, "s-alpha")
	if r == nil || r.State != pigtail.PauseStopping || r.SignaledAt == nil {
		t.Fatalf("row = %+v", r)
	}
	if b, err := os.ReadFile(sidFile(f)); err != nil || string(b) != "s-alpha\n" {
		t.Fatalf(".sid = %q, %v", b, err)
	}
	if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 0 {
		t.Fatalf("journal = %+v", rows)
	}
	if in, _ := f.rl.inStandby("s-alpha"); !in {
		t.Fatal("standby dropped")
	}
}

func TestPauseRefusesBackgroundWork(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		work         []string
		err          error
	}{
		{"agent", "background work (tâche a1)", []string{"tâche a1"}, nil},
		{"monitor", "background work (shell Bash 4242)", []string{"shell Bash 4242"}, nil},
		{"unreadable", "background work unreadable", nil, errors.New("transcript illisible")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fw, lg := idlePauseFixture(t)
			fw.background = func(string, string, int, string) ([]string, error) { return tc.work, tc.err }
			f.rl.sweepIdle()
			assertHeld(t, f, fw, lg, "s-alpha", tc.reason)
		})
	}
}

// T-k: a Standby's compaction is settled before a pause.
func TestPauseRefusesCompactInFlight(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		mark  func(f *relayFixture) error
		pause bool
	}{
		{"unsent", func(*relayFixture) error { return nil }, false},
		// Sent at the session's current status time: not proven yet.
		{"sent", func(f *relayFixture) error {
			return f.st.MarkStandbyCompactSent("s-alpha", f.now, f.now.Add(-3*time.Hour).UnixMilli(), 7)
		}, false},
		{"compacted", func(f *relayFixture) error { return f.st.MarkStandbyCompacted("s-alpha", f.now) }, true},
		{"failed", func(f *relayFixture) error { return f.st.MarkStandbyCompactFailed("s-alpha", f.now) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fw, lg := idlePauseFixture(t)
			if err := f.st.SetStandby("s-alpha", f.now); err != nil {
				t.Fatal(err)
			}
			if err := tc.mark(f); err != nil {
				t.Fatal(err)
			}
			// The compaction's own typing is not what this test measures.
			fw.screen = screenFixture(t, "screen-idle-empty.txt")
			f.rl.cfg.Wake.SendLine = func(string, string) error { return errors.New("no typing here") }
			f.rl.sweepIdle()
			if tc.pause {
				if len(fw.signals) != 1 {
					t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
				}
				return
			}
			assertHeld(t, f, fw, lg, "s-alpha", "compaction pending")
		})
	}
}

func TestPauseRefusesSessionTypedThisSweep(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	if err := f.st.SetStandby("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	// The compaction types /compact in this sweep; the row is then read as
	// settled (a fake), so only sw.typed can hold the pause.
	f.rl.getStandby = func(s string) (*pigtail.SessionStandby, error) {
		sb, err := f.st.GetStandby(s)
		if sb != nil && len(fw.compacts) > 0 {
			at := f.now
			sb.CompactedAt = &at
		}
		return sb, err
	}
	f.rl.sweepIdle()
	if len(fw.compacts) != 1 {
		t.Fatalf("compacts = %v; log:\n%s", fw.compacts, lg)
	}
	assertHeld(t, f, fw, lg, "s-alpha", "typed into during this sweep")
}

func TestPauseSettlesCompactionOnEnter(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	f.rl.sweepIdle()
	if len(fw.signals) != 1 {
		t.Fatalf("signals = %v", fw.signals)
	}
	sb, _ := f.st.GetStandby("s-alpha")
	if sb == nil || sb.CompactedAt == nil {
		t.Fatalf("standby = %+v", sb)
	}
	// Resumed: a new claude, idle, registered again.
	p := alphaPeer()
	p.PID = 9
	fw.natives[9] = claudesession.Native{SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: f.now.UnixMilli()}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 9, "s-alpha"))
	f.peers(t, p)
	f.rl.sweepIdle()
	if len(fw.compacts) != 0 {
		t.Fatalf("compacts = %v", fw.compacts)
	}
}

func TestPauseRecheckAliveAndFocus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		arm  func(f *relayFixture, fw *fakeWake)
	}{
		{"not alive", func(_ *relayFixture, fw *fakeWake) {
			fw.onScreen = func() { fw.alive = false }
		}},
		{"status moved", func(_ *relayFixture, fw *fakeWake) {
			fw.background = func(string, string, int, string) ([]string, error) {
				n := fw.natives[7]
				n.StatusUpdatedAt += 5
				fw.natives[7] = n
				return nil, nil
			}
		}},
		{"focused", func(_ *relayFixture, fw *fakeWake) {
			fw.background = func(string, string, int, string) ([]string, error) { fw.focused = surf; return nil, nil }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fw, lg := idlePauseFixture(t)
			tc.arm(f, fw)
			f.rl.sweepIdle()
			assertHeld(t, f, fw, lg, "s-alpha", "left idle during the checks")
			if in, _ := f.rl.inStandby("s-alpha"); in {
				t.Fatal("standby entered")
			}
			if p := peerOf(t, f, "s-alpha"); p.EndedAt != nil {
				t.Fatal("peer ended")
			}
			if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 0 {
				t.Fatalf("journal = %+v", rows)
			}
			// Not exempted: the next sweep, the move gone, pauses it.
			fw.alive, fw.onScreen, fw.background, fw.focused = true, nil, nil, ""
			f.idleSince(fw, 7, 3*time.Hour)
			f.rl.sweepIdle()
			if len(fw.signals) != 1 {
				t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
			}
		})
	}
}

// D20: the first of the two undos after the mutations began.
func TestPauseRecheckAbortRevivesPeer(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*relayFixture, *fakeWake, *wakeLog) {
		f, fw, lg := idlePauseFixture(t)
		fw.signalErr = errors.New("EPERM")
		return f, fw, lg
	}
	check := func(t *testing.T, f *relayFixture) {
		t.Helper()
		if in, _ := f.rl.inStandby("s-alpha"); in {
			t.Fatal("standby kept")
		}
		if r := pauseRow(t, f, "s-alpha"); r != nil {
			t.Fatalf("row = %+v", r)
		}
		if _, err := os.Stat(sidFile(f)); !os.IsNotExist(err) {
			t.Fatalf(".sid kept: %v", err)
		}
		if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 1 {
			t.Fatalf("journal = %+v", rows)
		}
	}
	t.Run("signal refused, pid alive", func(t *testing.T) {
		f, _, _ := setup(t)
		declareWait(t, f, "s-alpha", "wrapped") // a wait EndPeerClosing takes
		f.rl.sweepIdle()
		if p := peerOf(t, f, "s-alpha"); p.EndedAt != nil {
			t.Fatal("peer left ended")
		}
		check(t, f)
	})
	t.Run("signal refused, pid dead", func(t *testing.T) {
		f, fw, _ := setup(t)
		fw.onSignal = func(pid int) { fw.dead[pid] = true }
		f.rl.sweepIdle()
		if p := peerOf(t, f, "s-alpha"); p.EndedAt == nil {
			t.Fatal("peer revived while dead")
		}
		check(t, f)
	})
}

func TestPauseHoldsOnAnyNilSeam(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Background", "SessionAlive", "Signal", "WaitGone", "ProcessEnviron"} {
		t.Run(name, func(t *testing.T) {
			f, fw, lg := idlePauseFixture(t)
			w := f.rl.cfg.Wake
			switch name {
			case "Background":
				w.Background = nil
			case "SessionAlive":
				w.SessionAlive = nil
			case "Signal":
				w.Signal = nil
			case "WaitGone":
				w.WaitGone = nil
			case "ProcessEnviron":
				w.ProcessEnviron = nil
			}
			f.rl.sweepIdle()
			if r := pauseRow(t, f, "s-alpha"); r != nil || len(fw.signals) != 0 || fw.screenCalls != 0 {
				t.Fatalf("row %+v, signals %v, screen calls %d", r, fw.signals, fw.screenCalls)
			}
			if lg.count("pause: held: not wired ("+name+" nil)") != 1 {
				t.Fatalf("log:\n%s", lg)
			}
		})
	}
}

func TestPauseBackgroundSeamGetsConfigDirAndScreen(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	fw.screen = screenFixture(t, "screen-idle-empty.txt") + "\n"
	var got []any
	fw.background = func(cfg, s string, pid int, screen string) ([]string, error) {
		got = []any{cfg, s, pid, screen}
		return nil, nil
	}
	f.rl.sweepIdle()
	if want := []any{"/cfg", "s-alpha", 7, fw.screen}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Background got %q, want %q", got, want)
	}
	if fw.screenCalls != 1 {
		t.Fatalf("screen calls = %d", fw.screenCalls)
	}
}

func TestPauseBeginFailureSkipsToNextCandidate(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	twoIdle(t, f, fw)
	if err := f.st.BeginPause(pigtail.SessionPause{SessionID: "s-gamma", PID: 8, State: pigtail.PausePaused, Since: f.now}); err != nil {
		t.Fatal(err)
	}
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.signals, []int{7}) {
		t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
	}
}

func TestPauseFailureKeepsForeignSid(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	fw.onSignal = func(int) {
		if err := os.WriteFile(sidFile(f), []byte("s-other\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fw.signalErr = errors.New("EPERM")
	f.rl.sweepIdle()
	if b, err := os.ReadFile(sidFile(f)); err != nil || string(b) != "s-other\n" {
		t.Fatalf(".sid = %q, %v", b, err)
	}
}

func TestPauseOffAndStartupGrace(t *testing.T) {
	t.Parallel()
	t.Run("off", func(t *testing.T) {
		f, fw, _ := idlePauseFixture(t)
		called := false
		fw.background = func(string, string, int, string) ([]string, error) { called = true; return nil, nil }
		f.rl.cfg.IdlePause = 0
		f.rl.sweepIdle()
		if len(fw.signals) != 0 || fw.cmuxCalls != 0 || called || fw.screenCalls != 0 {
			t.Fatalf("signals %v, %d cmux calls, background %v", fw.signals, fw.cmuxCalls, called)
		}
	})
	t.Run("grace", func(t *testing.T) {
		f, fw, lg := idlePauseFixture(t)
		called := false
		fw.background = func(string, string, int, string) ([]string, error) { called = true; return nil, nil }
		f.rl.startedAt = f.now
		f.rl.sweepIdle()
		assertHeld(t, f, fw, lg, "s-alpha", "daemon start-up grace")
		if called || fw.screenCalls != 0 {
			t.Fatalf("background %v, screen calls %d", called, fw.screenCalls)
		}
	})
}

// Final review M2: the pid exited on its own between the last re-read and
// the kill (ESRCH): the session moved, the undo is silent — no failed line,
// no event, the pid not exempted.
func TestPauseSignalESRCHIsSilent(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionPauseFailed))
	fw.signalErr = fmt.Errorf("kill: %w", syscall.ESRCH)
	f.rl.sweepIdle()
	if r := pauseRow(t, f, "s-alpha"); r != nil {
		t.Fatalf("row kept: %+v", r)
	}
	if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 0 {
		t.Fatalf("failed journal on ESRCH: %+v", rows)
	}
	if evs := drainEvents(sub); len(evs) != 0 {
		t.Fatalf("failed event on ESRCH: %+v", evs)
	}
	if lg.count("gave up") != 0 || lg.count("pause: s-alpha held: process gone before the signal") != 1 {
		t.Fatalf("log:\n%s", lg)
	}
}

// The pause types nothing and never asks the session budget: paneGuard has no
// admission parameter. Passes on the base; fails if a later change makes the
// pause ask.
func TestPauseNeverAcquires(t *testing.T) {
	t.Parallel()
	f, fw, _ := idlePauseFixture(t)
	b := &fakeBudget{allow: 0}
	b.install(f.rl.cfg.Wake)
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.signals, []int{7}) || len(fw.sent)+len(fw.compacts) != 0 {
		t.Fatalf("signals %v, typed %v %v: want the idle session paused, nothing typed", fw.signals, fw.sent, fw.compacts)
	}
	if got := b.asked(); len(got) != 0 || len(b.released) != 0 {
		t.Fatalf("asked %v released %v: the pause never touches the budget", got, b.released)
	}
}

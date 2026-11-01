package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const surf3 = "7C2F1A55-8B3E-4D4F-AD9F-2E3F4A5B6C7D"

// pausedGamma: s-alpha (pid 7) idle a minute, s-gamma (pid 8, surf2) idle
// 3 h and paused by one sweep. slow: the signal leaves pid 8 alive (the row
// stays stopping).
func pausedGamma(t *testing.T, slow bool) (*relayFixture, *fakeWake, *wakeLog) {
	t.Helper()
	f, fw, lg := idlePauseFixture(t)
	twoIdle(t, f, fw)
	f.idleSince(fw, 7, time.Minute)
	fw.stayAlive = slow
	f.rl.svc.Bus = events.New()
	f.rl.sweepIdle()
	want := pigtail.PausePaused
	if slow {
		want = pigtail.PauseStopping
	}
	if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != want || r.PID != 8 {
		t.Fatalf("row = %+v; log:\n%s", r, lg)
	}
	fw.stayAlive = false
	return f, fw, lg
}

// relaunched: claude pid 9 of s-gamma, idle since d, on surface sf.
func relaunched(f *relayFixture, fw *fakeWake, sf string, d time.Duration) pigtail.PeerSession {
	fw.natives[9] = claudesession.Native{SessionID: "s-gamma", Status: "idle", Kind: "interactive"}
	f.idleSince(fw, 9, d)
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", sf, 9, "s-gamma"))
	p := gammaPeer()
	p.PID, p.CmuxSurfaceID = 9, sf
	return p
}

func hookRegister(t *testing.T, f *relayFixture, p pigtail.PeerSession) {
	t.Helper()
	p.Source = "hook"
	f.peers(t, p)
}

// assertResumed: no row, no paused_at, the Standby kept with its compaction
// settled, the journal line of the new pid, one paused:false event.
func assertResumed(t *testing.T, f *relayFixture, sub *events.Subscription, pid string) {
	t.Helper()
	if r := pauseRow(t, f, "s-gamma"); r != nil {
		t.Fatalf("row kept: %+v", r)
	}
	if rows, _ := f.st.ListPauses(); len(rows) != 0 {
		t.Fatalf("pauses = %+v", rows)
	}
	for _, p := range getPeers(t, f, "?all=1") {
		if p.PausedAt != nil {
			t.Fatalf("paused_at on %s", p.SessionID)
		}
	}
	if sb, _ := f.st.GetStandby("s-gamma"); sb == nil || sb.CompactedAt == nil {
		t.Fatalf("standby = %+v", sb)
	}
	if rows := pauseJournalRows(t, f, "session-pause-resumed"); len(rows) != 1 || rows[0].ID != "session-pause:s-gamma:"+pid+":resumed" {
		t.Fatalf("journal = %+v", rows)
	}
	if sub != nil {
		evs := drainEvents(sub)
		if len(evs) != 1 || evs[0].Data["paused"] != false || evs[0].Data["session_id"] != "s-gamma" {
			t.Fatalf("events = %+v", evs)
		}
	}
}

// D15·t3: cmux relaunching the paused session at its restart is an ordinary
// resume, then a new pause at the threshold.
func TestCmuxRestartResumeEndsPause(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		surface  string
		snapshot bool
	}{
		{"hook same surface", surf2, false},
		{"hook other surface", surf3, false},
		{"snapshot only", surf2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fw, lg := pausedGamma(t, false)
			sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionPauseChanged))
			p := relaunched(f, fw, tc.surface, time.Minute)
			if tc.snapshot {
				if err := f.rl.RecordSnapshot([]pigtail.PeerSession{p}); err != nil {
					t.Fatal(err)
				}
			} else {
				hookRegister(t, f, p)
			}
			f.rl.sweepIdle() // under the threshold: no new pause
			assertResumed(t, f, sub, "9")
			if len(fw.signals) != 1 || len(fw.sent)+len(fw.compacts) != 0 {
				t.Fatalf("signals %v, sent %v, compacts %v; log:\n%s", fw.signals, fw.sent, fw.compacts, lg)
			}
			f.idleSince(fw, 9, 3*time.Hour)
			f.rl.sweepIdle()
			if n := len(fw.signals); n != 2 || fw.signals[1] != 9 {
				t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
			}
			if r := pauseRow(t, f, "s-gamma"); r == nil || r.PID != 9 || r.EnteredStandby {
				t.Fatalf("row = %+v", r)
			}
			if len(fw.compacts) != 0 {
				t.Fatalf("compacts = %v", fw.compacts)
			}
		})
	}
	// (d) cmux relaunches while the row is stopping.
	t.Run("relaunch while stopping", func(t *testing.T) {
		f, fw, lg := pausedGamma(t, true)
		hookRegister(t, f, relaunched(f, fw, surf2, time.Minute))
		f.rl.sweepIdle()
		if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != pigtail.PauseStopping {
			t.Fatalf("row = %+v while pid 8 lives", r)
		}
		fw.dead[8] = true
		f.rl.sweepIdle()
		if rows := pauseJournalRows(t, f, "session-pause-paused"); len(rows) != 1 || rows[0].ID != "session-pause:s-gamma:8:paused" {
			t.Fatalf("paused journal = %+v; log:\n%s", rows, lg)
		}
		f.rl.sweepIdle()
		assertResumed(t, f, nil, "9")
		if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 0 {
			t.Fatalf("failed journal = %+v", rows)
		}
		f.idleSince(fw, 9, 3*time.Hour)
		f.rl.sweepIdle()
		if n := len(fw.signals); fw.signals[n-1] != 9 {
			t.Fatalf("pid 9 exempted: signals %v", fw.signals)
		}
	})
	// (e) a message from another session while paused, in Standby: the
	// re-pause is not held, the message stays queued (D21).
	t.Run("message from another session", func(t *testing.T) {
		f, fw, lg := pausedGamma(t, false)
		hookRegister(t, f, relaunched(f, fw, surf2, time.Minute))
		f.peers(t, pigtail.PeerSession{SessionID: "s-beta", Name: "beta", Scope: "gx"})
		f.sendTo(t, fw, "s-gamma")
		f.idleSince(fw, 9, 3*time.Hour)
		f.rl.sweepIdle()
		if n := len(fw.signals); n != 2 || fw.signals[1] != 9 {
			t.Fatalf("signals = %v; log:\n%s", fw.signals, lg)
		}
		if n, _ := f.st.CountPending("s-gamma", f.now); n != 1 {
			t.Fatalf("pending = %d", n)
		}
	})
	// (f) D23 A: the user answers a wait of the subject during the pause: kept,
	// delivered at the resume, which lifts the Standby; no re-pause then.
	t.Run("answer from Ada", func(t *testing.T) {
		f, fw, _ := subjectFixture(t)
		lg := &wakeLog{}
		f.rl.svc.Logf = lg.logf
		f.rl.cfg.IdlePause = time.Hour
		f.rl.cfg.RelaunchDir = t.TempDir()
		fw.environ = map[int]string{8: pauseEnv(f.rl.cfg.RelaunchDir), 9: pauseEnv(f.rl.cfg.RelaunchDir)}
		fw.natives = map[int]claudesession.Native{8: {SessionID: "s-gamma", Status: "idle", Kind: "interactive"}}
		f.idleSince(fw, 8, 3*time.Hour)
		fw.listing = cmuxListingOf(cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
		g := gammaPeer()
		g.Scope, g.CmuxWorkspaceID, g.Source = "perso", wsSubj, "hook"
		if rec := doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{g}}); rec.Code != http.StatusOK {
			t.Fatalf("register = %d %s", rec.Code, rec.Body.String())
		}
		// The wait of an earlier, ended session of the subject.
		registerOn(t, f, perso, "s-a", wsSubj)
		id := declareArbitrage(t, f, perso, "s-a")
		endSession(t, f, perso, "s-a")
		fw.resetCounts()
		f.rl.sweepIdle()
		if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != pigtail.PausePaused {
			t.Fatalf("row = %+v; log:\n%s", r, lg)
		}
		if out := answerReco(t, f, id); out.Delivery != pigtail.DeliveryKept {
			t.Fatalf("answer = %+v, want kept", out)
		}
		if n, _ := f.st.CountPending("s-gamma", f.now); n != 0 {
			t.Fatalf("pending = %d during the pause", n)
		}
		if in, _ := f.rl.inStandby("s-gamma"); !in {
			t.Fatal("standby lifted during the pause")
		}
		p := relaunched(f, fw, surf2, 3*time.Hour)
		fw.listing = cmuxListingOf(cmuxEntry("s-gamma", surf2, 9, "s-gamma"))
		p.Scope, p.CmuxWorkspaceID, p.Source = "perso", wsSubj, "hook"
		if rec := doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{p}}); rec.Code != http.StatusOK {
			t.Fatalf("relaunch = %d %s", rec.Code, rec.Body.String())
		}
		if in, _ := f.rl.inStandby("s-gamma"); in {
			t.Fatal("standby kept after the kept answer's delivery")
		}
		if r := pauseRow(t, f, "s-gamma"); r != nil {
			t.Fatalf("row kept: %+v", r)
		}
		f.rl.sweepIdle()
		if len(fw.signals) != 1 || lg.count("pause: s-gamma held: messages pending") != 1 {
			t.Fatalf("signals %v; log:\n%s", fw.signals, lg)
		}
	})
}

// Review Focus 3: a daemon restart at each step of the pause.
func TestPauseFollowUpAfterRestart(t *testing.T) {
	t.Parallel()
	restart := func(f *relayFixture) {
		rl2 := NewRelay(f.rl.svc, f.rl.cfg)
		rl2.startedAt = f.now.Add(-time.Hour)
		f.rl = rl2
	}
	t.Run("before the signal", func(t *testing.T) {
		f, fw, _ := idlePauseFixture(t)
		if err := f.st.BeginPause(pigtail.SessionPause{SessionID: "s-alpha", PID: 7, SurfaceID: surf, State: pigtail.PauseStopping, Since: f.now}); err != nil {
			t.Fatal(err)
		}
		restart(f)
		f.rl.sweepIdle()
		if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 0 {
			t.Fatalf("failed = %+v", rows)
		}
		// The row went, the pid not exempted: this same sweep paused it.
		if r := pauseRow(t, f, "s-alpha"); r == nil || r.State != pigtail.PausePaused || len(fw.signals) != 1 {
			t.Fatalf("row %+v, signals %v", r, fw.signals)
		}
	})
	t.Run("signalled, pid dead", func(t *testing.T) {
		f, fw, _ := pausedGamma(t, true)
		restart(f)
		fw.dead[8] = true
		fw.resetCounts()
		f.rl.sweepIdle()
		if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != pigtail.PausePaused {
			t.Fatalf("row = %+v", r)
		}
		if len(fw.sent)+len(fw.compacts) != 0 {
			t.Fatalf("cmux writes %v %v", fw.sent, fw.compacts)
		}
	})
	t.Run("signalled, pid recycled", func(t *testing.T) {
		f, fw, _ := pausedGamma(t, true)
		restart(f)
		// Another claude holds pid 8: SessionAlive (the session's id) is false.
		f.rl.cfg.Wake.SessionAlive = func(_ string, pid int, s string) bool { return (pid != 8 || s != "s-gamma") && !fw.dead[pid] }
		f.rl.sweepIdle()
		if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != pigtail.PausePaused {
			t.Fatalf("row = %+v", r)
		}
	})
	for _, between := range []bool{false, true} {
		name := "signalled, pid survives"
		if between {
			name = "restart between MarkSignaled and Signal"
		}
		t.Run(name, func(t *testing.T) {
			f, fw, _ := idlePauseFixture(t)
			twoIdle(t, f, fw)
			f.idleSince(fw, 7, time.Minute)
			if between {
				// The row as a restart right after MarkSignaled leaves it.
				fw.onSignal = func(int) { panic(errDaemonDied) }
				func() {
					defer func() { _ = recover() }()
					f.rl.sweepIdle()
				}()
				fw.onSignal = nil
			} else {
				fw.stayAlive = true
				f.rl.sweepIdle()
			}
			restart(f)
			r := pauseRow(t, f, "s-gamma")
			if r == nil || r.State != pigtail.PauseStopping || r.SignaledAt == nil {
				t.Fatalf("row = %+v", r)
			}
			f.advance(time.Minute)
			f.rl.sweepIdle()
			if r := pauseRow(t, f, "s-gamma"); r == nil {
				t.Fatal("given up before 2 min")
			}
			f.advance(2 * time.Minute)
			f.rl.sweepIdle()
			if r := pauseRow(t, f, "s-gamma"); r != nil {
				t.Fatalf("row kept: %+v", r)
			}
			if _, err := readSid(f); err == nil {
				t.Fatal(".sid kept")
			}
			if rows := pauseJournalRows(t, f, "session-pause-failed"); len(rows) != 1 || rows[0].ID != "session-pause:s-gamma:8:failed" {
				t.Fatalf("failed = %+v", rows)
			}
			if in, _ := f.rl.inStandby("s-gamma"); in {
				t.Fatal("standby kept")
			}
		})
	}
}

type daemonDied struct{}

func (daemonDied) Error() string { return "daemon died" }

var errDaemonDied error = daemonDied{}

func readSid(f *relayFixture) ([]byte, error) { return os.ReadFile(sidFile(f)) }

// Review Focus 2: a snapshot that revives the peer of a paused, dead pid.
func TestPauseReEndsPeerUpsertedBySnapshot(t *testing.T) {
	t.Parallel()
	f, _, _ := pausedGamma(t, false)
	p := gammaPeer()
	p.UpdatedAt, p.Source = f.now, "snapshot"
	if err := f.st.UpsertPeer(&p); err != nil {
		t.Fatal(err)
	}
	f.rl.sweepIdle()
	if got := peerOf(t, f, "s-gamma"); got.EndedAt == nil {
		t.Fatal("peer left alive")
	}
	if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != pigtail.PausePaused {
		t.Fatalf("row = %+v", r)
	}
}

func TestSnapshotResumeEndsPause(t *testing.T) {
	t.Parallel()
	t.Run("new pid alive", func(t *testing.T) {
		f, fw, _ := pausedGamma(t, false)
		if err := f.rl.RecordSnapshot([]pigtail.PeerSession{relaunched(f, fw, surf2, time.Minute)}); err != nil {
			t.Fatal(err)
		}
		f.rl.sweepIdle()
		assertResumed(t, f, nil, "9")
	})
	t.Run("new pid not alive", func(t *testing.T) {
		f, fw, _ := pausedGamma(t, false)
		p := relaunched(f, fw, surf2, time.Minute)
		p.UpdatedAt, p.Source = f.now, "snapshot"
		if err := f.st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
		fw.dead[9] = true
		f.rl.sweepIdle()
		if r := pauseRow(t, f, "s-gamma"); r == nil {
			t.Fatal("row ended for a dead pid")
		}
	})
}

func TestResumeRegistrationEndsPause(t *testing.T) {
	t.Parallel()
	f, fw, _ := pausedGamma(t, false)
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionPauseChanged))
	hookRegister(t, f, relaunched(f, fw, surf2, time.Minute))
	assertResumed(t, f, sub, "9")
}

func TestSnapshotOfDeadPidKeepsPause(t *testing.T) {
	t.Parallel()
	f, _, _ := pausedGamma(t, false)
	if err := f.rl.RecordSnapshot([]pigtail.PeerSession{gammaPeer()}); err != nil {
		t.Fatal(err)
	}
	f.rl.sweepIdle()
	if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != pigtail.PausePaused {
		t.Fatalf("row = %+v", r)
	}
}

func TestOtherSessionOnSurfaceEndsPause(t *testing.T) {
	t.Parallel()
	f, _, lg := pausedGamma(t, false)
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionPauseChanged))
	hookRegister(t, f, pigtail.PeerSession{SessionID: "s-new", Name: "new", Scope: "gx", PID: 12, ConfigDir: "/cfg", CmuxSurfaceID: surf2})
	if r := pauseRow(t, f, "s-gamma"); r != nil {
		t.Fatalf("row kept: %+v", r)
	}
	if evs := drainEvents(sub); len(evs) != 1 || evs[0].Data["session_id"] != "s-gamma" || evs[0].Data["paused"] != false {
		t.Fatalf("events = %+v", evs)
	}
	if lg.count("pause: s-gamma pause ended: s-new took its surface") != 1 {
		t.Fatalf("log:\n%s", lg)
	}
}

func TestGetPeersShowsPausedAt(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	g := gammaPeer()
	g.Scope = "perso"
	twoIdle(t, f, fw, alphaPeer(), g)
	f.idleSince(fw, 7, time.Minute)
	f.rl.sweepIdle()
	if r := pauseRow(t, f, "s-gamma"); r == nil || r.State != pigtail.PausePaused {
		t.Fatalf("row = %+v; log:\n%s", r, lg)
	}
	peersAs := func(c caller) []pigtail.PeerSession {
		rec := doAs(t, f.r, c, "GET", "/peers?all=1", nil)
		var ps []pigtail.PeerSession
		if err := json.Unmarshal(rec.Body.Bytes(), &ps); err != nil {
			t.Fatalf("GET /peers: %s", rec.Body.String())
		}
		return ps
	}
	seen := false
	for _, p := range peersAs(perso) {
		seen = seen || p.SessionID == "s-gamma"
		if (p.PausedAt != nil) != (p.SessionID == "s-gamma") {
			t.Fatalf("%s paused_at = %v", p.SessionID, p.PausedAt)
		}
	}
	if !seen {
		t.Fatal("s-gamma not listed for its own scope")
	}
	for _, p := range peersAs(work) {
		if p.SessionID == "s-gamma" {
			t.Fatal("s-gamma listed for another scope")
		}
	}
	// A stopping row shows no paused_at.
	f.idleSince(fw, 7, 3*time.Hour)
	fw.stayAlive = true
	f.rl.sweepIdle()
	if r := pauseRow(t, f, "s-alpha"); r == nil || r.State != pigtail.PauseStopping {
		t.Fatalf("alpha row = %+v", r)
	}
	for _, p := range peersAs(perso) {
		if p.SessionID == "s-alpha" && p.PausedAt != nil {
			t.Fatal("paused_at on a stopping row")
		}
	}
}

// Final review I1: the sidebar loop's proof that a paused tab was closed
// ends the pause — row gone, paused:false published in the session's scope.
func TestEndPauseGone(t *testing.T) {
	t.Parallel()
	f, _, lg := pausedGamma(t, false)
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionPauseChanged))
	if err := f.rl.EndPauseGone("s-gamma", "WS-G"); err != nil {
		t.Fatal(err)
	}
	if r := pauseRow(t, f, "s-gamma"); r != nil {
		t.Fatalf("row kept: %+v", r)
	}
	n := 0
	for _, e := range drainEvents(sub) {
		if e.Type == pigtail.EventSessionPauseChanged && e.Data["paused"] == false {
			n++
		}
	}
	if n != 1 || lg.count("pause ended: workspace WS-G closed") != 1 {
		t.Fatalf("events %d; log:\n%s", n, lg)
	}
	if err := f.rl.EndPauseGone("s-gamma", "WS-G"); err != nil { // already gone: nothing
		t.Fatal(err)
	}
}

package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The marker is per pid and journal-backed: a new Relay on the same store
// still sees it, another pid of the same session does not.
func TestWrapMarkerPerPid(t *testing.T) {
	t.Parallel()
	f, _, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	p10 := alphaPeer()
	p10.PID = 10
	p11 := alphaPeer()
	p11.PID = 11
	now := f.clock()
	if created, err := f.rl.wrapJournal(p10, "sent", "typed "+testDoneCommand, now); err != nil || !created {
		t.Fatalf("wrapJournal = %v, %v", created, err)
	}
	if created, _ := f.rl.wrapJournal(p10, "sent", "again", now); created {
		t.Fatal("second write created a row")
	}
	sent, failed, at, err := f.rl.wrapMarked(p10)
	if err != nil || !sent || failed || !at.Equal(now) {
		t.Fatalf("pid 10: sent=%v failed=%v at=%v err=%v", sent, failed, at, err)
	}
	if sent, failed, _, _ := f.rl.wrapMarked(p11); sent || failed {
		t.Fatal("marker of pid 10 seen for pid 11")
	}
	rl2 := NewRelay(f.rl.svc, f.rl.cfg)
	if sent, _, _, _ := rl2.wrapMarked(p10); !sent {
		t.Fatal("marker lost on a new Relay")
	}
	_, _ = f.rl.wrapJournal(p10, "failed", "send failed", now)
	if _, failed, _, _ := f.rl.wrapMarked(p10); !failed {
		t.Fatal("failed marker not read")
	}
}

// wrapFixture: the idle pause fixture (s-alpha pid 7 idle 3 h, threshold 1 h)
// with a declared done wait and NO register: an open waiter is a watcher, and
// the wrap waits for a watcher (TestWrapIdleWatcherOpen). No clock fallback
// either: an open waiter stays watched whatever the clock (relay.go watched).
func wrapFixture(t *testing.T) (*relayFixture, *fakeWake, *wakeLog) {
	t.Helper()
	f, fw, lg := idlePauseFixture(t)
	declareWait(t, f, "s-alpha", "done")
	return f, fw, lg
}

func wrapRows(t *testing.T, f *relayFixture, state string) int {
	t.Helper()
	return len(pauseJournalRows(t, f, wrapJournalKindPrefix+state))
}

// assertNoWrap: nothing typed, no marker, no signal.
func assertNoWrap(t *testing.T, f *relayFixture, fw *fakeWake, lg *wakeLog) {
	t.Helper()
	if len(fw.wraps) != 0 || wrapRows(t, f, "sent") != 0 || wrapRows(t, f, "failed") != 0 || len(fw.signals) != 0 {
		t.Fatalf("wraps %v sent %d failed %d signals %v; log:\n%s", fw.wraps, wrapRows(t, f, "sent"), wrapRows(t, f, "failed"), fw.signals, lg)
	}
}

func sweeps(f *relayFixture, n int) {
	for i := 0; i < n; i++ {
		f.advance(time.Minute)
		f.rl.sweepIdle()
	}
}

// Criterion 1: the done command exactly once, the marker written before the send, never
// signalled; a new Relay on the same store types nothing more.
func TestWrapIdleTypesOnce(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.onSend = func(line string) {
		if line == testDoneCommand && wrapRows(t, f, "sent") != 1 {
			t.Error("marker not written before the send")
		}
	}
	f.rl.sweepIdle()
	if len(fw.wraps) != 1 || fw.wraps[0] != surf {
		t.Fatalf("wraps = %v; log:\n%s", fw.wraps, lg)
	}
	sweeps(f, 3)
	rl2 := NewRelay(f.rl.svc, f.rl.cfg)
	rl2.startedAt = f.rl.startedAt
	rl2.sweepIdle()
	if len(fw.wraps) != 1 || len(fw.signals) != 0 || wrapRows(t, f, "sent") != 1 || lg.count("typed "+testDoneCommand) != 1 {
		t.Fatalf("wraps %v signals %v; log:\n%s", fw.wraps, fw.signals, lg)
	}
}

// The old pause scenario (idle Done, past the threshold) ends with the done command and
// neither SIGTERM nor Standby, however many sweeps follow.
func TestWrapIdleNeverPauses(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	sweeps(f, 10)
	if len(fw.wraps) != 1 || len(fw.signals) != 0 {
		t.Fatalf("wraps %v signals %v; log:\n%s", fw.wraps, fw.signals, lg)
	}
	if r := pauseRow(t, f, "s-alpha"); r != nil {
		t.Fatalf("pause row %+v", r)
	}
	if in, _ := f.rl.inStandby("s-alpha"); in {
		t.Fatal("put in Standby")
	}
}

// D3: a Standby done session is neither typed in nor paused.
func TestWrapIdleStandbyDoneUntouched(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	if err := f.st.SetStandby("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.st.MarkStandbyCompactFailed("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	sweeps(f, 3)
	assertNoWrap(t, f, fw, lg)
	if len(fw.sent) != 0 || len(fw.compacts) != 0 {
		t.Fatalf("typed %v %v", fw.sent, fw.compacts)
	}
}

// The wrap waits for the Stop watcher; once it is gone (grace elapsed) the
// wrap goes through, once.
func TestWrapIdleWatcherOpen(t *testing.T) {
	t.Parallel()
	f, fw, lg := idlePauseFixture(t)
	w := f.rl.register("s-alpha")
	declareWait(t, f, "s-alpha", "done")
	sweeps(f, 2)
	assertNoWrap(t, f, fw, lg)
	f.rl.unregister("s-alpha", w)
	f.advance(watchGrace + time.Second)
	f.rl.sweepIdle()
	sweeps(f, 2)
	if len(fw.wraps) != 1 || len(fw.signals) != 0 {
		t.Fatalf("wraps %v signals %v; log:\n%s", fw.wraps, fw.signals, lg)
	}
}

// The focus is re-read right before the send: it flipped onto the pane after
// paneGuard's read.
func TestWrapIdleFocusFlip(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.onFocus = func() {
		if fw.focusCalls == 2 {
			fw.focused = surf
		}
	}
	f.rl.sweepIdle()
	assertNoWrap(t, f, fw, lg)
	if fw.focusCalls != 2 {
		t.Fatalf("focus calls = %d, want 2 (guard + fresh re-read)", fw.focusCalls)
	}
}

func TestWrapIdleJournalWriteFails(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.rl.addJournal = func(*pigtail.JournalEntry) (bool, error) { return false, errors.New("disk") }
	f.rl.sweepIdle()
	assertNoWrap(t, f, fw, lg)
}

func TestWrapIdleJournalRowExists(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.rl.addJournal = func(*pigtail.JournalEntry) (bool, error) { return false, nil }
	f.rl.sweepIdle()
	assertNoWrap(t, f, fw, lg)
}

// SendLine fails after the marker: a failed row at once, no retype, no pause.
func TestWrapIdleSendFails(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.sendErr = errors.New("cmux gone")
	sweeps(f, 4)
	if len(fw.wraps) != 1 || wrapRows(t, f, "sent") != 1 || wrapRows(t, f, "failed") != 1 || len(fw.signals) != 0 {
		t.Fatalf("wraps %v sent %d failed %d signals %v; log:\n%s", fw.wraps, wrapRows(t, f, "sent"), wrapRows(t, f, "failed"), fw.signals, lg)
	}
	if rows := pauseJournalRows(t, f, "idle-wrap-failed"); rows[0].Summary != "send failed" {
		t.Fatalf("summary = %q", rows[0].Summary)
	}
}

// The wake accepts shell; the wrap, like the pause, needs idle at the re-read.
func TestWrapIdleNeedsIdleOnReread(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.onScreen = func() {
		n := fw.natives[7]
		n.Status = "shell"
		fw.natives[7] = n
	}
	f.rl.sweepIdle()
	assertNoWrap(t, f, fw, lg)
}

// Still idle but the status time moved since the candidate was read.
func TestWrapIdleNativeMovedOnReread(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.onScreen = func() {
		n := fw.natives[7]
		n.StatusUpdatedAt++
		fw.natives[7] = n
	}
	f.rl.sweepIdle()
	assertNoWrap(t, f, fw, lg)
}

// An identify error at the fresh focus read stops the sweep: the second idle
// Done session is not typed either.
func TestWrapIdleIdentifyErrorStopsSweep(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	twoIdle(t, f, fw)
	declareWait(t, f, "s-gamma", "done")
	fw.onFocus = func() {
		if fw.focusCalls == 2 {
			fw.focusErr = errors.New("identify")
		}
	}
	f.rl.sweepIdle()
	assertNoWrap(t, f, fw, lg)
	if lg.count("wrap: s-gamma: cmux identify") != 1 {
		t.Fatalf("identify not logged for the first candidate:\n%s", lg)
	}
}

// A refusal is noted once across sweeps with nothing pending.
func TestWrapIdleNoteMemory(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.focused = surf
	sweeps(f, 4)
	assertNoWrap(t, f, fw, lg)
	if n := lg.count("wrap: s-alpha held: pane has keyboard focus"); n != 1 {
		t.Fatalf("noted %d times, want 1; log:\n%s", n, lg)
	}
}

func TestWrapNoterStandbyAndMessages(t *testing.T) {
	t.Parallel()
	t.Run("standby unreadable", func(t *testing.T) {
		f, _, _ := wrapFixture(t)
		f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) { return nil, errors.New("disk") }
		if held, _ := (wrapNoter{f.rl}).watcherHolds("s-alpha"); !held {
			t.Fatal("not held")
		}
	})
	t.Run("standby readable", func(t *testing.T) {
		f, _, _ := wrapFixture(t)
		if err := f.st.SetStandby("s-alpha", f.now); err != nil {
			t.Fatal(err)
		}
		if held, why := (wrapNoter{f.rl}).watcherHolds("s-alpha"); !held || why != "standby (D16)" {
			t.Fatalf("held %v %q", held, why)
		}
	})
	t.Run("messages pending", func(t *testing.T) {
		f, fw, _ := wrapFixture(t)
		f.sendTo(t, fw, "s-alpha")
		if held, why := (wrapNoter{f.rl}).watcherHolds("s-alpha"); !held || why != "messages pending" {
			t.Fatalf("held %v %q", held, why)
		}
	})
}

// One wrap per sweep, oldest idle first.
func TestWrapIdleOnePerSweep(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	twoIdle(t, f, fw)
	declareWait(t, f, "s-gamma", "done")
	f.rl.sweepIdle()
	if len(fw.wraps) != 1 || fw.wraps[0] != surf2 {
		t.Fatalf("first sweep wraps = %v, want gamma (surf2) only; log:\n%s", fw.wraps, lg)
	}
	f.rl.sweepIdle()
	if len(fw.wraps) != 2 || fw.wraps[1] != surf {
		t.Fatalf("second sweep wraps = %v; log:\n%s", fw.wraps, lg)
	}
}

// Criterion 2: the done command stops on a question: the tab stays, nothing retyped,
// no failure.
func TestWrapIdleQuestion(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.onSend = func(line string) {
		n := fw.natives[7]
		n.Status = "waiting"
		fw.natives[7] = n
		declareWait(t, f, "s-alpha", "question")
	}
	sweeps(f, 10)
	if len(fw.wraps) != 1 || wrapRows(t, f, "failed") != 0 || len(fw.signals) != 0 {
		t.Fatalf("wraps %v failed %d signals %v; log:\n%s", fw.wraps, wrapRows(t, f, "failed"), fw.signals, lg)
	}
}

// The follow-up journals a done command that left the session open; it types
// nothing and signals nothing.
func TestFollowWrapsJournalsAStayOpen(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.rl.sweepIdle()
	if len(fw.wraps) != 1 {
		t.Fatalf("wraps = %v", fw.wraps)
	}
	f.advance(wrapProofWait - time.Minute)
	f.rl.sweepIdle()
	if wrapRows(t, f, "failed") != 0 {
		t.Fatal("failure journalled before the proof wait")
	}
	f.advance(2 * time.Minute)
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	rows := pauseJournalRows(t, f, "idle-wrap-failed")
	if len(rows) != 1 || rows[0].ID != wrapJournalID("s-alpha", 7, "failed") {
		t.Fatalf("rows = %+v; log:\n%s", rows, lg)
	}
	if len(fw.wraps) != 1 || len(fw.signals) != 0 || lg.count("stayed open after "+testDoneCommand) != 1 {
		t.Fatalf("wraps %v signals %v; log:\n%s", fw.wraps, fw.signals, lg)
	}
	// Still held from the pause, whatever the later sweeps.
	sweeps(f, 3)
	if len(fw.signals) != 0 || pauseRow(t, f, "s-alpha") != nil {
		t.Fatal("paused after a failed wrap")
	}
}

func TestFollowWrapsQuestionIsNotAFailure(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.onSend = func(string) {
		n := fw.natives[7]
		n.Status = "waiting"
		fw.natives[7] = n
		declareWait(t, f, "s-alpha", "question")
	}
	f.rl.sweepIdle()
	f.advance(wrapProofWait + time.Minute)
	f.rl.sweepIdle()
	if wrapRows(t, f, "failed") != 0 {
		t.Fatalf("failure journalled for a question; log:\n%s", lg)
	}
}

func TestFollowWrapsPidGoneIsSuccess(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.rl.sweepIdle()
	fw.dead[7] = true
	f.advance(wrapProofWait + time.Minute)
	f.rl.sweepIdle()
	if wrapRows(t, f, "failed") != 0 {
		t.Fatalf("failure journalled for a gone pid; log:\n%s", lg)
	}
}

// A done command that ends on purpose with a declared wrapped wait (the tab
// left open by it) is a normal end, not a failure; the pid still stays out of
// the pause.
func TestFollowWrapsWrappedIsNotAFailure(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	fw.onSend = func(string) { declareWait(t, f, "s-alpha", "wrapped") }
	f.rl.sweepIdle()
	f.advance(wrapProofWait + time.Minute)
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	if wrapRows(t, f, "failed") != 0 {
		t.Fatalf("failure journalled for a wrapped wait; log:\n%s", lg)
	}
	sweeps(f, 3)
	if len(fw.signals) != 0 || pauseRow(t, f, "s-alpha") != nil || len(fw.wraps) != 1 {
		t.Fatalf("wraps %v signals %v; log:\n%s", fw.wraps, fw.signals, lg)
	}
}

// A journal write that keeps failing is logged once, not every sweep.
func TestFollowWrapsFailureNotRecordedLoggedOnce(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.rl.sweepIdle()
	if len(fw.wraps) != 1 {
		t.Fatalf("wraps = %v", fw.wraps)
	}
	f.rl.addJournal = func(*pigtail.JournalEntry) (bool, error) { return false, errors.New("disk") }
	f.advance(wrapProofWait + time.Minute)
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	if n := lg.count("failure not recorded"); n != 1 {
		t.Fatalf("logged %d times, want 1; log:\n%s", n, lg)
	}
}

// typeGuarded maps beforeSend's errTypeStop to typeStop and any other error
// to typeRefused (pinned directly: the sweep's own focus error would stop the
// next guard anyway).
func TestTypeGuardedBeforeSendMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want typeOutcome
	}{
		{"stop", errTypeStop, typeStop},
		{"ordinary", errors.New("nope"), typeRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fw, _ := newWakeFixture(t)
			f.wakePeers(t, alphaPeer())
			fw.resetCounts()
			p, _ := f.st.GetPeer("s-alpha")
			c, err := f.rl.candidate(*p)
			if err != nil {
				t.Fatal(err)
			}
			sw := &wakeSweep{ls: newListing(fw.deps().CmuxSessions), typed: map[string]bool{}}
			_, out := f.rl.typeGuarded("s-alpha", relay.WakeLine, c, *p, f.now, sw, wakeNoter{f.rl}, nil, nil, nil, func() error { return tc.err })
			if out != tc.want || len(fw.sent) != 0 || sw.typed["s-alpha"] {
				t.Fatalf("outcome %v (want %v), sent %v, typed %v", out, tc.want, fw.sent, sw.typed)
			}
		})
	}
}

// No done command set (the default): an idle Done session past the threshold
// gets nothing typed, no marker, no signal — and stays out of the pause.
func TestWrapIdleOffWithoutDoneCommand(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.rl.cfg.DoneCommand = ""
	sweeps(f, 5)
	// Nothing is typed; the finished session is reclaimed by the idle pause instead.
	if len(fw.wraps) != 0 || wrapRows(t, f, "sent") != 0 || wrapRows(t, f, "failed") != 0 {
		t.Fatalf("wraps %v sent %d failed %d; log:\n%s", fw.wraps, wrapRows(t, f, "sent"), wrapRows(t, f, "failed"), lg)
	}
	if r := pauseRow(t, f, "s-alpha"); r == nil {
		t.Fatalf("a done session with no done command must be paused like any idle one; log:\n%s", lg)
	}
}

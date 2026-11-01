package handlers

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Resume at a limit's reset (relay_retry_limit.go).

// limitStop records a StopFailure rate_limit now whose reset is in 10 minutes;
// it returns the reset.
func (f *relayFixture) limitStop(t *testing.T, fw *fakeWake) time.Time {
	t.Helper()
	at := f.clock()
	reset := at.Add(10 * time.Minute)
	if err := f.st.SetSessionWait(retrySid, pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: at, Until: &reset}); err != nil {
		t.Fatal(err)
	}
	if fw.apiErrors == nil {
		fw.apiErrors = map[string]claudesession.APIError{}
	}
	fw.apiErrors[retrySid] = claudesession.APIError{Error: "rate_limit", Status: 429, At: at, ResetsAt: reset}
	return reset
}

func (f *relayFixture) setClock(at time.Time) {
	f.mu.Lock()
	f.now = at
	f.mu.Unlock()
}

func limitRows(t *testing.T, f *relayFixture, kind string) []pigtail.JournalEntry {
	return f.retryRows(t, kind)
}

func TestLimitResumeTypesOnceAfterReset(t *testing.T) {
	f, fw, lg := retryFixture(t)
	reset := f.limitStop(t, fw)
	f.setClock(reset.Add(time.Minute))
	f.rl.sweepIdle()
	if len(fw.continues) != 0 {
		t.Fatalf("typed before the grace: %v", fw.continues)
	}
	f.setClock(reset.Add(2 * time.Minute))
	f.rl.sweepIdle()
	if len(fw.continues) != 1 || fw.continues[0] != surf {
		t.Fatalf("continues = %v; log:\n%s", fw.continues, lg)
	}
	f.setClock(reset.Add(3 * time.Minute))
	f.rl.sweepIdle()
	if len(fw.continues) != 1 {
		t.Fatalf("typed twice for one reset: %v", fw.continues)
	}
	rows := limitRows(t, f, limitKindSent)
	if len(rows) != 1 {
		t.Fatalf("sent rows = %+v", rows)
	}
	for _, want := range []string{"s-alpha", "limit", "resume at reset", "typed"} {
		if !strings.Contains(rows[0].Summary, want) {
			t.Errorf("summary %q lacks %q", rows[0].Summary, want)
		}
	}
}

func TestLimitResumeSkipsNativeResume(t *testing.T) {
	f, fw, _ := retryFixture(t)
	reset := f.limitStop(t, fw)
	n := fw.natives[7]
	n.StatusUpdatedAt = reset.Add(30 * time.Second).UnixMilli()
	fw.natives[7] = n
	f.setClock(reset.Add(2 * time.Minute))
	f.rl.sweepIdle()
	f.setClock(reset.Add(3 * time.Minute))
	f.rl.sweepIdle()
	rows := limitRows(t, f, limitKindNative)
	if len(fw.continues) != 0 || len(rows) != 1 || !strings.Contains(rows[0].Summary, "native resume") {
		t.Fatalf("continues %v native rows %+v", fw.continues, rows)
	}
}

// The native status time is in milliseconds (native.go): the reset in seconds
// would read as a resume long gone.
func TestLimitResumeNativeStatusInMillis(t *testing.T) {
	f, fw, _ := retryFixture(t)
	reset := f.limitStop(t, fw)
	n := fw.natives[7]
	n.StatusUpdatedAt = reset.UnixMilli()
	fw.natives[7] = n
	f.setClock(reset.Add(2 * time.Minute))
	f.rl.sweepIdle()
	if len(fw.continues) != 0 || len(limitRows(t, f, limitKindNative)) != 1 {
		t.Fatalf("ms status at the reset must be a native resume: continues %v", fw.continues)
	}
	f2, fw2, _ := retryFixture(t)
	reset2 := f2.limitStop(t, fw2)
	n = fw2.natives[7]
	n.StatusUpdatedAt = reset2.Unix() // the same value read as seconds: far in the past
	fw2.natives[7] = n
	f2.setClock(reset2.Add(2 * time.Minute))
	f2.rl.sweepIdle()
	if len(fw2.continues) != 1 {
		t.Fatalf("a seconds value is not a resume: continues %v", fw2.continues)
	}
}

func TestLimitResumeRefusesAutoContinueScreen(t *testing.T) {
	f, fw, _ := retryFixture(t)
	reset := f.limitStop(t, fw)
	fw.screen = strings.Replace(screenFixture(t, "screen-idle-empty.txt"), "──────────", "⏺ Usage limit reached · continuing automatically at 3pm\n\n──────────", 1)
	f.setClock(reset.Add(2 * time.Minute))
	f.rl.sweepIdle()
	f.setClock(reset.Add(3 * time.Minute))
	f.rl.sweepIdle()
	rows := limitRows(t, f, limitKindHeld)
	if len(fw.continues) != 0 || len(rows) != 1 || !strings.Contains(rows[0].Summary, "held: usage limit wait") {
		t.Fatalf("continues %v held rows %+v", fw.continues, rows)
	}
}

func TestLimitResumeBeforeResetDoesNothing(t *testing.T) {
	f, fw, _ := retryFixture(t)
	reset := f.limitStop(t, fw)
	fw.resetCounts()
	f.setClock(reset.Add(-time.Minute))
	f.rl.sweepIdle()
	if fw.cmuxCalls != 0 || len(fw.continues) != 0 || len(limitRows(t, f, limitKindHeld))+len(limitRows(t, f, limitKindSent)) != 0 {
		t.Fatalf("cmux calls %d continues %v", fw.cmuxCalls, fw.continues)
	}
}

// The full stop screen of a limit, after the reset: the stop lines do not hold.
func TestLimitResumeTypesAfterFullStopScreen(t *testing.T) {
	f, fw, _ := retryFixture(t)
	reset := f.limitStop(t, fw)
	stop := "⏺ Usage limit reached · resets 3pm\n  ⎿  You've hit your session limit · resets 11:40pm\n     /usage-credits to finish what you’re working on.\n\n"
	fw.screen = strings.Replace(screenFixture(t, "screen-idle-empty.txt"), "──────────", stop+"──────────", 1)
	f.setClock(reset.Add(2 * time.Minute))
	f.rl.sweepIdle()
	f.setClock(reset.Add(3 * time.Minute))
	f.rl.sweepIdle()
	if len(fw.continues) != 1 {
		t.Fatalf("continues = %v", fw.continues)
	}
}

// B2/B4: the idle pause waits for the resume's decision, boundedly.
func TestPauseSweepHoldsLimitUntilResume(t *testing.T) {
	setup := func(t *testing.T) (*relayFixture, *fakeWake, time.Time) {
		f, fw, _ := idlePauseFixture(t)
		fw.apiErrors = map[string]claudesession.APIError{}
		fw.prompts = map[string][]claudesession.Prompt{}
		reset := f.clock().Add(-10 * time.Second)
		since := f.clock().Add(-time.Hour)
		if err := f.st.SetSessionWait(retrySid, pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: since, Until: &reset}); err != nil {
			t.Fatal(err)
		}
		fw.apiErrors[retrySid] = claudesession.APIError{Error: "rate_limit", At: since, ResetsAt: reset}
		return f, fw, reset
	}
	t.Run("held without a marker, then freed by each marker", func(t *testing.T) {
		for _, kind := range []string{limitKindSent, limitKindNative, limitKindHeld} {
			f, fw, reset := setup(t)
			f.rl.cfg.Wake.LastAPIError = nil // the resume is off: only the pause is under test
			p := alphaPeer()
			f.rl.sweepIdle()
			f.advance(2 * time.Minute)
			f.rl.sweepIdle()
			if len(fw.signals) != 0 {
				t.Fatalf("%s: paused with the resume pending: %v", kind, fw.signals)
			}
			id := limitIDPrefix(retrySid, 7, reset) + strings.TrimPrefix(kind, "limit-resume-")
			if _, err := f.rl.retryJournal(p, kind, id, "s-alpha: limit · resume at reset · "+kind, f.clock()); err != nil {
				t.Fatal(err)
			}
			f.rl.sweepIdle()
			if !reflect.DeepEqual(fw.signals, []int{7}) {
				t.Fatalf("%s: signals = %v, want the pause back", kind, fw.signals)
			}
		}
	})
	t.Run("the hold is bounded (b)", func(t *testing.T) {
		f, fw, reset := setup(t)
		f.rl.cfg.Wake.LastAPIError = nil
		f.setClock(reset.Add(relay.LimitResumeGrace + relay.LimitResumeHoldMax - time.Second))
		f.rl.sweepIdle()
		if len(fw.signals) != 0 {
			t.Fatalf("paused 1 s before the bound: %v", fw.signals)
		}
		f.setClock(reset.Add(relay.LimitResumeGrace + relay.LimitResumeHoldMax))
		f.rl.sweepIdle()
		if !reflect.DeepEqual(fw.signals, []int{7}) {
			t.Fatalf("signals = %v, want the pause at the bound", fw.signals)
		}
	})
	t.Run("a Standby session is never held (c)", func(t *testing.T) {
		f, fw, _ := setup(t)
		f.rl.cfg.Wake.LastAPIError = nil
		if err := f.st.SetStandby(retrySid, f.now); err != nil {
			t.Fatal(err)
		}
		compactedAlready(t, f, retrySid)
		f.rl.sweepIdle()
		if !reflect.DeepEqual(fw.signals, []int{7}) {
			t.Fatalf("signals = %v, want the Standby pause", fw.signals)
		}
	})
	t.Run("a refused resume writes held and frees the pause (a)", func(t *testing.T) {
		for name, edit := range map[string]func(*fakeWake, time.Time){
			"transcript diverges": func(fw *fakeWake, reset time.Time) {
				e := fw.apiErrors[retrySid]
				e.ResetsAt = reset.Add(time.Hour)
				fw.apiErrors[retrySid] = e
			},
			"transcript unreadable": func(fw *fakeWake, _ time.Time) { delete(fw.apiErrors, retrySid) },
		} {
			f, fw, reset := setup(t)
			edit(fw, reset)
			f.advance(2 * time.Minute)
			f.rl.sweepIdle()
			f.rl.sweepIdle()
			if len(limitRows(t, f, limitKindHeld)) != 1 || len(fw.continues) != 0 {
				t.Fatalf("%s: held rows %+v continues %v", name, limitRows(t, f, limitKindHeld), fw.continues)
			}
			if !reflect.DeepEqual(fw.signals, []int{7}) {
				t.Fatalf("%s: signals = %v, want the pause back", name, fw.signals)
			}
		}
	})
	t.Run("no cmux surface writes held and frees the pause (a)", func(t *testing.T) {
		// The peer is registered without a surface from the start (a later
		// registration cannot blank one: the upsert keeps the old value).
		f, fw, _ := newWakeFixture(t)
		f.rl.cfg.IdlePause = time.Hour
		f.rl.cfg.RelaunchDir = t.TempDir()
		fw.environ = map[int]string{7: pauseEnv(f.rl.cfg.RelaunchDir)}
		noSurface := alphaPeer()
		noSurface.CmuxSurfaceID = ""
		f.wakePeers(t, noSurface)
		f.idleSince(fw, 7, 3*time.Hour)
		fw.apiErrors = map[string]claudesession.APIError{}
		fw.prompts = map[string][]claudesession.Prompt{}
		reset := f.clock().Add(-10 * time.Second)
		since := f.clock().Add(-time.Hour)
		if err := f.st.SetSessionWait(retrySid, pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: since, Until: &reset}); err != nil {
			t.Fatal(err)
		}
		fw.apiErrors[retrySid] = claudesession.APIError{Error: "rate_limit", At: since, ResetsAt: reset}
		fw.resetCounts()
		f.advance(2 * time.Minute)
		f.rl.sweepIdle()
		f.rl.sweepIdle()
		rows := limitRows(t, f, limitKindHeld)
		if len(rows) != 1 || !strings.Contains(rows[0].Summary, "no cmux surface") || len(fw.continues) != 0 {
			t.Fatalf("held rows %+v continues %v", rows, fw.continues)
		}
		// The held row frees the pause's hold. The pause itself still refuses a
		// session with no surface (paneGuard), so it is the hold that is proven.
		c, err := f.rl.candidate(noSurface)
		if err != nil {
			t.Fatal(err)
		}
		if f.rl.limitResumePending(noSurface, c) {
			t.Fatal("the resume is still pending after the held row")
		}
	})
	t.Run("RetryAllowed refuses: held written (a)", func(t *testing.T) {
		f, fw, _ := setup(t)
		declareWait(t, f, retrySid, "review")
		f.advance(2 * time.Minute)
		f.rl.sweepIdle()
		rows := limitRows(t, f, limitKindHeld)
		if len(rows) != 1 || !strings.Contains(rows[0].Summary, "wait review") || len(fw.continues) != 0 {
			t.Fatalf("held rows %+v continues %v", rows, fw.continues)
		}
	})
}

// B4(a): a per-session cmux read-screen failure leaves a held row, never a
// silent exit.
func TestLimitResumeReadScreenErrorWritesHeld(t *testing.T) {
	f, fw, lg := retryFixture(t)
	reset := f.limitStop(t, fw)
	fw.screenErr = map[string]error{surf: errors.New("cmux read-screen: signal: killed")}
	f.setClock(reset.Add(2 * time.Minute))
	f.rl.sweepIdle()
	rows := limitRows(t, f, limitKindHeld)
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "read-screen") || len(fw.continues) != 0 {
		t.Fatalf("held rows %+v continues %v; log:\n%s", rows, fw.continues, lg)
	}
}

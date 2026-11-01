package daemonwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type rig struct {
	t      *testing.T
	now    time.Time
	st     State
	res    Result
	detail string
	probes int
	kicks  int
	kickFn func(ctx context.Context) error
	posted []Event
	postFn func([]Event) error
	maint  bool
	claim  bool
	claimE error
}

func newRig(t *testing.T) *rig {
	return &rig{t: t, now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), res: Healthy}
}

func (r *rig) deps() Deps {
	return Deps{
		Probe: func(context.Context) (Result, string) { r.probes++; return r.res, r.detail },
		Kick: func(ctx context.Context) error {
			r.kicks++
			if r.kickFn != nil {
				return r.kickFn(ctx)
			}
			return nil
		},
		KickTimeout: 50 * time.Millisecond,
		Post: func(es []Event) error {
			if r.postFn != nil {
				if err := r.postFn(es); err != nil {
					return err
				}
			}
			r.posted = append(r.posted, es...)
			return nil
		},
		Maintenance:  func() (bool, error) { return r.maint, nil },
		RuntimeClaim: func() (bool, error) { return r.claim, r.claimE },
		Load:         func() (State, error) { return r.st, nil },
		Save:         func(s State) error { r.st = s; return nil },
		Now:          func() time.Time { return r.now },
	}
}

func (r *rig) run(n int, step time.Duration) {
	r.t.Helper()
	for i := 0; i < n; i++ {
		if err := Run(context.Background(), r.deps()); err != nil {
			r.t.Fatal(err)
		}
		r.now = r.now.Add(step)
	}
}

func TestWatchKicksAtThirdWedged(t *testing.T) {
	r := newRig(t)
	r.res, r.detail = Wedged, "timeout"
	r.run(2, time.Minute)
	if r.kicks != 0 {
		t.Fatalf("kicks = %d after two wedged", r.kicks)
	}
	r.run(1, time.Minute)
	if r.kicks != 1 || r.st.Failures != 0 || len(r.st.Kicks) != 1 {
		t.Fatalf("kicks=%d failures=%d state=%+v", r.kicks, r.st.Failures, r.st)
	}
}

func TestWatchDownResetsAndNeverKicks(t *testing.T) {
	r := newRig(t)
	r.res = Wedged
	r.run(2, time.Minute)
	r.res = Down
	r.run(5, time.Minute)
	r.res = Wedged
	r.run(2, time.Minute)
	if r.kicks != 0 || r.st.Failures != 2 {
		t.Fatalf("kicks=%d failures=%d", r.kicks, r.st.Failures)
	}
}

func TestWatchStartingBreaksTheStreak(t *testing.T) {
	r := newRig(t)
	r.res = Wedged
	r.run(2, time.Minute)
	r.res = Starting
	r.run(5, time.Minute)
	if r.kicks != 0 || r.st.Failures != 0 {
		t.Fatalf("kicks=%d failures=%d", r.kicks, r.st.Failures)
	}
}

func TestWatchGivesUpAfterThreeKicksInAnHour(t *testing.T) {
	r := newRig(t)
	r.res, r.detail = Wedged, "timeout"
	r.run(9, time.Minute) // three kicks
	if r.kicks != 3 {
		t.Fatalf("kicks = %d, want 3", r.kicks)
	}
	r.run(3, time.Minute) // fourth trigger
	if r.kicks != 3 || r.st.GaveUp == nil {
		t.Fatalf("kicks=%d gaveUp=%v", r.kicks, r.st.GaveUp)
	}
	r.run(3, time.Minute) // fifth
	n := 0
	for _, e := range r.st.Unreported {
		if e.Kind == "gave-up" {
			n++
		}
	}
	if n != 1 || r.kicks != 3 {
		t.Fatalf("gave-up events = %d, kicks = %d", n, r.kicks)
	}
}

func TestWatchFailedKickCountsTowardCap(t *testing.T) {
	r := newRig(t)
	r.res, r.detail = Wedged, "503"
	r.kickFn = func(context.Context) error { return errors.New("boom") }
	r.run(9, time.Minute)
	if len(r.st.Kicks) != 3 {
		t.Fatalf("kicks recorded = %d", len(r.st.Kicks))
	}
	for _, e := range r.st.Unreported {
		if e.Kind == "kick" && e.Detail != "503 (failed: boom)" {
			t.Fatalf("detail = %q", e.Detail)
		}
	}
	r.run(3, time.Minute)
	if r.kicks != 3 || r.st.GaveUp == nil {
		t.Fatalf("kicks=%d gaveUp=%v", r.kicks, r.st.GaveUp)
	}
}

func TestWatchKickBoundedByTimeout(t *testing.T) {
	r := newRig(t)
	r.res = Wedged
	r.kickFn = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	r.run(2, time.Minute)
	start := time.Now()
	r.run(1, time.Minute)
	if time.Since(start) > time.Second {
		t.Fatal("Run did not return within 1 s")
	}
	if len(r.st.Unreported) != 1 || r.st.Unreported[0].Detail == "" ||
		!contains(r.st.Unreported[0].Detail, "kick timed out (may have taken effect)") {
		t.Fatalf("events = %+v", r.st.Unreported)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestWatchKicksAgainAfterAnHour(t *testing.T) {
	r := newRig(t)
	r.res = Wedged
	r.run(9, time.Minute)
	r.now = r.now.Add(2 * time.Hour)
	r.run(3, time.Minute)
	if r.kicks != 4 || r.st.GaveUp != nil {
		t.Fatalf("kicks=%d gaveUp=%v", r.kicks, r.st.GaveUp)
	}
}

func TestWatchPostsUnreportedWhenHealthy(t *testing.T) {
	r := newRig(t)
	r.res = Wedged
	r.run(3, time.Minute)
	if len(r.st.Unreported) != 1 {
		t.Fatalf("unreported = %+v", r.st.Unreported)
	}
	r.res = Healthy
	r.postFn = func([]Event) error { return errors.New("freeze") }
	r.run(1, time.Minute)
	if len(r.st.Unreported) != 1 || len(r.posted) != 0 {
		t.Fatalf("kept=%d posted=%d", len(r.st.Unreported), len(r.posted))
	}
	r.postFn = nil
	r.run(1, time.Minute)
	if len(r.st.Unreported) != 0 || len(r.posted) != 1 {
		t.Fatalf("kept=%d posted=%d", len(r.st.Unreported), len(r.posted))
	}
}

func TestWatchSkipsDuringMaintenanceButStampsLastRun(t *testing.T) {
	r := newRig(t)
	r.res = Wedged
	r.maint = true
	r.run(4, time.Minute)
	if r.probes != 0 || r.kicks != 0 || r.st.LastRun.IsZero() || r.st.LastSkip != "maintenance on" {
		t.Fatalf("probes=%d kicks=%d state=%+v", r.probes, r.kicks, r.st)
	}
}

func TestWatchSkipsWhileRuntimeClaimHeld(t *testing.T) {
	r := newRig(t)
	r.res = Wedged
	r.claim = true
	r.run(3, time.Minute)
	if r.probes != 0 || r.kicks != 0 || r.st.LastRun.IsZero() || !contains(r.st.LastSkip, "runtime claim") {
		t.Fatalf("probes=%d kicks=%d state=%+v", r.probes, r.kicks, r.st)
	}
	r.claim, r.claimE = false, errors.New("unreadable")
	r.run(3, time.Minute)
	if r.probes != 0 || r.kicks != 0 {
		t.Fatalf("a claim read error must skip: probes=%d kicks=%d", r.probes, r.kicks)
	}
}

func TestStateRoundTripAndCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "w.json")
	if s, err := LoadState(p); err != nil || !s.LastRun.IsZero() {
		t.Fatalf("missing file: %+v %v", s, err)
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	in := State{LastRun: now, Failures: 2, Kicks: []time.Time{now}, GaveUp: &now, Unreported: []Event{{Kind: "kick", At: now, Detail: "d"}}}
	if err := SaveState(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadState(p)
	if err != nil || !out.LastRun.Equal(now) || out.Failures != 2 || len(out.Kicks) != 1 || out.GaveUp == nil || len(out.Unreported) != 1 {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	// corrupt: LoadState errors; Run resets and still probes
	r := newRig(t)
	d := r.deps()
	d.Load = func() (State, error) { return LoadState(corrupt(t)) }
	if err := Run(context.Background(), d); err != nil || r.probes != 1 {
		t.Fatalf("corrupt state: err=%v probes=%d", err, r.probes)
	}
}

func corrupt(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := writeFile(p, "{nope"); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStaleRun(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if !StaleRun(State{}, now) || !StaleRun(State{LastRun: now.Add(-4 * time.Minute)}, now) || StaleRun(State{LastRun: now.Add(-30 * time.Second)}, now) {
		t.Fatal("StaleRun wrong")
	}
}

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o600) }

func TestWatchSkipBreaksTheWedgedStreak(t *testing.T) {
	r := newRig(t)
	r.res, r.detail = Wedged, "503"
	r.run(2, time.Minute)
	r.maint = true
	r.run(1, time.Minute)
	r.maint = false
	r.run(2, time.Minute)
	if r.kicks != 0 || r.st.Failures != 2 {
		t.Fatalf("kicks=%d failures=%d", r.kicks, r.st.Failures)
	}
}

func TestWatchStalledStreakRestartsAtOne(t *testing.T) {
	r := newRig(t)
	r.res, r.detail = Wedged, "503"
	r.run(2, time.Minute)
	r.now = r.now.Add(10 * time.Minute) // a sleep, a long run gap
	r.run(1, time.Minute)
	if r.kicks != 0 || r.st.Failures != 1 {
		t.Fatalf("kicks=%d failures=%d", r.kicks, r.st.Failures)
	}
}

func TestWatchStreakAtOneMinuteStillKicks(t *testing.T) {
	r := newRig(t)
	r.res, r.detail = Wedged, "503"
	r.run(3, time.Minute)
	if r.kicks != 1 {
		t.Fatalf("kicks = %d", r.kicks)
	}
}

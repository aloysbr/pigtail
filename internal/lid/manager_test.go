package lid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/powersrc"
	"github.com/aloysbr/pigtail/internal/daemon/thermal"
)

// fakeRunner is goroutine-safe (its own mutex, never held while onRead runs)
// and logs every call in order: "read0"/"read1" (value returned),
// "set0"/"set1", "probe0"/"probe1" — the D7 ordering tests read calls.
type fakeRunner struct {
	mu       sync.Mutex
	disabled bool
	setErr   error
	offErr   error // returned by SetSleepDisabled(false) only: fails On's rollback
	readErr  error
	probeErr error
	sets     []bool
	probes   []bool
	reads    int
	calls    []string
	onRead   func() // test hook, run after the read is recorded, before it returns
}

func (f *fakeRunner) ReadPM() (string, error) {
	f.mu.Lock()
	f.reads++
	if f.readErr != nil {
		f.mu.Unlock()
		return "", f.readErr
	}
	v := b01(f.disabled)
	f.calls = append(f.calls, fmt.Sprintf("read%d", v))
	hook := f.onRead
	f.mu.Unlock()
	if hook != nil {
		hook() // the value is already read: a write slipped in here is the D7 race
	}
	return fmt.Sprintf(" SleepDisabled\t\t%d\n", v), nil
}

func (f *fakeRunner) SetSleepDisabled(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, on)
	f.calls = append(f.calls, fmt.Sprintf("set%d", b01(on)))
	if f.setErr != nil {
		return f.setErr
	}
	if !on && f.offErr != nil {
		return f.offErr
	}
	f.disabled = on
	return nil
}

func (f *fakeRunner) ProbeSet(cur bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, cur)
	f.calls = append(f.calls, fmt.Sprintf("probe%d", b01(cur)))
	if f.probeErr != nil {
		return f.probeErr
	}
	f.disabled = cur
	return nil
}

func (f *fakeRunner) snapshot() (sets []bool, calls []string, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sets), slices.Clone(f.calls), f.reads
}

func (f *fakeRunner) setDisabled(v bool) {
	f.mu.Lock()
	f.disabled = v
	f.mu.Unlock()
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

var t0 = time.Date(2026, 10, 16, 22, 0, 0, 0, time.Local)

func newM(t *testing.T, r *fakeRunner, c *clock, lv *powersrc.Level, path string) *Manager {
	t.Helper()
	return New(Config{StatePath: path, Runner: r, Now: c.now, Logf: t.Logf,
		Battery: func() (powersrc.Level, error) {
			if lv == nil {
				return powersrc.Level{}, errors.New("no reading")
			}
			return *lv, nil
		}})
}

func tmpState(t *testing.T) string {
	t.Helper()
	return StatePath(filepath.Join(t.TempDir(), "p.db"))
}

func mustStatus(t *testing.T, m *Manager) Status {
	t.Helper()
	st, err := m.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return st
}

// probesFollowReads checks that every probeV in calls comes right after a
// readV of the same value: the probe never writes a value it did not just read.
func probesFollowReads(t *testing.T, calls []string) {
	t.Helper()
	for i, c := range calls {
		if !strings.HasPrefix(c, "probe") {
			continue
		}
		v := strings.TrimPrefix(c, "probe")
		if i == 0 || calls[i-1] != "read"+v {
			t.Errorf("calls %v: %s at %d does not follow read%s", calls, c, i, v)
		}
	}
}

func TestOnSetsDeadlineAndPersists(t *testing.T) {
	path := tmpState(t)
	r := &fakeRunner{}
	c := &clock{t0}
	m := newM(t, r, c, nil, path)
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	sets, _, _ := r.snapshot()
	if !slices.Equal(sets, []bool{true}) {
		t.Fatalf("sets = %v", sets)
	}
	st := mustStatus(t, m)
	if !st.SleepDisabled || !st.Deadline.Equal(t0.Add(8*time.Hour)) {
		t.Fatalf("status = %+v", st)
	}
	m2 := newM(t, r, c, nil, path)
	if st2 := mustStatus(t, m2); !st2.Deadline.Equal(t0.Add(8 * time.Hour)) {
		t.Fatalf("reloaded deadline = %v", st2.Deadline)
	}
}

func TestOnRejectsHours(t *testing.T) {
	r := &fakeRunner{}
	m := newM(t, r, &clock{t0}, nil, tmpState(t))
	for _, h := range []int{0, 25} {
		if _, err := m.On(h); !errors.Is(err, ErrHours) {
			t.Errorf("On(%d) = %v, want ErrHours", h, err)
		}
	}
	if sets, _, _ := r.snapshot(); len(sets) != 0 {
		t.Fatalf("sets = %v", sets)
	}
}

func TestOnNotSetUp(t *testing.T) {
	r := &fakeRunner{setErr: ErrNotSetUp}
	m := newM(t, r, &clock{t0}, nil, tmpState(t))
	if _, err := m.On(8); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("On = %v", err)
	}
	if st := mustStatus(t, m); !st.Deadline.IsZero() {
		t.Fatalf("deadline armed: %+v", st)
	}
}

func TestOnSaveFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "x", "lid-state.json")
	r := &fakeRunner{}
	m := newM(t, r, &clock{t0}, nil, path)
	if _, err := m.On(8); err == nil {
		t.Fatal("On succeeded without a persisted deadline")
	}
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, false}) {
		t.Fatalf("sets = %v, want [true false]", sets)
	}
}

func TestTickIdleNoExec(t *testing.T) {
	r := &fakeRunner{}
	m := newM(t, r, &clock{t0}, &powersrc.Level{OnBattery: true, Percent: 1}, tmpState(t))
	m.Tick()
	if sets, _, reads := r.snapshot(); reads != 0 || len(sets) != 0 {
		t.Fatalf("reads = %d, sets = %v", reads, sets)
	}
	th, ls := &fakeThermal{s: thermal.Critical}, &fakeLid{closed: true}
	newMT(t, r, &clock{t0}, nil, th, ls, tmpState(t)).Tick()
	if th.n() != 0 || ls.n() != 0 {
		t.Fatalf("off: thermal read %d times, lid read %d times, want 0", th.n(), ls.n())
	}
}

func TestTickDeadline(t *testing.T) {
	path := tmpState(t)
	r := &fakeRunner{}
	c := &clock{t0}
	m := newM(t, r, c, nil, path)
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(59 * time.Minute)
	m.Tick()
	if sets, _, _ := r.snapshot(); len(sets) != 1 {
		t.Fatalf("off before the deadline: %v", sets)
	}
	c.t = t0.Add(61 * time.Minute)
	m.Tick()
	sets, _, _ := r.snapshot()
	if sets[len(sets)-1] != false {
		t.Fatalf("sets = %v", sets)
	}
	st := mustStatus(t, m)
	if st.LastOff == nil || st.LastOff.Reason != "deadline" {
		t.Fatalf("last off = %+v", st.LastOff)
	}
	if st2 := mustStatus(t, newM(t, r, c, nil, path)); !st2.Deadline.IsZero() {
		t.Fatalf("reloaded state still armed: %+v", st2)
	}
}

func TestTickLowBattery(t *testing.T) {
	cases := []struct {
		lv     powersrc.Level
		offWas bool
	}{
		{powersrc.Level{OnBattery: true, Percent: 25}, true},
		{powersrc.Level{OnBattery: true, Percent: 26}, false},
		{powersrc.Level{OnBattery: false, Percent: 5}, false},
		{powersrc.Level{OnBattery: true, Percent: -1}, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%+v", tc.lv), func(t *testing.T) {
			r := &fakeRunner{}
			lv := tc.lv
			m := newM(t, r, &clock{t0}, &lv, tmpState(t))
			if _, err := m.On(8); err != nil {
				t.Fatal(err)
			}
			m.Tick()
			sets, _, _ := r.snapshot()
			st := mustStatus(t, m)
			if !tc.offWas {
				if len(sets) != 1 || st.LastOff != nil {
					t.Fatalf("unexpected off: sets %v, last off %+v", sets, st.LastOff)
				}
				return
			}
			if !slices.Equal(sets, []bool{true, false}) || st.LastOff == nil || st.LastOff.Reason != "battery 25%" {
				t.Fatalf("sets %v, last off %+v", sets, st.LastOff)
			}
		})
	}
}

func TestTickBatteryReadErrorKeepsDeadline(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m := newM(t, r, c, nil, tmpState(t))
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(30 * time.Minute)
	m.Tick()
	if sets, _, _ := r.snapshot(); len(sets) != 1 {
		t.Fatalf("off on a failed battery reading: %v", sets)
	}
	c.t = t0.Add(time.Hour)
	m.Tick()
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, false}) {
		t.Fatalf("no off at the deadline: %v", sets)
	}
}

func TestTickExternalOff(t *testing.T) {
	r := &fakeRunner{}
	m := newM(t, r, &clock{t0}, nil, tmpState(t))
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	r.setDisabled(false)
	m.Tick()
	if sets, _, _ := r.snapshot(); len(sets) != 1 {
		t.Fatalf("sudo called on an external off: %v", sets)
	}
	st := mustStatus(t, m)
	if st.LastOff == nil || st.LastOff.Reason != "external" || !st.Deadline.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}

func TestTickOffFailureVisible(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m := newM(t, r, c, nil, tmpState(t))
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.setErr = ErrNotSetUp
	r.mu.Unlock()
	c.t = t0.Add(2 * time.Hour)
	m.Tick()
	if st := mustStatus(t, m); !strings.Contains(st.LastError, "auto-off (deadline) failed") || st.Deadline.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	r.mu.Lock()
	r.setErr = nil
	r.mu.Unlock()
	m.Tick()
	st := mustStatus(t, m)
	if st.LastError != "" || st.SleepDisabled || st.LastOff == nil || st.LastOff.Reason != "deadline" {
		t.Fatalf("status = %+v", st)
	}
}

func TestRunReconcilesPastDeadlineAtStart(t *testing.T) {
	path := tmpState(t)
	r := &fakeRunner{}
	c := &clock{t0}
	if _, err := newM(t, r, c, nil, path).On(1); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(3 * time.Hour) // the daemon was down past the deadline
	m := newM(t, r, c, nil, path)
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time) // never fed: only the start-up Tick may act
	done := make(chan struct{})
	go func() { m.Run(ctx, tick); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		sets, _, _ := r.snapshot()
		if len(sets) == 2 {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("no off at start: sets %v", sets)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, false}) {
		t.Fatalf("sets = %v", sets)
	}
}

func TestOffNoTimer(t *testing.T) {
	r := &fakeRunner{disabled: true}
	m := newM(t, r, &clock{t0}, nil, tmpState(t))
	if st := mustStatus(t, m); !st.SleepDisabled || !st.Deadline.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if _, err := m.Off(); err != nil {
		t.Fatal(err)
	}
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{false}) {
		t.Fatalf("sets = %v", sets)
	}
	if st := mustStatus(t, m); st.LastOff == nil || st.LastOff.Reason != "manual" {
		t.Fatalf("last off = %+v", st.LastOff)
	}
}

func TestCorruptStateFile(t *testing.T) {
	path := tmpState(t)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newM(t, &fakeRunner{}, &clock{t0}, nil, path)
	if st := mustStatus(t, m); !st.Deadline.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}

func TestOnRearmsActiveState(t *testing.T) {
	path := tmpState(t)
	r := &fakeRunner{}
	c := &clock{t0}
	m := newM(t, r, c, nil, path)
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(2 * time.Hour)
	if _, err := m.On(3); err != nil {
		t.Fatal(err)
	}
	want := t0.Add(5 * time.Hour)
	if st := mustStatus(t, m); !st.Deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", st.Deadline, want)
	}
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, true}) {
		t.Fatalf("sets = %v", sets)
	}
	if st := mustStatus(t, newM(t, r, c, nil, path)); !st.Deadline.Equal(want) {
		t.Fatalf("reloaded deadline = %v", st.Deadline)
	}
	c.t = t0.Add(4*time.Hour + 59*time.Minute)
	m.Tick()
	if sets, _, _ := r.snapshot(); len(sets) != 2 {
		t.Fatalf("off before the re-armed deadline: %v", sets)
	}
	c.t = t0.Add(5*time.Hour + time.Minute)
	m.Tick()
	st := mustStatus(t, m)
	if st.LastOff == nil || st.LastOff.Reason != "deadline" {
		t.Fatalf("last off = %+v", st.LastOff)
	}
}

func TestProbeWritesCurrentValue(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		r := &fakeRunner{}
		m := newM(t, r, &clock{t0}, nil, tmpState(t))
		res, err := m.Probe()
		if err != nil || res != (ProbeResult{RuleOK: true}) {
			t.Fatalf("Probe = %+v, %v", res, err)
		}
		sets, calls, _ := r.snapshot()
		if !slices.Equal(calls, []string{"read0", "probe0"}) || len(sets) != 0 {
			t.Fatalf("calls = %v, sets = %v", calls, sets)
		}
	})
	t.Run("on with a deadline", func(t *testing.T) {
		r := &fakeRunner{disabled: true}
		m := newM(t, r, &clock{t0}, nil, tmpState(t))
		if _, err := m.On(8); err != nil {
			t.Fatal(err)
		}
		_, before, _ := r.snapshot()
		res, err := m.Probe()
		if err != nil || res != (ProbeResult{RuleOK: true}) {
			t.Fatalf("Probe = %+v, %v", res, err)
		}
		sets, calls, _ := r.snapshot()
		if !slices.Equal(calls[len(before):], []string{"read1", "probe1"}) || len(sets) != 1 {
			t.Fatalf("calls = %v, sets = %v", calls, sets)
		}
		probesFollowReads(t, calls)
	})
	for _, perr := range []error{ErrNotSetUp, errors.New("exit status 1")} {
		t.Run(perr.Error(), func(t *testing.T) {
			r := &fakeRunner{probeErr: perr}
			m := newM(t, r, &clock{t0}, nil, tmpState(t))
			res, err := m.Probe()
			if err != nil || res.RuleOK {
				t.Fatalf("Probe = %+v, %v", res, err)
			}
			sets, calls, _ := r.snapshot()
			if len(sets) != 0 {
				t.Fatalf("sets = %v", sets)
			}
			probesFollowReads(t, calls)
		})
	}
}

func TestProbeUnreadableState(t *testing.T) {
	r := &fakeRunner{readErr: errors.New("boom")}
	m := newM(t, r, &clock{t0}, nil, tmpState(t))
	_, err := m.Probe()
	if err == nil || !strings.Contains(err.Error(), "pmset -g") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Probe err = %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.probes) != 0 {
		t.Fatalf("probes = %v", r.probes)
	}
}

func TestProbeNoTimerWarning(t *testing.T) {
	t.Run("no timer", func(t *testing.T) {
		r := &fakeRunner{disabled: true}
		m := newM(t, r, &clock{t0}, nil, tmpState(t))
		res, err := m.Probe()
		if err != nil || res != (ProbeResult{RuleOK: true, Warning: ProbeNoTimerWarning}) {
			t.Fatalf("Probe = %+v, %v", res, err)
		}
		if _, calls, _ := r.snapshot(); !slices.Equal(calls, []string{"read1", "probe1", "read1"}) {
			t.Fatalf("calls = %v", calls)
		}
		if !strings.Contains(res.Warning, "pigtail lid off") {
			t.Fatalf("warning = %q", res.Warning)
		}
	})
	t.Run("probe fails: no re-read", func(t *testing.T) {
		r := &fakeRunner{disabled: true, probeErr: ErrNotSetUp}
		m := newM(t, r, &clock{t0}, nil, tmpState(t))
		res, err := m.Probe()
		if err != nil || res.Warning != "" || res.RuleOK {
			t.Fatalf("Probe = %+v, %v", res, err)
		}
		if _, calls, _ := r.snapshot(); !slices.Equal(calls, []string{"read1", "probe1"}) {
			t.Fatalf("calls = %v", calls)
		}
	})
	t.Run("timer armed", func(t *testing.T) {
		r := &fakeRunner{disabled: true}
		m := newM(t, r, &clock{t0}, nil, tmpState(t))
		if _, err := m.On(8); err != nil {
			t.Fatal(err)
		}
		if res, err := m.Probe(); err != nil || res.Warning != "" {
			t.Fatalf("Probe = %+v, %v", res, err)
		}
	})
}

func TestProbeHoldsLockAgainstTick(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m := newM(t, r, c, nil, tmpState(t))
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(2 * time.Hour) // the next Tick turns sleep control off (deadline)
	r.setDisabled(true)

	done := make(chan struct{})
	var fired atomic.Bool // first read only: Probe's own read
	r.mu.Lock()
	r.onRead = func() {
		if !fired.CompareAndSwap(false, true) {
			return
		}
		go func() { m.Tick(); close(done) }()
		select { // give the Tick every chance to slip in between read and probe
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
	}
	r.mu.Unlock()

	if _, err := m.Probe(); err != nil {
		t.Fatal(err)
	}
	<-done

	_, calls, _ := r.snapshot()
	for i := 0; i+2 < len(calls); i++ {
		if calls[i] == "read1" && calls[i+1] == "set0" && calls[i+2] == "probe1" {
			t.Fatalf("calls %v: a Tick off slipped between the probe's read and write", calls)
		}
	}
	probesFollowReads(t, calls)
	r.mu.Lock()
	disabled := r.disabled
	r.onRead = nil
	r.mu.Unlock()
	if disabled {
		t.Fatalf("calls %v: sleep left disabled after the deadline", calls)
	}
	if st := mustStatus(t, m); st.LastOff == nil || st.LastOff.Reason != "deadline" {
		t.Fatalf("last off = %+v", st.LastOff)
	}
}

func TestTickBatteryReadOutsideLock(t *testing.T) {
	r := &fakeRunner{}
	entered := make(chan struct{})
	release := make(chan struct{})
	m := New(Config{StatePath: tmpState(t), Runner: r, Now: (&clock{t0}).now, Logf: t.Logf,
		Battery: func() (powersrc.Level, error) {
			close(entered)
			<-release
			return powersrc.Level{OnBattery: true, Percent: 50}, nil
		}})
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	tickDone := make(chan struct{})
	go func() { m.Tick(); close(tickDone) }()
	<-entered

	offDone := make(chan error, 1)
	go func() { _, err := m.Off(); offDone <- err }()
	select {
	case err := <-offDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-tickDone
		t.Fatal("Off blocked behind the battery read: it must happen outside the lock")
	}
	close(release)
	<-tickDone
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, false}) {
		t.Fatalf("sets = %v, want [true false] (the Tick must re-read the state under the lock)", sets)
	}
}

// changeRec records every OnChange call and whether the manager lock was held
// at that moment (probed with TryLock once m is set).
type changeRec struct {
	mu           sync.Mutex
	got          []Change
	m            *Manager // set after New, to probe the lock
	lockedAtCall []bool
}

func (c *changeRec) on(ch Change) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ch)
	if c.m != nil {
		free := c.m.mu.TryLock()
		if free {
			c.m.mu.Unlock()
		}
		c.lockedAtCall = append(c.lockedAtCall, !free)
	}
}

func (c *changeRec) snapshot() []Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

// newMRec is newM with an OnChange recorder.
// path: tmpState(t) normally; a path in a missing directory makes saveLocked fail.
func newMRec(t *testing.T, r *fakeRunner, c *clock, lv *powersrc.Level, path string) (*Manager, *changeRec) {
	t.Helper()
	rec := &changeRec{}
	m := New(Config{StatePath: path, Runner: r, Now: c.now, Logf: t.Logf,
		OnChange: rec.on,
		Battery: func() (powersrc.Level, error) {
			if lv == nil {
				return powersrc.Level{}, errors.New("no reading")
			}
			return *lv, nil
		}})
	rec.m = m
	return m, rec
}

func wantChanges(t *testing.T, rec *changeRec, want ...Change) {
	t.Helper()
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
}

func TestOnChangeOnOffRearm(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m, rec := newMRec(t, r, c, nil, tmpState(t))
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.On(3); err != nil { // re-arm (D5)
		t.Fatal(err)
	}
	if _, err := m.Off(); err != nil {
		t.Fatal(err)
	}
	wantChanges(t, rec, Change{true, "on"}, Change{true, "on"}, Change{false, "manual"})
}

func TestOnChangeAutoOff(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		r := &fakeRunner{}
		c := &clock{t0}
		m, rec := newMRec(t, r, c, nil, tmpState(t))
		if _, err := m.On(1); err != nil {
			t.Fatal(err)
		}
		c.t = t0.Add(61 * time.Minute)
		m.Tick()
		wantChanges(t, rec, Change{true, "on"}, Change{false, "deadline"})
	})
	t.Run("battery", func(t *testing.T) {
		r := &fakeRunner{}
		m, rec := newMRec(t, r, &clock{t0}, &powersrc.Level{OnBattery: true, Percent: 10}, tmpState(t))
		if _, err := m.On(1); err != nil {
			t.Fatal(err)
		}
		m.Tick()
		wantChanges(t, rec, Change{true, "on"}, Change{false, "battery 10%"})
	})
	t.Run("thermal critical", func(t *testing.T) {
		r := &fakeRunner{}
		m, rec, _ := newMTRec(t, r, &clock{t0}, nil, &fakeThermal{s: thermal.Critical}, &fakeLid{closed: true}, tmpState(t))
		if _, err := m.On(1); err != nil {
			t.Fatal(err)
		}
		m.Tick()
		wantChanges(t, rec, Change{true, "on"}, Change{false, "thermal critical"})
	})
	t.Run("external", func(t *testing.T) {
		r := &fakeRunner{}
		m, rec := newMRec(t, r, &clock{t0}, nil, tmpState(t))
		if _, err := m.On(1); err != nil {
			t.Fatal(err)
		}
		r.setDisabled(false)
		m.Tick()
		wantChanges(t, rec, Change{true, "on"}, Change{false, "external"})
	})
}

func TestOnChangeNothingOnNoOp(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m, rec := newMRec(t, r, c, nil, tmpState(t))
	// Stopped: reads, the probe and an idle tick change nothing.
	mustStatus(t, m)
	if _, err := m.Probe(); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	wantChanges(t, rec)

	if _, err := m.On(1); err != nil { // priming: emits {true,"on"}, outside the scope
		t.Fatal(err)
	}
	base := len(rec.snapshot())
	if base != 1 {
		t.Fatalf("base = %d, want 1", base)
	}
	c.t = t0.Add(30 * time.Minute) // armed, before the deadline
	mustStatus(t, m)
	if _, err := m.Probe(); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	if _, err := m.On(99); !errors.Is(err, ErrHours) {
		t.Fatalf("On(99) = %v", err)
	}
	r.mu.Lock()
	r.setErr = errors.New("pmset")
	r.mu.Unlock()
	if _, err := m.On(1); err == nil {
		t.Fatal("On succeeded with a failing pmset")
	}
	if n := len(rec.snapshot()); n != base {
		t.Fatalf("changes = %+v, want only the priming one", rec.snapshot())
	}
}

func TestOnChangeAutoOffFailureOncePerError(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m, rec := newMRec(t, r, c, nil, tmpState(t))
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	setErr := func(err error) {
		r.mu.Lock()
		r.setErr = err
		r.mu.Unlock()
	}
	setErr(errors.New("boom"))
	c.t = t0.Add(2 * time.Hour)
	m.Tick()
	m.Tick()
	m.Tick()
	wantChanges(t, rec, Change{true, "on"}, Change{true, "auto-off failed"})
	setErr(errors.New("other"))
	m.Tick()
	wantChanges(t, rec, Change{true, "on"}, Change{true, "auto-off failed"}, Change{true, "auto-off failed"})
	setErr(nil)
	m.Tick()
	wantChanges(t, rec, Change{true, "on"}, Change{true, "auto-off failed"}, Change{true, "auto-off failed"},
		Change{false, "deadline"})
}

func TestOnChangeOnFailedSave(t *testing.T) {
	t.Run("rollback ok", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "lid-state.json")
		r := &fakeRunner{}
		m, rec := newMRec(t, r, &clock{t0}, nil, path)
		if _, err := m.On(1); err == nil {
			t.Fatal("On succeeded without a persisted deadline")
		}
		wantChanges(t, rec)
		if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, false}) {
			t.Fatalf("sets = %v, want [true false]", sets)
		}
		if st := mustStatus(t, m); !st.Deadline.IsZero() {
			t.Fatalf("status = %+v, want no deadline", st)
		}
	})
	t.Run("rollback fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "lid-state.json")
		r := &fakeRunner{offErr: errors.New("sudo")}
		m, rec := newMRec(t, r, &clock{t0}, nil, path)
		if _, err := m.On(1); err == nil {
			t.Fatal("On succeeded without a persisted deadline")
		}
		wantChanges(t, rec, Change{true, "on"})
		if st := mustStatus(t, m); !st.SleepDisabled || !st.Deadline.Equal(t0.Add(time.Hour)) {
			t.Fatalf("status = %+v, want sleep disabled until t0+1h", st)
		}
	})
}

func TestOnChangeCalledOutsideLock(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m, rec := newMRec(t, r, c, nil, tmpState(t))
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Off(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(2 * time.Hour)
	m.Tick() // auto-off (deadline)
	rec.mu.Lock()
	locked := slices.Clone(rec.lockedAtCall)
	rec.mu.Unlock()
	if len(locked) != 4 || slices.Contains(locked, true) {
		t.Fatalf("lock held at OnChange calls: %v (want 4 × false)", locked)
	}
}

func TestOnChangeNilIsFine(t *testing.T) {
	r := &fakeRunner{}
	c := &clock{t0}
	m := New(Config{StatePath: tmpState(t), Runner: r, Now: c.now, Logf: t.Logf}) // OnChange nil
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Off(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.On(1); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(2 * time.Hour)
	m.Tick()
}

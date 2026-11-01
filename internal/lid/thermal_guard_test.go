package lid

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/powersrc"
	"github.com/aloysbr/pigtail/internal/daemon/thermal"
)

// fakeThermal is the thermal reader of the lid tests: it returns its current
// (state, err) at every call — a test changes it between ticks with set — and
// counts the calls. block, when set, runs inside the read (outside the lock test).
type fakeThermal struct {
	mu    sync.Mutex
	s     thermal.State
	err   error
	calls int
	block func()
}

func (f *fakeThermal) read() (thermal.State, error) {
	f.mu.Lock()
	f.calls++
	s, err, b := f.s, f.err, f.block
	f.mu.Unlock()
	if b != nil {
		b()
	}
	return s, err
}

func (f *fakeThermal) set(s thermal.State, err error) {
	f.mu.Lock()
	f.s, f.err = s, err
	f.mu.Unlock()
}

func (f *fakeThermal) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeLid is the lid (clamshell) reader of the lid tests, on the same model.
type fakeLid struct {
	mu     sync.Mutex
	closed bool
	err    error
	calls  int
	block  func()
}

func (f *fakeLid) read() (bool, error) {
	f.mu.Lock()
	f.calls++
	c, err, b := f.closed, f.err, f.block
	f.mu.Unlock()
	if b != nil {
		b()
	}
	return c, err
}

func (f *fakeLid) set(closed bool, err error) {
	f.mu.Lock()
	f.closed, f.err = closed, err
	f.mu.Unlock()
}

func (f *fakeLid) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// logRec records every Logf line (and passes it on to t.Logf).
type logRec struct {
	mu sync.Mutex
	t  *testing.T
	ls []string
}

func (l *logRec) logf(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.ls = append(l.ls, s)
	l.mu.Unlock()
	l.t.Log(s)
}

func (l *logRec) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.ls)
}

var errRead = errors.New("boom")

// newMTRec builds a Manager with the thermal and lid fakes, an OnChange
// recorder and a log recorder. lv nil = on AC at 100 %; th nil = no thermal
// guard; ls nil = no lid reader. Battery reads *lv at every call.
func newMTRec(t *testing.T, r *fakeRunner, c *clock, lv *powersrc.Level, th *fakeThermal, ls *fakeLid, path string) (*Manager, *changeRec, *logRec) {
	t.Helper()
	if lv == nil {
		lv = &powersrc.Level{OnBattery: false, Percent: 100}
	}
	rec := &changeRec{}
	lr := &logRec{t: t}
	cfg := Config{StatePath: path, Runner: r, Now: c.now, Logf: lr.logf, OnChange: rec.on,
		Battery: func() (powersrc.Level, error) { return *lv, nil }}
	if th != nil {
		cfg.Thermal = th.read
	}
	if ls != nil {
		cfg.LidClosed = ls.read
	}
	m := New(cfg)
	rec.m = m
	return m, rec, lr
}

func newMT(t *testing.T, r *fakeRunner, c *clock, lv *powersrc.Level, th *fakeThermal, ls *fakeLid, path string) *Manager {
	t.Helper()
	m, _, _ := newMTRec(t, r, c, lv, th, ls, path)
	return m
}

func ticks(m *Manager, n int) {
	for range n {
		m.Tick()
	}
}

// armed returns a manager turned on for 8 h with the given fakes.
func armed(t *testing.T, r *fakeRunner, lv *powersrc.Level, th *fakeThermal, ls *fakeLid) *Manager {
	t.Helper()
	m := newMT(t, r, &clock{t0}, lv, th, ls, tmpState(t))
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	return m
}

func wantNoOff(t *testing.T, r *fakeRunner) {
	t.Helper()
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true}) {
		t.Fatalf("sets = %v, want [true] (no off)", sets)
	}
}

func wantOff(t *testing.T, r *fakeRunner, m *Manager, reason string) {
	t.Helper()
	if sets, _, _ := r.snapshot(); len(sets) < 2 || sets[len(sets)-1] {
		t.Fatalf("sets = %v, want an off last", sets)
	}
	if st := mustStatus(t, m); st.LastOff == nil || st.LastOff.Reason != reason {
		t.Fatalf("last off = %+v, want %q", st.LastOff, reason)
	}
}

func TestTickThermalCriticalLidOpenNoOff(t *testing.T) {
	r := &fakeRunner{}
	th, ls := &fakeThermal{s: thermal.Critical}, &fakeLid{closed: false}
	m := armed(t, r, nil, th, ls)
	ticks(m, 3)
	wantNoOff(t, r)
	st := mustStatus(t, m)
	if st.ThermalState != "critical" || st.LidClosed == nil || *st.LidClosed {
		t.Fatalf("status = %+v (lid %v)", st, st.LidClosed)
	}
}

func TestTickThermalCriticalLidClosedImmediate(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Critical}, &fakeLid{closed: true})
	m.Tick()
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, false}) {
		t.Fatalf("sets = %v", sets)
	}
	wantOff(t, r, m, "thermal critical")
}

func TestTickThermalSeriousTenClosed(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Serious}, &fakeLid{closed: true})
	ticks(m, SeriousTicks-1)
	wantNoOff(t, r)
	m.Tick()
	wantOff(t, r, m, "thermal serious")
	if SeriousTicks != 10 {
		t.Fatalf("SeriousTicks = %d, want 10 (D2·t2)", SeriousTicks)
	}
}

func TestTickThermalLidOpenNeverOff(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Serious}, &fakeLid{closed: false})
	ticks(m, 30)
	wantNoOff(t, r)
}

func TestTickThermalLidOpenResetsStreak(t *testing.T) {
	r := &fakeRunner{}
	ls := &fakeLid{closed: true}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Serious}, ls)
	ticks(m, 5)
	ls.set(false, nil)
	m.Tick()
	ls.set(true, nil)
	ticks(m, 9)
	wantNoOff(t, r)
	m.Tick()
	wantOff(t, r, m, "thermal serious")
}

func TestTickThermalNominalFairNeverOff(t *testing.T) {
	r := &fakeRunner{}
	th := &fakeThermal{s: thermal.Fair}
	m := armed(t, r, nil, th, &fakeLid{closed: true})
	ticks(m, 5)
	th.set(thermal.Nominal, nil)
	ticks(m, 5)
	wantNoOff(t, r)
}

func TestTickThermalStreakResets(t *testing.T) {
	r := &fakeRunner{}
	th := &fakeThermal{s: thermal.Serious}
	m := armed(t, r, nil, th, &fakeLid{closed: true})
	ticks(m, 9)
	th.set(thermal.Fair, nil)
	m.Tick()
	th.set(thermal.Serious, nil)
	ticks(m, 9)
	wantNoOff(t, r)
	m.Tick()
	wantOff(t, r, m, "thermal serious")
}

func TestTickLidReadErrorNoThermalOff(t *testing.T) {
	r := &fakeRunner{}
	th, ls := &fakeThermal{s: thermal.Critical}, &fakeLid{err: errRead}
	m := armed(t, r, nil, th, ls)
	ticks(m, 2)
	wantNoOff(t, r)
	st := mustStatus(t, m)
	if !strings.Contains(st.GuardError, "lid reading failed") || st.LidClosed != nil || st.ThermalState != "critical" {
		t.Fatalf("status = %+v", st)
	}
	th.set(thermal.Fair, nil)
	ls.set(true, nil)
	m.Tick()
	if st := mustStatus(t, m); st.GuardError != "" {
		t.Fatalf("guard error after a good reading = %q", st.GuardError)
	}
	th.set(thermal.Critical, nil)
	m.Tick()
	wantOff(t, r, m, "thermal critical")
}

func TestTickLidReadErrorKeepsStreak(t *testing.T) {
	r := &fakeRunner{}
	ls := &fakeLid{closed: true}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Serious}, ls)
	ticks(m, 9)
	ls.set(false, errRead)
	m.Tick()
	wantNoOff(t, r)
	ls.set(true, nil)
	m.Tick()
	wantOff(t, r, m, "thermal serious")
}

func TestTickLidUnknownNominalKeepsStreak(t *testing.T) {
	r := &fakeRunner{}
	th, ls := &fakeThermal{s: thermal.Serious}, &fakeLid{closed: true}
	m := armed(t, r, nil, th, ls)
	ticks(m, 5)
	th.set(thermal.Nominal, nil)
	ls.set(false, errRead)
	m.Tick()
	th.set(thermal.Serious, nil)
	ls.set(true, nil)
	ticks(m, 4)
	wantNoOff(t, r) // 5 + 4 = 9
	m.Tick()
	wantOff(t, r, m, "thermal serious") // 5 + 5 = 10: the unknown lid did not reset
}

func TestTickLidNoReader(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Critical}, nil)
	m.Tick()
	wantNoOff(t, r)
	st := mustStatus(t, m)
	if !strings.Contains(st.GuardError, "lid reading failed: no reader") || st.LidClosed != nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestTickThermalReadErrorKeepsStreak(t *testing.T) {
	r := &fakeRunner{}
	th := &fakeThermal{s: thermal.Serious}
	m := armed(t, r, nil, th, &fakeLid{closed: true})
	ticks(m, 9)
	th.set(0, errRead)
	m.Tick()
	wantNoOff(t, r)
	if st := mustStatus(t, m); !strings.Contains(st.GuardError, "thermal reading failed") || st.ThermalState != "serious" {
		t.Fatalf("status = %+v", st)
	}
	th.set(thermal.Serious, nil)
	m.Tick()
	wantOff(t, r, m, "thermal serious")
	if st := mustStatus(t, m); st.GuardError != "" {
		t.Fatalf("guard error = %q", st.GuardError)
	}
}

func TestTickThermalStreakResetOnOff(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Serious}, &fakeLid{closed: true})
	ticks(m, 9)
	if _, err := m.Off(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	if sets, _, _ := r.snapshot(); !slices.Equal(sets, []bool{true, false, true}) {
		t.Fatalf("sets = %v, want [true false true]", sets)
	}
}

func TestTickThermalSeriousSurvivesRearm(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Serious}, &fakeLid{closed: true})
	ticks(m, 9)
	if _, err := m.On(4); err != nil {
		t.Fatal(err)
	}
	m.Tick()
	wantOff(t, r, m, "thermal serious")
}

func TestTickBatteryIgnoresLid(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, &powersrc.Level{OnBattery: true, Percent: 10}, &fakeThermal{s: thermal.Nominal}, &fakeLid{closed: false})
	m.Tick()
	wantOff(t, r, m, "battery 10%")
}

func TestTickReasonOrder(t *testing.T) {
	t.Run("deadline before critical", func(t *testing.T) {
		r := &fakeRunner{}
		c := &clock{t0}
		m := newMT(t, r, c, nil, &fakeThermal{s: thermal.Critical}, &fakeLid{closed: true}, tmpState(t))
		if _, err := m.On(1); err != nil {
			t.Fatal(err)
		}
		c.t = t0.Add(2 * time.Hour)
		m.Tick()
		wantOff(t, r, m, "deadline")
	})
	t.Run("critical before battery", func(t *testing.T) {
		r := &fakeRunner{}
		m := armed(t, r, &powersrc.Level{OnBattery: true, Percent: 10}, &fakeThermal{s: thermal.Critical}, &fakeLid{closed: true})
		m.Tick()
		wantOff(t, r, m, "thermal critical")
	})
	t.Run("battery before serious", func(t *testing.T) {
		r := &fakeRunner{}
		lv := &powersrc.Level{OnBattery: true, Percent: 100}
		m := armed(t, r, lv, &fakeThermal{s: thermal.Serious}, &fakeLid{closed: true})
		ticks(m, 9)
		wantNoOff(t, r)
		*lv = powersrc.Level{OnBattery: true, Percent: 10}
		m.Tick()
		wantOff(t, r, m, "battery 10%")
	})
}

func TestTickThermalStatus(t *testing.T) {
	r := &fakeRunner{}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Fair}, &fakeLid{closed: true})
	m.Tick()
	st := mustStatus(t, m)
	if st.ThermalState != "fair" || st.LidClosed == nil || !*st.LidClosed {
		t.Fatalf("status = %+v (lid %v)", st, st.LidClosed)
	}
	if _, err := m.Off(); err != nil {
		t.Fatal(err)
	}
	st = mustStatus(t, m)
	if st.ThermalState != "" || st.LidClosed != nil || st.GuardError != "" {
		t.Fatalf("status after off = %+v", st)
	}
}

func TestTickThermalReadOutsideLock(t *testing.T) {
	for _, which := range []string{"thermal", "lid"} {
		t.Run(which, func(t *testing.T) {
			r := &fakeRunner{}
			entered := make(chan struct{})
			release := make(chan struct{})
			block := func() {
				close(entered)
				<-release
			}
			th, ls := &fakeThermal{s: thermal.Nominal}, &fakeLid{closed: true}
			m := armed(t, r, nil, th, ls)
			if which == "thermal" {
				th.block = block
			} else {
				ls.block = block
			}
			tickDone := make(chan struct{})
			go func() { m.Tick(); close(tickDone) }()
			<-entered
			statusDone := make(chan error, 1)
			go func() { _, err := m.Status(); statusDone <- err }()
			select {
			case err := <-statusDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				close(release)
				<-tickDone
				t.Fatalf("Status blocked behind the %s read: it must happen outside the lock", which)
			}
			close(release)
			<-tickDone
		})
	}
}

func TestTickLidTransitionLogged(t *testing.T) {
	r := &fakeRunner{}
	ls := &fakeLid{}
	m, _, lr := newMTRec(t, r, &clock{t0}, nil, &fakeThermal{s: thermal.Nominal}, ls, tmpState(t))
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"closed", "closed", "open", "open", "error", "error", "closed"} {
		switch step {
		case "closed":
			ls.set(true, nil)
		case "open":
			ls.set(false, nil)
		default:
			ls.set(false, errRead)
		}
		m.Tick()
	}
	transitions := func() []string {
		var out []string
		for _, l := range lr.lines() {
			if l == "lid: lid closed" || l == "lid: lid open" || l == "lid: lid unknown" {
				out = append(out, l)
			}
		}
		return out
	}
	want := []string{"lid: lid closed", "lid: lid open", "lid: lid unknown", "lid: lid closed"}
	if got := transitions(); !slices.Equal(got, want) {
		t.Fatalf("transitions = %q, want %q", got, want)
	}
	if _, err := m.Off(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.On(8); err != nil {
		t.Fatal(err)
	}
	ls.set(true, nil)
	m.Tick()
	want = append(want, "lid: lid closed")
	if got := transitions(); !slices.Equal(got, want) {
		t.Fatalf("after off/on: transitions = %q, want %q", got, want)
	}
}

func TestTickBatteryReadErrorGuardError(t *testing.T) {
	t.Run("battery wired, reading fails", func(t *testing.T) {
		r := &fakeRunner{}
		m := newM(t, r, &clock{t0}, nil, tmpState(t)) // lv nil: Battery errors
		if _, err := m.On(8); err != nil {
			t.Fatal(err)
		}
		m.Tick()
		if st := mustStatus(t, m); !strings.Contains(st.GuardError, "battery reading failed") {
			t.Fatalf("guard error = %q", st.GuardError)
		}
	})
	t.Run("no battery guard", func(t *testing.T) {
		r := &fakeRunner{}
		m := New(Config{StatePath: tmpState(t), Runner: r, Now: (&clock{t0}).now, Logf: t.Logf})
		if _, err := m.On(8); err != nil {
			t.Fatal(err)
		}
		m.Tick()
		if st := mustStatus(t, m); st.GuardError != "" {
			t.Fatalf("guard error = %q, want empty (no battery guard is not a failure)", st.GuardError)
		}
	})
}

func TestTickLidUnknownSeriousKeepsStreak(t *testing.T) {
	r := &fakeRunner{}
	ls := &fakeLid{closed: true}
	m := armed(t, r, nil, &fakeThermal{s: thermal.Serious}, ls)
	ticks(m, 8)
	ls.set(false, errRead)
	m.Tick() // unknown lid + serious: the count must not move
	ls.set(true, nil)
	m.Tick()
	wantNoOff(t, r) // 8 + 0 + 1 = 9
	m.Tick()
	wantOff(t, r, m, "thermal serious") // 10
}

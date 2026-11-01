package spawnreg

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCmux is the tab list WithReservation diffs around a create.
type fakeCmux struct {
	mu      sync.Mutex
	tabs    []Tab
	errAt   map[int]error // call number (1-based) → error
	calls   int
	creates int
}

func (f *fakeCmux) list() ([]Tab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := f.errAt[f.calls]; err != nil {
		return nil, err
	}
	return append([]Tab(nil), f.tabs...), nil
}

func (f *fakeCmux) add(ts ...Tab) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tabs = append(f.tabs, ts...)
}

type handoffRec struct {
	id   string
	tab  Tab
	born time.Time
}

func newReg(t *testing.T) *Registry {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "spawns.jsonl"))
}

func byID(es []Entry, id string) []Entry {
	var out []Entry
	for _, e := range es {
		if e.ID == id {
			out = append(out, e)
		}
	}
	return out
}

func ends(es []Entry) (failed, launched int, refs int) {
	for _, e := range es {
		switch {
		case e.Kind == KindLaunchResult && e.Result == ResultFailed:
			failed++
		case e.Kind == "" && e.Result == ResultLaunched:
			launched++
		case e.Kind == KindLaunchRef:
			refs++
		}
	}
	return
}

func TestWithReservationClosesOnEveryExit(t *testing.T) {
	old := Tab{ID: "OLD", Ref: "workspace:1"}
	nw := Tab{ID: "NEW", Ref: "workspace:9"}
	nw2 := Tab{ID: "NEW2", Ref: "workspace:10"}
	type row struct {
		name     string
		errAt    map[int]error
		fn       func(l *Launch, f *fakeCmux) error
		panics   bool
		wantFail string // "" = no failed record
		wantRef  bool
		wantHand bool
		launched bool
		creates  int
	}
	createWith := func(tab Tab, err error, born ...Tab) func(l *Launch, f *fakeCmux) error {
		return func(l *Launch, f *fakeCmux) error {
			_, cerr := l.Create(func() (Tab, error) { f.creates++; f.add(born...); return tab, err })
			return cerr
		}
	}
	rows := []row{
		{name: "a fn error before Create", fn: func(*Launch, *fakeCmux) error { return errors.New("consume failed") }, wantFail: "consume failed"},
		{name: "b nil without Create", fn: func(*Launch, *fakeCmux) error { return nil }, wantFail: "ended before creation"},
		{name: "c tab returned then fn error", fn: func(l *Launch, f *fakeCmux) error {
			if err := createWith(nw, nil, nw)(l, f); err != nil {
				return err
			}
			return errors.New("bind failed")
		}, wantRef: true, wantHand: true, creates: 1},
		{name: "d ref without id, one new tab", fn: createWith(Tab{Ref: "workspace:9"}, errors.New("resolve failed"), nw), wantRef: true, wantHand: true, creates: 1},
		{name: "e create error, one new tab", fn: func(l *Launch, f *fakeCmux) error {
			tab, err := l.Create(func() (Tab, error) { f.creates++; f.add(nw); return Tab{}, errors.New("timeout") })
			if err != nil || tab.ID != "NEW" {
				t.Errorf("(e) Create = %+v %v, want the diffed tab and nil", tab, err)
			}
			return nil
		}, wantRef: true, wantHand: true, creates: 1},
		{name: "f create error, no new tab", fn: createWith(Tab{}, errors.New("refused")), wantFail: "cmux_create_failed", creates: 1},
		{name: "g two new tabs", fn: createWith(Tab{}, errors.New("timeout"), nw, nw2), wantFail: "several new tabs", creates: 1},
		{name: "h tabs error after", errAt: map[int]error{2: errors.New("socket gone")}, fn: createWith(Tab{}, errors.New("timeout"), nw), wantFail: "tab state unknown", creates: 1},
		{name: "i tabs error before", errAt: map[int]error{1: errors.New("socket gone")}, fn: createWith(nw, nil, nw), wantFail: "cmux_unreachable", creates: 0},
		{name: "j panic before Create", fn: func(*Launch, *fakeCmux) error { panic("boom") }, panics: true, wantFail: "panic: boom"},
		{name: "k panic in create after the tab", fn: func(l *Launch, f *fakeCmux) error {
			_, _ = l.Create(func() (Tab, error) { f.creates++; f.add(nw); panic("cmux crashed") })
			return nil
		}, panics: true, wantRef: true, wantHand: true, creates: 1},
		{name: "l Launched", fn: func(l *Launch, f *fakeCmux) error {
			tab, err := l.Create(func() (Tab, error) { f.creates++; f.add(nw); return nw, nil })
			if err != nil {
				return err
			}
			return l.Launched(Entry{WorkspaceID: tab.ID, WorkspaceRef: tab.Ref, PID: 77})
		}, wantRef: true, launched: true, creates: 1},
		{name: "m second Create", fn: func(l *Launch, f *fakeCmux) error {
			if _, err := l.Create(func() (Tab, error) { f.creates++; f.add(nw); return nw, nil }); err != nil {
				return err
			}
			if _, err := l.Create(func() (Tab, error) { f.creates++; return nw2, nil }); err == nil {
				t.Error("(m) second Create accepted")
			}
			return nil
		}, wantRef: true, wantHand: true, creates: 1},
	}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			r := newReg(t)
			f := &fakeCmux{tabs: []Tab{old}, errAt: rw.errAt}
			var hands []handoffRec
			req := ReserveRequest{SubjectID: "sub_a", Origin: "cli", Alive: aliveAll, Tabs: f.list,
				Handoff: func(id string, tab Tab, born time.Time) { hands = append(hands, handoffRec{id, tab, born}) }}
			var id string
			func() {
				defer func() {
					p := recover()
					if (p != nil) != rw.panics {
						t.Fatalf("panic = %v, want panic %v", p, rw.panics)
					}
				}()
				id, _ = r.WithReservation(req, func(l *Launch) error { id = l.ID; return rw.fn(l, f) })
			}()
			es, err := r.Entries()
			if err != nil {
				t.Fatal(err)
			}
			mine := byID(es, id)
			if len(mine) == 0 || mine[0].Kind != KindLaunchReserve || mine[0].OwnerPID != os.Getpid() || mine[0].Origin != "cli" {
				t.Fatalf("records = %+v, want a reserve first", mine)
			}
			failed, launched, refs := ends(mine)
			if rw.wantFail != "" {
				if failed != 1 || !strings.Contains(mine[len(mine)-1].Detail, rw.wantFail) {
					t.Fatalf("records = %+v, want one failed %q", mine, rw.wantFail)
				}
			} else if failed != 0 {
				t.Fatalf("records = %+v, want no failed", mine)
			}
			if (refs == 1) != rw.wantRef || refs > 1 {
				t.Fatalf("launch-ref count = %d, want %v (%+v)", refs, rw.wantRef, mine)
			}
			if (launched == 1) != rw.launched {
				t.Fatalf("launched = %d, want %v", launched, rw.launched)
			}
			if (len(hands) == 1) != rw.wantHand || len(hands) > 1 {
				t.Fatalf("handoffs = %+v, want %v", hands, rw.wantHand)
			}
			if rw.wantHand {
				h := hands[0]
				if h.id != id || h.tab.ID != "NEW" || h.tab.Ref != "workspace:9" || h.born.IsZero() {
					t.Fatalf("handoff = %+v", h)
				}
			}
			if f.creates != rw.creates {
				t.Fatalf("create called %d times, want %d", f.creates, rw.creates)
			}
			_, pending := Pending(es, "sub_a", time.Now(), PendingOpts{Alive: aliveAll})
			wantPending := rw.wantHand
			if pending != wantPending {
				t.Fatalf("Pending = %v, want %v", pending, wantPending)
			}
		})
	}
}

func TestWithReservationRefusesPending(t *testing.T) {
	r := newReg(t)
	now := time.Now().UTC()
	_ = r.Append(Entry{TS: now.Add(-time.Minute), Kind: KindLaunchReserve, ID: "ln_x", SubjectID: "sub_a", Origin: "api", OwnerPID: 1, Result: ResultPending})
	_ = r.Append(Entry{TS: now.Add(-30 * time.Second), Kind: KindLaunchRef, ID: "ln_x", SubjectID: "sub_a", WorkspaceID: "W", WorkspaceRef: "workspace:5", Result: ResultPending})
	called := false
	_, err := r.WithReservation(ReserveRequest{SubjectID: "sub_a", Alive: aliveAll}, func(*Launch) error { called = true; return nil })
	var pe *PendingError
	if !errors.As(err, &pe) || pe.Entry.Origin != "api" || pe.Entry.WorkspaceRef != "workspace:5" {
		t.Fatalf("err = %v, want *PendingError naming the api launch in workspace:5", err)
	}
	if called {
		t.Fatal("fn called")
	}
	if es, _ := r.Entries(); len(es) != 2 {
		t.Fatalf("written: %+v", es)
	}
}

func TestWithReservationSubjectless(t *testing.T) {
	r := newReg(t)
	_ = r.Append(Entry{TS: time.Now().UTC(), Kind: KindLaunchReserve, ID: "ln_p", SubjectID: "", OwnerPID: os.Getpid(), Result: ResultPending})
	for i := 0; i < 2; i++ {
		if _, err := r.WithReservation(ReserveRequest{Alive: aliveAll}, func(*Launch) error { return nil }); err != nil {
			t.Fatalf("subjectless reservation %d: %v", i, err)
		}
	}
	es, _ := r.Entries()
	reserves := 0
	for _, e := range es {
		if e.Kind == KindLaunchReserve && e.SubjectID == "" {
			reserves++
		}
	}
	if reserves != 3 {
		t.Fatalf("reserves = %d, want 3", reserves)
	}
	// The two closed ones (ended before creation, tabless) leave the burst; the open one stays.
	if n := len(Launches(es, time.Now().Add(-time.Hour), time.Now(), PendingOpts{Alive: aliveAll})); n != 1 {
		t.Fatalf("Launches = %d, want 1", n)
	}
	// While open, both successive reservations count.
	r2 := newReg(t)
	var inner int
	_, _ = r2.WithReservation(ReserveRequest{Alive: aliveAll}, func(*Launch) error {
		_, _ = r2.WithReservation(ReserveRequest{Alive: aliveAll}, func(*Launch) error {
			es, _ := r2.Entries()
			inner = len(Launches(es, time.Now().Add(-time.Hour), time.Now(), PendingOpts{Alive: aliveAll}))
			return nil
		})
		return nil
	})
	if inner != 2 {
		t.Fatalf("two open subjectless reservations: Launches = %d, want 2", inner)
	}
}

func TestWithReservationBurstUnderLock(t *testing.T) {
	r := newReg(t)
	now := time.Now().UTC()
	for i := 0; i < 9; i++ {
		_ = r.Append(Entry{TS: now.Add(-time.Minute), WorkspaceID: "W" + string(rune('A'+i)), Result: ResultLaunched})
	}
	check := func(es []Entry) error {
		if len(Launches(es, time.Now().Add(-10*time.Minute), time.Now(), PendingOpts{Alive: aliveAll})) >= 10 {
			return errors.New("burst")
		}
		return nil
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, refused int
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := Open(r.path).WithReservation(ReserveRequest{Alive: aliveAll, Check: check}, func(*Launch) error {
				time.Sleep(50 * time.Millisecond) // hold the launch open while the other one checks
				return nil
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil && err.Error() == "burst" {
				refused++
			} else {
				ok++
			}
		}()
	}
	close(start)
	wg.Wait()
	es, _ := r.Entries()
	reserves := 0
	for _, e := range es {
		if e.Kind == KindLaunchReserve {
			reserves++
		}
	}
	if reserves != 1 || refused != 1 {
		t.Fatalf("reserves = %d, refused = %d (ok %d), want exactly one reservation", reserves, refused, ok)
	}
}

func TestWithReservationRegistryUnreadable(t *testing.T) {
	r := newReg(t)
	if err := os.WriteFile(r.path, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := r.WithReservation(ReserveRequest{SubjectID: "s", Check: func([]Entry) error { called = true; return nil }}, func(*Launch) error { called = true; return nil })
	if !errors.Is(err, ErrRegistryUnreadable) || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
	if b, _ := os.ReadFile(r.path); string(b) != "{not json\n" {
		t.Fatalf("written: %q", b)
	}
}

func TestWithReservationAppliesDefaultCap(t *testing.T) {
	for _, c := range []struct {
		age     time.Duration
		pending bool
	}{{ReserveCap - time.Minute, true}, {ReserveCap + time.Minute, false}} {
		r := newReg(t)
		_ = r.Append(Entry{TS: time.Now().UTC().Add(-c.age), Kind: KindLaunchReserve, ID: "ln_old", SubjectID: "s", OwnerPID: 1, Result: ResultPending})
		_, err := r.WithReservation(ReserveRequest{SubjectID: "s", Alive: aliveAll}, func(*Launch) error { return nil })
		var pe *PendingError
		if errors.As(err, &pe) != c.pending {
			t.Fatalf("reserve %s old: err = %v, want pending %v", c.age, err, c.pending)
		}
	}
}

func TestReserveCapSingleSource(t *testing.T) {
	if ReserveCap != 15*time.Minute+ReserveMargin {
		t.Fatalf("ReserveCap = %s", ReserveCap)
	}
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "github.com/aloysbr/pigtail/internal/spawnreg") {
		t.Fatalf("go list read nothing: %s", out)
	}
	for _, dep := range []string{"internal/devserver", "internal/cmux"} {
		if strings.Contains(string(out), "github.com/aloysbr/pigtail/"+dep+"\n") {
			t.Fatalf("spawnreg depends on %s", dep)
		}
	}
}

func TestWithReservationRepeatsIdentity(t *testing.T) {
	nw := Tab{ID: "NEW", Ref: "workspace:9"}
	cases := map[string]func(l *Launch, f *fakeCmux) error{
		"c": func(l *Launch, f *fakeCmux) error {
			_, _ = l.Create(func() (Tab, error) { f.add(nw); return nw, nil })
			return errors.New("later")
		},
		"d": func(l *Launch, f *fakeCmux) error {
			_, err := l.Create(func() (Tab, error) { f.add(nw); return Tab{Ref: nw.Ref}, errors.New("resolve") })
			return err
		},
		"e": func(l *Launch, f *fakeCmux) error {
			_, err := l.Create(func() (Tab, error) { f.add(nw); return Tab{}, errors.New("timeout") })
			return err
		},
		"fail": func(l *Launch, f *fakeCmux) error {
			tab, _ := l.Create(func() (Tab, error) { f.add(nw); return nw, nil })
			return l.Fail("wrong account", tab)
		},
		"a": func(*Launch, *fakeCmux) error { return errors.New("before") },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			r := newReg(t)
			f := &fakeCmux{}
			var id string
			_, _ = r.WithReservation(ReserveRequest{SubjectID: "sub_a", Alive: aliveAll, Tabs: f.list, Handoff: func(string, Tab, time.Time) {}},
				func(l *Launch) error { id = l.ID; return fn(l, f) })
			es, _ := r.Entries()
			mine := byID(es, id)
			for _, e := range mine {
				if e.SubjectID != "sub_a" {
					t.Fatalf("record without SubjectID: %+v", e)
				}
				if e.Kind == KindLaunchRef && (e.WorkspaceID != "NEW" || e.WorkspaceRef != "workspace:9") {
					t.Fatalf("launch-ref = %+v", e)
				}
			}
			last := mine[len(mine)-1]
			switch name {
			case "fail":
				if last.Kind != KindLaunchResult || last.WorkspaceID != "NEW" || last.WorkspaceRef != "workspace:9" {
					t.Fatalf("failed = %+v", last)
				}
			case "a":
				if last.Kind != KindLaunchResult || last.WorkspaceID != "" || last.WorkspaceRef != "" {
					t.Fatalf("failed before creation = %+v", last)
				}
			}
			want := 1
			if last.WorkspaceID == "" {
				want = 0
			}
			if n := len(Launches(es, time.Now().Add(-time.Hour), time.Now(), PendingOpts{Alive: aliveAll})); n != want {
				t.Fatalf("Launches = %d, want %d", n, want)
			}
		})
	}
}

func TestReserveOnlyThroughWithReservation(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	write := regexp.MustCompile(`Kind\s*(:|=)\s*(spawnreg\.)?KindLaunchReserve\b`)
	seen := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen++
		if write.Match(b) && filepath.Base(path) != "reservation.go" {
			t.Errorf("%s writes a KindLaunchReserve record: only WithReservation (reservation.go) may", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 50 {
		t.Fatalf("walked %d Go files under %s: the scan read nothing", seen, root)
	}
	b, err := os.ReadFile("reservation.go")
	if err != nil || !write.Match(b) {
		t.Fatalf("positive control: reservation.go must write the reserve (%v)", err)
	}
}

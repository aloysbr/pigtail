package ports

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestReserveRefusesPendingAndOtherEnvLocal(t *testing.T) {
	self := writeEnv(t, "PORT=38030\n")
	other := writeEnv(t, "PORT=38031\n")
	a := New(fakeUsed{m: map[int]bool{38029: true, 38033: true}}, // 38029: self's own row; 38033: another row or a static server
		WithBindProbe(func(int) bool { return true }),
		WithEnvSources(fakeEnv{{Dir: self, Vars: []string{"PORT"}}, {Dir: other, Vars: []string{"PORT"}}}))

	var regErr *pigtail.APIError
	if err := a.Reserve("dev", 38033, self, 38029); !errors.As(err, &regErr) || regErr.Details["reason"] != "registered" {
		t.Fatalf("port registered elsewhere (row or static server): %v", err)
	}
	if err := a.Reserve("dev", 38030, self, 38029); err != nil {
		t.Fatalf("its own .env.local must not block a resync: %v", err)
	}
	if !a.Pending("dev")[38030] {
		t.Fatal("a reservation is pending until confirmed or released")
	}
	var apiErr *pigtail.APIError
	if err := a.Reserve("dev", 38030, other, 0); !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrConflict {
		t.Fatalf("pending port reserved twice: %v", err)
	}
	if err := a.Reserve("dev", 38031, self, 38029); !errors.As(err, &apiErr) || apiErr.Details["holder_path"] != other {
		t.Fatalf("port named in another live .env.local: %v", err)
	}
	a.Release("dev", 38030)
	if p, _ := a.Allocate(map[string]pigtail.PortRange{"dev": {38030, 38030}}, "dev"); p != 0 {
		// 38030 is still named in self's .env.local: Allocate (no self) must skip it.
		t.Fatalf("Allocate handed out %d, named in a live .env.local", p)
	}
}

// fakeRegistrants is a UsedLister that also names who registers a port.
type fakeRegistrants struct {
	used   map[int]bool
	ids    map[int][]string
	static map[int]bool
}

func (f fakeRegistrants) UsedPorts(string) (map[int]bool, error) { return f.used, nil }
func (f fakeRegistrants) PortRegistrants(_ string, port int) ([]string, bool, error) {
	return f.ids[port], f.static[port], nil
}

// ResyncBlockers is Reserve's own predicate, every refusal named: who
// registers the port (rows and static servers), whether it is pending, and
// every other .env.local naming it.
func TestResyncBlockersNamesEveryBlocker(t *testing.T) {
	self := writeEnv(t, "PORT=38030\n")
	h := writeEnv(t, "PORT=38033\n")
	x := writeEnv(t, "PORT=38033\n")
	a := New(fakeRegistrants{
		used:   map[int]bool{38033: true, 38034: true},
		ids:    map[int][]string{38033: {"ws_dead", "ws_h"}},
		static: map[int]bool{38034: true},
	}, WithBindProbe(func(int) bool { return true }),
		WithEnvSources(fakeEnv{{Dir: self, Vars: []string{"PORT"}}, {Dir: h, Vars: []string{"PORT"}}, {Dir: x, Vars: []string{"PORT"}}}))

	got, err := a.ResyncBlockers("dev", 38033, self, 38029)
	if err != nil {
		t.Fatal(err)
	}
	want := []Blocker{
		{Reason: BlockRegistered, WorkspaceID: "ws_dead"},
		{Reason: BlockRegistered, WorkspaceID: "ws_h"},
		{Reason: BlockEnvLocal, Dir: h},
		{Reason: BlockEnvLocal, Dir: x},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blockers %+v, want %+v", got, want)
	}
	if got, _ := a.ResyncBlockers("dev", 38034, self, 38029); !reflect.DeepEqual(got, []Blocker{{Reason: BlockRegistered, Static: true}}) {
		t.Fatalf("static server: %+v", got)
	}
	if got, _ := a.ResyncBlockers("dev", 38030, self, 38029); len(got) != 0 {
		t.Fatalf("its own .env.local and a free number block nothing: %+v", got)
	}
	if got, _ := a.ResyncBlockers("dev", 38033, self, 38033); len(got) != 2 {
		// selfRegistered == port: the registry rule is skipped, as in Reserve; the two .env.local remain.
		t.Fatalf("own registered port: %+v", got)
	}
	if err := a.Reserve("dev", 38040, self, 38029); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.ResyncBlockers("dev", 38040, x, 0); !reflect.DeepEqual(got, []Blocker{{Reason: BlockPending}}) {
		t.Fatalf("pending: %+v", got)
	}
}

// Asking never reserves: a port asked about stays allocatable.
func TestResyncBlockersLeavesNothingPending(t *testing.T) {
	a := New(fakeUsed{m: map[int]bool{}}, WithBindProbe(func(int) bool { return true }))
	if _, err := a.ResyncBlockers("dev", 38050, "/nowhere", 0); err != nil {
		t.Fatal(err)
	}
	if len(a.Pending("dev")) != 0 {
		t.Fatalf("ResyncBlockers left %v pending", a.Pending("dev"))
	}
}

// A UsedLister that cannot name registrants still yields one anonymous
// "registered" blocker — never a false "free".
func TestResyncBlockersAnonymousRegistrant(t *testing.T) {
	a := New(fakeUsed{m: map[int]bool{38060: true}}, WithBindProbe(func(int) bool { return true }))
	got, err := a.ResyncBlockers("dev", 38060, "/nowhere", 0)
	if err != nil || !reflect.DeepEqual(got, []Blocker{{Reason: BlockRegistered}}) {
		t.Fatalf("blockers %+v err %v", got, err)
	}
}

// countingRegistrants counts the registry scans; countingEnv the .env.local listings.
type countingRegistrants struct {
	fakeRegistrants
	used, regs *int
}

func (c countingRegistrants) UsedPorts(k string) (map[int]bool, error) {
	*c.used++
	return c.fakeRegistrants.UsedPorts(k)
}
func (c countingRegistrants) PortRegistrants(k string, p int) ([]string, bool, error) {
	*c.regs++
	return c.fakeRegistrants.PortRegistrants(k, p)
}

type countingEnv struct {
	fakeEnv
	n    *int
	hook func()
}

func (c countingEnv) EnvSources() ([]EnvSource, error) {
	*c.n++
	if c.hook != nil {
		c.hook()
	}
	return c.fakeEnv.EnvSources()
}

// Review focus 7: GET /servers asks every question of a request at once —
// EnvSources (and every .env.local) once, UsedPorts once per kind,
// PortRegistrants once per blocked (kind, port) — with the same answers as
// one ResyncBlockers per question.
func TestResyncBlockersAllReadsEachSourceOnce(t *testing.T) {
	self := writeEnv(t, "PORT=38030\n")
	h := writeEnv(t, "PORT=38033\n")
	var used, regs, envs int
	reg := fakeRegistrants{used: map[int]bool{38033: true}, ids: map[int][]string{38033: {"ws_h"}}}
	a := New(countingRegistrants{reg, &used, &regs}, WithBindProbe(func(int) bool { return true }),
		WithEnvSources(countingEnv{fakeEnv: fakeEnv{{Dir: self, Vars: []string{"PORT"}}, {Dir: h, Vars: []string{"PORT"}}}, n: &envs}))
	qs := []ResyncQuery{
		{Kind: "dev", Port: 38033, Self: self, SelfRegistered: 38029},
		{Kind: "dev", Port: 38031, Self: self, SelfRegistered: 38029},
		{Kind: "storybook", Port: 38033, Self: self, SelfRegistered: 0},
	}
	got, err := a.ResyncBlockersAll(qs)
	if err != nil {
		t.Fatal(err)
	}
	if used != 2 || envs != 1 || regs != 2 {
		t.Fatalf("reads: UsedPorts %d (want 2, one per kind), EnvSources %d (want 1), PortRegistrants %d (want 2, one per blocked kind:port)", used, envs, regs)
	}
	for _, q := range qs {
		one, err := a.ResyncBlockers(q.Kind, q.Port, q.Self, q.SelfRegistered)
		if err != nil || !reflect.DeepEqual(got[q], one) {
			t.Fatalf("%+v: batch %+v, alone %+v (err %v)", q, got[q], one, err)
		}
	}
}

// The batch reads the registry and the .env.local files without holding the
// allocator's mutex: a slow GET /servers never holds up an allocation.
func TestResyncBlockersAllReadsOutsideTheLock(t *testing.T) {
	var a *Allocator
	var envs int
	locked := false
	a = New(fakeUsed{m: map[int]bool{}}, WithBindProbe(func(int) bool { return true }),
		WithEnvSources(countingEnv{fakeEnv: fakeEnv{}, n: &envs, hook: func() {
			if a.mu.TryLock() {
				a.mu.Unlock()
				return
			}
			locked = true
		}}))
	if _, err := a.ResyncBlockersAll([]ResyncQuery{{Kind: "dev", Port: 38070, Self: "/nowhere"}}); err != nil {
		t.Fatal(err)
	}
	if envs != 1 || locked {
		t.Fatalf("EnvSources called %d time(s), under the allocator's lock: %v", envs, locked)
	}
}

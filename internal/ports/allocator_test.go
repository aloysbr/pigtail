package ports

import (
	"errors"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type fakeUsed struct{ m map[int]bool }

func (f fakeUsed) UsedPorts(_ string) (map[int]bool, error) { return f.m, nil }

func TestAllocateDistinct(t *testing.T) {
	a := New(fakeUsed{m: map[int]bool{}})
	ranges := map[string]pigtail.PortRange{"dev": {38000, 38099}}

	got := map[int]bool{}
	for i := 0; i < 3; i++ {
		p, err := a.Allocate(ranges, "dev")
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		if got[p] {
			t.Fatalf("duplicate port %d", p)
		}
		got[p] = true
	}
}

func TestAllocateSkipsUsed(t *testing.T) {
	used := fakeUsed{m: map[int]bool{38000: true, 38001: true}}
	a := New(used)
	ranges := map[string]pigtail.PortRange{"dev": {38000, 38099}}
	p, err := a.Allocate(ranges, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if p < 38002 {
		t.Fatalf("expected port >= 38002, got %d", p)
	}
}

func TestAllocateExhausted(t *testing.T) {
	a := New(fakeUsed{m: map[int]bool{38500: true}})
	ranges := map[string]pigtail.PortRange{"dev": {38500, 38500}}
	_, err := a.Allocate(ranges, "dev")
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	var apiErr *pigtail.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrPortExhausted {
		t.Fatalf("expected PORT_EXHAUSTED, got %v", err)
	}
}

func TestAllocateUnknownKind(t *testing.T) {
	a := New(fakeUsed{m: map[int]bool{}})
	_, err := a.Allocate(map[string]pigtail.PortRange{}, "dev")
	if err == nil {
		t.Fatal("expected error for missing kind")
	}
}

// One listener snapshot per Allocate, however many candidates are tried:
// the probe must never fork lsof per port.
func TestAllocateTakesOneListenerSnapshot(t *testing.T) {
	calls := 0
	a := New(fakeUsed{m: map[int]bool{}},
		WithBindProbe(func(int) bool { return true }),
		WithListenerSnapshot(func() (map[int]bool, error) {
			calls++
			return map[int]bool{38000: true, 38001: true, 38002: true}, nil // e.g. 0.0.0.0, ::, a LAN IP
		}))
	p, err := a.Allocate(map[string]pigtail.PortRange{"dev": {38000, 38099}}, "dev")
	if err != nil || p != 38003 {
		t.Fatalf("got %d (%v), want 38003: 38000-38002 are in the snapshot", p, err)
	}
	if calls != 1 {
		t.Fatalf("%d listener snapshots for one Allocate, want 1", calls)
	}
}

func TestAllocateUsesInjectedBindProbe(t *testing.T) {
	a := New(fakeUsed{m: map[int]bool{}}, WithBindProbe(func(p int) bool { return p != 38000 }))
	p, err := a.Allocate(map[string]pigtail.PortRange{"dev": {38000, 38099}}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if p != 38001 {
		t.Fatalf("got %d, want 38001 (38000 refused by the probe)", p)
	}
}

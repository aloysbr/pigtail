package cmuxsync

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
)

// The pill's text, verbatim (G2).
const wantInputDead = "entrée morte · pigtail cmux check"

func TestInputDeadValueVerbatim(t *testing.T) {
	t.Parallel()
	if inputDeadValue != wantInputDead {
		t.Fatalf("value %q", inputDeadValue)
	}
}

// inputHarness: wsA (surfA1) and wsC (surfC1) live; dead lists the surfaces
// whose input the daemon's probe found dead, defects the last check's.
func inputHarness(t *testing.T) (*harness, *[]string, *[]CheckDefect) {
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsC, "c")
	h.world.openSurface(wsA, surfA1)
	h.world.openSurface(wsC, surfC1)
	var dead []string
	var defects []CheckDefect
	h.r.deps.DeadInputs = func() []string { return dead }
	h.r.deps.CmuxCheck = func() ([]CheckDefect, error) { return defects, nil }
	return h, &dead, &defects
}

func TestRunnerInputDeadOnItsWorkspaceOnly(t *testing.T) {
	t.Parallel()
	h, dead, _ := inputHarness(t)
	*dead = []string{strings.ToLower(surfA1)}
	h.step(0)
	if !hasCall(h.world.writes(), "set-status "+wsA+" pigtail-chk "+inputDeadValue) {
		t.Fatalf("writes %q", h.world.writes())
	}
	if _, ok := h.world.status[wsC][KeyCmuxCheck]; ok {
		t.Fatal("pill on a workspace whose input is alive")
	}
}

func TestRunnerInputDeadClearedWhenSetEmpties(t *testing.T) {
	t.Parallel()
	h, dead, _ := inputHarness(t)
	*dead = []string{surfA1}
	h.step(0)
	*dead = nil
	h.world.calls = nil
	h.step(time.Second)
	if !hasCall(h.world.writes(), "clear-status "+wsA+" pigtail-chk") {
		t.Fatalf("writes %q, want the pill cleared", h.world.writes())
	}
}

func TestInputDeadTextHasNoSessionNorTitle(t *testing.T) {
	t.Parallel()
	e := checkStatus(CheckPill{InputDead: 1})
	if e.Value != inputDeadValue {
		t.Fatalf("value %q", e.Value)
	}
	for _, leak := range []string{"sid", "S-", surfA1, "alpha"} {
		if strings.Contains(e.Value, leak) {
			t.Fatalf("value %q names %q", e.Value, leak)
		}
	}
}

func TestInputDeadUsesExistingCheckStyle(t *testing.T) {
	t.Parallel()
	e := checkStatus(CheckPill{InputDead: 2})
	if e.Key != KeyCmuxCheck || e.Icon != checkIcon || e.Priority != 85 || e.Color != waittype.Orange.Color() {
		t.Fatalf("entry %+v", e)
	}
}

func TestOwnedKeysUnchangedByInputDead(t *testing.T) {
	t.Parallel()
	want := []string{KeyAttention, KeyServers, KeyMaintenance, KeyOffTopic, KeyCmuxCheck, KeyPaused, KeyRole}
	if !reflect.DeepEqual(OwnedKeys, want) {
		t.Fatalf("OwnedKeys %v", OwnedKeys)
	}
}

func TestInputDeadSurfaceNotInAnyWorkspaceLightsNothing(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: wsA, Surfaces: map[string]bool{surfA1: true}}}
	if got := inputDeadPills([]string{"FFFF0000-0000-4000-8000-0000000000F1", ""}, ws); len(got) != 0 {
		t.Fatalf("pills %v", got)
	}
	if got := inputDeadPills([]string{strings.ToLower(surfA1)}, ws); got[wsA] != 1 {
		t.Fatalf("pills %v", got)
	}
}

func TestInputDeadTextWinsOverCheckCount(t *testing.T) {
	t.Parallel()
	h, dead, defects := inputHarness(t)
	*dead = []string{surfA1}
	*defects = []CheckDefect{{SurfaceID: surfA1}}
	h.step(0)
	if e := h.world.status[wsA][KeyCmuxCheck]; e.Value != inputDeadValue {
		t.Fatalf("pill %+v, want the dead-input text", e)
	}
	n := 0
	for _, c := range h.world.writes() {
		if strings.HasPrefix(c, "set-status "+wsA+" pigtail-chk") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("writes %q, want one pigtail-chk entry", h.world.writes())
	}
}

func TestInputDeadAloneReachesStatus(t *testing.T) {
	t.Parallel()
	st := (Desired{Check: &CheckPill{Left: 0, InputDead: 1}}).Statuses()
	if e, ok := st[KeyCmuxCheck]; !ok || e.Value != inputDeadValue {
		t.Fatalf("statuses %+v", st)
	}
}

func TestInputDeadOrangeAloneRedWithBlockedDefect(t *testing.T) {
	t.Parallel()
	if e := checkStatus(CheckPill{InputDead: 1}); e.Color != waittype.Orange.Color() {
		t.Fatalf("alone: %+v", e)
	}
	if e := checkStatus(CheckPill{InputDead: 1, Left: 1, Blocked: true}); e.Color != waittype.Red.Color() || e.Value != inputDeadValue {
		t.Fatalf("with a blocked defect: %+v", e)
	}
}

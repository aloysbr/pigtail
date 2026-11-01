package cmuxsync

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/waittype"
)

// D4: the session refused by the freeze reads "Blocked · freeze" on its
// workspace, whatever it shows otherwise (here Your turn), counted once.
func TestFrozenSessionIsBlocked(t *testing.T) {
	t.Parallel()
	w := Workspace{ID: wsA, WindowID: "W", Surfaces: surfaces("S-a", "S-b")}
	in := Input{Now: t0, Workspaces: []Workspace{w}, Sessions: []Session{yourTurn("a")}, Frozen: map[string]bool{"a": true}}
	if e := Compute(in).Desired[wsA].Statuses()[KeyAttention]; e.Value != "Blocked · freeze" || e.Color != waittype.Red.Color() || e.Icon != "hand.raised.fill" {
		t.Fatalf("pill = %+v", e)
	}
	// D8 within one session: a Permission dialog of the frozen session outranks Blocked.
	perm := waitingSess("p", "waiting")
	perm.SurfaceID = "S-a"
	perm.Auto = auto(waittype.Permission, "Bash", t0)
	if e := Compute(Input{Now: t0, Workspaces: []Workspace{w}, Sessions: []Session{perm}, Frozen: map[string]bool{"p": true}}).Desired[wsA].Statuses()[KeyAttention]; e.Value != "Permission · Bash" {
		t.Fatalf("Permission + frozen: %+v", e)
	}
	in.Sessions = append(in.Sessions, declared("b", "idle", waittype.Arbitrage, "d"))
	if e := Compute(in).Desired[wsA].Statuses()[KeyAttention]; e.Value != "Blocked · freeze" {
		t.Fatalf("Blocked beats Decision: %+v", e)
	}
	in.Frozen = nil // maintenance lifted
	if e := Compute(in).Desired[wsA].Statuses()[KeyAttention]; e.Value != "Decision ×2" {
		t.Fatalf("after the lift: %+v", e)
	}
}

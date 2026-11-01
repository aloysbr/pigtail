package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
)

// A session in Standby shows no inferred Your turn (2026-10-16, the user,
// sub_x000000000000041): idle with a turn seen and nothing declared reads
// nothing. A failure and a declared or held wait stay shown as before; a
// session outside Standby is unchanged.
func TestStandbyHidesInferredYourTurn(t *testing.T) {
	t.Parallel()
	turnEnded := func(standby bool) Session {
		s := waitingSess("s1", "idle")
		s.BusySeen = true
		s.Standby = standby
		return s
	}
	pill := func(s Session) string { return computeOne(t, s).Statuses()[KeyAttention].Value }

	if v := pill(turnEnded(false)); v != "Your turn" {
		t.Errorf("outside Standby: pill %q, want Your turn", v)
	}
	if v := pill(turnEnded(true)); v != "" {
		t.Errorf("in Standby: pill %q, want none", v)
	}

	declared := turnEnded(true)
	declared.Declared = Wait{Type: waittype.Review, Reason: "PR #1", Since: t0.Add(-time.Minute)}
	if waits, typ, _ := waitOf(declared, time.UTC); !waits || typ != waittype.Review {
		t.Errorf("in Standby, declared review: waitOf %v %q", waits, typ)
	}

	held := turnEnded(true)
	held.Held = heldMac()
	if v := pill(held); v != "Waiting for Mac" {
		t.Errorf("in Standby, held Mac: pill %q", v)
	}

	for _, ty := range []waittype.Type{waittype.Limit, waittype.Login, waittype.APIError} {
		f := failed("s1", ty, time.Time{}, "")
		f.Standby = true
		if waits, typ, _ := waitOf(f, time.UTC); !waits || typ != ty {
			t.Errorf("in Standby, failure %q: waitOf %v %q", ty, waits, typ)
		}
	}

	dialog := waitingSess("s1", "waiting")
	dialog.Standby = true
	if waits, _, _ := waitOf(dialog, time.UTC); !waits {
		t.Error("in Standby, a dialog open: no wait")
	}
}

// The loop reads Deps.Standby each pass: a session put in Standby loses its
// inferred Your turn, and gets it back when its Standby ends.
func TestRunnerStandbyHidesYourTurn(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.Standby = func() (map[string]bool, error) {
		rows, err := h.st.ListStandby()
		if err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.SessionID] = true
		}
		return out, nil
	}
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0) // the loop sees it busy
	h.nativeWait("s1", "idle", "", h.now.Add(time.Second))
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("turn ended: pill %q, want Your turn", p)
	}
	if err := h.st.SetStandby("s1", h.now); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	h.step(WaitingHold + time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("in Standby: pill %q, want none", p)
	}
	if _, err := h.st.ClearStandby("s1", h.now); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("Standby ended: pill %q, want Your turn", p)
	}
}

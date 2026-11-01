package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
)

func declared(id, status string, ty waittype.Type, reason string) Session {
	s := waitingSess(id, status)
	s.SurfaceID = "S-" + id
	s.Declared = Wait{Type: ty, Reason: reason, Since: t0.Add(-time.Minute)}
	return s
}

func yourTurn(id string) Session {
	s := waitingSess(id, "idle")
	s.SurfaceID = "S-" + id
	s.BusySeen = true
	return s
}

func deadOn(id string) Session { return sess(id, wsA, "S-"+id, "busy", false) }

func pillOf(t *testing.T, ss ...Session) string {
	t.Helper()
	w := Workspace{ID: wsA, WindowID: "W", Surfaces: surfaces("S-a", "S-b", "S-c", "S-d")}
	d := Compute(Input{Now: t0, Workspaces: []Workspace{w}, Sessions: ss}).Desired[wsA]
	e := d.Statuses()[KeyAttention]
	if e.Value == "" {
		return ""
	}
	return e.Value + " " + e.Color
}

func TestAggregationByGravity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ss   []Session
		want string
	}{
		{"A15 Dead beats orange", []Session{deadOn("a"), declared("b", "idle", waittype.Question, "q")}, "Dead #FF3B30"},
		{"A16 Your turn beats Review", []Session{yourTurn("a"), declared("b", "idle", waittype.Review, "r")}, "Your turn #FF9500"},
		{"A16 Decision + Your turn: Decision ×2", []Session{declared("a", "idle", waittype.Arbitrage, "d"), yourTurn("b")}, "Decision ×2 #FF9500"},
		{"A17 Review + Done: two greens, Review ×2 (D15)", []Session{declared("a", "idle", waittype.Review, "PR 9"), declared("b", "idle", waittype.Done, "")}, "Review ×2 #34C759"},
		{"A30 Question + Your turn: ×2", []Session{declared("a", "idle", waittype.Question, "q"), yourTurn("b")}, "Question ×2 #FF9500"},
		{"A30 Question + Done: reason kept", []Session{declared("a", "idle", waittype.Question, "indice IRL"), declared("b", "idle", waittype.Done, "")}, "Question · indice IRL #FF9500"},
		{"A31 Waiting for you + Your turn", []Session{waitingSess("a", "waiting"), yourTurn("b")}, "Waiting for you ×2 #FF9500"},
		{"A31 Decision + Waiting for you", []Session{declared("a", "idle", waittype.Arbitrage, "d"), func() Session { s := waitingSess("b", "waiting"); s.SurfaceID = "S-b"; return s }()}, "Decision ×2 #FF9500"},
		{"A9 Permission + Blocked + Review + busy", []Session{
			func() Session {
				s := waitingSess("a", "waiting")
				s.SurfaceID = "S-a"
				s.Auto = auto(waittype.Permission, "Bash", t0)
				return s
			}(),
			declared("b", "idle", waittype.Blocked, "login"),
			declared("c", "idle", waittype.Review, "PR"),
			func() Session { s := waitingSess("d", "busy"); s.SurfaceID = "S-d"; return s }(),
		}, "Permission ×2 #FF3B30"},
		{"A9 note: Review + Blocked → Blocked · reason", []Session{declared("a", "idle", waittype.Review, "PR"), declared("b", "idle", waittype.Blocked, "login")}, "Blocked · login #FF3B30"},
		{"Permission + Dead: the dead surface counts red", []Session{func() Session {
			s := waitingSess("a", "waiting")
			s.SurfaceID = "S-a"
			s.Auto = auto(waittype.Permission, "", t0)
			return s
		}(), deadOn("b")}, "Permission ×2 #FF3B30"},
		{"A8 Done with a reason", []Session{declared("a", "idle", waittype.Done, "PR #31 mergée")}, "Done · PR #31 mergée #34C759"},
		{"A8 Done without a reason, no done command set", []Session{declared("a", "idle", waittype.Done, "")}, "Done #34C759"},
		{"A28 Wrapped grey", []Session{declared("a", "idle", waittype.Wrapped, "")}, "Wrapped #8E8E93"},
		{"Wrapped never shows its reason", []Session{declared("a", "idle", waittype.Wrapped, "x")}, "Wrapped #8E8E93"},
		{"Done + Wrapped: Done", []Session{declared("a", "idle", waittype.Done, "x"), declared("b", "idle", waittype.Wrapped, "")}, "Done · x #34C759"},
		{"Working blue, with its reason", []Session{declared("a", "idle", waittype.Working, "CI #9205")}, "Background · CI #9205 #0A84FF"},
		{"Working without a reason", []Session{declared("a", "idle", waittype.Working, "")}, "Background #0A84FF"},
		{"Working is not shown mid-turn", []Session{declared("a", "busy", waittype.Working, "CI")}, ""},
		{"Your turn beats Working", []Session{declared("a", "idle", waittype.Working, "CI"), yourTurn("b")}, "Your turn #FF9500"},
		{"Done beats Working", []Session{declared("a", "idle", waittype.Working, "CI"), declared("b", "idle", waittype.Done, "x")}, "Done · x #34C759"},
		{"Working beats Wrapped: blue over grey, counted alone", []Session{declared("a", "idle", waittype.Working, "CI"), declared("b", "idle", waittype.Wrapped, "")}, "Background · CI #0A84FF"},
		{"A7 Review green", []Session{declared("a", "idle", waittype.Review, "PR #142 ready")}, "Review · PR #142 ready #34C759"},
		{"A4 Decision orange", []Session{declared("a", "idle", waittype.Arbitrage, "D1–D6 layout reset")}, "Decision · D1–D6 layout reset #FF9500"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pillOf(t, c.ss...); got != c.want {
				t.Fatalf("pill = %q, want %q", got, c.want)
			}
		})
	}
}

// A3: Dead is red, with the filled octagon (D13).
func TestDeadPillIsRedFilled(t *testing.T) {
	t.Parallel()
	w := Workspace{ID: wsA, WindowID: "W", Surfaces: surfaces("S-a")}
	e := Compute(Input{Now: t0, Workspaces: []Workspace{w}, Sessions: []Session{deadOn("a")}}).Desired[wsA].Statuses()[KeyAttention]
	if e.Value != "Dead" || e.Icon != "xmark.octagon.fill" || e.Color != "#FF3B30" || e.Priority != 90 {
		t.Fatalf("pill = %+v", e)
	}
}

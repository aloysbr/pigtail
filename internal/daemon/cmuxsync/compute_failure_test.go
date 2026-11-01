package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
)

var paris = time.FixedZone("CEST", 2*3600)

func failed(id string, ty waittype.Type, until time.Time, cell string) Session {
	s := waitingSess(id, "idle")
	s.SurfaceID = "S-" + id
	s.BusySeen = true // without the failure it would read Your turn
	s.CellID = cell
	s.Auto = Wait{Type: ty, Since: t0.Add(-time.Second), Until: until}
	return s
}

func pillAt(t *testing.T, loc *time.Location, ss ...Session) string {
	t.Helper()
	w := Workspace{ID: wsA, WindowID: "W", Surfaces: surfaces("S-a", "S-b", "S-c")}
	e := Compute(Input{Now: t0, Location: loc, Workspaces: []Workspace{w}, Sessions: ss}).Desired[wsA].Statuses()[KeyAttention]
	if e.Value == "" {
		return ""
	}
	return e.Value + " " + e.Icon + " " + e.Color
}

func TestFailurePills(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) // 02:00 in Paris
	cases := []struct {
		name string
		ss   []Session
		want string
	}{
		{"D2 Limit with its reset, local 24 h", []Session{failed("a", waittype.Limit, reset, "mp")}, "Limit · 02:00 gauge.with.dots.needle.0percent #FF3B30"},
		{"D2 Limit without a reset", []Session{failed("a", waittype.Limit, time.Time{}, "mp")}, "Limit gauge.with.dots.needle.0percent #FF3B30"},
		{"D2 Limit ×n", []Session{failed("a", waittype.Limit, reset, "mp"), failed("b", waittype.Limit, reset, "jt")}, "Limit ×2 gauge.with.dots.needle.0percent #FF3B30"},
		{"D3 Login · cell", []Session{failed("a", waittype.Login, time.Time{}, "it")}, "Login · it person.crop.circle.badge.exclamationmark #FF3B30"},
		{"D9 Login alone without a cell", []Session{failed("a", waittype.Login, time.Time{}, "")}, "Login person.crop.circle.badge.exclamationmark #FF3B30"},
		{"D3 API error orange", []Session{failed("a", waittype.APIError, time.Time{}, "t")}, "API error exclamationmark.triangle.fill #FF9500"},
		{"D8 Limit beats Decision", []Session{failed("a", waittype.Limit, reset, "mp"), declared("b", "idle", waittype.Arbitrage, "d")}, "Limit · 02:00 gauge.with.dots.needle.0percent #FF3B30"},
		{"D8 Blocked beats Limit", []Session{failed("a", waittype.Limit, reset, "mp"), declared("b", "idle", waittype.Blocked, "x")}, "Blocked ×2 hand.raised.fill #FF3B30"},
		{"D8 Limit beats Dead", []Session{failed("a", waittype.Limit, reset, "mp"), deadOn("b")}, "Limit ×2 gauge.with.dots.needle.0percent #FF3B30"},
		{"D8 API error beats Your turn", []Session{failed("a", waittype.APIError, time.Time{}, "t"), yourTurn("b")}, "API error ×2 exclamationmark.triangle.fill #FF9500"},
		{"D8 Decision beats API error", []Session{failed("a", waittype.APIError, time.Time{}, "t"), declared("b", "idle", waittype.Arbitrage, "d")}, "Decision ×2 arrow.triangle.branch #FF9500"},
		{"same session: Limit beats its declared Working", []Session{func() Session {
			s := failed("a", waittype.Limit, reset, "mp")
			s.Declared = Wait{Type: waittype.Working, Since: t0.Add(-time.Hour)}
			return s
		}()}, "Limit · 02:00 gauge.with.dots.needle.0percent #FF3B30"},
		{"same session D8: declared Blocked + Limit → Blocked", []Session{func() Session {
			s := failed("a", waittype.Limit, reset, "mp")
			s.Declared = Wait{Type: waittype.Blocked, Reason: "login", Since: t0.Add(-time.Hour)}
			return s
		}()}, "Blocked · login hand.raised.fill #FF3B30"},
		{"same session D8: Limit beats its declared Decision", []Session{func() Session {
			s := failed("a", waittype.Limit, reset, "mp")
			s.Declared = Wait{Type: waittype.Arbitrage, Reason: "d", Since: t0.Add(-time.Hour)}
			return s
		}()}, "Limit · 02:00 gauge.with.dots.needle.0percent #FF3B30"},
		{"not shown mid-turn", []Session{func() Session { s := failed("a", waittype.Limit, reset, "mp"); s.Status = "busy"; return s }()}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pillAt(t, paris, c.ss...); got != c.want {
				t.Fatalf("pill = %q, want %q", got, c.want)
			}
		})
	}
}

// A9: the API error pill carries the retry's reason, nothing before a retry.
func TestFailureReasonAPIErrorShowsRetryReason(t *testing.T) {
	withReason := func(reason string) Session {
		s := failed("a", waittype.APIError, time.Time{}, "t")
		s.Auto.Reason = reason
		return s
	}
	for _, c := range []struct{ reason, want string }{
		{"relance à 14:05 (2/5)", "API error · relance à 14:05 (2/5) exclamationmark.triangle.fill #FF9500"},
		// The network reason is worded to fit pillMaxRunes (36) whole.
		{"relance au retour réseau", "API error · relance au retour réseau exclamationmark.triangle.fill #FF9500"},
		// Over 36 runes the pill keeps the label alone rather than cut the reason.
		{"relance au retour du réseau", "API error exclamationmark.triangle.fill #FF9500"},
		{"relances épuisées", "API error · relances épuisées exclamationmark.triangle.fill #FF9500"},
		{"", "API error exclamationmark.triangle.fill #FF9500"},
	} {
		if got := pillAt(t, paris, withReason(c.reason)); got != c.want {
			t.Errorf("reason %q: pill = %q, want %q", c.reason, got, c.want)
		}
	}
}

// m20: a held series shows "relance en attente".
func TestFailureReasonAPIErrorShowsHeldReason(t *testing.T) {
	s := failed("a", waittype.APIError, time.Time{}, "t")
	s.Auto.Reason = "relance en attente"
	if got, want := pillAt(t, paris, s), "API error · relance en attente exclamationmark.triangle.fill #FF9500"; got != want {
		t.Fatalf("pill = %q, want %q", got, want)
	}
}

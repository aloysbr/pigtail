package cmuxsync

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// respawnPill is the attention pill of wsA (value, icon, colour) under one
// respawn mark.
func respawnPill(t *testing.T, m respawnMark, ss ...Session) (string, string, string) {
	t.Helper()
	w := Workspace{ID: wsA, WindowID: "W", Surfaces: surfaces("S1", "S2", "S-a", "S-b")}
	d := Compute(Input{Now: t0, Workspaces: []Workspace{w}, Sessions: ss, Respawn: map[string]respawnMark{wsA: m}}).Desired[wsA]
	e := d.Statuses()[KeyAttention]
	return e.Value, e.Icon, e.Color
}

func waitingMark(reason string) respawnMark {
	return respawnMark{state: pigtail.CmuxRespawnWaiting, reason: reason, surface: "S1", pid: 7000}
}

// D16 / D16-bis: a held respawn reads "Respawn · <reason>" in orange under
// its own icon, on the run's workspace.
func TestComputeRespawnWaitingPill(t *testing.T) {
	t.Parallel()
	v, icon, colour := respawnPill(t, waitingMark("draft in the input"), sess("s1", wsA, "S1", "idle", true))
	if v != "Respawn · draft in the input" || icon != "arrow.clockwise.circle.fill" || colour != waittype.Orange.Color() {
		t.Fatalf("pill = %q %q %q", v, icon, colour)
	}
}

// The held session's own turn ended: its computed Your turn is the respawn,
// not a second orange (no "Respawn ×2"). Another surface's still counts.
func TestComputeRespawnHidesTheSurfaceYourTurn(t *testing.T) {
	t.Parallel()
	own := sess("s1", wsA, "S1", "idle", true)
	own.BusySeen = true
	if v, _, _ := respawnPill(t, waitingMark("draft in the input"), own); v != "Respawn · draft in the input" {
		t.Fatalf("own surface: pill = %q", v)
	}
	other := sess("s2", wsA, "S2", "idle", true)
	other.BusySeen = true
	if v, _, _ := respawnPill(t, waitingMark("draft in the input"), own, other); v != "Respawn ×2" {
		t.Fatalf("another surface's Your turn: pill = %q", v)
	}
}

// A real state outranks it: a red wins, a more severe orange counts it.
func TestComputeRespawnLosesToARealState(t *testing.T) {
	t.Parallel()
	blocked := declared("b", "idle", waittype.Blocked, "login")
	if v, _, c := respawnPill(t, waitingMark("background work"), blocked); v != "Blocked · login" || c != waittype.Red.Color() {
		t.Fatalf("red: pill = %q %s", v, c)
	}
	q := declared("a", "idle", waittype.Question, "q")
	if v, _, _ := respawnPill(t, waitingMark("background work"), q); v != "Question ×2" {
		t.Fatalf("question: pill = %q", v)
	}
}

// D2: an abandoned respawn is one orange Respawn · abandoned.
func TestComputeRespawnAbandonedIsOrange(t *testing.T) {
	t.Parallel()
	own := sess("s1", wsA, "S1", "idle", true)
	own.BusySeen = true
	v, icon, c := respawnPill(t, respawnMark{state: pigtail.CmuxRespawnAbandoned, surface: "S1"}, own)
	if v != "Respawn · abandoned" || icon != "arrow.clockwise.circle.fill" || c != waittype.Orange.Color() {
		t.Fatalf("pill = %q %q %q", v, icon, c)
	}
}

// A failed respawn stays one red Blocked.
func TestComputeRespawnFailedIsBlocked(t *testing.T) {
	t.Parallel()
	own := sess("s1", wsA, "S1", "idle", true)
	own.BusySeen = true
	v, icon, c := respawnPill(t, respawnMark{state: pigtail.CmuxRespawnFailed, surface: "S1"}, own)
	if v != "Blocked · respawn failed" || icon != "hand.raised.fill" || c != waittype.Red.Color() {
		t.Fatalf("pill = %q %q %q", v, icon, c)
	}
}

// An abandoned respawn stands in for its surface's Your turn.
func TestComputeRespawnAbandonedHidesTheSurfaceYourTurn(t *testing.T) {
	t.Parallel()
	m := respawnMark{state: pigtail.CmuxRespawnAbandoned, surface: "S1"}
	own := sess("s1", wsA, "S1", "idle", true)
	own.BusySeen = true
	if v, _, _ := respawnPill(t, m, own); v != "Respawn · abandoned" {
		t.Fatalf("own surface: pill = %q", v)
	}
	other := sess("s2", wsA, "S2", "idle", true)
	other.BusySeen = true
	if v, _, _ := respawnPill(t, m, own, other); v != "Respawn ×2" {
		t.Fatalf("another surface's Your turn: pill = %q", v)
	}
}

// Nothing was closed by an abandoned respawn: a death there is a real Dead.
func TestComputeRespawnAbandonedLetsTheDeadSurfaceShow(t *testing.T) {
	t.Parallel()
	dead := sess("s1", wsA, "S1", "idle", false)
	v, _, c := respawnPill(t, respawnMark{state: pigtail.CmuxRespawnAbandoned, surface: "S1"}, dead)
	if v != "Dead" || c != waittype.Red.Color() {
		t.Fatalf("pill = %q %s, want a red Dead", v, c)
	}
}

// r1·3: a failed respawn killed the session of its surface; with no
// SessionEnd it reads dead — the same fact as the red pill, not a second one.
func TestComputeRespawnFailedAbsorbsTheDeadSurface(t *testing.T) {
	t.Parallel()
	dead := sess("s1", wsA, "S1", "idle", false)
	if v, _, _ := respawnPill(t, respawnMark{state: pigtail.CmuxRespawnFailed, surface: "S1"}, dead); v != "Blocked · respawn failed" {
		t.Fatalf("pill = %q, want the failed pill alone", v)
	}
	// Another surface's dead session still counts.
	other := sess("s2", wsA, "S2", "idle", false)
	if v, _, _ := respawnPill(t, respawnMark{state: pigtail.CmuxRespawnFailed, surface: "S1"}, dead, other); v != "Blocked ×2" {
		t.Fatalf("with another dead surface: pill = %q", v)
	}
}

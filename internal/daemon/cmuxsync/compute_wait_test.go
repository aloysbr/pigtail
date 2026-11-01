package cmuxsync

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
)

// waitingSess is a live session in wsA whose native file says status since
// t0 (NativeStatusAt), the moment the dialog opened or the turn ended.
func waitingSess(id, status string) Session {
	s := sess(id, wsA, "S1", status, true)
	s.NativeStatusAt = t0
	return s
}

func auto(t waittype.Type, reason string, since time.Time) Wait {
	return Wait{Type: t, Reason: reason, Since: since}
}

func computeOne(t *testing.T, ss ...Session) Desired {
	t.Helper()
	return Compute(Input{Now: t0, Workspaces: []Workspace{live(wsA)}, Sessions: ss}).Desired[wsA]
}

// Every type of the closed list, told by a hook while the dialog is open,
// becomes the pill: its label, its icon, its colour.
func TestComputeTypedWaitEveryType(t *testing.T) {
	t.Parallel()
	for _, ty := range []waittype.Type{waittype.Permission, waittype.Question, waittype.Arbitrage, waittype.Review, waittype.Blocked} {
		s := waitingSess("s1", "waiting")
		s.Auto = auto(ty, "", t0.Add(-100*time.Millisecond))
		d := computeOne(t, s)
		if d.Attention != AttentionWaiting || d.WaitType != ty {
			t.Fatalf("%s: attention %q type %q", ty, d.Attention, d.WaitType)
		}
		e := d.Statuses()[KeyAttention]
		if e.Value != ty.Label() || e.Icon != ty.Icon() || e.Color != ty.Color() || e.Priority != attentionPriority {
			t.Errorf("%s: pill = %+v", ty, e)
		}
	}
}

// A declared wait shows while the session is idle (the agent handed back) or
// waiting, and hides while it works.
func TestComputeDeclaredWait(t *testing.T) {
	t.Parallel()
	decl := auto(waittype.Arbitrage, "Sidebar pigtail", t0.Add(-time.Minute))
	cases := []struct {
		status   string
		want     Attention
		wantType waittype.Type
	}{
		{"idle", AttentionWaiting, waittype.Arbitrage},
		{"waiting", AttentionWaiting, waittype.Arbitrage},
		{"busy", AttentionNone, waittype.Unknown},
	}
	for _, c := range cases {
		s := waitingSess("s1", c.status)
		s.Declared = decl
		d := computeOne(t, s)
		if d.Attention != c.want || d.WaitType != c.wantType {
			t.Errorf("declared while %s: %q %q, want %q %q", c.status, d.Attention, d.WaitType, c.want, c.wantType)
		}
	}
	s := waitingSess("s1", "idle")
	s.Declared = decl
	if got := computeOne(t, s).Statuses()[KeyAttention].Value; got != "Decision · Sidebar pigtail" {
		t.Errorf("pill = %q", got)
	}
	// A dead session waits for nothing.
	s.Alive = false
	if d := computeOne(t, s); d.Attention == AttentionWaiting {
		t.Errorf("a dead session's declaration still shows")
	}
}

// An automatic wait shows only while the native status is "waiting": a hook
// that fired but whose end was lost never leaves a pill behind.
func TestComputeAutoWaitOnlyWhileWaiting(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"idle", "busy"} {
		s := waitingSess("s1", status)
		s.Auto = auto(waittype.Permission, "Bash", t0.Add(-time.Second))
		if d := computeOne(t, s); d.Attention != AttentionNone {
			t.Errorf("auto wait while %s: %q %q", status, d.Attention, d.WaitType)
		}
	}
}

// The automatic wait (a dialog that blocks) beats a declaration while it lasts.
func TestComputeAutoBeatsDeclared(t *testing.T) {
	t.Parallel()
	s := waitingSess("s1", "waiting")
	s.Declared = auto(waittype.Review, "PR #9", t0.Add(-time.Minute))
	s.Auto = auto(waittype.Permission, "Bash", t0)
	if d := computeOne(t, s); d.WaitType != waittype.Permission || d.WaitReason != "Bash" {
		t.Errorf("got %q %q, want the permission", d.WaitType, d.WaitReason)
	}
}

// An automatic wait recorded long before the current wait started belongs to
// an earlier dialog whose end the loop missed: not this wait's type.
func TestComputeStaleAutoIgnored(t *testing.T) {
	t.Parallel()
	s := waitingSess("s1", "waiting")
	s.Auto = auto(waittype.Permission, "Bash", t0.Add(-AutoWaitSkew-time.Second))
	if d := computeOne(t, s); d.WaitType != waittype.Unknown || d.Attention != AttentionWaiting {
		t.Errorf("stale auto: %q %q", d.Attention, d.WaitType)
	}
}

// No hook typed it: the native waitingFor refines the pill when it is
// telling, else the generic "Waiting for you".
func TestComputeWaitingForFallback(t *testing.T) {
	t.Parallel()
	for wf, want := range map[string]string{
		"sandbox request": "Permission",
		"goal proposal":   "Review",
		"input needed":    "Waiting for you",
		"dialog open":     "Waiting for you",
		"":                "Waiting for you",
	} {
		s := waitingSess("s1", "waiting")
		s.WaitingFor = wf
		if got := computeOne(t, s).Statuses()[KeyAttention].Value; got != want {
			t.Errorf("waitingFor %q: pill %q, want %q", wf, got, want)
		}
	}
	// A hook-typed wait beats the loose native hint.
	s := waitingSess("s1", "waiting")
	s.WaitingFor = "goal proposal"
	s.Auto = auto(waittype.Question, "", t0)
	if d := computeOne(t, s); d.WaitType != waittype.Question {
		t.Errorf("hook vs waitingFor: %q", d.WaitType)
	}
}

// Several sessions wait: the most urgent type, then " ×n"; no reason (it
// belongs to one session only).
func TestComputeWaitPriorityAcrossSessions(t *testing.T) {
	t.Parallel()
	a := waitingSess("a", "idle")
	a.Declared = auto(waittype.Blocked, "login", t0.Add(-time.Minute))
	b := sess("b", wsA, "S2", "waiting", true)
	b.NativeStatusAt = t0
	b.Auto = auto(waittype.Question, "Date lib", t0)
	c := sess("c", wsA, "S3", "waiting", true)
	c.NativeStatusAt = t0
	c.Auto = auto(waittype.Permission, "Bash", t0)
	d := computeOne(t, a, b, c)
	if d.WaitType != waittype.Permission || d.WaitingCount != 2 {
		t.Fatalf("got %q ×%d", d.WaitType, d.WaitingCount)
	}
	// ×n counts the winner's colour: two reds, the orange Question aside.
	if got := d.Statuses()[KeyAttention].Value; got != "Permission ×2" {
		t.Errorf("pill = %q", got)
	}
	// An unknown wait (orange) ranks below Blocked (red), and is not counted.
	u := sess("u", wsA, "S2", "waiting", true)
	if d := computeOne(t, a, u); d.WaitType != waittype.Blocked || d.WaitingCount != 1 {
		t.Errorf("blocked + unknown: %q ×%d", d.WaitType, d.WaitingCount)
	} else if got := d.Statuses()[KeyAttention].Value; got != "Blocked · login" {
		t.Errorf("blocked + unknown: pill %q", got)
	}
}

// The reason shows only when the whole pill fits; else the type alone.
func TestPillReasonOnlyWhenItFits(t *testing.T) {
	t.Parallel()
	d := liveDesired(waittype.Review, 1, "PR #142 ready")
	if got := d.Statuses()[KeyAttention].Value; got != "Review · PR #142 ready" {
		t.Errorf("short reason: %q", got)
	}
	d.WaitReason = strings.Repeat("long ", 20)
	if got := d.Statuses()[KeyAttention].Value; got != "Review" {
		t.Errorf("long reason: %q", got)
	}
	// A reason never reads as a flag to cmux, nor spans lines.
	d.WaitReason = "-x\nnext"
	if got := d.Statuses()[KeyAttention].Value; strings.Contains(got, "\n") || strings.Contains(got, "· -") {
		t.Errorf("unsafe reason rendered: %q", got)
	}
}

// The hysteresis holds the type with the pill: an answered permission does
// not blink to "Waiting for you" during WaitingHold.
func TestDamperHoldsTheWaitType(t *testing.T) {
	t.Parallel()
	var dm Damper
	dm.Apply(t0, map[string]Desired{wsA: liveDesired(waittype.Permission, 1, "Bash")})
	held := map[string]Desired{wsA: {WorkspaceID: wsA}}
	dm.Apply(t0.Add(time.Second), held)
	if d := held[wsA]; d.Attention != AttentionWaiting || d.WaitType != waittype.Permission || d.WaitReason != "Bash" {
		t.Fatalf("held: %+v", d)
	}
	gone := map[string]Desired{wsA: {WorkspaceID: wsA}}
	dm.Apply(t0.Add(time.Second+WaitingHold), gone)
	if d := gone[wsA]; d.Attention != AttentionNone || d.WaitType != waittype.Unknown {
		t.Fatalf("after the hold: %+v", d)
	}
}

// D1/D5/D10, D24: an idle session the loop saw busy since its start (or its
// resume) and that declared nothing reads "Your turn"; one never seen busy
// reads nothing, whatever its native timestamps say — Claude moves
// statusUpdatedAt 100–450 ms after startedAt with no prompt (B1; A6, A11,
// A18).
func TestYourTurnNeedsAFinishedTurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		busySeen bool
		want     Attention
	}{
		{"seen busy since the start, now idle", true, AttentionWaiting},
		{"never seen busy: launched or resumed, statusUpdatedAt = startedAt + 300 ms", false, AttentionNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := waitingSess("s1", "idle")
			s.BusySeen = c.busySeen
			d := computeOne(t, s)
			if d.Attention != c.want {
				t.Fatalf("attention = %q, want %q", d.Attention, c.want)
			}
			if c.want == AttentionWaiting {
				e := d.Statuses()[KeyAttention]
				if e.Value != "Your turn" || e.Icon != "ellipsis.bubble.fill" || e.Color != "#FF9500" || e.Priority != 90 {
					t.Fatalf("pill = %+v", e)
				}
			}
		})
	}
	// A declaration wins over Your turn; busy shows nothing.
	s := waitingSess("s1", "idle")
	s.BusySeen = true
	s.Declared = auto(waittype.Review, "PR", t0.Add(-time.Second))
	if d := computeOne(t, s); d.WaitType != waittype.Review {
		t.Fatalf("declared review shows %q", d.WaitType)
	}
	b := waitingSess("s2", "busy")
	b.BusySeen = true
	if d := computeOne(t, b); d.Attention != AttentionNone {
		t.Fatalf("busy shows %q", d.Attention)
	}
}

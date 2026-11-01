package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
)

// TestSweepReplay20261030OneAlert replays, on a synthesised fixture, the
// Pilote's 2026-10-30 timeline measured on the real transcript: a wakeup
// armed 11:06:11 for 11:31:00 never fired; human turns, an idle pause and a
// relaunch followed; the next arm came at 15:57:57. One alert, one recovery,
// across a daemon restart at 12:20:30 (the arm then lies > 256 KiB from the
// end of the transcript).
func TestSweepReplay20261030OneAlert(t *testing.T) {
	prevLocal := time.Local // reasons print local times; the timeline is UTC
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = prevLocal })
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	at := func(h, m, s int) time.Time {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
	}
	r := newSweepRoleRig(t, at(11, 6, 30))
	r.role("perso", "pilote", "sub-pilote", at(11, 6, 0))
	r.session("sid-pilote", "sub-pilote", 100, true)

	type turn struct{ from, to time.Time }
	turns := []turn{
		{at(11, 15, 46), at(11, 16, 23)}, {at(11, 21, 33), at(11, 22, 28)}, {at(11, 27, 35), at(11, 28, 37)},
		{at(11, 28, 59), at(11, 29, 44)}, {at(11, 30, 51), at(11, 31, 11)}, {at(11, 33, 9), at(11, 34, 1)},
		{at(12, 6, 49), at(12, 7, 47)}, {at(12, 9, 34), at(12, 9, 49)}, {at(15, 55, 22), at(15, 58, 22)},
	}
	type ev struct {
		at    time.Time
		lines []string
	}
	pad := strings.Repeat("x", 4000)
	var events []ev
	events = append(events, ev{at(11, 6, 11), []string{rsArm(at(11, 6, 11), at(11, 31, 0))}})
	for _, tn := range turns {
		events = append(events, ev{tn.from, []string{rsUser(tn.from)}})
		if tn.from.Equal(at(11, 33, 9)) { // 400 KiB of turn lines between the arm and 12:09
			mid := at(11, 33, 30)
			var filler []string
			for i := 0; i < 100; i++ {
				filler = append(filler, fmt.Sprintf(`{"type":"assistant","timestamp":"%s","message":"%s"}`, rsTS(mid), pad))
			}
			events = append(events, ev{mid, filler})
		}
		if tn.from.Equal(at(15, 55, 22)) {
			events = append(events, ev{at(15, 57, 57), []string{rsArm(at(15, 57, 57), at(16, 23, 0))}})
		}
		events = append(events, ev{tn.to, []string{rsAsst(tn.to)}})
	}
	busy := func(now time.Time) bool {
		for _, tn := range turns {
			if !now.Before(tn.from) && now.Before(tn.to) {
				return true
			}
		}
		return false
	}

	var alertAt, recoveredAt time.Time
	next := 0
	snaps := map[string]struct{ state, reason string }{}
	for now := at(11, 6, 30); !now.After(at(16, 10, 30)); now = now.Add(time.Minute) {
		// The rig acts on the world up to now.
		for next < len(events) && !events[next].at.After(now) {
			r.clock = events[next].at
			r.write("sid-pilote", events[next].at, events[next].lines...)
			next++
		}
		r.clock = now
		switch {
		case now.Equal(at(13, 10, 30)): // idle pause: ended, Standby
			r.setLive("sid-pilote", 100, "sub-pilote", false)
			if err := r.st.EnterStandby("sid-pilote", at(13, 10, 0)); err != nil {
				t.Fatal(err)
			}
		case now.Equal(at(15, 55, 30)): // relaunch at 15:55:00, same session id; Standby row kept
			r.clock = at(15, 55, 0)
			r.setLive("sid-pilote", 100, "sub-pilote", true)
			r.clock = now
			if _, err := r.st.ClearStandby("sid-pilote", at(15, 55, 22)); err != nil { // Ada' prompt
				t.Fatal(err)
			}
		}
		if r.alive["sid-pilote"] {
			if busy(now) {
				r.status("sid-pilote", "busy")
			} else {
				r.status("sid-pilote", "idle")
			}
		}
		if now.Equal(at(12, 20, 30)) {
			r.newSweep() // a daemon restart: fresh sweep and fresh reader
		}
		nA, nR := len(r.rows("role-alert")), len(r.rows("role-recovered"))
		r.sw.pass()
		if len(r.rows("role-alert")) > nA && alertAt.IsZero() {
			alertAt = now
		}
		if len(r.rows("role-recovered")) > nR && recoveredAt.IsZero() {
			recoveredAt = now
		}
		for _, k := range []time.Time{at(12, 20, 30), at(13, 30, 30), at(15, 56, 30)} {
			if now.Equal(k) {
				s := r.sw.Snapshot()[0]
				snaps[k.Format("15:04")] = struct{ state, reason string }{s.State, s.Reason}
			}
		}
	}

	al, rec := r.rows("role-alert"), r.rows("role-recovered")
	key := at(11, 31, 0)
	if len(al) != 1 || al[0].ID != roles.AlertID("pilote", key) {
		t.Fatalf("alerts = %+v", al)
	}
	if !alertAt.Equal(at(11, 41, 30)) {
		t.Fatalf("alert written at %s, want 11:41:30", alertAt.Format("15:04:05"))
	}
	if len(rec) != 1 || rec[0].ID != roles.RecoveredID("pilote", key) {
		t.Fatalf("recovered = %+v", rec)
	}
	if !recoveredAt.Equal(at(15, 58, 30)) {
		t.Fatalf("recovered written at %s, want 15:58:30", recoveredAt.Format("15:04:05"))
	}
	if all, err := r.st.ListJournal(store.JournalFilter{Scope: scope.Any, Limit: 500}); err != nil {
		t.Fatal(err)
	} else {
		for _, e := range all {
			if strings.HasPrefix(e.Kind, "role-") && e.Kind != "role-alert" && e.Kind != "role-recovered" {
				t.Fatalf("unexpected role row %+v", e)
			}
		}
		if n := len(all); n != 2 {
			t.Fatalf("journal rows = %d, want 2", n)
		}
	}
	if s := snaps["12:20"]; s.state != "silent" || s.reason != "wakeup due 11:31 never fired" {
		t.Fatalf("12:20:30 (fresh sweep) = %+v", s)
	}
	if s := snaps["13:30"]; s.state != "absent" {
		t.Fatalf("13:30:30 = %+v", s)
	}
	if s := snaps["15:56"]; s.state != "ok" || s.reason != "busy" {
		t.Fatalf("15:56:30 = %+v", s)
	}
}

package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestSidebarEventScope(t *testing.T) {
	scopetest.Three(t)
	for _, c := range []struct {
		subject string
		reason  bool // told by a session (false: none, or pigtail's own)
		wait    string
		want    string
	}{
		{"", false, "", "globex"},       // unbound, no session reason: the default scope, as work today
		{"globex", false, "", "globex"}, // no reason, or a system one (trust folder, respawn): workspace scope, as today
		{"acme", false, "", "acme"},
		{"acme", true, "acme", "acme"},     // base: tagged perso, acme loses its own event
		{"globex", true, "gx", "globex"},   // alias: same scope
		{"globex", true, "perso", "perso"}, // reason told by a session globex may not see
		{"acme", true, "globex", "perso"},  // mixed: master only
		{"", true, "acme", "perso"},
		{"globex", true, "", "perso"}, // D17: reason of an unscoped session = master only
		{"", true, "", "perso"},       // base: tagged work, every work reader got the words
	} {
		if got := sidebarEventScope(c.subject, c.reason, c.wait); got != c.want {
			t.Errorf("sidebarEventScope(%q, %v, %q) = %q, want %q", c.subject, c.reason, c.wait, got, c.want)
		}
	}
}

// bindGlobex creates a Globex/ theme (thm_ts) and subject (sub_ts) and
// binds ws to it.
func bindGlobex(t *testing.T, h *nativeHarness, ws string) {
	t.Helper()
	now := time.UnixMilli(1).UTC()
	if err := h.st.CreateTheme(&pigtail.Theme{ID: "thm_ts", Name: "ts", Kind: "project", VaultLink: "Globex/x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.CreateSubject(&pigtail.Subject{ID: "sub_ts", ThemeID: "thm_ts", Title: "ts", Slug: "ts", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: "sub_ts", Source: "attach", BoundAt: h.now}); err != nil {
		t.Fatal(err)
	}
}

// lastSidebarEvent is the last cmux.sidebar_changed the runner published.
func lastSidebarEvent(h *nativeHarness) *pigtail.Event {
	var last *pigtail.Event
	for i := range h.events {
		if h.events[i].Type == pigtail.EventCmuxSidebarChanged {
			last = &h.events[i]
		}
	}
	return last
}

// A reason pigtail tells itself (trust folder, respawn) names no session: D17
// does not touch it. Its event keeps the workspace's scope, as today — a
// globex cell keeps the event of its own workspace.
func TestRunnerSystemReasonKeepsWorkspaceScope(t *testing.T) {
	scopetest.Three(t)
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	bindGlobex(t, h, wsA) // thm_ts (Globex/x) → sub_ts, wsA bound to sub_ts
	h.world.add(winA, wsA, "fresh")
	h.r.TrustPending(wsA, 5000)
	h.step(0)
	last := lastSidebarEvent(h)
	if last == nil || last.Data["wait_reason"] != "trust folder" || last.Scope != "globex" {
		t.Fatalf("event = %+v, want the trust reason tagged globex (the workspace's subject)", last)
	}
	if w := h.r.State().Workspaces[0]; w.WaitFromSession {
		t.Fatal("trust folder marked as a session's reason")
	}
}

// Same for a final respawn (respawn failed): pigtail's own reason.
func TestRunnerSystemReasonRespawnFailedKeepsWorkspaceScope(t *testing.T) {
	scopetest.Three(t)
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return false }
	bindGlobex(t, h, wsA)
	h.world.add(winA, wsA, "a")
	h.nativeOn("s2", wsA, rsSurface, 4343, "busy", h.now.Add(-time.Hour))
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	last := lastSidebarEvent(h)
	if last == nil || last.Data["wait_reason"] != "respawn failed" || last.Scope != "globex" {
		t.Fatalf("event = %+v, want the respawn reason tagged globex", last)
	}
	if w := h.r.State().Workspaces[0]; w.WaitFromSession {
		t.Fatal("respawn failed marked as a session's reason")
	}
}

// Same for a respawn still waiting: its reason is pigtail's own.
func TestRunnerSystemReasonRespawnWaitingKeepsWorkspaceScope(t *testing.T) {
	scopetest.Three(t)
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	bindGlobex(t, h, wsA)
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "idle", h.now.Add(-time.Minute))
	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitDraft))
	h.step(0)
	last := lastSidebarEvent(h)
	if last == nil || last.Data["wait_reason"] == "" || last.Data["wait_type"] != "respawn" || last.Scope != "globex" {
		t.Fatalf("event = %+v, want the respawn reason tagged globex", last)
	}
	if w := h.r.State().Workspaces[0]; w.WaitFromSession {
		t.Fatal("a waiting respawn marked as a session's reason")
	}
}

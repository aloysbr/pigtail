package cmuxsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// nativeWait rewrites the session's native file: status since at, with the
// loose waitingFor hint.
func (h *nativeHarness) nativeWait(id, status, waitingFor string, at time.Time) {
	h.t.Helper()
	b, _ := json.Marshal(claudesession.Native{PID: h.pid, SessionID: id, Status: status, WaitingFor: waitingFor,
		Kind: "interactive", StatusUpdatedAt: at.UnixMilli()})
	path := filepath.Join(h.cfg, "sessions", strconv.Itoa(h.pid)+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		h.t.Fatal(err)
	}
	_ = os.Chtimes(path, at, at)
}

// nativeTurn rewrites the native file with a startedAt: status since at.
func (h *nativeHarness) nativeTurn(id, status string, started, at time.Time) {
	h.t.Helper()
	b, _ := json.Marshal(claudesession.Native{PID: h.pid, SessionID: id, Status: status, Kind: "interactive",
		StartedAt: started.UnixMilli(), StatusUpdatedAt: at.UnixMilli()})
	path := filepath.Join(h.cfg, "sessions", strconv.Itoa(h.pid)+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		h.t.Fatal(err)
	}
	_ = os.Chtimes(path, at, at)
}

func (h *nativeHarness) wait(id, typ, reason, source string) {
	h.t.Helper()
	if err := h.st.SetSessionWait(id, pigtail.SessionWait{Type: typ, Reason: reason, Source: source, Since: h.now}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *nativeHarness) pill() string { return h.world.status[wsA][KeyAttention].Value }

func (h *nativeHarness) waits(id string) []pigtail.SessionWait {
	ws, _ := h.st.SessionWaits(id)
	return ws
}

// The agent declares a decision mid-turn: hidden while it works, shown once
// it hands back, gone at the next turn — and dropped from the store.
func TestRunnerDeclaredWaitLifecycle(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "arbitrage", "short title", pigtail.WaitSourceDeclared)
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("shown while the agent works: %q", p)
	}
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Decision · short title" {
		t.Fatalf("pill after handing back = %q", p)
	}
	st := h.r.State()
	if len(st.Workspaces) != 1 || st.Workspaces[0].WaitType != "arbitrage" || st.Workspaces[0].WaitReason != "short title" {
		t.Fatalf("state = %+v", st.Workspaces)
	}
	h.step(time.Hour) // a decision waits as long as it takes
	if p := h.pill(); p != "Decision · short title" {
		t.Fatalf("pill after an hour = %q", p)
	}
	h.nativeWait("s1", "busy", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if len(h.waits("s1")) != 0 {
		t.Fatalf("declared wait kept after the next turn: %+v", h.waits("s1"))
	}
	h.step(WaitingHold + time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("pill kept: %q", p)
	}
}

// A hook typed the dialog: the pill names it while it is open; answered, the
// pill goes and the automatic wait is dropped.
func TestRunnerAutoWaitLifecycle(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "permission", "Bash", pigtail.WaitSourceAuto)
	h.nativeWait("s1", "waiting", "", h.now.Add(50*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Permission · Bash" {
		t.Fatalf("pill = %q", p)
	}
	if e := h.world.status[wsA][KeyAttention]; e.Icon != "lock.fill" || e.Color != "#FF3B30" {
		t.Fatalf("icon/colour = %+v", e)
	}
	h.nativeWait("s1", "busy", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if len(h.waits("s1")) != 0 {
		t.Fatalf("auto wait kept: %+v", h.waits("s1"))
	}
}

// No hook (cell not equipped): the native waitingFor refines the pill.
func TestRunnerWaitingForFallback(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.nativeWait("s1", "waiting", "goal proposal", h.now)
	h.step(0)
	h.step(time.Second)
	if p := h.pill(); p != "Review" {
		t.Fatalf("pill = %q", p)
	}
}

// The event names the type and the reason, and is tagged perso when the
// reason comes from a perso session even in an unbound workspace.
func TestRunnerPublishesWaitType(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "idle", h.now.Add(-time.Minute))
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", Scope: "perso"}); err != nil {
		t.Fatal(err)
	}
	h.wait("s1", "review", "PR #9", pigtail.WaitSourceDeclared)
	h.nativeWait("s1", "idle", "", h.now.Add(time.Second))
	h.step(0)
	h.step(time.Second)
	var last *pigtail.Event
	for i := range h.events {
		if h.events[i].Type == pigtail.EventCmuxSidebarChanged {
			last = &h.events[i]
		}
	}
	if last == nil || last.Data["wait_type"] != "review" || last.Data["wait_reason"] != "PR #9" || last.Scope != "perso" {
		t.Fatalf("event = %+v", last)
	}
	st := h.r.State()
	if st.Workspaces[0].WaitScope != "perso" {
		t.Fatalf("view scope = %q", st.Workspaces[0].WaitScope)
	}
}

// startShape writes what Claude Code 2.1.288 really leaves at start and at
// --resume with no prompt (measured on 5 sessions, verify N2 round 1, B1):
// idle, statusUpdatedAt 100–450 ms after startedAt — no turn ran.
func (h *nativeHarness) startShape(id string, started time.Time) {
	h.t.Helper()
	h.nativeTurn(id, "idle", started, started.Add(300*time.Millisecond))
}

// D24: Your turn needs a turn the loop saw. A fresh session whose
// statusUpdatedAt moved 300 ms after its startedAt is not one (B1); once the
// loop observed it busy, the next idle reads Your turn, kept however long
// nobody answers (A12), gone within WaitingHold of the next busy (A13).
func TestRunnerYourTurnLifecycle(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	start := h.now.Add(-time.Minute)
	h.native("s1", wsA, "idle", start)
	h.startShape("s1", start) // launched, nothing typed (A11)
	h.step(0)
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("fresh session, no turn: pill %q (B1)", p)
	}
	h.nativeTurn("s1", "busy", start, h.now.Add(500*time.Millisecond))
	h.step(time.Second) // the loop sees it busy
	h.nativeTurn("s1", "idle", start, h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("after a turn: %q", p)
	}
	h.step(time.Hour) // A12: nothing but an answer turns it off
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("after an hour: %q", p)
	}
	h.nativeTurn("s1", "busy", start, h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	h.step(WaitingHold + time.Second) // A13: gone within 3 s of busy
	if p := h.pill(); p != "" {
		t.Fatalf("busy: pill kept %q", p)
	}
}

// D24 / A18: --resume rewrites startedAt (and statusUpdatedAt 300 ms later)
// with no prompt: a new instance, no turn seen yet, no pill — even though
// the session ran turns before.
func TestRunnerYourTurnResetOnResume(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	start := h.now.Add(-time.Hour)
	h.native("s1", wsA, "busy", start)
	h.nativeTurn("s1", "busy", start, h.now)
	h.step(0)
	h.nativeTurn("s1", "idle", start, h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("before the resume: %q", p)
	}
	resumed := h.now.Add(time.Minute)
	h.startShape("s1", resumed)
	h.step(2 * time.Minute)
	h.step(WaitingHold + time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("resumed, no prompt: pill %q", p)
	}
}

// D24: the observation is memory only. A daemon restarted (fresh Runner)
// over an idle session that ran turns long ago shows nothing until its next
// turn (accepted).
func TestRunnerYourTurnAfterDaemonRestart(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	start := h.now.Add(-time.Hour)
	h.native("s1", wsA, "idle", start)
	h.nativeTurn("s1", "idle", start, h.now.Add(-time.Minute)) // turns ran before the restart
	h.step(0)
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("fresh runner, no busy seen: pill %q", p)
	}
}

// A14: Done declared, the next prompt clears it, then Your turn at the end
// of that turn.
func TestRunnerDoneThenNextTurn(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	start := h.now.Add(-time.Minute)
	h.native("s1", wsA, "busy", start)
	h.step(0)
	h.wait("s1", "done", "", pigtail.WaitSourceDeclared)
	h.nativeTurn("s1", "idle", start, h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Done" {
		t.Fatalf("done declared: %q", p)
	}
	h.nativeTurn("s1", "busy", start, h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	h.step(WaitingHold + time.Second)
	if p := h.pill(); p != "" || len(h.waits("s1")) != 0 {
		t.Fatalf("next turn: pill %q, waits %+v", p, h.waits("s1"))
	}
	h.nativeTurn("s1", "idle", start, h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("end of that turn: %q", p)
	}
}

// A rate limit read from the store (hook → relay → session_waits) shows on the
// idle session instead of Your turn, with the cell's login, and ends at its reset.
func TestRunnerLimitPillSurvivesAndEnds(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.nativeWait("s1", "idle", "", h.now.Add(200*time.Millisecond))
	until := h.now.Add(time.Hour)
	if err := h.st.SetSessionWait("s1", pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: h.now, Until: &until}); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	if p := h.pill(); p != "Limit · "+until.In(time.Local).Format("15:04") {
		t.Fatalf("pill = %q", p)
	}
	h.step(time.Hour + time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("after the reset the session is back to an ordinary idle: %q", p)
	}
	if ws := h.waits("s1"); len(ws) != 0 {
		t.Fatalf("waits left in the store: %+v", ws)
	}
}

// D4 end to end: the session a freeze refused reads a red "Blocked · freeze"
// until the lift, then its own state again.
func TestRunnerFrozenSessionPill(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	dir := t.TempDir()
	st := maintenance.State{ID: "mnt_r", On: true, Freeze: true}
	h.r.deps.Maintenance = func() (maintenance.State, bool, error) { return st, true, nil }
	h.r.deps.FrozenSessions = func() map[string]bool { return maintenance.FrozenSessions(dir, st) }
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("before the refusal: %q", p)
	}
	if err := maintenance.MarkFrozen(dir, "s1", st.ID); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	if e := h.world.status[wsA][KeyAttention]; e.Value != "Blocked · freeze" || e.Color != "#FF3B30" {
		t.Fatalf("refused session pill = %+v", e)
	}
	st = maintenance.State{ID: "mnt_r"}
	if err := maintenance.ClearFrozen(dir); err != nil {
		t.Fatal(err)
	}
	h.step(WaitingHold + time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("after the lift: %q", p)
	}
}

// The state GET /cmux/sidebar serves carries the cells of the workspace's live
// sessions (with their scopes, for the handler's masking) and the failure type.
func TestRunnerStateCarriesCellsAndFailureType(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: wsA, CmuxSurfaceID: "S-s1",
		PID: h.pid, ConfigDir: h.cfg, Status: "busy", CellID: "h1", Scope: "perso", UpdatedAt: h.now}); err != nil {
		t.Fatal(err)
	}
	h.step(0)
	h.nativeWait("s1", "idle", "", h.now.Add(200*time.Millisecond))
	until := h.now.Add(time.Hour)
	if err := h.st.SetSessionWait("s1", pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: h.now, Until: &until}); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	ws := h.r.State().Workspaces
	if len(ws) != 1 {
		t.Fatalf("workspaces = %+v", ws)
	}
	w := ws[0]
	if !reflect.DeepEqual(w.Cells, []string{"h1"}) || !reflect.DeepEqual(w.CellScopes, []string{"perso"}) {
		t.Fatalf("cells = %v / %v", w.Cells, w.CellScopes)
	}
	if w.WaitType != "limit" || w.WaitReason != until.In(time.Local).Format("15:04") {
		t.Fatalf("wait = %q %q", w.WaitType, w.WaitReason)
	}
}

// D17: the reason of a session with no scope is told to the master only — the
// sidebar event is tagged master, and the view keeps WaitScope "" so that
// GET /cmux/sidebar hides the words from every other caller.
func TestRunnerUnscopedWaitTagsMaster(t *testing.T) {
	scopetest.Three(t)
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "idle", h.now.Add(-time.Minute))
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", Scope: ""}); err != nil {
		t.Fatal(err)
	}
	h.wait("s1", "review", "PR #9", pigtail.WaitSourceDeclared)
	h.nativeWait("s1", "idle", "", h.now.Add(time.Second))
	h.step(0)
	h.step(time.Second)
	var last *pigtail.Event
	for i := range h.events {
		if h.events[i].Type == pigtail.EventCmuxSidebarChanged {
			last = &h.events[i]
		}
	}
	if last == nil || last.Data["wait_reason"] != "PR #9" || last.Scope != "perso" {
		t.Fatalf("event = %+v, want the reason tagged perso (master)", last)
	}
}

// Verify #178: the real case of a StopFailure — the turn ended on the failure,
// the native file reads idle (a stale cmux "Running" is not read) — shows each
// failure's pill with its gravity colour, a perso session's Login included.
func TestRunnerFailurePillOnTheIdleSession(t *testing.T) {
	t.Parallel()
	until := newNativeHarness(t, on()).now.Add(2 * time.Hour) // resetsAt = now+2h, as in the live run
	cases := []struct {
		typ   string
		until *time.Time
		want  string
		icon  string
		color string
	}{
		{"limit", &until, "Limit · " + until.In(time.Local).Format("15:04"), "gauge.with.dots.needle.0percent", waittype.Red.Color()},
		{"login", nil, "Login · h1", "person.crop.circle.badge.exclamationmark", waittype.Red.Color()},
		{"api_error", nil, "API error", "exclamationmark.triangle.fill", waittype.Orange.Color()},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			h := newNativeHarness(t, on())
			h.world.add(winA, wsA, "a")
			h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
			if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: wsA, CmuxSurfaceID: "S-s1",
				PID: h.pid, ConfigDir: h.cfg, Status: "busy", CellID: "h1", Scope: "perso", UpdatedAt: h.now}); err != nil {
				t.Fatal(err)
			}
			h.step(0)
			if err := h.st.SetSessionWait("s1", pigtail.SessionWait{Type: c.typ, Source: pigtail.WaitSourceAuto, Since: h.now, Until: c.until}); err != nil {
				t.Fatal(err)
			}
			h.nativeWait("s1", "idle", "", h.now.Add(200*time.Millisecond))
			h.step(time.Second)
			if e := h.world.status[wsA][KeyAttention]; e.Value != c.want || e.Icon != c.icon || e.Color != c.color {
				t.Fatalf("pill = %+v, want %q %s %s", e, c.want, c.icon, c.color)
			}
		})
	}
}

// Verify #178, the live simulation: the StopFailure payload was posted from
// a session whose native status is "shell". "shell" is Claude's idle with a
// background shell alive (F1), so the failure shows at once.
func TestRunnerFailureShownWhileABackgroundShellRuns(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "shell", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "login", "", pigtail.WaitSourceAuto)
	h.step(time.Second)
	if p := h.pill(); p != "Login" {
		t.Fatalf("pill with a background shell = %q", p)
	}
	h.nativeWait("s1", "idle", "", h.now.Add(200*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Login" {
		t.Fatalf("pill once idle = %q", p)
	}
}

// A2: native "shell" is idle with a background shell, not a new turn: the
// Limit stays. Only a real turn ("busy") ends it, and its end reads Your turn.
func TestRunnerLimitKeptThroughABackgroundShell(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.nativeWait("s1", "idle", "", h.now.Add(200*time.Millisecond))
	until := h.now.Add(time.Hour)
	if err := h.st.SetSessionWait("s1", pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: h.now, Until: &until}); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	if p := h.pill(); !strings.HasPrefix(p, "Limit") {
		t.Fatalf("pill = %q", p)
	}
	h.nativeWait("s1", "shell", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if ws := h.waits("s1"); len(ws) != 1 {
		t.Fatalf("limit dropped by a background shell: %+v", ws)
	}
	if p := h.pill(); !strings.HasPrefix(p, "Limit") {
		t.Fatalf("pill with a background shell = %q", p)
	}
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if ws := h.waits("s1"); len(ws) != 1 {
		t.Fatalf("limit dropped by idle: %+v", ws)
	}
	h.nativeWait("s1", "busy", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if ws := h.waits("s1"); len(ws) != 0 {
		t.Fatalf("limit kept through a real turn: %+v", ws)
	}
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("end of the real turn: %q", p)
	}
}

// D24: "shell" is idle, it marks no busy. An instance the loop only ever saw
// idle-with-shell has no proven turn: its idle reads no "Your turn".
func TestRunnerNoYourTurnAfterAShellOnlyInstance(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "shell", h.now.Add(-time.Minute))
	h.step(0)
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("pill = %q, want none", p)
	}
}

// ghostTranscript is a transcript fixture served through Deps.FindTranscript:
// its lines are synthetic, with the real key shapes (measured 2026-10-27).
type ghostTranscript struct {
	t    *testing.T
	path string
}

func newGhostTranscript(t *testing.T, h *nativeHarness) *ghostTranscript {
	t.Helper()
	g := &ghostTranscript{t: t, path: filepath.Join(t.TempDir(), "s1.jsonl")}
	if err := os.WriteFile(g.path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.r.deps.FindTranscript = func(id, cfg string) (string, bool) { return g.path, true }
	return g
}

func (g *ghostTranscript) append(lines ...string) {
	g.t.Helper()
	f, err := os.OpenFile(g.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			g.t.Fatal(err)
		}
	}
}

func stampTS(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

// A background notification that runs no turn writes only a queue enqueue and
// remove; the native status still flickers idle → busy → idle.
func ghostNotification(at time.Time) []string {
	return []string{
		`{"type":"queue-operation","operation":"enqueue","timestamp":"` + stampTS(at) + `","content":"<task-notification>done</task-notification>"}`,
		`{"type":"queue-operation","operation":"remove","timestamp":"` + stampTS(at.Add(58*time.Millisecond)) + `"}`,
	}
}

// A real notification turn writes a user entry with origin task-notification.
func realNotification(at time.Time) string {
	return `{"type":"user","timestamp":"` + stampTS(at) + `","origin":{"kind":"task-notification"},"message":{"content":"<task-notification>done</task-notification>"}}`
}

// AC1 + AC2: a declared « working » survives a notification that runs no
// turn, and is cleared once a turn-opening entry shows.
func TestRunnerGhostNotificationKeepsDeclaredWait(t *testing.T) {
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	g := newGhostTranscript(t, h)
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "working", "background build", pigtail.WaitSourceDeclared)
	h.step(time.Second)
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	// The flicker: busy then idle again, the transcript holding the ghost only.
	g.append(ghostNotification(h.now.Add(2 * time.Second))...)
	h.nativeWait("s1", "busy", "", h.now.Add(2*time.Second))
	h.step(time.Second)
	h.nativeWait("s1", "idle", "", h.now.Add(2500*time.Millisecond))
	h.step(time.Second)
	h.step(time.Second)
	if len(h.waits("s1")) != 1 {
		t.Fatalf("declared wait lost to a notification that ran no turn: %+v", h.waits("s1"))
	}
	// A real turn: the notification now writes its user entry.
	g.append(realNotification(h.now.Add(2 * time.Second)))
	h.nativeWait("s1", "busy", "", h.now.Add(2*time.Second))
	h.step(time.Second)
	h.nativeWait("s1", "idle", "", h.now.Add(3*time.Second))
	h.step(time.Second)
	if len(h.waits("s1")) != 0 {
		t.Fatalf("declared wait kept after a real turn: %+v", h.waits("s1"))
	}
}

// turnIO counts the transcript reads and lookups of the declared-wait check.
type turnIO struct {
	mu     sync.Mutex
	reads  int
	finds  int
	covers bool // the fake window's coverage
	ran    bool
}

func (io *turnIO) install(h *nativeHarness, path string) {
	h.r.deps.FindTranscript = func(id, cfg string) (string, bool) {
		io.mu.Lock()
		defer io.mu.Unlock()
		io.finds++
		return path, path != ""
	}
	h.r.deps.ReadTurns = func(p string, from int64, since time.Time) (bool, bool, int64, error) {
		io.mu.Lock()
		defer io.mu.Unlock()
		io.reads++
		return io.ran, io.covers, 100, nil
	}
}

func (io *turnIO) counts() (reads, finds int) {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.reads, io.finds
}

// declaredIdleThenMoved leaves s1 with a declared wait, idle seen, and its
// status moved again: the tracker asks for the transcript verdict.
func declaredIdleThenMoved(h *nativeHarness, status string) {
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "working", "background build", pigtail.WaitSourceDeclared)
	h.step(time.Second)
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	h.nativeWait("s1", status, "", h.now.Add(500*time.Millisecond))
}

// AC4: a pills-only sync reads no transcript; the full pass does.
func TestRunnerPillsOnlyNeverReadsTurns(t *testing.T) {
	h := newNativeHarness(t, on())
	io := &turnIO{covers: true}
	io.install(h, filepath.Join(t.TempDir(), "s1.jsonl"))
	declaredIdleThenMoved(h, "busy")
	h.now = h.now.Add(time.Second)
	reads0, finds0 := io.counts() // the full passes so far (they prime the cursor)
	for i := 0; i < 3; i++ {
		if _, err := h.r.sync(h.now, true); err != nil {
			t.Fatal(err)
		}
	}
	if reads, finds := io.counts(); reads != reads0 || finds != finds0 {
		t.Fatalf("pills-only sync: %d reads, %d lookups more, want none", reads-reads0, finds-finds0)
	}
	if len(h.waits("s1")) != 1 {
		t.Fatalf("pills-only sync cleared the wait: %+v", h.waits("s1"))
	}
	h.step(time.Second)
	if reads, _ := io.counts(); reads <= reads0 {
		t.Fatalf("full pass: %d reads, want more than %d", reads, reads0)
	}
}

// A cursor whose session no longer holds a declared wait is dropped.
func TestRunnerTurnCursorsSwept(t *testing.T) {
	h := newNativeHarness(t, on())
	io := &turnIO{covers: true}
	io.install(h, filepath.Join(t.TempDir(), "s1.jsonl"))
	declaredIdleThenMoved(h, "busy")
	h.step(time.Second)
	if len(h.r.turns) != 1 {
		t.Fatalf("cursors = %d, want 1", len(h.r.turns))
	}
	if _, err := h.st.ClearSessionWaits("s1", pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	h.step(time.Second)
	if len(h.r.turns) != 0 {
		t.Fatalf("cursor kept after the wait went: %d", len(h.r.turns))
	}
}

// A missing transcript is looked up once per slow pass, not per observation.
func TestRunnerTranscriptLookupOncePerSlowPass(t *testing.T) {
	h := newNativeHarness(t, on())
	io := &turnIO{}
	io.install(h, "") // not found
	declaredIdleThenMoved(h, "idle")
	for i := 0; i < 3; i++ {
		h.r.waitingSessions(h.now, false)
	}
	if _, finds := io.counts(); finds != 1 {
		t.Fatalf("lookups in one slow pass = %d, want 1", finds)
	}
	h.r.slowSeq++
	h.r.waitingSessions(h.now, false)
	if _, finds := io.counts(); finds != 2 {
		t.Fatalf("lookups after the next slow pass = %d, want 2", finds)
	}
}

// AC3: a window that does not reach the declaration gives no verdict: the old
// rule applies (a busy after the idle clears).
func TestRunnerUncoveredWindowFallsBack(t *testing.T) {
	h := newNativeHarness(t, on())
	io := &turnIO{covers: false}
	io.install(h, filepath.Join(t.TempDir(), "s1.jsonl"))
	declaredIdleThenMoved(h, "busy")
	h.step(time.Second)
	if len(h.waits("s1")) != 0 {
		t.Fatalf("uncovered window: wait kept, want today's behaviour: %+v", h.waits("s1"))
	}
}

// A later read that skipped bytes (covered=false: a replaced file, a capped
// window) gives no « no turn » verdict either: the old rule applies.
func TestRunnerGapOnLaterReadFallsBack(t *testing.T) {
	h := newNativeHarness(t, on())
	io := &turnIO{covers: true}
	io.install(h, filepath.Join(t.TempDir(), "s1.jsonl"))
	declaredIdleThenMoved(h, "idle") // primed by the full passes, covered
	h.step(time.Second)
	if len(h.waits("s1")) != 1 {
		t.Fatalf("wait lost before the busy: %+v", h.waits("s1"))
	}
	io.covers = false // the next read skips bytes
	h.nativeWait("s1", "busy", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if len(h.waits("s1")) != 0 {
		t.Fatalf("gap on a later read: wait kept, want today's behaviour: %+v", h.waits("s1"))
	}
}

// The cursor is primed at the first full pass that sees the declaration: a
// long tail written afterwards (the rest of the declaring turn) cannot push
// the declaration out of the first window.
func TestRunnerPrimedCursorSurvivesLongTail(t *testing.T) {
	h := newNativeHarness(t, on())
	g := newGhostTranscript(t, h)
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "working", "background build", pigtail.WaitSourceDeclared)
	h.step(time.Second) // first full pass with the declaration: primes the cursor
	pad := `{"type":"assistant","timestamp":"` + stampTS(h.now.Add(time.Second)) + `","message":{"content":"` + strings.Repeat("a", 1000) + `"}}`
	for i := 0; i < 300; i++ { // > 256 KiB, the tail window of a first read
		g.append(pad)
	}
	h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	g.append(ghostNotification(h.now.Add(2 * time.Second))...)
	h.nativeWait("s1", "busy", "", h.now.Add(2*time.Second))
	h.step(time.Second)
	h.nativeWait("s1", "idle", "", h.now.Add(2500*time.Millisecond))
	h.step(time.Second)
	if len(h.waits("s1")) != 1 {
		t.Fatalf("declared wait lost after a long tail: %+v", h.waits("s1"))
	}
}

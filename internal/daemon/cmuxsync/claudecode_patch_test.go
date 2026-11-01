package cmuxsync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The temporary claude_code patch is the one exception to OwnedKeys: a
// dedicated op kind that can only ever write cmux's native Idle row. Every
// other status op on a foreign key stays refused, claude_code included.
func TestClaudeCodeIdleOpIsTheOnlyException(t *testing.T) {
	t.Parallel()
	refused := []Op{
		{Kind: OpSetStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: "claude_code", Value: "Idle"}},
		{Kind: OpSetStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: "claude_code", Value: "Running"}},
		{Kind: OpClearStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: "claude_code"}},
		{Kind: OpSetStatus, WorkspaceID: wsA, Status: cmux.StatusEntry{Key: cmux.InboxStatusKey, Value: "x"}},
	}
	for _, op := range refused {
		w := newWorld()
		w.add(winA, wsA, "a")
		if err := op.Apply(w); !errors.Is(err, ErrNotOwned) {
			t.Errorf("%+v: err %v, want ErrNotOwned", op, err)
		}
		if len(w.calls) != 0 {
			t.Errorf("%+v reached cmux: %q", op, w.calls)
		}
	}
	if Owns("claude_code") {
		t.Fatal("claude_code must not become an owned key")
	}

	// Whatever Status the op carries, it writes the native Idle row and
	// nothing else: no other value, no other key.
	for _, smuggled := range []cmux.StatusEntry{
		{},
		{Key: "claude_code", Value: "Running", Icon: "bolt.fill"},
		{Key: "claude_code", Value: "Needs input"},
		{Key: cmux.InboxStatusKey, Value: "x"},
	} {
		w := newWorld()
		w.add(winA, wsA, "a")
		if err := (Op{Kind: OpClaudeCodeIdle, WorkspaceID: wsA, Status: smuggled}).Apply(w); err != nil {
			t.Fatalf("apply: %v", err)
		}
		want := cmux.StatusEntry{Key: "claude_code", Value: "Idle", Icon: "pause.circle.fill", Color: "#8E8E93"}
		if len(w.status[wsA]) != 1 || w.status[wsA]["claude_code"] != want {
			t.Fatalf("smuggled %+v wrote %+v", smuggled, w.status[wsA])
		}
	}

	// Record never folds it into the owned state.
	applied := map[string]Applied{}
	Record(applied, Op{Kind: OpClaudeCodeIdle, WorkspaceID: wsA})
	if _, ok := applied[wsA].Statuses["claude_code"]; ok {
		t.Fatal("claude_code recorded as owned")
	}
}

func idleFor(id, surface string, since time.Duration, now time.Time) Session {
	s := sess(id, wsA, surface, "idle", true)
	s.NativeStatusAt = now.Add(-since)
	return s
}

func TestClaudeCodeStuckRunning(t *testing.T) {
	t.Parallel()
	now := t0.Add(time.Hour)
	running := []cmux.StatusEntry{{Key: "claude_code", Value: "Running", Icon: "bolt.fill", Color: "#4C8DFF"}}
	busy := idleFor("b", "S2", time.Minute, now)
	busy.Status = "busy"
	waiting := idleFor("w", "S2", time.Minute, now)
	waiting.Status = "waiting"
	noNative := idleFor("n", "S2", time.Minute, now)
	noNative.NativeStatusAt = time.Time{}
	dead := idleFor("d", "S2", time.Minute, now)
	dead.Status, dead.Alive = "busy", false
	ended := idleFor("e", "S3", time.Minute, now)
	ended.Status, ended.Ended, ended.Alive = "busy", true, false
	offSurface := idleFor("o", "S9", time.Minute, now)
	noSurface := idleFor("u", "", time.Minute, now)
	elsewhere := sess("x", wsB, "S1", "busy", true)
	unknown := live(wsA)
	unknown.Surfaces = nil

	cases := []struct {
		name     string
		w        Workspace
		sessions []Session
		shown    []cmux.StatusEntry
		want     bool
	}{
		{"idle for a minute, cmux says Running: fix", live(wsA), []Session{idleFor("a", "S1", time.Minute, now)}, running, true},
		{"exactly 10 s: fix", live(wsA), []Session{idleFor("a", "S1", 10*time.Second, now)}, running, true},
		{"idle for 5 s only: wait", live(wsA), []Session{idleFor("a", "S1", 5*time.Second, now)}, running, false},
		{"cmux already says Idle", live(wsA), []Session{idleFor("a", "S1", time.Minute, now)},
			[]cmux.StatusEntry{{Key: "claude_code", Value: "Idle"}}, false},
		{"cmux says Needs input", live(wsA), []Session{idleFor("a", "S1", time.Minute, now)},
			[]cmux.StatusEntry{{Key: "claude_code", Value: "Needs input"}}, false},
		{"no claude_code row", live(wsA), []Session{idleFor("a", "S1", time.Minute, now)}, nil, false},
		{"another session busy", live(wsA), []Session{idleFor("a", "S1", time.Minute, now), busy}, running, false},
		{"another session waiting", live(wsA), []Session{idleFor("a", "S1", time.Minute, now), waiting}, running, false},
		{"native file absent or stale", live(wsA), []Session{noNative}, running, false},
		{"surfaces unknown (#149)", unknown, []Session{idleFor("a", "S1", time.Minute, now)}, running, false},
		{"live session on a surface not listed", live(wsA), []Session{idleFor("a", "S1", time.Minute, now), offSurface}, running, false},
		{"live session with no surface", live(wsA), []Session{idleFor("a", "S1", time.Minute, now), noSurface}, running, false},
		{"no live session at all", live(wsA), []Session{dead, ended}, running, false},
		{"dead and ended sessions do not count", live(wsA), []Session{idleFor("a", "S1", time.Minute, now), dead, ended}, running, true},
		{"another workspace's busy session does not count", live(wsA), []Session{idleFor("a", "S1", time.Minute, now), elsewhere}, running, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeCodeStuckRunning(c.w, c.sessions, c.shown, now); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// nativeHarness gives each registered session a native sessions/<pid>.json
// in a temp config dir, read through the runner's real cache.
type nativeHarness struct {
	*harness
	cfg string
	pid int
}

func newNativeHarness(t *testing.T, cfg Config) *nativeHarness {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, cfg)
	h.r.deps.Native = func(cfg string, pid int) (claudesession.Native, bool) {
		b, err := os.ReadFile(filepath.Join(cfg, "sessions", strconv.Itoa(pid)+".json"))
		if err != nil {
			return claudesession.Native{}, false
		}
		var n claudesession.Native
		return n, json.Unmarshal(b, &n) == nil
	}
	h.r.natives = newNativeCache(h.r.deps.Native)
	return &nativeHarness{harness: h, cfg: dir, pid: 4242}
}

// native registers session id in ws with a native file saying status since
// at.
func (h *nativeHarness) native(id, ws, status string, at time.Time) {
	h.t.Helper()
	h.alive[id] = true
	h.world.openSurface(ws, "S-"+id)
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, CmuxWorkspaceID: ws, CmuxSurfaceID: "S-" + id,
		PID: h.pid, ConfigDir: h.cfg, Status: status, UpdatedAt: h.now}); err != nil {
		h.t.Fatal(err)
	}
	b, _ := json.Marshal(claudesession.Native{PID: h.pid, SessionID: id, Status: status, Kind: "interactive", StatusUpdatedAt: at.UnixMilli()})
	path := filepath.Join(h.cfg, "sessions", strconv.Itoa(h.pid)+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		h.t.Fatal(err)
	}
	_ = os.Chtimes(path, at, at)
}

func (h *nativeHarness) claudeCodeWrites() int {
	n := 0
	for _, c := range h.world.calls {
		if strings.HasPrefix(c, "set-status ") && strings.Contains(c, " claude_code ") {
			n++
		}
	}
	return n
}

func withFix() Config { return Config{Enabled: true, ClaudeCodeIdleFix: true} }

// A session idle for ≥ 10 s under a claude_code row stuck on Running: the
// slow pass puts the native Idle row back, and if cmux sets Running again,
// pigtail corrects it at most once per slow pass — never on the 1 s passes.
func TestRunnerClaudeCodePatchOncePerSlowPass(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, withFix())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "idle", h.now.Add(-time.Minute))
	h.world.status[wsA]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Running", Icon: "bolt.fill", Color: "#4C8DFF"}

	h.step(0)
	want := cmux.StatusEntry{Key: "claude_code", Value: "Idle", Icon: "pause.circle.fill", Color: "#8E8E93"}
	if got := h.world.status[wsA]["claude_code"]; got != want {
		t.Fatalf("claude_code %+v, want %+v", got, want)
	}
	if n := h.claudeCodeWrites(); n != 1 {
		t.Fatalf("%d claude_code writes, want 1", n)
	}

	h.world.status[wsA]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Running", Icon: "bolt.fill", Color: "#4C8DFF"}
	for i := 0; i < 29; i++ {
		h.step(time.Second)
	}
	if n := h.claudeCodeWrites(); n != 1 {
		t.Fatalf("rewritten on a fast pass: %d writes", n)
	}
	h.step(time.Second)
	if n := h.claudeCodeWrites(); n != 2 || h.world.status[wsA]["claude_code"] != want {
		t.Fatalf("not corrected on the next slow pass: %d writes, %+v", n, h.world.status[wsA]["claude_code"])
	}
	if !h.r.State().ClaudeCodeIdleFix {
		t.Fatal("state does not report the patch")
	}
}

// Busy, waiting, too recent, or no native file: Running stays.
func TestRunnerClaudeCodePatchLeavesRealActivity(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, status string
		since        time.Duration
	}{
		{"busy", "busy", time.Minute},
		{"waiting", "waiting", time.Minute},
		{"idle for 3 s", "idle", 3 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newNativeHarness(t, withFix())
			h.world.add(winA, wsA, "a")
			h.native("s1", wsA, c.status, h.now.Add(-c.since))
			h.world.status[wsA]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Running"}
			h.step(0)
			if n := h.claudeCodeWrites(); n != 0 {
				t.Fatalf("claude_code rewritten: %q", h.world.calls)
			}
		})
	}
	t.Run("no native file", func(t *testing.T) {
		h := newNativeHarness(t, withFix())
		h.world.add(winA, wsA, "a")
		h.session("s1", wsA, "idle", true) // registry only, no native file
		h.world.status[wsA]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Running"}
		h.step(0)
		h.step(30 * time.Second)
		if n := h.claudeCodeWrites(); n != 0 {
			t.Fatalf("claude_code rewritten: %q", h.world.calls)
		}
	})
}

// Switched off (Config.ClaudeCodeIdleFix false), the loop runs as before and
// never touches claude_code.
func TestRunnerClaudeCodePatchSwitchedOff(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "idle", h.now.Add(-time.Minute))
	h.world.status[wsA]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Running"}
	h.step(0)
	h.step(30 * time.Second)
	if n := h.claudeCodeWrites(); n != 0 || h.world.status[wsA]["claude_code"].Value != "Running" {
		t.Fatalf("patch ran while off: %q", h.world.calls)
	}
	if h.r.State().ClaudeCodeIdleFix {
		t.Fatal("state reports the patch on")
	}
}

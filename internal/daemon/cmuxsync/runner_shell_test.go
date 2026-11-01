package cmuxsync

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Native "shell" is Claude's idle with a background shell alive (F1): the
// session waits for its user. Incident 22c3e2da: a declared review hidden and
// claude_code stuck on Running for 1 h 30.
func TestShellIsIdleAndFixesClaudeCode(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, withFix())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.wait("s1", "review", "PR #9", pigtail.WaitSourceDeclared)
	h.nativeWait("s1", "shell", "", h.now.Add(time.Second))
	h.world.status[wsA]["claude_code"] = cmux.StatusEntry{Key: "claude_code", Value: "Running", Icon: "bolt.fill", Color: "#4C8DFF"}
	for i := 0; i < 31; i++ { // past ClaudeCodeIdleAfter and one slow pass
		h.step(time.Second)
	}
	if p := h.pill(); p != "Review · PR #9" {
		t.Fatalf("pill = %q", p)
	}
	if got := h.world.status[wsA]["claude_code"].Value; got != "Idle" {
		t.Fatalf("claude_code = %q, want Idle", got)
	}
}

// A StopFailure (Limit) ends the turn; a background shell makes Claude write
// "shell" right after. That is not a new turn: the Limit stays.
func TestLimitKeptWhileABackgroundShellRuns(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	until := h.now.Add(time.Hour)
	if err := h.st.SetSessionWait("s1", pigtail.SessionWait{Type: "limit", Source: pigtail.WaitSourceAuto, Since: h.now, Until: &until}); err != nil {
		t.Fatal(err)
	}
	h.nativeWait("s1", "shell", "", h.now.Add(20*time.Millisecond))
	h.step(time.Second)
	if len(h.waits("s1")) != 1 {
		t.Fatalf("limit dropped by a background shell: %+v", h.waits("s1"))
	}
	if p := h.pill(); !strings.HasPrefix(p, "Limit") {
		t.Fatalf("pill = %q", p)
	}
}

// Daemon restarted after a declaration (the loop never saw the declaring
// turn): a "shell" written after it is not the next turn — the wait stays.
func TestDeclaredKeptAcrossRestartWithABackgroundShell(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.wait("s1", "arbitrage", "A ou B", pigtail.WaitSourceDeclared)
	h.native("s1", wsA, "shell", h.now.Add(time.Second))
	h.step(2 * time.Second)
	if len(h.waits("s1")) != 1 {
		t.Fatalf("declared wait dropped: %+v", h.waits("s1"))
	}
	if p := h.pill(); p != "Decision · A ou B" {
		t.Fatalf("pill = %q", p)
	}
}

// D24 BusySeen: "busy" then "shell" reads Your turn ("shell" is idle).
func TestBusyThenShellReadsYourTurn(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.nativeWait("s1", "shell", "", h.now.Add(500*time.Millisecond))
	h.step(time.Second)
	if p := h.pill(); p != "Your turn" {
		t.Fatalf("pill = %q", p)
	}
}

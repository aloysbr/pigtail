package cmuxsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	rsSurface = "5E5E5E5E-0000-4000-8000-0000000000A1" // the run's surface (upper case, as the loop keeps it)
	rsWorker  = 7000                                   // the respawn worker's pid
)

// nativeOn registers session id in ws on surface, run by pid, with its own
// native file (one per pid) saying status since at.
func (h *nativeHarness) nativeOn(id, ws, surface string, pid int, status string, at time.Time) {
	h.t.Helper()
	h.alive[id] = true
	h.world.openSurface(ws, surface)
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, CmuxWorkspaceID: ws, CmuxSurfaceID: surface,
		PID: pid, ConfigDir: h.cfg, Status: status, UpdatedAt: h.now}); err != nil {
		h.t.Fatal(err)
	}
	h.statusOn(id, pid, status, at)
}

// statusOn rewrites pid's native file: session id, status since at.
func (h *nativeHarness) statusOn(id string, pid int, status string, at time.Time) {
	h.t.Helper()
	b, _ := json.Marshal(claudesession.Native{PID: pid, SessionID: id, Status: status, Kind: "interactive", StatusUpdatedAt: at.UnixMilli()})
	path := filepath.Join(h.cfg, "sessions", strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		h.t.Fatal(err)
	}
	_ = os.Chtimes(path, at, at)
}

func waitingReq(wait string) pigtail.CmuxRespawnRequest {
	return pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnWaiting, Wait: wait, SurfaceID: rsSurface, PID: rsWorker}
}

// settle runs the passes that let a dropped state leave the pill (hysteresis).
func (h *nativeHarness) settle() {
	h.step(time.Second)
	h.step(WaitingHold + time.Second)
}

// D16: a held respawn shows on the run's workspace while the worker waits,
// keeps its pill across slow passes, and goes once cleared.
func TestRunnerRespawnWaitingLifecycle(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "idle", h.now.Add(-time.Minute))
	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitDraft))
	h.step(0)
	e := h.world.status[wsA][KeyAttention]
	if e.Value != "Respawn · draft in the input" || e.Icon != "arrow.clockwise.circle.fill" || e.Color != "#FF9500" {
		t.Fatalf("pill = %+v", e)
	}
	h.step(35 * time.Second)
	if p := h.pill(); p != "Respawn · draft in the input" {
		t.Fatalf("after 35 s: %q", p)
	}
	h.r.RespawnClear(wsA)
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("cleared: %q", p)
	}
}

// A worker that died (killed, crashed) leaves no orange pill behind.
func TestRunnerRespawnWaitingDiesWithItsWorker(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	alive := true
	h.r.deps.PidAlive = func(pid int) bool { return pid != rsWorker || alive }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "idle", h.now.Add(-time.Minute))
	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitBackground))
	h.step(0)
	if p := h.pill(); p != "Respawn · background work" {
		t.Fatalf("pill = %q", p)
	}
	alive = false
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("worker gone: %q", p)
	}
}

// failed stays red — through an idle reading — until the surface's session
// turns busy after an idle one: the next prompt (Claude's clock both sides).
func TestRunnerRespawnFailedStaysUntilNextPrompt(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return false } // a final state ignores the worker
	h.world.add(winA, wsA, "a")
	// A stale busy read first: no idle seen yet, nothing proves a new prompt.
	h.nativeOn("s2", wsA, rsSurface, 4343, "busy", h.now.Add(-time.Hour))
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	if p := h.pill(); p != "Blocked · respawn failed" {
		t.Fatalf("pill = %q", p)
	}
	h.statusOn("s2", 4343, "idle", h.now)
	h.step(35 * time.Second)
	if p := h.pill(); p != "Blocked · respawn failed" {
		t.Fatalf("idle: %q", p)
	}
	h.statusOn("s2", 4343, "busy", h.now)
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("after the next prompt: %q", p)
	}
}

// relaunchOn registers session id in ws on surface, run by pid, whose
// native file says it started at started and is busy since at: a Claude
// launched by hand with a prompt, never read idle.
func (h *nativeHarness) relaunchOn(id, ws, surface string, pid int, started, at time.Time) {
	h.t.Helper()
	h.nativeOn(id, ws, surface, pid, "busy", at)
	b, _ := json.Marshal(claudesession.Native{PID: pid, SessionID: id, Status: "busy", Kind: "interactive",
		StartedAt: started.UnixMilli(), StatusUpdatedAt: at.UnixMilli()})
	path := filepath.Join(h.cfg, "sessions", strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		h.t.Fatal(err)
	}
	_ = os.Chtimes(path, at, at)
}

// Incident 2026-10-04: the respawn failed (it had closed the old session),
// then the user relaunched Claude by hand in the same tab, straight with a
// prompt — busy from its first read, never idle. That relaunch clears the
// red at once: the tab is no longer blocked.
func TestRunnerRespawnFailedClearedByRelaunch(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return false }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "idle", h.now.Add(-time.Minute))
	h.alive["s1"] = false // the failed respawn closed it
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	if p := h.pill(); p != "Blocked · respawn failed" {
		t.Fatalf("pill = %q", p)
	}
	h.step(7 * time.Minute)
	h.relaunchOn("s3", wsA, rsSurface, 4444, h.now, h.now.Add(500*time.Millisecond))
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("after the relaunch: %q", p)
	}
}

// A Claude that started before the mark is no relaunch, busy as it may be:
// the respawn's own bad launch (wrong account, not fresh) keeps the red.
func TestRunnerRespawnFailedKeptByEarlierLaunch(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return false }
	h.world.add(winA, wsA, "a")
	h.relaunchOn("s3", wsA, rsSurface, 4444, h.now.Add(-2*time.Second), h.now.Add(-time.Second))
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	h.settle()
	if p := h.pill(); p != "Blocked · respawn failed" {
		t.Fatalf("pill = %q", p)
	}
}

// abandoned: the old session is still there, idle, its turn over (Your
// turn under the red); the user's next prompt clears it.
func TestRunnerRespawnAbandonedClearedByNextPrompt(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "busy", h.now.Add(-time.Minute))
	h.step(0)
	h.statusOn("s1", 4242, "idle", h.now)
	h.step(time.Second)
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnAbandoned, SurfaceID: rsSurface})
	h.step(time.Second)
	if p := h.pill(); p != "Respawn · abandoned" {
		t.Fatalf("pill = %q", p)
	}
	if c := h.world.status[wsA][KeyAttention].Color; c != "#FF9500" {
		t.Fatalf("colour = %q, want orange", c)
	}
	h.step(time.Second)
	h.statusOn("s1", 4242, "busy", h.now)
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("after the next prompt: %q", p)
	}
}

// A new mark replaces the workspace's last one.
func TestRunnerRespawnMarkReplaces(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "idle", h.now.Add(-time.Minute))
	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitDraft))
	h.step(0)
	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitScreen))
	h.settle()
	if p := h.pill(); p != "Respawn · screen unreadable" {
		t.Fatalf("pill = %q", p)
	}
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnAbandoned, SurfaceID: rsSurface})
	h.step(time.Second)
	if p := h.pill(); p != "Respawn · abandoned" {
		t.Fatalf("pill = %q", p)
	}
}

// The worker re-posts an unchanged pill every minute (a restarted daemon
// forgets it): that refresh asks for no extra pass.
func TestRunnerRespawnRefreshDoesNotNudge(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitDraft))
	h.step(0)
	h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitDraft))
	h.r.mu.Lock()
	nudged := h.r.nudge
	h.r.mu.Unlock()
	if nudged {
		t.Fatal("an unchanged mark nudged the loop")
	}
}

// A listing made after the mark that no longer shows the workspace drops it.
func TestRunnerRespawnGoneWithWorkspace(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	h.world.close(wsA)
	h.step(31 * time.Second)
	h.r.mu.Lock()
	n := len(h.r.respawn)
	h.r.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d marks kept for a closed workspace", n)
	}
}

// Switching the loop off forgets the marks: switched on again, no pill comes
// back without a new one.
func TestRunnerRespawnForgottenWhenSwitchedOff(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	if p := h.pill(); p != "Blocked · respawn failed" {
		t.Fatalf("pill = %q", p)
	}
	h.r.SetEnabled(false)
	h.step(time.Second)
	h.r.SetEnabled(true)
	h.step(time.Second)
	h.step(time.Second)
	if p := h.pill(); p != "" {
		t.Fatalf("switched on again: %q", p)
	}
}

// transcriptPath is where session id's synthetic transcript lives in the
// harness config dir.
func (h *nativeHarness) transcriptPath(id string) string {
	return filepath.Join(h.cfg, "projects", "-w", id+".jsonl")
}

// transcriptAppendRaw appends raw bytes (no newline added) to id's transcript.
func (h *nativeHarness) transcriptAppendRaw(id, raw string) {
	h.t.Helper()
	path := h.transcriptPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		h.t.Fatal(err)
	}
}

// transcriptAppend appends whole lines to id's transcript.
func (h *nativeHarness) transcriptAppend(id string, lines ...string) {
	h.t.Helper()
	for _, l := range lines {
		h.transcriptAppendRaw(id, l+"\n")
	}
}

func stamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The entry shapes measured on real transcripts (2026-10-23), synthetic text.
func humanLine(at time.Time, text string) string {
	return `{"type":"user","timestamp":"` + stamp(at) + `","origin":{"kind":"human"},"message":{"role":"user","content":` + jsonString(text) + `}}`
}

func queuedLine(at time.Time, text string) string {
	return `{"type":"attachment","timestamp":"` + stamp(at) + `","attachment":{"type":"queued_command","prompt":` + jsonString(text) + `,"origin":{"kind":"human"}}}`
}

func slashLine(at time.Time, name, args string) string {
	c := "<command-message>" + name[1:] + "</command-message>\n<command-name>" + name + "</command-name>"
	if args != "" {
		c += "\n<command-args>" + args + "</command-args>"
	}
	return humanLine(at, c)
}

func imageLine(at time.Time) string {
	return `{"type":"user","timestamp":"` + stamp(at) + `","origin":{"kind":"human"},"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AAAA"}}]}}`
}

func taskNoteLine(at time.Time) string {
	return `{"type":"user","timestamp":"` + stamp(at) + `","origin":{"kind":"task-notification","producer":"session-task"},"promptId":"p9","message":{"role":"user","content":"<task-notification>\n<status>completed</status>"}}`
}

func peerMetaLine(at time.Time) string {
	return `{"type":"user","timestamp":"` + stamp(at) + `","origin":{"kind":"peer"},"isMeta":true,"message":{"role":"user","content":"peer says hi"}}`
}

func compactLines(at time.Time) []string {
	u := func(content string) string {
		return `{"type":"user","timestamp":"` + stamp(at) + `","message":{"role":"user","content":` + jsonString(content) + `}}`
	}
	return []string{u("/compact"), u("<command-name>/compact</command-name>"), u("<local-command-stdout>done</local-command-stdout>"),
		`{"type":"user","timestamp":"` + stamp(at) + `","isCompactSummary":true,"message":{"role":"user","content":"summary"}}`}
}

func abandonedReq() pigtail.CmuxRespawnRequest {
	return pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnAbandoned, SurfaceID: rsSurface}
}

// busyFrozen is a session of the run's surface that Claude keeps at busy for
// a background task: its native file is never rewritten.
func (h *nativeHarness) busyFrozen(t *testing.T) {
	t.Helper()
	h.r.deps.PidAlive = func(int) bool { return true }
	h.world.add(winA, wsA, "a")
	h.nativeOn("s1", wsA, rsSurface, 4242, "busy", h.now.Add(-time.Hour))
	h.transcriptAppend("s1", humanLine(h.now.Add(-time.Hour), "start"))
}

// Incident 2026-10-23: Claude kept sessions/<pid>.json at busy for a
// background task, so neither a relaunch nor an idle-then-busy read ever
// fired: the abandoned pill stayed. The user's next prompt, read in the
// transcript, drops it.
func TestRunnerRespawnAbandonedClearedByPromptWhileBusy(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.busyFrozen(t)
	h.r.RespawnMark(wsA, abandonedReq())
	h.step(time.Second)
	if p := h.pill(); p != "Respawn · abandoned" {
		t.Fatalf("pill = %q", p)
	}
	h.step(35 * time.Second)
	if p := h.pill(); p != "Respawn · abandoned" {
		t.Fatalf("after 35 s: %q", p)
	}
	h.transcriptAppend("s1", humanLine(h.now, "carry on"))
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("after the next prompt: %q", p)
	}
}

// A prompt typed during a turn is an attachment / queued_command entry, with
// no user entry; an image-only prompt counts too.
func TestRunnerRespawnClearedByQueuedPrompt(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]func(time.Time) string{
		"queued": func(at time.Time) string { return queuedLine(at, "carry on") },
		"image":  imageLine,
	} {
		t.Run(name, func(t *testing.T) {
			h := newNativeHarness(t, on())
			h.busyFrozen(t)
			h.r.RespawnMark(wsA, abandonedReq())
			h.step(time.Second)
			if p := h.pill(); p != "Respawn · abandoned" {
				t.Fatalf("pill = %q", p)
			}
			h.transcriptAppend("s1", line(h.now))
			h.settle()
			if p := h.pill(); p != "" {
				t.Fatalf("after the prompt: %q", p)
			}
		})
	}
}

// D4: a failed mark ignores prompts: the fresh session the respawn launched
// carries its own prompt in argv, the first human entry of its transcript.
func TestRunnerRespawnFailedKeptByPrompt(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.deps.PidAlive = func(int) bool { return false }
	h.world.add(winA, wsA, "a")
	h.relaunchOn("s3", wsA, rsSurface, 4444, h.now.Add(-2*time.Second), h.now.Add(-time.Second))
	h.r.RespawnMark(wsA, pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: rsSurface})
	h.step(0)
	h.transcriptAppend("s3", humanLine(h.now.Add(time.Second), "the argv prompt"), queuedLine(h.now.Add(2*time.Second), "typed"))
	h.step(35 * time.Second)
	h.settle()
	if p := h.pill(); p != "Blocked · respawn failed" {
		t.Fatalf("pill = %q", p)
	}
}

// A line Claude is still writing is not parsed: the next pass reads it whole.
func TestRunnerRespawnClearedByPromptAppendedLater(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.busyFrozen(t)
	h.r.RespawnMark(wsA, abandonedReq())
	h.step(time.Second)
	h.transcriptAppendRaw("s1", humanLine(h.now, "carry on"))
	h.step(time.Second)
	if p := h.pill(); p != "Respawn · abandoned" {
		t.Fatalf("half-written line: %q", p)
	}
	h.transcriptAppendRaw("s1", "\n")
	h.settle()
	if p := h.pill(); p != "" {
		t.Fatalf("once complete: %q", p)
	}
}

// What is not the user's own real turn leaves the abandoned mark standing.
func TestRunnerRespawnFinalKeptByNonHumanEntries(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Turnless: []*regexp.Regexp{regexp.MustCompile(`(?i)^\s*/show(\s|$)`)}})
	const otherSurface = "5E5E5E5E-0000-4000-8000-0000000000A2"
	type row struct {
		name  string
		lines func(since time.Time) []string
		other bool // lines go to a session of another surface
	}
	after := func(since time.Time) time.Time { return since.Add(2 * time.Second) }
	rows := []row{
		{"task notification", func(s time.Time) []string { return []string{taskNoteLine(after(s))} }, false},
		{"peer message", func(s time.Time) []string { return []string{peerMetaLine(after(s))} }, false},
		{"compact", func(s time.Time) []string { return compactLines(after(s)) }, false},
		{"wake line", func(s time.Time) []string { return []string{humanLine(after(s), relay.WakeLine)} }, false},
		{"padded wake line", func(s time.Time) []string { return []string{humanLine(after(s), " "+relay.WakeLine+"\n")} }, false},
		{"turnless /show", func(s time.Time) []string { return []string{slashLine(after(s), "/show", "")} }, false},
		{"turnless /show 2", func(s time.Time) []string { return []string{slashLine(after(s), "/show", "2")} }, false},
		{"queued non-human", func(s time.Time) []string {
			return []string{`{"type":"attachment","timestamp":"` + stamp(after(s)) + `","attachment":{"type":"queued_command","prompt":"bg","origin":{"kind":"task-notification"}}}`}
		}, false},
		{"prompt before the mark", func(s time.Time) []string { return []string{humanLine(s.Add(-time.Second), "x")} }, false},
		{"prompt at the mark", func(s time.Time) []string { return []string{humanLine(s, "x")} }, false},
		{"another surface", func(s time.Time) []string { return []string{humanLine(after(s), "x")} }, true},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			h := newNativeHarness(t, on())
			h.busyFrozen(t)
			h.nativeOn("s2", wsA, otherSurface, 4343, "busy", h.now.Add(-time.Hour))
			since := h.now
			h.r.RespawnMark(wsA, abandonedReq())
			h.step(time.Second)
			target := "s1"
			if c.other {
				target = "s2"
			}
			h.transcriptAppend(target, c.lines(since)...)
			h.step(35 * time.Second)
			h.settle()
			if p := h.pill(); p != "Respawn · abandoned" {
				t.Fatalf("pill = %q, want the abandoned mark kept", p)
			}
		})
	}
}

// A new mark keeps no earlier prompt against it. A final mark carries no pid,
// so a second abandoned equals the first (early return, since kept): a real
// new mark goes through waiting.
func TestRunnerRespawnNewMarkIgnoresAnEarlierPrompt(t *testing.T) {
	t.Parallel()
	t.Run("new mark", func(t *testing.T) {
		h := newNativeHarness(t, on())
		h.busyFrozen(t)
		h.r.RespawnMark(wsA, abandonedReq())
		h.step(time.Second) // a cursor is made, since1
		h.transcriptAppend("s1", humanLine(h.now, "x"))
		h.now = h.now.Add(time.Second)
		h.r.RespawnMark(wsA, waitingReq(pigtail.CmuxRespawnWaitDraft))
		h.r.RespawnMark(wsA, abandonedReq()) // since2, later than the prompt
		h.settle()
		if p := h.pill(); p != "Respawn · abandoned" {
			t.Fatalf("pill = %q, want the new mark kept", p)
		}
	})
	t.Run("positive control", func(t *testing.T) {
		h := newNativeHarness(t, on())
		h.busyFrozen(t)
		h.r.RespawnMark(wsA, abandonedReq())
		h.step(time.Second)
		h.transcriptAppend("s1", humanLine(h.now, "x"))
		h.settle()
		if p := h.pill(); p != "" {
			t.Fatalf("pill = %q, want the prompt to clear the one mark", p)
		}
	})
}

// The since check of respawnSet, driven directly: a pass reads the transcript
// for one mark, a newer mark can land before the set is computed, and the
// prompt read for the older one must not clear the newer one. (The sequence
// test above cannot hit that window: the prompt is filtered against the
// mark's own since at read time.)
func TestRespawnSetIgnoresAPromptReadForAnOlderMark(t *testing.T) {
	t.Parallel()
	h := newNativeHarness(t, on())
	h.r.RespawnMark(wsA, abandonedReq())
	ws := strings.ToUpper(wsA)
	h.r.mu.Lock()
	since := h.r.respawn[ws].since
	h.r.mu.Unlock()
	if got := h.r.respawnSet(nil, map[string]time.Time{ws: since.Add(-time.Second)}); len(got) != 1 {
		t.Fatalf("a prompt read for an older mark cleared the newer one: %v", got)
	}
	if got := h.r.respawnSet(nil, map[string]time.Time{ws: since}); len(got) != 0 {
		t.Fatalf("a prompt read for this mark did not clear it: %v", got)
	}
}

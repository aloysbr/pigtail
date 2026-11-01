package cmuxsync

import (
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// TEMPORARY PATCH (rustine temporaire) — remove this file, OpClaudeCodeIdle,
// Config.ClaudeCodeIdleFix and pigtaild's -cmux-claude-code-fix once cmux
// ships manaflow-ai/cmux#15666 and #15603 (merged 2026-09-29, in no release
// as of 0.64.25: the first release after it carries them).
//
// cmux's own Claude hooks leave the native `claude_code` row on "Running"
// while the session is idle: cmux receives the Stop and does not apply it
// when background_tasks holds ambient tasks, or when a Stop with
// stop_hook_active follows an asyncRewake. Decision (a') of 2026-10-01:
// pigtail rewrites that one row to cmux's own Idle, and nothing else.
//
// This is the one and only exception to OwnedKeys. It cannot write any other
// key, any other value, nor clear the row: OpClaudeCodeIdle ignores the
// Status it carries and always writes claudeCodeIdle. OpSetStatus and
// OpClearStatus on claude_code stay refused like any foreign key.

// ClaudeCodeKey is cmux's native Claude status row.
const ClaudeCodeKey = "claude_code"

// OpClaudeCodeIdle puts cmux's native Idle row back on a workspace whose
// claude_code row is stuck on Running (temporary patch, see above).
const OpClaudeCodeIdle OpKind = "claude-code-idle"

// ClaudeCodeIdleAfter is how long every live session of a workspace must have
// been idle, per its native file, before a Running row is called stuck.
const ClaudeCodeIdleAfter = 10 * time.Second

// claudeCodeRunning is the value the row is stuck on; the only one corrected.
const claudeCodeRunning = "Running"

// claudeCodeIdle is cmux's own Idle row, exactly as `cmux list-status` shows
// it on a really idle session (cmux 0.64.24): no priority.
var claudeCodeIdle = cmux.StatusEntry{Key: ClaudeCodeKey, Value: "Idle", Icon: "pause.circle.fill", Color: "#8E8E93"}

// claudeCodeStuckRunning reports whether workspace w shows claude_code
// Running while every live Claude session in it has been idle for at least
// ClaudeCodeIdleAfter, per its native file. Any doubt answers false: surfaces
// unknown (#149), a live session with no native file, on no listed surface,
// busy, waiting or idle too recently, or no live session at all.
func claudeCodeStuckRunning(w Workspace, sessions []Session, shown []cmux.StatusEntry, now time.Time) bool {
	running := false
	for _, e := range shown {
		if e.Key == ClaudeCodeKey {
			running = e.Value == claudeCodeRunning
		}
	}
	if !running || w.Surfaces == nil {
		return false
	}
	n := 0
	for _, s := range sessions {
		if s.WorkspaceID != w.ID || s.Ended || !s.Alive {
			continue
		}
		if s.SurfaceID == "" || !w.Surfaces[s.SurfaceID] {
			return false
		}
		if s.NativeStatusAt.IsZero() || s.Status != "idle" || now.Sub(s.NativeStatusAt) < ClaudeCodeIdleAfter {
			return false
		}
		n++
	}
	return n > 0
}

// fixClaudeCode is the patch's pass, run on slow passes only: a row cmux sets
// back to Running is corrected at most once per slow pass. It reads
// list-status only on workspaces whose sessions are all idle, and never
// touches a workspace outside the windows the loop may write.
func (r *Runner) fixClaudeCode(now time.Time) {
	sessions := r.sessions()
	hasLive := map[string]bool{}
	for _, s := range sessions {
		if s.Alive && !s.Ended {
			hasLive[s.WorkspaceID] = true
		}
	}
	for _, w := range r.workspaces {
		if !hasLive[w.ID] || !r.allowed[w.ID] {
			continue
		}
		// Cheap check first, without the row: no list-status for a busy one.
		if !claudeCodeStuckRunning(w, sessions, []cmux.StatusEntry{{Key: ClaudeCodeKey, Value: claudeCodeRunning}}, now) {
			continue
		}
		r.pillCheckpoint()
		shown, err := r.deps.Sidebar.ListStatus(w.ID)
		if err != nil {
			r.deps.Logf("cmuxsync: claude_code patch: list-status %s: %v", w.ID, err)
			return
		}
		if !claudeCodeStuckRunning(w, sessions, shown, now) {
			continue
		}
		if err := (Op{Kind: OpClaudeCodeIdle, WorkspaceID: w.ID}).Apply(r.deps.Sidebar); err != nil {
			r.deps.Logf("cmuxsync: claude_code patch on %s: %v", w.ID, err)
			continue
		}
		r.deps.Logf("cmuxsync: claude_code Running → Idle on %s (temporary patch, cmux #15666/#15603)", w.ID)
	}
}

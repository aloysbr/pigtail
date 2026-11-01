package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
)

// runMaintenanceHook is `pigtail hook claude-maintenance` (UserPromptSubmit +
// PostToolUse, behind the maintenance hook script): the single judge
// and the single writer of the witness (D12). Exit 0, failures on stderr,
// except when the per-session lock cannot be taken: exit 1 with the cause as
// the last stderr line, which the hook script turns into its failure
// alert (the notice could not be judged at all).
func runMaintenanceHook(stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, now func() time.Time) int {
	var p struct {
		SessionID     string `json:"session_id"`
		HookEventName string `json:"hook_event_name"`
		AgentID       string `json:"agent_id"`
	}
	if err := json.NewDecoder(stdin).Decode(&p); err != nil || !validMarkerKey(p.SessionID) {
		return 0
	}
	if p.AgentID != "" {
		return 0 // a subagent shares the parent's session_id: the notice is for the main session
	}
	dir := maintenanceDir(getenv)
	unlock, ok, err := maintenance.LockSession(dir, p.SessionID)
	if err != nil {
		// Without the lock the witness cannot be kept either: fail loudly (exit
		// 1, cause as the last stderr line) so the hook script raises its
		// failure alert instead of the notice being lost in silence.
		fmt.Fprintf(stderr, "pigtail maintenance: cannot lock %s: %v\n", filepath.Join(dir, maintenance.SeenDirName, p.SessionID+".lock"), err)
		return 1
	}
	if !ok {
		return 0 // another call holds this session: it speaks for it
	}
	defer unlock()
	w := maintenance.ReadWitness(dir, p.SessionID)
	// The reader's scope, read locally (no daemon): its cell, else the
	// default scope — D17 for a session outside any cell (D22).
	reader := scope.Scope(sessionScope(getenv))
	d := maintenance.Decide(dir, w, now(), p.SessionID, reader)
	// Always rewrite: the witness must be newer than the JSON just judged (bash -nt).
	if err := maintenance.WriteWitness(dir, p.SessionID, d.Next); err != nil {
		fmt.Fprintf(stderr, "pigtail maintenance: cannot write witness %s: %v\n", filepath.Join(dir, maintenance.SeenDirName, p.SessionID), err)
	}
	if !d.Show {
		return 0
	}
	event := p.HookEventName
	if event == "" {
		event = "PostToolUse"
	}
	text := fmt.Sprintf("<pigtail-maintenance id=%q phase=%q>\npigtail system message (maintenance), %s — not from a peer session. Follow it, and tell %s in your next reply that you received it.\n---\n%s</pigtail-maintenance>",
		d.ID, d.Phase, maintenanceOrigin(d, dir, reader), humanName(), d.Text)
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": event, "additionalContext": text}})
	return 0
}

// maintenanceOrigin says where the notice comes from: who set it (on, late)
// and where pigtail read it; at off the state file is gone, so who lifted it
// (D15); an unreadable state names no one (who set it is unreadable too).
// A session is named as reader may see it (D22).
func maintenanceOrigin(d maintenance.Decision, dir string, reader scope.Scope) string {
	path := filepath.Join(dir, maintenance.FileName)
	switch d.Phase {
	case "off":
		return d.State.LiftedByEN(reader) + " (`pigtail maintenance off`)"
	case "corrupt":
		return "read by pigtail from " + path + " (unreadable: who set it is unknown)"
	}
	who := humanName() + " outside Claude Code"
	if d.State.Owner != "" {
		who = "session " + d.State.OwnerNameFor(reader)
	}
	return "set by " + who + " (`pigtail maintenance on`) and read by pigtail from " + path
}

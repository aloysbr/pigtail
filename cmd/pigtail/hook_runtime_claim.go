package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/scope"
)

// runRuntimeClaimHook is `pigtail hook claude-runtime-claim` // a shell hook): once per lease, every other main
// session gets a notice « on », then « off » at the release if it saw the
// lease. The holder and subagents never. Same contract as runMaintenanceHook:
// exit 0, except 1 when the per-session lock (or the witness folder) cannot
// be used — the cause as the last stderr line, so the bash side raises its
// failure alert instead of a silence; the witness is always rewritten.
func runRuntimeClaimHook(stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, now func() time.Time) int {
	var p struct {
		SessionID     string `json:"session_id"`
		HookEventName string `json:"hook_event_name"`
		AgentID       string `json:"agent_id"`
	}
	if err := json.NewDecoder(stdin).Decode(&p); err != nil || !runtimeclaim.ValidSession(p.SessionID) {
		return 0
	}
	if p.AgentID != "" {
		return 0
	}
	dir := runtimeClaimDir(getenv)
	unlock, ok, err := runtimeclaim.LockSession(dir, p.SessionID)
	if err != nil {
		fmt.Fprintf(stderr, "pigtail runtime claim: cannot lock %s: %v\n", filepath.Join(dir, runtimeclaim.SeenDirName, p.SessionID+".lock"), err)
		return 1
	}
	if !ok {
		return 0
	}
	defer unlock()
	w := runtimeclaim.ReadWitness(dir, p.SessionID)
	key := runtimeclaim.TokenKey(dir)
	next := runtimeclaim.Witness{Key: key, Shown: w.Shown}
	id, phase := "", ""
	st, held, lerr := runtimeclaim.Load(dir)
	switch {
	case lerr != nil:
		// Unreadable: the witness is written (one alert per key), and the
		// failure goes to the bash side's alert — never a silence.
		_ = runtimeclaim.WriteWitness(dir, p.SessionID, next)
		fmt.Fprintf(stderr, "pigtail runtime claim: %s unreadable: %v\n", filepath.Join(dir, runtimeclaim.FileName), lerr)
		return 1
	case held && st.Session != p.SessionID:
		id, phase = st.ID, "on"
	case !held:
		// The token says which lease was released: « <id>:off+- ».
		if k := strings.TrimSuffix(key, "+-"); strings.HasSuffix(k, ":off") {
			id, phase = strings.TrimSuffix(k, ":off"), "off"
		}
	}
	show := false
	if phase != "" {
		shown := id + ":" + phase
		if w.Shown != shown && (phase == "on" || strings.HasPrefix(w.Shown, id+":")) {
			show, next.Shown = true, shown
		}
	}
	if err := runtimeclaim.WriteWitness(dir, p.SessionID, next); err != nil {
		fmt.Fprintf(stderr, "pigtail runtime claim: cannot write witness %s: %v\n", filepath.Join(dir, runtimeclaim.SeenDirName, p.SessionID), err)
		return 1
	}
	if !show {
		return 0
	}
	var text string
	if phase == "on" {
		reader := scope.Scope(sessionScope(getenv))
		where := fmt.Sprintf("branch %s, %s", st.Branch, sha12(st.SHA))
		if st.Dirty {
			where += " dirty"
		}
		if st.PR > 0 {
			where += fmt.Sprintf(", PR #%d", st.PR)
		}
		text = fmt.Sprintf("pigtail system message (runtime claim) — not from a peer session. The installed pigtail runtime (binaries, pigtaild, its LaunchAgent plist) is held by %s since %s for: %s (%s). "+
			"Do not run `make install` in a pigtail checkout, `launchctl bootstrap|bootout|load|unload … sh.pigtail.daemon` nor edit that plist until it is released; a PreToolUse hook refuses them. "+
			"Tell %s in your next reply that you received it.\n",
			holderName(st, reader), st.Since.Local().Format("15:04"), st.Reason, where, humanName())
	} else {
		if exp, ok := runtimeclaim.ReadExpiry(dir); ok && exp.Lease.ID == id {
			text = fmt.Sprintf("pigtail system message (runtime claim) — not from a peer session. Runtime claim %s expired at %s (4 h without renewal by its holder) and was lifted — the runtime is free again.\n",
				id, exp.At.Local().Format("15:04"))
		} else {
			text = "pigtail system message (runtime claim) — not from a peer session. Runtime claim " + id + " released — the runtime is free again.\n"
		}
	}
	event := p.HookEventName
	if event == "" {
		event = "PostToolUse"
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": event, "additionalContext": fmt.Sprintf("<pigtail-runtime-claim id=%q phase=%q>\n%s</pigtail-runtime-claim>", id, phase, text)}})
	return 0
}

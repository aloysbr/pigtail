package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func waitsOf(t *testing.T, c *pigtail.Client, id string) []pigtail.SessionWait {
	t.Helper()
	// Read as the master: a hook run outside any cell registers a session
	// with no scope, which only the master sees (D17).
	ps, err := c.WithScope(string(scope.Master())).Peers(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.SessionID == id {
			return p.Waits
		}
	}
	return nil
}

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// Real hook payloads (shapes of Claude Code 2.1.28x, no private data) record
// the automatic wait; the hook prints nothing — a PreToolUse or
// PermissionRequest hook's stdout could be read as a decision.
func TestWaitHookRecordsAutomaticWait(t *testing.T) {
	c, _ := startRelayDaemon(t)
	cases := []struct {
		payload, typ, reason string
	}{
		{`{"session_id":"s-auto","cwd":"/w","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make"}}`, "permission", "Bash"},
		{`{"session_id":"s-auto","hook_event_name":"Notification","message":"Claude needs your permission to use Edit","notification_type":"permission_prompt"}`, "permission", "Edit"},
		{`{"session_id":"s-auto","hook_event_name":"Notification","message":"x","notification_type":"elicitation_dialog"}`, "question", ""},
		{`{"session_id":"s-auto","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which one?","header":"Pick lib"}]}}`, "question", "Pick lib"},
		{`{"session_id":"s-auto","hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"x"}}`, "review", "Plan"},
	}
	for _, cs := range cases {
		var out bytes.Buffer
		runWaitHook(strings.NewReader(cs.payload), &out, noEnv)
		if out.Len() != 0 {
			t.Fatalf("hook printed %q", out.String())
		}
		// The first payload also registers the session the daemon did not know.
		ws := waitsOf(t, c, "s-auto")
		if len(ws) != 1 || ws[0].Source != "auto" || ws[0].Type != cs.typ || ws[0].Reason != cs.reason {
			t.Fatalf("%s: waits = %+v", cs.payload, ws)
		}
	}
	// A reminder is not a wait: nothing changes.
	runWaitHook(strings.NewReader(`{"session_id":"s-auto","hook_event_name":"Notification","notification_type":"idle_prompt","message":"x"}`), &bytes.Buffer{}, noEnv)
	if ws := waitsOf(t, c, "s-auto"); len(ws) != 1 || ws[0].Type != "review" {
		t.Fatalf("idle_prompt changed the wait: %+v", ws)
	}
}

// UserPromptSubmit clears every wait — and, without a marker, never reaches
// the daemon.
func TestWaitHookPromptClears(t *testing.T) {
	c, dir := startRelayDaemon(t)
	runWaitHook(strings.NewReader(`{"session_id":"s-p","hook_event_name":"PermissionRequest","tool_name":"Bash"}`), &bytes.Buffer{}, noEnv)
	marker := filepath.Join(dir, "wait", "s-p")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no wait marker: %v", err)
	}
	prompt := `{"session_id":"s-p","hook_event_name":"UserPromptSubmit","prompt":"ok"}`

	// Fast path: marker gone (pretend), the daemon is not called.
	_ = os.Rename(marker, marker+".hidden")
	runWaitHook(strings.NewReader(prompt), &bytes.Buffer{}, noEnv)
	if ws := waitsOf(t, c, "s-p"); len(ws) != 1 {
		t.Fatalf("cleared without a marker: %+v", ws)
	}
	_ = os.Rename(marker+".hidden", marker)

	runWaitHook(strings.NewReader(prompt), &bytes.Buffer{}, noEnv)
	if ws := waitsOf(t, c, "s-p"); len(ws) != 0 {
		t.Fatalf("prompt did not clear: %+v", ws)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker left: %v", err)
	}
}

// The hook never fails its caller: no daemon, garbage, off switch.
func TestWaitHookFailsOpen(t *testing.T) {
	old := flagSocket
	flagSocket = filepath.Join(t.TempDir(), "none.sock")
	t.Cleanup(func() { flagSocket = old })
	for _, in := range []string{
		`{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
		`not json`, ``, `{"hook_event_name":"PermissionRequest"}`,
		`{"session_id":"../x","hook_event_name":"UserPromptSubmit"}`,
	} {
		var out bytes.Buffer
		runWaitHook(strings.NewReader(in), &out, noEnv) // returns, never panics
		if out.Len() != 0 {
			t.Fatalf("printed %q", out.String())
		}
	}
	c, _ := startRelayDaemon(t)
	runWaitHook(strings.NewReader(`{"session_id":"s-off","hook_event_name":"PermissionRequest","tool_name":"Bash"}`), &bytes.Buffer{}, envOf(map[string]string{"PIGTAIL_WAIT": "off"}))
	if ws := waitsOf(t, c, "s-off"); len(ws) != 0 {
		t.Fatalf("PIGTAIL_WAIT=off still recorded: %+v", ws)
	}
}

// `pigtail session wait` end to end against the relay: the session names
// itself, a declaration is recorded and replaced, --clear removes it.
func TestSessionWaitCommand(t *testing.T) {
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-decl"})
	var out bytes.Buffer
	if err := runSessionWait(context.Background(), &out, env, []string{"arbitrage", "Sidebar", "pigtail"}, false); err != nil {
		t.Fatal(err)
	}
	ws := waitsOf(t, c, "s-decl")
	if len(ws) != 1 || ws[0].Source != "declared" || ws[0].Type != "arbitrage" || ws[0].Reason != "Sidebar pigtail" {
		t.Fatalf("waits = %+v", ws)
	}
	if !strings.Contains(out.String(), "Decision") {
		t.Fatalf("output %q", out.String())
	}
	if err := runSessionWait(context.Background(), &bytes.Buffer{}, env, []string{"review", "PR #9 ready"}, false); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-decl"); len(ws) != 1 || ws[0].Type != "review" {
		t.Fatalf("not replaced: %+v", ws)
	}
	if err := runSessionWait(context.Background(), &bytes.Buffer{}, env, nil, true); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-decl"); len(ws) != 0 {
		t.Fatalf("not cleared: %+v", ws)
	}
}

func TestSessionWaitCommandRefusals(t *testing.T) {
	startRelayDaemon(t)
	err := runSessionWait(context.Background(), &bytes.Buffer{}, noEnv, []string{"review"}, false)
	if err == nil || !strings.Contains(err.Error(), "Claude Code session") {
		t.Fatalf("outside a session: %v", err)
	}
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-x"})
	err = runSessionWait(context.Background(), &bytes.Buffer{}, env, []string{"fini"}, false)
	if err == nil || !strings.Contains(err.Error(), "permission, question, arbitrage (or decision), review, blocked, done, working, wrapped") {
		t.Fatalf("bad type: %v", err)
	}
	if err := runSessionWait(context.Background(), &bytes.Buffer{}, env, nil, false); err == nil {
		t.Fatal("no type accepted")
	}
}

// A23: done and wrapped are declared like the other types, case-insensitive
// (output: D21·C5).
func TestSessionWaitDoneAndWrapped(t *testing.T) {
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-done"})
	var out bytes.Buffer
	if err := runSessionWait(context.Background(), &out, env, []string{"done", "x"}, false); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-done"); len(ws) != 1 || ws[0].Type != "done" || ws[0].Reason != "x" {
		t.Fatalf("waits = %+v", ws)
	}
	out.Reset()
	if err := runSessionWait(context.Background(), &out, env, []string{"Done"}, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "waiting: Done (cleared by the next prompt)\n" {
		t.Fatalf("output %q, want the pill text Done alone (no done command set)", out.String())
	}
	out.Reset()
	if err := runSessionWait(context.Background(), &out, env, []string{"wrapped", "x"}, false); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-done"); len(ws) != 1 || ws[0].Type != "wrapped" {
		t.Fatalf("waits = %+v", ws)
	}
	if out.String() != "waiting: Wrapped (cleared by the next prompt)\n" { // D21·C11
		t.Fatalf("output %q, want Wrapped without its reason", out.String())
	}
}

// withWaitCommands sets the user's own commands (settings.yaml) for one
// serial test and puts the previous ones back.
func withWaitCommands(t *testing.T, c waittype.Commands) {
	t.Helper()
	testutil.SerialOnly(t)
	prev := waittype.Configured()
	waittype.Configure(c)
	t.Cleanup(func() { waittype.Configure(prev) })
}

// With session.done_command set, a Done with no reason names it; a Mac wait
// with no reason names mac.release_command.
func TestSessionWaitHintsSet(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Done: "/finish", MacRelease: "/lend"})
	startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-hint"})
	var out bytes.Buffer
	if err := runSessionWait(context.Background(), &out, env, []string{"done"}, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "waiting: Done · /finish (cleared by the next prompt)\n" {
		t.Fatalf("done output %q", out.String())
	}
	out.Reset()
	if err := runSessionWaitOpts(context.Background(), &out, env, []string{"mac"}, sessionWaitOpts{HolderPID: 4243}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "waiting: Waiting for Mac · /lend (held until the wait helper exits)\n"; got != want {
		t.Fatalf("mac output %q, want %q", got, want)
	}
}

// working is declared like the other types; its output says it is the
// agent's turn, not a wait on the user.
func TestSessionWaitWorking(t *testing.T) {
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-work"})
	var out bytes.Buffer
	if err := runSessionWait(context.Background(), &out, env, []string{"working", "CI", "#9205"}, false); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-work"); len(ws) != 1 || ws[0].Source != "declared" || ws[0].Type != "working" || ws[0].Reason != "CI #9205" {
		t.Fatalf("waits = %+v", ws)
	}
	if got, want := out.String(), "working: Background · CI #9205 (cleared by your next turn or the next prompt)\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	cmd := sessionWaitCmd()
	if !strings.Contains(cmd.Use, "working") || !strings.Contains(cmd.Long, "working") {
		t.Fatalf("help misses working: %q / %q", cmd.Use, cmd.Long)
	}
}

// A24: the usage and the help name done and wrapped.
func TestSessionWaitHelpListsDoneAndWrapped(t *testing.T) {
	t.Parallel()
	cmd := sessionWaitCmd()
	for _, s := range []string{cmd.Use, cmd.Long} {
		if !strings.Contains(s, "done") || !strings.Contains(s, "wrapped") {
			t.Fatalf("help misses the done and wrapped types: %q", s)
		}
	}
}

// H1 probe: a StopFailure payload is logged — its keys, its error value, and
// what the transcript says at that instant — never a message text.
func TestWaitHookLogsStopFailureShape(t *testing.T) {
	startRelayDaemon(t) // isolated daemon: points flagSocket at a test socket (see below)
	logDir := t.TempDir()
	tr := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(tr, []byte(`{"type":"assistant","timestamp":"2026-10-05T00:01:00Z","error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"quotaLimits":{"resetsAt":1790159400}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s-limit","hook_event_name":"StopFailure","error":"rate_limit","error_details":"SECRET-DETAIL","last_assistant_message":"PRIVATE-TEXT","transcript_path":"` + tr + `"}`
	env := envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": logDir})
	var out bytes.Buffer
	runWaitHook(strings.NewReader(payload), &out, env)
	if out.Len() != 0 {
		t.Fatalf("hook printed %q", out.String())
	}
	raw, err := os.ReadFile(filepath.Join(logDir, "hooks.jsonl"))
	if err != nil {
		t.Fatalf("no probe line: %v", err)
	}
	if strings.Contains(string(raw), "SECRET-DETAIL") || strings.Contains(string(raw), "PRIVATE-TEXT") {
		t.Fatalf("the probe logged a message body: %s", raw)
	}
	var rec map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(raw), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["hook"] != "wait-stopfailure" || rec["outcome"] != "rate_limit" ||
		!strings.Contains(rec["detail"], "keys=error,error_details,hook_event_name,last_assistant_message,session_id,transcript_path") ||
		!strings.Contains(rec["detail"], "transcript_error=rate_limit") ||
		!strings.Contains(rec["detail"], "resets_at=1790159400") {
		t.Fatalf("probe = %+v", rec)
	}
}

// Real StopFailure shapes (Claude Code 2.1.289): the hook records the failure
// as an automatic wait; a rate limit carries the reset read from the transcript.
func TestWaitHookRecordsStopFailure(t *testing.T) {
	c, _ := startRelayDaemon(t)
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	tr := filepath.Join(t.TempDir(), "s.jsonl")
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"quotaLimits":{"resetsAt":%d}}`, time.Now().UTC().Format(time.RFC3339), reset.Unix())
	if err := os.WriteFile(tr, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": t.TempDir()})
	cases := []struct {
		payload, typ string
		until        bool
	}{
		{`{"session_id":"s-fail","hook_event_name":"StopFailure","error":"rate_limit","transcript_path":"` + tr + `"}`, "limit", true},
		{`{"session_id":"s-fail","hook_event_name":"StopFailure","error":"rate_limit"}`, "limit", false},
		{`{"session_id":"s-fail","hook_event_name":"StopFailure","error":"authentication_failed"}`, "login", false},
		{`{"session_id":"s-fail","hook_event_name":"StopFailure","error":"overloaded"}`, "api_error", false},
	}
	for _, cs := range cases {
		var out bytes.Buffer
		runWaitHook(strings.NewReader(cs.payload), &out, env)
		if out.Len() != 0 {
			t.Fatalf("hook printed %q", out.String())
		}
		ws := waitsOf(t, c, "s-fail")
		if len(ws) != 1 || ws[0].Source != "auto" || ws[0].Type != cs.typ || (ws[0].Until != nil) != cs.until {
			t.Fatalf("%s: waits = %+v", cs.payload, ws)
		}
		if cs.until && !ws[0].Until.Equal(reset) {
			t.Fatalf("until = %v, want %v", ws[0].Until, reset)
		}
	}
	// invalid_request and a subagent's failure change nothing.
	for _, p := range []string{
		`{"session_id":"s-fail","hook_event_name":"StopFailure","error":"invalid_request"}`,
		`{"session_id":"s-fail","hook_event_name":"StopFailure","error":"rate_limit","agent_id":"a1"}`,
	} {
		runWaitHook(strings.NewReader(p), &bytes.Buffer{}, env)
		if ws := waitsOf(t, c, "s-fail"); len(ws) != 1 || ws[0].Type != "api_error" {
			t.Fatalf("%s changed the wait: %+v", p, ws)
		}
	}
	// A2/D3 regression guard (need not be red on base): the next prompt clears
	// a Limit and a Login — the failure leaves a wait marker, so the
	// UserPromptSubmit fast path does reach the daemon.
	for _, e := range []string{"rate_limit", "authentication_failed"} {
		runWaitHook(strings.NewReader(`{"session_id":"s-fail","hook_event_name":"StopFailure","error":"`+e+`"}`), &bytes.Buffer{}, env)
		if ws := waitsOf(t, c, "s-fail"); len(ws) != 1 {
			t.Fatalf("%s: no wait recorded: %+v", e, ws)
		}
		runWaitHook(strings.NewReader(`{"session_id":"s-fail","hook_event_name":"UserPromptSubmit","prompt":"go"}`), &bytes.Buffer{}, env)
		if ws := waitsOf(t, c, "s-fail"); len(ws) != 0 {
			t.Fatalf("%s then UserPromptSubmit: waits left %+v", e, ws)
		}
	}
}

// Review Focus 1: a reset already past (an older limit's entry, the current
// one not yet written) is dropped — "Limit" alone, never a pill that ends at once.
func TestWaitHookStaleResetIsDropped(t *testing.T) {
	c, _ := startRelayDaemon(t)
	tr := filepath.Join(t.TempDir(), "s.jsonl")
	line := fmt.Sprintf(`{"type":"assistant","timestamp":"2026-09-23T10:10:18Z","error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"quotaLimits":{"resetsAt":%d}}`, time.Now().Add(-time.Hour).Unix())
	if err := os.WriteFile(tr, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWaitHook(strings.NewReader(`{"session_id":"s-stale","hook_event_name":"StopFailure","error":"rate_limit","transcript_path":"`+tr+`"}`), &bytes.Buffer{}, envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": t.TempDir()}))
	if ws := waitsOf(t, c, "s-stale"); len(ws) != 1 || ws[0].Type != "limit" || ws[0].Until != nil {
		t.Fatalf("waits = %+v", ws)
	}
}

// Probe (plan Task 11 Step 2): a Stop leaves one wait-stop line with the turn
// read from the transcript — never the message text — so the log tells
// whether a Stop follows a StopFailure of the same turn.
func TestWaitHookLogsStopShape(t *testing.T) {
	startRelayDaemon(t)
	logDir := t.TempDir()
	tr := turnTranscript(t, "pid-probe", time.Now().Add(-time.Minute))
	payload := `{"session_id":"s-stop","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"PRIVATE","transcript_path":"` + tr + `"}`
	runWaitHook(strings.NewReader(payload), &bytes.Buffer{}, envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": logDir}))
	raw, err := os.ReadFile(filepath.Join(logDir, "hooks.jsonl"))
	if err != nil {
		t.Fatalf("no probe line: %v", err)
	}
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("the probe logged the message: %s", raw)
	}
	var rec map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(raw), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["hook"] != "wait-stop" || !strings.Contains(rec["detail"], "turn_id=pid-probe") ||
		!strings.Contains(rec["detail"], "stop_hook_active=false") ||
		!strings.Contains(rec["detail"], "keys=hook_event_name,last_assistant_message,session_id,stop_hook_active,transcript_path") {
		t.Fatalf("probe = %+v", rec)
	}
	// the StopFailure line carries the turn too
	sf := `{"session_id":"s-stop","hook_event_name":"StopFailure","error":"server_error","transcript_path":"` + tr + `"}`
	runWaitHook(strings.NewReader(sf), &bytes.Buffer{}, envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": logDir}))
	raw, _ = os.ReadFile(filepath.Join(logDir, "hooks.jsonl"))
	if !strings.Contains(string(raw), `"hook":"wait-stopfailure"`) || strings.Count(string(raw), "turn_id=pid-probe") != 2 {
		t.Fatalf("stopfailure line without the turn: %s", raw)
	}
}

func turnTranscript(t *testing.T, promptID string, start time.Time) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	line := fmt.Sprintf(`{"type":"user","promptId":%q,"entrypoint":"cli","timestamp":%q,"message":{"role":"user","content":"go"}}`, promptID, start.UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func stopPayload(session, tr string) string {
	return `{"session_id":"` + session + `","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"x","transcript_path":"` + tr + `"}`
}

// D12 end to end: a Stop records the turn under the session's last skill,
// once per turn, and only when nothing was declared in it and it did not fail.
func TestWaitHookStopRecordsUndeclaredTurns(t *testing.T) {
	c, _, st := startRelayDaemonStore(t) // isolated daemon and its store
	env := envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": t.TempDir()})
	run := func(p string) { runWaitHook(strings.NewReader(p), &bytes.Buffer{}, env) }
	count := func() int {
		got, err := st.HandbacksBySkill(time.Now().Add(-time.Hour), scope.Any)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, g := range got {
			n += g.Count
		}
		return n
	}
	start := time.Now().Add(-time.Minute)

	// 1. Stop without declaration → 1 (with the last skill)
	run(`{"session_id":"s-a","hook_event_name":"PreToolUse","tool_name":"Skill","tool_input":{"skill":"plan-skill"}}`)
	run(stopPayload("s-a", turnTranscript(t, "pa1", start)))
	if n := count(); n != 1 {
		t.Fatalf("undeclared Stop: %d, want 1", n)
	}
	// A session registered outside any cell has no scope: its turns are the master's (D17).
	if got, _ := st.HandbacksBySkill(time.Now().Add(-time.Hour), scope.Master()); len(got) != 1 || got[0].Skill != "plan-skill" {
		t.Fatalf("the turn's skill = %+v", got)
	}
	// 2. a second Stop of the same turn → still 1
	run(strings.Replace(stopPayload("s-a", turnTranscript(t, "pa1", start)), `"stop_hook_active":false`, `"stop_hook_active":true`, 1))
	if n := count(); n != 1 {
		t.Fatalf("two Stops of one turn: %d, want 1", n)
	}
	// 3. Stop after a `session wait` in the turn → 0 more
	if _, err := c.SetSessionWait(context.Background(), "s-a", pigtail.SessionWaitRequest{Type: "review", Reason: "PR"}); err != nil {
		t.Fatal(err)
	}
	run(stopPayload("s-a", turnTranscript(t, "pa2", start.Add(30*time.Second))))
	if n := count(); n != 1 {
		t.Fatalf("declared turn counted: %d", n)
	}
	// 3b. declare → permission dialog → the loop clears the declaration (busy, !sawBefore) → Stop ⇒ 0 more
	if _, err := c.SetSessionWait(context.Background(), "s-a", pigtail.SessionWaitRequest{Type: "arbitrage", Reason: "D1"}); err != nil {
		t.Fatal(err)
	}
	run(`{"session_id":"s-a","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make"}}`)
	if _, err := c.ClearSessionWait(context.Background(), "s-a", pigtail.WaitSourceDeclared); err != nil { // what WaitTracker does
		t.Fatal(err)
	}
	run(stopPayload("s-a", turnTranscript(t, "pa2b", start.Add(40*time.Second))))
	if n := count(); n != 1 {
		t.Fatalf("declared turn counted after the loop cleared it: %d", n)
	}
	// 4. StopFailure then Stop of the same turn → 0 more
	tr := turnTranscript(t, "pa3", time.Now())
	run(`{"session_id":"s-a","hook_event_name":"StopFailure","error":"rate_limit","transcript_path":"` + tr + `"}`)
	run(stopPayload("s-a", tr))
	if n := count(); n != 1 {
		t.Fatalf("failed turn counted: %d", n)
	}
	// 4b. a non-interactive session (claude -p, entrypoint sdk-cli) records nothing (A9)
	pTr := filepath.Join(t.TempDir(), "p.jsonl")
	_ = os.WriteFile(pTr, []byte(fmt.Sprintf(`{"type":"user","promptId":"pp1","entrypoint":"sdk-cli","timestamp":%q,"message":{"role":"user","content":"go"}}`+"\n", time.Now().UTC().Format(time.RFC3339Nano))), 0o600)
	run(stopPayload("s-p", pTr))
	if n := count(); n != 1 {
		t.Fatalf("claude -p turn counted: %d", n)
	}
	// 5. a subagent's payload (A7) records nothing
	run(`{"session_id":"s-a","agent_id":"a1","hook_event_name":"Stop","transcript_path":"` + turnTranscript(t, "pa4", time.Now()) + `"}`)
	if n := count(); n != 1 {
		t.Fatalf("subagent Stop counted: %d", n)
	}
}

// pigtail session wait mac: a held wait, set by the wait helper itself.
func TestSessionWaitMacHeld(t *testing.T) {
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-mac"})
	ctx := context.Background()
	var out bytes.Buffer
	if err := runSessionWaitOpts(ctx, &out, env, []string{"mac"}, sessionWaitOpts{HolderPID: 4242}); err != nil {
		t.Fatal(err)
	}
	ws := waitsOf(t, c, "s-mac")
	if len(ws) != 1 || ws[0].Source != "held" || ws[0].Type != "mac" || ws[0].HolderPID != 4242 {
		t.Fatalf("waits = %+v", ws)
	}
	if got, want := out.String(), "waiting: Waiting for Mac (held until the wait helper exits)\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	// An ordinary clear (no source) spares it.
	if _, err := sessionClient(env).ClearSessionWait(ctx, "s-mac", ""); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-mac"); len(ws) != 1 {
		t.Fatalf("a bare clear ended the held wait: %+v", ws)
	}
	// A clear by another holder spares it; the holder's own ends it.
	if err := runSessionWaitOpts(ctx, &bytes.Buffer{}, env, []string{"mac"}, sessionWaitOpts{Clear: true, HolderPID: 1}); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-mac"); len(ws) != 1 {
		t.Fatalf("another holder's clear ended it: %+v", ws)
	}
	out.Reset()
	if err := runSessionWaitOpts(ctx, &out, env, []string{"mac"}, sessionWaitOpts{Clear: true, HolderPID: 4242}); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-mac"); len(ws) != 0 {
		t.Fatalf("not cleared: %+v", ws)
	}
	if out.String() != "wait cleared\n" {
		t.Fatalf("output %q", out.String())
	}
	// --clear mac without a pid ends it whoever holds it.
	_ = runSessionWaitOpts(ctx, &bytes.Buffer{}, env, []string{"mac"}, sessionWaitOpts{HolderPID: 7})
	if err := runSessionWaitOpts(ctx, &bytes.Buffer{}, env, []string{"mac"}, sessionWaitOpts{Clear: true}); err != nil {
		t.Fatal(err)
	}
	if ws := waitsOf(t, c, "s-mac"); len(ws) != 0 {
		t.Fatalf("--clear mac left %+v", ws)
	}
}

func TestSessionWaitMacUntil(t *testing.T) {
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-macfor"})
	before := time.Now()
	if err := runSessionWaitOpts(context.Background(), &bytes.Buffer{}, env, []string{"mac"}, sessionWaitOpts{HolderPID: 9, For: 62 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	ws := waitsOf(t, c, "s-macfor")
	if len(ws) != 1 || ws[0].Until == nil {
		t.Fatalf("waits = %+v", ws)
	}
	if d := ws[0].Until.Sub(before); d < 61*time.Minute || d > 63*time.Minute {
		t.Fatalf("until = now+%v, want about 62m", d)
	}
}

func TestSessionWaitMacRefusals(t *testing.T) {
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-macref"})
	ctx := context.Background()
	for name, tc := range map[string]struct {
		args []string
		o    sessionWaitOpts
	}{
		"mac without a holder pid":        {[]string{"mac"}, sessionWaitOpts{}},
		"--for outside mac":               {[]string{"review"}, sessionWaitOpts{For: time.Hour}},
		"--holder-pid outside mac":        {[]string{"review"}, sessionWaitOpts{HolderPID: 3}},
		"--holder-pid with a bare clear":  {nil, sessionWaitOpts{Clear: true, HolderPID: 3}},
		"--holder-pid with --clear other": {[]string{"working"}, sessionWaitOpts{Clear: true, HolderPID: 3}},
		"--clear working":                 {[]string{"working"}, sessionWaitOpts{Clear: true}},
		"--for with --clear":              {[]string{"mac"}, sessionWaitOpts{Clear: true, For: time.Hour}},
		"negative holder pid":             {[]string{"mac"}, sessionWaitOpts{HolderPID: -1}},
	} {
		if err := runSessionWaitOpts(ctx, &bytes.Buffer{}, env, tc.args, tc.o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ws := waitsOf(t, c, "s-macref"); len(ws) != 0 {
		t.Fatalf("a refusal still wrote: %+v", ws)
	}
}

// F14: --clear mac on a session the daemon does not know is a quiet success
// and never registers it.
func TestSessionWaitClearMacUnknownSession(t *testing.T) {
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-ghost"})
	var out bytes.Buffer
	if err := runSessionWaitOpts(context.Background(), &out, env, []string{"mac"}, sessionWaitOpts{Clear: true, HolderPID: 5}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "wait cleared\n" {
		t.Fatalf("output %q", out.String())
	}
	ps, err := c.WithScope(string(scope.Master())).Peers(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.SessionID == "s-ghost" {
			t.Fatalf("--clear mac registered the session: %+v", p)
		}
	}
}

func TestSessionWaitCommandFlagsAndHelp(t *testing.T) {
	t.Parallel()
	cmd := sessionWaitCmd()
	if !strings.Contains(cmd.Use, "mac") || !strings.Contains(cmd.Long, "wait-for-a-free-Mac helper") || !strings.Contains(cmd.Long, "held until it exits") {
		t.Fatalf("help misses mac: %q / %q", cmd.Use, cmd.Long)
	}
	for _, f := range []string{"holder-pid", "for"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("no --%s flag", f)
		}
	}
	if !strings.Contains(waitHookCmd().Long, "session.turnless_prompts") || !strings.Contains(waitHookCmd().Long, "session.done_command") {
		t.Fatal("claude-wait help misses the turnless prompts or the done command")
	}
}

// A held Mac wait outlives an ordinary prompt; a turnless prompt (one the
// settings name) with the marker present touches nothing; a declared wait
// still goes at an ordinary prompt.
func TestWaitHookPromptSparesHeldAndTurnless(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Turnless: []*regexp.Regexp{
		regexp.MustCompile(`^/?clip(?:\s+(?i:\d+|list|ls))?\s*$`),
		regexp.MustCompile(`(?s)^/(?:lend|reclaim)(?:\s+.*?)?\s*$`),
		regexp.MustCompile(`(?i)^/show(?:\s+(?:\d+|list|ls))?\s*$`),
	}})
	c, _ := startRelayDaemon(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-hh"})
	ctx := context.Background()
	runWaitHook(strings.NewReader(`{"session_id":"s-hh","hook_event_name":"PermissionRequest","tool_name":"Bash"}`), &bytes.Buffer{}, noEnv)
	if err := runSessionWaitOpts(ctx, &bytes.Buffer{}, env, []string{"mac"}, sessionWaitOpts{HolderPID: 4242}); err != nil {
		t.Fatal(err)
	}
	prompt := func(p string) {
		runWaitHook(strings.NewReader(fmt.Sprintf(`{"session_id":"s-hh","hook_event_name":"UserPromptSubmit","prompt":%q}`, p)), &bytes.Buffer{}, noEnv)
	}
	for _, p := range []string{"/clip", "/lend", "/reclaim", "/show list"} {
		prompt(p)
		if ws := waitsOf(t, c, "s-hh"); len(ws) != 2 {
			t.Fatalf("%q touched the waits: %+v", p, ws)
		}
	}
	prompt("go on")
	ws := waitsOf(t, c, "s-hh")
	if len(ws) != 1 || ws[0].Source != "held" {
		t.Fatalf("an ordinary prompt left %+v, want the held wait only", ws)
	}
}

func stopWithMessage(session, tr, msg string) string {
	m, _ := json.Marshal(msg)
	return `{"session_id":"` + session + `","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":` + string(m) + `,"transcript_path":"` + tr + `"}`
}

// A turn closed on a 🔄 line with nothing declared: pigtail declares the
// working wait itself (shown "Background · <reason>", not "Your turn"), and the
// turn still counts as handed back undeclared (D12).
func TestWaitHookStopInfersWorking(t *testing.T) {
	c, _, st := startRelayDaemonStore(t)
	logDir := t.TempDir()
	env := envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": logDir})
	run := func(p string) { runWaitHook(strings.NewReader(p), &bytes.Buffer{}, env) }
	count := func() int {
		got, err := st.HandbacksBySkill(time.Now().Add(-time.Hour), scope.Any)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, g := range got {
			n += g.Count
		}
		return n
	}
	start := time.Now().Add(-time.Minute)
	msg := "Done.\n\n---\n**Summary**\n\n🔄 Running — the build of the PR runs. Nothing to do on your side."

	// 1. 🔄 line, nothing declared → declared working with the line's reason, turn counted
	run(stopWithMessage("s-w", turnTranscript(t, "pw1", start), msg))
	ws := waitsOf(t, c, "s-w")
	if len(ws) != 1 || ws[0].Type != "working" || ws[0].Source != pigtail.WaitSourceDeclared || ws[0].Reason != "the build of the PR runs" {
		t.Fatalf("waits = %+v", ws)
	}
	if n := count(); n != 1 {
		t.Fatalf("inferred turn not counted as undeclared: %d", n)
	}
	raw, _ := os.ReadFile(filepath.Join(logDir, "hooks.jsonl"))
	if !strings.Contains(string(raw), "inferred_working=yes") || strings.Contains(string(raw), "the build") {
		t.Fatalf("log line: %s", raw)
	}
	// a second Stop of the same turn changes nothing
	run(stopWithMessage("s-w", turnTranscript(t, "pw1", start), msg))
	if n := count(); n != 1 {
		t.Fatalf("two Stops: %d", n)
	}

	// 2. the next turn ends without a 🔄 line nor a declaration → the stale working goes ("Your turn")
	run(stopWithMessage("s-w", turnTranscript(t, "pw2", time.Now().Add(10*time.Millisecond)), "Voilà. Rien à faire de ton côté."))
	if ws := waitsOf(t, c, "s-w"); len(ws) != 0 {
		t.Fatalf("stale working kept: %+v", ws)
	}
	if n := count(); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}

	// 3. a wait declared in the turn is kept, whatever the message says
	t3 := time.Now().Add(-time.Second)
	if _, err := c.SetSessionWait(context.Background(), "s-w", pigtail.SessionWaitRequest{Type: "review", Reason: "PR #9"}); err != nil {
		t.Fatal(err)
	}
	run(stopWithMessage("s-w", turnTranscript(t, "pw3", t3), msg))
	if ws := waitsOf(t, c, "s-w"); len(ws) != 1 || ws[0].Type != "review" || ws[0].Reason != "PR #9" {
		t.Fatalf("declared wait replaced: %+v", ws)
	}
	if n := count(); n != 2 {
		t.Fatalf("declared turn counted: %d", n)
	}

	// 4. no 🔄 line → nothing (a fresh session: "Your turn" as before)
	run(stopWithMessage("s-n", turnTranscript(t, "pn1", time.Now()), "Rien à faire de ton côté."))
	if ws := waitsOf(t, c, "s-n"); len(ws) != 0 {
		t.Fatalf("waits without a 🔄 line: %+v", ws)
	}
	raw, _ = os.ReadFile(filepath.Join(logDir, "hooks.jsonl"))
	if !strings.Contains(string(raw), "inferred_working=no") {
		t.Fatalf("log line without the flag: %s", raw)
	}

	// 5. a subagent's Stop never infers
	sub := strings.Replace(stopWithMessage("s-sub", turnTranscript(t, "ps1", time.Now()), msg), `{"session_id"`, `{"agent_id":"a1","session_id"`, 1)
	run(sub)
	if ws := waitsOf(t, c, "s-sub"); len(ws) != 0 {
		t.Fatalf("subagent Stop inferred: %+v", ws)
	}
}

// putStandby puts the session in Standby through the CLI (registering it),
// as the /standby skill does.
func putStandby(t *testing.T, session string) {
	t.Helper()
	freeBgDeps(t) // the background readings are the session's own: free here
	if err := runSessionStandby(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": session}), false, false); err != nil {
		t.Fatal(err)
	}
}

func standbyMarkerAt(s *standbyServer, session string) bool {
	_, err := os.Stat(filepath.Join(s.dir, "standby", session))
	return err == nil
}

// A prompt Ada types ends the Standby (one DELETE); the /compact pigtail
// types does not; without a marker the daemon is not called; a typed
// /standby is cleared (the skill sets it again). A /standby expansion only
// records the skill: one prompt, one DELETE, never two.
func TestWaitHookPromptEndsStandby(t *testing.T) {
	s := startStandbyServer(t)
	del := "DELETE /peers/s1/standby"
	prompt := func(p string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"session_id": "s1", "hook_event_name": "UserPromptSubmit", "prompt": p})
		runWaitHook(bytes.NewReader(b), &bytes.Buffer{}, noEnv)
	}

	putStandby(t, "s1")
	prompt("reprends")
	if s.count(del) != 1 || standbyMarkerAt(s, "s1") {
		t.Fatalf("reprends: calls %v marker=%v", s.calls(), standbyMarkerAt(s, "s1"))
	}

	putStandby(t, "s1")
	prompt("/compact")
	if s.count(del) != 1 || !standbyMarkerAt(s, "s1") {
		t.Fatalf("/compact ended the Standby: calls %v", s.calls())
	}

	if _, err := s.st.ClearStandby("s1", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(s.dir, "standby", "s1"))
	before := len(s.calls())
	prompt("reprends")
	if len(s.calls()) != before {
		t.Fatalf("no marker, still called: %v", s.calls()[before:])
	}

	putStandby(t, "s1")
	prompt("/standby")
	if s.count(del) != 2 {
		t.Fatalf("/standby typed: calls %v", s.calls())
	}

	putStandby(t, "s1")
	before = len(s.calls())
	runWaitHook(strings.NewReader(`{"session_id":"s1","hook_event_name":"UserPromptExpansion","command_name":"standby","expansion_type":"slash_command"}`), &bytes.Buffer{}, noEnv)
	if got := s.calls()[before:]; len(got) != 1 || got[0] != "POST /peers/s1/skill" {
		t.Fatalf("expansion: calls %v", got)
	}
}

// The dispatcher types into a workspace's active surface; only the session
// whose UserPromptSubmit carries its session_id leaves Standby.
func TestWaitHookPromptEndsOnlyItsSession(t *testing.T) {
	s := startStandbyServer(t)
	putStandby(t, "s1")
	putStandby(t, "s2")
	before := len(s.calls())
	runWaitHook(strings.NewReader(`{"session_id":"s2","hook_event_name":"UserPromptSubmit","prompt":"reprends"}`), &bytes.Buffer{}, noEnv)
	got := s.calls()[before:]
	if len(got) != 1 || got[0] != "DELETE /peers/s2/standby" {
		t.Fatalf("calls %v", got)
	}
	if !standbyMarkerAt(s, "s1") || standbyMarkerAt(s, "s2") {
		t.Fatalf("markers: s1=%v s2=%v", standbyMarkerAt(s, "s1"), standbyMarkerAt(s, "s2"))
	}
}

// stopBgPayload is the P17-shaped Stop payload fixture, its transcript path
// pointed at a one-turn transcript (the fixture alone cannot give a turn).
func stopBgPayload(t *testing.T, tr string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "stop-bg-payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(strings.TrimSpace(string(b)), "TRANSCRIPT", tr, 1)
}

func stubStopCallerPID(t *testing.T, pid int) {
	t.Helper()
	testutil.SerialOnly(t) // swaps stopCallerPID
	saved := stopCallerPID
	stopCallerPID = func() int { return pid }
	t.Cleanup(func() { stopCallerPID = saved })
}

// D42, D44: each main-session Stop records the crons of its payload (never the
// background tasks, a prompt or the reply), the caller pid and the turn that
// ended — even with the daemon down; nothing else writes it.
func TestStopRecordsBackgroundLists(t *testing.T) {
	startRelayDaemon(t)
	stubStopCallerPID(t, 4242)
	dir := t.TempDir()
	env := envOf(map[string]string{"PIGTAIL_BGWORK_DIR": dir})
	file := filepath.Join(dir, "s-bgrec")
	tr := turnTranscript(t, "pt1", time.Now().Add(-time.Minute))
	var out bytes.Buffer
	runWaitHook(strings.NewReader(stopBgPayload(t, tr)), &out, env)
	if out.Len() != 0 {
		t.Fatalf("hook printed %q", out.String())
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("no record: %v", err)
	}
	var r bgRecord
	if err := json.Unmarshal(b, &r); err != nil || r.PID != 4242 || r.Turn != "pt1" || r.At == 0 || r.Crons == nil || len(*r.Crons) != 1 || (*r.Crons)[0].ID != "bcef88fa" {
		t.Fatalf("record = %s (%v)", b, err)
	}
	for _, leak := range []string{"PRIVATE", "background_tasks", "b4sorpvo8", "monitor"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("record carries %q: %s", leak, b)
		}
	}
	// Daemon unreachable: the record is still written.
	_ = os.Remove(file)
	old := flagSocket
	flagSocket = filepath.Join(t.TempDir(), "dead.sock")
	runWaitHook(strings.NewReader(stopBgPayload(t, tr)), &bytes.Buffer{}, env)
	flagSocket = old
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("daemon down: no record: %v", err)
	}
	// A subagent's Stop, a StopFailure, a prompt: no write.
	_ = os.Remove(file)
	sub := strings.Replace(stopBgPayload(t, tr), `"hook_event_name":"Stop"`, `"agent_id":"a1","hook_event_name":"Stop"`, 1)
	runWaitHook(strings.NewReader(sub), &bytes.Buffer{}, env)
	fail := strings.Replace(stopBgPayload(t, tr), `"hook_event_name":"Stop"`, `"hook_event_name":"StopFailure","error":"server_error"`, 1)
	runWaitHook(strings.NewReader(fail), &bytes.Buffer{}, env)
	runWaitHook(strings.NewReader(`{"session_id":"s-bgrec","hook_event_name":"UserPromptSubmit","prompt":"x"}`), &bytes.Buffer{}, env)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a subagent Stop, a StopFailure or a prompt wrote the record")
	}
	// A Stop without session_crons: the record is written, its list absent.
	runWaitHook(strings.NewReader(stopPayload("s-bgrec", tr)), &bytes.Buffer{}, env)
	b, err = os.ReadFile(file)
	if err != nil {
		t.Fatalf("no record without the field: %v", err)
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Crons != nil {
		t.Fatalf("absent field recorded as a list: %s", b)
	}
	// Hooks off: nothing.
	_ = os.Remove(file)
	runWaitHook(strings.NewReader(stopBgPayload(t, tr)), &bytes.Buffer{}, envOf(map[string]string{"PIGTAIL_BGWORK_DIR": dir, "PIGTAIL_WAIT": "off"}))
	if _, err := os.Stat(file); err == nil {
		t.Fatal("PIGTAIL_WAIT=off wrote the record")
	}
	// A directory that cannot be written: no panic, nothing printed.
	notADir := filepath.Join(dir, "plain")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	runWaitHook(strings.NewReader(stopBgPayload(t, tr)), &out, envOf(map[string]string{"PIGTAIL_BGWORK_DIR": filepath.Join(notADir, "x")}))
	if out.Len() != 0 {
		t.Fatalf("hook printed %q", out.String())
	}
}

// The record the hook writes at a Stop is the one /standby trusts in the next
// turn: real writer, real reader, real previous-turn reading → one PUT.
func TestStopRecordThenStandbyRoundTrip(t *testing.T) {
	s := startStandbyServer(t)
	stubStopCallerPID(t, 4242)
	dir := t.TempDir()
	tr := turnTranscript(t, "rt1", time.Now().Add(-time.Minute))
	payload := strings.Replace(stopBgPayload(t, tr), `"session_crons":[{"id":"bcef88fa","schedule":"*/1 * * * *","recurring":true,"prompt":"PRIVATE-PROMPT"}]`, `"session_crons":[]`, 1)
	payload = strings.Replace(payload, `"s-bgrec"`, `"s-bg"`, 1)
	runWaitHook(strings.NewReader(payload), &bytes.Buffer{}, envOf(map[string]string{"PIGTAIL_BGWORK_DIR": dir}))
	f, err := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(stopSummaryLine + "\n")
	_, _ = f.WriteString(`{"type":"user","promptId":"rt2","entrypoint":"cli","timestamp":"2026-10-13T10:00:00Z","message":{"role":"user","content":"/standby"}}` + "\n")
	_ = f.Close()
	d := freeBgDeps(t)
	d.Transcript = func(string) (string, bool, error) { return tr, true, nil }
	d.PrevTurn = claudesession.PreviousTurnID
	d.Record = func(sid string, pid int, prev string) (bgRecord, error) { return readBgRecord(dir, sid, pid, prev) }
	if stderr, code, puts := standbyRun(t, s, nil, false, false); code != 0 || puts == 0 {
		t.Fatalf("round trip: stderr %q code %d puts %d", stderr, code, puts)
	}
}

// D-B (verify N2 of PR #210): the first turn of a new session, or after
// /clear or /compact, writes lines after its prompt heavier than 256 KiB.
// Its Stop still records the turn, and /standby on the next turn passes
// (it refused D33, "dernière fin de tour enregistrée pour un autre tour").
func TestStopRecordThenStandbyAfterHeavyTurn(t *testing.T) {
	s := startStandbyServer(t)
	stubStopCallerPID(t, 4242)
	dir := t.TempDir()
	tr := turnTranscript(t, "rt1", time.Now().Add(-time.Minute))
	f, err := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"system","subtype":"api-request-blob","content":"` + strings.Repeat("x", 384<<10) + `"}` + "\n")
	_ = f.Close()
	payload := strings.Replace(stopBgPayload(t, tr), `"session_crons":[{"id":"bcef88fa","schedule":"*/1 * * * *","recurring":true,"prompt":"PRIVATE-PROMPT"}]`, `"session_crons":[]`, 1)
	payload = strings.Replace(payload, `"s-bgrec"`, `"s-bg"`, 1)
	runWaitHook(strings.NewReader(payload), &bytes.Buffer{}, envOf(map[string]string{"PIGTAIL_BGWORK_DIR": dir}))
	f, err = os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(stopSummaryLine + "\n")
	_, _ = f.WriteString(`{"type":"user","promptId":"rt2","entrypoint":"cli","timestamp":"2026-10-13T10:00:00Z","message":{"role":"user","content":"/standby"}}` + "\n")
	_ = f.Close()
	d := freeBgDeps(t)
	d.Transcript = func(string) (string, bool, error) { return tr, true, nil }
	d.PrevTurn = claudesession.PreviousTurnID
	d.Record = func(sid string, pid int, prev string) (bgRecord, error) { return readBgRecord(dir, sid, pid, prev) }
	if stderr, code, puts := standbyRun(t, s, nil, false, false); code != 0 || puts == 0 {
		t.Fatalf("after a heavy turn: stderr %q code %d puts %d", stderr, code, puts)
	}
}

// stopSummaryLine is what Claude Code appends to the transcript once a turn's
// Stop hooks ran (keys of a real one): the mark of a turn that ended.
const stopSummaryLine = `{"isSidechain":false,"type":"system","subtype":"stop_hook_summary","hookCount":1,"hookInfos":[{"command":"wait-hook.sh"}],"hookErrors":[],"preventedContinuation":false,"stopReason":"","hasOutput":false,"level":"suggestion","timestamp":"2026-10-13T09:59:00Z"}`

// appendTranscript appends lines to the transcript at tr.
func appendTranscript(t *testing.T, tr string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, l := range lines {
		if _, err := f.WriteString(strings.TrimSpace(l) + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// D-B2 (verify N2 of PR #210, round 2): /compact between the Stop and
// /standby is a local command with a promptId of its own and no Stop. The
// record of the turn before it is still the one /standby trusts — it refused
// D33 once ("dernière fin de tour enregistrée pour un autre tour"). Positive
// control: a turn that did end with a Stop whose record was not written
// (another process, a lost write) still refuses as stale.
func TestStopRecordThenStandbyAfterCompact(t *testing.T) {
	compact, err := os.ReadFile(filepath.Join("..", "..", "internal", "claudesession", "testdata", "transcript-compact.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	standby := `{"type":"user","promptId":"rt2","entrypoint":"cli","timestamp":"2026-10-14T23:00:00Z","message":{"role":"user","content":"/standby"}}`
	run := func(t *testing.T, between ...string) (string, int, int) {
		s := startStandbyServer(t)
		stubStopCallerPID(t, 4242)
		dir := t.TempDir()
		tr := turnTranscript(t, "rt1", time.Now().Add(-time.Minute))
		payload := strings.Replace(stopBgPayload(t, tr), `"session_crons":[{"id":"bcef88fa","schedule":"*/1 * * * *","recurring":true,"prompt":"PRIVATE-PROMPT"}]`, `"session_crons":[]`, 1)
		payload = strings.Replace(payload, `"s-bgrec"`, `"s-bg"`, 1)
		runWaitHook(strings.NewReader(payload), &bytes.Buffer{}, envOf(map[string]string{"PIGTAIL_BGWORK_DIR": dir}))
		appendTranscript(t, tr, append(between, standby)...)
		d := freeBgDeps(t)
		d.Transcript = func(string) (string, bool, error) { return tr, true, nil }
		d.PrevTurn = claudesession.PreviousTurnID
		d.Record = func(sid string, pid int, prev string) (bgRecord, error) { return readBgRecord(dir, sid, pid, prev) }
		return standbyRun(t, s, nil, false, false)
	}
	t.Run("compact", func(t *testing.T) {
		// The fixture opens on rt1's stop_hook_summary, then the /compact lines.
		if stderr, code, puts := run(t, string(compact)); code != 0 || puts == 0 {
			t.Fatalf("after /compact: stderr %q code %d puts %d", stderr, code, puts)
		}
	})
	t.Run("stale", func(t *testing.T) {
		other := `{"type":"user","promptId":"rt1b","entrypoint":"cli","timestamp":"2026-10-14T22:59:30Z","message":{"role":"user","content":"go"}}`
		asst := `{"type":"assistant","timestamp":"2026-10-14T22:59:31Z","message":{"role":"assistant","content":[]}}`
		stderr, code, puts := run(t, stopSummaryLine, other, asst, stopSummaryLine)
		if code == 0 || puts != 0 || !strings.Contains(stderr, "dernière fin de tour enregistrée pour un autre tour") {
			t.Fatalf("a Stop'd turn with no record must refuse as stale: stderr %q code %d puts %d", stderr, code, puts)
		}
	})
}

// The hook sends what a dialog's payload carries (waittype.DialogDetails) as
// the auto wait's details; a permission_prompt notification carries none.
func TestWaitHookSendsDialogDetails(t *testing.T) {
	_, _, st := startRelayDaemonStore(t)
	runWaitHook(strings.NewReader(`{"session_id":"s-h1","cwd":"/w","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make"}}`), &bytes.Buffer{}, noEnv)
	r := openWait(t, st, "s-h1", pigtail.WaitSourceAuto)
	if r == nil || r.Type != "permission" || r.Details == nil || r.Details.Cmd != "make" || r.Details.Where != "/w" {
		t.Fatalf("permission record = %+v", r)
	}
	runWaitHook(strings.NewReader(`{"session_id":"s-h2","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which date lib?","header":"Date lib","options":[{"label":"a"},{"label":"b"}]}]}}`), &bytes.Buffer{}, noEnv)
	r = openWait(t, st, "s-h2", pigtail.WaitSourceAuto)
	if r == nil || r.Type != "question" || r.Details == nil || r.Details.Summary != "Which date lib?" || len(r.Details.Options) != 2 ||
		r.Details.Options[0].Label != "a" || r.Details.Options[1].Label != "b" {
		t.Fatalf("question record = %+v", r)
	}
	runWaitHook(strings.NewReader(`{"session_id":"s-h3","hook_event_name":"Notification","message":"Claude needs your permission to use Edit","notification_type":"permission_prompt"}`), &bytes.Buffer{}, noEnv)
	r = openWait(t, st, "s-h3", pigtail.WaitSourceAuto)
	if r == nil || r.Details != nil {
		t.Fatalf("permission_prompt record = %+v (no details expected)", r)
	}
}

// The done command (session.done_command) types no answer to the subject's
// wait — the hook's clear keeps it open; any other prompt clears it as before.
// With no done command set, every prompt clears it.
func TestWaitHookDonePromptKeepsSubjectWait(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Done: "/finish"})
	runDonePromptCases(t, []donePromptCase{
		{"s-w1", "/finish next", pigtail.WaitStateOpen},
		{"s-w2", "/finish", pigtail.WaitStateOpen},
		{"s-w3", "/finisher", pigtail.WaitStateCleared},
		{"s-w4", "ok", pigtail.WaitStateCleared},
	})
}

func TestWaitHookDonePromptUnset(t *testing.T) {
	withWaitCommands(t, waittype.Commands{})
	runDonePromptCases(t, []donePromptCase{
		{"s-u1", "/finish next", pigtail.WaitStateCleared},
		{"s-u2", "/finish", pigtail.WaitStateCleared},
	})
}

type donePromptCase struct{ session, prompt, want string }

func runDonePromptCases(t *testing.T, cases []donePromptCase) {
	t.Helper()
	_, dir, st := startRelayDaemonStore(t)
	for _, c := range cases {
		runSessionHook(strings.NewReader(fmt.Sprintf(`{"session_id":%q,"cwd":"/w","hook_event_name":"SessionStart"}`, c.session)), noEnv, false)
		tr, err := st.SetSessionWaitRecord(c.session, pigtail.SessionWait{Type: "arbitrage", Reason: "D1", Source: pigtail.WaitSourceDeclared},
			store.WaitMeta{Scope: "perso", SubjectID: "sub_s"})
		if err != nil || tr.OpenedID == "" {
			t.Fatalf("seed %s: %+v, %v", c.session, tr, err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "wait", c.session), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		runWaitHook(strings.NewReader(fmt.Sprintf(`{"session_id":%q,"hook_event_name":"UserPromptSubmit","prompt":%q}`, c.session, c.prompt)), &bytes.Buffer{}, noEnv)
		ws, err := st.SessionWaits(c.session)
		if err != nil || len(ws) != 0 {
			t.Fatalf("%q: rows left %+v, %v", c.prompt, ws, err)
		}
		r, err := st.GetWaitRecord(tr.OpenedID)
		if err != nil || r.State != c.want {
			t.Fatalf("%q: subject's wait = %+v, %v, want %s", c.prompt, r, err, c.want)
		}
	}
}

// --- account rotation (sub_x000000000000008, Task 8) ------------------------

type rotationStart struct {
	sid, surface string
	until        time.Time
	afterPID     int
}

// stubStartRotation records the worker starts (never a real process); err is
// what the start returns.
func stubStartRotation(t *testing.T, err error) *[]rotationStart {
	t.Helper()
	testutil.SerialOnly(t) // swaps startRotation
	var got []rotationStart
	saved := startRotation
	startRotation = func(sid, surface string, until time.Time, afterPID int) error {
		got = append(got, rotationStart{sid, surface, until, afterPID})
		return err
	}
	t.Cleanup(func() { startRotation = saved })
	return &got
}

// limitTranscript is a turn of entrypoint ep (none: no readable turn) that
// ended on a rate limit resetting at reset.
func limitTranscript(t *testing.T, ep string, reset time.Time) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var lines []string
	if ep != "" {
		lines = append(lines, fmt.Sprintf(`{"type":"user","promptId":"p1","entrypoint":%q,"timestamp":%q,"message":{"role":"user","content":"go"}}`, ep, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)))
	}
	lines = append(lines, fmt.Sprintf(`{"type":"assistant","timestamp":%q,"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"quotaLimits":{"resetsAt":%d}}`, time.Now().UTC().Format(time.RFC3339), reset.Unix()))
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func limitPayload(tr, agent string) string {
	a := ""
	if agent != "" {
		a = `,"agent_id":"` + agent + `"`
	}
	return `{"session_id":"s-rot","hook_event_name":"StopFailure","error":"rate_limit","transcript_path":"` + tr + `"` + a + `}`
}

func rotEnv(logDir, surface string, extra ...string) func(string) string {
	m := map[string]string{"CLAUDE_HOOK_LOG_DIR": logDir, "CMUX_SURFACE_ID": surface}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return envOf(m)
}

func TestWaitHookStartsRotation(t *testing.T) {
	c, _ := startRelayDaemon(t)
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	got := stubStartRotation(t, nil)
	var seenWait bool
	saved := startRotation
	startRotation = func(sid, surface string, until time.Time, afterPID int) error {
		ws := waitsOf(t, c, "s-rot")
		seenWait = len(ws) == 1 && ws[0].Type == "limit" && ws[0].Until != nil && ws[0].Until.Equal(reset)
		return saved(sid, surface, until, afterPID)
	}
	runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, "cli", reset), "")), &bytes.Buffer{}, rotEnv(t.TempDir(), "3a23d5dd-59ee-471d-9270-32f0fb06e099"))
	if len(*got) != 1 {
		t.Fatalf("starts = %+v, want one", *got)
	}
	s := (*got)[0]
	if s.sid != "s-rot" || s.surface != "3a23d5dd-59ee-471d-9270-32f0fb06e099" || !s.until.Equal(reset) || s.afterPID != os.Getpid() {
		t.Fatalf("start = %+v", s)
	}
	if !seenWait {
		t.Fatal("the Limit wait was not posted before the worker started")
	}
}

func TestWaitHookNoRotationForSubagent(t *testing.T) {
	startRelayDaemon(t)
	got := stubStartRotation(t, nil)
	tr := limitTranscript(t, "cli", time.Now().Add(time.Hour))
	runWaitHook(strings.NewReader(limitPayload(tr, "a1")), &bytes.Buffer{}, rotEnv(t.TempDir(), rotSurface))
	if len(*got) != 0 {
		t.Fatalf("a subagent's limit started a rotation: %+v", *got)
	}
}

func TestWaitHookNoRotationForNonInteractive(t *testing.T) {
	c, _ := startRelayDaemon(t)
	got := stubStartRotation(t, nil)
	for _, ep := range []string{"sdk-cli", "sdk-ts", ""} {
		reset := time.Now().Add(time.Hour).Truncate(time.Second)
		logDir := t.TempDir()
		runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, ep, reset), "")), &bytes.Buffer{}, rotEnv(logDir, rotSurface))
		if len(*got) != 0 {
			t.Fatalf("%q: rotation started: %+v", ep, *got)
		}
		ws := waitsOf(t, c, "s-rot")
		if len(ws) != 1 || ws[0].Type != "limit" || ws[0].Until == nil || strings.HasPrefix(ws[0].Reason, "rotation: ") {
			t.Fatalf("%q: waits = %+v, want the base Limit", ep, ws)
		}
		if b, _ := os.ReadFile(filepath.Join(logDir, "hooks.jsonl")); strings.Contains(string(b), `"hook":"rotate"`) {
			t.Fatalf("%q: a rotate line for a non-interactive session: %s", ep, b)
		}
	}
}

func TestWaitHookNoRotationWithoutSurface(t *testing.T) {
	startRelayDaemon(t)
	got := stubStartRotation(t, nil)
	runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, "cli", time.Now().Add(time.Hour)), "")), &bytes.Buffer{}, rotEnv(t.TempDir(), ""))
	if len(*got) != 0 {
		t.Fatalf("starts = %+v", *got)
	}
}

func TestWaitHookNoRotationForPastReset(t *testing.T) {
	startRelayDaemon(t)
	got := stubStartRotation(t, nil)
	runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, "cli", time.Now().Add(-time.Hour)), "")), &bytes.Buffer{}, rotEnv(t.TempDir(), rotSurface))
	if len(*got) != 0 {
		t.Fatalf("starts = %+v", *got)
	}
}

func TestWaitHookNoRotationForOtherErrors(t *testing.T) {
	startRelayDaemon(t)
	got := stubStartRotation(t, nil)
	tr := turnTranscript(t, "p1", time.Now().Add(-time.Minute))
	for _, e := range []string{"authentication_failed", "overloaded", "server_error", "invalid_request"} {
		runWaitHook(strings.NewReader(`{"session_id":"s-rot","hook_event_name":"StopFailure","error":"`+e+`","transcript_path":"`+tr+`"}`), &bytes.Buffer{}, rotEnv(t.TempDir(), rotSurface))
	}
	if len(*got) != 0 {
		t.Fatalf("starts = %+v", *got)
	}
}

func TestWaitHookRotationStartFailureLogged(t *testing.T) {
	startRelayDaemon(t)
	stubStartRotation(t, errors.New("fork: resource temporarily unavailable"))
	logDir := t.TempDir()
	var out bytes.Buffer
	runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, "cli", time.Now().Add(time.Hour)), "")), &out, rotEnv(logDir, rotSurface))
	if out.Len() != 0 {
		t.Fatalf("hook printed %q", out.String())
	}
	b, _ := os.ReadFile(filepath.Join(logDir, "hooks.jsonl"))
	if !strings.Contains(string(b), `"hook":"rotate"`) || !strings.Contains(string(b), `"outcome":"start-failed"`) || !strings.Contains(string(b), "fork: resource") {
		t.Fatalf("hooks.jsonl = %s", b)
	}
}

func TestRotationOffHook(t *testing.T) {
	startRelayDaemon(t)
	got := stubStartRotation(t, nil)
	runWaitHook(strings.NewReader(limitPayload(limitTranscript(t, "cli", time.Now().Add(time.Hour)), "")), &bytes.Buffer{}, rotEnv(t.TempDir(), rotSurface, envRotation, "off"))
	if len(*got) != 0 {
		t.Fatalf("starts = %+v", *got)
	}
}

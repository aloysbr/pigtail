package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type hookRun struct {
	out     map[string]any
	logRecs []map[string]string
}

func runHook(t *testing.T, env map[string]string, payload map[string]any) hookRun {
	t.Helper()
	logDir := t.TempDir()
	get := func(k string) string {
		if k == "CLAUDE_HOOK_LOG_DIR" {
			return logDir
		}
		return env[k]
	}
	in, _ := json.Marshal(payload)
	var stdout bytes.Buffer
	runClaudeBashHook(bytes.NewReader(in), &stdout, get, func() time.Time { return time.Unix(0, 0) })
	var r hookRun
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &r.out); err != nil {
			t.Fatalf("stdout is not JSON: %q", stdout.String())
		}
	}
	data, _ := os.ReadFile(filepath.Join(logDir, "hooks.jsonl"))
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]string
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		r.logRecs = append(r.logRecs, rec)
	}
	return r
}

func bash(command string, extra map[string]any) map[string]any {
	in := map[string]any{"command": command, "description": "type-check"}
	for k, v := range extra {
		in[k] = v
	}
	return map[string]any{
		"session_id": "0123456789abcdef", "cwd": "/tmp", "permission_mode": "auto",
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": in,
	}
}

func TestHookRewriteInAutoMode(t *testing.T) {
	t.Parallel()
	r := runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, bash("npx tsc --noEmit | head -50", nil))
	hso, _ := r.out["hookSpecificOutput"].(map[string]any)
	if hso == nil {
		t.Fatal("no hookSpecificOutput")
	}
	if _, ok := hso["permissionDecision"]; ok {
		t.Fatal("the hook must never decide the permission")
	}
	in := hso["updatedInput"].(map[string]any)
	want := "pigtail admit --class=tsc --max-wait=8m0s -- npx tsc --noEmit | head -50"
	if in["command"] != want {
		t.Fatalf("command = %q\nwant      %q", in["command"], want)
	}
	if in["description"] != "type-check" {
		t.Fatal("other tool_input fields must survive the round trip")
	}
	if in["timeout"] != float64(600000) {
		t.Fatalf("an unset timeout must be raised to 600000, got %v", in["timeout"])
	}
	if !strings.Contains(hso["additionalContext"].(string), "Exit code 75") {
		t.Fatal("the agent must be told what exit 75 means")
	}
	if len(r.logRecs) != 1 || r.logRecs[0]["outcome"] != "rewritten" || r.logRecs[0]["hook"] != "admit-heavy" ||
		r.logRecs[0]["sid"] != "01234567" || r.logRecs[0]["detail"] != "classes=tsc cmds=npx" {
		t.Fatalf("log = %+v", r.logRecs)
	}
}

func TestHookKeepsAnExplicitTimeout(t *testing.T) {
	t.Parallel()
	r := runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, bash("npx eslint .", map[string]any{"timeout": 300000}))
	in := r.out["hookSpecificOutput"].(map[string]any)["updatedInput"].(map[string]any)
	if in["timeout"] != float64(300000) || !strings.Contains(in["command"].(string), "--max-wait=4m0s") {
		t.Fatalf("timeout=%v command=%v, want 300000 kept and a 4 min wait", in["timeout"], in["command"])
	}
}

func TestHookObserveIsTheDefault(t *testing.T) {
	t.Parallel()
	r := runHook(t, map[string]string{}, bash("npx tsc", nil))
	if r.out != nil {
		t.Fatalf("observe must not change anything, got %v", r.out)
	}
	if len(r.logRecs) != 1 || r.logRecs[0]["outcome"] != "would-rewrite" || r.logRecs[0]["detail"] != "classes=tsc cmds=npx" {
		t.Fatalf("log = %+v", r.logRecs)
	}
}

func TestHookOnlyActsInAllowedPermissionModes(t *testing.T) {
	t.Parallel()
	p := bash("npx tsc", nil)
	p["permission_mode"] = "default"
	r := runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, p)
	if r.out != nil || r.logRecs[0]["outcome"] != "skip-mode" {
		t.Fatalf("default mode: out=%v log=%+v", r.out, r.logRecs)
	}
	r = runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite", "PIGTAIL_ADMIT_HOOK_MODES": "default,auto"}, p)
	if r.out == nil {
		t.Fatal("PIGTAIL_ADMIT_HOOK_MODES must widen the modes")
	}
}

func TestHookDenyNamesTheCommand(t *testing.T) {
	t.Parallel()
	r := runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "deny"}, bash("yarn exec tsc", nil))
	hso := r.out["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" || !strings.Contains(hso["permissionDecisionReason"].(string), "pigtail admit --class=tsc") {
		t.Fatalf("deny output = %v", hso)
	}
}

func TestHookBackgroundCommands(t *testing.T) {
	t.Parallel()
	r := runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, bash("npx jest", map[string]any{"run_in_background": true}))
	in := r.out["hookSpecificOutput"].(map[string]any)["updatedInput"].(map[string]any)
	if _, ok := in["timeout"]; ok || !strings.Contains(in["command"].(string), "--max-wait=30m0s") {
		t.Fatalf("background: %v", in)
	}
}

func TestHookLeavesEverythingElseAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		env     map[string]string
		payload map[string]any
		logged  string
	}{
		{"light command", map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, bash("git status && ls", nil), ""},
		{"off", map[string]string{"PIGTAIL_ADMIT_HOOK": "off"}, bash("npx tsc", nil), ""},
		{"other tool", map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/x"}}, ""},
		{"already routed", map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, bash("pigtail admit -- npx tsc", nil), "nested"},
		{"opted out", map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, bash("PIGTAIL_ADMIT=0 npx tsc", nil), "optout"},
		{"unparsed", map[string]string{"PIGTAIL_ADMIT_HOOK": "rewrite"}, bash("echo $(npx tsc)", nil), "unparsed"},
		{"unknown mode", map[string]string{"PIGTAIL_ADMIT_HOOK": "yolo"}, bash("npx tsc", nil), "error"},
	} {
		r := runHook(t, tc.env, tc.payload)
		if r.out != nil {
			t.Errorf("%s: must not output anything, got %v", tc.name, r.out)
		}
		switch {
		case tc.logged == "" && len(r.logRecs) != 0:
			t.Errorf("%s: must not log, got %+v", tc.name, r.logRecs)
		case tc.logged != "" && (len(r.logRecs) != 1 || r.logRecs[0]["outcome"] != tc.logged):
			t.Errorf("%s: log = %+v, want outcome %q", tc.name, r.logRecs, tc.logged)
		}
	}
}

func TestHookFailsOpenOnGarbage(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	runClaudeBashHook(strings.NewReader("not json"), &stdout, func(string) string { return "rewrite" }, time.Now)
	if stdout.Len() != 0 {
		t.Fatalf("garbage in must mean nothing out, got %q", stdout.String())
	}
}

func TestHookBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		timeoutMS  int64
		bg         bool
		newTimeout int64
		wait       time.Duration
	}{
		{0, false, 600000, 8 * time.Minute},
		{600000, false, 0, 8 * time.Minute},
		{300000, false, 0, 4 * time.Minute},
		{60000, false, 0, 30 * time.Second}, // floored
		{0, true, 0, 30 * time.Minute},
	}
	for _, c := range cases {
		nt, w := hookBudget(c.timeoutMS, c.bg)
		if nt != c.newTimeout || w != c.wait {
			t.Errorf("hookBudget(%d, %v) = %d, %s; want %d, %s", c.timeoutMS, c.bg, nt, w, c.newTimeout, c.wait)
		}
	}
}

// An unparsed line must leave a usable trace: why, and on which command.
// The observe run of 2026-09-23 logged 26 of these with an empty detail,
// which made the whole observation window undecidable.
func TestHookLogsWhyItDeclined(t *testing.T) {
	t.Parallel()
	cases := []struct{ command, want string }{
		{"cat <<EOF\nnpx tsc\nEOF", "reason=heredoc cmd=cat"},
		{"npx tsc $(git rev-parse --show-toplevel)", "reason=command-substitution cmd=npx"},
		{"bash -c 'npx tsc'", "reason=shell-c cmd=bash"},
		{"eval npx tsc", "reason=eval cmd=eval"},
		{"xargs npx tsc < list", "reason=xargs cmd=xargs"},
		{"find . -name '*.ts' -exec tsc {} +", "reason=find-exec cmd=find"},
		{"$TOOL --noEmit", "reason=var-command cmd=?"},
	}
	for _, c := range cases {
		r := runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "observe"}, bash(c.command, nil))
		if len(r.logRecs) != 1 || r.logRecs[0]["outcome"] != "unparsed" || r.logRecs[0]["detail"] != c.want {
			t.Errorf("%q: log = %+v, want outcome unparsed and detail %q", c.command, r.logRecs, c.want)
		}
	}
}

// The log must not be able to hold a secret passed as an argument.
func TestHookLogNeverCarriesArguments(t *testing.T) {
	t.Parallel()
	const secret = "ghp_AbCdEf0123456789SECRET"
	commands := []string{
		"gh auth login --with-token <<EOF\n" + secret + "\nEOF",
		"bash -c 'curl -H \"Authorization: Bearer " + secret + "\" https://api'",
		"eval \"export NPM_TOKEN=" + secret + "\"",
		"psql 'postgres://u:" + secret + "@h/db' -c 'select 1' | npx tsc",
		"TOKEN=" + secret + " npx tsc `hostname`",
	}
	for _, command := range commands {
		r := runHook(t, map[string]string{"PIGTAIL_ADMIT_HOOK": "observe"}, bash(command, nil))
		for _, rec := range r.logRecs {
			for field, v := range rec {
				if strings.Contains(v, secret) {
					t.Fatalf("%q leaked into the log field %q: %q", secret, field, v)
				}
				if field == "detail" && v != "" && !strings.HasPrefix(v, "reason=") && !strings.HasPrefix(v, "classes=") {
					t.Fatalf("a detail must be reason/cmd or classes/cmds only, got %q", v)
				}
			}
		}
	}
}

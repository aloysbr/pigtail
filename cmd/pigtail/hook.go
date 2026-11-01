package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/heavycmd"
)

// Hook modes, from PIGTAIL_ADMIT_HOOK.
const (
	hookOff     = "off"
	hookObserve = "observe" // log what would be rewritten, change nothing
	hookRewrite = "rewrite" // route heavy commands through `pigtail admit`
	hookDeny    = "deny"    // refuse, naming the command to run instead
)

const (
	// hookDefaultMode is observe: a new hook first proves, from its own log,
	// what it would do before it is allowed to do it.
	hookDefaultMode = hookObserve
	// hookDefaultPermissionModes are the Claude Code permission modes in
	// which rewrite/deny act. Claude Code evaluates permissions on the
	// *rewritten* command (measured 2026-09-22, CLI 2.1.259): an allowlist
	// rule like Bash(npx tsc:*) stops matching `pigtail admit … -- npx tsc`.
	// In auto and bypassPermissions no allowlist is consulted, so a rewrite
	// costs nothing; in the other modes it would turn an allowed command into
	// a prompt (or, under dontAsk, a refusal). There the hook only logs.
	hookDefaultPermissionModes = "auto,bypassPermissions"

	// bashMaxTimeout is the most a Bash call may ask for. A call that sets
	// no timeout gets Claude Code's default of 120 s, too short to queue in.
	bashMaxTimeout = 600 * time.Second
	// hookWaitCeiling bounds the queue wait of a foreground command, and the
	// margin keeps the command itself some time inside the Bash timeout.
	hookWaitCeiling = 8 * time.Minute
	hookWaitMargin  = 60 * time.Second
	hookWaitFloor   = 30 * time.Second
	// hookBackgroundWait: a background command is not bound by the Bash
	// timeout, so it may wait longer.
	hookBackgroundWait = 30 * time.Minute
)

func hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Agent hooks (called by the agent, not by hand)",
		Hidden: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "claude-bash",
		Short: "Claude Code PreToolUse hook: route heavy Bash commands through pigtail admit",
		Long: `Claude Code PreToolUse hook for the Bash tool. Reads the hook payload on
stdin; for every heavy command in it (tsc, eslint, jest, vitest, nx, storybook
build — directly or through npx/npm/yarn/pnpm scripts) it inserts
"pigtail admit --class=… --max-wait=… --" before the command.

PIGTAIL_ADMIT_HOOK selects what it does:
  off      nothing
  observe  (default) log what it would rewrite, change nothing
  rewrite  return the rewritten command (updatedInput); never decides the
           permission — Claude Code's own rules apply to the rewritten command
  deny     refuse, naming the exact command to run instead

rewrite and deny act only in the permission modes of
PIGTAIL_ADMIT_HOOK_MODES (default "auto,bypassPermissions"); elsewhere they
log, because a rewritten command no longer matches the allowlist.

Decisions are appended to $CLAUDE_HOOK_LOG_DIR/hooks.jsonl (default
~/.claude/logs). Fails open: any error leaves the command untouched.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runClaudeBashHook(os.Stdin, os.Stdout, os.Getenv, time.Now)
			return nil
		},
	})
	addRelayHooks(cmd) // session-message relay, see hook_relay.go
	cmd.AddCommand(&cobra.Command{
		Use:   "claude-maintenance",
		Short: "Claude Code UserPromptSubmit/PostToolUse hook: deliver the system maintenance notice once per phase",
		Long: `Reads the maintenance state set with ` + "`pigtail maintenance on|off`" + ` and injects
its notice as additionalContext at most once per <id>:<phase> (on, late,
corrupt, off) for the session; the lift only to a session that saw that
maintenance. The single judge and the single writer of the witness
<dir>/maintenance-seen/<session>. Subagent payloads are skipped. Exits 0,
except 1 when the per-session lock cannot be taken (the cause is the last
stderr line; the hook script then raises its failure alert).`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if rc := runMaintenanceHook(os.Stdin, os.Stdout, os.Stderr, os.Getenv, time.Now); rc != 0 {
				os.Exit(rc) // no cobra "Error:" line: the cause must stay the last stderr line
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "claude-runtime-claim",
		Short: "Claude Code UserPromptSubmit/PostToolUse hook: deliver the runtime claim notice once per lease",
		Long: `Reads the lease set with ` + "`pigtail runtime claim|release`" + ` and injects its notice as
additionalContext once per lease (on) for every other main session, then once
at the release (off) to a session that saw it. The holder and subagents never
get it. The single writer of the witness <dir>/runtime-claim-seen/<session>.
Exits 0, except 1 when the per-session lock or the witness cannot be used, or
the lease file is unreadable (the cause is the last stderr line;
the hook script then raises its failure alert).`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if rc := runRuntimeClaimHook(os.Stdin, os.Stdout, os.Stderr, os.Getenv, time.Now); rc != 0 {
				os.Exit(rc) // the cause must stay the last stderr line
			}
			return nil
		},
	})
	return cmd
}

// hookPayload is the part of Claude Code's PreToolUse payload the hook reads.
// tool_input is kept raw so fields this code does not know survive the
// round trip into updatedInput.
type hookPayload struct {
	SessionID      string                     `json:"session_id"`
	AgentID        string                     `json:"agent_id"`
	Cwd            string                     `json:"cwd"`
	PermissionMode string                     `json:"permission_mode"`
	ToolName       string                     `json:"tool_name"`
	ToolInput      map[string]json.RawMessage `json:"tool_input"`
}

// runClaudeBashHook never fails: whatever goes wrong, the command is left as
// the agent wrote it.
func runClaudeBashHook(stdin io.Reader, stdout io.Writer, getenv func(string) string, now func() time.Time) {
	mode := strings.TrimSpace(getenv("PIGTAIL_ADMIT_HOOK"))
	if mode == "" {
		mode = hookDefaultMode
	}
	if mode == hookOff {
		return
	}
	var p hookPayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil || p.ToolName != "Bash" {
		return
	}
	var command string
	if raw, ok := p.ToolInput["command"]; !ok || json.Unmarshal(raw, &command) != nil || command == "" {
		return
	}
	var timeoutMS int64
	if raw, ok := p.ToolInput["timeout"]; ok {
		_ = json.Unmarshal(raw, &timeoutMS)
	}
	var background bool
	if raw, ok := p.ToolInput["run_in_background"]; ok {
		_ = json.Unmarshal(raw, &background)
	}
	newTimeoutMS, maxWait := hookBudget(timeoutMS, background)

	cwd := p.Cwd
	if cwd == "" {
		cwd = heavycmd.Getwd()
	}
	res := heavycmd.Rewrite(command, cwd, maxWait)
	log := hookLogger{getenv: getenv, now: now, sid: p.SessionID, agent: p.AgentID, cwd: cwd}
	if res.Outcome != heavycmd.OutcomeRewritten {
		if res.Outcome != heavycmd.OutcomePass {
			log.write(string(res.Outcome), declinedDetail(res))
		}
		return
	}
	detail := routedDetail(res)

	allowed := false
	modes := getenv("PIGTAIL_ADMIT_HOOK_MODES")
	if modes == "" {
		modes = hookDefaultPermissionModes
	}
	for _, m := range strings.Split(modes, ",") {
		if strings.TrimSpace(m) == p.PermissionMode {
			allowed = true
		}
	}
	switch {
	case mode == hookObserve:
		log.write("would-rewrite", detail)
		return
	case mode != hookRewrite && mode != hookDeny:
		log.write("error", "unknown PIGTAIL_ADMIT_HOOK="+mode)
		return
	case !allowed:
		log.write("skip-mode", p.PermissionMode+": "+detail)
		return
	}

	note := "pigtail's memory admission routed this command through `pigtail admit`: it starts once " +
		"the machine has the memory for one more heavy command, then runs unchanged. " +
		"Exit code 75 from it means the machine stayed short of memory for the whole wait budget; " +
		"the command itself did not run."
	out := map[string]any{"hookEventName": "PreToolUse"}
	if mode == hookDeny {
		out["permissionDecision"] = "deny"
		out["permissionDecisionReason"] = "Heavy command: run it through pigtail's memory admission instead, exactly as: " + res.Line
		log.write("denied", detail)
	} else {
		input := make(map[string]json.RawMessage, len(p.ToolInput)+1)
		for k, v := range p.ToolInput {
			input[k] = v
		}
		input["command"], _ = json.Marshal(res.Line)
		if newTimeoutMS > 0 {
			input["timeout"], _ = json.Marshal(newTimeoutMS)
		}
		// Deliberately no permissionDecision: "allow" would auto-approve
		// whatever else the line runs (`tsc && rm -rf …`). Claude Code's own
		// rules still judge the rewritten command.
		out["updatedInput"] = input
		out["additionalContext"] = note
		log.write("rewritten", detail)
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": out})
}

// routedDetail describes a rewrite in the two terms that are safe to write
// down: the classes routed and the command words they were routed on.
//
// Not the rewritten line, tempting as it is in observe mode: a line carries
// its arguments, and one command's secret would be logged because *another*
// command on the same line was heavy (`psql '…password…' | npx tsc`). To see
// the literal rewrite of a given line, run `pigtail hook claude-bash` on it
// by hand; what the log has to answer is narrower — which commands the hook
// acts on, and whether those are the right ones.
func routedDetail(res heavycmd.Result) string {
	return "classes=" + classList(res.Classes) + " cmds=" + strings.Join(res.Commands, ",")
}

// declinedDetail describes a line the router would not touch, in the only
// two terms that are safe to write down: why it declined, and the command
// word it declined on.
//
// Never the line, never its arguments. The log is a plain file read later by
// a human or another agent; a token passed as an argument — a `--with-token`,
// a connection string, an Authorization header — must not be able to land in
// it. That is also why the command word itself is reduced to a bare basename
// (heavycmd.Rewrite does it) rather than the path as typed.
func declinedDetail(res heavycmd.Result) string {
	if res.Reason == "" {
		return ""
	}
	cmd := res.Command
	if cmd == "" {
		cmd = "?"
	}
	return "reason=" + res.Reason.String() + " cmd=" + cmd
}

// hookBudget returns the Bash timeout to set (0 = leave it alone) and the
// admission wait that fits inside it.
func hookBudget(timeoutMS int64, background bool) (newTimeoutMS int64, maxWait time.Duration) {
	if background {
		return 0, hookBackgroundWait
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		// Without this the agent's command would be killed at the default
		// 120 s while still queued, then retried — losing its place.
		timeout = bashMaxTimeout
		newTimeoutMS = timeout.Milliseconds()
	}
	maxWait = timeout - hookWaitMargin
	if maxWait > hookWaitCeiling {
		maxWait = hookWaitCeiling
	}
	if maxWait < hookWaitFloor {
		maxWait = hookWaitFloor
	}
	return newTimeoutMS, maxWait
}

func classList(cs []heavycmd.Class) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = string(c)
	}
	return strings.Join(parts, ",")
}

// hookLogger appends one JSON line per decision, in the schema the other
// hooks of this machine already write (hook, outcome, sid, wt, detail): every
// outcome is logged, not only the rewrites, so the log can say how often the
// hook acts relative to how often it is asked. Silent and fail-open.
type hookLogger struct {
	hook       string // the "hook" field; "admit-heavy" when empty
	getenv     func(string) string
	now        func() time.Time
	sid, agent string
	cwd        string
}

func (l hookLogger) write(outcome, detail string) {
	dir := l.getenv("CLAUDE_HOOK_LOG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir = filepath.Join(home, ".claude", "logs")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	sid := l.sid
	if len(sid) > 8 {
		sid = sid[:8]
	}
	if l.agent != "" {
		sid += "." + l.agent
	}
	wt, _ := admissionGroup(l.cwd)
	rec, err := json.Marshal(map[string]string{
		"ts":      l.now().Format("2006-01-02T15:04:05-0700"),
		"hook":    l.hookName(),
		"outcome": outcome,
		"sid":     sid,
		"wt":      wt,
		"detail":  truncateDetail(detail),
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "hooks.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(rec, '\n'))
	_ = f.Close()
}

func (l hookLogger) hookName() string {
	if l.hook != "" {
		return l.hook
	}
	return "admit-heavy"
}

func truncateDetail(s string) string {
	const max = 400
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

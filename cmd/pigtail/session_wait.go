package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Typed waits (spec "Sidebar pigtail" §4.8): what a Claude session waits for
// from its user, shown by the cmux sidebar loop in place of the generic
// "Waiting for you". Two writers, both acting as the session (sessionClient):
//
//	pigtail session wait <type> "<reason>"   the agent, before handing back
//	pigtail hook claude-wait                 Claude Code hooks, automatically

// sessionWaitLong is the help of `pigtail session wait` (D21·C2).
const sessionWaitLong = `Records what this Claude Code session waits for. The cmux sidebar shows it on
the session's workspace while the session is idle; the next prompt clears it.
A turn ended with nothing declared shows "Your turn" — unless its last message
closes on a line starting with 🔄: the Stop hook then declares working itself,
the reason read from that line (the turn still counts as undeclared).

Types:
  arbitrage   a choice between options you laid out (shown "Decision";
              "decision" is accepted too)
  review      a result to look at: a visual review, a PR ready, a spec
  question    information you lack, asked in prose
  blocked     an action only the user can take: a login, a secret, an access
  permission  an authorisation (usually typed by the hooks automatically)
  done        the work is finished; only closing the session is left
              (shown "Done", or "Done · <command>" when settings.yaml names
              session.done_command and no reason is given)
  working     the turn ended but your background work goes on (shells,
              agents, a Monitor, a CI watch): shown "Background · <reason>" in
              blue instead of "Your turn"; your next turn clears it too
  wrapped     the closing ran and left the tab open; only closing the tab
              is left (neither done nor wrapped, nor typing the done
              command, closes an arbitrage, question, review or blocked left
              open on your subject: its answer reaches the subject's next
              session)
  mac         set by the wait-for-a-free-Mac helper itself; you never declare it by hand;
              held until it exits (shown "Waiting for Mac", plus
              settings.yaml mac.release_command when set);
              needs --holder-pid, takes --for

The reason is a few words ("PR #142 ready"); it shows in the pill only when
it fits. Permission dialogs and AskUserQuestion are detected by the hooks:
nothing to declare for them. Run from inside a Claude Code session
(CLAUDE_CODE_SESSION_ID).

--details <file|-> adds a typed content to the wait, as JSON (a file, or - for
stdin): title, summary, step, url, shots, plus by type — arbitrage: options
(id, label, detail, one reco) and why; question: options (no reco), other;
review: items (label, detail, url, shots), or url and lead. The daemon checks
it (16 KiB at most, no secret); the output then names the wait (pigtail wait
show <id>). The user answers it with pigtail wait answer; you read the answer as a
message from them. One example per type:

  arbitrage   {"title":"Sidebar order","options":[{"id":"A","label":"Keep","reco":true},
               {"id":"B","label":"Drop"}],"why":"cheaper to undo"}
  question    {"summary":"Which branch?","options":[{"id":"main","label":"main"}],"other":"another name"}
  review      {"items":[{"label":"Header aligned"},{"label":"Dark mode","detail":"check contrast"}]}
              or {"url":"http://localhost:5173","lead":"the new header"}
  blocked     {"summary":"Log in to the staging console"}
  permission  {"cmd":"make check","where":"/path/to/worktree"}  (shown only)

"kind" (the wait's type) and "schema" (1) are optional; the daemon fills them
and refuses a kind that is not the type or a schema it does not read:
  {"kind":"arbitrage","schema":1,"options":[{"id":"A","label":"Keep","reco":true},{"id":"B","label":"Drop"}]}

--clear withdraws the declaration; --clear mac [--holder-pid N] ends the held
Mac wait (only if N still holds it).`

func sessionWaitCmd() *cobra.Command {
	var o sessionWaitOpts
	cmd := &cobra.Command{
		Use:   "wait <arbitrage|review|blocked|question|permission|done|working|wrapped|mac> [reason]",
		Short: "Say what this Claude session waits for from you, before handing back",
		Long:  sessionWaitLong, // D21·C2, verbatim
		Args: func(_ *cobra.Command, args []string) error {
			if o.Clear && len(args) > 1 {
				return errors.New("--clear takes at most one argument (mac)")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			return runSessionWaitOpts(c.Context(), c.OutOrStdout(), os.Getenv, args, o)
		},
	}
	cmd.Flags().BoolVar(&o.Clear, "clear", false, "withdraw this session's declared wait (--clear mac: its held Mac wait)")
	cmd.Flags().IntVar(&o.HolderPID, "holder-pid", 0, "mac only: the process that holds the wait (required to set it)")
	cmd.Flags().DurationVar(&o.For, "for", 0, "mac only: the hold ends on its own after this long")
	cmd.Flags().StringVar(&o.Details, "details", "", "the wait's typed content, as JSON: a file, or - for stdin")
	return cmd
}

// sessionWaitOpts are the flags of `pigtail session wait`.
type sessionWaitOpts struct {
	Clear     bool
	HolderPID int
	For       time.Duration
	// Details is --details: a JSON file, or "-" for Stdin (nil = os.Stdin).
	Details string
	Stdin   io.Reader
}

func runSessionWait(ctx context.Context, out io.Writer, getenv func(string) string, args []string, clear bool) error {
	return runSessionWaitOpts(ctx, out, getenv, args, sessionWaitOpts{Clear: clear})
}

func runSessionWaitOpts(ctx context.Context, out io.Writer, getenv func(string) string, args []string, o sessionWaitOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if o.HolderPID < 0 || o.For < 0 {
		return errors.New("--holder-pid and --for take positive values")
	}
	mac := len(args) > 0 && strings.EqualFold(args[0], string(waittype.Mac))
	if o.Clear {
		if len(args) > 1 || (len(args) == 1 && !mac) {
			return errors.New("--clear takes no argument, or mac")
		}
		if !mac && o.HolderPID != 0 {
			return errors.New("--holder-pid goes with mac only")
		}
		if o.For != 0 {
			return errors.New("--for goes with setting a mac wait, not with --clear")
		}
	} else if !mac && (o.HolderPID != 0 || o.For != 0) {
		return errors.New("--holder-pid and --for go with mac only")
	}
	var details []byte
	if o.Details != "" {
		if o.Clear || mac {
			return errors.New("--details goes with declaring a wait, not with --clear or mac")
		}
		b, err := readDetails(o.Details, o.Stdin)
		if err != nil {
			return err
		}
		details = b
	}
	me, err := resolveSelf(readSelfEnv(getenv), nil)
	if err != nil {
		return err
	}
	c := sessionClient(getenv)
	if o.Clear {
		// A session the daemon does not know holds nothing: quiet success,
		// never a registration (F14).
		switch {
		case mac && o.HolderPID > 0:
			_, err = c.ClearHeldWait(ctx, me, o.HolderPID)
		case mac:
			_, err = c.ClearSessionWait(ctx, me, pigtail.WaitSourceHeld)
		default:
			_, err = c.ClearSessionWait(ctx, me, pigtail.WaitSourceDeclared)
		}
		if err != nil && !isNotFound(err) {
			return err
		}
		fmt.Fprintln(out, "wait cleared")
		return nil
	}
	if len(args) == 0 {
		return errors.New(`say what you wait for: pigtail session wait <arbitrage|review|blocked|question|permission|done|working|wrapped> "<reason>"`)
	}
	if mac {
		return setMacWait(ctx, out, c, getenv, me, args[1:], o)
	}
	t, err := waittype.Parse(args[0])
	if err != nil {
		return err
	}
	req := pigtail.SessionWaitRequest{Type: string(t), Reason: strings.Join(args[1:], " "), Source: pigtail.WaitSourceDeclared, Details: details}
	p, err := c.SetSessionWait(ctx, me, req)
	if isNotFound(err) {
		// Not registered yet (its SessionStart ran while the daemon was
		// down): register it as itself, then retry once, same request.
		registerSession(ctx, relayHookPayload{SessionID: me}, getenv)
		p, err = c.SetSessionWait(ctx, me, req)
	}
	if err != nil {
		return err
	}
	named := ""
	if len(details) > 0 && p != nil {
		for _, w := range p.Waits {
			if w.Source == pigtail.WaitSourceDeclared && w.ID != "" {
				named = " (wait " + w.ID + ")"
			}
		}
	}
	label := t.Label()
	r := strings.TrimSpace(req.Reason)
	if r == "" && t == waittype.Done {
		r = waittype.DoneHint()
	}
	if r != "" && t != waittype.Wrapped { // Wrapped shows no reason (spec § 1; D21·C11)
		label += " · " + r
	}
	if t == waittype.Working {
		fmt.Fprintf(out, "working: %s (cleared by your next turn or the next prompt)%s\n", label, named)
		return nil
	}
	fmt.Fprintf(out, "waiting: %s (cleared by the next prompt)%s\n", label, named)
	return nil
}

// setMacWait records the held Mac wait (the wait helper runs it as itself).
func setMacWait(ctx context.Context, out io.Writer, c *pigtail.Client, getenv func(string) string, me string, reason []string, o sessionWaitOpts) error {
	t, err := waittype.ParseHeld(string(waittype.Mac))
	if err != nil {
		return err
	}
	if o.HolderPID <= 0 {
		return errors.New("a mac wait needs the process that holds it: --holder-pid <pid>")
	}
	req := pigtail.SessionWaitRequest{Type: string(t), Reason: strings.Join(reason, " "), Source: pigtail.WaitSourceHeld, HolderPID: o.HolderPID}
	if o.For > 0 {
		u := time.Now().Add(o.For)
		req.Until = &u
	}
	_, err = c.SetSessionWait(ctx, me, req)
	if isNotFound(err) {
		registerSession(ctx, relayHookPayload{SessionID: me}, getenv)
		_, err = c.SetSessionWait(ctx, me, req)
	}
	if err != nil {
		return err
	}
	r := strings.TrimSpace(req.Reason)
	if r == "" {
		r = waittype.MacHint()
	}
	label := t.Label()
	if r != "" {
		label += " · " + r
	}
	fmt.Fprintf(out, "waiting: %s (held until the wait helper exits)\n", label)
	return nil
}

// --- hook ---------------------------------------------------------------------

func waitHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "claude-wait",
		Short: "Claude Code hook: record what the session waits for (permission, question, review), clear it at the next prompt",
		Long: `Reads the hook payload on stdin and translates it (internal/waittype):
PermissionRequest and Notification permission_prompt → permission, except for
AskUserQuestion (→ question) and ExitPlanMode (→ review);
Notification elicitation_dialog / elicitation_url_dialog → question;
PreToolUse AskUserQuestion → question, ExitPlanMode → review;
StopFailure rate_limit → limit (reset from the transcript's quotaLimits.resetsAt),
authentication_failed|billing_error|oauth_org_not_allowed → login,
server_error|overloaded → api_error; any other error, or a subagent's, does nothing;
UserPromptSubmit → clear this session's turn waits (auto, declared), except a
prompt a user hook answers without a turn (settings.yaml
session.turnless_prompts): those, and a held Mac wait, stand; the done command
(session.done_command) clears them but keeps the subject's wait. It also ends
the session's Standby (marker <socket dir>/standby/<session>), except /compact
(typed by pigtail), /exit and those turnless prompts.
PreToolUse Skill / UserPromptExpansion slash_command → the session's last
skill; Stop → a turn end, counted when nothing was declared in the turn and
no StopFailure came (D12); when nothing was declared and last_assistant_message
holds a line starting with 🔄, the daemon declares working, the reason taken
from that line after its dash (cut at the sentence, a few words); an
undeclared Stop without one ends a working wait an earlier turn left;
interactive sessions only (transcript entrypoint cli or claude-desktop);
subagent payloads ignored (A7). Anything else does nothing.
A permission_prompt that names no tool does not replace a question or review
still standing.

Fails open: whatever goes wrong, it exits 0 and prints nothing. The
UserPromptSubmit path only stats the session's wait marker
(<socket dir>/wait/<session>) when nothing waits. PIGTAIL_RELAY=off or
PIGTAIL_WAIT=off disables it.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			runWaitHook(os.Stdin, c.OutOrStdout(), os.Getenv)
			return nil
		},
	}
}

// waitMarkerDir must agree with the daemon's RelayConfig.WaitMarkerDir: the
// directory of the socket, plus "wait".
func waitMarkerDir(getenv func(string) string) string {
	if d := getenv("PIGTAIL_WAIT_DIR"); d != "" {
		return d
	}
	if s := socketPath(); s != "" {
		return filepath.Join(filepath.Dir(s), "wait")
	}
	return filepath.Join(setup.ConfigDir(), "wait")
}

// stopCallerPID is the Claude process running this hook (0 unknown); a seam.
var stopCallerPID = func() int {
	p, ok, err := claudesession.CallerClaude(os.Getpid())
	if err != nil || !ok {
		return 0
	}
	return p.PID
}

// runWaitHook never fails and never writes to stdout: a PreToolUse or
// PermissionRequest hook's stdout may be read by Claude Code as a decision.
func runWaitHook(stdin io.Reader, _ io.Writer, getenv func(string) string) {
	defer func() { _ = recover() }() // a hook must never break the session
	if relayOff(getenv, "PIGTAIL_RELAY") || relayOff(getenv, "PIGTAIL_WAIT") {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return
	}
	var in waittype.HookInput
	if json.Unmarshal(raw, &in) != nil || !validMarkerKey(in.SessionID) {
		return
	}
	if in.HookEventName == "UserPromptSubmit" && inStandby(getenv, in.SessionID) && standbyEnds(in.Prompt) {
		// A prompt typed ends this session's Standby (D5, D16): its own
		// session_id only, never its workspace's other sessions.
		postAsSession(getenv, in, func(ctx context.Context, c *pigtail.Client) error { return c.ClearSessionStandby(ctx, in.SessionID) })
	}
	turnEnd := in.HookEventName == "Stop" || in.HookEventName == "StopFailure"
	var turn claudesession.Turn
	var haveTurn bool
	var working bool
	var workingReason string
	if turnEnd {
		var turnErr error
		turn, turnErr = claudesession.ReadLastTurn(in.TranscriptPath)
		haveTurn = turnErr == nil
		if in.HookEventName == "Stop" && in.AgentID == "" {
			// D42, D44: the session's crons and wakeups as this Stop lists
			// them, for /standby's next turn; written before any daemon call.
			_ = recordStopBackground(bgWorkDir(getenv), raw, in.SessionID, stopCallerPID(), turn.ID, turnErr, time.Now())
		}
		if in.HookEventName == "Stop" && in.AgentID == "" {
			workingReason, working = waittype.WorkingLine(in.LastAssistantMessage)
		}
		logTurnEnd(raw, turn.ID, working, getenv) // probes H1 (Task 1) and Stop-after-StopFailure (Task 11): shape only, never a text
	}
	if turnEnd && in.AgentID != "" {
		if in.HookEventName == "Stop" {
			return // A7: a subagent's end is not the session's turn
		}
	} else if sk := waittype.SkillOf(in); sk != "" {
		postAsSession(getenv, in, func(ctx context.Context, c *pigtail.Client) error { return c.SetSessionSkill(ctx, in.SessionID, sk) })
		return // a Skill call or a slash expansion types no wait
	} else if turnEnd {
		// A9: claude -p and SDK runs are not turns handed back.
		if interactiveTurn(turn, haveTurn) {
			if in.HookEventName == "Stop" && in.AgentID == "" {
				// The handoff draft is written before any daemon call, so it
				// exists even when pigtaild is down while the session dies.
				recordHandoffDraft(getenv, in, turn)
			}
			req := pigtail.SessionTurnRequest{TurnID: turn.ID, TurnStart: turn.Start, Event: pigtail.TurnEventStop, Working: working, Reason: workingReason}
			if in.HookEventName == "StopFailure" {
				req = pigtail.SessionTurnRequest{TurnID: turn.ID, TurnStart: turn.Start, Event: pigtail.TurnEventFailure}
			}
			// The daemon decides the 🔄 inference: it alone knows whether the
			// turn declared a wait (session_last_declared), in the same request
			// that records the turn end.
			postAsSession(getenv, in, func(ctx context.Context, c *pigtail.Client) error {
				return c.PostSessionTurn(ctx, in.SessionID, req)
			})
		}
		if in.HookEventName == "Stop" {
			// The anticipatory switch (sub_x000000000000023): past the
			// account's switch threshold, move at this turn end.
			maybeRotateAhead(in, turn, haveTurn, working, getenv)
			return
		}
	}
	res := waittype.FromHook(in) // StopFailure goes on to its wait (Task 4); a subagent's types none (A7)
	if res.Action == waittype.ActionNone {
		return
	}
	if res.Action == waittype.ActionClear {
		ctx, cancel := context.WithTimeout(context.Background(), relayHookTimeout)
		defer cancel()
		if _, err := os.Stat(filepath.Join(waitMarkerDir(getenv), in.SessionID)); err != nil {
			return // fast path: this session waits for nothing
		}
		if res.KeepSubject {
			// The done command answers nothing of the subject's wait.
			_, _ = sessionClient(getenv).ClearSessionWaitKeepingSubject(ctx, in.SessionID)
			return
		}
		_, _ = sessionClient(getenv).ClearSessionWait(ctx, in.SessionID, "")
		return
	}
	req := pigtail.SessionWaitRequest{Type: string(res.Type), Reason: res.Reason, Source: pigtail.WaitSourceAuto}
	if d := waittype.DialogDetails(in); d != nil {
		if b, err := json.Marshal(d); err == nil {
			req.Details = b // what the dialog's payload carries; the daemon drops it if invalid
		}
	}
	if res.Type == waittype.Limit {
		if e, ok := claudesession.LastAPIError(in.TranscriptPath); ok && e.Error == "rate_limit" && e.ResetsAt.After(time.Now()) {
			u := e.ResetsAt
			req.Until = &u // A1; a reset already past is an older limit's: dropped
		}
	}
	postAsSession(getenv, in, func(ctx context.Context, c *pigtail.Client) error {
		_, err := c.SetSessionWait(ctx, in.SessionID, req)
		return err
	})
	// The account rotation (sub_x000000000000008): an interactive session in a
	// cmux pane stopped by a usage limit with a known reset moves to the
	// account the picker chooses, in a detached worker that waits for this
	// hook to end. Its Limit wait above stays until the worker clears it.
	surface := getenv("CMUX_SURFACE_ID")
	if res.Type == waittype.Limit && in.AgentID == "" && interactiveTurn(turn, haveTurn) &&
		surface != "" && req.Until != nil && !rotationOff(getenv) {
		if err := startRotation(in.SessionID, surface, *req.Until, os.Getpid()); err != nil {
			hookLogger{hook: "rotate", getenv: getenv, now: time.Now, sid: in.SessionID, cwd: in.Cwd}.write("start-failed", err.Error())
		}
	}
}

// interactiveTurn reports whether the turn read from the transcript is one
// handed back to a person (A9): claude -p and SDK runs are not, and an
// unreadable turn proves nothing.
func interactiveTurn(turn claudesession.Turn, ok bool) bool {
	return ok && (turn.Entrypoint == "cli" || turn.Entrypoint == "claude-desktop")
}

// startRotation starts the detached rotation worker (pigtail session rotate);
// a seam, so that no test ever forks one.
var startRotation = func(sid, surface string, until time.Time, afterPID int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_, err = startDetachedWorkerCmd(self, rotateWorkerArgv(sid, surface, until, afterPID), nil, rotateLogPath(sid))
	return err
}

// postAsSession runs call as the hook's session; a session the daemon does
// not know yet is registered from the payload, then call runs once more.
// Errors are dropped: a hook fails open.
func postAsSession(getenv func(string) string, in waittype.HookInput, call func(context.Context, *pigtail.Client) error) {
	ctx, cancel := context.WithTimeout(context.Background(), relayHookTimeout)
	defer cancel()
	c := sessionClient(getenv)
	if err := call(ctx, c); isNotFound(err) {
		registerSession(ctx, relayHookPayload{SessionID: in.SessionID, Cwd: in.Cwd, HookEventName: in.HookEventName}, getenv)
		_ = call(ctx, c)
	}
}

// logTurnEnd appends one line per Stop (wait-stop) or StopFailure
// (wait-stopfailure) to hooks.jsonl: the payload's keys and the turn read from
// the transcript, plus for a StopFailure its error value and the transcript's
// last API error at this instant — the facts H1 needs, and whether a Stop
// follows a StopFailure of the same turn; for a Stop, whether its message
// closes on a 🔄 line (inferred_working). Never error_details,
// last_assistant_message nor any other text.
func logTurnEnd(raw []byte, turnID string, working bool, getenv func(string) string) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	detail := "keys=" + strings.Join(keys, ",")
	if str("agent_id") != "" {
		detail += " agent=yes"
	}
	if turnID == "" {
		turnID = "none"
	}
	detail += " turn_id=" + turnID
	log := hookLogger{getenv: getenv, now: time.Now, sid: str("session_id"), agent: str("agent_id"), cwd: str("cwd")}
	if str("hook_event_name") == "Stop" {
		var active bool
		_ = json.Unmarshal(m["stop_hook_active"], &active)
		log.hook = "wait-stop"
		inferred := "no"
		if working {
			inferred = "yes"
		}
		log.write("stop", detail+" stop_hook_active="+strconv.FormatBool(active)+" inferred_working="+inferred)
		return
	}
	if e, ok := claudesession.LastAPIError(str("transcript_path")); ok {
		detail += " transcript_error=" + e.Error + " transcript_at=" + e.At.UTC().Format(time.RFC3339)
		if !e.ResetsAt.IsZero() {
			detail += " resets_at=" + strconv.FormatInt(e.ResetsAt.Unix(), 10)
		}
	} else {
		detail += " transcript_error=none"
	}
	outcome := str("error")
	if outcome == "" {
		outcome = "no-error-field"
	}
	log.hook = "wait-stopfailure"
	log.write(outcome, detail)
}

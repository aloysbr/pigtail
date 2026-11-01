// Package waittype is the closed list of what a Claude session can be waiting
// for (spec "Sidebar pigtail" §4.8, PR 6), plus the display-only states the
// sidebar computes (Dead, Your turn, Respawn): their identifiers, their rendering in
// the cmux sidebar, their display priority, and the translation of a Claude
// Code hook payload into a wait. Every state's colour comes from its gravity
// (gravity.go). Pure: no I/O, no clock.
//
// The labels are English and carry no glyph (decision of 2026-10-01): cmux
// draws the icon, an SF Symbol tinted with the colour, in front of the value.
package waittype

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// Type is what a waiting session expects from its user. Unknown ("") is a
// wait whose reason nobody told: the generic "Waiting for you".
type Type string

// Declarable states: ASCII and stable, they travel in the API, the SSE
// stream and the database.
const (
	Permission Type = "permission" // a tool, a sandbox escape to allow
	Question   Type = "question"   // information the agent lacks
	Arbitrage  Type = "arbitrage"  // a choice between options it laid out
	Review     Type = "review"     // a result to look at: plan, PR, visual
	Blocked    Type = "blocked"    // an action only the user can take
	Done       Type = "done"       // the work is finished; only closing it is left (the done command, Commands.Done)
	Wrapped    Type = "wrapped"    // the closing ran and left the tab open; only the tab is left to close
	Working    Type = "working"    // the turn ended but background work runs (shells, agents, a CI watch): the agent's turn, not the user's
	Unknown    Type = ""
)

// Mac is a wait held by a process, not declared by the session: a
// background Mac wait keeps it (slot "held"), the session waits for the Mac to be free
// and is released when the user gives the Mac back (the release command,
// Commands.MacRelease, when one is set). Parse refuses it; ParseHeld takes it.
const Mac Type = "mac"

// Held is the closed list of what a held wait may carry (ParseHeld).
var Held = []Type{Mac}

// Display-only states: computed by the sidebar, never declared, never stored.
const (
	Dead     Type = "dead"      // a surface whose session died without SessionEnd
	YourTurn Type = "your_turn" // a turn ended with nothing declared
	Respawn  Type = "respawn"   // a respawn held by its user: a draft, background work, an unreadable screen (D16), or a respawn that gave up (abandoned, D2 2026-10-23)
)

// Hook-typed states (StopFailure, D2–D3): told by a Claude Code hook as an
// automatic wait, never declared (Parse refuses them), shown while the
// session is idle (cmuxsync.waitOf).
const (
	Limit    Type = "limit"     // rate_limit: the usage limit; resumes at its reset (D2)
	Login    Type = "login"     // authentication_failed, billing_error, oauth_org_not_allowed (D3)
	APIError Type = "api_error" // server_error, overloaded (D3)
)

// RotationReasonPrefix starts the reason of a Limit wait written by the
// account rotation worker (pigtail session rotate): a refusal keeps the reset,
// a move without its resume prompt carries none. cmuxsync shows it as « not
// switched » / « switched, not resumed » on the pill.
const RotationReasonPrefix = "rotation: "

// HookOnly is the list only an automatic wait may carry (ParseAuto).
var HookOnly = []Type{Limit, Login, APIError}

// AnswerableTypes are the wait types the user answers outside the terminal: the
// set waitdetail.ParseAnswer accepts. A declared wait of one of them bound to
// a subject belongs to the subject (D28); migration 0027's CHECK lists the
// same set (store.TestOwnerRuleMatchesCheck).
var AnswerableTypes = []Type{Arbitrage, Question, Review, Blocked}

// Answerable reports whether t is one of AnswerableTypes.
func Answerable(t Type) bool {
	for _, a := range AnswerableTypes {
		if a == t {
			return true
		}
	}
	return false
}

// Declarable is the closed list a session may declare or a hook may type,
// in the order the help and the errors list them.
var Declarable = []Type{Permission, Question, Arbitrage, Review, Blocked, Done, Working, Wrapped}

// Order is the display priority, most severe first (D6, D15, D16): the pill
// of a workspace shows the first state present. Unknown ("Waiting for you")
// sits between Decision and Your turn; Respawn (a respawn held by its user,
// D16) above API error; Working ranks under everything that asks the
// user for something, above Wrapped; Mac (held by the Mac wait, D1) sits
// just above Your turn: orange, it waits on the user releasing the Mac, and neither
// another session's turn end nor a green Review or Done hides it; anything off
// the list ranks last.
// The failure states follow D8 of subject sub_x000000000000032: Limit and
// Login under Blocked, above Dead; API error under Respawn, above Mac.
var Order = []Type{Permission, Blocked, Limit, Login, Dead, Question, Arbitrage, Unknown, Respawn, APIError, Mac, YourTurn, Review, Done, Working, Wrapped}

// MaxReasonBytes bounds the reason stored with a wait.
const MaxReasonBytes = 200

// Commands are the user's own slash commands, read from settings.yaml
// (usersettings.Settings.WaitCommands). pigtail ships none of them: the zero
// value names none and matches no prompt.
type Commands struct {
	// Done closes a finished session (session.done_command). Set: a Done pill
	// with no reason reads "Done · <Done>", and the prompt <Done> [args] is a
	// DonePrompt.
	Done string
	// MacRelease gives the Mac to a session waiting for it
	// (mac.release_command). Set: a Mac pill with no reason reads
	// "Waiting for Mac · <MacRelease>".
	MacRelease string
	// Turnless are the prompts a user hook answers without an agent turn
	// (session.turnless_prompts): TurnlessPrompt.
	Turnless []*regexp.Regexp
}

type configured struct {
	Commands
	donePrompt *regexp.Regexp // nil when Done is ""
}

var commands atomic.Pointer[configured]

// Configure sets the user's commands for the process. pigtaild and the CLI
// call it once at start, from settings.yaml; a test that calls it restores
// the previous value itself.
func Configure(c Commands) {
	k := &configured{Commands: c}
	if c.Done != "" {
		// Claude Code reads a slash command only at the very start of the
		// prompt, case included: then nothing, or whitespace and anything.
		k.donePrompt = regexp.MustCompile(`(?s)^` + regexp.QuoteMeta(c.Done) + `(?:\s.*)?$`)
	}
	commands.Store(k)
}

func current() *configured {
	if k := commands.Load(); k != nil {
		return k
	}
	return &configured{}
}

// Configured returns the commands Configure set (the zero value before).
func Configured() Commands { return current().Commands }

// MacHint is what a Mac pill reads after its label when no reason was given:
// the release command, else nothing ("Waiting for Mac").
func MacHint() string { return current().MacRelease }

// macNeedRE is the reason of a Mac wait that announced its duration
// (maintenance window, contract C4): "90 min", 1 to 720 once parsed.
var macNeedRE = regexp.MustCompile(`^[1-9][0-9]{0,2} min$`)

// MacNeed parses the reason of a Mac wait that announced how long the session
// needs the Mac ("90 min": a Mac wait announcing a 90 min need). ok is false for any
// other reason, a command name and the empty one included, and for a duration
// outside 1..720. The one parser of the format: the sidebar folder
// « Maintenance » and the pill read it here and nowhere else.
func MacNeed(reason string) (minutes int, ok bool) {
	if !macNeedRE.MatchString(reason) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(reason, " min"))
	if err != nil || n < 1 || n > 720 {
		return 0, false
	}
	return n, true
}

// DoneHint is what a Done pill reads after its label when no reason was
// given: the done command, else nothing ("Done").
func DoneHint() string { return current().Done }

// yourTurnIcon is the "Your turn" bubble with dots (D10).
const yourTurnIcon = "ellipsis.bubble.fill"

// wrappedIcon is Wrapped's SF Symbol (D20).
const wrappedIcon = "archivebox.fill"

// workingIcon is Working's SF Symbol. Working is blue (nothing for the user
// to do, not the orange "Your turn" the turn would read undeclared, nor the
// grey of a session that is idle or wrapped).
const workingIcon = "hourglass"

// respawnIcon is Respawn's SF Symbol (D16-bis): orange, the user's move — a
// draft to clear or send, background work to end, before the session closes,
// or a respawn that gave up (abandoned, D2 2026-10-23).
const respawnIcon = "arrow.clockwise.circle.fill"

type style struct {
	label, icon string
	gravity     Gravity
}

// Icons are SF Symbols cmux renders as the pill's icon; the colour is the
// gravity's (gravity.go).
var styles = map[Type]style{
	Permission: {"Permission", "lock.fill", Red},
	Blocked:    {"Blocked", "hand.raised.fill", Red},
	Dead:       {"Dead", "xmark.octagon.fill", Red},
	Question:   {"Question", "questionmark.bubble.fill", Orange},
	Arbitrage:  {"Decision", "arrow.triangle.branch", Orange},
	Unknown:    {"Waiting for you", "bell.fill", Orange},
	YourTurn:   {"Your turn", yourTurnIcon, Orange},
	Respawn:    {"Respawn", respawnIcon, Orange},
	Review:     {"Review", "eye.fill", Green},
	Done:       {"Done", "checkmark.circle.fill", Green},
	Mac:        {"Waiting for Mac", "desktopcomputer", Orange},
	Working:    {"Background", workingIcon, Blue}, // the type stays "working"; the label reads apart from cmux's own "Running"
	Wrapped:    {"Wrapped", wrappedIcon, Grey},
	Limit:      {"Limit", "gauge.with.dots.needle.0percent", Red},
	Login:      {"Login", "person.crop.circle.badge.exclamationmark", Red},
	APIError:   {"API error", "exclamationmark.triangle.fill", Orange},
}

// DeclarableList is the list an error or a usage line names.
func DeclarableList() string {
	return "permission, question, arbitrage (or decision), review, blocked, done, working, wrapped"
}

// Parse reads a type as the CLI and the API take it, case-insensitive.
// "decision" is accepted for arbitrage, whose pill reads "Decision".
// Display-only states (dead, your_turn, respawn) are refused.
func Parse(s string) (Type, error) {
	v := Type(strings.ToLower(strings.TrimSpace(s)))
	if v == "decision" {
		return Arbitrage, nil
	}
	for _, t := range Declarable {
		if v == t {
			return t, nil
		}
	}
	return Unknown, fmt.Errorf("unknown wait type %q: one of %s (mac is held by a background gate)", s, DeclarableList()) // D21·C4
}

// ParseHeld reads a type a held wait may carry (Held), case-insensitive.
func ParseHeld(s string) (Type, error) {
	v := Type(strings.ToLower(strings.TrimSpace(s)))
	for _, t := range Held {
		if v == t {
			return t, nil
		}
	}
	return Unknown, fmt.Errorf("unknown held wait type %q: one of mac", s)
}

// ParseAuto reads a type an automatic wait may carry: the declarable list
// plus HookOnly. The relay's POST takes it for source=auto only.
func ParseAuto(s string) (Type, error) {
	if t, err := Parse(s); err == nil {
		return t, nil
	}
	v := Type(strings.ToLower(strings.TrimSpace(s)))
	for _, t := range HookOnly {
		if v == t {
			return t, nil
		}
	}
	return Unknown, fmt.Errorf("unknown wait type %q: one of %s, limit, login, api_error", s, DeclarableList())
}

// Failure reports a hook-typed failure state (HookOnly).
func (t Type) Failure() bool {
	for _, h := range HookOnly {
		if t == h {
			return true
		}
	}
	return false
}

// Valid reports whether t is declarable (Unknown and display states are not).
func Valid(t Type) bool {
	for _, d := range Declarable {
		if d == t {
			return true
		}
	}
	return false
}

func (t Type) style() style {
	if s, ok := styles[t]; ok {
		return s
	}
	return styles[Unknown]
}

// Label is the pill's text for the state.
func (t Type) Label() string { return t.style().label }

// Icon is the pill's SF Symbol.
func (t Type) Icon() string { return t.style().icon }

// Gravity is the state's gravity (gravity.go).
func (t Type) Gravity() Gravity { return t.style().gravity }

// Color is the pill's tint: its gravity's colour.
func (t Type) Color() string { return t.Gravity().Color() }

// rank is t's place in Order; anything off the list is last.
func (t Type) rank() int {
	for i, o := range Order {
		if o == t {
			return i
		}
	}
	return len(Order)
}

// Before reports whether a outranks b in the display priority.
func Before(a, b Type) bool { return a.rank() < b.rank() }

// FromWaitingFor reads Claude's loose native hint (sessions/<pid>.json
// waitingFor, spec C10) when it is telling: the fallback when no hook typed
// the wait.
func FromWaitingFor(s string) Type {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sandbox request":
		return Permission
	case "goal proposal":
		return Review
	}
	return Unknown
}

// CleanReason makes a reason one line, trimmed, and at most MaxReasonBytes
// (cut on a rune boundary).
func CleanReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= MaxReasonBytes {
		return s
	}
	cut := MaxReasonBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

// --- hooks ------------------------------------------------------------------

// HookInput is the part of a Claude Code hook payload the wait reads.
type HookInput struct {
	SessionID        string          `json:"session_id"`
	Cwd              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	Error            string          `json:"error"`            // StopFailure: rate_limit, authentication_failed, …
	TranscriptPath   string          `json:"transcript_path"`  // where the reset of a limit is read (A1)
	AgentID          string          `json:"agent_id"`         // set when a subagent fired the hook
	CommandName      string          `json:"command_name"`     // UserPromptExpansion: the command expanded
	ExpansionType    string          `json:"expansion_type"`   // UserPromptExpansion: slash_command | mcp_prompt
	Prompt           string          `json:"prompt"`           // UserPromptSubmit: what the user typed
	StopHookActive   bool            `json:"stop_hook_active"` // Stop: a re-fired Stop of the same turn (same turn_id anyway)
	// Stop: the turn's final assistant text, read for a 🔄 line (WorkingLine)
	// and never logged.
	LastAssistantMessage string `json:"last_assistant_message"`
}

var skillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,99}$`)

// ValidSkillName is the one name check, shared with POST /peers/{session}/skill.
func ValidSkillName(name string) bool { return skillName.MatchString(name) }

// SkillOf names the skill a hook payload invokes (D11): the Skill tool
// (PreToolUse, tool_input.skill) or a slash command the user typed
// (UserPromptExpansion, slash_command only). "" for anything else, a
// subagent's call (A8: it does not change the session's last skill), or a
// name that is not one.
func SkillOf(in HookInput) string {
	if in.AgentID != "" {
		return ""
	}
	var name string
	switch {
	case in.HookEventName == "PreToolUse" && in.ToolName == "Skill":
		var ti struct {
			Skill string `json:"skill"`
		}
		if json.Unmarshal(in.ToolInput, &ti) != nil {
			return ""
		}
		name = ti.Skill
	case in.HookEventName == "UserPromptExpansion" && in.ExpansionType == "slash_command":
		name = in.CommandName // an MCP prompt (mcp_prompt) is not a skill
	}
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if !ValidSkillName(name) {
		return ""
	}
	return name
}

// Action is what a hook asks of the session's automatic wait.
type Action int

// Actions.
const (
	ActionNone  Action = iota // nothing to record
	ActionSet                 // record Type (and Reason) as the automatic wait
	ActionClear               // the user answered: the turn's waits are over
)

// HookResult is FromHook's answer.
type HookResult struct {
	Action Action
	Type   Type
	Reason string
	// KeepSubject, on an ActionClear: the prompt is the done command
	// (DonePrompt) — the session's waits clear, the wait of its subject it
	// emitted stays open.
	KeepSubject bool
}

// DonePrompt reports whether a typed prompt is the done command or the done
// command followed by whitespace and anything (its arguments): it starts a
// turn, but answers nothing of the subject's wait. Exactly the command at the
// start: a leading space, another case or a longer name is a prompt like any
// other. Always false while no done command is set.
func DonePrompt(p string) bool {
	re := current().donePrompt
	return re != nil && re.MatchString(p)
}

// TurnlessPrompt reports whether a typed prompt is answered by a user hook
// with no agent turn (Commands.Turnless), so the session's wait must stand.
// Each pattern must match exactly what the hook accepts: a pattern wider than
// its hook keeps a wait standing after a prompt that did start a turn. RE2's
// \s is ASCII ([\t\n\f\r ]); a hook written in a language whose \s is
// Unicode must restrict it the same way. No pattern set: false.
func TurnlessPrompt(p string) bool {
	for _, re := range current().Turnless {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

const permissionPrefix = "Claude needs your permission to use "

// FromHook translates one hook payload (spec C11):
//
//	PermissionRequest AskUserQuestion       → question · <header> (the dialog itself, D3)
//	PermissionRequest ExitPlanMode          → review · Plan (D3)
//	PermissionRequest <tool>                → permission · <tool>
//	Notification permission_prompt          → permission (· <tool> from the message);
//	                                          naming AskUserQuestion → question,
//	                                          ExitPlanMode → review · Plan (D3)
//	Notification elicitation_dialog,
//	             elicitation_url_dialog     → question (an MCP server asks)
//	Notification idle_prompt, auth_success,
//	             agent_needs_input, other   → nothing: idle_prompt is a reminder,
//	                                          not a wait; agent_needs_input says
//	                                          nothing of what for, so the generic
//	                                          pill (native status) covers it
//	PreToolUse AskUserQuestion              → question · <header of the first question>
//	PreToolUse ExitPlanMode                 → review · Plan
//	StopFailure rate_limit                  → limit (D2)
//	StopFailure authentication_failed,
//	            billing_error,
//	            oauth_org_not_allowed       → login (D3)
//	StopFailure server_error, overloaded    → api_error (D3)
//	StopFailure any other error, or with
//	            an agent_id (a subagent)    → nothing
//	UserPromptSubmit, turnless command      → nothing: no turn starts, the wait stands
//	UserPromptSubmit, the done command      → clear, keeping the subject's wait
//	UserPromptSubmit, anything else         → clear: the next turn starts
//
// Anything else asks nothing.
func FromHook(in HookInput) HookResult {
	switch in.HookEventName {
	case "PermissionRequest":
		if r, ok := dialogWait(in.ToolName, in.ToolInput); ok {
			return r
		}
		return set(Permission, in.ToolName)
	case "Notification":
		switch in.NotificationType {
		case "permission_prompt":
			tool := ""
			if strings.HasPrefix(in.Message, permissionPrefix) {
				tool = strings.TrimSpace(strings.TrimPrefix(in.Message, permissionPrefix))
			}
			if r, ok := dialogWait(tool, nil); ok {
				return r
			}
			return set(Permission, tool)
		case "elicitation_dialog", "elicitation_url_dialog":
			return set(Question, "")
		}
	case "PreToolUse":
		if r, ok := dialogWait(in.ToolName, in.ToolInput); ok {
			return r
		}
	case "StopFailure":
		if in.AgentID != "" {
			return HookResult{} // a subagent failed; the main session goes on
		}
		switch in.Error {
		case "rate_limit":
			return set(Limit, "")
		case "authentication_failed", "billing_error", "oauth_org_not_allowed":
			return set(Login, "")
		case "server_error", "overloaded":
			return set(APIError, "")
		}
	case "UserPromptSubmit":
		if TurnlessPrompt(in.Prompt) {
			return HookResult{} // a command a hook answers without a turn: the wait stands
		}
		return HookResult{Action: ActionClear, KeepSubject: DonePrompt(in.Prompt)}
	}
	return HookResult{}
}

// dialogWait is the wait of the two tools whose "permission" is the dialog
// itself (D3, probe 2026-10-04 18:04: an AskUserQuestion read
// permission|AskUserQuestion, then permission|): AskUserQuestion asks a
// question, ExitPlanMode a review of the plan.
func dialogWait(tool string, input json.RawMessage) (HookResult, bool) {
	switch tool {
	case "AskUserQuestion":
		return set(Question, firstQuestion(input)), true
	case "ExitPlanMode":
		return set(Review, "Plan"), true
	}
	return HookResult{}, false
}

// Supersedes reports whether an automatic wait just told replaces the
// automatic wait standing (D3). The last told wins, except a bare follow-up
// of a dialog already typed: Claude Code also sends a permission_prompt
// notification for AskUserQuestion and ExitPlanMode, without the tool's name.
// Over a standing Question or Review:
//   - a Permission without a reason is that notification: kept out;
//   - the same type without a reason does not erase the standing reason.
//
// A Permission naming a tool always replaces: a real permission prompt names
// its tool in its PermissionRequest. No time window: the hooks carry no id
// tying a notification to its dialog. The standing row is cleared by the next
// prompt (UserPromptSubmit), or by the sidebar loop once the native status
// leaves "waiting" — the latter only while that loop runs: with the loop off
// or paused, a question row can outlive its dialog until the next prompt, and
// a bare permission_prompt meanwhile is kept out.
func Supersedes(standingType Type, standingReason string, newType Type, newReason string) bool {
	if standingType != Question && standingType != Review {
		return true
	}
	if newReason != "" {
		return true
	}
	if newType == Permission {
		return false
	}
	return newType != standingType || standingReason == ""
}

func set(t Type, reason string) HookResult {
	return HookResult{Action: ActionSet, Type: t, Reason: CleanReason(reason)}
}

// firstQuestion is the header of AskUserQuestion's first question (a chip of
// a dozen characters, made to be shown), else the question itself.
func firstQuestion(raw json.RawMessage) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
		} `json:"questions"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil || len(in.Questions) == 0 {
		return ""
	}
	q := in.Questions[0]
	if h := strings.TrimSpace(q.Header); h != "" {
		return h
	}
	return q.Question
}

// Rank is t's place in Order, 0 the most severe; a type off the list ranks
// len(Order). The cmux folders sort by it (sub_x000000000000041, D2).
func Rank(t Type) int { return t.rank() }

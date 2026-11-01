package waittype

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestParseClosedList(t *testing.T) {
	for in, want := range map[string]Type{
		"permission": Permission, "question": Question, "arbitrage": Arbitrage, "decision": Arbitrage,
		"review": Review, "blocked": Blocked, "done": Done, "Done": Done, " wrapped ": Wrapped, "Working": Working,
	} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"fini", "", "your_turn", "dead", "respawn", "mac"} {
		_, err := Parse(bad)
		if err == nil {
			t.Errorf("Parse(%q) accepted a display-only or unknown state", bad)
			continue
		}
		if !strings.Contains(err.Error(), "permission, question, arbitrage (or decision), review, blocked, done, working, wrapped") {
			t.Errorf("Parse(%q) error = %q", bad, err)
		}
	}
}

func TestPriorityOrder(t *testing.T) {
	want := []Type{Permission, Blocked, Limit, Login, Dead, Question, Arbitrage, Unknown, Respawn, APIError, Mac, YourTurn, Review, Done, Working, Wrapped}
	if !reflect.DeepEqual(Order, want) {
		t.Fatalf("Order = %q, want %q", Order, want)
	}
	for i := 1; i < len(want); i++ {
		if !Before(want[i-1], want[i]) {
			t.Errorf("%q should outrank %q", want[i-1], want[i])
		}
	}
}

func TestRenderingEnglishWithIcon(t *testing.T) {
	want := map[Type]string{
		Permission: "Permission", Question: "Question", Arbitrage: "Decision",
		Review: "Review", Blocked: "Blocked", Unknown: "Waiting for you",
		Dead: "Dead", YourTurn: "Your turn", Done: "Done", Working: "Background", Wrapped: "Wrapped",
		Respawn: "Respawn", Mac: "Waiting for Mac",
	}
	icons := map[string]Type{}
	for ty, label := range want {
		if got := ty.Label(); got != label {
			t.Errorf("%q.Label() = %q, want %q", ty, got, label)
		}
		if ty.Icon() == "" {
			t.Errorf("%q: no icon", ty)
		}
		if prev, dup := icons[ty.Icon()]; dup {
			t.Errorf("%q and %q share the icon %q: the pill would not tell them apart", ty, prev, ty.Icon())
		}
		icons[ty.Icon()] = ty
	}
}

func TestRenderingByGravity(t *testing.T) {
	for ty, want := range map[Type]struct {
		label, icon string
		g           Gravity
	}{
		Permission: {"Permission", "lock.fill", Red},
		Blocked:    {"Blocked", "hand.raised.fill", Red},
		Dead:       {"Dead", "xmark.octagon.fill", Red},
		Question:   {"Question", "questionmark.bubble.fill", Orange},
		Arbitrage:  {"Decision", "arrow.triangle.branch", Orange},
		Unknown:    {"Waiting for you", "bell.fill", Orange},
		YourTurn:   {"Your turn", "ellipsis.bubble.fill", Orange},
		Respawn:    {"Respawn", "arrow.clockwise.circle.fill", Orange}, // D16-bis: a held respawn waits on the user
		Review:     {"Review", "eye.fill", Green},
		Done:       {"Done", "checkmark.circle.fill", Green},
		Mac:        {"Waiting for Mac", "desktopcomputer", Orange}, // waits on the user releasing the Mac
		Working:    {"Background", "hourglass", Blue},              // the agent works: not the orange "Your turn", nor an idle grey
		Wrapped:    {"Wrapped", wrappedIcon, Grey},
	} {
		if ty.Label() != want.label || ty.Icon() != want.icon || ty.Gravity() != want.g {
			t.Errorf("%q = %s %s %d, want %+v", ty, ty.Label(), ty.Icon(), ty.Gravity(), want)
		}
	}
	if wrappedIcon != "archivebox.fill" {
		t.Errorf("wrappedIcon = %q, want archivebox.fill (D20)", wrappedIcon)
	}
}

func TestFromWaitingFor(t *testing.T) {
	for in, want := range map[string]Type{
		"sandbox request": Permission,
		"goal proposal":   Review,
		"input needed":    Unknown,
		"dialog open":     Unknown,
		"worker request":  Unknown,
		"":                Unknown,
	} {
		if got := FromWaitingFor(in); got != want {
			t.Errorf("FromWaitingFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanReason(t *testing.T) {
	if got := CleanReason("  PR #142\n ready\t "); got != "PR #142 ready" {
		t.Errorf("CleanReason = %q", got)
	}
	if got := CleanReason(" \n "); got != "" {
		t.Errorf("CleanReason(blank) = %q", got)
	}
}

// Hook payloads as Claude Code 2.1.28x sends them (shapes only, no private
// data): the automatic source of the wait type (spec C11).
func TestFromHook(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    HookResult
	}{
		{"PermissionRequest Bash",
			`{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"},"permission_suggestions":[]}`,
			HookResult{Action: ActionSet, Type: Permission, Reason: "Bash"}},
		{"Notification permission_prompt",
			`{"session_id":"s1","hook_event_name":"Notification","message":"Claude needs your permission to use Write","notification_type":"permission_prompt"}`,
			HookResult{Action: ActionSet, Type: Permission, Reason: "Write"}},
		{"Notification permission_prompt, message without tool",
			`{"session_id":"s1","hook_event_name":"Notification","message":"Claude needs your permission","notification_type":"permission_prompt"}`,
			HookResult{Action: ActionSet, Type: Permission}},
		{"Notification elicitation_dialog",
			`{"session_id":"s1","hook_event_name":"Notification","message":"MCP server requests input","notification_type":"elicitation_dialog"}`,
			HookResult{Action: ActionSet, Type: Question}},
		{"Notification elicitation_url_dialog",
			`{"session_id":"s1","hook_event_name":"Notification","message":"x","notification_type":"elicitation_url_dialog"}`,
			HookResult{Action: ActionSet, Type: Question}},
		{"Notification idle_prompt is not a wait",
			`{"session_id":"s1","hook_event_name":"Notification","message":"Claude is waiting for your input","notification_type":"idle_prompt"}`,
			HookResult{}},
		{"Notification agent_needs_input: generic fallback, no type",
			`{"session_id":"s1","hook_event_name":"Notification","message":"x","notification_type":"agent_needs_input"}`,
			HookResult{}},
		{"Notification auth_success",
			`{"session_id":"s1","hook_event_name":"Notification","message":"x","notification_type":"auth_success"}`,
			HookResult{}},
		{"Notification of an unknown kind",
			`{"session_id":"s1","hook_event_name":"Notification","message":"x","notification_type":"something_new"}`,
			HookResult{}},
		{"PreToolUse AskUserQuestion, header as reason",
			`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which library should we use for dates?","header":"Date lib","options":[{"label":"a"},{"label":"b"}],"multiSelect":false}]}}`,
			HookResult{Action: ActionSet, Type: Question, Reason: "Date lib"}},
		{"PreToolUse AskUserQuestion without header",
			`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Go on?"}]}}`,
			HookResult{Action: ActionSet, Type: Question, Reason: "Go on?"}},
		{"PreToolUse ExitPlanMode",
			`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan\n1. x"}}`,
			HookResult{Action: ActionSet, Type: Review, Reason: "Plan"}},
		{"PreToolUse of another tool",
			`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
			HookResult{}},
		{"PermissionRequest AskUserQuestion is the question itself (D3)",
			`{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which?","header":"D1 Gel"}]}}`,
			HookResult{Action: ActionSet, Type: Question, Reason: "D1 Gel"}},
		{"PermissionRequest ExitPlanMode is the plan review (D3)",
			`{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan"}}`,
			HookResult{Action: ActionSet, Type: Review, Reason: "Plan"}},
		{"Notification permission_prompt naming AskUserQuestion (D3)",
			`{"session_id":"s1","hook_event_name":"Notification","message":"Claude needs your permission to use AskUserQuestion","notification_type":"permission_prompt"}`,
			HookResult{Action: ActionSet, Type: Question}},
		{"Notification permission_prompt naming ExitPlanMode (D3)",
			`{"session_id":"s1","hook_event_name":"Notification","message":"Claude needs your permission to use ExitPlanMode","notification_type":"permission_prompt"}`,
			HookResult{Action: ActionSet, Type: Review, Reason: "Plan"}},
		{"UserPromptSubmit clears",
			`{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"go ahead"}`,
			HookResult{Action: ActionClear}},
		{"Stop does nothing",
			`{"session_id":"s1","hook_event_name":"Stop","stop_hook_active":false}`,
			HookResult{}},
	}
	for _, c := range cases {
		var in HookInput
		if err := json.Unmarshal([]byte(c.payload), &in); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := FromHook(in); got != c.want {
			t.Errorf("%s: FromHook = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestFromHookReasonIsBounded(t *testing.T) {
	long := strings.Repeat("why ", 100)
	in := HookInput{HookEventName: "PreToolUse", ToolName: "AskUserQuestion",
		ToolInput: json.RawMessage(`{"questions":[{"question":"` + long + `"}]}`)}
	got := FromHook(in)
	if len(got.Reason) > MaxReasonBytes {
		t.Fatalf("reason is %d bytes, max %d", len(got.Reason), MaxReasonBytes)
	}
}

// D3: over a standing question or review, a bare follow-up (the
// permission_prompt notification Claude sends for the dialog itself, or the
// same type without its reason) does not replace it; a named permission and
// anything over another standing type do.
func TestSupersedes(t *testing.T) {
	cases := []struct {
		st   Type
		sr   string
		nt   Type
		nr   string
		want bool
	}{
		{Question, "D1 Gel", Permission, "", false},
		{Review, "Plan", Permission, "", false},
		{Question, "D1 Gel", Question, "", false},
		{Review, "Plan", Review, "", false},
		{Question, "D1 Gel", Permission, "Bash", true},
		{Question, "D1 Gel", Question, "D2", true},
		{Question, "", Question, "", true},
		{Permission, "Bash", Permission, "", true},
		{Unknown, "", Permission, "", true},
	}
	for _, c := range cases {
		if got := Supersedes(c.st, c.sr, c.nt, c.nr); got != c.want {
			t.Errorf("Supersedes(%q %q ← %q %q) = %v, want %v", c.st, c.sr, c.nt, c.nr, got, c.want)
		}
	}
}

func TestFailureStatesRendering(t *testing.T) {
	cases := []struct {
		t           Type
		label, icon string
		g           Gravity
	}{
		{Limit, "Limit", "gauge.with.dots.needle.0percent", Red},
		{Login, "Login", "person.crop.circle.badge.exclamationmark", Red},
		{APIError, "API error", "exclamationmark.triangle.fill", Orange},
	}
	for _, c := range cases {
		if c.t.Label() != c.label || c.t.Icon() != c.icon || c.t.Gravity() != c.g || c.t.Color() != c.g.Color() || !c.t.Failure() {
			t.Errorf("%q: %q %q %v %q failure=%v", c.t, c.t.Label(), c.t.Icon(), c.t.Gravity(), c.t.Color(), c.t.Failure())
		}
	}
	for _, d := range Declarable {
		if d.Failure() {
			t.Errorf("%q is declarable, not a failure", d)
		}
	}
}

// Derived, never declarable (spec § Surfaces): the CLI refuses them, the hook path takes them.
func TestFailureStatesAreHookOnly(t *testing.T) {
	for _, s := range []string{"limit", "login", "api_error"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) accepted a hook-only state", s)
		}
		if ty, err := ParseAuto(s); err != nil || string(ty) != s {
			t.Errorf("ParseAuto(%q) = %q, %v", s, ty, err)
		}
	}
	if ty, err := ParseAuto("decision"); err != nil || ty != Arbitrage {
		t.Errorf("ParseAuto keeps the declarable list: %q %v", ty, err)
	}
	if _, err := ParseAuto("dead"); err == nil {
		t.Error("ParseAuto accepted a display-only state")
	}
}

func TestFromHookStopFailure(t *testing.T) {
	cases := []struct {
		name, payload string
		want          HookResult
	}{
		{"rate_limit", `{"hook_event_name":"StopFailure","error":"rate_limit"}`, HookResult{Action: ActionSet, Type: Limit}},
		{"auth", `{"hook_event_name":"StopFailure","error":"authentication_failed"}`, HookResult{Action: ActionSet, Type: Login}},
		{"billing", `{"hook_event_name":"StopFailure","error":"billing_error"}`, HookResult{Action: ActionSet, Type: Login}},
		{"oauth org", `{"hook_event_name":"StopFailure","error":"oauth_org_not_allowed"}`, HookResult{Action: ActionSet, Type: Login}},
		{"server", `{"hook_event_name":"StopFailure","error":"server_error"}`, HookResult{Action: ActionSet, Type: APIError}},
		{"overloaded", `{"hook_event_name":"StopFailure","error":"overloaded"}`, HookResult{Action: ActionSet, Type: APIError}},
		{"invalid_request", `{"hook_event_name":"StopFailure","error":"invalid_request"}`, HookResult{}},
		{"unknown", `{"hook_event_name":"StopFailure","error":"something_new"}`, HookResult{}},
		{"no error", `{"hook_event_name":"StopFailure"}`, HookResult{}},
		{"agent", `{"hook_event_name":"StopFailure","error":"rate_limit","agent_id":"a1"}`, HookResult{}},
	}
	for _, c := range cases {
		var in HookInput
		if err := json.Unmarshal([]byte(c.payload), &in); err != nil {
			t.Fatal(err)
		}
		if got := FromHook(in); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestSkillOf(t *testing.T) {
	cases := []struct{ payload, want string }{
		{`{"hook_event_name":"PreToolUse","tool_name":"Skill","tool_input":{"skill":"example-skill-plan","args":"x"}}`, "example-skill-plan"},
		{`{"hook_event_name":"PreToolUse","tool_name":"Skill","tool_input":{"skill":"superpowers:writing-plans"}}`, "superpowers:writing-plans"},
		{`{"hook_event_name":"UserPromptExpansion","expansion_type":"slash_command","command_name":"example-skill","command_args":"x"}`, "example-skill"},
		{`{"hook_event_name":"UserPromptExpansion","expansion_type":"slash_command","command_name":"/finish"}`, "finish"},
		{`{"hook_event_name":"UserPromptExpansion","expansion_type":"mcp_prompt","command_name":"notion:search"}`, ""},
		{`{"hook_event_name":"UserPromptExpansion","command_name":"example-skill"}`, ""},
		{`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`, ""},
		{`{"hook_event_name":"PreToolUse","tool_name":"Skill","tool_input":{"skill":"bad name; rm -rf"}}`, ""},
		{`{"hook_event_name":"PreToolUse","tool_name":"Skill","tool_input":{"skill":"` + strings.Repeat("a", 101) + `"}}`, ""},
		{`{"hook_event_name":"PreToolUse","tool_name":"Skill","agent_id":"a1","tool_input":{"skill":"example-skill"}}`, ""},
	}
	for _, c := range cases {
		var in HookInput
		if err := json.Unmarshal([]byte(c.payload), &in); err != nil {
			t.Fatal(err)
		}
		if got := SkillOf(in); got != c.want {
			t.Errorf("%s: %q, want %q", c.payload, got, c.want)
		}
	}
	var in HookInput
	_ = json.Unmarshal([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Skill","tool_input":{"skill":"example-skill"}}`), &in)
	if r := FromHook(in); r.Action != ActionNone {
		t.Fatalf("a Skill call typed a wait: %+v", r)
	}
}

func TestParseHeld(t *testing.T) {
	if got, err := ParseHeld("mac"); err != nil || got != Mac {
		t.Errorf("ParseHeld(mac) = %q, %v", got, err)
	}
	if got, err := ParseHeld(" MAC "); err != nil || got != Mac {
		t.Errorf("ParseHeld(MAC) = %q, %v", got, err)
	}
	for _, bad := range []string{"working", "done", ""} {
		if _, err := ParseHeld(bad); err == nil {
			t.Errorf("ParseHeld(%q) accepted", bad)
		}
	}
	if _, err := Parse("mac"); err == nil || !strings.Contains(err.Error(), "mac is held by a background gate") {
		t.Errorf("Parse(mac) error = %v", err)
	}
}

// withCommands sets the user's commands for one test (never a parallel one)
// and puts the previous ones back.
func withCommands(t *testing.T, c Commands) {
	t.Helper()
	prev := Configured()
	Configure(c)
	t.Cleanup(func() { Configure(prev) })
}

// turnlessFixture: the shapes a user's hooks accept, under invented names —
// a command taking any argument, a short one with an optional slash and a
// case-insensitive argument list, one with a short argument list, a pair, and
// a command whose class of spaces is written out.
func turnlessFixture() []*regexp.Regexp {
	var res []*regexp.Regexp
	for _, p := range []string{
		`(?is)^\s*/rename(?:\s+\S.*?)?\s*$`,
		`^/?clip(?:\s+(?i:\d+|list|ls))?\s*$`,
		`(?i)^/show(?:\s+(?:\d+|list|ls))?\s*$`,
		`(?s)^/(?:lend|reclaim)(?:\s+.*?)?\s*$`,
		`(?is)^\s*/pin(?:\s+\S.*?)?\s*$`,
	} {
		res = append(res, regexp.MustCompile(p))
	}
	return res
}

var turnlessNone = []string{"/lend", "/reclaim", "/lend x", "/rename", "/rename foo", "/RENAME foo",
	" /rename x", "/rename a\nb", "/clip", "clip", "/clip 2", "/clip list", "/clip LIST", "/show", "/show 3", "/SHOW list"}

func TestTurnlessPrompts(t *testing.T) {
	withCommands(t, Commands{Turnless: turnlessFixture()})
	clear := []string{"do this", "", "do /clip", " /clip", "/CLIP", " /show", "/Lend", " /lend", "/lendx", "/shower"}
	hook := func(p string) HookResult {
		return FromHook(HookInput{HookEventName: "UserPromptSubmit", Prompt: p})
	}
	for _, p := range turnlessNone {
		if got := hook(p); got.Action != ActionNone {
			t.Errorf("prompt %q: action %v, want ActionNone", p, got.Action)
		}
	}
	for _, p := range clear {
		if got := hook(p); got.Action != ActionClear {
			t.Errorf("prompt %q: action %v, want ActionClear", p, got.Action)
		}
	}
}

// With no pattern set (the default), no prompt is turnless: each one clears.
func TestTurnlessPromptsUnset(t *testing.T) {
	withCommands(t, Commands{})
	for _, p := range turnlessNone {
		if TurnlessPrompt(p) {
			t.Errorf("TurnlessPrompt(%q) = true with no pattern set", p)
		}
		if got := FromHook(HookInput{HookEventName: "UserPromptSubmit", Prompt: p}); got.Action != ActionClear || got.KeepSubject {
			t.Errorf("prompt %q: %+v, want a plain ActionClear", p, got)
		}
	}
}

// A pattern is applied with RE2's semantics: \s is [\t\n\f\r ] (no \v, no
// NBSP), so a hook mirror in another language writes that class out.
func TestTurnlessPromptSpaceClass(t *testing.T) {
	withCommands(t, Commands{Turnless: turnlessFixture()})
	for _, c := range []struct {
		p    string
		want bool
	}{
		{"/pin", true},
		{"/pin off", true},
		{" /PIN Off ", true},
		{"/pin x", true},
		{"/pin\toff", true},
		{"/pin\n", true},
		{"/pinx", false},
		{"pin", false},
		{"do /pin", false},
		{"/pin\u00a0off", false},
		{"\u00a0/pin", false},
		{"/pin\voff", false},
	} {
		if got := TurnlessPrompt(c.p); got != c.want {
			t.Errorf("TurnlessPrompt(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestRankFollowsOrder(t *testing.T) {
	if Rank(Permission) != 0 || Rank(Wrapped) != len(Order)-1 || Rank("zzz") != len(Order) {
		t.Fatalf("Rank: permission %d, wrapped %d, unknown type %d", Rank(Permission), Rank(Wrapped), Rank("zzz"))
	}
	if Rank(Arbitrage) >= Rank(Working) {
		t.Fatal("arbitrage must rank before working")
	}
}

func TestAnswerable(t *testing.T) {
	for _, typ := range []Type{Arbitrage, Question, Review, Blocked} {
		if !Answerable(typ) {
			t.Errorf("Answerable(%q) = false", typ)
		}
	}
	no := append([]Type{Done, Wrapped, Working, Permission}, HookOnly...)
	for _, typ := range no {
		if Answerable(typ) {
			t.Errorf("Answerable(%q) = true", typ)
		}
	}
	if len(AnswerableTypes) != 4 {
		t.Fatalf("AnswerableTypes = %v", AnswerableTypes)
	}
	for _, typ := range AnswerableTypes {
		if !Answerable(typ) {
			t.Errorf("AnswerableTypes holds %q, not Answerable", typ)
		}
	}
}

// The done command and its forms start a turn (the session's wait clears)
// but never answer the subject's wait: KeepSubject.
func TestDonePromptKeepsSubjectWait(t *testing.T) {
	withCommands(t, Commands{Done: "/finish"})
	keep := []string{"/finish", "/finish next", "/finish close the subject", "/finish\n", "/finish  next  ", "/finish\tnext", "/finish a\nb"}
	plain := []string{"/finisher", "/finishx", "/finish-next", "do /finish", "please /finish next", " /finish", "/FINISH", "/Finish next", "finish", "", "ok"}
	hook := func(p string) HookResult {
		return FromHook(HookInput{HookEventName: "UserPromptSubmit", Prompt: p})
	}
	for _, p := range keep {
		if !DonePrompt(p) {
			t.Errorf("DonePrompt(%q) = false, want true", p)
		}
		if got := hook(p); got.Action != ActionClear || !got.KeepSubject {
			t.Errorf("prompt %q: %+v, want ActionClear keeping the subject's wait", p, got)
		}
	}
	for _, p := range plain {
		if DonePrompt(p) {
			t.Errorf("DonePrompt(%q) = true, want false", p)
		}
		if got := hook(p); got.Action != ActionClear || got.KeepSubject {
			t.Errorf("prompt %q: %+v, want a plain ActionClear", p, got)
		}
	}
	// The command is literal, never a pattern.
	withCommands(t, Commands{Done: "/a.b"})
	if DonePrompt("/axb") || !DonePrompt("/a.b next") {
		t.Error("the done command was read as a pattern")
	}
}

// No done command (the default): no prompt keeps the subject's wait.
func TestDonePromptUnset(t *testing.T) {
	withCommands(t, Commands{})
	for _, p := range []string{"/finish", "/finish next", "/x"} {
		if DonePrompt(p) {
			t.Errorf("DonePrompt(%q) = true with no done command", p)
		}
		if got := FromHook(HookInput{HookEventName: "UserPromptSubmit", Prompt: p}); got.Action != ActionClear || got.KeepSubject {
			t.Errorf("prompt %q: %+v, want a plain ActionClear", p, got)
		}
	}
}

// The pill hints name the user's commands only when set.
func TestHints(t *testing.T) {
	withCommands(t, Commands{})
	if DoneHint() != "" || MacHint() != "" {
		t.Fatalf("unset hints = %q, %q", DoneHint(), MacHint())
	}
	withCommands(t, Commands{Done: "/finish", MacRelease: "/lend"})
	if DoneHint() != "/finish" || MacHint() != "/lend" {
		t.Fatalf("set hints = %q, %q", DoneHint(), MacHint())
	}
}

// C4 of the maintenance window: the one parser of a Mac wait's announced
// duration.
func TestMacNeedParses(t *testing.T) {
	for in, want := range map[string]int{"90 min": 90, "1 min": 1, "30 min": 30, "720 min": 720, "9 min": 9} {
		if got, ok := MacNeed(in); !ok || got != want {
			t.Errorf("MacNeed(%q) = %d, %v, want %d, true", in, got, ok, want)
		}
	}
	for _, bad := range []string{"/lend", "", "0 min", "00 min", "721 min", "1000 min", "90min", "90 mins", " 90 min", "90 min ", "-5 min", "+5 min", "9 0 min", "90  min", "90 min\n", "ninety min"} {
		if got, ok := MacNeed(bad); ok {
			t.Errorf("MacNeed(%q) = %d, true, want false", bad, got)
		}
	}
}

func TestRotationTransient(t *testing.T) {
	for _, why := range []string{RotationNoInputBox, "not quit — " + RotationNoInputBox, RotationNoNativeFile} {
		if !RotationTransient(why) {
			t.Errorf("%q not transient", why)
		}
	}
	for _, why := range []string{"no other account", "launcher proto unreadable", "rotated 10 min ago", ""} {
		if RotationTransient(why) {
			t.Errorf("%q transient", why)
		}
	}
}

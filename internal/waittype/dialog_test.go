package waittype

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// dialogDetailsCases are the hook payloads of TestDialogDetails, with the
// details each one carries. internal/waitdetail's TestDialogDetailsPassValidate
// replays the same payloads (an import cycle keeps that check out of here).
var dialogDetailsCases = []struct {
	name    string
	payload string
	want    *pigtail.WaitDetails
}{
	{"PermissionRequest Bash: the command, the cwd",
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make"},"cwd":"/w"}`,
		&pigtail.WaitDetails{Kind: "permission", Schema: 1, Cmd: "make", Where: "/w"}},
	{"PermissionRequest Edit: the tool and its file",
		`{"hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":{"file_path":"/w/a.go"},"cwd":"/w"}`,
		&pigtail.WaitDetails{Kind: "permission", Schema: 1, Cmd: "Edit /w/a.go", Where: "/w"}},
	{"PermissionRequest WebFetch: the tool and its url",
		`{"hook_event_name":"PermissionRequest","tool_name":"WebFetch","tool_input":{"url":"https://x.test","prompt":"p"}}`,
		&pigtail.WaitDetails{Kind: "permission", Schema: 1, Cmd: "WebFetch https://x.test"}},
	{"PermissionRequest of a tool with no known argument: its name",
		`{"hook_event_name":"PermissionRequest","tool_name":"mcp__x__y","tool_input":{"a":1},"cwd":"/w"}`,
		&pigtail.WaitDetails{Kind: "permission", Schema: 1, Cmd: "mcp__x__y", Where: "/w"}},
	{"PreToolUse AskUserQuestion: the first question and its options",
		`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which library should we use for dates?","header":"Date lib","options":[{"label":"a"},{"label":"b"}],"multiSelect":false}]}}`,
		&pigtail.WaitDetails{Kind: "question", Schema: 1, Title: "Date lib", Summary: "Which library should we use for dates?",
			Options: []pigtail.WaitOption{{ID: "1", Label: "a"}, {ID: "2", Label: "b"}}}},
	{"PermissionRequest AskUserQuestion: the option descriptions",
		`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which?","header":"D1","options":[{"label":"a","description":"da"}]}]}}`,
		&pigtail.WaitDetails{Kind: "question", Schema: 1, Title: "D1", Summary: "Which?",
			Options: []pigtail.WaitOption{{ID: "1", Label: "a", Detail: "da"}}}},
	{"Notification permission_prompt: nothing beyond the reason",
		`{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`,
		nil},
	{"Notification elicitation_dialog: its message",
		`{"hook_event_name":"Notification","message":"Pick a server","notification_type":"elicitation_dialog"}`,
		&pigtail.WaitDetails{Kind: "question", Schema: 1, Summary: "Pick a server"}},
	{"PreToolUse ExitPlanMode: nothing",
		`{"hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan"}}`,
		nil},
	{"PermissionRequest ExitPlanMode: nothing",
		`{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode","tool_input":{"plan":"# Plan"}}`,
		nil},
	{"PreToolUse of another tool: nothing",
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
		nil},
}

func TestDialogDetails(t *testing.T) {
	for _, c := range dialogDetailsCases {
		t.Run(c.name, func(t *testing.T) {
			var in HookInput
			if err := json.Unmarshal([]byte(c.payload), &in); err != nil {
				t.Fatal(err)
			}
			got := DialogDetails(in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("DialogDetails = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestDialogDetailsCutsALongCommand(t *testing.T) {
	long := strings.Repeat("é", 1500)
	raw, _ := json.Marshal(map[string]any{"command": long})
	got := DialogDetails(HookInput{HookEventName: "PermissionRequest", ToolName: "Bash", ToolInput: raw})
	if got == nil || len([]rune(got.Cmd)) != 1000 {
		t.Fatalf("cmd not cut to 1000 runes: %v", got)
	}
}

func TestDialogType(t *testing.T) {
	for typ, want := range map[Type]bool{Permission: true, Question: true, Review: false, Limit: false, Arbitrage: false, Unknown: false} {
		if got := DialogType(typ); got != want {
			t.Errorf("DialogType(%q) = %v, want %v", typ, got, want)
		}
	}
}

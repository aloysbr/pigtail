package waittype

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// maxDialogCmdRunes bounds the command shown for a permission dialog
// (technical choice of sub_x000000000000026).
const maxDialogCmdRunes = 1000

// maxDialogOptions is the cap waitdetail.Validate puts on options.
const maxDialogOptions = 10

// DialogType reports whether t is the wait of a Claude Code dialog that
// opens a wait record: a permission or a question. Readable, never answered
// outside the terminal (D2).
func DialogType(t Type) bool {
	return t == Permission || t == Question
}

// DialogDetails is what a hook payload really carries of a dialog (no dialog
// id, no permission choice), nil when it carries nothing beyond the reason:
//
//	PermissionRequest of a plain tool  → cmd: the Bash command, else the tool
//	                                     and its file_path|url|path|pattern,
//	                                     else the tool; where: the cwd
//	PreToolUse / PermissionRequest of
//	AskUserQuestion                    → its first question: title (header),
//	                                     summary (question), options
//	Notification elicitation_dialog,
//	elicitation_url_dialog             → summary: the message
//	Notification permission_prompt,
//	ExitPlanMode, anything else        → nil
func DialogDetails(in HookInput) *pigtail.WaitDetails {
	switch in.HookEventName {
	case "PermissionRequest":
		switch in.ToolName {
		case "AskUserQuestion":
			return questionDetails(in.ToolInput)
		case "ExitPlanMode":
			return nil
		}
		return permissionDetails(in)
	case "PreToolUse":
		if in.ToolName == "AskUserQuestion" {
			return questionDetails(in.ToolInput)
		}
	case "Notification":
		switch in.NotificationType {
		case "elicitation_dialog", "elicitation_url_dialog":
			if m := strings.TrimSpace(in.Message); m != "" {
				return envelope(&pigtail.WaitDetails{Summary: m}, Question)
			}
		}
	}
	return nil
}

func permissionDetails(in HookInput) *pigtail.WaitDetails {
	tool := strings.TrimSpace(in.ToolName)
	var input map[string]json.RawMessage
	if len(in.ToolInput) > 0 {
		_ = json.Unmarshal(in.ToolInput, &input) // a payload of another shape reads as no argument
	}
	cmd := ""
	if tool == "Bash" {
		cmd = stringField(input, "command")
	}
	if cmd == "" {
		cmd = tool
		for _, k := range []string{"file_path", "url", "path", "pattern"} {
			if v := stringField(input, k); v != "" {
				cmd = strings.TrimSpace(tool + " " + v)
				break
			}
		}
	}
	if cmd == "" && in.Cwd == "" {
		return nil
	}
	return envelope(&pigtail.WaitDetails{Cmd: cutRunes(cmd, maxDialogCmdRunes), Where: in.Cwd}, Permission)
}

func questionDetails(raw json.RawMessage) *pigtail.WaitDetails {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil || len(in.Questions) == 0 {
		return nil
	}
	q := in.Questions[0]
	d := &pigtail.WaitDetails{Title: strings.TrimSpace(q.Header), Summary: strings.TrimSpace(q.Question)}
	for i, o := range q.Options {
		if i == maxDialogOptions {
			break
		}
		d.Options = append(d.Options, pigtail.WaitOption{ID: strconv.Itoa(i + 1), Label: o.Label, Detail: o.Description})
	}
	if d.Title == "" && d.Summary == "" && len(d.Options) == 0 {
		return nil
	}
	return envelope(d, Question)
}

// envelope sets the versioned envelope of a dialog's details (D28 c).
func envelope(d *pigtail.WaitDetails, t Type) *pigtail.WaitDetails {
	d.Normalize(string(t))
	return d
}

// stringField is input[k] when it is a JSON string, else "".
func stringField(input map[string]json.RawMessage, k string) string {
	var s string
	if raw, ok := input[k]; ok && json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

package waitdetail

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestValidate(t *testing.T) {
	big := `{"summary":"` + strings.Repeat("a", 17<<10) + `"}`
	cases := []struct {
		name    string
		typ     waittype.Type
		raw     string
		wantErr string // "" = valid
	}{
		{"arbitrage: two options, one reco, why", waittype.Arbitrage,
			`{"title":"D1","options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b"}],"why":"cheaper"}`, ""},
		{"arbitrage: two recos", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b","reco":true}],"why":"w"}`, "details.options[1].reco"},
		{"arbitrage: no reco", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a"},{"id":"B","label":"b"}],"why":"w"}`, "details.options"},
		{"arbitrage: one option", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a","reco":true}],"why":"w"}`, "details.options"},
		{"arbitrage: duplicate id", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a","reco":true},{"id":"A","label":"b"}],"why":"w"}`, "details.options[1].id"},
		{"arbitrage: empty id", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a","reco":true},{"id":"","label":"b"}],"why":"w"}`, "details.options[1].id"},
		{"arbitrage: no why", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b"}]}`, "details.why"},
		{"arbitrage: other refused", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b"}],"why":"w","other":"x"}`, "details.other"},
		{"arbitrage: items refused", waittype.Arbitrage,
			`{"options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b"}],"why":"w","items":[{"label":"x"}]}`, "details.items"},
		{"review: nothing", waittype.Review, `{}`, "details.items"},
		{"review: items", waittype.Review, `{"items":[{"label":"x","url":"http://localhost:3000/a"}]}`, ""},
		{"review: url and lead", waittype.Review, `{"url":"https://github.com/o/r/pull/1","lead":"read the body"}`, ""},
		{"review: items and url+lead", waittype.Review,
			`{"items":[{"label":"x"}],"url":"https://x.test","lead":"l"}`, "details.items"},
		{"review: options refused", waittype.Review,
			`{"items":[{"label":"x"}],"options":[{"id":"1","label":"a"}]}`, "details.options"},
		{"review: 31 items", waittype.Review, `{"items":[` + strings.TrimSuffix(strings.Repeat(`{"label":"x"},`, 31), ",") + `]}`, "details.items"},
		{"question: option with reco", waittype.Question,
			`{"options":[{"id":"1","label":"a","reco":true}]}`, "details.options[0].reco"},
		{"question: options and other", waittype.Question,
			`{"summary":"which?","options":[{"id":"1","label":"a"}],"other":"free text"}`, ""},
		{"question: option without id", waittype.Question, `{"options":[{"label":"a"}]}`, "details.options[0].id"},
		{"question: blank id", waittype.Question, `{"options":[{"id":" ","label":"a"}]}`, "details.options[0].id"},
		{"question: duplicate id", waittype.Question, `{"options":[{"id":"1","label":"a"},{"id":"1","label":"b"}]}`, "details.options[1].id"},
		{"question: why refused", waittype.Question, `{"why":"w"}`, "details.why"},
		{"question: 11 options", waittype.Question, `{"options":[` + strings.TrimSuffix(strings.Repeat(`{"id":"1","label":"x"},`, 11), ",") + `]}`, "details.options"},
		{"permission: cmd and where", waittype.Permission, `{"cmd":"make","where":"/w"}`, ""},
		{"permission: options refused", waittype.Permission, `{"options":[{"id":"1","label":"a"}]}`, "details.options"},
		{"blocked: common fields", waittype.Blocked, `{"title":"t","summary":"s","step":"login","url":"/abs/path"}`, ""},
		{"done: cmd refused", waittype.Done, `{"cmd":"x"}`, "details.cmd"},
		{"unknown field", waittype.Blocked, `{"foo":1}`, "foo"},
		{"too big", waittype.Blocked, big, "16384"},
		{"secret in summary", waittype.Blocked, `{"summary":"ghp_0123456789abcdefghij0123456789abcd"}`, "details.summary"},
		{"secret in an option label", waittype.Question, `{"options":[{"id":"1","label":"ghp_0123456789abcdefghij"}]}`, "details.options[0].label"},
		{"relative url", waittype.Blocked, `{"url":"a/b"}`, "details.url"},
		{"bad shot src", waittype.Blocked, `{"shots":[{"src":"ftp://x"}]}`, "details.shots[0].src"},
		{"file url", waittype.Blocked, `{"shots":[{"src":"file:///tmp/a.png","caption":"c"}]}`, ""},
		{"11 shots", waittype.Blocked, `{"shots":[` + strings.TrimSuffix(strings.Repeat(`{"src":"/a.png"},`, 11), ",") + `]}`, "details.shots"},
		{"trailing data", waittype.Blocked, `{} {}`, "details"},
		{"not an object", waittype.Blocked, `[1]`, "details"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Validate(c.typ, []byte(c.raw))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if d == nil {
					t.Fatal("Validate: nil details for a valid content")
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted %s", c.raw)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not name %q", err, c.wantErr)
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("error %T is not a *FieldError", err)
			}
			if !strings.HasPrefix(err.Error(), "details") {
				t.Fatalf("error %q does not start with details", err)
			}
		})
	}
}

func TestValidateArbitrageKeepsTheReco(t *testing.T) {
	d, err := Validate(waittype.Arbitrage, []byte(`{"options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b"}],"why":"w"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Options[0].Reco || d.Options[1].Reco {
		t.Fatalf("reco not kept: %+v", d.Options)
	}
}

func TestValidateEmpty(t *testing.T) {
	for _, raw := range [][]byte{nil, {}} {
		d, err := Validate(waittype.Arbitrage, raw)
		if d != nil || err != nil {
			t.Fatalf("Validate(%q) = %v, %v; want nil, nil", raw, d, err)
		}
	}
}

func TestFieldErrorFormat(t *testing.T) {
	if got := (&FieldError{Field: "options[1].reco", Msg: "only one reco"}).Error(); got != "details.options[1].reco: only one reco" {
		t.Fatalf("Error() = %q", got)
	}
}

// TestDialogDetailsPassValidate replays the payloads of waittype's
// TestDialogDetails: what the hook reads of a dialog is always a valid
// content of the dialog's type.
func TestDialogDetailsPassValidate(t *testing.T) {
	payloads := []string{
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make"},"cwd":"/w"}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":{"file_path":"/w/a.go"},"cwd":"/w"}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"WebFetch","tool_input":{"url":"https://x.test","prompt":"p"}}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"mcp__x__y","tool_input":{"a":1},"cwd":"/w"}`,
		`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which library should we use for dates?","header":"Date lib","options":[{"label":"a"},{"label":"b"}],"multiSelect":false}]}}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which?","header":"D1","options":[{"label":"a","description":"da"}]}]}}`,
		`{"hook_event_name":"Notification","message":"Pick a server","notification_type":"elicitation_dialog"}`,
	}
	for _, p := range payloads {
		var in waittype.HookInput
		if err := json.Unmarshal([]byte(p), &in); err != nil {
			t.Fatal(err)
		}
		d := waittype.DialogDetails(in)
		if d == nil {
			t.Fatalf("no details for %s", p)
		}
		r := waittype.FromHook(in)
		if !waittype.DialogType(r.Type) {
			t.Fatalf("%s: wait type %q is not a dialog", p, r.Type)
		}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Validate(r.Type, raw); err != nil {
			t.Errorf("%s: details of a %s do not pass Validate: %v", p, r.Type, err)
		}
		if d.Kind != string(r.Type) || d.Schema != pigtail.WaitDetailsSchema {
			t.Errorf("%s: envelope = %q/%d, want %q/%d", p, d.Kind, d.Schema, r.Type, pigtail.WaitDetailsSchema)
		}
	}
}

// TestValidateEnvelope: kind and schema (D28 c) are optional, normalised when
// absent, refused in clear when they disagree with the wait or name a version
// this pigtaild does not read.
func TestValidateEnvelope(t *testing.T) {
	const opts = `"options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b"}],"why":"w"`
	d, err := Validate(waittype.Arbitrage, []byte(`{`+opts+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "arbitrage" || d.Schema != 1 {
		t.Fatalf("envelope not normalised: kind %q schema %d", d.Kind, d.Schema)
	}
	_, err = Validate(waittype.Arbitrage, []byte(`{"schema":2,`+opts+`}`))
	if err == nil || !strings.Contains(err.Error(), "details.schema") || !strings.Contains(err.Error(), "2") {
		t.Fatalf("schema 2: err = %v", err)
	}
	_, err = Validate(waittype.Arbitrage, []byte(`{"kind":"review",`+opts+`}`))
	if err == nil || !strings.Contains(err.Error(), "details.kind") {
		t.Fatalf("kind review on an arbitrage: err = %v", err)
	}
	if d, err := Validate(waittype.Arbitrage, []byte(`{"kind":"arbitrage","schema":1,`+opts+`}`)); err != nil || d.Kind != "arbitrage" || d.Schema != 1 {
		t.Fatalf("full envelope: %+v, %v", d, err)
	}
	if _, err := Validate(waittype.Arbitrage, []byte(`{"kind":"arbitrage","schema":1,"foo":1,`+opts+`}`)); err == nil || !strings.Contains(err.Error(), "foo") {
		t.Fatalf("unknown field: err = %v", err)
	}
}

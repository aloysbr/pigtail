package cmux

import (
	"strings"
	"testing"
)

func TestInCmux(t *testing.T) {
	t.Setenv(EnvWorkspaceID, "")
	if InCmux() {
		t.Error("empty workspace id should not count as in-cmux")
	}
	t.Setenv(EnvWorkspaceID, "WS-123")
	if !InCmux() {
		t.Error("set workspace id should count as in-cmux")
	}
	if WorkspaceID() != "WS-123" {
		t.Errorf("WorkspaceID() = %q, want WS-123", WorkspaceID())
	}
}

func TestParseSurfaceRef(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    string
		wantErr bool
	}{
		{"typical", "OK surface:33 pane:15 workspace:15", "surface:33", false},
		{"leading whitespace", "\n  OK surface:7 workspace:2\n", "surface:7", false},
		{"multi-digit", "OK surface:1024 pane:1", "surface:1024", false},
		{"no ref", "OK pane:15 workspace:15", "", true},
		{"empty", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSurfaceRef(c.out)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", c.out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("parseSurfaceRef(%q) = %q, want %q", c.out, got, c.want)
			}
		})
	}
}

func TestNewSurfaceArgs(t *testing.T) {
	args := newSurfaceArgs("WS-1", "/wt/feat-x")
	joined := strings.Join(args, " ")
	want := "new-surface --workspace WS-1 --working-directory /wt/feat-x --focus false"
	if joined != want {
		t.Errorf("newSurfaceArgs = %q, want %q", joined, want)
	}
}

func TestSendArgs(t *testing.T) {
	args := sendArgs("surface:5", "/wt/feat-x", "set -a && . .env.local && set +a && npm run dev")
	if len(args) != 4 {
		t.Fatalf("sendArgs len = %d, want 4 (send --surface <ref> <payload>)", len(args))
	}
	if args[0] != "send" || args[1] != "--surface" || args[2] != "surface:5" {
		t.Errorf("unexpected head args: %v", args[:3])
	}
	payload := args[3]
	// Explicit cd prefix so the sidecar shell lands in the worktree.
	if !strings.HasPrefix(payload, "cd '/wt/feat-x' && ") {
		t.Errorf("payload should start with a quoted cd into the worktree, got %q", payload)
	}
	if !strings.Contains(payload, "npm run dev") {
		t.Errorf("payload should carry the wrapped command, got %q", payload)
	}
	// Literal backslash-n (cmux interprets it as Enter); NOT a real newline.
	if !strings.HasSuffix(payload, `\n`) {
		t.Errorf("payload should end with a literal backslash-n, got %q", payload)
	}
	if strings.Contains(payload, "\n") {
		t.Errorf("payload must not contain a real newline, got %q", payload)
	}
}

func TestShellQuotePathWithSpace(t *testing.T) {
	got := shellQuote("/wt/my feature")
	if got != "'/wt/my feature'" {
		t.Errorf("shellQuote = %q, want '/wt/my feature'", got)
	}
	// Embedded single quote is escaped.
	if q := shellQuote("a'b"); q != `'a'\''b'` {
		t.Errorf("shellQuote(a'b) = %q, want 'a'\\''b'", q)
	}
}

// A real binding, as printed by `cmux surface resume show`. The quoting is
// cmux's own: the command is a /bin/sh -c payload with escaped single quotes.
const realBinding = `cd -- '/Users/a/code/acme' 2>/dev/null || [ ! -d '/Users/a/code/acme' ] && /bin/sh -c '"$([ -x "${CMUX_CLAUDE_WRAPPER_SHIM:-}" ] && printf '\''%s'\'' "$CMUX_CLAUDE_WRAPPER_SHIM" || printf claude)" '\''--resume'\'' '\''1bb0480e-a291-4dab-bb69-8190b97b6824'\'' '\''--permission-mode'\'' '\''auto'\'''`

func TestSessionIDFrom(t *testing.T) {
	if got, want := SessionIDFrom(realBinding), "1bb0480e-a291-4dab-bb69-8190b97b6824"; got != want {
		t.Fatalf("SessionIDFrom = %q, want %q", got, want)
	}
	if got := SessionIDFrom("No resume binding"); got != "" {
		t.Fatalf("SessionIDFrom on a bindingless surface = %q, want empty", got)
	}
}

func TestCwdFrom(t *testing.T) {
	if got, want := CwdFrom(realBinding), "/Users/a/code/acme"; got != want {
		t.Fatalf("CwdFrom = %q, want %q", got, want)
	}
	if got := CwdFrom("nothing here"); got != "" {
		t.Fatalf("CwdFrom = %q, want empty", got)
	}
}

func TestSurfaceRefsFromTree(t *testing.T) {
	const tree = `window window:1 [current]
├── workspace workspace:3 "Shed"
│   └── pane pane:3 [focused]
│       ├── surface surface:3 [terminal] "Investigate" tty=ttys009
│       └── surface surface:5 [terminal] "~/Developer" tty=ttys018
`
	got := parseSurfaceRefs(tree)
	want := []string{"surface:3", "surface:5"}
	if len(got) != len(want) {
		t.Fatalf("parseSurfaceRefs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseSurfaceRefs = %v, want %v", got, want)
		}
	}
}

func TestResumeSetArgs(t *testing.T) {
	got := resumeSetArgs("surface:3", "/w t", "abc-123", "/home/u/.claude-two-max", "", "auto")
	want := []string{
		"surface", "resume", "set",
		"--surface", "surface:3",
		"--cwd", "/w t",
		"--kind", "agent",
		"--checkpoint", "abc-123",
		"--",
		"/usr/bin/env", "CLAUDE_CONFIG_DIR=/home/u/.claude-two-max",
		"claude", "--resume", "abc-123", "--permission-mode", "auto",
	}
	if len(got) != len(want) {
		t.Fatalf("resumeSetArgs =\n%v\nwant\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSendLineArgsAppendEnter(t *testing.T) {
	got := sendLineArgs("surface:3", "cr mt -r abc")
	want := []string{"send", "--surface", "surface:3", `cr mt -r abc\n`}
	if len(got) != len(want) {
		t.Fatalf("sendLineArgs =\n%v\nwant\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSessionIDFromAnchoredOnResume(t *testing.T) {
	// A binding where the cwd contains a UUID-shaped path segment.
	// The old unanchored regex would return the cwd's UUID, not the session id.
	bindingWithUUIDPath := `cd -- '/Users/a/11bb0480-a291-4dab-bb69-8190b97b6824/proj' 2>/dev/null && /bin/sh -c '"$([ -x "${CMUX_CLAUDE_WRAPPER_SHIM:-}" ] && printf '\''%s'\'' "$CMUX_CLAUDE_WRAPPER_SHIM" || printf claude)" '\''--resume'\'' '\''22cc0480-a291-4dab-bb69-8190b97b6825'\'' '\''--permission-mode'\'' '\''auto'\'''`
	if got := SessionIDFrom(bindingWithUUIDPath); got != "22cc0480-a291-4dab-bb69-8190b97b6825" {
		t.Fatalf("SessionIDFrom with UUID-shaped cwd = %q, want 22cc0480-a291-4dab-bb69-8190b97b6825", got)
	}
}

// TestCallerSurfaceRefFromIdentify pins the reason CurrentSurfaceRef exists:
// $CMUX_SURFACE_ID holds the surface's UUID, while every ref pigtail compares
// it with comes from `cmux tree` as `surface:N`. The two never match, so the
// caller's own pane was invisible to `session move` — `--all` then selected
// the very session running it. `cmux identify` is the one call that maps the
// caller onto its short ref.
func TestCallerSurfaceRefFromIdentify(t *testing.T) {
	const out = `{
  "app_cli_path" : "/Applications/cmux.app/Contents/Resources/bin/cmux",
  "caller" : {
    "is_browser_surface" : false,
    "pane_ref" : "pane:81",
    "surface_ref" : "surface:93",
    "surface_type" : "terminal",
    "workspace_ref" : "workspace:80"
  },
  "focused" : {
    "surface_ref" : "surface:11"
  }
}`
	got, err := parseCallerSurfaceRef([]byte(out))
	if err != nil {
		t.Fatalf("parseCallerSurfaceRef: %v", err)
	}
	if got != "surface:93" {
		t.Fatalf("got %q, want the caller's ref surface:93 (not the focused surface:11)", got)
	}
}

// A caller cmux cannot place (surface_ref null) must not come back as an
// empty ref: the caller would then match no row, and --all would move the
// session running it — the exact bug this guards against.
func TestCallerSurfaceRefUnresolvedIsAnError(t *testing.T) {
	for name, out := range map[string]string{
		"null ref":    `{"caller": {"surface_ref": null, "window_ref": "window:1"}}`,
		"no caller":   `{"socket_path": "/tmp/cmux.sock"}`,
		"not json":    `Error: surface not found`,
		"wrong shape": `{"caller": {"surface_ref": "pane:3"}}`,
	} {
		if ref, err := parseCallerSurfaceRef([]byte(out)); err == nil {
			t.Errorf("%s: got ref %q and no error, want an error", name, ref)
		}
	}
}

func TestCurrentSurfaceRefOutsideCmux(t *testing.T) {
	t.Setenv(EnvSurfaceID, "")
	ref, err := CurrentSurfaceRef()
	if err != nil || ref != "" {
		t.Fatalf("CurrentSurfaceRef() = %q, %v; want \"\", nil outside cmux", ref, err)
	}
}

func TestSendKeyArgs(t *testing.T) {
	got := sendKeyArgs("surface:3", "Escape")
	want := []string{"send-key", "--surface", "surface:3", "--", "Escape"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sendKeyArgs = %v, want %v", got, want)
	}
}

// TestReadScreenArgsVisibleViewportOnly pins the absence of --scrollback and
// --lines: a dialog is only open if it is on screen now, not somewhere in the
// scrollback of an earlier one.
func TestReadScreenArgsVisibleViewportOnly(t *testing.T) {
	got := readScreenArgs("surface:3")
	want := []string{"read-screen", "--surface", "surface:3"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("readScreenArgs = %v, want %v", got, want)
	}
}

// TestResumeSetArgsThroughWrapper: the binding cmux replays on restore goes
// through cmux-claude-wrapper by absolute path, so the restored pane does not
// depend on its (possibly empty) claude shim. It is an argv, not a shell
// line: the path is passed as is, unquoted.
func TestResumeSetArgsThroughWrapper(t *testing.T) {
	const w = "/Apps/c mux/bin/cmux-claude-wrapper"
	got := resumeSetArgs("surface:3", "/w", "abc-123", "/cfg", w, "auto")
	tail := []string{"--", "/usr/bin/env", "CLAUDE_CONFIG_DIR=/cfg", w, "--resume", "abc-123", "--permission-mode", "auto"}
	if len(got) < len(tail) {
		t.Fatalf("resumeSetArgs = %v", got)
	}
	gotTail := got[len(got)-len(tail):]
	for i := range tail {
		if gotTail[i] != tail[i] {
			t.Fatalf("resumeSetArgs tail = %q, want %q", gotTail, tail)
		}
	}
}

// TestResumeSetArgsMode: a respawn's binding carries its own permission mode;
// session move's path (SetResumeBinding) keeps auto.
func TestResumeSetArgsMode(t *testing.T) {
	got := resumeSetArgs("surface:3", "/w", "sid", "/cfg", "", "plan")
	tail := []string{"--resume", "sid", "--permission-mode", "plan"}
	if strings.Join(got[len(got)-len(tail):], "|") != strings.Join(tail, "|") {
		t.Fatalf("resumeSetArgs = %v, want it to end with %v", got, tail)
	}
	old := []string{"surface", "resume", "set", "--surface", "surface:3", "--cwd", "/w", "--kind", "agent",
		"--checkpoint", "sid", "--", "/usr/bin/env", "CLAUDE_CONFIG_DIR=/cfg", "claude", "--resume", "sid", "--permission-mode", "auto"}
	if a := resumeSetArgs("surface:3", "/w", "sid", "/cfg", "", "auto"); strings.Join(a, "|") != strings.Join(old, "|") {
		t.Fatalf("auto binding = %v, want the old literal %v", a, old)
	}
}

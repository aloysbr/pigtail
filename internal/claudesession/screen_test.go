package claudesession

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures reproduce the shape of Claude Code 2.1.288's screens under cmux
// read-screen; their conversation text is invented. screen-bg-agent.txt is a
// pane idle after a completed turn, one background agent running: its input
// box reads "❯" followed by a no-break space and nothing else — no suggested
// prompt. The other four keep its conversation, rules and box bytes, and
// change only the box content and the status region (typed draft,
// shift-enter, /exit to the launcher prompt).
func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInputDraft(t *testing.T) {
	cases := []struct {
		file, want string
		found      bool
	}{
		{"screen-idle-empty.txt", "", true},
		{"screen-draft.txt", "hello", true},
		{"screen-draft-multiline.txt", "", true}, // want: non-empty; asserted below
		{"screen-bg-agent.txt", "", true},
		{"screen-no-input.txt", "", false},
		// U+00A0 follows "❯", as cmux read-screen renders it — read as blank.
		{"screen-exit-dialog-cancelled.txt", "", true},
	}
	for _, c := range cases {
		got, found := InputDraft(string(mustRead(t, c.file)))
		if found != c.found {
			t.Errorf("%s: found=%v want %v", c.file, found, c.found)
		}
		if c.file == "screen-draft-multiline.txt" {
			if got != "first line\nsecond line" {
				t.Errorf("multiline draft = %q", got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: draft=%q want %q", c.file, got, c.want)
		}
	}
	// a "❯" quoted in the conversation above the box is not the box
	quoted := "assistant said: ❯ run this\n" + string(mustRead(t, "screen-idle-empty.txt"))
	if got, _ := InputDraft(quoted); got != "" {
		t.Errorf("quoted ❯ read as a draft: %q", got)
	}
	if _, found := InputDraft(""); found {
		t.Error("an empty screen has no box")
	}
}

func TestBackgroundWorkShown(t *testing.T) {
	if !BackgroundWorkShown(string(mustRead(t, "screen-bg-agent.txt"))) {
		t.Error("background agent not seen")
	}
	// The box back after Esc on the background-work
	// dialog, a shell still running ("1 shell · … · ↓ to manage").
	if !BackgroundWorkShown(string(mustRead(t, "screen-exit-dialog-cancelled.txt"))) {
		t.Error("background shell after the cancelled exit dialog not seen")
	}
	if BackgroundWorkShown(string(mustRead(t, "screen-idle-empty.txt"))) {
		t.Error("idle screen read as background work")
	}
	quoted := "we discussed the background agent and ↓ to manage\nWaiting for 1 background agent to finish (quoted)\n\n" + string(mustRead(t, "screen-idle-empty.txt"))
	if BackgroundWorkShown(quoted) {
		t.Error("words quoted in the conversation read as background work")
	}
	rule := "────────────────────────────────────────"
	waiting := "conversation\n\n✻ Waiting for 1 background agent to finish\n" + rule + "\n❯ \n" + rule + "\n  ? for shortcuts\n"
	if !BackgroundWorkShown(waiting) {
		t.Error("the waiting line above the box not seen")
	}
	// Claude's line for a task the relaunch killed sits just above the box;
	// it is history, not running work (reported only).
	lost := string(mustRead(t, "screen-bg-lost.txt"))
	if BackgroundWorkShown(lost) {
		t.Error("the lost-background line above the box read as running work")
	}
	// Agent rows below the box still count behind such a line.
	if !BackgroundWorkShown(lost + "\n  ◯ verify static rows · 2m\n  ↓ to manage\n") {
		t.Error("agent rows below the box not seen behind a lost-background line")
	}
}

// screen-draft-history.txt has the shape of a 2.1.288 screen: a draft recalled
// from the prompt history with ↑, where Claude Code labels the box's opening rule
// "─── History 99/100 ───…". The label must not hide the box.
func TestInputDraftHistoryLabelledRule(t *testing.T) {
	screen := string(mustRead(t, "screen-draft-history.txt"))
	if got, found := InputDraft(screen); !found || got != "ijfafjioezaigjf" {
		t.Errorf("history draft = (%q, %v), want (%q, true)", got, found, "ijfafjioezaigjf")
	}
	if BackgroundWorkShown(screen) {
		t.Error("history screen read as background work")
	}

	rule := "────────────────────────────────────────────────────────────"
	cleared := "conversation\n\n─── History 99/100 " + rule + "\n❯ \n" + rule + "\n  ⏵⏵ auto mode on\n"
	if got, found := InputDraft(cleared); !found || got != "" {
		t.Errorf("history recalled then cleared = (%q, %v), want (\"\", true)", got, found)
	}
}

// Only a frame rule may carry a label: a conversation line holding a few "─"
// or a label not framed by "─" never opens a box.
func TestIsRuleLabelled(t *testing.T) {
	long := "────────────────────────────────────────"
	cases := []struct {
		line string
		want bool
	}{
		{long, true},
		{"─── History 99/100 " + long, true},
		{"  ─── History 1/3 " + long + "  ", true},
		{"─── History 99/100 ───", false},        // too few "─"
		{"History 99/100 " + long, false},        // does not start with "─"
		{"─── History " + long + " tail", false}, // does not end with "─"
		{"─── a " + long + " b " + long, false},  // two labels
		{"see ─── this ───", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isRule(c.line); got != c.want {
			t.Errorf("isRule(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

// A fresh session shows a dimmed suggestion in its empty box; read-screen
// returns it as plain text. Only its exact shape counts as a placeholder.
func TestInputPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		draft string
		want  bool
	}{
		{`Try "how does <filepath> work?"`, true},
		{`Try "create a util logging.py that..."`, true},
		{`Try "create a util logging.py that…"`, true},
		{``, false},
		{`Try "foo`, false},
		{`Try "x" and more`, false},
		{`Try ""`, false},
		{`Try "a "quoted" b"`, false},
		{"Try \"how does <filepath> work?\"\nmore", false},
		{"Try \"line one\nline two\"", false},
		{`try "lowercase"`, false},
		{`Try  "two spaces"`, false},
		{`lis ta boîte pigtail`, false},
	} {
		if got := InputPlaceholder(tc.draft); got != tc.want {
			t.Errorf("InputPlaceholder(%q) = %v, want %v", tc.draft, got, tc.want)
		}
	}
}

// The confirming footer counts only on the last two non-empty
// lines — under the input box —, never quoted higher in the conversation.
func TestCtrlCAgainShownReadsTheEnd(t *testing.T) {
	b, err := os.ReadFile("testdata/screen-ctrlc-again.txt")
	if err != nil {
		t.Fatal(err)
	}
	real := string(b)
	if !CtrlCAgainShown(real) {
		t.Fatal("read after the first Ctrl+C: footer not seen")
	}
	idle, err := os.ReadFile("testdata/screen-idle-empty.txt")
	if err != nil {
		t.Fatal(err)
	}
	if CtrlCAgainShown(string(idle)) {
		t.Fatal("idle screen: no footer expected")
	}
	quoted := "⏺ the footer reads \"Press Ctrl-C again to exit\" after one press\n\n" + string(idle)
	if CtrlCAgainShown(quoted) {
		t.Fatal("footer quoted in the conversation must not count")
	}
	if CtrlCAgainShown("") {
		t.Fatal("empty screen")
	}
}

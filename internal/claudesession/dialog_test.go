package claudesession

import (
	"os"
	"strings"
	"testing"
)

// T-h / F11: the dialogs a resumed session can stop on. Each kind needs its
// option lines (a whole line each), so a dialog quoted in the conversation
// above an idle prompt never counts.
func TestDialogShown(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	idle := read("screen-idle-empty.txt")
	rule := strings.Repeat("─", 40)
	box := "\n" + rule + "\n❯ \n" + rule + "\n" // a short box: the dialog above stays in the last 20 lines
	cases := []struct {
		name, screen, want string
	}{
		{"folder trust (08/10 12:40)", read("screen-trust-dialog.txt"), "folder trust"},
		{"folder trust, older wording", "Do you trust the files in this folder?\n\n ❯ 1. Yes, proceed\n   2. No, exit\n\n Enter to confirm · Esc to cancel\n", "folder trust"},
		{"background work at /exit", exitDialogScreen, "background work"},
		{"unknown dialog footer", " Pick one\n ❯ 1. A\n   2. B\n\n Enter to confirm · Esc to cancel\n", "dialog"},
		{"idle prompt", idle, ""},
		{"draft", read("screen-draft.txt"), ""},
		{"trust quoted in the conversation", "⏺ it asked No, exit or Yes, I trust this folder\n" + idle, ""},
		// T-k / F15 (13:24): the trust dialog shows before the REPL starts, so
		// a box drawn below it proves it answered — ignored above the box,
		// its own footer included.
		{"trust answered, left above the box", read("screen-restore-after-trust.txt"), ""},
		// Round 6: whether the other dialogs leave text above a redrawn box
		// is not measured — read on the whole tail, they still count (blocks).
		{"background work drawn above a visible box", exitDialogScreen + box, "background work"},
		{"unknown footer above a visible box", " Pick one\n ❯ 1. A\n   2. B\n\n Enter to confirm · Esc to cancel\n" + box, "dialog"},
		{"background work opened below a box", idle + "\n" + exitDialogScreen, "background work"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := DialogShown(tc.screen)
			if kind != tc.want || ok != (tc.want != "") {
				t.Fatalf("DialogShown = %q, %v, want %q", kind, ok, tc.want)
			}
		})
	}
}

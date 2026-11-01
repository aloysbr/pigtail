package claudesession

import "strings"

// DialogShown reports whether a screen shows one of Claude Code's modal
// dialogs, and which: a session stopped on one waits for a human, and an
// Enter typed into it picks an option (the folder-trust dialog preselects
// "No, exit"). Only the bottom of the screen counts (a dialog is drawn
// last), and each kind needs its option lines as whole lines, so a dialog
// quoted in the conversation does not count. Strings are Claude Code's own
// (measured 2026-10-08); should they change, an unknown dialog is still seen
// by its "Enter to confirm · Esc to cancel" footer, else the caller blocks on
// "no input box" — never typed into either way.
//
// T-k (F15, 13:24), narrowed at review round 6: the folder-trust dialog shows
// before the REPL starts, so an input box drawn below it proves it was
// answered — it, and its own footer, count only below the last input box.
// The background-work dialog and an unknown footer keep the whole tail:
// whether they leave text above a redrawn box is not measured, and reading
// them there blocks (safe).
func DialogShown(screen string) (string, bool) {
	whole := lastLines(screen, 20)
	below := lastLines(belowInputBox(screen), 20)
	if ExitDialogShown(whole) {
		return "background work", true
	}
	if trustShown(below) {
		return "folder trust", true
	}
	footer := whole
	if trustShown(whole) {
		footer = below // the answered trust dialog's own footer, above the box
	}
	for _, l := range strings.Split(footer, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "Enter to confirm") && strings.Contains(t, "Esc to cancel") {
			return "dialog", true
		}
	}
	return "", false
}

// ExitDialogBelowBox: "Background work is running" below the last input box
// (the whole tail when none) — É2's reading for waitExit and
// cancelOwnExitDialog. Missing it leaves the pid alive: "did not exit",
// nothing typed (safe).
func ExitDialogBelowBox(screen string) bool {
	return ExitDialogShown(lastLines(belowInputBox(screen), 20))
}

func trustShown(s string) bool {
	return optionLine(s, "No, exit") && (optionLine(s, "Yes, I trust this folder") || optionLine(s, "Yes, proceed"))
}

// optionLine: some line is exactly label once its cursor ("❯") and its
// number ("2.") are stripped.
func optionLine(s, label string) bool {
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "❯"))
		if i := strings.Index(t, ". "); i > 0 && strings.Trim(t[:i], "0123456789") == "" {
			t = t[i+2:]
		}
		if t == label {
			return true
		}
	}
	return false
}

// belowInputBox is the screen below the closing rule of the last input box
// (a box whose first line is the "❯" prompt, as InputDraft reads it), or the
// whole screen when there is none.
func belowInputBox(screen string) string {
	if _, found := InputDraft(screen); !found {
		return screen
	}
	lines := strings.Split(screen, "\n")
	_, closing, _ := inputBox(lines)
	return strings.Join(lines[closing+1:], "\n")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n "), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

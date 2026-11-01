package claudesession

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Claude Code's input box, as cmux read-screen renders it (2.1.288): a rule of
// "─" (possibly labelled, see isRule), the input line starting with "❯", any continuation lines, a closing
// rule. Below the closing rule sits the status region (mode footer, agent
// rows); just above the opening rule, the spinner/status line while work runs.
const (
	inputPrompt  = "❯"
	ruleRune     = '─'
	minRuleRunes = 20
)

// isRule reports whether a screen line is one of the box's horizontal rules:
// only "─", or one label framed by "─" — Claude Code labels the opening rule
// while a prompt is recalled from history ("─── History 99/100 ───…"). The
// line starts and ends with "─" and holds ≥ 20 of them, so a conversation line
// with a few "─" never counts.
func isRule(line string) bool {
	t := strings.TrimFunc(line, unicode.IsSpace)
	rule := string(ruleRune)
	if !strings.HasPrefix(t, rule) || !strings.HasSuffix(t, rule) {
		return false
	}
	label := strings.Trim(t, rule)
	if strings.ContainsRune(label, ruleRune) {
		return false // more than one label
	}
	return utf8.RuneCountInString(t)-utf8.RuneCountInString(label) >= minRuleRunes
}

// inputBox locates the box: the indices of the last two rule lines, the
// second being the closing one. ok=false when fewer than two rules show.
func inputBox(lines []string) (open, closing int, ok bool) {
	open, closing = -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !isRule(lines[i]) {
			continue
		}
		if closing < 0 {
			closing = i
			continue
		}
		open = i
		break
	}
	return open, closing, open >= 0
}

// InputDraft returns the text typed in Claude Code's input box and whether the
// box was found: the line starting with "❯" between the last two rules of "─"
// (≥ 20 runes), plus any non-blank line before the closing rule. found=false
// when no box is on screen (a dialog, a launcher prompt): callers fail closed.
func InputDraft(screen string) (draft string, found bool) {
	lines := strings.Split(screen, "\n")
	open, closing, ok := inputBox(lines)
	if !ok || closing-open < 2 {
		return "", false
	}
	first := strings.TrimLeftFunc(lines[open+1], unicode.IsSpace)
	if !strings.HasPrefix(first, inputPrompt) {
		return "", false
	}
	parts := []string{strings.TrimFunc(strings.TrimPrefix(first, inputPrompt), unicode.IsSpace)}
	for _, l := range lines[open+2 : closing] {
		if t := strings.TrimFunc(l, unicode.IsSpace); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.TrimFunc(strings.Join(parts, "\n"), unicode.IsSpace), true
}

// messageMarker starts a message block in Claude Code's conversation ("⏺ …"
// an assistant message or a tool call; its continuation lines are indented).
const messageMarker = "⏺"

// StatusArea returns the screen's status area and whether the box was found:
// the last message block above the input box — from the last line starting
// with "⏺" down to the box's opening rule, where Claude draws a usage-limit
// notice and its "⎿ …" lines (D8) — and every line below the closing rule (the
// footer, agent rows). With no "⏺" line above the box, only the non-blank line
// just above it (where "✻ …" status lines are drawn). The older conversation
// is excluded: words quoted there are history, not state. found=false when no
// box is on screen.
func StatusArea(screen string) (area string, found bool) {
	lines := strings.Split(screen, "\n")
	open, closing, ok := inputBox(lines)
	if !ok {
		return "", false
	}
	var out []string
	start := -1
	for i := open - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimFunc(lines[i], unicode.IsSpace), messageMarker) {
			start = i
			break
		}
	}
	if start >= 0 {
		out = append(out, lines[start:open]...)
	} else {
		for i := open - 1; i >= 0; i-- {
			if strings.TrimFunc(lines[i], unicode.IsSpace) == "" {
				continue
			}
			out = append(out, lines[i])
			break
		}
	}
	out = append(out, lines[closing+1:]...)
	return strings.Join(out, "\n"), true
}

// InputPlaceholder reports whether an InputDraft result is Claude Code's
// dimmed suggestion in the empty box of a session that never ran a turn
// (`Try "how does <filepath> work?"`), not text typed by a person. cmux
// read-screen returns plain text, so only the exact shape counts: one line,
// `Try "` then a non-empty suggestion with no quote of its own, then a closing
// `"` that ends the line. Any other draft — a second line, text after the
// quote, no closing quote — stays a draft: a guard that types must never
// take typed text for a placeholder.
func InputPlaceholder(draft string) bool {
	const open = `Try "`
	if strings.ContainsAny(draft, "\r\n") || !strings.HasPrefix(draft, open) || !strings.HasSuffix(draft, `"`) {
		return false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(draft, open), `"`)
	return strings.TrimSpace(inner) != "" && !strings.Contains(inner, `"`)
}

// BackgroundWorkShown reports subagents or shells still running in the
// session, from Claude Code's own markers (2.1.288), read ONLY in the status
// region: the non-blank line just above the box's opening rule ("✻ Waiting for
// N background agent(s)…", "… background task/shell …") and the lines below
// its closing rule (the agent rows "◯ …" and the footer "↓ to manage"). The
// same words quoted in the conversation above never count, and claude's
// "Background … didn't finish" line (F17) is history and never counts.
func BackgroundWorkShown(screen string) bool {
	lines := strings.Split(screen, "\n")
	open, closing, ok := inputBox(lines)
	if !ok {
		return false
	}
	for i := open - 1; i >= 0; i-- {
		t := strings.ToLower(strings.TrimFunc(lines[i], unicode.IsSpace))
		if t == "" {
			continue
		}
		// Review round 7 (É11): claude's line for a background task the
		// relaunch killed (LostBackgroundShown, F17) is history, not the
		// status line: no running work shown above the box.
		if lostScreenRe.MatchString(lines[i]) {
			break
		}
		if strings.Contains(t, "background") &&
			(strings.Contains(t, "agent") || strings.Contains(t, "task") || strings.Contains(t, "shell")) {
			return true
		}
		break
	}
	for _, l := range lines[closing+1:] {
		t := strings.ToLower(strings.TrimFunc(l, unicode.IsSpace))
		if strings.HasPrefix(t, "◯") || strings.Contains(t, "↓ to manage") {
			return true
		}
	}
	return false
}

// CtrlCAgainText is the footer Claude Code draws under its input box after a
// first Ctrl+C on an empty input (measured 2026-10-09, claude 2.1.29x in cmux
// 0.64.24: readable even on a frozen-width screen whose box is not).
const CtrlCAgainText = "Press Ctrl-C again to exit"

// CtrlCAgainShown reports the footer on the last two non-empty lines of the
// screen — where Claude draws it, under the box —, never higher, where the
// conversation may quote it.
func CtrlCAgainShown(screen string) bool {
	lines := strings.Split(screen, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < 2; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		seen++
		if strings.Contains(l, CtrlCAgainText) {
			return true
		}
	}
	return false
}

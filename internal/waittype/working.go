package waittype

import (
	"strings"
	"unicode"
)

// WorkingMark opens the closing line of a turn whose work goes on in the
// background (for example "🔄 Running — <what runs>, back on my own …"). Such
// a line goes with `pigtail session wait working`; when the agent forgot it,
// the Stop hook infers it from the line.
const WorkingMark = "🔄"

// workingReasonRunes bounds an inferred reason: a few words, cut on a word.
const workingReasonRunes = 40

// WorkingLine reports whether msg (a turn's final assistant message) holds a
// line that, after leading whitespace, starts with WorkingMark — the last one
// counts — and the reason read from it: the text after the first " — " (or
// " – ", " - "), up to the first sentence end, without markdown emphasis,
// shortened to about workingReasonRunes runes. A line with no dash gives an
// empty reason.
func WorkingLine(msg string) (string, bool) {
	line, found := "", false
	for _, l := range strings.Split(msg, "\n") {
		if l = strings.TrimLeftFunc(l, unicode.IsSpace); strings.HasPrefix(l, WorkingMark) {
			line, found = strings.TrimPrefix(l, WorkingMark), true
		}
	}
	if !found {
		return "", false
	}
	line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
	cut := -1
	for _, d := range []string{" — ", " – ", " - "} {
		if i := strings.Index(line, d); i >= 0 && (cut < 0 || i < cut) {
			cut = i + len(d)
		}
	}
	if cut < 0 {
		return "", true
	}
	r := line[cut:]
	for i := 0; i < len(r); i++ {
		if r[i] == '.' && (i+1 == len(r) || r[i+1] == ' ') {
			r = r[:i]
			break
		}
	}
	return shortenWords(strings.Join(strings.Fields(r), " "), workingReasonRunes), true
}

// shortenWords cuts s to at most n runes on a word boundary, then adds "…".
func shortenWords(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	head := string(rs[:n+1])
	if i := strings.LastIndex(head, " "); i > 0 {
		head = head[:i]
	} else {
		head = string(rs[:n])
	}
	return strings.TrimRight(head, " ,;:") + "…"
}

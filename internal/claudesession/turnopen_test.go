package claudesession

// Synthetic lines with the real key shapes (measured 2026-10-27): a turn
// opens with a "user" entry carrying an origin, or a "system"
// scheduled_task_fire entry; queue operations, tool results, sidechains and
// queued_command attachments never open one.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const toSince = "2026-10-27T10:00:00.000Z"

func toLine(kind string, rest string) string {
	return `{"type":"` + kind + `",` + rest + `}` + "\n"
}

func toStamped(ts, kind, rest string) string {
	return toLine(kind, `"timestamp":"`+ts+`",`+rest)
}

func toWriteFile(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTurnOpenedAfter(t *testing.T) {
	later := "2026-10-27T10:00:05.000Z"
	older := "2026-10-27T09:59:00.000Z"
	cases := []struct {
		name string
		body string
		ran  bool
	}{
		{"enqueue and remove of a notification",
			toStamped(later, "queue-operation", `"operation":"enqueue","content":"<task-notification>x</task-notification>"`) +
				toStamped(later, "queue-operation", `"operation":"remove"`), false},
		{"user task-notification",
			toStamped(later, "user", `"origin":{"kind":"task-notification"},"message":{"content":"x"}`), true},
		{"user human",
			toStamped(later, "user", `"origin":{"kind":"human"},"message":{"content":"x"}`), true},
		{"user peer isMeta",
			toStamped(later, "user", `"isMeta":true,"origin":{"kind":"peer"},"message":{"content":"x"}`), true},
		{"scheduled_task_fire",
			toStamped(later, "system", `"subtype":"scheduled_task_fire"`), true},
		{"other system subtype",
			toStamped(later, "system", `"subtype":"local_command"`), false},
		{"tool_result without origin",
			toStamped(later, "user", `"message":{"content":[{"type":"tool_result"}]}`), false},
		{"null origin",
			toStamped(later, "user", `"origin":null,"message":{"content":"x"}`), false},
		{"queued_command attachment",
			toStamped(later, "attachment", `"attachment":{"type":"queued_command","origin":{"kind":"human"},"prompt":"x"}`), false},
		{"sidechain user",
			toStamped(later, "user", `"isSidechain":true,"origin":{"kind":"human"},"message":{"content":"x"}`), false},
		{"entry before toSince",
			toStamped(older, "user", `"origin":{"kind":"human"},"message":{"content":"x"}`), false},
		{"entry at toSince",
			toStamped(toSince, "user", `"origin":{"kind":"human"},"message":{"content":"x"}`), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := toWriteFile(t, c.body)
			ran, covered, next, err := TurnOpenedAfter(p, -1, at(toSince))
			if err != nil || ran != c.ran || !covered {
				t.Fatalf("ran=%v covered=%v err=%v, want ran=%v covered=true", ran, covered, err, c.ran)
			}
			if next != int64(len(c.body)) {
				t.Fatalf("next = %d, want %d", next, len(c.body))
			}
		})
	}
}

func TestTurnOpenedAfterNoTranscript(t *testing.T) {
	_, _, _, err := TurnOpenedAfter(filepath.Join(t.TempDir(), "none.jsonl"), -1, at(toSince))
	if !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("err = %v", err)
	}
	if _, _, _, err := TurnOpenedAfter("", -1, at(toSince)); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("empty path err = %v", err)
	}
}

func TestTurnOpenedAfterIncremental(t *testing.T) {
	ghost := toStamped("2026-10-27T10:00:05.000Z", "queue-operation", `"operation":"enqueue"`)
	p := toWriteFile(t, ghost)
	ran, _, next, err := TurnOpenedAfter(p, -1, at(toSince))
	if err != nil || ran {
		t.Fatalf("first read ran=%v err=%v", ran, err)
	}
	appendRaw(t, p, toStamped("2026-10-27T10:00:09.000Z", "user", `"origin":{"kind":"task-notification"},"message":{"content":"x"}`))
	ran, covered, next2, err := TurnOpenedAfter(p, next, at(toSince))
	if err != nil || !ran || !covered || next2 <= next {
		t.Fatalf("second read ran=%v covered=%v next=%d->%d err=%v", ran, covered, next, next2, err)
	}
	ran, covered, _, _ = TurnOpenedAfter(p, next2, at(toSince))
	if ran || !covered {
		t.Fatalf("nothing new: ran=%v covered=%v", ran, covered)
	}
}

// A window that starts mid-file is covered only if its first stamped line is
// not after toSince; the first lines are unstamped (last-prompt, mode, …).
func TestTurnOpenedAfterTailNotCovered(t *testing.T) {
	old := promptReadMax
	promptReadMax = 1 << 10
	t.Cleanup(func() { promptReadMax = old })
	opener := toStamped("2026-10-27T10:00:05.000Z", "user", `"origin":{"kind":"human"},"message":{"content":"x"}`)
	filler := toStamped("2026-10-27T10:00:06.000Z", "assistant", `"message":{"content":"`+strings.Repeat("a", 200)+`"}`)
	unstamped := toLine("last-prompt", `"lastPrompt":"`+strings.Repeat("b", 100)+`"`) + toLine("mode", `"mode":"auto"`)
	body := opener + strings.Repeat(filler, 12)
	p := toWriteFile(t, body)
	// The opener lies before the capped window: ran=false, not covered.
	ran, covered, _, err := TurnOpenedAfter(p, -1, at(toSince))
	if err != nil || ran || covered {
		t.Fatalf("capped tail: ran=%v covered=%v err=%v, want false/false", ran, covered, err)
	}
	// The window starts on unstamped lines, then a stamped line after toSince:
	// still not covered (judged on the first stamped line).
	p2 := toWriteFile(t, opener+strings.Repeat(unstamped, 6)+strings.Repeat(filler, 5))
	ran, covered, _, err = TurnOpenedAfter(p2, -1, at(toSince))
	if err != nil || ran || covered {
		t.Fatalf("unstamped window start: ran=%v covered=%v err=%v", ran, covered, err)
	}
	// Monotonic transcript: the window starts on lines stamped at or before
	// since, then lines after it: covered (an opener, if any, is in the window).
	early := toStamped("2026-10-27T09:59:00.000Z", "assistant", `"message":{"content":"`+strings.Repeat("c", 200)+`"}`)
	p3 := toWriteFile(t, opener0()+strings.Repeat(early, 8)+filler)
	ran, covered, _, err = TurnOpenedAfter(p3, -1, at(toSince))
	if err != nil || ran || !covered {
		t.Fatalf("monotonic window: ran=%v covered=%v err=%v, want false/true", ran, covered, err)
	}
}

// opener0 is an old human prompt, long before the bound.
func opener0() string {
	return toStamped("2026-10-27T09:00:00.000Z", "user", `"origin":{"kind":"human"},"message":{"content":"x"}`)
}

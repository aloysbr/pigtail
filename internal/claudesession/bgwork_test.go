package claudesession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	bgBash    = `{"type":"user","timestamp":"2026-10-08T10:40:00Z","toolUseResult":{"stdout":"","backgroundTaskId":"btig1w0fj"}}`
	bgAgent   = `{"type":"user","timestamp":"2026-10-08T10:41:00Z","toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a13cf0c385d07580c"}}`
	bgOld     = `{"type":"user","timestamp":"2026-10-08T09:00:00Z","toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"aold"}}`
	bgSync    = `{"type":"user","timestamp":"2026-10-08T10:42:00Z","toolUseResult":{"status":"completed","agentId":"async-false"}}`
	bgStrRes  = `{"type":"user","timestamp":"2026-10-08T10:42:30Z","toolUseResult":"Error: boom"}`
	bgBashEnd = `{"type":"queue-operation","timestamp":"2026-10-08T10:43:00Z","content":"<task-notification>\n<task-id>btig1w0fj</task-id>\n<status>completed</status>"}`
	bgAgentNt = `{"type":"attachment","timestamp":"2026-10-08T10:44:00Z","attachment":{"prompt":"<task-notification>\n<task-id>a13cf0c385d07580c</task-id>\n<status>failed</status>"}}`
	bgAgentHb = `{"type":"user","timestamp":"2026-10-08T10:44:00Z","origin":{"kind":"peer","senderTaskId":"a13cf0c385d07580c"},"message":{"content":"report"}}`
)

func TestPendingBackground(t *testing.T) {
	since := time.Date(2026, 10, 8, 10, 31, 0, 0, time.UTC) // the live process started here
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"bash and agent running", []string{tUser, bgBash, bgAgent, bgSync, bgStrRes}, "btig1w0fj,a13cf0c385d07580c"},
		{"bash ended by its notification", []string{bgBash, bgAgent, bgBashEnd}, "a13cf0c385d07580c"},
		{"agent ended by its notification", []string{bgAgent, bgAgentNt}, ""},
		{"agent ended by its hand-back", []string{bgAgent, bgAgentHb}, ""},
		{"launched before the process started: died with it", []string{bgOld}, ""},
		{"finished turn, nothing in the background", []string{tUser, tEnd, tDur}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PendingBackground(transcript(t, c.lines...), since)
			if err != nil || strings.Join(got, ",") != c.want {
				t.Fatalf("PendingBackground = %v, %v; want %q", got, err, c.want)
			}
		})
	}
	if got, err := PendingBackground(filepath.Join(t.TempDir(), "missing.jsonl"), since); err != nil || len(got) != 0 {
		t.Fatalf("missing transcript = %v, %v; want none, no error", got, err)
	}
	if _, err := PendingBackground(t.TempDir(), since); err == nil {
		t.Fatal("an unreadable transcript is an error (the caller fails closed), never \"nothing runs\"")
	}
}

// F17 / D9 (measured 08/10 12:55, Claude Code 2.1.293): on resume, claude
// marks each background task of the previous process "stopped … didn't
// finish before the previous session ended" (a queue-operation line and a
// user line, same id). A launch of the previous cmux run that never ended
// is lost too (an older claude that writes no such note).
const (
	lostNoteQ = `{"type":"queue-operation","timestamp":"2026-10-08T10:31:30Z","content":"<task-notification>\n<task-id>a9ca</task-id>\n<status>stopped</status>\n<summary>Background agent \"x implement\" didn't finish before the previous session ended</summary>"}`
	lostNoteU = `{"type":"user","timestamp":"2026-10-08T10:31:30Z","origin":{"kind":"task-notification"},"message":{"content":"<task-notification>\n<task-id>a9ca</task-id>\n<status>stopped</status>\n<summary>Background agent \"x implement\" didn't finish before the previous session ended</summary>"}}`
	lostOldNt = `{"type":"queue-operation","timestamp":"2026-10-08T09:30:00Z","content":"<task-notification>\n<task-id>aearly</task-id>\n<status>stopped</status>\n<summary>Background agent \"y\" didn't finish before the previous session ended</summary>"}`
	prevRun   = `{"type":"user","timestamp":"2026-10-08T10:10:00Z","toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"aprev"}}`
	prevDone  = `{"type":"user","timestamp":"2026-10-08T10:12:00Z","toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"adone"}}`
	prevDoneN = `{"type":"queue-operation","timestamp":"2026-10-08T10:20:00Z","content":"<task-notification>\n<task-id>adone</task-id>\n<status>completed</status>"}`
)

func TestLostBackground(t *testing.T) {
	prevFrom := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)   // the previous cmux run's first event
	liveStart := time.Date(2026, 10, 8, 10, 31, 0, 0, time.UTC) // the live (resumed) process's start
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"stopped note written at the resume (both lines)", []string{lostNoteQ, lostNoteU}, "a9ca"},
		{"launched in the previous run, never ended", []string{prevRun}, "aprev"},
		{"launched in the previous run, ended", []string{prevDone, prevDoneN}, ""},
		{"launched before the previous run", []string{bgOld}, ""},
		{"launched by the live process: pending, not lost", []string{bgAgent}, ""},
		{"stopped note of an earlier resume", []string{lostOldNt}, ""},
		{"both kinds, sorted", []string{prevRun, lostNoteQ}, "a9ca,aprev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := LostBackground(transcript(t, c.lines...), prevFrom, liveStart)
			if err != nil || strings.Join(got, ",") != c.want {
				t.Fatalf("LostBackground = %v, %v; want %q", got, err, c.want)
			}
		})
	}
	if got, err := LostBackground(filepath.Join(t.TempDir(), "missing.jsonl"), prevFrom, liveStart); err != nil || len(got) != 0 {
		t.Fatalf("missing transcript = %v, %v; want none, no error", got, err)
	}
	if _, err := LostBackground(t.TempDir(), prevFrom, liveStart); err == nil {
		t.Fatal("an unreadable transcript is an error, never \"nothing lost\"")
	}
}

func TestLostBackgroundShown(t *testing.T) {
	b, err := os.ReadFile("testdata/screen-bg-lost.txt")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		screen string
		want   bool
	}{
		"agent (measured 08/10)":        {string(b), true},
		"shell command (reconstructed)": {"⏺ Background shell command \"npm test\" didn’t finish before the previous session ended\n", true},
		"agent still running":           {"✻ Waiting for 1 background agent\n", false},
		"empty":                         {"", false},
	}
	for name, c := range cases {
		if got := LostBackgroundShown(c.screen); got != c.want {
			t.Errorf("%s: LostBackgroundShown = %v, want %v", name, got, c.want)
		}
	}
}

package claudesession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func transcript(t *testing.T, lines ...string) string {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	tUser    = `{"type":"user","timestamp":"2026-10-08T09:30:00Z","message":{"role":"user","content":"fais X"}}`
	tToolUse = `{"type":"assistant","timestamp":"2026-10-08T09:30:01Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use"}]}}`
	tResult  = `{"type":"user","timestamp":"2026-10-08T09:30:02Z","message":{"content":[{"type":"tool_result"}]}}`
	tEnd     = `{"type":"assistant","timestamp":"2026-10-08T09:30:03Z","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"fini"}]}}`
	tDur     = `{"type":"system","subtype":"turn_duration","timestamp":"2026-10-08T09:30:04Z"}`
	tMeta    = `{"type":"user","isMeta":true,"timestamp":"2026-10-08T09:35:00Z","message":{"content":"<local-command-caveat>x"}}`
	tCmd     = `{"type":"user","timestamp":"2026-10-08T09:35:01Z","message":{"content":"<command-name>/exit</command-name>"}}`
	tIntr    = `{"type":"user","timestamp":"2026-10-08T09:30:05Z","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`
)

func TestTurnCut(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		cut   bool
		why   string
	}{
		{"finished turn", []string{tUser, tToolUse, tResult, tEnd, tDur}, false, ""},
		{"finished, then /exit and meta lines", []string{tUser, tEnd, tDur, tMeta, tCmd}, false, ""},
		{"tool_result without an answer", []string{tUser, tToolUse, tResult}, true, "tool_result"},
		{"tool_use without its result", []string{tUser, tToolUse}, true, "tool_use"},
		{"prompt without an answer", []string{tEnd, tDur, tUser}, true, "prompt"},
		{"interrupted by the user", []string{tUser, tToolUse, tIntr}, false, ""},
		{"turn_duration after a tool_result (hook-only end)", []string{tUser, tResult, tDur}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := TurnCut(transcript(t, c.lines...))
			if ok != c.cut || (c.cut && !strings.Contains(got.Why, c.why)) {
				t.Fatalf("TurnCut = %+v, %v; want cut=%v why~%q", got, ok, c.cut, c.why)
			}
			if c.cut && got.At.IsZero() {
				t.Fatal("a cut carries the time of its last entry")
			}
		})
	}
	if _, ok := TurnCut(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Fatal("missing transcript is not a cut")
	}
}

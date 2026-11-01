package claudesession

import (
	"encoding/json"
	"strings"
	"time"
)

// Cut is a turn a transcript left without its answer.
type Cut struct {
	At  time.Time // timestamp of the last user/assistant entry
	Why string
}

// TurnCut reports whether the transcript ends mid-turn (F6): its last
// user/assistant entry — meta entries, slash-command echoes and local command
// output skipped — is not an end_turn answer, and no system turn_duration
// follows it. A user interruption ends a turn. Missing or unreadable: false.
func TurnCut(path string) (Cut, bool) {
	lines, ok := transcriptTail(path)
	if !ok {
		return Cut{}, false
	}
	type entry struct {
		Type      string    `json:"type"`
		Subtype   string    `json:"subtype"`
		IsMeta    bool      `json:"isMeta"`
		Sidechain bool      `json:"isSidechain"`
		Timestamp time.Time `json:"timestamp"`
		Message   struct {
			StopReason string          `json:"stop_reason"`
			Content    json.RawMessage `json:"content"`
		} `json:"message"`
	}
	var last *entry
	ended := false
	for _, l := range lines {
		var e entry
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		if e.Type == "system" && e.Subtype == "turn_duration" {
			ended = true
			continue
		}
		if (e.Type != "user" && e.Type != "assistant") || e.IsMeta || e.Sidechain {
			continue
		}
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil && strings.HasPrefix(strings.TrimSpace(s), "<") {
			continue // <command-name>, <local-command-stdout>: echoes, not a turn
		}
		cp := e
		last, ended = &cp, false
	}
	if last == nil || ended {
		return Cut{}, false
	}
	if last.Type == "assistant" {
		if last.Message.StopReason == "end_turn" || last.Message.StopReason == "stop_sequence" {
			return Cut{}, false
		}
		return Cut{At: last.Timestamp, Why: "assistant stopped on " + orNone(last.Message.StopReason) + " (tool_use without its result)"}, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(last.Message.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && strings.HasPrefix(b.Text, "[Request interrupted by user") {
				return Cut{}, false
			}
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return Cut{At: last.Timestamp, Why: "tool_result without an answer"}, true
			}
		}
	}
	return Cut{At: last.Timestamp, Why: "prompt without an answer"}, true
}

func orNone(s string) string {
	if s == "" {
		return "no stop_reason"
	}
	return s
}

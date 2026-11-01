package claudesession

import (
	"bytes"
	"encoding/json"
	"time"
)

// turnEntry is the part of a transcript line TurnOpenedAfter reads.
type turnEntry struct {
	Type        string                 `json:"type"`
	Subtype     string                 `json:"subtype"`
	IsSidechain bool                   `json:"isSidechain"`
	Timestamp   string                 `json:"timestamp"`
	Origin      *struct{ Kind string } `json:"origin"`
}

// TurnOpenedAfter reports whether the transcript at path holds an entry that
// opens a model turn, stamped strictly after since, read from byte offset
// from like PromptsAfter (same window, same cap), and the offset just past
// the last complete line read.
//
// A turn opens (measured 2026-10-27, 1,655 turns on 145 local transcripts)
// with a "user" entry carrying an origin of any kind (human, peer,
// task-notification, auto-continuation; isMeta included), or, for a /loop or
// ScheduleWakeup wake, a "system" entry of subtype scheduled_task_fire. Tool
// results (no origin), queue operations, sidechain entries and queued_command
// attachments (always written mid-turn) never open one: a background
// notification that runs no turn writes only a queue enqueue and remove.
//
// covered tells whether the window reaches since: true when it starts at byte
// 0 or continues a previous read; otherwise (a tail read, a capped one) only
// when its first line carrying a top-level timestamp is not after since,
// since an opening entry may lie before the window. About a third of the
// lines (last-prompt, mode, permission-mode, …) carry no timestamp and are
// skipped for that judgement. ran false with covered false is unknown.
func TurnOpenedAfter(path string, from int64, since time.Time) (ran, covered bool, next int64, err error) {
	w, err := readWindow(path, from)
	if err != nil {
		return false, false, from, err
	}
	covered = w.whole || w.resumed
	judged := covered
	for _, line := range w.lines {
		stampOnly := !judged
		maybe := bytes.Contains(line, []byte(`"origin":`)) || bytes.Contains(line, []byte("scheduled_task_fire"))
		if !maybe && !stampOnly {
			continue
		}
		var e turnEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		at, perr := time.Parse(time.RFC3339Nano, e.Timestamp)
		if perr != nil {
			continue // unstamped: no verdict on either question
		}
		if !judged {
			judged, covered = true, !at.After(since)
		}
		if !maybe || !at.After(since) || e.IsSidechain {
			continue
		}
		if (e.Type == "user" && e.Origin != nil) || (e.Type == "system" && e.Subtype == "scheduled_task_fire") {
			return true, covered, w.next, nil
		}
	}
	return false, covered, w.next, nil
}

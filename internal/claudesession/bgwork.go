package claudesession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"time"
)

var taskIDRe = regexp.MustCompile(`<task-id>([^<\s]+)</task-id>`)

// PendingBackground lists the background tasks a transcript shows launched
// at or after since — the live process's start — and not ended (F13): a
// Bash run_in_background (toolUseResult.backgroundTaskId) or an async
// subagent (toolUseResult.isAsync + agentId), ended by a line of any type
// carrying <task-notification> and its <task-id>, or by a hand-back whose
// origin.senderTaskId is the id. A task launched before since died with the
// previous process. The whole file is read (a launch may be far from the
// tail); only lines naming one of those markers are decoded. A missing
// transcript runs nothing; any other read error is returned, so the caller
// fails closed.
func PendingBackground(path string, since time.Time) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	type launch struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		IsAsync          bool   `json:"isAsync"`
		AgentID          string `json:"agentId"`
	}
	type entry struct {
		Timestamp time.Time       `json:"timestamp"`
		Result    json.RawMessage `json:"toolUseResult"` // an object, or a string on a tool error
		Origin    struct {
			SenderTaskID string `json:"senderTaskId"`
		} `json:"origin"`
	}
	var order []string
	launched, ended := map[string]bool{}, map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		l := sc.Bytes()
		if bytes.Contains(l, []byte("<task-notification>")) {
			for _, m := range taskIDRe.FindAllSubmatch(l, -1) {
				ended[string(m[1])] = true
			}
		}
		if !bytes.Contains(l, []byte(`"backgroundTaskId"`)) && !bytes.Contains(l, []byte(`"isAsync"`)) && !bytes.Contains(l, []byte(`"senderTaskId"`)) {
			continue
		}
		var e entry
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		if id := e.Origin.SenderTaskID; id != "" {
			ended[id] = true
		}
		var lr launch
		if len(e.Result) == 0 || json.Unmarshal(e.Result, &lr) != nil {
			continue
		}
		id := lr.BackgroundTaskID
		if id == "" && lr.IsAsync {
			id = lr.AgentID
		}
		if id == "" || e.Timestamp.Before(since) || launched[id] {
			continue
		}
		launched[id] = true
		order = append(order, id)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, id := range order {
		if !ended[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

var (
	lostNoteRe   = regexp.MustCompile(`didn't finish before the previous session ended`)
	lostScreenRe = regexp.MustCompile(`Background (agent|shell command)\b.*didn['’]t finish`)
)

// LostBackground lists the background tasks the relaunch killed (F17, D9):
// a "stopped … didn't finish before the previous session ended" notification
// the live process wrote at or after liveStart (measured 08/10, 2.1.293), or
// a launch of the previous cmux run — in [prevFrom, liveStart) — that never
// ended (a claude that writes no such note). Sorted, deduplicated. Missing
// transcript: none; any other read error is returned (the caller says it
// could not tell, never "nothing lost").
func LostBackground(path string, prevFrom, liveStart time.Time) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	type launch struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		IsAsync          bool   `json:"isAsync"`
		AgentID          string `json:"agentId"`
	}
	type entry struct {
		Timestamp time.Time       `json:"timestamp"`
		Result    json.RawMessage `json:"toolUseResult"`
		Origin    struct {
			SenderTaskID string `json:"senderTaskId"`
		} `json:"origin"`
	}
	lost, launched, ended := map[string]bool{}, map[string]bool{}, map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		l := sc.Bytes()
		var e entry
		if bytes.Contains(l, []byte("<task-notification>")) {
			stopped := lostNoteRe.Match(l)
			if stopped && (json.Unmarshal(l, &e) != nil || e.Timestamp.Before(liveStart)) {
				stopped = false // an earlier resume's note, or unreadable
			}
			for _, m := range taskIDRe.FindAllSubmatch(l, -1) {
				ended[string(m[1])] = true
				if stopped {
					lost[string(m[1])] = true
				}
			}
		}
		if !bytes.Contains(l, []byte(`"backgroundTaskId"`)) && !bytes.Contains(l, []byte(`"isAsync"`)) && !bytes.Contains(l, []byte(`"senderTaskId"`)) {
			continue
		}
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		if id := e.Origin.SenderTaskID; id != "" {
			ended[id] = true
		}
		var lr launch
		if len(e.Result) == 0 || json.Unmarshal(e.Result, &lr) != nil {
			continue
		}
		id := lr.BackgroundTaskID
		if id == "" && lr.IsAsync {
			id = lr.AgentID
		}
		if id != "" && !e.Timestamp.Before(prevFrom) && e.Timestamp.Before(liveStart) {
			launched[id] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for id := range launched {
		if !ended[id] {
			lost[id] = true
		}
	}
	out := make([]string, 0, len(lost))
	for id := range lost {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// LostBackgroundShown: the screen carries claude's line for a background
// task the relaunch killed (F17) — "Background agent … didn't finish"
// (measured), "Background shell command … didn't finish" (reconstructed).
// Report only (D9): anywhere on the screen.
func LostBackgroundShown(screen string) bool {
	return lostScreenRe.MatchString(screen)
}

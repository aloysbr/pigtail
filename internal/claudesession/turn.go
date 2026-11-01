package claudesession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// Turn is the turn a transcript ends on: the promptId Claude Code stamps on
// the prompt entry and every tool_result of that turn (D12).
type Turn struct {
	ID         string    // the last user entry's promptId
	Start      time.Time // timestamp of the first tail entry with that promptId
	Entrypoint string    // that entry's entrypoint: cli, claude-desktop, sdk-cli, sdk-ts, …
}

// lastTurnScanMax bounds how far from the end ReadLastTurn looks for the
// turn's prompt entry (a var for tests). The lines a turn writes after its
// prompt (api-request-blob, a compaction summary) can outweigh the 256 KiB
// tail (P17, verify N2 of PR #210: ~384 KB on a first turn).
var lastTurnScanMax int64 = 64 << 20

// Why ReadLastTurn found no turn.
var (
	ErrNoTranscript   = errors.New("transcript introuvable")
	ErrNoTurn         = errors.New("aucune entrée user à promptId au transcript")
	ErrTurnBeyondScan = errors.New("aucune entrée user à promptId dans la fin lue du transcript")
)

// LastTurn is ReadLastTurn without its reason.
func LastTurn(path string) (Turn, bool) {
	t, err := ReadLastTurn(path)
	return t, err == nil
}

// ReadLastTurn returns the turn the transcript at path ends on. It reads the
// file backwards by blocks, from the same tail as LastAPIError, doubling the
// window until a user entry carries a promptId, up to lastTurnScanMax. A
// transcript whose user entries carry no promptId has no turn, never a
// guess; each failure is named (ErrNoTranscript, ErrNoTurn,
// ErrTurnBeyondScan, or the read error). A turn longer than the window read
// keeps a slightly late Start.
func ReadLastTurn(path string) (Turn, error) {
	if path == "" {
		return Turn{}, ErrNoTranscript
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Turn{}, ErrNoTranscript
	}
	if err != nil {
		return Turn{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Turn{}, err
	}
	size := st.Size()
	var buf []byte
	off, window := size, min(int64(apiErrorTail), lastTurnScanMax)
	for {
		from := size - window
		if from < 0 {
			from = 0
		}
		block := make([]byte, off-from)
		if _, err := f.ReadAt(block, from); err != nil && !errors.Is(err, io.EOF) {
			return Turn{}, err
		}
		buf, off = append(block, buf...), from
		if t, ok := lastTurnIn(bytes.Split(buf, []byte("\n"))); ok {
			return t, nil
		}
		switch {
		case off == 0:
			return Turn{}, ErrNoTurn
		case window >= lastTurnScanMax:
			return Turn{}, fmt.Errorf("%w (%d KiB lus sur %d)", ErrTurnBeyondScan, window>>10, size>>10)
		}
		window *= 2
		if window > lastTurnScanMax {
			window = lastTurnScanMax
		}
	}
}

// lastTurnIn: the turn the lines end on — the last user entry's promptId,
// started at its first entry among the lines.
func lastTurnIn(lines [][]byte) (Turn, bool) {
	type entry struct {
		Type       string    `json:"type"`
		PromptID   string    `json:"promptId"`
		Entrypoint string    `json:"entrypoint"`
		Timestamp  time.Time `json:"timestamp"`
	}
	var users []entry
	for _, l := range lines {
		if !bytes.Contains(l, []byte(`"promptId"`)) {
			continue
		}
		var e entry
		if json.Unmarshal(l, &e) != nil || e.Type != "user" || e.PromptID == "" {
			continue // a line cut by the window, or not a user entry
		}
		users = append(users, e)
	}
	if len(users) == 0 {
		return Turn{}, false
	}
	id := users[len(users)-1].PromptID
	for _, e := range users {
		if e.PromptID == id {
			return Turn{ID: id, Start: e.Timestamp, Entrypoint: e.Entrypoint}, true
		}
	}
	return Turn{}, false // unreachable: the last entry has that id
}

// PreviousTurnID returns the promptId of the turn before the last one: while
// a turn runs, the last turn that ended — that is, whose Stop hooks ran, which
// Claude Code marks with a stop_hook_summary entry after the turn's lines. A
// promptId with no such mark ended without a Stop and is not a turn here: a
// local command (/compact, whose command, summary and stdout entries carry a
// promptId of their own; /permissions, /mcp, /exit…), a prompt a
// UserPromptSubmit hook blocked (/pb, /open), an interrupted turn, a usage
// limit (D-B2, verify N2 of PR #210). This is the turn the Stop hook's
// bgwork record names. Unlike LastTurn it reads the whole file (only lines
// naming "promptId" or "stop_hook_summary" are decoded), since the turn in
// progress may alone outgrow the tail. false: no such turn, or the file is
// missing or unreadable.
func PreviousTurnID(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	type entry struct {
		Type     string `json:"type"`
		Subtype  string `json:"subtype"`
		PromptID string `json:"promptId"`
	}
	// cur: the promptId of the latest user entries; stopped: a Stop ran
	// since cur began; ended: the last promptId before cur that saw a Stop.
	var cur, ended string
	var stopped bool
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		l := sc.Bytes()
		if !bytes.Contains(l, []byte(`"promptId"`)) && !bytes.Contains(l, []byte(`"stop_hook_summary"`)) {
			continue
		}
		var e entry
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		switch {
		case e.Type == "system" && e.Subtype == "stop_hook_summary":
			stopped = cur != ""
		case e.Type == "user" && e.PromptID != "" && e.PromptID != cur:
			if stopped {
				ended = cur
			}
			cur, stopped = e.PromptID, false
		}
	}
	if sc.Err() != nil || ended == "" {
		return "", false
	}
	return ended, true
}

// TurnStartOf returns the timestamp of the first user entry carrying promptID,
// read on the whole file (like PreviousTurnID: only lines naming "promptId"
// are decoded), so a long turn keeps its true start where ReadLastTurn's
// tail window would give a late one. false: not found or unreadable.
func TurnStartOf(path, promptID string) (time.Time, bool) {
	if path == "" || promptID == "" {
		return time.Time{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = f.Close() }()
	type entry struct {
		Type      string    `json:"type"`
		PromptID  string    `json:"promptId"`
		Timestamp time.Time `json:"timestamp"`
	}
	needle := []byte(`"` + promptID + `"`)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		l := sc.Bytes()
		if !bytes.Contains(l, []byte(`"promptId"`)) || !bytes.Contains(l, needle) {
			continue
		}
		var e entry
		if json.Unmarshal(l, &e) != nil || e.Type != "user" || e.PromptID != promptID {
			continue
		}
		return e.Timestamp, true
	}
	return time.Time{}, false
}

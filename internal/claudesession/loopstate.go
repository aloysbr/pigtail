package claudesession

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// LoopState is what a transcript says about a role's loop (role supervision):
// the last armed ScheduleWakeup, the scheduled-task fires, the last turn.
type LoopState struct {
	ArmAt, ScheduledFor time.Time // newest ScheduleWakeup result line (its timestamp) and its toolUseResult.scheduledFor; zero = none seen
	LoopFireAt          time.Time // newest scheduled_task_fire with taskKind "loop" AFTER ArmAt (zero = none)
	FireAt              time.Time // newest scheduled_task_fire of any kind
	TurnAt              time.Time // newest line of type user or assistant
	Capped              bool      // promptReadMax reached before an arm was found
}

const loopChunk = 256 << 10

// Test seams: lines JSON-parsed and bytes read by the backward scan.
var (
	loopParses    atomic.Int64
	loopBytesRead atomic.Int64
)

type loopMemo struct {
	size  int64
	mtime time.Time
	ino   uint64
	state LoopState
}

// LoopReader reads LoopState from transcripts, memoising per path on
// (size, mtime, inode): an unchanged file is never read again.
type LoopReader struct {
	mu   sync.Mutex
	memo map[string]loopMemo
}

// NewLoopReader returns an empty reader.
func NewLoopReader() *LoopReader { return &LoopReader{memo: map[string]loopMemo{}} }

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// Read returns the loop state of the transcript at path (ErrNoTranscript when
// there is none). A changed file is scanned backwards, whole, up to the
// newest arm or promptReadMax; there is no incremental path.
func (r *LoopReader) Read(path string) (LoopState, error) {
	if path == "" {
		return LoopState{}, ErrNoTranscript
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoopState{}, ErrNoTranscript
		}
		return LoopState{}, err
	}
	key := loopMemo{size: fi.Size(), mtime: fi.ModTime(), ino: inodeOf(fi)}
	r.mu.Lock()
	m, ok := r.memo[path]
	r.mu.Unlock()
	if ok && m.size == key.size && m.mtime.Equal(key.mtime) && m.ino == key.ino {
		return m.state, nil
	}
	st, err := scanLoopBackward(path, key.size)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoopState{}, ErrNoTranscript
		}
		return LoopState{}, err
	}
	key.state = st
	r.mu.Lock()
	r.memo[path] = key
	r.mu.Unlock()
	return st, nil
}

var (
	candArm  = []byte("scheduledFor")
	candFire = []byte("scheduled_task_fire")
	candUser = []byte(`"type":"user"`)
	candAsst = []byte(`"type":"assistant"`)
)

type loopLine struct {
	Type          string `json:"type"`
	Subtype       string `json:"subtype"`
	TaskKind      string `json:"taskKind"`
	Timestamp     string `json:"timestamp"`
	ToolUseResult *struct {
		ScheduledFor *int64 `json:"scheduledFor"`
	} `json:"toolUseResult"`
}

func parseLoopLine(b []byte) (loopLine, time.Time, bool) {
	loopParses.Add(1)
	var l loopLine
	if json.Unmarshal(b, &l) != nil {
		return l, time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		return l, time.Time{}, false
	}
	return l, ts, true
}

// scanLoopBackward reads whole lines from the end of the file towards the
// start; a partial last line (being written) is ignored.
func scanLoopBackward(path string, size int64) (LoopState, error) {
	var st LoopState
	f, err := os.Open(path)
	if err != nil {
		return st, err
	}
	defer func() { _ = f.Close() }()
	var (
		pos     = size
		carry   []byte // head of an incomplete line, awaiting its start
		first   = true
		scanned int64
		turnSet bool
		armed   bool
	)
	handle := func(line []byte) (stop bool) {
		if len(line) == 0 {
			return false
		}
		isArm := bytes.Contains(line, candArm)
		isFire := bytes.Contains(line, candFire)
		wantTurn := !turnSet && (bytes.Contains(line, candUser) || bytes.Contains(line, candAsst))
		if !isArm && !isFire && !wantTurn {
			return false
		}
		l, ts, ok := parseLoopLine(line)
		if !ok {
			return false
		}
		if wantTurn && (l.Type == "user" || l.Type == "assistant") {
			st.TurnAt, turnSet = ts, true
		}
		if isFire && l.Type == "system" && l.Subtype == "scheduled_task_fire" {
			if st.FireAt.IsZero() {
				st.FireAt = ts
			}
			if l.TaskKind == "loop" && st.LoopFireAt.IsZero() {
				st.LoopFireAt = ts
			}
		}
		if isArm && l.ToolUseResult != nil && l.ToolUseResult.ScheduledFor != nil {
			st.ArmAt = ts
			st.ScheduledFor = time.UnixMilli(*l.ToolUseResult.ScheduledFor).UTC()
			armed = true
			return true
		}
		return false
	}
	for pos > 0 {
		n := int64(loopChunk)
		if n > pos {
			n = pos
		}
		pos -= n
		buf := make([]byte, n, n+int64(len(carry)))
		rn, err := f.ReadAt(buf, pos)
		if err != nil && !errors.Is(err, io.EOF) {
			return st, err
		}
		buf = append(buf[:rn], carry...)
		loopBytesRead.Add(int64(rn))
		scanned += int64(rn)
		if first {
			first = false
			// Drop a partial last line: everything after the final newline.
			i := bytes.LastIndexByte(buf, '\n')
			if i < 0 {
				buf = nil
			} else {
				buf = buf[:i+1]
			}
		}
		// Lines are '\n'-terminated; the first segment may lack its start.
		body := bytes.TrimSuffix(buf, []byte("\n"))
		parts := bytes.Split(body, []byte("\n"))
		start := 0
		if pos > 0 {
			carry = append([]byte(nil), parts[0]...)
			start = 1
		} else {
			carry = nil
		}
		for i := len(parts) - 1; i >= start; i-- {
			if handle(parts[i]) {
				goto done
			}
		}
		if scanned >= promptReadMax && pos > 0 {
			st.Capped = true
			goto done
		}
	}
done:
	if !armed {
		st.LoopFireAt = time.Time{}
	}
	return st, nil
}

// ActivityMtime is the last write to a session: max(mtime of the transcript,
// newest file under <transcript minus .jsonl>/subagents/), so a foreground
// subagent writing only its own file is not mistaken for a hung turn.
func ActivityMtime(path string) (time.Time, error) {
	if path == "" {
		return time.Time{}, ErrNoTranscript
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, ErrNoTranscript
		}
		return time.Time{}, err
	}
	m := fi.ModTime()
	dir := strings.TrimSuffix(path, ".jsonl") + "/subagents"
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return time.Time{}, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return time.Time{}, err
		}
		if info.ModTime().After(m) {
			m = info.ModTime()
		}
	}
	return m, nil
}

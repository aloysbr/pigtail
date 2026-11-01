package staffing

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	logFileName = "staffing-decisions.jsonl"
	// LogMaxBytes is the size past which the daemon rotates the journal on
	// startup.
	LogMaxBytes = 20 << 20
)

// errEmptyLog is ReplaceLast on a journal with no line.
var errEmptyLog = errors.New("staffing journal: no line to replace")

// LogPath returns where the decision journal lives for a daemon whose
// database is at dbPath; "" (no journal) when dbPath is empty.
func LogPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), logFileName)
}

// Log is the decision journal: one JSON line per StaffingDecision, written
// under a mutex (the admission.History pattern). A nil *Log writes nothing.
type Log struct {
	path string
	mu   sync.Mutex
}

// OpenLog returns the journal at path; nil when path is empty.
func OpenLog(path string) *Log {
	if path == "" {
		return nil
	}
	return &Log{path: path}
}

// Rotate renames the file to <path>.1 when it has grown past maxBytes.
// Meant for startup.
func (l *Log) Rotate(maxBytes int64) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fi, err := os.Stat(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Size() <= maxBytes {
		return nil
	}
	return os.Rename(l.path, l.path+".1")
}

// Append writes one decision as a new line.
func (l *Log) Append(d pigtail.StaffingDecision) error {
	if l == nil {
		return nil
	}
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReplaceLast rewrites the last line of the live file with d (truncate to its
// start, then write). A missing or empty file is an error.
func (l *Log) ReplaceLast(d pigtail.StaffingDecision) error {
	if l == nil {
		return nil
	}
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	body := bytes.TrimRight(data, "\n")
	if len(body) == 0 {
		return errEmptyLog
	}
	start := bytes.LastIndexByte(body, '\n') + 1 // 0 when the file holds one line
	f, err := os.OpenFile(l.path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(start)); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteAt(line, int64(start)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Read returns the decisions whose last occurrence (LastAt, else TS) is at or
// after since, oldest first, from the rotated file then the live one; limit >
// 0 keeps the last limit. A line that does not decode is skipped.
func (l *Log) Read(since time.Time, limit int) ([]pigtail.StaffingDecision, error) {
	out := []pigtail.StaffingDecision{}
	if l == nil {
		return out, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range []string{l.path + ".1", l.path} {
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var d pigtail.StaffingDecision
			if json.Unmarshal(sc.Bytes(), &d) != nil {
				continue
			}
			at := d.TS
			if d.LastAt.After(at) {
				at = d.LastAt
			}
			if at.Before(since) {
				continue
			}
			out = append(out, d)
		}
		err = sc.Err()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

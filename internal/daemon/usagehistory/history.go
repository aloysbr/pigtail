// Package usagehistory keeps the daemon's history of Claude usage readings:
// one JSON line per account and sweep pass, appended to a file next to the
// usage refresh state, and the last Keep of it in memory for the rhythm and
// the verdict of GET /usage/overview. Patterned on the admission history
// (internal/daemon/admission/history.go): O_APPEND per write, rotation at
// startup, unreadable lines skipped.
package usagehistory

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeusage"
)

const (
	// FileName sits in the usage state dir, next to usage-refresh.json.
	FileName = "usage-history.jsonl"
	// MaxBytes is the size past which Open rotates the file to <path>.1,
	// the same as the admission history (admission.HistoryMaxBytes).
	MaxBytes = 20 << 20
	// Keep is how much of the history is held in memory.
	Keep = 72 * time.Hour
)

// History is the usage history. A nil *History is valid: it records nothing
// and returns no sample.
type History struct {
	path   string
	mu     sync.Mutex
	recent []claudeusage.Sample // write order, At >= last At - Keep
}

// Open loads the history at path: rotates the file when it has grown past
// MaxBytes, then reads <path>.1 and <path>, keeping the samples with
// At >= now-Keep. A line that does not decode is skipped. "" → nil, nil.
func Open(path string, now time.Time) (*History, error) {
	if path == "" {
		return nil, nil
	}
	h := &History{path: path, recent: []claudeusage.Sample{}}
	if fi, err := os.Stat(path); err == nil && fi.Size() > MaxBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	since := now.Add(-Keep)
	for _, p := range []string{path + ".1", path} {
		if err := h.load(p, since); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// load appends the samples of one file with At >= since. Read by line of any
// length (a garbage line past a scanner's buffer must not end the read).
func (h *History) load(p string, since time.Time) error {
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var s claudeusage.Sample
			if json.Unmarshal(line, &s) == nil && !s.At.IsZero() && !s.At.Before(since) {
				h.recent = append(h.recent, s)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Append writes the samples, one line each, then adds them to memory, pruned
// to Keep before the latest At. The file (and its dir, 0700) is opened per
// write, O_APPEND, 0600:
// a file deleted or rotated under the daemon is simply recreated.
func (h *History) Append(ss []claudeusage.Sample) error {
	if h == nil || len(ss) == 0 {
		return nil
	}
	var buf []byte
	for _, s := range ss {
		line, err := json.Marshal(s)
		if err != nil {
			return err
		}
		buf = append(append(buf, line...), '\n')
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	h.recent = append(h.recent, ss...)
	last := h.recent[0].At
	for _, s := range h.recent {
		if s.At.After(last) {
			last = s.At
		}
	}
	cut := last.Add(-Keep)
	kept := h.recent[:0]
	for _, s := range h.recent {
		if !s.At.Before(cut) {
			kept = append(kept, s)
		}
	}
	h.recent = kept
	return nil
}

// Since returns a copy of the samples in memory with At >= t, in write order;
// empty (never nil) on a nil History.
func (h *History) Since(t time.Time) []claudeusage.Sample {
	out := []claudeusage.Sample{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.recent {
		if !s.At.Before(t) {
			out = append(out, s)
		}
	}
	return out
}

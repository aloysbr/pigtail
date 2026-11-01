package sessbudget

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Sample is one tick of the sampler: the record appended to
// session-samples.jsonl and handed to Sampler.OnSample.
type Sample = pigtail.SessionSample

const (
	samplesFileName = "session-samples.jsonl"
	// SamplesMaxBytes is the size past which Append renames the live file to
	// <path>.1. At the 30 s default tick, with ~20 sessions, a day is some
	// 12 MiB: the live file plus .1 cover about five days.
	SamplesMaxBytes = 32 << 20
)

// SamplesPath is where the samples live: the state dir
// $XDG_STATE_HOME/pigtail (default ~/.local/state/pigtail), not next to the
// database — the samples are measurement, like the dev-server markers.
func SamplesPath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail", samplesFileName)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "pigtail", samplesFileName)
	}
	return filepath.Join(os.TempDir(), "pigtail-state", samplesFileName)
}

// History is the append-only JSONL log of samples. It is measurement, never
// state: a write that fails is the caller's to log and forget. A nil *History
// is valid and writes nothing.
type History struct {
	path     string
	maxBytes int64
	mu       sync.Mutex // one line at a time; also guards the rotation
}

// NewHistory returns a History appending to path ("" = none), rotating at
// SamplesMaxBytes.
func NewHistory(path string) *History {
	if path == "" {
		return nil
	}
	return &History{path: path, maxBytes: SamplesMaxBytes}
}

// Append writes one record. Before each write it stats the live file and, past
// the cap, renames it to <path>.1 (replacing the previous one): rotation
// happens mid-run, with the writer holding the mutex.
func (h *History) Append(rec Sample) error {
	if h == nil {
		return nil
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(h.path); err == nil && fi.Size() >= h.maxBytes {
		if err := os.Rename(h.path, h.path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Read returns the records taken at or after since, oldest first, from the
// rotated file and then the live one. A line that does not decode (a write
// cut short by a crash) is skipped.
func (h *History) Read(since time.Time) ([]Sample, error) {
	out := []Sample{}
	if h == nil {
		return out, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range []string{h.path + ".1", h.path} {
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			var rec Sample
			if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.TS.Before(since) {
				continue
			}
			out = append(out, rec)
		}
		err = sc.Err()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

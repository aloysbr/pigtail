package cpuslots

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

const (
	// historyFileName sits next to admission-history.jsonl.
	historyFileName = "cpu-slots-history.jsonl"
	// HistoryMaxBytes is the size past which the daemon rotates the history
	// on startup (the same cap as the admission history).
	HistoryMaxBytes = 20 << 20
)

// Outcomes of a CPUSlotRecord.
const (
	outcomeReleased  = pigtail.CPUSlotOutcomeReleased
	outcomeRevoked   = pigtail.CPUSlotOutcomeRevoked
	outcomeCancelled = pigtail.CPUSlotOutcomeCancelled
)

// HistoryPath returns where the CPU slot history lives for a daemon whose
// database is at dbPath; "" (no history) when dbPath is empty.
func HistoryPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), historyFileName)
}

// History is the append-only log of ended slot holds: one JSON line each,
// modelled on admission's. Measurement, never state: a write that fails is
// logged and forgotten.
type History struct {
	path string
	mu   sync.Mutex // one line at a time, never two interleaved
}

// NewHistory returns a History appending to path. A nil *History is valid and
// writes nothing.
func NewHistory(path string) *History {
	if path == "" {
		return nil
	}
	return &History{path: path}
}

// Rotate renames the file to <path>.1, replacing the previous one, when it
// has grown past maxBytes. Meant for startup.
func (h *History) Rotate(maxBytes int64) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	fi, err := os.Stat(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Size() <= maxBytes {
		return nil
	}
	return os.Rename(h.path, h.path+".1")
}

// Append writes one record (opened per line, O_APPEND: a file deleted or
// rotated under the daemon is simply recreated).
func (h *History) Append(rec pigtail.CPUSlotRecord) error {
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

// Read returns the records that ended at or after since, oldest first, from
// the rotated file and then the live one. A line that does not decode is
// skipped.
func (h *History) Read(since time.Time) ([]pigtail.CPUSlotRecord, error) {
	out := []pigtail.CPUSlotRecord{}
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
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var rec pigtail.CPUSlotRecord
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

// WithHistory attaches the history the arbiter appends to.
func WithHistory(h *History) Option { return func(a *Arbiter) { a.history = h } }

// History returns the recorded holds that ended at or after since; empty when
// no history is kept.
func (a *Arbiter) History(since time.Time) ([]pigtail.CPUSlotRecord, error) {
	return a.history.Read(since)
}

// appendHistory writes rec, logging a failure: the history must never be why
// a slot leaks. Caller does not hold mu.
func (a *Arbiter) appendHistory(rec pigtail.CPUSlotRecord) {
	if a.history == nil {
		return
	}
	if err := a.history.Append(rec); err != nil {
		a.logf("cpuslots: appending to the history: %v", err)
	}
}

// holdRecord is the history line of a hold ending at end.
func holdRecord(h *holder, outcome string, end time.Time) pigtail.CPUSlotRecord {
	return pigtail.CPUSlotRecord{
		TS: end.UTC(), Group: h.group, Label: h.label, EnqueuedAt: h.enqueuedAt.UTC(),
		WaitedMS: h.waited.Milliseconds(), HeldMS: end.Sub(h.since).Milliseconds(), Outcome: outcome,
	}
}

// cancelRecord is the history line of a waiter that left the queue at end.
func cancelRecord(w *waiter, end time.Time) pigtail.CPUSlotRecord {
	return pigtail.CPUSlotRecord{
		TS: end.UTC(), Group: w.group, Label: w.label, EnqueuedAt: w.since.UTC(),
		WaitedMS: end.Sub(w.since).Milliseconds(), Outcome: outcomeCancelled,
	}
}

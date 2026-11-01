package admission

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
	// historyFileName sits next to admission-costs.json.
	historyFileName = "admission-history.jsonl"
	// HistoryMaxBytes is the size past which the daemon rotates the history
	// on startup. At ~500 bytes a line that is some 40 000 commands — weeks
	// of a busy machine, which is the span the file exists to measure.
	HistoryMaxBytes = 20 << 20
	// reasonOther books a wait no gate claimed (see Ticket.accrue).
	reasonOther Reason = "other"
)

// HistoryPath returns where the admission history lives for a daemon whose
// database is at dbPath; "" (no history) when dbPath is empty.
func HistoryPath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dbPath), historyFileName)
}

// History is the append-only log of ended commands: one JSON line each,
// written when a lease is released or revoked, or a queued ticket cancelled.
// It is measurement, never state — nothing in the daemon reads it back to
// decide anything, so a write that fails is logged and forgotten.
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
// has grown past maxBytes. Meant for startup: rotating under a live writer
// would only race it for nothing.
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

// Append writes one record. Opened per line, O_APPEND, like the hooks log:
// a file deleted or rotated under the daemon is simply recreated.
func (h *History) Append(rec pigtail.AdmissionRecord) error {
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
// the rotated file and then the live one. A line that does not decode (a
// write cut short by a crash) is skipped rather than failing the whole read.
func (h *History) Read(since time.Time) ([]pigtail.AdmissionRecord, error) {
	out := []pigtail.AdmissionRecord{}
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
			var rec pigtail.AdmissionRecord
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

// appendHistory writes rec, logging a failure: the history must never be
// why a command waits or a lease leaks. Caller does not hold mu.
func (c *Controller) appendHistory(rec pigtail.AdmissionRecord) {
	if c.history == nil {
		return
	}
	if err := c.history.Append(rec); err != nil {
		c.logf("admission: appending to the history: %v", err)
	}
}

// History returns the recorded commands that ended at or after since; empty
// when no history is kept.
func (c *Controller) History(since time.Time) ([]pigtail.AdmissionRecord, error) {
	return c.history.Read(since)
}

// leaseRecord is the history line of a lease ending at end.
func leaseRecord(l lease, outcome string, end time.Time) pigtail.AdmissionRecord {
	var exit *int
	if outcome == pigtail.AdmissionOutcomeReleased {
		exit = l.exit // a revoked lease's command is still running: no exit yet
	}
	return pigtail.AdmissionRecord{
		TS: end.UTC(), Group: l.req.Group, Repo: l.req.Repo, Class: l.req.Class, Label: l.req.Label,
		Outcome:    outcome,
		ExitCode:   exit,
		Focus:      l.req.Focus,
		EnqueuedAt: l.enqueuedAt.UTC(), WaitedMS: l.waited.Milliseconds(),
		WaitByReasonMS: waitByMS(l.waitBy),
		HeldMS:         end.Sub(l.since).Milliseconds(),
		PeakBytes:      l.peak, CostBytes: l.cost, AvailAtEnqueue: l.availAtEnq,
		MaxPressure: l.maxPressure, SwapoutBytesDuring: l.swapDuring, Degraded: l.starved,
		CompressBytesDuring: l.compDuring, MaxCompressRate: uint64(l.maxCompRate),
		AdmitCompressRate: uint64(l.admitCompRate), AdmitCompressCalmRate: uint64(l.admitCompCalmRate),
	}
}

// ticketRecord is the history line of a ticket withdrawn from the queue at
// end. Caller holds mu (the ticket's wait is still being accrued).
func ticketRecord(t *Ticket, end time.Time) pigtail.AdmissionRecord {
	return pigtail.AdmissionRecord{
		TS: end.UTC(), Group: t.req.Group, Repo: t.req.Repo, Class: t.req.Class, Label: t.req.Label,
		Outcome:    pigtail.AdmissionOutcomeCancelled,
		Focus:      t.req.Focus,
		EnqueuedAt: t.since.UTC(), WaitedMS: end.Sub(t.since).Milliseconds(),
		WaitByReasonMS: waitByMS(t.waitBy),
		CostBytes:      t.cost, AvailAtEnqueue: t.availAtEnq,
	}
}

func waitByMS(m map[Reason]time.Duration) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for r, d := range m {
		out[string(r)] = d.Milliseconds()
	}
	return out
}

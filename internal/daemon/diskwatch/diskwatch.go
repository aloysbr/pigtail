// Package diskwatch tells when the disk runs low. A full disk stops every
// session on the machine at once (ENOSPC, 2026-10-31), and nothing else in
// pigtail looks at it before admission refuses to start a heavy command.
//
// The Watcher reads the free space once a minute. Under the alert threshold
// it writes one disk-low journal line and publishes disk.low; once the space
// is back above the threshold plus RearmMargin it writes disk-ok and
// publishes disk.ok. The gap between the two levels is the hysteresis: a
// disk hovering around the threshold must not flap. State is derived from
// the journal at start (OpenAlert), so a restart under the threshold neither
// re-announces nor forgets the open alert, and GET /alerts reads the same
// function: the alert is open while the last disk-low has no disk-ok after it.
//
// The alert is machine-wide: its journal lines are written under the master
// scope and name no subject, session or scope in their text.
package diskwatch

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	// KindLow and KindOK are the journal kinds of the two crossings.
	KindLow = "disk-low"
	KindOK  = "disk-ok"
	// DefaultInterval paces the reads.
	DefaultInterval = time.Minute
	// RearmMargin is how far above the alert threshold the space must climb
	// before the alert closes.
	RearmMargin = 10 << 30
)

// Watcher is the periodic reader. Build it with New; the fields are seams.
type Watcher struct {
	// AlertBytes is the threshold under which the alert opens; 0 = the alert
	// is cut (it only closes one a previous run left open). FloorBytes is the
	// admission floor, carried in the payload for the notification.
	AlertBytes, FloorBytes uint64
	Interval               time.Duration

	Free    func() (uint64, error)
	Now     func() time.Time
	Journal func(pigtail.JournalEntry) error
	Publish func(pigtail.Event)
	// Open reports whether the journal holds an unanswered disk-low.
	Open func() (bool, error)
	Logf func(format string, args ...any)

	low bool
	// errLogged is set after a read failure was logged, cleared by a good read.
	errLogged bool
}

// New returns a Watcher with the real clock and logger; the caller sets Free,
// Journal, Publish and Open.
func New(alertBytes, floorBytes uint64) *Watcher {
	return &Watcher{
		AlertBytes: alertBytes, FloorBytes: floorBytes,
		Interval: DefaultInterval, Now: time.Now, Logf: log.Printf,
	}
}

// Start reads the alert state from the journal. With the alert cut and one
// still open, it closes it now: an alert must never stay open with no loop
// left to close it. A journal that cannot be read leaves the state closed.
func (w *Watcher) Start() {
	open, err := w.Open()
	if err != nil {
		w.Logf("disk watch: journal unreadable at start: %v", err)
		return
	}
	w.low = open
	if open && w.AlertBytes == 0 {
		w.close(0)
	}
}

// Run logs its start, runs Start, then ticks every Interval until ctx is
// done. With the alert cut there is nothing to tick.
func (w *Watcher) Run(ctx context.Context) {
	if w.AlertBytes == 0 {
		w.Logf("disk watch: disk alert cut (it only closes an alert left open by a previous run)")
	} else {
		w.Logf("disk watch: alert under %d GB, closes above %d GB, reads every %s",
			w.AlertBytes>>30, (w.AlertBytes+RearmMargin)>>30, w.Interval)
	}
	w.Start()
	if w.AlertBytes == 0 {
		return
	}
	// One reading right away: a disk already low at start alerts now, not a
	// minute later.
	w.Tick()
	if ctx.Err() != nil {
		return
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick()
		}
	}
}

// Tick reads the disk once and crosses a level if it moved. A failed read
// says nothing and keeps the state: not knowing is not an alert.
func (w *Watcher) Tick() {
	if w.AlertBytes == 0 {
		return
	}
	free, err := w.Free()
	if err != nil {
		if !w.errLogged {
			w.errLogged = true
			w.Logf("disk watch: reading the free space failed (logged once until a read succeeds): %v", err)
		}
		return
	}
	w.errLogged = false
	switch {
	case !w.low && free < w.AlertBytes:
		if w.write(KindLow, pigtail.EventDiskLow, free) {
			w.low = true
		}
	case w.low && free >= w.AlertBytes+RearmMargin:
		w.close(free)
	}
}

func (w *Watcher) close(free uint64) {
	if w.write(KindOK, pigtail.EventDiskOK, free) {
		w.low = false
	}
}

// write journals a crossing, then publishes it: an event without its line
// would announce what GET /alerts cannot show. A failed write leaves the
// state alone, so the next tick retries.
func (w *Watcher) write(kind string, typ pigtail.EventType, free uint64) bool {
	now := w.Now()
	e := pigtail.JournalEntry{
		ID:        fmt.Sprintf("%s:%d", kind, now.UnixNano()),
		Timestamp: now,
		Kind:      kind,
		Scope:     string(scope.Master()),
		Summary: fmt.Sprintf("free_bytes=%d threshold_bytes=%d floor_bytes=%d",
			free, w.AlertBytes, w.FloorBytes),
		Source: "proc:pigtaild",
	}
	if err := w.Journal(e); err != nil {
		w.Logf("disk watch: journal %s: %v", e.ID, err)
		return false
	}
	if w.Publish != nil {
		w.Publish(pigtail.Event{Type: typ, Data: map[string]interface{}{
			"free_bytes": free, "threshold_bytes": w.AlertBytes, "floor_bytes": w.FloorBytes,
		}})
	}
	return true
}

// OpenAlert returns the unanswered disk-low line, nil when the alert is
// closed (no disk-low, or a disk-ok after the last one). It reads the master
// scope whoever is asking: the alert belongs to the machine.
func OpenAlert(st *store.Store) (*pigtail.JournalEntry, error) {
	last := func(kind string) (*pigtail.JournalEntry, error) {
		es, err := st.ListJournal(store.JournalFilter{Scope: scope.Master(), Kind: kind, Limit: 1})
		if err != nil || len(es) == 0 {
			return nil, err
		}
		return &es[0], nil
	}
	low, err := last(KindLow)
	if err != nil || low == nil {
		return nil, err
	}
	ok, err := last(KindOK)
	if err != nil {
		return nil, err
	}
	if ok != nil && !low.Timestamp.After(ok.Timestamp) {
		return nil, nil
	}
	return low, nil
}

// FreeBytesOf reads free_bytes back from a disk-low summary.
func FreeBytesOf(summary string) (uint64, bool) { return fieldOf(summary, "free_bytes") }

// FloorBytesOf reads floor_bytes back from a disk-low summary.
func FloorBytesOf(summary string) (uint64, bool) { return fieldOf(summary, "floor_bytes") }

func fieldOf(summary, key string) (uint64, bool) {
	for _, f := range strings.Fields(summary) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			n, err := strconv.ParseUint(v, 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

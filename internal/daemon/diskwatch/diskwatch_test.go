package diskwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const gib = uint64(1) << 30

type rig struct {
	w       *Watcher
	free    uint64
	freeErr error
	now     time.Time
	lines   []pigtail.JournalEntry
	events  []pigtail.Event
	open    bool // what the journal says at startup
	jErr    error
}

func newRig(alertGB uint64) *rig {
	r := &rig{free: 100 * gib, now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	r.w = New(alertGB*gib, 20*gib)
	r.w.Free = func() (uint64, error) { return r.free, r.freeErr }
	r.w.Now = func() time.Time { r.now = r.now.Add(time.Minute); return r.now }
	r.w.Journal = func(e pigtail.JournalEntry) error {
		if r.jErr != nil {
			return r.jErr
		}
		r.lines = append(r.lines, e)
		return nil
	}
	r.w.Publish = func(e pigtail.Event) { r.events = append(r.events, e) }
	r.w.Open = func() (bool, error) { return r.open, nil }
	r.w.Logf = func(string, ...any) {}
	return r
}

func (r *rig) kinds() []string {
	var out []string
	for _, l := range r.lines {
		out = append(out, l.Kind)
	}
	return out
}

func eq(a []string, b ...string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestCrossingDownThenUp(t *testing.T) {
	r := newRig(40)
	r.w.Start()
	r.w.Tick()
	if len(r.lines) != 0 {
		t.Fatalf("above the threshold nothing is written: %v", r.kinds())
	}
	r.free = 31 * gib
	r.w.Tick()
	if !eq(r.kinds(), "disk-low") {
		t.Fatalf("kinds = %v, want disk-low", r.kinds())
	}
	l := r.lines[0]
	if l.Scope != string(scope.Master()) {
		t.Errorf("scope = %q, want the master scope", l.Scope)
	}
	if !strings.HasPrefix(l.ID, "disk-low:") || l.SubjectID != "" || strings.ContainsAny(l.Summary, "\r\n") {
		t.Errorf("line = %+v", l)
	}
	if err := l.Validate(); err != nil {
		t.Errorf("the line must be a valid journal entry: %v", err)
	}
	if len(r.events) != 1 || r.events[0].Type != pigtail.EventDiskLow {
		t.Fatalf("events = %+v", r.events)
	}
	d := r.events[0].Data
	if d["free_bytes"] != 31*gib || d["threshold_bytes"] != 40*gib || d["floor_bytes"] != 20*gib {
		t.Errorf("payload = %+v", d)
	}
	// Between the threshold and the re-arm level: still low, still silent.
	r.free = 45 * gib
	r.w.Tick()
	if len(r.lines) != 1 {
		t.Fatalf("hysteresis: no line between 40 and 50 GB, got %v", r.kinds())
	}
	r.free = 50 * gib
	r.w.Tick()
	if !eq(r.kinds(), "disk-low", "disk-ok") {
		t.Fatalf("kinds = %v, want disk-low, disk-ok", r.kinds())
	}
	if r.events[1].Type != pigtail.EventDiskOK {
		t.Fatalf("events = %+v", r.events)
	}
	if r.lines[0].ID == r.lines[1].ID {
		t.Fatal("ids must be unique per crossing (AddJournal keeps the first writer)")
	}
}

func TestNoDuplicateOnTwoLowTicks(t *testing.T) {
	r := newRig(40)
	r.w.Start()
	r.free = 10 * gib
	r.w.Tick()
	r.w.Tick()
	r.w.Tick()
	if !eq(r.kinds(), "disk-low") || len(r.events) != 1 {
		t.Fatalf("kinds = %v, events = %d: one crossing, one line", r.kinds(), len(r.events))
	}
}

func TestRestartBelowTheThresholdWritesNothing(t *testing.T) {
	r := newRig(40)
	r.open = true // the journal already holds an unanswered disk-low
	r.w.Start()
	r.free = 10 * gib
	r.w.Tick()
	if len(r.lines) != 0 || len(r.events) != 0 {
		t.Fatalf("a restart under the threshold must not re-announce: %v", r.kinds())
	}
	// ...and the close still happens when the space comes back.
	r.free = 80 * gib
	r.w.Tick()
	if !eq(r.kinds(), "disk-ok") {
		t.Fatalf("kinds = %v, want disk-ok", r.kinds())
	}
}

func TestReadErrorWritesNothing(t *testing.T) {
	r := newRig(40)
	r.w.Start()
	r.freeErr = errors.New("statfs: boom")
	r.free = 1 * gib
	r.w.Tick()
	if len(r.lines) != 0 || len(r.events) != 0 {
		t.Fatalf("a failed read says nothing: %v", r.kinds())
	}
}

func TestAlertCutWithAnOpenAlertClosesItAtStart(t *testing.T) {
	r := newRig(0) // -disk-alert-gb 0
	r.open = true
	r.w.Start()
	if !eq(r.kinds(), "disk-ok") || len(r.events) != 1 || r.events[0].Type != pigtail.EventDiskOK {
		t.Fatalf("kinds = %v, events = %+v: an open alert must not outlive its loop", r.kinds(), r.events)
	}
	r.free = 1 * gib
	r.w.Tick()
	if len(r.lines) != 1 {
		t.Fatalf("a cut alert never opens: %v", r.kinds())
	}
}

func TestAlertCutWithoutAnOpenAlertIsSilent(t *testing.T) {
	r := newRig(0)
	r.w.Start()
	r.free = 1 * gib
	r.w.Tick()
	if len(r.lines) != 0 {
		t.Fatalf("kinds = %v", r.kinds())
	}
}

// A journal write that fails leaves the state alone: the next tick retries,
// and nothing is published for a line that does not exist.
func TestJournalFailureRetriesNextTick(t *testing.T) {
	r := newRig(40)
	r.w.Start()
	r.free = 10 * gib
	r.jErr = errors.New("db locked")
	r.w.Tick()
	if len(r.events) != 0 {
		t.Fatalf("no event without its line: %+v", r.events)
	}
	r.jErr = nil
	r.w.Tick()
	if !eq(r.kinds(), "disk-low") || len(r.events) != 1 {
		t.Fatalf("kinds = %v, events = %d", r.kinds(), len(r.events))
	}
}

func TestSummaryCarriesTheNumbers(t *testing.T) {
	r := newRig(40)
	r.w.Start()
	r.free = 31 * gib
	r.w.Tick()
	free, ok := FreeBytesOf(r.lines[0].Summary)
	if !ok || free != 31*gib {
		t.Fatalf("FreeBytesOf(%q) = %d, %v", r.lines[0].Summary, free, ok)
	}
	if _, ok := FreeBytesOf("garbage"); ok {
		t.Fatal("garbage must not parse")
	}
}

// A disk already low when pigtaild starts alerts at once, not a minute later.
func TestRunReadsOnceRightAfterStart(t *testing.T) {
	r := newRig(40)
	r.free = 10 * gib
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.w.Run(ctx)
	if !eq(r.kinds(), "disk-low") {
		t.Fatalf("kinds = %v, want one disk-low taken at start", r.kinds())
	}
}

// Cut mode says so; it never claims "alert under 0 GB".
func TestRunLogsCutMode(t *testing.T) {
	r := newRig(0)
	var logs []string
	r.w.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.w.Run(ctx)
	if len(logs) == 0 || !strings.Contains(logs[0], "disk alert cut") || strings.Contains(logs[0], "under 0 GB") {
		t.Fatalf("logs = %q, want a 'disk alert cut' line", logs)
	}
}

// A failing disk is logged once per failure run, not once a minute; a good
// read re-arms the log.
func TestReadErrorLogsOncePerRun(t *testing.T) {
	r := newRig(40)
	var logs []string
	r.w.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	r.w.Start()
	r.freeErr = errors.New("statfs: boom")
	r.w.Tick()
	r.w.Tick()
	r.w.Tick()
	if len(logs) != 1 {
		t.Fatalf("logs = %q, want one line for three failures", logs)
	}
	r.freeErr = nil
	r.w.Tick()
	r.freeErr = errors.New("statfs: boom")
	r.w.Tick()
	if len(logs) != 2 {
		t.Fatalf("logs = %q, want a second line after a good read in between", logs)
	}
}

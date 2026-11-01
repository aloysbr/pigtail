package sessbudget

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestHistoryRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-samples.jsonl")
	h := NewHistory(path)
	h.maxBytes = 600 // a few records
	var n int
	for i := 0; i < 20; i++ {
		rec := pigtail.SessionSample{TS: t0.Add(time.Duration(i) * time.Second), Live: i}
		if err := h.Append(rec); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no .1 file mid-run, no restart: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > h.maxBytes+300 {
		t.Fatalf("live file grew to %d past the cap %d", fi.Size(), h.maxBytes)
	}
	recs, err := h.Read(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 || len(recs) > n {
		t.Fatalf("read %d records", len(recs))
	}
	// Spans both files, oldest first, ending with the newest.
	if recs[len(recs)-1].Live != 19 {
		t.Fatalf("newest record: %+v", recs[len(recs)-1])
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].Live <= recs[i-1].Live {
			t.Fatalf("not oldest first: %d then %d", recs[i-1].Live, recs[i].Live)
		}
	}
	old, _ := os.ReadFile(path + ".1")
	if len(old) == 0 {
		t.Fatal(".1 is empty")
	}
	// since filter.
	recs, _ = h.Read(t0.Add(18 * time.Second))
	if len(recs) != 2 {
		t.Fatalf("since filter returned %d", len(recs))
	}
}

func TestSamplesPathInStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got := SamplesPath(); got != "/xdg/state/pigtail/session-samples.jsonl" {
		t.Fatalf("XDG: %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got := SamplesPath(); got != "/home/u/.local/state/pigtail/session-samples.jsonl" {
		t.Fatalf("default: %q", got)
	}
}

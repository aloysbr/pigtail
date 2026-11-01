package staffing

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func dec(ts time.Time, outcome string) pigtail.StaffingDecision {
	return pigtail.StaffingDecision{TS: ts, LastAt: ts, Mode: pigtail.StaffingAdvise, Ceiling: 3, Outcome: outcome}
}

func TestLogAppendReadSince(t *testing.T) {
	l := OpenLog(filepath.Join(t.TempDir(), "staffing-decisions.jsonl"))
	t0 := time.Unix(1700000000, 0).UTC()
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	for _, ts := range []time.Time{t0, t1, t2} {
		if err := l.Append(dec(ts, pigtail.StaffingOutcomeIdle)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.Read(t1, 0)
	if err != nil || len(got) != 2 || !got[0].TS.Equal(t1) || !got[1].TS.Equal(t2) {
		t.Fatalf("Read(t1, 0) = %+v, %v", got, err)
	}
	got, err = l.Read(time.Time{}, 1)
	if err != nil || len(got) != 1 || !got[0].TS.Equal(t2) {
		t.Fatalf("Read(zero, 1) = %+v, %v", got, err)
	}
	if LogPath("/a/pigtail.db") != "/a/staffing-decisions.jsonl" || LogPath("") != "" {
		t.Fatalf("LogPath = %q", LogPath("/a/pigtail.db"))
	}
	nilLog := OpenLog("")
	if nilLog != nil || nilLog.Append(dec(t0, "idle")) != nil {
		t.Fatal("a log without path must be nil and write nothing")
	}
}

func TestLogReadSkipsBadLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "staffing-decisions.jsonl")
	l := OpenLog(p)
	t0 := time.Unix(1700000000, 0).UTC()
	_ = l.Append(dec(t0, "idle"))
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("not json\n")
	_ = f.Close()
	_ = l.Append(dec(t0.Add(time.Second), "idle"))
	got, err := l.Read(time.Time{}, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("Read = %+v, %v; want the two good lines", got, err)
	}
}

func TestLogRotate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "staffing-decisions.jsonl")
	l := OpenLog(p)
	t0 := time.Unix(1700000000, 0).UTC()
	for i := 0; i < 5; i++ {
		_ = l.Append(dec(t0.Add(time.Duration(i)*time.Second), "idle"))
	}
	if err := l.Rotate(1 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotated under the limit: %v", err)
	}
	if err := l.Rotate(10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("not rotated past the limit: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("live file still there: %v", err)
	}
	got, _ := l.Read(time.Time{}, 0)
	if len(got) != 5 {
		t.Fatalf("Read after rotate = %d lines, want 5 (rotated file read too)", len(got))
	}
}

func TestLogReplaceLast(t *testing.T) {
	p := filepath.Join(t.TempDir(), "staffing-decisions.jsonl")
	l := OpenLog(p)
	if err := l.ReplaceLast(dec(time.Now(), "idle")); err == nil {
		t.Fatal("ReplaceLast on a missing file: no error")
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.ReplaceLast(dec(time.Now(), "idle")); err == nil {
		t.Fatal("ReplaceLast on an empty file: no error")
	}
	t0 := time.Unix(1700000000, 0).UTC()
	for i := 0; i < 3; i++ {
		_ = l.Append(dec(t0.Add(time.Duration(i)*time.Minute), "idle"))
	}
	last := dec(t0.Add(2*time.Minute), "idle")
	last.Repeats, last.LastAt = 4, t0.Add(10*time.Minute)
	if err := l.ReplaceLast(last); err != nil {
		t.Fatal(err)
	}
	got, err := l.Read(time.Time{}, 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("Read = %+v, %v", got, err)
	}
	if got[0].Repeats != 0 || got[1].Repeats != 0 || got[2].Repeats != 4 || !got[2].LastAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("after ReplaceLast = %+v", got)
	}
}

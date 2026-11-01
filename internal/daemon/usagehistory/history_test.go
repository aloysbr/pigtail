package usagehistory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeusage"
)

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func sample(uuid string, at time.Time, p7 float64) claudeusage.Sample {
	return claudeusage.Sample{At: at, AccountUUID: uuid, Label: uuid[:1], FetchedAt: at.Add(-time.Minute),
		SevenDay: &claudeusage.SampleWindow{Percent: p7}}
}

func open(t *testing.T, path string, now time.Time) *History {
	t.Helper()
	h, err := Open(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("Open returned nil for a path")
	}
	return h
}

func ats(ss []claudeusage.Sample) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.AccountUUID[:1]+"@"+s.At.Format("15:04"))
	}
	return out
}

func TestHistoryAppendAndSince(t *testing.T) {
	h := open(t, filepath.Join(t.TempDir(), FileName), t0)
	if err := h.Append([]claudeusage.Sample{sample("m1", t0.Add(-2*time.Hour), 10), sample("j1", t0.Add(-2*time.Hour), 20)}); err != nil {
		t.Fatal(err)
	}
	if err := h.Append([]claudeusage.Sample{sample("m1", t0.Add(-time.Hour), 11)}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ats(h.Since(t0.Add(-3*time.Hour))), " ")
	if got != "m@10:00 j@10:00 m@11:00" {
		t.Fatalf("Since = %q", got)
	}
	got = strings.Join(ats(h.Since(t0.Add(-time.Hour))), " ")
	if got != "m@11:00" {
		t.Fatalf("Since(-1h) = %q, want the At >= t only", got)
	}
	// Since returns a copy.
	s := h.Since(time.Time{})
	s[0].Label = "x"
	if h.Since(time.Time{})[0].Label == "x" {
		t.Fatal("Since shares its slice with the history")
	}
}

func TestHistoryReloadsOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	h := open(t, path, t0)
	in := []claudeusage.Sample{sample("m1", t0.Add(-50*time.Hour), 10), sample("m1", t0.Add(-time.Hour), 30)}
	if err := h.Append(in); err != nil {
		t.Fatal(err)
	}
	h2 := open(t, path, t0)
	got := h2.Since(time.Time{})
	if len(got) != 2 || !got[0].At.Equal(in[0].At) || !got[1].At.Equal(in[1].At) ||
		got[0].SevenDay == nil || got[0].SevenDay.Percent != 10 || got[1].SevenDay.Percent != 30 {
		t.Fatalf("reloaded = %+v", got)
	}
}

func TestHistoryKeepPrunesMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	h := open(t, path, t0)
	if err := h.Append([]claudeusage.Sample{sample("m1", t0.Add(-73*time.Hour), 1)}); err != nil {
		t.Fatal(err)
	}
	if err := h.Append([]claudeusage.Sample{sample("m1", t0, 2)}); err != nil {
		t.Fatal(err)
	}
	if got := ats(h.Since(time.Time{})); len(got) != 1 || got[0] != "m@12:00" {
		t.Fatalf("memory = %v, want the 73 h sample pruned", got)
	}
	// Reopened at t0, the old sample stays out of memory too.
	if got := ats(open(t, path, t0).Since(time.Time{})); len(got) != 1 {
		t.Fatalf("reopened memory = %v", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Fatalf("file has %d lines, want both samples kept on disk", n)
	}
}

func TestHistoryRotatesOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	h := open(t, path, t0)
	if err := h.Append([]claudeusage.Sample{sample("m1", t0.Add(-time.Hour), 5)}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(strings.Repeat("x", MaxBytes) + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	h2 := open(t, path, t0)
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the live file is still there after rotation")
	}
	if got := ats(h2.Since(time.Time{})); len(got) != 1 || got[0] != "m@11:00" {
		t.Fatalf("after rotation = %v, want the rotated sample read back", got)
	}
	if err := h2.Append([]claudeusage.Sample{sample("m1", t0, 6)}); err != nil {
		t.Fatal(err)
	}
	if got := ats(open(t, path, t0).Since(time.Time{})); strings.Join(got, " ") != "m@11:00 m@12:00" {
		t.Fatalf("reopened = %v, want .1 then the live file", got)
	}
}

func TestHistorySkipsBadLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	h := open(t, path, t0)
	if err := h.Append([]claudeusage.Sample{sample("m1", t0.Add(-2*time.Hour), 1)}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("{\"at\": \"cut short\n"))
	_ = f.Close()
	if err := h.Append([]claudeusage.Sample{sample("m1", t0.Add(-time.Hour), 2)}); err != nil {
		t.Fatal(err)
	}
	if got := ats(open(t, path, t0).Since(time.Time{})); strings.Join(got, " ") != "m@10:00 m@11:00" {
		t.Fatalf("reopened = %v, want the bad line skipped", got)
	}
}

func TestHistoryNilSafe(t *testing.T) {
	h, err := Open("", t0)
	if err != nil || h != nil {
		t.Fatalf("Open(\"\") = %v, %v; want nil, nil", h, err)
	}
	if err := h.Append([]claudeusage.Sample{sample("m1", t0, 1)}); err != nil {
		t.Fatal(err)
	}
	if got := h.Since(time.Time{}); got == nil || len(got) != 0 {
		t.Fatalf("nil Since = %#v, want empty non-nil", got)
	}
}

func TestHistoryFileMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	h := open(t, path, t0)
	if err := h.Append([]claudeusage.Sample{sample("m1", t0, 1)}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

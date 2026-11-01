package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/cliexec"
	"github.com/aloysbr/pigtail/internal/heavycmd"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestChildGuard(t *testing.T) {
	watch := func() bool { return false }

	// Not an agent (a human at a terminal, the pre-push hook: stdin a pipe,
	// stderr a tty, a controlling terminal present): nothing changes.
	if o := childGuard(false, false, "tsc", 0, false, watch); o.Group || o.Orphaned != nil || o.MaxRun != 0 || o.Tree != nil {
		t.Errorf("non-agent: %+v, want the zero Opts", o)
	}
	// Nested under another admitted command: the outer one holds the tree.
	if o := childGuard(true, true, "tsc", 0, false, watch); o.Group || o.Orphaned != nil || o.MaxRun != 0 {
		t.Errorf("nested: %+v, want the zero Opts", o)
	}
	// Agent: own group, the class ceiling, the probe and the tree reader.
	o := childGuard(true, false, "tsc", 0, false, watch)
	if !o.Group || o.MaxRun != heavycmd.RunCeiling(heavycmd.ClassTSC) || o.Orphaned == nil || o.Tree == nil {
		t.Errorf("agent: %+v", o)
	}
	// --max-run=0 lifts the ceiling, --max-run=5m replaces it.
	if o := childGuard(true, false, "tsc", 0, true, watch); o.MaxRun != 0 || !o.Group {
		t.Errorf("--max-run=0: MaxRun=%s group=%v", o.MaxRun, o.Group)
	}
	if o := childGuard(true, false, "tsc", 5*time.Minute, true, watch); o.MaxRun != 5*time.Minute {
		t.Errorf("--max-run=5m: MaxRun=%s", o.MaxRun)
	}
	// A probe that was not armed (nohup, launchd parent) stays nil.
	if o := childGuard(true, false, "tsc", 0, false, nil); o.Orphaned != nil || !o.Group {
		t.Errorf("unarmed: %+v", o)
	}
}

type fakeTTY struct{ closed *bool }

func (f fakeTTY) Close() error { *f.closed = true; return nil }

func TestHasControllingTTY(t *testing.T) {
	old := openTTY
	t.Cleanup(func() { openTTY = old })

	closed := false
	openTTY = func() (interface{ Close() error }, error) { return fakeTTY{&closed}, nil }
	if !hasControllingTTY() || !closed {
		t.Errorf("open succeeded: has=%v closed=%v, want true and the fd closed", hasControllingTTY(), closed)
	}
	openTTY = func() (interface{ Close() error }, error) { return nil, errors.New("no tty") }
	if hasControllingTTY() {
		t.Error("open failed: must read as no controlling terminal")
	}
}

func seamAgent(t *testing.T, agent bool, watch func() func() bool) {
	t.Helper()
	oldTTY, oldArm := openTTY, armOrphanWatch
	t.Cleanup(func() { openTTY, armOrphanWatch = oldTTY, oldArm })
	closed := false
	if agent {
		openTTY = func() (interface{ Close() error }, error) { return nil, errors.New("no tty") }
	} else {
		openTTY = func() (interface{ Close() error }, error) { return fakeTTY{&closed}, nil }
	}
	armOrphanWatch = watch
}

type journalSink struct {
	mu      sync.Mutex
	entries []pigtail.JournalEntry
}

func (j *journalSink) send(_ context.Context, e pigtail.JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, e)
	return nil
}

func TestRunAdmittedNotAgentIgnoresOrphanWatch(t *testing.T) {
	startAdmissionDaemon(t, 2)
	orphan := func() func() bool { return func() bool { return true } }

	// A human at a terminal: the probe is never consulted.
	t.Setenv(envAdmitLease, "")
	seamAgent(t, false, orphan)
	if code := runAdmitted([]string{"sh", "-c", "exit 3"}, admitOptions{maxWait: time.Second, onTimeout: "fail"}); code != 3 {
		t.Fatalf("not agent: code %d, want the child's 3 (never 129)", code)
	}

	// An agent, but nested under an outer admit: same.
	t.Setenv(envAdmitLease, "adm-outer")
	seamAgent(t, true, orphan)
	if code := runAdmitted([]string{"sh", "-c", "exit 4"}, admitOptions{maxWait: time.Second, onTimeout: "fail"}); code != 4 {
		t.Fatalf("nested: code %d, want the child's 4 (never 129)", code)
	}
}

func TestRunAdmittedSkipsWhenOrphanedBeforeStart(t *testing.T) {
	c := startAdmissionDaemon(t, 2)
	t.Setenv(envAdmitLease, "")
	seamAgent(t, true, func() func() bool { return func() bool { return true } })
	ran := filepath.Join(t.TempDir(), "ran")
	sink := &journalSink{}
	code := runAdmitted([]string{"sh", "-c", `echo ran > "$0"`, ran},
		admitOptions{maxWait: time.Second, onTimeout: "fail", journal: sink.send})
	if code != 129 {
		t.Fatalf("code = %d, want 129", code)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the child was started")
	}
	if len(sink.entries) != 1 || sink.entries[0].Kind != "admit-orphan-skipped" {
		t.Fatalf("journal = %+v, want one admit-orphan-skipped", sink.entries)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(c.Status().Leases) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the lease was not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAdmitOnStop(t *testing.T) {
	longLabel := "line one\nline two " + strings.Repeat("é", 400)
	for _, tc := range []struct{ reason, kind, line string }{
		{"ceiling", "admit-ceiling-killed", "ceiling"},
		{"orphaned", "admit-orphan-killed", "orphan"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var stderr bytes.Buffer
			sink := &journalSink{}
			newOnStop(&stderr, "admit", sink.send, "tsc", longLabel, "/work/tree", 30*time.Minute)(tc.reason)
			if !strings.Contains(stderr.String(), tc.line) || strings.Count(stderr.String(), "\n") != 1 {
				t.Errorf("stderr = %q, want one line mentioning %q", stderr.String(), tc.line)
			}
			if len(sink.entries) != 1 {
				t.Fatalf("entries = %d", len(sink.entries))
			}
			e := sink.entries[0]
			if e.Kind != tc.kind || !strings.HasPrefix(e.ID, tc.kind+":") || e.Source != "file:/work/tree" || e.Scope != "" {
				t.Errorf("entry = %+v", e)
			}
			if err := e.Validate(); err != nil {
				t.Errorf("Validate: %v (summary %q)", err, e.Summary)
			}
			if strings.ContainsAny(e.Summary, "\r\n") || len([]rune(e.Summary)) > 200 {
				t.Errorf("summary not one line <= 200 runes: %d runes", len([]rune(e.Summary)))
			}
		})
	}

	t.Run("send failure is loud", func(t *testing.T) {
		var stderr bytes.Buffer
		newOnStop(&stderr, "admit", func(context.Context, pigtail.JournalEntry) error { return errors.New("boom") },
			"tsc", "x", "/w", time.Minute)("ceiling")
		if !strings.Contains(stderr.String(), "pigtail admit: journal: boom") {
			t.Errorf("stderr = %q", stderr.String())
		}
	})

	t.Run("a blocked journal is bounded", func(t *testing.T) {
		var stderr bytes.Buffer
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		start := time.Now()
		newOnStop(&stderr, "admit", func(context.Context, pigtail.JournalEntry) error { <-release; return nil },
			"tsc", "x", "/w", time.Minute)("orphaned")
		if d := time.Since(start); d > 2500*time.Millisecond {
			t.Errorf("onStop took %s, want <= 2.5s", d)
		}
		if !strings.Contains(stderr.String(), "pigtail admit: journal:") {
			t.Errorf("stderr = %q, want a journal line", stderr.String())
		}
	})
}

func TestAdmitMaxRunFlag(t *testing.T) {
	cmd := admitCmd()
	f := cmd.Flags().Lookup("max-run")
	if f == nil {
		t.Fatal("--max-run is missing")
	}
	if err := cmd.ParseFlags([]string{"--max-run=0"}); err != nil {
		t.Fatal(err)
	}
	if !cmd.Flags().Changed("max-run") || f.Value.String() != "0s" {
		t.Errorf("--max-run=0: changed=%v value=%s", cmd.Flags().Changed("max-run"), f.Value)
	}
	cmd = admitCmd()
	if err := cmd.ParseFlags([]string{"--max-run", "5m"}); err != nil {
		t.Fatal(err)
	}
	if got := cmd.Flags().Lookup("max-run").Value.String(); got != "5m0s" {
		t.Errorf("--max-run 5m: %s", got)
	}
}

var _ = cliexec.Opts{}

func TestRecordSourceCutOnRuneKeepsSpaces(t *testing.T) {
	cwd := "/work/my tree/" + strings.Repeat("é", 700) // 2 bytes each: 1400 bytes
	var stderr bytes.Buffer
	sink := &journalSink{}
	newOnStop(&stderr, "admit", sink.send, "tsc", "x", cwd, time.Minute)("ceiling")
	if len(sink.entries) != 1 {
		t.Fatalf("entries = %d, stderr %q", len(sink.entries), stderr.String())
	}
	src := sink.entries[0].Source
	if !utf8.ValidString(src) || len(src) > pigtail.JournalSourceMax {
		t.Errorf("source valid=%v len=%d, want valid UTF-8 within %d bytes", utf8.ValidString(src), len(src), pigtail.JournalSourceMax)
	}
	if !strings.HasPrefix(src, "file:/work/my tree/") {
		t.Errorf("source = %.40q, want the path's space kept", src)
	}
	if strings.Contains(stderr.String(), "journal:") {
		t.Errorf("journal refused the entry: %q", stderr.String())
	}
}

func TestGuardLinesNameTheirCommand(t *testing.T) {
	for _, who := range []string{"admit", "test"} {
		var stderr bytes.Buffer
		newOnStop(&stderr, who, nil, "tsc", "x", "/w", time.Minute)("ceiling")
		skipOrphaned(&stderr, who, nil, "tsc", "x", "/w")
		for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
			if !strings.HasPrefix(line, "pigtail "+who+": ") {
				t.Errorf("line %q lacks the prefix %q", line, "pigtail "+who+": ")
			}
		}
	}
}

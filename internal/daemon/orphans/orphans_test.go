package orphans

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Real command lines of Claude Code's Bash-tool shells (29/10), shortened
// after the snapshot path: the signature only reads the head.
const (
	zshMaxPerso = `/bin/zsh -c source /Users/ada/.claude-max/shell-snapshots/snapshot-zsh-1793267822649-o4yq4h.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL 2>/dev/null || true && eval 'pnpm vitest run' < /dev/null && pwd -P >| /tmp/claude-3e73-cwd`
	zshDefault  = `/bin/zsh -c source /Users/ada/.claude/shell-snapshots/snapshot-zsh-1793237492646-gnjr63.sh 2>/dev/null || true && eval 'make check' < /dev/null && pwd -P >| /tmp/claude-1a2b-cwd`
	bashMax     = `/bin/bash -c source /home/ada/.claude-max/shell-snapshots/snapshot-bash-1793267822649-o4yq4h.sh 2>/dev/null || true && eval 'go test ./...' < /dev/null && pwd -P >| /tmp/claude-9f00-cwd`
	bashDefault = `/bin/bash -c source /home/ada/.claude/shell-snapshots/snapshot-bash-1793237492646-gnjr63.sh 2>/dev/null || true && eval 'ls' < /dev/null`
)

func TestSignature(t *testing.T) {
	for _, c := range []string{zshMaxPerso, zshDefault, bashMax, bashDefault} {
		if !IsAgentShell(c) {
			t.Errorf("not matched: %.80s", c)
		}
	}
	for _, c := range []string{
		`sh -c ` + zshMaxPerso,
		`/bin/sh -c echo ` + zshDefault,
		`sh -c set -a; . ./.env.local; set +a; exec pnpm dev`,
		`-zsh`,
		`/bin/zsh -il`,
		`/bin/zsh`,
		`/bin/zsh -c source /Users/x/.claude/not-snapshots/snapshot-zsh-1.sh && true`,
		`/usr/local/bin/zsh -c source /Users/x/.claude/shell-snapshots/snapshot-zsh-1-a.sh && true`,
	} {
		if IsAgentShell(c) {
			t.Errorf("matched: %.80s", c)
		}
	}
}

func TestParsePS(t *testing.T) {
	raw := "  101     1   101   0.5 " + zshMaxPerso + "\n" +
		"  102   101   101  12.0 node /x/vitest.mjs run\n" +
		"garbage line\n" +
		"  103   102   103   1,5 bad-cpu\n" +
		"  104   102   104   3.0 chromium --type=renderer\n" +
		"\n"
	got, err := parsePS([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d procs, want 3: %+v", len(got), got)
	}
	want := Proc{PID: 101, PPID: 1, PGID: 101, CPU: 0.5, Command: zshMaxPerso}
	if got[0] != want {
		t.Errorf("first = %+v, want %+v", got[0], want)
	}
	if got[2].Command != "chromium --type=renderer" || got[2].PGID != 104 {
		t.Errorf("third = %+v", got[2])
	}
	if _, err := parsePS([]byte("garbage\n\n")); err == nil {
		t.Error("an output with no readable line must be an error")
	}
	if _, err := parsePS(nil); err == nil {
		t.Error("an empty output must be an error")
	}
}

// harness is a fake process table, clock and signal recorder.
type harness struct {
	t       *testing.T
	now     time.Time
	procs   []Proc
	after   func() // runs at the grace sleep: the table changes then
	signals []string
	journal []pigtail.JournalEntry
	logs    []string
	r       *Reaper
}

func newHarness(t *testing.T, mode Mode) *harness {
	h := &harness{t: t, now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	h.r = New(mode, 15*time.Minute)
	h.r.Procs = func() ([]Proc, error) { return append([]Proc(nil), h.procs...), nil }
	h.r.Signal = func(pid int, sig syscall.Signal) error {
		h.signals = append(h.signals, fmt.Sprintf("%d:%s", pid, sigName(sig)))
		return nil
	}
	h.r.Now = func() time.Time { return h.now }
	h.r.Sleep = func(d time.Duration) {
		h.now = h.now.Add(d)
		if h.after != nil {
			h.after()
		}
	}
	h.r.Journal = func(e pigtail.JournalEntry) error {
		h.journal = append(h.journal, e)
		return nil
	}
	h.r.Logf = func(f string, a ...any) { h.logs = append(h.logs, fmt.Sprintf(f, a...)) }
	return h
}

func sigName(s syscall.Signal) string {
	switch s {
	case syscall.SIGTERM:
		return "TERM"
	case syscall.SIGKILL:
		return "KILL"
	}
	return s.String()
}

func (h *harness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	h.r.Tick(h.now)
}

// orphanTree is a snapshot root orphaned at pid 500, a vitest below it in
// its group, and a chromium below that which called setsid (pgid 520).
func orphanTree() []Proc {
	return []Proc{
		{PID: 500, PPID: 1, PGID: 500, CPU: 0.1, Command: zshMaxPerso},
		{PID: 510, PPID: 500, PGID: 500, CPU: 40, Command: "node vitest.mjs"},
		{PID: 520, PPID: 510, PGID: 520, CPU: 60.5, Command: "chromium --headless"},
		{PID: 900, PPID: 1, PGID: 900, CPU: 0, Command: "/usr/sbin/unrelated"},
	}
}

func TestReapAfterThreshold(t *testing.T) {
	h := newHarness(t, ModeOn)
	h.procs = orphanTree()
	t0 := h.now
	h.tick(0)
	h.tick(14 * time.Minute)
	if len(h.signals) != 0 || len(h.journal) != 0 {
		t.Fatalf("before 15 min: signals %v journal %v", h.signals, h.journal)
	}
	// At the grace: 510 is gone, 520 survives, and 500's pid now runs
	// another command (reused) — only 520 gets KILL.
	h.after = func() {
		h.procs = []Proc{
			{PID: 500, PPID: 1, PGID: 500, Command: "/usr/bin/other"},
			{PID: 520, PPID: 1, PGID: 520, Command: "chromium --headless"},
		}
	}
	h.tick(time.Minute)
	got := strings.Join(h.signals, " ")
	if got != "-500:TERM -520:TERM 520:KILL" {
		t.Fatalf("signals = %q, want TERM to both groups then KILL to 520 alone", got)
	}
	if len(h.journal) != 1 {
		t.Fatalf("journal = %+v, want one entry", h.journal)
	}
	e := h.journal[0]
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if e.Kind != "orphan-tree-reaped" || e.Scope != string(scope.Master()) || e.Source != "proc:500" {
		t.Errorf("entry = %+v", e)
	}
	if e.ID != fmt.Sprintf("orphan-tree:500:%d", t0.Unix()) {
		t.Errorf("ID = %q", e.ID)
	}
	if !e.Timestamp.Equal(t0.Add(15 * time.Minute)) {
		t.Errorf("Timestamp = %v, want the reap time %v", e.Timestamp, t0.Add(15*time.Minute))
	}
	for _, want := range []string{"/bin/zsh -c source", "15m", "3 pids", "100.6%"} {
		if !strings.Contains(e.Summary, want) {
			t.Errorf("summary %q lacks %q", e.Summary, want)
		}
	}
	// The tree is gone: no second reap.
	h.after = nil
	h.procs = nil
	h.tick(time.Minute)
	if len(h.journal) != 1 {
		t.Errorf("reaped twice: %+v", h.journal)
	}
}

// A pid that survives the grace with the same command but another process
// group is not the captured process any more (it called setsid or setpgid, or
// the pid was reused by a twin): no KILL.
func TestReapNoKillWhenPgidChanged(t *testing.T) {
	h := newHarness(t, ModeOn)
	h.procs = orphanTree()
	h.tick(0)
	h.after = func() {
		h.procs = []Proc{
			{PID: 520, PPID: 1, PGID: 777, Command: "chromium --headless"},
		}
	}
	h.tick(15 * time.Minute)
	got := strings.Join(h.signals, " ")
	if got != "-500:TERM -520:TERM" {
		t.Fatalf("signals = %q, want the two TERMs and no KILL", got)
	}
}

func TestReapSummaryIsBounded(t *testing.T) {
	h := newHarness(t, ModeObserve)
	long := zshMaxPerso + " " + strings.Repeat("é", 600)
	h.procs = []Proc{{PID: 500, PPID: 1, PGID: 500, Command: long}}
	h.tick(0)
	h.tick(15 * time.Minute)
	if len(h.journal) != 1 {
		t.Fatalf("journal = %+v", h.journal)
	}
	e := h.journal[0]
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestObserveModeNeverSignals(t *testing.T) {
	h := newHarness(t, ModeObserve)
	h.procs = orphanTree()
	h.tick(0)
	h.tick(15 * time.Minute)
	h.tick(time.Minute)
	if len(h.signals) != 0 {
		t.Fatalf("observe signalled: %v", h.signals)
	}
	if len(h.journal) != 1 || h.journal[0].Kind != "orphan-tree-would-reap" {
		t.Fatalf("journal = %+v, want one orphan-tree-would-reap", h.journal)
	}
	if err := h.journal[0].Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNeverSignalsLowOrOwnGroup(t *testing.T) {
	h := newHarness(t, ModeOn)
	own := syscall.Getpgrp()
	self := os.Getpid()
	h.procs = []Proc{
		{PID: 500, PPID: 1, PGID: 500, Command: zshMaxPerso},
		{PID: 510, PPID: 500, PGID: 0, Command: "pgid-zero"},
		{PID: 511, PPID: 500, PGID: 1, Command: "pgid-one"},
		{PID: 512, PPID: 500, PGID: own, Command: "my-group"},
		{PID: self, PPID: 500, PGID: 530, Command: "pigtaild"},
	}
	h.after = func() {} // everything survives the grace
	h.tick(0)
	h.tick(15 * time.Minute)
	for _, s := range h.signals {
		for _, bad := range []string{"-0:", "-1:", fmt.Sprintf("-%d:", own), fmt.Sprintf("%d:", self), "-530:"} {
			if strings.HasPrefix(s, bad) {
				t.Errorf("signalled %s", s)
			}
		}
	}
	if got := strings.Join(h.signals, " "); got != "-500:TERM 500:KILL" {
		t.Errorf("signals = %q, want the root's group alone", got)
	}
}

func TestNotOrphanNotTouched(t *testing.T) {
	h := newHarness(t, ModeOn)
	// A live agent shell (ppid = its claude) and an orphan that is not its
	// group's leader: never signalled.
	h.procs = []Proc{
		{PID: 400, PPID: 300, PGID: 400, Command: zshMaxPerso},
		{PID: 300, PPID: 1, PGID: 300, Command: "claude"},
		{PID: 600, PPID: 1, PGID: 650, Command: zshDefault},
	}
	h.tick(0)
	h.tick(30 * time.Minute)
	if len(h.signals) != 0 || len(h.journal) != 0 {
		t.Fatalf("signals %v journal %v", h.signals, h.journal)
	}

	// A root that disappears, or whose pid now runs another command, starts
	// over.
	h.procs = []Proc{{PID: 500, PPID: 1, PGID: 500, Command: zshMaxPerso}}
	h.tick(time.Minute)
	h.procs = nil
	h.tick(10 * time.Minute)
	h.procs = []Proc{{PID: 500, PPID: 1, PGID: 500, Command: zshMaxPerso}}
	h.tick(10 * time.Minute)
	if len(h.signals) != 0 {
		t.Fatalf("a vanished root kept its count: %v", h.signals)
	}
	h.procs = []Proc{{PID: 500, PPID: 1, PGID: 500, Command: zshDefault}}
	h.tick(10 * time.Minute)
	if len(h.signals) != 0 {
		t.Fatalf("a changed command kept its count: %v", h.signals)
	}
	h.tick(14 * time.Minute)
	if len(h.signals) != 0 {
		t.Fatalf("reaped before 15 min of its own: %v", h.signals)
	}
	h.tick(time.Minute)
	if len(h.signals) == 0 {
		t.Fatal("the new root was never reaped")
	}
}

func TestSignatureMissingLogsOncePer24h(t *testing.T) {
	h := newHarness(t, ModeOn)
	h.procs = []Proc{
		{PID: 300, PPID: 200, PGID: 300, Command: "/Users/x/.local/bin/claude --resume"},
	}
	count := func() int {
		n := 0
		for _, l := range h.logs {
			if strings.Contains(l, "signature shell-snapshots introuvable") {
				n++
			}
		}
		return n
	}
	h.tick(0)
	for i := 0; i < 24*60; i++ {
		h.tick(time.Minute)
	}
	if count() != 1 {
		t.Fatalf("after 24 h: %d logs, want 1 (%v)", count(), h.logs)
	}
	for i := 0; i < 60; i++ {
		h.tick(time.Minute)
	}
	if count() != 1 {
		t.Fatalf("logged again inside the next window: %d", count())
	}
	for i := 0; i < 23*60; i++ {
		h.tick(time.Minute)
	}
	if count() != 2 {
		t.Fatalf("after 48 h: %d logs, want 2", count())
	}

	// A snapshot shell seen in the window: no log.
	h2 := newHarness(t, ModeOn)
	h2.procs = []Proc{
		{PID: 300, PPID: 200, PGID: 300, Command: "claude"},
		{PID: 301, PPID: 300, PGID: 301, Command: zshMaxPerso},
	}
	h2.tick(0)
	h2.procs = h2.procs[:1]
	for i := 0; i < 25*60; i++ {
		h2.tick(time.Minute)
	}
	for _, l := range h2.logs {
		if strings.Contains(l, "introuvable") {
			t.Fatalf("logged although a snapshot shell was seen: %q", l)
		}
	}

	// No claude alive: no log either.
	h3 := newHarness(t, ModeOn)
	h3.procs = []Proc{{PID: 300, PPID: 1, PGID: 300, Command: "/usr/bin/claudette"}}
	h3.tick(0)
	for i := 0; i < 25*60; i++ {
		h3.tick(time.Minute)
	}
	for _, l := range h3.logs {
		if strings.Contains(l, "introuvable") {
			t.Fatalf("logged with no claude alive: %q", l)
		}
	}
}

func TestStartupLog(t *testing.T) {
	h := newHarness(t, ModeObserve)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.r.Run(ctx)
	if len(h.logs) == 0 || !strings.Contains(h.logs[0], "orphan reaper") ||
		!strings.Contains(h.logs[0], "observe") || !strings.Contains(h.logs[0], "15m") {
		t.Fatalf("startup log = %v", h.logs)
	}
}

func TestProcsErrorSkipsTick(t *testing.T) {
	h := newHarness(t, ModeOn)
	h.procs = orphanTree()
	h.tick(0)
	h.r.Procs = func() ([]Proc, error) { return nil, fmt.Errorf("ps: boom") }
	h.tick(15 * time.Minute)
	if len(h.signals) != 0 {
		t.Fatalf("signalled on a failed listing: %v", h.signals)
	}
	// A failed listing is no "vanished root": the count survives it.
	h.r.Procs = func() ([]Proc, error) { return append([]Proc(nil), h.procs...), nil }
	h.after = func() { h.procs = nil }
	h.tick(time.Minute)
	if len(h.signals) == 0 {
		t.Fatal("the count did not survive a failed listing")
	}
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"on", "observe", "off"} {
		if m, err := ParseMode(s); err != nil || string(m) != s {
			t.Errorf("ParseMode(%q) = %q, %v", s, m, err)
		}
	}
	if _, err := ParseMode("yes"); err == nil {
		t.Error("ParseMode(yes) accepted")
	}
}

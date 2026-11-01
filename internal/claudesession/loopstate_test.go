package claudesession

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ls0 = time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)

func lsTS(d time.Duration) string { return ls0.Add(d).Format("2006-01-02T15:04:05.000Z") }

func lsArm(at, due time.Duration) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"%s","toolUseResult":{"scheduledFor":%d,"clampedDelaySeconds":60,"wasClamped":false}}`,
		lsTS(at), ls0.Add(due).UnixMilli())
}
func lsFire(at time.Duration, kind string) string {
	return fmt.Sprintf(`{"type":"system","subtype":"scheduled_task_fire","taskKind":"%s","taskId":"x","timestamp":"%s"}`, kind, lsTS(at))
}
func lsTurn(at time.Duration, typ string) string {
	return fmt.Sprintf(`{"type":"%s","timestamp":"%s","message":{"content":"hello"}}`, typ, lsTS(at))
}
func lsWrite(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
func lsRead(t *testing.T, p string) LoopState {
	t.Helper()
	st, err := NewLoopReader().Read(p)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestLoopStateLastUnfired(t *testing.T) {
	st := lsRead(t, lsWrite(t, lsArm(1*time.Minute, 5*time.Minute), lsArm(2*time.Minute, 9*time.Minute)))
	if !st.ArmAt.Equal(ls0.Add(2*time.Minute)) || !st.ScheduledFor.Equal(ls0.Add(9*time.Minute)) || !st.LoopFireAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}

func TestLoopStateLoopFireConsumesArm(t *testing.T) {
	st := lsRead(t, lsWrite(t, lsArm(1*time.Minute, 5*time.Minute), lsFire(5*time.Minute, "loop"), lsTurn(5*time.Minute, "user")))
	if !st.LoopFireAt.Equal(ls0.Add(5*time.Minute)) || !st.ArmAt.Equal(ls0.Add(time.Minute)) || !st.FireAt.Equal(st.LoopFireAt) {
		t.Fatalf("state = %+v", st)
	}
}

func TestLoopStateFiredThenRearmed(t *testing.T) {
	st := lsRead(t, lsWrite(t, lsArm(1*time.Minute, 5*time.Minute), lsFire(5*time.Minute, "loop"), lsArm(6*time.Minute, 30*time.Minute)))
	if !st.ArmAt.Equal(ls0.Add(6*time.Minute)) || !st.LoopFireAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}

func TestLoopStateCronFireDoesNotConsumeArm(t *testing.T) {
	st := lsRead(t, lsWrite(t, lsArm(1*time.Minute, 5*time.Minute), lsFire(6*time.Minute, "cron")))
	if !st.LoopFireAt.IsZero() || !st.FireAt.Equal(ls0.Add(6*time.Minute)) || st.ArmAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}

func TestLoopStateArmBeyond256KiB(t *testing.T) {
	lines := []string{lsArm(1*time.Minute, 30*time.Minute)}
	filler := strings.Repeat("x", 900)
	for i := 0; i < 400; i++ { // > 300 KiB of turn lines
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","timestamp":"%s","message":{"content":"%s"}}`, lsTS(time.Duration(2+i)*time.Second), filler))
	}
	st := lsRead(t, lsWrite(t, lines...))
	if !st.ArmAt.Equal(ls0.Add(time.Minute)) || st.TurnAt.IsZero() || st.Capped {
		t.Fatalf("state = %+v", st)
	}
}

func TestLoopStateCappedNoArm(t *testing.T) {
	old := promptReadMax
	promptReadMax = 64 << 10
	defer func() { promptReadMax = old }()
	lines := []string{lsArm(1*time.Minute, 30*time.Minute)}
	filler := strings.Repeat("x", 900)
	for i := 0; i < 400; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","timestamp":"%s","message":{"content":"%s"}}`, lsTS(time.Duration(2+i)*time.Second), filler))
	}
	st := lsRead(t, lsWrite(t, lines...))
	if !st.Capped || !st.ArmAt.IsZero() || st.TurnAt.IsZero() {
		t.Fatalf("state = %+v", st)
	}
}

func TestLoopStateParsesOnlyCandidateLines(t *testing.T) {
	lines := []string{
		lsArm(1*time.Minute, 5*time.Minute),
		`{this is not json at all`,
		`{"type":"assistant","timestamp":"` + lsTS(2*time.Minute) + `"} but scheduledFor broken {`,
		lsTurn(3*time.Minute, "assistant"),
		lsTurn(4*time.Minute, "user"),
	}
	before := loopParses.Load()
	st := lsRead(t, lsWrite(t, lines...))
	parses := loopParses.Load() - before
	if !st.ArmAt.Equal(ls0.Add(time.Minute)) || !st.TurnAt.Equal(ls0.Add(4*time.Minute)) {
		t.Fatalf("state = %+v", st)
	}
	// the turn line (1) + the malformed candidate (1) + the arm (1)
	if parses != 3 {
		t.Fatalf("parses = %d, want 3 (candidates + 1 turn line)", parses)
	}
}

func TestLoopReaderMemoSkipsUnchanged(t *testing.T) {
	p := lsWrite(t, lsArm(1*time.Minute, 5*time.Minute))
	r := NewLoopReader()
	if _, err := r.Read(p); err != nil {
		t.Fatal(err)
	}
	b0 := loopBytesRead.Load()
	if _, err := r.Read(p); err != nil {
		t.Fatal(err)
	}
	if loopBytesRead.Load() != b0 {
		t.Fatal("an unchanged file was read again")
	}
	// append: rescanned, the new fire seen
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(lsFire(5*time.Minute, "loop") + "\n")
	_ = f.Close()
	st, _ := r.Read(p)
	if st.LoopFireAt.IsZero() || loopBytesRead.Load() == b0 {
		t.Fatalf("append not rescanned: %+v", st)
	}
	// same size and mtime, new inode (rename over)
	fi, _ := os.Stat(p)
	b1 := loopBytesRead.Load()
	body, _ := os.ReadFile(p)
	repl := strings.Replace(string(body), `"loop"`, `"cron"`, 1)
	if len(repl) != len(body) {
		repl = strings.Replace(string(body), `"loop"`, `"lupo"`, 1)
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, []byte(repl), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	st, _ = r.Read(p)
	if loopBytesRead.Load() == b1 || !st.LoopFireAt.IsZero() {
		t.Fatalf("same size+mtime, new inode not rescanned")
	}
	// truncation
	b2 := loopBytesRead.Load()
	if err := os.WriteFile(p, []byte(lsTurn(time.Minute, "user")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ = r.Read(p)
	if loopBytesRead.Load() == b2 || !st.ArmAt.IsZero() {
		t.Fatalf("truncation not rescanned: %+v", st)
	}
}

func TestLoopStateMissingFile(t *testing.T) {
	_, err := NewLoopReader().Read(filepath.Join(t.TempDir(), "nope.jsonl"))
	if !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("err = %v", err)
	}
}

func TestActivityMtimeIncludesSubagents(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = os.Chtimes(p, now.Add(-30*time.Minute), now.Add(-30*time.Minute))
	m, err := ActivityMtime(p)
	if err != nil || now.Sub(m) < 29*time.Minute {
		t.Fatalf("no subagents: %v %v", m, err)
	}
	sub := filepath.Join(dir, "s", "subagents")
	_ = os.MkdirAll(sub, 0o700)
	sp := filepath.Join(sub, "agent-1.jsonl")
	_ = os.WriteFile(sp, []byte("y\n"), 0o600)
	_ = os.Chtimes(sp, now.Add(-time.Minute), now.Add(-time.Minute))
	m, err = ActivityMtime(p)
	if err != nil || now.Sub(m) > 2*time.Minute {
		t.Fatalf("with subagent: age %v err %v", now.Sub(m), err)
	}
}

package main

// Stop-hook tests that run runWaitHook without startRelayDaemon cannot reach
// the real ~/.config/pigtail: TestMain (main_test.go hermeticEnv) moves HOME to
// a temp dir, so the default draft dir (next to the socket, else
// ConfigDir()/handoff-draft) is hermetic. The tests below still pin
// PIGTAIL_HANDOFF_DRAFT_DIR on t.TempDir(); only
// TestStopHookDraftDirFollowsSocket removes the override. A test of
// another file that wants the hook off sets PIGTAIL_HANDOFF_DRAFT=off.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/handoffdraft"
	"github.com/aloysbr/pigtail/internal/testutil"
)

func draftStopPayload(session, tr, msg string, extra string) string {
	m, _ := json.Marshal(msg)
	return `{"session_id":"` + session + `","hook_event_name":"Stop","stop_hook_active":false,"cwd":"/w","last_assistant_message":` + string(m) + `,"transcript_path":"` + tr + `"` + extra + `}`
}

func draftEnv(dir string) func(string) string {
	return envOf(map[string]string{
		"CLAUDE_HOOK_LOG_DIR":       filepath.Join(dir, "hooklog"),
		"PIGTAIL_HANDOFF_DRAFT_DIR": filepath.Join(dir, "drafts"),
	})
}

func TestStopHookWritesDraft(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	tr := turnTranscript(t, "p-1", start)
	var out bytes.Buffer
	runWaitHook(strings.NewReader(draftStopPayload("s-draft", tr, "work\n**TL;DR**\nshipped it", "")), &out, draftEnv(dir))
	d, err := handoffdraft.Read(filepath.Join(dir, "drafts"), "s-draft")
	if err != nil {
		t.Fatalf("no draft: %v", err)
	}
	if d.TLDR != "shipped it" || d.TurnID != "p-1" || d.Cwd != "/w" {
		t.Fatalf("draft = %+v", d)
	}
	if !d.TurnAt.Equal(start) {
		t.Fatalf("TurnAt = %v, want the turn start %v", d.TurnAt, start)
	}
	if out.Len() != 0 {
		t.Fatalf("hook printed: %q", out.String())
	}
}

func TestStopHookDraftDirFollowsSocket(t *testing.T) {
	testutil.SerialOnly(t)
	dir := testutil.MkdirTemp(t, "ptdraft-")
	old := flagSocket
	flagSocket = filepath.Join(dir, "p.sock")
	t.Cleanup(func() { flagSocket = old })
	tr := turnTranscript(t, "p-1", time.Now().Add(-time.Minute))
	env := envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": t.TempDir()})
	runWaitHook(strings.NewReader(draftStopPayload("s-sock", tr, "x", "")), &bytes.Buffer{}, env)
	if _, err := handoffdraft.Read(filepath.Join(dir, "handoff-draft"), "s-sock"); err != nil {
		t.Fatalf("draft not next to the socket: %v", err)
	}
}

func TestStopHookDraftSkippedForSubagent(t *testing.T) {
	dir := t.TempDir()
	tr := turnTranscript(t, "p-1", time.Now().Add(-time.Minute))
	runWaitHook(strings.NewReader(draftStopPayload("s-sub", tr, "x", `,"agent_id":"a1"`)), &bytes.Buffer{}, draftEnv(dir))
	if _, err := os.Stat(filepath.Join(dir, "drafts", "s-sub.json")); !os.IsNotExist(err) {
		t.Fatalf("subagent wrote a draft: %v", err)
	}
}

func TestStopHookDraftSkippedForNonInteractive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	line := `{"type":"user","promptId":"p-1","entrypoint":"sdk-cli","timestamp":"2026-10-05T09:00:00Z","message":{"role":"user","content":"go"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	runWaitHook(strings.NewReader(draftStopPayload("s-p", p, "x", "")), &bytes.Buffer{}, draftEnv(dir))
	if _, err := os.Stat(filepath.Join(dir, "drafts", "s-p.json")); !os.IsNotExist(err) {
		t.Fatalf("claude -p wrote a draft: %v", err)
	}
}

func TestStopHookDraftOffSwitch(t *testing.T) {
	dir := t.TempDir()
	tr := turnTranscript(t, "p-1", time.Now().Add(-time.Minute))
	env := envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": t.TempDir(), "PIGTAIL_HANDOFF_DRAFT": "off", "PIGTAIL_HANDOFF_DRAFT_DIR": filepath.Join(dir, "d")})
	runWaitHook(strings.NewReader(draftStopPayload("s-off", tr, "x", "")), &bytes.Buffer{}, env)
	if _, err := os.Stat(filepath.Join(dir, "d", "s-off.json")); !os.IsNotExist(err) {
		t.Fatalf("off switch ignored: %v", err)
	}
}

func TestStopHookDraftNeverBlocksOnWriteError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": filepath.Join(dir, "log"), "PIGTAIL_HANDOFF_DRAFT_DIR": filepath.Join(blocker, "sub")})
	tr := turnTranscript(t, "p-1", time.Now().Add(-time.Minute))
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWaitHook(strings.NewReader(draftStopPayload("s-err", tr, "x", "")), &bytes.Buffer{}, env)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("hook blocked")
	}
	// the failure is visible in the hook's local log, shape only
	b, _ := os.ReadFile(filepath.Join(dir, "log", fmt.Sprintf("%s.log", "")))
	_ = b
	found := false
	_ = filepath.Walk(filepath.Join(dir, "log"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			c, _ := os.ReadFile(p)
			if strings.Contains(string(c), "handoff-draft") {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Fatal("write failure left no line in the hook log")
	}
}

func TestStopHookDraftOverwritesPreviousTurn(t *testing.T) {
	dir := t.TempDir()
	env := draftEnv(dir)
	tr1 := turnTranscript(t, "p-1", time.Now().Add(-time.Hour))
	runWaitHook(strings.NewReader(draftStopPayload("s-ow", tr1, "**TL;DR**\none", "")), &bytes.Buffer{}, env)
	tr2 := turnTranscript(t, "p-2", time.Now().Add(-time.Minute))
	runWaitHook(strings.NewReader(draftStopPayload("s-ow", tr2, "**TL;DR**\ntwo", "")), &bytes.Buffer{}, env)
	d, err := handoffdraft.Read(filepath.Join(dir, "drafts"), "s-ow")
	if err != nil || d.TurnID != "p-2" || d.TLDR != "two" {
		t.Fatalf("draft = %+v err=%v", d, err)
	}
}

// longTurnTranscript: the prompt of p-1 at 09:00, a heavy line, then a tool
// result of the same turn at 09:30. ReadLastTurn's 256 KiB tail only sees the
// tool result: its Start is late.
func longTurnTranscript(t *testing.T, heavyBytes int) (path string, first, late time.Time) {
	t.Helper()
	first = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	late = first.Add(30 * time.Minute)
	p := filepath.Join(t.TempDir(), "long.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintf(f, `{"type":"user","promptId":"p-1","entrypoint":"cli","timestamp":%q,"message":{"role":"user","content":"go"}}`+"\n", first.Format(time.RFC3339))
	chunk := strings.Repeat("x", 1<<20)
	for written := 0; written < heavyBytes; written += len(chunk) + 60 {
		fmt.Fprintf(f, `{"type":"system","subtype":"api-request-blob","content":"%s"}`+"\n", chunk)
	}
	fmt.Fprintf(f, `{"type":"user","promptId":"p-1","entrypoint":"cli","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","content":"x"}]}}`+"\n", late.Format(time.RFC3339))
	return p, first, late
}

func TestStopHookDraftTurnAtFromWholeTranscript(t *testing.T) {
	dir := t.TempDir()
	p, first, late := longTurnTranscript(t, 600<<10)
	// positive control: the tail reader really gives a late Start here
	turn, err := claudesession.ReadLastTurn(p)
	if err != nil || !turn.Start.Equal(late) {
		t.Fatalf("control: ReadLastTurn.Start = %v (err %v), want the late %v", turn.Start, err, late)
	}
	runWaitHook(strings.NewReader(draftStopPayload("s-long", p, "x", "")), &bytes.Buffer{}, draftEnv(dir))
	d, err := handoffdraft.Read(filepath.Join(dir, "drafts"), "s-long")
	if err != nil {
		t.Fatal(err)
	}
	if !d.TurnAt.Equal(first) || !d.TurnAt.Before(turn.Start) {
		t.Fatalf("TurnAt = %v, want first user entry %v (before %v)", d.TurnAt, first, turn.Start)
	}
}

func TestStopHookDraftReusesTurnAtSameTurn(t *testing.T) {
	dir := t.TempDir()
	env := draftEnv(dir)
	p, first, _ := longTurnTranscript(t, 600<<10)
	runWaitHook(strings.NewReader(draftStopPayload("s-re", p, "x", "")), &bytes.Buffer{}, env)
	// the second Stop of the same turn must not rescan the whole transcript:
	// a transcript reduced to the tool result alone would give the late start.
	late := first.Add(30 * time.Minute)
	short := filepath.Join(t.TempDir(), "short.jsonl")
	line := fmt.Sprintf(`{"type":"user","promptId":"p-1","entrypoint":"cli","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","content":"x"}]}}`+"\n", late.Format(time.RFC3339))
	if err := os.WriteFile(short, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	runWaitHook(strings.NewReader(draftStopPayload("s-re", short, "y", "")), &bytes.Buffer{}, env)
	d, err := handoffdraft.Read(filepath.Join(dir, "drafts"), "s-re")
	if err != nil || !d.TurnAt.Equal(first) {
		t.Fatalf("TurnAt = %v err=%v, want the first scan's %v", d.TurnAt, err, first)
	}
}

func TestReadBranchWorktreeGitFile(t *testing.T) {
	root := t.TempDir()
	gitdir := filepath.Join(root, "main", ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/feat/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	sub := filepath.Join(wt, "a", "b")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readBranch(sub); got != "feat/x" {
		t.Fatalf("branch = %q", got)
	}
	// plain repo, and detached HEAD gives ""
	plain := filepath.Join(root, "plain")
	_ = os.MkdirAll(filepath.Join(plain, ".git"), 0o700)
	_ = os.WriteFile(filepath.Join(plain, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	if got := readBranch(plain); got != "main" {
		t.Fatalf("plain = %q", got)
	}
	_ = os.WriteFile(filepath.Join(plain, ".git", "HEAD"), []byte("0123456789abcdef\n"), 0o600)
	if got := readBranch(plain); got != "" {
		t.Fatalf("detached = %q", got)
	}
}

// scanBudget is generous: the hook runs once per turn, off the user's path.
const scanBudget = 500 * time.Millisecond

// scanTranscript writes a transcript of about mib MiB that forces a full scan:
// older turns (other promptIds, heavy lines between them) fill the file and the
// target turn's only user entry is the LAST line, so TurnStartOf cannot return
// before it has read everything. Real transcripts reach 110 MiB.
func scanTranscript(t *testing.T, mib int) (path string, first time.Time, size int64) {
	t.Helper()
	first = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	path = filepath.Join(t.TempDir(), "scan.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	chunk := strings.Repeat("x", 1<<20)
	for n := 0; n < mib; n++ {
		fmt.Fprintf(f, `{"type":"user","promptId":"old-%d","entrypoint":"cli","timestamp":%q,"message":{"role":"user","content":"older turn"}}`+"\n", n, first.Add(-time.Hour).Format(time.RFC3339))
		fmt.Fprintf(f, `{"type":"system","subtype":"api-request-blob","content":"%s"}`+"\n", chunk)
	}
	fmt.Fprintf(f, `{"type":"user","promptId":"p-1","entrypoint":"cli","timestamp":%q,"message":{"role":"user","content":"go"}}`+"\n", first.Format(time.RFC3339))
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return path, first, st.Size()
}

// Rough safety limits, not a measurement (plan task 3 step 5). The scan is over
// the whole file: the target turn sits on the last line.
func TestStopHookDraftBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 110 MiB transcript")
	}
	dir := t.TempDir()
	d := handoffdraft.Draft{SessionID: "s-b", TurnID: "t", TurnAt: time.Now(), Cwd: "/w", TLDR: strings.Repeat("é", 2000)}
	t0 := time.Now()
	if err := handoffdraft.Write(filepath.Join(dir, "d"), d); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(t0); el > 50*time.Millisecond {
		t.Fatalf("draft write took %v", el)
	}
	p, first, size := scanTranscript(t, 110)
	if size < 110<<20 {
		t.Fatalf("control: transcript is %d bytes, want >= 110 MiB", size)
	}
	t0 = time.Now()
	got, ok := claudesession.TurnStartOf(p, "p-1")
	el := time.Since(t0)
	if !ok || !got.Equal(first) {
		t.Fatalf("TurnStartOf = %v %v: the last line was not reached (full scan of %d bytes)", got, ok, size)
	}
	t.Logf("TurnStartOf full scan of %d MiB: %v", size>>20, el)
	if el > scanBudget {
		t.Fatalf("TurnStartOf full scan of %d MiB took %v (budget %v)", size>>20, el, time.Duration(scanBudget))
	}
}

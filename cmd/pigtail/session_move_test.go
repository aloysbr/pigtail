package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/testutil"
)

func TestValidateMoveRejectsCrossScope(t *testing.T) {
	t.Parallel()
	src := claudeprofile.Cell{ID: "t", Scope: "globex", ConfigDir: "/a"}
	dst := claudeprofile.Cell{ID: "p", Scope: "perso", ConfigDir: "/b"}
	if err := validateMove(src, dst); err == nil {
		t.Fatal("expected cross-scope move to be refused")
	}
}

func TestValidateMoveRejectsSameCell(t *testing.T) {
	t.Parallel()
	c := claudeprofile.Cell{ID: "t", Scope: "globex", ConfigDir: "/a"}
	if err := validateMove(c, c); err == nil {
		t.Fatal("expected a move onto the same cell to be refused")
	}
}

func TestValidateMoveAcceptsSameScope(t *testing.T) {
	t.Parallel()
	src := claudeprofile.Cell{ID: "t", Scope: "globex", ConfigDir: "/a"}
	dst := claudeprofile.Cell{ID: "mt", Scope: "globex", ConfigDir: "/b"}
	if err := validateMove(src, dst); err != nil {
		t.Fatalf("same-scope move refused: %v", err)
	}
}

func TestAuthenticated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if authenticated(dir) {
		t.Fatal("a config dir with no .claude.json is not authenticated")
	}
	body := `{"oauthAccount":{"emailAddress":"a@b.c"}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !authenticated(dir) {
		t.Fatal("expected authenticated")
	}

	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, ".claude.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if authenticated(empty) {
		t.Fatal("a config dir without oauthAccount is not authenticated")
	}
}

// TestValidSessionIDRejectsGlobMetacharacters covers decision 1: --session
// feeds claudesession.Find, which builds a glob pattern from the id. A value
// containing glob metacharacters would silently change match semantics, so it
// must be rejected before it reaches any lookup. Only a canonical 36-char
// lowercase UUID (8-4-4-4-12 hex) is accepted.
func TestValidSessionIDRejectsGlobMetacharacters(t *testing.T) {
	t.Parallel()
	if validSessionID("1bb0480e-a291-4dab-bb69-8190b97b682*") {
		t.Fatal("expected a value with a glob metacharacter to be rejected")
	}
	if validSessionID("1bb0480e-a291-4dab-bb69-8190b97b68?4") {
		t.Fatal("expected a value with a glob metacharacter to be rejected")
	}
	if !validSessionID("1bb0480e-a291-4dab-bb69-8190b97b6824") {
		t.Fatal("expected a canonical UUID to be accepted")
	}
	if validSessionID("1BB0480E-A291-4DAB-BB69-8190B97B6824") {
		t.Fatal("expected an uppercase UUID to be rejected (canonical form is lowercase)")
	}
}

// TestSessionMoveRejectsInvalidSessionFlag exercises the validation at the
// command boundary: a malformed --session must be refused before the command
// does any other work (claudeprofile.Load, cmux calls), which is why this
// works without a real cmux binary or pigtail config dir present.
func TestSessionMoveRejectsInvalidSessionFlag(t *testing.T) {
	t.Parallel()
	cmd := sessionMoveCmd()
	cmd.SetArgs([]string{"mt", "--session", "not-a-uuid*glob"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for a malformed --session value")
	}
	if !strings.Contains(err.Error(), "invalid --session") {
		t.Fatalf("error = %v, want it to mention invalid --session", err)
	}
}

// TestSessionMoveRejectsFromWithoutAll covers the "minor" fix: --from only
// means anything alongside --all, so leaving it silently ignored on its own
// would let a typo'd invocation move the wrong thing without any signal.
func TestSessionMoveRejectsFromWithoutAll(t *testing.T) {
	t.Parallel()
	cmd := sessionMoveCmd()
	cmd.SetArgs([]string{"mt", "--from", "t"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for --from without --all")
	}
	if !strings.Contains(err.Error(), "--from requires --all") {
		t.Fatalf("error = %v, want it to mention --from requires --all", err)
	}
}

// TestTrackedInventoryReportsUnreadableSurfaces covers decision 2: session
// move must not act on a partial picture in silence. trackedInventory is the
// helper both listSessions and sessionMoveCmd share; it must report which
// surfaces could not be read (a real error, not merely "no binding") so the
// caller can warn instead of quietly skipping a session that should move.
func TestTrackedInventoryReportsUnreadableSurfaces(t *testing.T) {
	t.Parallel()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	tbl := newTableWithTranscript(t, id)

	binding := func(ref string) (string, error) {
		switch ref {
		case "surface:1":
			return "", fmt.Errorf("cmux surface resume show surface:1: exit status 1 (output: unknown surface)")
		case "surface:2":
			return realBindingFor(id), nil
		default:
			t.Fatalf("unexpected ref %q", ref)
			return "", nil
		}
	}

	rows, unreadable, ambiguous := trackedInventory([]string{"surface:1", "surface:2"}, binding, tbl)
	if len(rows) != 1 || rows[0].SessionID != id {
		t.Fatalf("rows = %+v, want the one readable row", rows)
	}
	if len(unreadable) != 1 || unreadable[0] != "surface:1" {
		t.Fatalf("unreadable = %v, want [surface:1]", unreadable)
	}
	if len(ambiguous) != 0 {
		t.Fatalf("ambiguous = %v, want none", ambiguous)
	}
}

// newMoveFixture builds a two-cell claudeprofile.Table ("t" source, "mt"
// destination), seeds a real transcript for id under the "-w" slug in the
// source config dir, and returns the table, both cells, the sessionRow
// moveOne would see for it, and the on-disk transcript path. Shared by every
// moveOne/moveDetached test below so the fixture setup — which is
// deliberately real filesystem state, since moveOne's pre-flight and
// belt-and-braces checks stat real files — isn't duplicated per test.
func newMoveFixture(t *testing.T, id string) (tbl *claudeprofile.Table, src, dst claudeprofile.Cell, row sessionRow, transcriptPath string) {
	t.Helper()
	cfgDir := t.TempDir()
	srcDir, dstDir := t.TempDir(), t.TempDir()
	body := "cells:\n  t: { config_dir: " + srcDir + ", scope: globex }\n  mt: { config_dir: " + dstDir + ", scope: globex }\n"
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	seedDir := filepath.Join(srcDir, "projects", "-w")
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcriptPath = filepath.Join(seedDir, id+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, ok := tbl.Cell("t")
	if !ok {
		t.Fatal("cell t not found")
	}
	dst, ok = tbl.Cell("mt")
	if !ok {
		t.Fatal("cell mt not found")
	}
	row = sessionRow{SurfaceRef: "surface:1", SessionID: id, Cwd: "/w", CellID: "t", ConfigDir: srcDir, Slug: "-w"}
	return tbl, src, dst, row, transcriptPath
}

// fakeProcs is the process table the move tests see, in place of ps and the
// sessions/<pid>.json files. A test registers the Claude serving a session
// under a config dir (serve), or any other Claude process (spawn); onList
// runs before every listing, which is how a test plays the pane coming back.
type fakeProc struct{ args, env string }

type fakeProcs struct {
	procs   map[int]fakeProc
	native  map[string]int // cfg+"|"+sid → pid, what findNative answers
	next    int
	listErr error
	onList  func(f *fakeProcs)
}

func installFakeProcs() *fakeProcs {
	f := &fakeProcs{procs: map[int]fakeProc{}, native: map[string]int{}, next: 5000}
	findNative = func(cfg, sid string) (claudesession.Native, bool) {
		pid, ok := f.native[cfg+"|"+sid]
		if !ok {
			return claudesession.Native{}, false
		}
		return claudesession.Native{PID: pid, SessionID: sid, ConfigDir: cfg}, true
	}
	pidAlive = func(pid int) bool { _, ok := f.procs[pid]; return ok }
	processEnviron = func(pid int) (string, error) {
		p, ok := f.procs[pid]
		if !ok {
			return "", os.ErrNotExist
		}
		return p.args + " " + p.env, nil
	}
	claudeProcs = func() ([]claudesession.Proc, error) {
		if f.onList != nil {
			f.onList(f)
		}
		if f.listErr != nil {
			return nil, f.listErr
		}
		var out []claudesession.Proc
		for pid, p := range f.procs {
			out = append(out, claudesession.Proc{PID: pid, Args: p.args})
		}
		return out, nil
	}
	return f
}

func (f *fakeProcs) spawn(args, env string) int {
	f.next++
	f.procs[f.next] = fakeProc{args: args, env: env}
	return f.next
}

// serve registers the Claude serving sid under cfg as a bare `c <cell>`
// launch: no session id in argv, so only its pid tells it is still there.
func (f *fakeProcs) serve(cfg, sid, env string) int {
	pid := f.spawn("/bin/claude", "CLAUDE_CONFIG_DIR="+cfg+" "+env)
	f.native[cfg+"|"+sid] = pid
	return pid
}

// comesBack spawns the relaunched Claude: argv resumes sid, environment as given.
func (f *fakeProcs) comesBack(sid, env string) int {
	return f.spawn("/bin/claude --settings /tmp/s --resume "+sid+" --permission-mode auto", env)
}

// interruptCapture replaces interruptNotify and records what moveOne asked it
// to run on Ctrl-C.
type interruptCapture struct {
	cleanup func()
	stops   int
}

// stubMoveOps replaces moveOne/moveDetached's injected operations
// (sendLine, waitExit, runningProbe, copyTranscript, setResumeBinding,
// removeSource) with no-op successes, and restores the real ones on
// cleanup. Tests then override individual vars to record calls or inject a
// specific failure.
func stubMoveOps(t *testing.T) (*fakeProcs, *interruptCapture) {
	t.Helper()
	// Real moves take the per-session lock: keep its files out of the real state dir.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	savedSendLine, savedWaitExit, savedRunning := sendLine, waitExit, runningProbe
	savedCopy, savedSetBinding, savedRemove := copyTranscript, setResumeBinding, removeSource
	savedReadScreen, savedSendKey := readScreen, sendKey
	savedWrapper := claudeWrapper
	savedFind, savedAlive, savedEnv, savedProcs := findNative, pidAlive, processEnviron, claudeProcs
	savedQT, savedQI, savedVT, savedVI := quitTimeout, quitInterval, relaunchVerifyTimeout, relaunchVerifyInterval
	savedInterrupt, savedConfirm := interruptNotify, confirmRelaunch
	savedPreTrust := movePreTrust
	t.Cleanup(func() {
		movePreTrust = savedPreTrust
		findNative, pidAlive, processEnviron, claudeProcs = savedFind, savedAlive, savedEnv, savedProcs
		quitTimeout, quitInterval, relaunchVerifyTimeout, relaunchVerifyInterval = savedQT, savedQI, savedVT, savedVI
		interruptNotify, confirmRelaunch = savedInterrupt, savedConfirm
		sendLine, waitExit, runningProbe = savedSendLine, savedWaitExit, savedRunning
		copyTranscript, setResumeBinding, removeSource = savedCopy, savedSetBinding, savedRemove
		readScreen, sendKey = savedReadScreen, savedSendKey
		claudeWrapper = savedWrapper
	})

	claudeWrapper = func() (string, error) { return "/cmux/bin/cmux-claude-wrapper", nil }

	sendLine = func(string, string) error { return nil }
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error { return nil }
	runningProbe = func(string) bool { return false }
	copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		return claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}, nil
	}
	setResumeBinding = func(string, string, string, string, string) error { return nil }
	removeSource = func(claudesession.Transcript) error { return nil }
	readScreen = func(string) (string, error) { return "", nil }
	sendKey = func(string, string) error { return nil }
	movePreTrust = func(string, string) claudeconfig.Result { return claudeconfig.Result{} }

	// Process side: an empty table (no Claude found for the session, so the
	// typed-line route), short deadlines, and a relaunch taken as confirmed —
	// the tests of the post-relaunch check put verifyRelaunch back.
	quitTimeout, quitInterval = 30*time.Millisecond, 5*time.Millisecond
	relaunchVerifyTimeout, relaunchVerifyInterval = 100*time.Millisecond, 5*time.Millisecond
	confirmRelaunch = func(relaunchCheck) (int, error) { return 4343, nil }
	ic := &interruptCapture{}
	interruptNotify = func(cleanup func()) func() {
		ic.cleanup = cleanup
		return func() { ic.stops++ }
	}
	return installFakeProcs(), ic
}

// TestMoveOneDryRunHasNoSideEffects covers decision 3: a dry run must not
// quit, copy, relaunch or delete anything. It asserts on the filesystem
// directly (source transcript untouched, nothing copied to the destination)
// rather than trusting the returned error alone.
func TestMoveOneDryRunHasNoSideEffects(t *testing.T) {
	t.Parallel()
	tbl, _, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, true, false); err != nil {
		t.Fatalf("dry-run moveOne returned error: %v", err)
	}

	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("source transcript missing after dry run: %v", err)
	}
	dstMatches, _ := filepath.Glob(filepath.Join(dst.ConfigDir, "projects", "*", row.SessionID+".jsonl"))
	if len(dstMatches) != 0 {
		t.Fatalf("dry run copied the transcript: %v", dstMatches)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry run output = %q, want a description of what would happen", out.String())
	}
}

// TestMoveDetachedDryRunHasNoSideEffects covers CRITICAL 2: the detached path
// took no dryRun parameter at all, so `--dry-run` on it silently performed a
// real copy-and-delete. Mirrors TestMoveOneDryRunHasNoSideEffects.
func TestMoveDetachedDryRunHasNoSideEffects(t *testing.T) {
	// Stubbed because the dry run now runs the running probe before printing:
	// the real one shells out to ps, which has nothing to do with what this
	// test asserts (that nothing on disk moved).
	stubMoveOps(t)
	_, src, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	tr := claudesession.Transcript{ID: row.SessionID, ConfigDir: src.ConfigDir, Slug: row.Slug}

	var out, warn bytes.Buffer
	if err := moveDetached(&out, &warn, tr, src, dst, true, false); err != nil {
		t.Fatalf("dry-run moveDetached returned error: %v", err)
	}

	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("source transcript missing after dry run: %v", err)
	}
	dstMatches, _ := filepath.Glob(filepath.Join(dst.ConfigDir, "projects", "*", row.SessionID+".jsonl"))
	if len(dstMatches) != 0 {
		t.Fatalf("dry run copied the transcript: %v", dstMatches)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry run output = %q, want a description of what would happen", out.String())
	}
}

// TestMoveOneDryRunReportsAConflictInsteadOfPromisingAMove is what makes a
// dry run worth running: it used to print its canned line and return BEFORE
// the source-exists and destination-conflict checks — that is, before
// everything that actually makes a move fail — so it happily described a move
// that could not happen. This branch has never met a real cmux, so the first
// manual test is exactly where an honest dry run pays for itself.
func TestMoveOneDryRunReportsAConflictInsteadOfPromisingAMove(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	dstSeedDir := filepath.Join(dst.ConfigDir, "projects", "-w")
	if err := os.MkdirAll(dstSeedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstSeedDir, row.SessionID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, warn bytes.Buffer
	err := moveOne(&out, &warn, row, dst, tbl, true, false)
	if err == nil {
		t.Fatal("a dry run must report the destination conflict, not describe a move that would fail")
	}
	if !strings.Contains(err.Error(), "target already has a transcript") {
		t.Fatalf("err = %v, want the destination conflict", err)
	}
	if strings.Contains(out.String(), "would") {
		t.Fatalf("out = %q, a dry run that cannot happen must not describe it happening", out.String())
	}
}

// TestMoveDetachedDryRunRefusesARunningSession is the same ruling on the
// detached path, where a running session is the other reason the move is
// refused: the dry run must run the probe, not promise a move the real run
// would reject.
func TestMoveDetachedDryRunRefusesARunningSession(t *testing.T) {
	stubMoveOps(t)
	_, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	tr := claudesession.Transcript{ID: row.SessionID, ConfigDir: src.ConfigDir, Slug: row.Slug}

	runningProbe = func(string) bool { return true }

	var out, warn bytes.Buffer
	err := moveDetached(&out, &warn, tr, src, dst, true, false)
	if err == nil {
		t.Fatal("a dry run must report that the session is still running")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v, want it to say the session is still running", err)
	}
	if strings.Contains(out.String(), "would") {
		t.Fatalf("out = %q, a dry run that cannot happen must not describe it happening", out.String())
	}
}

// TestMoveDetachedPreflightFailsBeforeRunningCheck covers NEW-1's second half:
// moveDetached now gets the same destination pre-flight moveOne has. With a
// conflicting destination transcript already present, it must fail before
// ever calling runningProbe — a doomed move should not need a process check
// (or, on the real path, a running-session refusal) to tell the user why.
func TestMoveDetachedPreflightFailsBeforeRunningCheck(t *testing.T) {
	stubMoveOps(t)
	_, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	tr := claudesession.Transcript{ID: row.SessionID, ConfigDir: src.ConfigDir, Slug: row.Slug}

	dstSeedDir := filepath.Join(dst.ConfigDir, "projects", "-w")
	if err := os.MkdirAll(dstSeedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstSeedDir, row.SessionID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runningCalled := false
	runningProbe = func(string) bool { runningCalled = true; return false }

	var out, warn bytes.Buffer
	if err := moveDetached(&out, &warn, tr, src, dst, false, false); err == nil {
		t.Fatal("expected an error for a pre-existing destination transcript")
	}
	if runningCalled {
		t.Fatal("moveDetached must not check whether the session is running before the pre-flight checks pass")
	}
}

// TestMoveDetachedKeepsSourceWhenItChangedDuringCopy covers NEW-1's
// belt-and-braces half for the detached path: mirrors
// TestMoveOneKeepsSourceWhenItChangedDuringCopy, since this is the weakest
// path in the feature (no pane to observe) and must not be a lighter version
// of moveOne's protections.
func TestMoveDetachedKeepsSourceWhenItChangedDuringCopy(t *testing.T) {
	stubMoveOps(t)
	_, src, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	tr := claudesession.Transcript{ID: row.SessionID, ConfigDir: src.ConfigDir, Slug: row.Slug}

	removeCalled := false
	removeSource = func(claudesession.Transcript) error { removeCalled = true; return nil }
	copyTranscript = func(s claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		if err := os.WriteFile(transcriptPath, []byte("{}\n{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return claudesession.Transcript{ID: s.ID, ConfigDir: dstConfigDir, Slug: s.Slug}, nil
	}

	var out, warn bytes.Buffer
	if err := moveDetached(&out, &warn, tr, src, dst, false, false); err == nil {
		t.Fatal("expected an error: the source changed during the copy")
	}
	if removeCalled {
		t.Fatal("removeSource must not be called when the source changed during the copy")
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("source transcript should still be on disk: %v", err)
	}
	if !strings.Contains(warn.String(), "kept") {
		t.Fatalf("warn should explain the source was kept, got %q", warn.String())
	}
}

// TestMoveOneRemovesSourceLastOnSuccess is the ordering test IMPORTANT 3
// asked for: without it, an implementation that called removeSource right
// after the quit SendLine would pass every other test in this file. It
// records the actual call sequence and asserts removeSource is both called
// and last, strictly after a successful copy.
func TestMoveOneRemovesSourceLastOnSuccess(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	var calls []string
	sendLine = func(ref, line string) error { calls = append(calls, "sendLine:"+line); return nil }
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		calls = append(calls, "waitExit")
		return nil
	}
	copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		calls = append(calls, "copy")
		return claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}, nil
	}
	setResumeBinding = func(string, string, string, string, string) error {
		calls = append(calls, "setResumeBinding")
		return nil
	}
	removeSource = func(claudesession.Transcript) error {
		calls = append(calls, "removeSource")
		return nil
	}

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v", err)
	}

	if len(calls) == 0 || calls[len(calls)-1] != "removeSource" {
		t.Fatalf("calls = %v, want removeSource last", calls)
	}
	copyIdx, removeIdx := -1, -1
	for i, c := range calls {
		if c == "copy" {
			copyIdx = i
		}
		if c == "removeSource" {
			removeIdx = i
		}
	}
	if copyIdx == -1 || removeIdx == -1 || copyIdx > removeIdx {
		t.Fatalf("calls = %v, want copy strictly before removeSource", calls)
	}
}

// TestMoveOneNeverRemovesSourceOnEarlyFailure injects a failure at each step
// of the ordered sequence in turn (quit, wait, copy, relaunch, rebind) and
// asserts removeSource is never reached and the source file survives
// untouched on disk — the property the whole "delete last" design exists to
// guarantee. The rebind case (CRITICAL 3, fix round 2) is the one step whose
// failure previously fell through to RemoveSource anyway — a pane whose
// binding still named the source account, pointing at a transcript that no
// longer existed there.
func TestMoveOneNeverRemovesSourceOnEarlyFailure(t *testing.T) {
	cases := []struct {
		name  string
		setup func()
	}{
		{
			name: "quit fails",
			setup: func() {
				sendLine = func(string, string) error { return errors.New("quit boom") }
			},
		},
		{
			name: "wait fails",
			setup: func() {
				waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
					return errors.New("still running")
				}
			},
		},
		{
			name: "copy fails",
			setup: func() {
				copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
					return claudesession.Transcript{}, errors.New("copy boom")
				}
			},
		},
		{
			name: "relaunch fails",
			setup: func() {
				calls := 0
				sendLine = func(string, string) error {
					calls++
					if calls == 1 {
						return nil // /exit succeeds
					}
					return errors.New("relaunch boom") // relaunch fails
				}
			},
		},
		{
			name: "resume binding rewrite fails",
			setup: func() {
				setResumeBinding = func(string, string, string, string, string) error {
					return errors.New("cmux surface resume set: unknown surface")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubMoveOps(t)
			removeCalled := false
			removeSource = func(claudesession.Transcript) error { removeCalled = true; return nil }
			tc.setup()

			tbl, _, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
			var out, warn bytes.Buffer
			if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
				t.Fatal("expected an error")
			}
			if removeCalled {
				t.Fatal("removeSource must not be called when an earlier step fails")
			}
			if _, err := os.Stat(transcriptPath); err != nil {
				t.Fatalf("source transcript should still be on disk: %v", err)
			}
		})
	}
}

// TestMoveOneKeepsSourceOnRebindFailureEvenWithKeepSource is CRITICAL 3's
// specific requirement: the source must be kept regardless of --keep-source
// when the resume binding cannot be rewritten (as opposed to the normal
// --keep-source path, which is a deliberate choice made only after a fully
// successful move). Also asserts the failure surfaces plainly to the caller
// (moveOne returns a non-nil error) rather than the old silent-warning
// behaviour, and — per NEW-2/NEW-3 (fix round 3) — that the guidance lands
// on stderr (warn), names the manual `cmux surface resume set` fix command,
// and tells the user to delete the source transcript by hand afterwards
// (rather than the old, contradictory "re-run with --keep-source", which
// both said the opposite of what that flag does and could never work: the
// pre-flight check would refuse the re-run with "target already has a
// transcript" since the copy already landed).
func TestMoveOneKeepsSourceOnRebindFailureEvenWithKeepSource(t *testing.T) {
	stubMoveOps(t)
	removeCalled := false
	removeSource = func(claudesession.Transcript) error { removeCalled = true; return nil }
	setResumeBinding = func(string, string, string, string, string) error {
		return errors.New("cmux surface resume set: unknown surface")
	}

	tbl, _, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	var out, warn bytes.Buffer
	// keepSource=true here: even the flag that would normally skip deletion
	// must not be what's "deciding" this — the rebind failure alone must
	// force keeping the source, and must be reported as a failure either way.
	err := moveOne(&out, &warn, row, dst, tbl, false, true)
	if err == nil {
		t.Fatal("expected moveOne to report the rebind failure as an error")
	}
	if removeCalled {
		t.Fatal("removeSource must not be called when the resume binding could not be rewritten")
	}
	if _, statErr := os.Stat(transcriptPath); statErr != nil {
		t.Fatalf("source transcript should still be on disk: %v", statErr)
	}
	if out.String() != "" && strings.Contains(out.String(), "✗") {
		t.Fatalf("the failure block must not land on stdout, got %q", out.String())
	}
	if !strings.Contains(warn.String(), "cmux surface resume set --surface surface:1") {
		t.Fatalf("warn should print the manual fix command, got %q", warn.String())
	}
	if !strings.Contains(warn.String(), "kept on purpose") {
		t.Fatalf("warn should explain the source was kept on purpose, got %q", warn.String())
	}
	if strings.Contains(warn.String(), "re-run with --keep-source") {
		t.Fatalf("warn should not tell the user to re-run (the pre-flight check would refuse it), got %q", warn.String())
	}
	if !strings.Contains(warn.String(), "delete the source transcript by hand: "+transcriptPath) {
		t.Fatalf("warn should tell the user to delete the source by hand and print its exact path, got %q", warn.String())
	}
}

// TestMoveOneKeepsSourceWhenItChangedDuringCopy is CRITICAL 1's belt-and-
// braces layer: even though WaitExit reported the process gone, if the
// source transcript's size changed between the stat taken right before Copy
// and the stat taken right before RemoveSource, the source must be kept
// rather than deleted. The stubbed copyTranscript simulates a process that
// is still (somehow) writing mid-copy by mutating the real file as a side
// effect.
//
// Per NEW-3 (fix round 3): this is no longer a silent success. The stat
// guard tripping means the primary defence (Running/WaitExit) got it wrong —
// exactly when the user needs to know — so moveOne must now return a non-nil
// error and report on stderr (warn), not print a warning to stdout and
// return nil.
func TestMoveOneKeepsSourceWhenItChangedDuringCopy(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	removeCalled := false
	removeSource = func(claudesession.Transcript) error { removeCalled = true; return nil }
	copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		if err := os.WriteFile(transcriptPath, []byte("{}\n{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}, nil
	}

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected moveOne to report the stat-guard trip as an error, not a silent success")
	}
	if removeCalled {
		t.Fatal("removeSource must not be called when the source changed during the copy")
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("source transcript should still be on disk: %v", err)
	}
	if strings.Contains(out.String(), "kept") {
		t.Fatalf("the stat-guard message must not land on stdout, got %q", out.String())
	}
	if !strings.Contains(warn.String(), "kept") {
		t.Fatalf("warn should explain the source was kept, got %q", warn.String())
	}
}

// TestMoveOneKeepsSourceWhenOnlyMtimeChangedDuringCopy extends the guard's
// coverage to the case a size-only check would miss: a same-size rewrite of
// the transcript (exactly what the mtime half of the check exists to catch).
func TestMoveOneKeepsSourceWhenOnlyMtimeChangedDuringCopy(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	original, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	removeCalled := false
	removeSource = func(claudesession.Transcript) error { removeCalled = true; return nil }
	copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		// Rewrite with content of the exact same length (same size, so a
		// size-only check would miss this), then bump the mtime forward —
		// exactly what a real in-place rewrite between the two stats does.
		if err := os.WriteFile(transcriptPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(transcriptPath, future, future); err != nil {
			t.Fatal(err)
		}
		return claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}, nil
	}

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected an error: same size, different mtime must still trip the guard")
	}
	if removeCalled {
		t.Fatal("removeSource must not be called when only the mtime changed during the copy")
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("source transcript should still be on disk: %v", err)
	}
	if !strings.Contains(warn.String(), "kept") {
		t.Fatalf("warn should explain the source was kept, got %q", warn.String())
	}
}

// TestMoveAllAggregatesStatGuardFailure covers NEW-3's other half: the stat
// guard's non-nil error must propagate all the way through moveAll's
// aggregation, the same as any other moveOne failure, so a scripted --all
// run does not report total success ($?==0) while quietly leaving a
// duplicate transcript behind.
func TestMoveAllAggregatesStatGuardFailure(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		if err := os.WriteFile(transcriptPath, []byte("{}\n{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}, nil
	}

	var out, warn bytes.Buffer
	err := moveAll(&out, &warn, []sessionRow{row}, dst, tbl, false, false)
	if err == nil {
		t.Fatal("expected moveAll to report the stat-guard trip as a batch failure")
	}
	if !strings.Contains(err.Error(), "1 of 1") {
		t.Fatalf("error = %v, want it to report 1 of 1 session(s) failed", err)
	}
}

// TestMoveOnePreflightFailsBeforeQuitting covers IMPORTANT 1: with a
// destination transcript already present, moveOne must fail before the first
// SendLine — the session is never quit for a move that was going to fail
// anyway.
func TestMoveOnePreflightFailsBeforeQuitting(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	// Seed a conflicting transcript at the destination.
	dstSeedDir := filepath.Join(dst.ConfigDir, "projects", "-w")
	if err := os.MkdirAll(dstSeedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstSeedDir, row.SessionID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sendLineCalled := false
	sendLine = func(string, string) error { sendLineCalled = true; return nil }

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected an error for a pre-existing destination transcript")
	}
	if sendLineCalled {
		t.Fatal("moveOne must not quit the session before the pre-flight checks pass")
	}
}

// TestMoveAllContinuesPastFailureAndAggregatesError covers the --all loop
// (extracted into moveAll so it's reachable through the same moveOne seam,
// without a real cmux CLI or claudeprofile.Load — both of those happen
// earlier in RunE, before targets are ever selected). Two targets share the
// source config dir; copyTranscript is stubbed to fail only for the first
// one. Asserts: an aggregate error is returned, the second target is still
// fully processed (copyTranscript is called for both), and the failure is
// named on the warn stream.
func TestMoveAllContinuesPastFailureAndAggregatesError(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, rowA, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	const idB = "2cc0480e-a291-4dab-bb69-8190b97b6825"
	seedDir := filepath.Join(rowA.ConfigDir, "projects", "-w")
	if err := os.WriteFile(filepath.Join(seedDir, idB+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rowB := rowA
	rowB.SessionID = idB
	rowB.SurfaceRef = "surface:2"

	copyCalls := 0
	copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		copyCalls++
		if src.ID == rowA.SessionID {
			return claudesession.Transcript{}, errors.New("copy boom for A")
		}
		return claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}, nil
	}

	var out, warn bytes.Buffer
	err := moveAll(&out, &warn, []sessionRow{rowA, rowB}, dst, tbl, false, false)
	if err == nil {
		t.Fatal("expected an aggregate error when one of two targets failed")
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("error = %v, want it to report 1 of 2 session(s) failed", err)
	}
	if copyCalls != 2 {
		t.Fatalf("copyTranscript called %d time(s), want 2 — the second target must still be processed after the first fails", copyCalls)
	}
	if !strings.Contains(warn.String(), rowA.SessionID[:8]) {
		t.Fatalf("warn = %q, want it to name the failed session", warn.String())
	}
}

// TestMoveReportsAnIncompletePictureAsAFailure covers the ruling that
// `session move` cannot warn-and-exit-0 the way `session ls` does: an
// unreadable surface may be exactly the session --all should have moved, and
// $? is all a script sees. Both cases matter — a batch where every readable
// session moved (err == nil, which used to return 0) and one that already
// failed (the surfaces must be folded into the same error, not replace it).
func TestMoveReportsAnIncompletePictureAsAFailure(t *testing.T) {
	t.Parallel()
	// Nothing unresolved: the move's own result passes through untouched,
	// including a clean success.
	if err := withIncompletePicture(nil, nil, nil); err != nil {
		t.Fatalf("a fully readable, fully successful move must return nil, got %v", err)
	}
	moveErr := errors.New("1 of 2 session(s) failed to move: 1bb0480e")
	if err := withIncompletePicture(moveErr, nil, nil); !errors.Is(err, moveErr) {
		t.Fatalf("err = %v, want the move error unchanged", err)
	}

	// Every readable session moved, but a surface could not be read: the
	// command must still exit non-zero and name the surface.
	err := withIncompletePicture(nil, []string{"surface:7"}, nil)
	if err == nil {
		t.Fatal("an unreadable surface must make the move exit non-zero, not warn and report success")
	}
	if !strings.Contains(err.Error(), "surface:7") {
		t.Fatalf("err = %v, want it to name the unreadable surface", err)
	}

	// A session that failed AND a surface that could not be read: one
	// aggregate error carrying both, with the move error still unwrappable.
	err = withIncompletePicture(moveErr, []string{"surface:7"}, []error{errors.New("surface:9: session x exists in 2 accounts at once")})
	if !errors.Is(err, moveErr) {
		t.Fatalf("err = %v, want the move error to remain unwrappable", err)
	}
	for _, want := range []string{"1 of 2 session(s) failed", "surface:7", "surface:9"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}

// TestMoveResultNarrowsIncompletePictureToAll covers the owner's decision
// that a single unreadable surface elsewhere in the workspace must not turn
// a fully successful single-session move into a failure — only --all keeps
// the old, broad behaviour, because there an unreadable surface may be
// exactly the session that should have been part of the batch.
//
// moveResult is the exact function sessionMoveCmd's RunE calls at every one
// of its three return points (the --session pane match, moveDetached, and
// the default/--all target list), so exercising it directly here is
// exercising the real decision, not a simulation of it.
func TestMoveResultNarrowsIncompletePictureToAll(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	unreadable := []string{"surface:9"}

	// Single-session path (all=false): a successful move must return nil
	// even though the inventory reported an unreadable surface elsewhere.
	var out, warn bytes.Buffer
	err := moveResult(moveAll(&out, &warn, []sessionRow{row}, dst, tbl, false, false), false, unreadable, nil)
	if err != nil {
		t.Fatalf("single-session move = %v, want nil despite an unrelated unreadable surface", err)
	}

	// Reset the fixture: the previous move already relocated (and deleted)
	// the source transcript, so a second run needs its own clean state.
	tbl, _, dst, row, _ = newMoveFixture(t, "2cc0480e-a291-4dab-bb69-8190b97b6825")

	// --all (all=true): the same successful move, in the same incomplete
	// picture, must still return non-nil.
	out.Reset()
	warn.Reset()
	err = moveResult(moveAll(&out, &warn, []sessionRow{row}, dst, tbl, false, false), true, unreadable, nil)
	if err == nil {
		t.Fatal("--all move = nil, want non-nil: an unreadable surface may be exactly the session --all should have moved")
	}
	if !strings.Contains(err.Error(), "surface:9") {
		t.Fatalf("err = %v, want it to name the unreadable surface", err)
	}
}

// newSelectTable writes a claude-cells table with the given body and loads it.
func newSelectTable(t *testing.T, body string) *claudeprofile.Table {
	t.Helper()
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := claudeprofile.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func twoCellTable(t *testing.T) *claudeprofile.Table {
	return newSelectTable(t, "cells:\n  t: { config_dir: "+t.TempDir()+", scope: globex }\n  p: { config_dir: "+t.TempDir()+", scope: perso }\n")
}

func sessionIDs(rows []sessionRow) []string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.SessionID)
	}
	return ids
}

// TestSelectTargetsDefaultsToCurrentPane covers the no-flags path: the
// session in the pane the command is run from.
func TestSelectTargetsDefaultsToCurrentPane(t *testing.T) {
	t.Parallel()
	rows := []sessionRow{
		{SurfaceRef: "surface:1", SessionID: "a", CellID: "t"},
		{SurfaceRef: "surface:2", SessionID: "b", CellID: "t"},
	}
	got, _, err := selectTargets(rows, false, "", nil, caller{SurfaceRef: "surface:2"})
	if err != nil {
		t.Fatalf("selectTargets: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "b" {
		t.Fatalf("got %+v, want the current pane's row (b)", got)
	}
}

func TestSelectTargetsErrorsWithoutCurrentPane(t *testing.T) {
	t.Parallel()
	rows := []sessionRow{{SurfaceRef: "surface:1", SessionID: "a", CellID: "t"}}
	if _, _, err := selectTargets(rows, false, "", nil, caller{}); err == nil {
		t.Fatal("expected an error when there is no session in the current pane")
	}
}

func TestSelectTargetsAllRestrictsToSourceCell(t *testing.T) {
	t.Parallel()
	tbl := twoCellTable(t)
	rows := []sessionRow{
		{SurfaceRef: "surface:1", SessionID: "a", CellID: "t"},
		{SurfaceRef: "surface:2", SessionID: "b", CellID: "p"},
		{SurfaceRef: "surface:3", SessionID: "c", CellID: "t"},
	}
	got, _, err := selectTargets(rows, true, "t", tbl, caller{})
	if err != nil {
		t.Fatalf("selectTargets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (only cell t)", len(got))
	}
	for _, r := range got {
		if r.CellID != "t" {
			t.Fatalf("row %+v leaked from another cell", r)
		}
	}
}

// TestSelectTargetsAllExcludesCallingPane is the bug this subject was opened
// for: `session move <cell> --all`, run from inside a Claude session, selected
// that very session too — which then got /exit typed into it mid-turn, killing
// the command that was moving everything else.
func TestSelectTargetsAllExcludesCallingPane(t *testing.T) {
	t.Parallel()
	tbl := twoCellTable(t)
	rows := []sessionRow{
		{SurfaceRef: "surface:1", SessionID: "a", CellID: "t"},
		{SurfaceRef: "surface:2", SessionID: "b", CellID: "t"},
		{SurfaceRef: "surface:3", SessionID: "c", CellID: "t"},
	}
	got, skipped, err := selectTargets(rows, true, "", tbl, caller{SurfaceRef: "surface:2"})
	if err != nil {
		t.Fatalf("selectTargets: %v", err)
	}
	if ids := strings.Join(sessionIDs(got), ","); ids != "a,c" {
		t.Fatalf("targets = %s, want a,c (the calling session b left out)", ids)
	}
	if skipped == nil || skipped.SessionID != "b" {
		t.Fatalf("skipped = %+v, want the calling session b reported", skipped)
	}
}

// The calling session is recognised by its Claude session id too, not only by
// its pane: a Claude session whose pane cmux does not report as the caller
// (run from a detached shell inside it, or a pane whose ref could not be
// read) must still never select itself.
func TestSelectTargetsAllExcludesCallingSessionByID(t *testing.T) {
	t.Parallel()
	tbl := twoCellTable(t)
	rows := []sessionRow{
		{SurfaceRef: "surface:1", SessionID: "a", CellID: "t"},
		{SurfaceRef: "surface:2", SessionID: "b", CellID: "t"},
	}
	got, skipped, err := selectTargets(rows, true, "t", tbl, caller{SessionID: "a"})
	if err != nil {
		t.Fatalf("selectTargets: %v", err)
	}
	if ids := strings.Join(sessionIDs(got), ","); ids != "b" {
		t.Fatalf("targets = %s, want b only", ids)
	}
	if skipped == nil || skipped.SessionID != "a" {
		t.Fatalf("skipped = %+v, want a reported", skipped)
	}
}

// A caller that sits on another cell than --from is simply not a candidate:
// nothing to exclude, nothing to report.
func TestSelectTargetsAllCallerOnOtherCell(t *testing.T) {
	t.Parallel()
	tbl := twoCellTable(t)
	rows := []sessionRow{
		{SurfaceRef: "surface:1", SessionID: "a", CellID: "t"},
		{SurfaceRef: "surface:2", SessionID: "b", CellID: "p"},
	}
	got, skipped, err := selectTargets(rows, true, "t", tbl, caller{SurfaceRef: "surface:2", SessionID: "b"})
	if err != nil {
		t.Fatalf("selectTargets: %v", err)
	}
	if ids := strings.Join(sessionIDs(got), ","); ids != "a" || skipped != nil {
		t.Fatalf("targets = %s, skipped = %+v; want a and nothing skipped", ids, skipped)
	}
}

func TestSelectTargetsAllOutsidePaneNeedsFrom(t *testing.T) {
	t.Parallel()
	rows := []sessionRow{{SurfaceRef: "surface:1", SessionID: "a", CellID: "t"}}
	if _, _, err := selectTargets(rows, true, "", nil, caller{}); err == nil {
		t.Fatal("expected --all outside a pane to require --from")
	}
}

func TestSelectTargetsUnknownFrom(t *testing.T) {
	t.Parallel()
	tbl := newSelectTable(t, "cells:\n  t: { config_dir: "+t.TempDir()+", scope: globex }\n")
	if _, _, err := selectTargets(nil, true, "nope", tbl, caller{}); err == nil {
		t.Fatal("expected an unknown --from cell to be refused")
	}
}

// exitDialogScreen is what `cmux read-screen` shows when the /exit pigtail sent
// met background work: Claude Code's dialog, with the destructive option
// preselected.
const exitDialogScreen = `
 Background work is running

 The following will stop when you exit:
   · 2 subagents running

 ❯ 1. Exit and stop tasks
   2. Move to background and exit
   3. Stay
`

// TestMoveOneCancelsExitDialogOnTimeout: when the wait times out because the
// /exit opened the "Background work is running" dialog, the move must not
// walk away with "Exit and stop tasks" preselected — one stray Enter in that
// pane would kill the session's subagents and shells. It presses Escape
// (Stay), says so, and still fails the move with the source untouched.
func TestMoveOneCancelsExitDialogOnTimeout(t *testing.T) {
	stubMoveOps(t)
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		return errors.New("session still running after 20s")
	}
	readScreen = func(string) (string, error) { return exitDialogScreen, nil }
	var keys []string
	sendKey = func(ref, key string) error { keys = append(keys, ref+" "+key); return nil }
	removeCalled := false
	removeSource = func(claudesession.Transcript) error { removeCalled = true; return nil }

	tbl, _, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	var out, warn bytes.Buffer
	err := moveOne(&out, &warn, row, dst, tbl, false, false)
	if err == nil {
		t.Fatal("expected the move to fail")
	}
	if want := []string{row.SurfaceRef + " Escape"}; strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys sent = %v, want %v", keys, want)
	}
	if !strings.Contains(err.Error(), "background work") {
		t.Fatalf("error = %q, want it to name the background work that blocked the exit", err)
	}
	if !strings.Contains(warn.String(), "dismissed") {
		t.Fatalf("warn = %q, want it to say the exit dialog was dismissed", warn.String())
	}
	if removeCalled {
		t.Fatal("removeSource must not be called")
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("source transcript should still be on disk: %v", err)
	}
}

// TestMoveOneSendsNoKeyOnTimeoutWithoutDialog: a timeout without the dialog
// on screen means the session is busy mid-turn, and there Escape interrupts
// the running turn. Nothing is pressed.
func TestMoveOneSendsNoKeyOnTimeoutWithoutDialog(t *testing.T) {
	stubMoveOps(t)
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		return errors.New("session still running after 20s")
	}
	readScreen = func(string) (string, error) {
		return "\n ✻ Cogitating… (12s · esc to interrupt)\n > /exit\n", nil
	}
	keySent := false
	sendKey = func(string, string) error { keySent = true; return nil }

	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected the move to fail")
	}
	if keySent {
		t.Fatal("no key may be sent when the exit dialog is not on screen")
	}
}

// TestMoveOneWarnsWhenScreenUnreadableOnTimeout: if the pane cannot be read,
// whether the dialog is open is unknown. Pressing Escape blind could
// interrupt a busy turn, so nothing is pressed — but the user is told to look
// at the pane, since a dialog may be sitting there.
func TestMoveOneWarnsWhenScreenUnreadableOnTimeout(t *testing.T) {
	stubMoveOps(t)
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		return errors.New("session still running after 20s")
	}
	readScreen = func(string) (string, error) { return "", errors.New("cmux read-screen: boom") }
	keySent := false
	sendKey = func(string, string) error { keySent = true; return nil }

	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected the move to fail")
	}
	if keySent {
		t.Fatal("no key may be sent when the screen could not be read")
	}
	if !strings.Contains(warn.String(), row.SurfaceRef) {
		t.Fatalf("warn = %q, want it to point at the pane to check", warn.String())
	}
}

// TestMoveOneReportsFailedDismissal: the dialog is open and Escape could not
// be delivered — the pane is still one Enter away from killing its tasks, and
// the user must be told to press Escape there by hand.
func TestMoveOneReportsFailedDismissal(t *testing.T) {
	stubMoveOps(t)
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		return errors.New("session still running after 20s")
	}
	readScreen = func(string) (string, error) { return exitDialogScreen, nil }
	sendKey = func(string, string) error { return errors.New("cmux send-key: boom") }

	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected the move to fail")
	}
	if !strings.Contains(warn.String(), "Escape") || !strings.Contains(warn.String(), row.SurfaceRef) {
		t.Fatalf("warn = %q, want it to tell the user to press Escape in %s", warn.String(), row.SurfaceRef)
	}
}

// relaunchFileFor returns the .sid path a `c`-launched Claude advertises in
// a temporary launcher state dir, and where the handoff must land.
func relaunchFileFor(t *testing.T) (sidFile, handoff string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "_dev_ttys009.sid"), filepath.Join(dir, "_dev_ttys009.handoff")
}

func handoffEnv(sidFile string) string {
	return "CLAUDE_RELAUNCH_FILE=" + sidFile + " CLAUDE_RELAUNCH_PROTO=1"
}

// TestMoveOneHandsOffToTheCLauncher is the bug: under `c`, a typed resume
// line loses its first character to the launcher's one-key prompt and
// Claude comes back with no profile. The pane must receive /exit and
// nothing else; the relaunch goes through the handoff file.
func TestMoveOneHandsOffToTheCLauncher(t *testing.T) {
	procs, _ := stubMoveOps(t)
	tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))
	me := strconv.Itoa(os.Getpid())

	var lines []string
	var atExit string
	sendLine = func(ref, line string) error {
		lines = append(lines, line)
		if line == "/exit" {
			b, _ := os.ReadFile(handoff)
			atExit = string(b)
		}
		return nil
	}
	removed := false
	removeSource = func(claudesession.Transcript) error { removed = true; return nil }

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v (warn=%s)", err, warn.String())
	}
	if len(lines) != 1 || lines[0] != "/exit" {
		t.Fatalf("lines sent to the pane = %q, want only /exit — anything typed after it is eaten by the launcher prompt", lines)
	}
	if want := "pending " + dst.ID + " " + row.SessionID + " " + me + "\n"; atExit != want {
		t.Fatalf("handoff when /exit was sent = %q, want %q", atExit, want)
	}
	b, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatalf("handoff missing after the move (the fake launcher never consumes it): %v", err)
	}
	if want := "ready " + dst.ID + " " + row.SessionID + " " + me + "\n"; string(b) != want {
		t.Fatalf("handoff = %q, want %q", b, want)
	}
	if !removed {
		t.Fatal("source not removed after a confirmed handoff relaunch")
	}
}

func TestMoveOneRefusesAnOldLauncherBeforeQuitting(t *testing.T) {
	procs, _ := stubMoveOps(t)
	tbl, src, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, "CLAUDE_RELAUNCH_FILE="+sidFile) // no proto

	sent := 0
	sendLine = func(string, string) error { sent++; return nil }

	var out, warn bytes.Buffer
	err := moveOne(&out, &warn, row, dst, tbl, false, false)
	if err == nil || !strings.Contains(err.Error(), "exec zsh") {
		t.Fatalf("err = %v, want a refusal telling to re-source the launcher", err)
	}
	if sent != 0 {
		t.Fatalf("%d line(s) sent to the pane, want none: the refusal comes before /exit", sent)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("a refused move left a handoff behind: %v", err)
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		t.Fatalf("source transcript touched: %v", err)
	}
}

func TestMoveAllRefusesOldLauncherPaneAndContinues(t *testing.T) {
	procs, _ := stubMoveOps(t)
	tbl, src, dst, rowA, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	const idB = "2cc0480e-a291-4dab-bb69-8190b97b6825"
	if err := os.WriteFile(filepath.Join(rowA.ConfigDir, "projects", "-w", idB+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rowB := rowA
	rowB.SessionID, rowB.SurfaceRef = idB, "surface:2"
	sidFile, _ := relaunchFileFor(t)
	procs.serve(src.ConfigDir, rowA.SessionID, "CLAUDE_RELAUNCH_FILE="+sidFile) // old launcher

	sentTo := map[string][]string{}
	sendLine = func(ref, line string) error { sentTo[ref] = append(sentTo[ref], line); return nil }

	var out, warn bytes.Buffer
	err := moveAll(&out, &warn, []sessionRow{rowA, rowB}, dst, tbl, false, false)
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v, want 1 of 2 failed", err)
	}
	if len(sentTo[rowA.SurfaceRef]) != 0 {
		t.Fatalf("old-launcher pane received %q, want nothing", sentTo[rowA.SurfaceRef])
	}
	if len(sentTo[rowB.SurfaceRef]) == 0 {
		t.Fatal("bare-shell pane was not moved: --all must continue past the refusal")
	}
	if !strings.Contains(warn.String(), rowA.SessionID[:8]) {
		t.Fatalf("warn = %q, want it to name the refused session", warn.String())
	}
}

// TestMoveOneWaitsForTheQuitProcessItself: a Claude started by a bare
// `c mt` has no session id in argv, so the argv probe sees it gone at once.
// The quit wait must also watch the pid found before /exit — otherwise the
// copy races a live writer and the exit dialog is never looked for.
func TestMoveOneWaitsForTheQuitProcessItself(t *testing.T) {
	for _, route := range []string{"handoff", "typed"} {
		t.Run(route, func(t *testing.T) {
			procs, _ := stubMoveOps(t)
			tbl, src, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
			sidFile, handoff := relaunchFileFor(t)
			env := "CMUX_SURFACE_ID=" + surfaceA
			if route == "handoff" {
				env += " " + handoffEnv(sidFile)
			}
			procs.serve(src.ConfigDir, row.SessionID, env) // never exits

			runningProbe = func(string) bool { return false } // no id in argv
			waitExit = claudesession.WaitExit                 // the real wait, on the short quitTimeout
			readScreen = func(string) (string, error) { return exitDialogScreen, nil }
			var keys []string
			sendKey = func(ref, key string) error { keys = append(keys, key); return nil }
			copied := false
			copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
				copied = true
				return claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}, nil
			}

			var out, warn bytes.Buffer
			if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
				t.Fatal("the session never quit: the move must fail")
			}
			if copied {
				t.Fatal("transcript copied while its Claude was still running")
			}
			if strings.Join(keys, ",") != "Escape" {
				t.Fatalf("keys = %v, want the exit dialog dismissed with Escape", keys)
			}
			if _, err := os.Stat(handoff); !os.IsNotExist(err) {
				t.Fatalf("handoff left behind: %v", err)
			}
			if _, err := os.Stat(transcriptPath); err != nil {
				t.Fatalf("source transcript touched: %v", err)
			}
		})
	}
}

func TestMoveOneRemovesHandoffWhenQuitTimesOut(t *testing.T) {
	procs, _ := stubMoveOps(t)
	tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		return errors.New("still running")
	}

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected the quit timeout to fail the move")
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("pending handoff left behind after a failed quit (the next real exit would wait): %v", err)
	}
}

func TestMoveOneRemovesHandoffWhenCopyFails(t *testing.T) {
	procs, _ := stubMoveOps(t)
	tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))
	copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
		return claudesession.Transcript{}, errors.New("copy boom")
	}

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected the copy failure to fail the move")
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("handoff left behind after a failed copy: %v", err)
	}
}

// TestMoveOneWithdrawsHandoffOnInterrupt: a Ctrl-C while pigtail waits for
// the quit must not leave a pending handoff for the launcher to wait on.
func TestMoveOneWithdrawsHandoffOnInterrupt(t *testing.T) {
	procs, ic := stubMoveOps(t)
	tbl, src, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))

	var out, warn bytes.Buffer
	interrupted := false
	var atInterrupt string
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		if ic.cleanup == nil {
			t.Fatal("no interrupt handler installed while a pending handoff exists")
		}
		ic.cleanup() // what SIGINT would run
		interrupted = true
		atInterrupt = warn.String()
		if _, err := os.Stat(handoff); !os.IsNotExist(err) {
			t.Fatalf("handoff still there after the interrupt cleanup: %v", err)
		}
		return errors.New("interrupted")
	}

	_ = moveOne(&out, &warn, row, dst, tbl, false, false)
	if !interrupted {
		t.Fatal("waitExit never ran")
	}
	if ic.stops != 1 {
		t.Fatalf("interrupt handler stopped %d time(s), want once when moveOne returns", ic.stops)
	}
	// Interrupted before the copy: nothing on the target, the session resumes
	// on the source — through `c`, never a typed line the c prompt would eat.
	for _, want := range []string{"✗ interrupted", "source kept", transcriptPath, "c " + src.ID + " --resume " + row.SessionID, "any other key"} {
		if !strings.Contains(atInterrupt, want) {
			t.Fatalf("interrupt said %q, want it to mention %q", atInterrupt, want)
		}
	}
	if strings.Contains(atInterrupt, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("interrupt said %q: offers a typed line on the handoff route", atInterrupt)
	}
}

// TestMoveOneInterruptAfterCopyNamesTheRecovery: a Ctrl-C while the relaunch
// is being confirmed leaves a copy on the target and the source in place —
// pigtail must say so, with the recovery fitting the route, not exit mute.
func TestMoveOneInterruptAfterCopyNamesTheRecovery(t *testing.T) {
	for _, route := range []string{"handoff", "typed"} {
		t.Run(route, func(t *testing.T) {
			procs, ic := stubMoveOps(t)
			tbl, src, dst, row, transcriptPath := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
			sidFile, _ := relaunchFileFor(t)
			env := "CMUX_SURFACE_ID=" + surfaceA
			if route == "handoff" {
				env += " " + handoffEnv(sidFile)
			}
			procs.serve(src.ConfigDir, row.SessionID, env)

			var out, warn bytes.Buffer
			var atInterrupt string
			confirmRelaunch = func(relaunchCheck) (int, error) {
				if ic.cleanup == nil {
					t.Fatal("no interrupt handler installed while the relaunch is being confirmed")
				}
				ic.cleanup() // what SIGINT would run
				atInterrupt = warn.String()
				return 0, errRelaunchNotSeen
			}
			_ = moveOne(&out, &warn, row, dst, tbl, false, false)

			typedLine := cmux.ResumeLine("/cmux/bin/cmux-claude-wrapper", dst.ConfigDir, row.SessionID)
			for _, want := range []string{"✗ interrupted", "source kept", transcriptPath, "a copy already exists", "cmux surface resume set"} {
				if !strings.Contains(atInterrupt, want) {
					t.Fatalf("interrupt said %q, want it to mention %q", atInterrupt, want)
				}
			}
			if route == "handoff" {
				if !strings.Contains(atInterrupt, "c "+dst.ID+" --resume "+row.SessionID) {
					t.Fatalf("interrupt said %q, want `c %s --resume <sid>`", atInterrupt, dst.ID)
				}
				if strings.Contains(atInterrupt, typedLine) {
					t.Fatalf("interrupt said %q: offers the typed line the c prompt would eat", atInterrupt)
				}
			} else if !strings.Contains(atInterrupt, typedLine) {
				t.Fatalf("interrupt said %q, want the typed resume line", atInterrupt)
			}
		})
	}
}

func TestMoveOneDryRunNamesTheRelaunchRoute(t *testing.T) {
	cases := []struct {
		name, env, wantOut, wantErr string
	}{
		{name: "bare shell", env: "", wantOut: "typing the resume line"},
		{name: "launcher", env: "proto", wantOut: "launcher handoff"},
		{name: "old launcher", env: "old", wantErr: "exec zsh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			procs, _ := stubMoveOps(t)
			tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
			sidFile, handoff := relaunchFileFor(t)
			switch c.env {
			case "proto":
				procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))
			case "old":
				procs.serve(src.ConfigDir, row.SessionID, "CLAUDE_RELAUNCH_FILE="+sidFile)
			}
			sendLine = func(string, string) error { t.Fatal("dry run sent a line"); return nil }

			var out, warn bytes.Buffer
			err := moveOne(&out, &warn, row, dst, tbl, true, false)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}
			if !strings.Contains(out.String(), c.wantOut) {
				t.Fatalf("out = %q, want it to name the route %q", out.String(), c.wantOut)
			}
			if _, err := os.Stat(handoff); !os.IsNotExist(err) {
				t.Fatal("dry run wrote a handoff")
			}
		})
	}
}

// guardFixture is a typed-route move whose post-relaunch check is the real
// one: what comes back in the pane is decided by comeBack, called when the
// resume line is typed.
func guardFixture(t *testing.T, comeBack func(f *fakeProcs, sid, dstDir string)) (
	tbl *claudeprofile.Table, dst claudeprofile.Cell, row sessionRow, transcriptPath string, procs *fakeProcs,
) {
	t.Helper()
	testutil.SerialOnly(t) // swaps confirmRelaunch
	procs, _ = stubMoveOps(t)
	confirmRelaunch = verifyRelaunch
	tbl, _, dst, row, transcriptPath = newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sendLine = func(ref, line string) error {
		if strings.Contains(line, "--resume") && comeBack != nil {
			comeBack(procs, row.SessionID, dst.ConfigDir)
		}
		return nil
	}
	return tbl, dst, row, transcriptPath, procs
}

// TestMoveOneKeepsSourceWhenRelaunchIsNotConfirmed is requirement 3: pigtail
// used to print "✓ session now on X" and delete the source without ever
// looking at what came back in the pane.
func TestMoveOneKeepsSourceWhenRelaunchIsNotConfirmed(t *testing.T) {
	cases := []struct {
		name     string
		comeBack func(f *fakeProcs, sid, dstDir string)
		stale    bool
		wantErr  string
	}{
		{name: "comes back without a profile (the original bug)", wantErr: "wrong account",
			comeBack: func(f *fakeProcs, sid, _ string) { f.comesBack(sid, "HOME=/Users/x") }},
		{name: "comes back under another config dir", wantErr: "wrong account",
			comeBack: func(f *fakeProcs, sid, dstDir string) {
				f.comesBack(sid, "CLAUDE_CONFIG_DIR="+dstDir+" CLAUDE_CONFIG_DIR=/Users/x/.claude") // last wins
			}},
		{name: "nothing comes back", wantErr: "relaunch not observed"},
		{name: "only a process older than /exit matches", wantErr: "relaunch not observed", stale: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, keep := range []bool{false, true} {
				tbl, dst, row, transcriptPath, procs := guardFixture(t, c.comeBack)
				if c.stale {
					procs.comesBack(row.SessionID, "CLAUDE_CONFIG_DIR="+dst.ConfigDir) // alive before /exit
				}
				removed, rebound := false, false
				removeSource = func(claudesession.Transcript) error { removed = true; return nil }
				setResumeBinding = func(string, string, string, string, string) error { rebound = true; return nil }

				var out, warn bytes.Buffer
				err := moveOne(&out, &warn, row, dst, tbl, false, keep)
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("keep=%v: err = %v, want %q", keep, err, c.wantErr)
				}
				if removed || rebound {
					t.Fatalf("keep=%v: removed=%v rebound=%v, want neither when the relaunch is not confirmed", keep, removed, rebound)
				}
				if strings.Contains(out.String(), "now on") || strings.Contains(out.String(), "✓ relaunched") {
					t.Fatalf("keep=%v: out = %q, must not claim success", keep, out.String())
				}
				if _, err := os.Stat(transcriptPath); err != nil {
					t.Fatalf("keep=%v: source transcript gone: %v", keep, err)
				}
				if !strings.Contains(warn.String(), "source kept") {
					t.Fatalf("keep=%v: warn = %q, want it to say the source is kept", keep, warn.String())
				}
			}
		})
	}
}

func TestMoveOneConfirmsRelaunchBeforeRemovingSource(t *testing.T) {
	var pid int
	tbl, dst, row, _, _ := guardFixture(t, func(f *fakeProcs, sid, dstDir string) {
		pid = f.comesBack(sid, "CLAUDE_CONFIG_DIR="+dstDir)
	})
	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v (warn=%s)", err, warn.String())
	}
	o := out.String()
	want := fmt.Sprintf("✓ relaunched on %s (pid %d)", dst.ID, pid)
	if !strings.Contains(o, want) || !strings.Contains(o, "now on "+dst.ID) {
		t.Fatalf("out = %q, want %q then the success line", o, want)
	}
	if strings.Index(o, "✓ relaunched") > strings.Index(o, "now on") {
		t.Fatalf("out = %q, relaunch confirmation must precede the success line", o)
	}
}

func TestMoveOneRefusesWhenProcessesCannotBeListed(t *testing.T) {
	tbl, dst, row, _, procs := guardFixture(t, nil)
	procs.listErr = errors.New("ps boom")
	sent := 0
	sendLine = func(string, string) error { sent++; return nil }

	var out, warn bytes.Buffer
	err := moveOne(&out, &warn, row, dst, tbl, false, false)
	if err == nil || !strings.Contains(err.Error(), "ps boom") {
		t.Fatalf("err = %v, want the listing failure", err)
	}
	if sent != 0 {
		t.Fatal("/exit sent although the relaunch could never be checked")
	}
}

// TestMoveOneHandoffFailureNeverOffersTheTypedLine: under `c`, the recovery
// line must be `c <cell> --resume <sid>` — a CLAUDE_CONFIG_DIR=… line typed
// there would be eaten by the launcher prompt exactly like the bug. The
// ready handoff is withdrawn so it is not consumed later.
func TestMoveOneHandoffFailureNeverOffersTheTypedLine(t *testing.T) {
	procs, _ := stubMoveOps(t)
	confirmRelaunch = verifyRelaunch
	tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("nothing came back: the move must fail")
	}
	w := warn.String()
	if !strings.Contains(w, "c "+dst.ID+" --resume "+row.SessionID) {
		t.Fatalf("warn = %q, want `c %s --resume <sid>`", w, dst.ID)
	}
	if strings.Contains(w, cmux.ResumeLine("/cmux/bin/cmux-claude-wrapper", dst.ConfigDir, row.SessionID)) {
		t.Fatalf("warn = %q offers the typed line the c prompt would eat", w)
	}
	if _, err := os.Stat(handoff); !os.IsNotExist(err) {
		t.Fatalf("ready handoff left behind after an unconfirmed relaunch: %v", err)
	}
}

// The launcher reads the handoff only when <key>.sid exists and holds the
// handoff's id; the SessionEnd hook (session-relaunch.sh) normally writes it
// on /exit. A cell without the hook, a hook that fails or times out, and the
// move used to fail at confirmRelaunch. pigtail writes it itself, in the
// hook's format, before /exit — the fake here runs no SessionEnd hook.
func TestMoveOneWritesSidWithoutSessionEnd(t *testing.T) {
	procs, _ := stubMoveOps(t)
	tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))

	var atExit, atReady string
	sendLine = func(_, line string) error {
		if line == "/exit" {
			b, _ := os.ReadFile(sidFile)
			atExit = string(b)
		}
		return nil
	}
	confirmRelaunch = func(relaunchCheck) (int, error) {
		h, _ := os.ReadFile(handoff)
		if !strings.HasPrefix(string(h), handoffReady+" ") {
			t.Fatalf("handoff at the relaunch check = %q, want ready", h)
		}
		b, _ := os.ReadFile(sidFile)
		atReady = string(b)
		return 4343, nil
	}

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v (warn=%s)", err, warn.String())
	}
	want := row.SessionID + "\n"
	if atExit != want {
		t.Fatalf("<key>.sid when /exit was sent = %q, want %q", atExit, want)
	}
	if atReady != want {
		t.Fatalf("<key>.sid when the handoff went ready = %q, want %q", atReady, want)
	}
	if fi, err := os.Stat(sidFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("<key>.sid after the move: %v (err %v), want it kept for the launcher, mode 0600", fi, err)
	}
}

// Every abandon takes back the .sid pigtail wrote — while it still holds
// pigtail's id; one rewritten since (a SessionEnd hook for another id, a new
// Claude) is someone else's and stays.
func TestMoveOneAbandonWithdrawsItsSid(t *testing.T) {
	const other = "9f9f9f9f-0000-4000-8000-000000000000\n"
	cases := []struct {
		name    string
		rewrite bool
		fail    func(sidFile string)
	}{
		{"quit fails", false, func(string) {
			sendLine = func(string, string) error { return errors.New("quit boom") }
		}},
		{"copy fails", false, func(string) {
			copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
				return claudesession.Transcript{}, errors.New("copy boom")
			}
		}},
		{"relaunch unconfirmed", false, func(string) {
			confirmRelaunch = func(relaunchCheck) (int, error) { return 0, errors.New("nothing came back") }
		}},
		{"rewritten by another is kept", true, func(sidFile string) {
			copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
				_ = os.WriteFile(sidFile, []byte(other), 0o600)
				return claudesession.Transcript{}, errors.New("copy boom")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			procs, _ := stubMoveOps(t)
			tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
			sidFile, handoff := relaunchFileFor(t)
			procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))
			tc.fail(sidFile)

			var out, warn bytes.Buffer
			if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
				t.Fatal("expected the move to fail")
			}
			if fileExists(handoff) {
				t.Fatal("handoff left behind")
			}
			b, err := os.ReadFile(sidFile)
			if tc.rewrite {
				if string(b) != other {
					t.Fatalf("<key>.sid = %q (err %v), want the other id kept", b, err)
				}
				return
			}
			if !os.IsNotExist(err) {
				t.Fatalf("<key>.sid pigtail wrote left behind after the abandon: %q (err %v)", b, err)
			}
		})
	}
}

// A Ctrl-C takes the .sid back with the handoff.
func TestMoveOneInterruptWithdrawsItsSid(t *testing.T) {
	procs, ic := stubMoveOps(t)
	tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	sidFile, _ := relaunchFileFor(t)
	procs.serve(src.ConfigDir, row.SessionID, handoffEnv(sidFile))
	var before, after bool
	waitExit = func(string, time.Duration, time.Duration, func(string) bool) error {
		before = fileExists(sidFile)
		ic.cleanup()
		after = fileExists(sidFile)
		return errors.New("interrupted")
	}
	var out, warn bytes.Buffer
	_ = moveOne(&out, &warn, row, dst, tbl, false, false)
	if !before || after {
		t.Fatalf("<key>.sid before the interrupt %v, after it %v; want written, then withdrawn", before, after)
	}
}

// Two movers of one session never run at once: Copy's sidecar cleanup would
// remove the other's files. A handover (or another move) holding the session
// lock makes session move refuse before it touches anything.
func TestMoveRefusesWhileTheSessionLockIsHeld(t *testing.T) {
	stubMoveOps(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tbl, src, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	unlock, err := claudesession.LockSession(row.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	copied, sent := false, false
	copyTranscript = func(s claudesession.Transcript, d string) (claudesession.Transcript, error) {
		copied = true
		return claudesession.Transcript{ID: s.ID, ConfigDir: d, Slug: s.Slug}, nil
	}
	sendLine = func(string, string) error { sent = true; return nil }
	tr := claudesession.Transcript{ID: row.SessionID, ConfigDir: src.ConfigDir, Slug: row.Slug}
	var out, warn bytes.Buffer
	if err := moveDetached(&out, &warn, tr, src, dst, false, false); !errors.Is(err, claudesession.ErrSessionBusy) || copied {
		t.Fatalf("moveDetached: err %v, copied %v; want ErrSessionBusy, nothing copied", err, copied)
	}
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); !errors.Is(err, claudesession.ErrSessionBusy) || sent || copied {
		t.Fatalf("moveOne: err %v, sent %v, copied %v; want ErrSessionBusy, nothing sent", err, sent, copied)
	}
}

func TestMoveOnePreTrustsTargetCellBeforeRelaunch(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	var calls []string
	copyTranscript = func(src claudesession.Transcript, d string) (claudesession.Transcript, error) {
		calls = append(calls, "copy")
		return claudesession.Transcript{ID: src.ID, ConfigDir: d, Slug: src.Slug}, nil
	}
	sendLine = func(ref, line string) error {
		if line != "/exit" {
			calls = append(calls, "relaunch")
		}
		return nil
	}
	movePreTrust = func(dir, cwd string) claudeconfig.Result {
		calls = append(calls, "pretrust:"+dir+"|"+cwd)
		return claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/w"}
	}
	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatal(err)
	}
	want := []string{"copy", "pretrust:" + dst.ConfigDir + "|/w", "relaunch"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}
	if !strings.Contains(out.String(), "✓ folder trust pre-accepted on mt (/w)") {
		t.Fatalf("out %q", out.String())
	}
}

func TestMoveOnePreTrustFailureWarnsAndMoves(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	movePreTrust = func(string, string) claudeconfig.Result {
		return claudeconfig.Result{Outcome: claudeconfig.Failed, Reason: "lock held"}
	}
	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), `⚠ folder trust not pre-accepted on mt: lock held — the relaunched session may stop on "trust this folder"`) {
		t.Fatalf("warn %q", warn.String())
	}
}

func TestMoveOneDryRunDoesNotPreTrust(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	called := false
	movePreTrust = func(string, string) claudeconfig.Result { called = true; return claudeconfig.Result{} }
	var out, warn bytes.Buffer
	_ = moveOne(&out, &warn, row, dst, tbl, true, false)
	if called {
		t.Fatal("a dry run wrote trust")
	}
}

// A cell still written with a retired alias and a globex cell are
// one scope: moving between them is a same-scope move.
func TestValidateMoveWorkAndGlobexCells(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := "cells:\n  t: { config_dir: /a, scope: globex }\n  s: { config_dir: /b, scope: globex }\n"
	if err := os.WriteFile(filepath.Join(dir, claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tb, err := claudeprofile.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	src, _ := tb.Cell("t")
	dst, _ := tb.Cell("s")
	if err := validateMove(src, dst); err != nil {
		t.Fatalf("work → globex move refused: %v", err)
	}
}

// --- moveOneGuarded (account rotation, sub_x000000000000008) ---------------

func TestMoveOneGuardedHoldsRelaunchLine(t *testing.T) {
	_, _ = stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, rotSID)
	var typed []string
	sendLine = func(_, line string) error { typed = append(typed, line); return nil }
	guard := func(stage string) error {
		if stage == moveStageRelaunch {
			return &rotateGuardErr{why: "relaunch held — pane selected and cmux frontmost"}
		}
		return nil
	}
	err := moveOneGuarded(io.Discard, io.Discard, row, dst, tbl, false, false, guard)
	var after *errAfterExit
	if !errors.As(err, &after) || after.Stage != "relaunch" || !strings.Contains(err.Error(), "relaunch held") {
		t.Fatalf("err = %v", err)
	}
	if len(typed) != 1 || typed[0] != "/exit" {
		t.Fatalf("typed = %v, want /exit alone", typed)
	}
	if want := cmux.ResumeLine("/cmux/bin/cmux-claude-wrapper", dst.ConfigDir, rotSID); after.Run != want {
		t.Fatalf("run = %q, want %q", after.Run, want)
	}
}

func TestMoveOneGuardedHandoffRouteNoGuard(t *testing.T) {
	procs, _ := stubMoveOps(t)
	tbl, src, dst, row, _ := newMoveFixture(t, rotSID)
	sidFile, handoff := relaunchFileFor(t)
	procs.serve(src.ConfigDir, rotSID, handoffEnv(sidFile))
	var stages []string
	guard := func(stage string) error { stages = append(stages, stage); return nil }
	if err := moveOneGuarded(io.Discard, io.Discard, row, dst, tbl, false, false, guard); err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(stages) != 1 || stages[0] != moveStageQuit {
		t.Fatalf("guard asked at %v, want quit only (the handoff route types nothing after /exit)", stages)
	}
	if _, err := os.Stat(handoff); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// The manual session move types /exit and the resume line as on the base, and
// nothing else: no resume prompt, no guard even with the pane focused.
func TestMoveTypesNoResumePrompt(t *testing.T) {
	_, _ = stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, rotSID)
	var typed []string
	sendLine = func(_, line string) error { typed = append(typed, line); return nil }
	if err := moveOne(io.Discard, io.Discard, row, dst, tbl, false, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(typed) != 2 || typed[0] != "/exit" || !strings.Contains(typed[1], "--resume "+rotSID) {
		t.Fatalf("typed = %v", typed)
	}
	for _, l := range typed {
		if strings.Contains(l, "Reprise auto") {
			t.Fatalf("the manual move typed the resume prompt: %q", l)
		}
	}
}

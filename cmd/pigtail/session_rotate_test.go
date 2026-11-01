package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	rotSID     = "7c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f"
	rotSurface = "3A23D5DD-59EE-471D-9270-32F0FB06E099"
	rotOther   = "11111111-2222-4333-8444-555555555555"
	rotWS      = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "claudesession", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// rotFixture is everything the worker sees, faked: the cmux calls, the
// native session files, the front app, the clock, the pick, and a real test
// daemon for the waits. No real cmux, Claude or lsappinfo ever runs.
type rotFixture struct {
	t        *testing.T
	c        *pigtail.Client
	logDir   string
	getenv   func(string) string
	tbl      *claudeprofile.Table
	src, dst claudeprofile.Cell
	row      sessionRow
	procs    *fakeProcs
	pid      int

	mu        sync.Mutex
	now       time.Time
	until     time.Time
	focused   func() string
	front     func() (string, error)
	frontHits int
	screen    func() string
	status    func(cfg string) string
	listing   func() []peers.CmuxSession
	ttys      func() (map[string]string, error)
	refs      map[string]string
	bindings  map[string]string
	running   func(n int) bool
	runCalls  int
	sendErr   error
	sent      []string // rotateCmux.Send lines
	typed     []string // moveOne's sendLine lines (ref|line)
	marks     []string
	markErr   error
	moves     int
	turnAfter bool // a typed resume prompt starts a turn
	prompted  bool
	journal   []pigtail.JournalEntry // rotAddJournal rows
	standby   string                 // the Standby marker dir
	log       bytes.Buffer
}

func newRotFixture(t *testing.T) *rotFixture {
	t.Helper()
	c, _ := startRelayDaemon(t)
	procs, _ := stubMoveOps(t)
	tbl, src, dst, row, _ := newMoveFixture(t, rotSID)
	f := &rotFixture{t: t, c: c, tbl: tbl, src: src, dst: dst, row: row, procs: procs,
		logDir: t.TempDir(), now: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), turnAfter: true}
	f.until = f.now.Add(3 * time.Hour)
	f.standby = t.TempDir()
	f.getenv = envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": f.logDir, envLaunchProto: "3", "PIGTAIL_STANDBY_DIR": f.standby})
	f.pid = procs.serve(src.ConfigDir, rotSID, "CMUX_SURFACE_ID="+strings.ToLower(rotSurface))
	procs.native[dst.ConfigDir+"|"+rotSID] = f.pid // the relaunched Claude, same pid for the fake listing
	idle := readFixture(t, "screen-idle-empty.txt")
	f.focused = func() string { return "" }
	f.front = func() (string, error) { return "com.apple.Safari", nil }
	f.screen = func() string { return idle }
	f.status = func(string) string {
		if f.prompted && f.turnAfter {
			return "busy"
		}
		return "idle"
	}
	f.listing = func() []peers.CmuxSession {
		return []peers.CmuxSession{{SessionID: rotSID, WorkspaceID: rotWS, SurfaceID: strings.ToLower(rotSurface), PID: f.pid,
			ActiveForSurface: true, ActiveSurfaceSessionID: rotSID}}
	}
	f.ttys = func() (map[string]string, error) { return map[string]string{rotSurface: "ttys001"}, nil }
	f.refs = map[string]string{rotSurface: "surface:1"}
	f.bindings = map[string]string{"surface:1": realBindingFor(rotSID)}
	f.running = func(int) bool { return false }
	f.install()
	return f
}

func (f *rotFixture) install() {
	t := f.t
	savedCmux, savedFront, savedRead, savedRun := rotateCmux, frontApp, rotReadNative, rotCheckRunning
	savedNow, savedSleep, savedAlive, savedLoad := rotateNow, rotateSleep, rotPidAlive, rotLoadTable
	savedFrozen, savedMove, savedSidebar, savedCli, savedAcct := rotFrozen, rotMove, rotSidebar, rotSidebarCli, rotSourceAccount
	savedPick, savedJournal, savedWaits := pickCell, rotAddJournal, rotSessionWaits
	t.Cleanup(func() {
		rotAddJournal, rotSessionWaits = savedJournal, savedWaits
		rotateCmux, frontApp, rotReadNative, rotCheckRunning = savedCmux, savedFront, savedRead, savedRun
		rotateNow, rotateSleep, rotPidAlive, rotLoadTable = savedNow, savedSleep, savedAlive, savedLoad
		rotFrozen, rotMove, rotSidebar, rotSidebarCli, rotSourceAccount = savedFrozen, savedMove, savedSidebar, savedCli, savedAcct
		pickCell = savedPick
	})
	rotateCmux = rotateCmuxOps{
		Sessions: func() ([]byte, error) {
			return json.Marshal(map[string]any{"sessions": f.listing()})
		},
		SurfaceTTYs: func() (map[string]string, error) { return f.ttys() },
		SurfaceRefs: func() (map[string]string, error) { return f.refs, nil },
		Binding:     func(ref string) (string, error) { return f.bindings[ref], nil },
		Focused:     func() (string, error) { return f.focused(), nil },
		ReadScreen: func(id string) (string, error) {
			if !strings.EqualFold(id, rotSurface) {
				t.Fatalf("screen read on %s", id)
			}
			return f.screen(), nil
		},
		Send: func(id, line string) error {
			if !strings.EqualFold(id, rotSurface) {
				t.Fatalf("typed into %s", id)
			}
			if f.sendErr != nil {
				return f.sendErr
			}
			f.sent = append(f.sent, line)
			f.prompted = true
			return nil
		},
	}
	frontApp = func(context.Context) (string, error) { f.frontHits++; return f.front() }
	rotReadNative = func(cfg string, pid int) (claudesession.Native, bool) {
		return claudesession.Native{PID: pid, SessionID: rotSID, Status: f.status(cfg)}, true
	}
	rotCheckRunning = func() bool { f.runCalls++; return f.running(f.runCalls) }
	rotAddJournal = func(_ context.Context, _ func(string) string, e pigtail.JournalEntry) error {
		f.journal = append(f.journal, e)
		return nil
	}
	// The fixture's session has no scope (a hook run outside any cell): only
	// the master sees it, as waitsOf does.
	rotSessionWaits = func(context.Context, func(string) string, string) ([]pigtail.SessionWait, error) {
		return waitsOf(t, f.c, rotSID), nil
	}
	rotateNow = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	rotateSleep = func(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }
	rotPidAlive = func(int) bool { return false }
	rotLoadTable = func() (*claudeprofile.Table, error) { return f.tbl, nil }
	rotFrozen = func(func(string) string) error { return nil }
	rotSidebar = func(_ context.Context, _ *pigtail.Client, wsID string, req *pigtail.CmuxRespawnRequest) (*pigtail.CmuxRespawnNotice, error) {
		if req.RunID != "" || req.State != pigtail.CmuxRespawnFailed {
			t.Fatalf("mark = %+v", req)
		}
		if f.markErr != nil {
			return nil, f.markErr
		}
		f.marks = append(f.marks, wsID)
		return nil, nil
	}
	rotSidebarCli = func() *pigtail.Client { return nil }
	rotSourceAccount = func(claudeprofile.Cell) (string, error) { return "acct-src", nil }
	pickCell = func(_ *claudeprofile.Table, _ string, l map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		if !l["acct-src"].Equal(f.until) {
			t.Fatalf("limited = %v, want acct-src until %v", l, f.until)
		}
		ranking := []claudeusage.Candidate{{Label: "j", AccountUUID: "acct-dst"}, {Label: "m", AccountUUID: "acct-src"}}
		return f.dst, claudeusage.Choice{Candidate: ranking[0], Ranking: ranking, Warnings: []string{}}, nil
	}
	sendLine = func(ref, line string) error { f.typed = append(f.typed, ref+"|"+line); return nil }
	rotMove = func(out, warn io.Writer, r sessionRow, dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun, keepSource bool, guard func(string) error) error {
		f.moves++
		return moveOneGuarded(out, warn, r, dst, tbl, dryRun, keepSource, guard)
	}
}

func (f *rotFixture) opts() rotateOpts {
	return rotateOpts{Session: rotSID, Surface: rotSurface, Until: f.until}
}

func (f *rotFixture) run() error {
	return runRotate(context.Background(), f.opts(), f.getenv, &f.log)
}

func (f *rotFixture) waits() []pigtail.SessionWait { return waitsOf(f.t, f.c, rotSID) }

// hooks returns the hook=rotate lines of hooks.jsonl.
func (f *rotFixture) hooks() []map[string]string {
	b, _ := os.ReadFile(filepath.Join(f.logDir, "hooks.jsonl"))
	var out []map[string]string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]string
		if json.Unmarshal([]byte(l), &m) == nil && m["hook"] == "rotate" {
			out = append(out, m)
		}
	}
	return out
}

func (f *rotFixture) hasHook(outcome, contains string) bool {
	for _, h := range f.hooks() {
		if h["outcome"] == outcome && strings.Contains(h["detail"], contains) {
			return true
		}
	}
	return false
}

// typedExit: was /exit typed (by moveOne)?
func (f *rotFixture) typedExit() bool {
	for _, l := range f.typed {
		if strings.HasSuffix(l, "|/exit") {
			return true
		}
	}
	return false
}

// limitWait is the session's one Limit wait, or fails.
func (f *rotFixture) limitWait() pigtail.SessionWait {
	f.t.Helper()
	ws := f.waits()
	if len(ws) != 1 || ws[0].Type != "limit" || ws[0].Source != "auto" {
		f.t.Fatalf("waits = %+v, want one auto limit", ws)
	}
	return ws[0]
}

func (f *rotFixture) wantRefused(why string) {
	f.t.Helper()
	w := f.limitWait()
	if !strings.HasPrefix(w.Reason, "rotation: ") || !strings.Contains(w.Reason, why) {
		f.t.Fatalf("reason = %q, want rotation: …%s…", w.Reason, why)
	}
	if w.Until == nil || !w.Until.Equal(f.until) {
		f.t.Fatalf("until = %v, want %v", w.Until, f.until)
	}
	if !f.hasHook("refused", why) {
		f.t.Fatalf("no refused line with %q: %+v", why, f.hooks())
	}
}

func (f *rotFixture) wantNothingTyped() {
	f.t.Helper()
	if len(f.typed) != 0 || len(f.sent) != 0 {
		f.t.Fatalf("typed %v / sent %v, want nothing", f.typed, f.sent)
	}
}

// --- the move and its refusals ---------------------------------------------

func TestRotateWorkerMovesToPickedAccount(t *testing.T) {
	f := newRotFixture(t)
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if f.moves != 1 || !f.typedExit() {
		t.Fatalf("moves=%d typed=%v", f.moves, f.typed)
	}
	if !f.hasHook("moved", "from=t to=mt") || !f.hasHook("resumed", "") {
		t.Fatalf("hooks = %+v", f.hooks())
	}
	if _, ok := lastRotated(rotSID); !ok {
		t.Fatal(".last not written")
	}
}

// A .last that cannot be written turns the 30-min ping-pong guard off: the
// hooks.jsonl line says so, not only the worker log.
func TestRotateWorkerLastWriteFailureIsInHooks(t *testing.T) {
	f := newRotFixture(t)
	if err := os.MkdirAll(rotateLastPath(rotSID), 0o700); err != nil { // a directory: ReadFile and WriteFile both fail
		t.Fatal(err)
	}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if !f.hasHook("moved", "from=t to=mt") {
		t.Fatalf("the move itself must still be reported: %+v", f.hooks())
	}
	if !f.hasHook("last-write-failed", "30-min guard off") {
		t.Fatalf("no last-write-failed line: %+v", f.hooks())
	}
}

func TestRotateWorkerSameAccountKeepsLimit(t *testing.T) {
	f := newRotFixture(t)
	pickCell = func(*claudeprofile.Table, string, map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		r := []claudeusage.Candidate{{Label: "m", AccountUUID: "acct-src"}}
		return f.src, claudeusage.Choice{Candidate: r[0], Ranking: r, Warnings: []string{"every account of scope work is over the margin"}}, nil
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantRefused("no other account")
	if !strings.Contains(f.limitWait().Reason, "over the margin") {
		t.Fatalf("the pick's warning is not repeated: %q", f.limitWait().Reason)
	}
	f.wantNothingTyped()
}

// D3 (sub_x000000000000011): a rotation resumes a conversation tied to an
// account and exists to leave a limited one — the launch margin guard never
// refuses it. With every account of the scope over the margin, rotate still
// moves to the account Pick chose, with the pick's warning, never exit 3.
// Pinning test: green on base and on the branch.
func TestRotateStillPicksWhenAllOverMargin(t *testing.T) {
	f := newRotFixture(t)
	pickCell = func(*claudeprofile.Table, string, map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		r := []claudeusage.Candidate{
			{Label: "j", AccountUUID: "acct-dst", Group: claudeusage.GroupOverMargin},
			{Label: "m", AccountUUID: "acct-src", Group: claudeusage.GroupOverMargin},
		}
		return f.dst, claudeusage.Choice{Candidate: r[0], Ranking: r,
			Warnings: []string{"every account of scope work is over the margin"}}, nil
	}
	err := f.run()
	if err != nil || exitCode(err) == 3 {
		t.Fatalf("run = %v (exit %d), want the move\n%s", err, exitCode(err), f.log.String())
	}
	if f.moves != 1 || !f.typedExit() {
		t.Fatalf("moves=%d typed=%v, want the rotation to proceed", f.moves, f.typed)
	}
}

func TestRotateWorkerOncePer30Min(t *testing.T) {
	f := newRotFixture(t)
	_ = os.MkdirAll(rotateDir(), 0o700)
	if err := os.WriteFile(rotateLastPath(rotSID), []byte(f.now.Add(-10*time.Minute).Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantRefused("rotated 10 min ago")
	f.wantNothingTyped()
}

func TestRotateWorkerLastRecheckedBeforeMove(t *testing.T) {
	f := newRotFixture(t)
	// .last appears while the guard waits (another rotation landed meanwhile).
	n := 0
	f.screen = func() string {
		n++
		if n == 1 {
			_ = os.WriteFile(rotateLastPath(rotSID), []byte(f.now.Format(time.RFC3339)), 0o600)
		}
		return readFixture(t, "screen-idle-empty.txt")
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	if f.moves != 0 {
		t.Fatal("moved despite a fresh .last")
	}
	f.wantRefused("rotated 0 min ago")
}

func TestRotateWorkerSecondWorkerExits(t *testing.T) {
	f := newRotFixture(t)
	var second error
	inner := rotMove
	rotMove = func(out, warn io.Writer, r sessionRow, dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun, keepSource bool, guard func(string) error) error {
		// A second rate_limit while the first worker moves.
		var log bytes.Buffer
		second = runRotate(context.Background(), f.opts(), f.getenv, &log)
		if ws := f.waits(); len(ws) != 0 {
			t.Fatalf("the second worker wrote a wait: %+v", ws)
		}
		return inner(out, warn, r, dst, tbl, dryRun, keepSource, guard)
	}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if second != nil {
		t.Fatalf("second worker: %v, want a quiet exit 0", second)
	}
	if f.moves != 1 || !f.hasHook("skipped", "reason=worker running") {
		t.Fatalf("moves=%d hooks=%+v", f.moves, f.hooks())
	}
}

func TestRotateWorkerNoPane(t *testing.T) {
	f := newRotFixture(t)
	f.refs = map[string]string{rotOther: "surface:2"}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantRefused("no cmux pane for this surface")
	f.wantNothingTyped()
}

func TestRotateWorkerNoRowForSurface(t *testing.T) {
	f := newRotFixture(t)
	f.bindings = map[string]string{"surface:1": ""} // the surface binds no session
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantRefused("no cmux pane for this surface")
	f.wantNothingTyped()
}

func TestRotateWorkerPicksHookSurfaceRow(t *testing.T) {
	f := newRotFixture(t)
	// surface:2 binds the same session (an old tab Claude left) and comes first.
	f.refs = map[string]string{rotOther: "surface:2", rotSurface: "surface:7"}
	f.bindings = map[string]string{"surface:2": realBindingFor(rotSID), "surface:7": realBindingFor(rotSID)}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	for _, l := range f.typed {
		if !strings.HasPrefix(l, "surface:7|") {
			t.Fatalf("typed into another surface: %v", f.typed)
		}
	}
	if !f.typedExit() {
		t.Fatal("no /exit")
	}
}

func TestRotateWorkerMoveRefused(t *testing.T) {
	f := newRotFixture(t)
	rotMove = func(io.Writer, io.Writer, sessionRow, claudeprofile.Cell, *claudeprofile.Table, bool, bool, func(string) error) error {
		return errors.New("target already has a transcript")
	}
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	f.wantRefused("target already has a transcript")
}

func TestRotateWorkerNoPromptAfterRefusedMove(t *testing.T) {
	f := newRotFixture(t)
	rotMove = func(io.Writer, io.Writer, sessionRow, claudeprofile.Cell, *claudeprofile.Table, bool, bool, func(string) error) error {
		return errors.New("refusing cross-scope move")
	}
	_ = f.run()
	if len(f.sent) != 0 {
		t.Fatalf("resume prompt sent after a refused move: %v", f.sent)
	}
}

func TestRotateWorkerFrozen(t *testing.T) {
	f := newRotFixture(t)
	var sid string
	rotFrozen = func(g func(string) string) error {
		sid = g("CLAUDE_CODE_SESSION_ID")
		return errors.New("maintenance frozen")
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	if sid != rotSID {
		t.Fatalf("freeze asked for %q", sid)
	}
	f.wantRefused("maintenance freeze")
	f.wantNothingTyped()
}

func TestRotateWorkerNoSurfaceID(t *testing.T) {
	f := newRotFixture(t)
	o := f.opts()
	o.Surface = ""
	if err := runRotate(context.Background(), o, f.getenv, &f.log); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantRefused("no cmux surface id")
	f.wantNothingTyped()
}

func TestRotateWorkerWaitsAfterPid(t *testing.T) {
	f := newRotFixture(t)
	alive := 3
	rotPidAlive = func(pid int) bool {
		if pid != 4242 {
			t.Fatalf("pid %d", pid)
		}
		if f.moves > 0 {
			t.Fatal("moved before the hook ended")
		}
		alive--
		return alive > 0
	}
	o := f.opts()
	o.AfterPID = 4242
	if err := runRotate(context.Background(), o, f.getenv, &f.log); err != nil {
		t.Fatalf("run: %v", err)
	}
	if alive > 0 {
		t.Fatal("did not wait for the hook's pid")
	}
}

func TestRotationOffRotate(t *testing.T) {
	f := newRotFixture(t)
	f.getenv = envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": f.logDir, envRotation: "off"})
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	if f.moves != 0 || len(f.waits()) != 0 {
		t.Fatalf("off: moves=%d waits=%+v — the base changes nothing", f.moves, f.waits())
	}
	if !f.hasHook("refused", "PIGTAIL_ROTATION=off") {
		t.Fatalf("hooks = %+v", f.hooks())
	}
}

func TestRotateWorkerDryRunChangesNothing(t *testing.T) {
	f := newRotFixture(t)
	o := f.opts()
	o.DryRun = true
	if err := runRotate(context.Background(), o, f.getenv, &f.log); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	f.wantNothingTyped()
	if f.moves != 0 || len(f.waits()) != 0 {
		t.Fatalf("dry run moved or wrote a wait")
	}
	out := f.log.String()
	for _, want := range []string{"would quit: guard ok", "would type the resume prompt once ready", "screen read:", "❯"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run says %q, want %q", out, want)
		}
	}
}

// --- the launcher's proto, before /exit ------------------------------------

// withProto: the fixture's worker env with CLAUDE_LAUNCH_PROTO set to v
// ("" = absent), as the hook's environment hands it down.
func (f *rotFixture) withProto(v string) {
	kv := map[string]string{"CLAUDE_HOOK_LOG_DIR": f.logDir, "PIGTAIL_STANDBY_DIR": f.standby}
	if v != "" {
		kv[envLaunchProto] = v
	}
	f.getenv = envOf(kv)
}

func (f *rotFixture) wantProtoRefused(why string) {
	f.t.Helper()
	if err := f.run(); err == nil {
		f.t.Fatalf("run succeeded, want a refusal")
	}
	f.wantRefused(why)
	f.wantNothingTyped()
	if f.typedExit() || f.moves != 0 {
		f.t.Fatalf("typed /exit or moved: typed=%v moves=%d", f.typed, f.moves)
	}
	if !strings.Contains(f.limitWait().Reason, "exec zsh in this tab, then relaunch with c") {
		f.t.Fatalf("reason = %q, want the exec zsh hint", f.limitWait().Reason)
	}
}

func TestRotateWorkerRefusesOldLauncherProto(t *testing.T) {
	f := newRotFixture(t)
	f.withProto("2")
	f.wantProtoRefused("this tab's launcher speaks proto 2 (switching account needs ≥ 3)")
}

func TestRotateWorkerRefusesProtoAbsent(t *testing.T) {
	f := newRotFixture(t)
	f.withProto("")
	f.wantProtoRefused("proto unreadable")
}

func TestRotateWorkerRefusesProtoGarbage(t *testing.T) {
	f := newRotFixture(t)
	f.withProto("abc")
	f.wantProtoRefused("proto unreadable")
}

func TestRotateWorkerProto3Moves(t *testing.T) {
	f := newRotFixture(t)
	f.withProto("3")
	if err := f.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if f.moves != 1 || !f.hasHook("moved", "") {
		t.Fatalf("moves = %d, hooks %+v, want one move", f.moves, f.hooks())
	}
}

func TestRotateWorkerDryRunProtoRefusal(t *testing.T) {
	f := newRotFixture(t)
	f.withProto("2")
	o := f.opts()
	o.DryRun = true
	if err := runRotate(context.Background(), o, f.getenv, &f.log); err == nil {
		t.Fatalf("dry run succeeded, want a refusal")
	}
	f.wantNothingTyped()
	if out := f.log.String(); !strings.Contains(out, "would refuse: not quit — this tab's launcher speaks proto 2") || strings.Contains(out, "would quit") {
		t.Fatalf("dry run says %q, want would refuse", out)
	}
	if f.moves != 0 || len(f.waits()) != 0 {
		t.Fatalf("dry run moved or wrote a wait")
	}
}

// --- the guard before /exit --------------------------------------------------

func (f *rotFixture) wantNotQuit(why string) {
	f.t.Helper()
	if err := f.run(); err == nil {
		f.t.Fatal("want a refusal")
	}
	f.wantNothingTyped()
	f.wantRefused("not quit — " + why)
}

func TestRotateWorkerQuitGuardDraft(t *testing.T) {
	f := newRotFixture(t)
	draft := readFixture(t, "screen-draft.txt")
	f.screen = func() string { return draft }
	f.wantNotQuit("draft in the input")
}

func TestRotateWorkerQuitGuardFocused(t *testing.T) {
	f := newRotFixture(t)
	f.focused = func() string { return rotSurface }
	f.front = func() (string, error) { return "com.cmuxterm.app", nil }
	f.wantNotQuit("pane selected and cmux frontmost")
}

func TestRotateWorkerQuitGuardSelectedOtherAppFront(t *testing.T) {
	f := newRotFixture(t)
	f.focused = func() string { return rotSurface }
	f.front = func() (string, error) { return "com.apple.Safari", nil }
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if n := strings.Count(strings.Join(f.typed, "\n"), "|/exit"); n != 1 {
		t.Fatalf("/exit typed %d times", n)
	}
}

func TestRotateWorkerQuitGuardFrontUnreadable(t *testing.T) {
	for _, front := range []func() (string, error){
		func() (string, error) { return "", errors.New("lsappinfo: no answer") },
		func() (string, error) { return "", nil },
	} {
		f := newRotFixture(t)
		f.focused = func() string { return rotSurface }
		f.front = front
		f.wantNotQuit("front app unreadable")
	}
}

func TestRotateWorkerQuitGuardNotSelectedSkipsFrontApp(t *testing.T) {
	f := newRotFixture(t)
	f.focused = func() string { return rotOther }
	f.front = func() (string, error) { t.Fatal("front app read for a pane not selected"); return "", nil }
	if err := f.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if f.frontHits != 0 {
		t.Fatalf("frontApp called %d times", f.frontHits)
	}
}

func TestRotateWorkerQuitGuardCmuxCheck(t *testing.T) {
	f := newRotFixture(t)
	f.running = func(int) bool { return true }
	f.wantNotQuit("pigtail cmux check running")
}

func TestRotateWorkerQuitGuardUnknownScreen(t *testing.T) {
	// A screen with no input box at all: not quit, said as such.
	f := newRotFixture(t)
	noInput := readFixture(t, "screen-no-input.txt")
	f.screen = func() string { return noInput }
	f.wantNotQuit("no input box on screen")

	// The limit menu (closest real limit-screen shape in the repo): the
	// "You've hit your" marker is seen first, so it reads as a usage-limit
	// wait. Both mean not quit.
	limitMenu := "  ⎿  You've hit your limit · resets 3pm\n\n  What do you want to do?\n  ❯ 1. Stop and wait for limit to reset\n    2. Switch account\n"
	g := newRotFixture(t)
	g.screen = func() string { return limitMenu }
	g.wantNotQuit("no input box on screen")
}

// Rotation exists to /exit a session at its usage limit: the auto-continue it
// cancels costs nothing there, so a boxed limit screen with an empty input
// lets it quit (ScreenAllowsIgnoringUsageLimit); main allowed it too.
func TestRotateWorkerQuitGuardAllowsBoxedUsageLimitScreen(t *testing.T) {
	f := newRotFixture(t)
	idle := readFixture(t, "screen-idle-empty.txt")
	i := strings.Index(idle, "──────────")
	if i < 0 {
		t.Fatal("fixture has no input box")
	}
	limit := "⏺ Usage limit reached · continuing automatically at 3pm\n  ⎿  You've hit your limit · resets 3pm\n\n"
	f.screen = func() string { return limit + idle[i:] }
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if !f.typedExit() {
		t.Fatalf("no /exit typed: %v", f.typed)
	}
}

func TestRotateWorkerQuitGuardSurfaceNotVerified(t *testing.T) {
	f := newRotFixture(t)
	f.listing = func() []peers.CmuxSession {
		return []peers.CmuxSession{{SessionID: rotSID, SurfaceID: rotOther, PID: f.pid, ActiveForSurface: true, ActiveSurfaceSessionID: rotSID}}
	}
	f.wantNotQuit("cmux places it on another surface")
}

func TestRotateWorkerQuitGuardWaitsThenQuits(t *testing.T) {
	f := newRotFixture(t)
	start := f.now
	f.focused = func() string { return rotSurface }
	f.front = func() (string, error) {
		if f.now.Sub(start) < 10*time.Second {
			return "com.cmuxterm.app", nil
		}
		return "com.apple.Safari", nil
	}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if n := strings.Count(strings.Join(f.typed, "\n"), "|/exit"); n != 1 {
		t.Fatalf("/exit typed %d times", n)
	}
}

func TestMoveOneGuardedRechecksBeforeQuit(t *testing.T) {
	f := newRotFixture(t)
	n := 0
	draft := readFixture(t, "screen-draft.txt")
	idle := readFixture(t, "screen-idle-empty.txt")
	f.screen = func() string {
		n++
		if n == 1 {
			return idle // step 6 passes
		}
		return draft // stage=quit sees a draft typed meanwhile
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	if f.moves != 1 {
		t.Fatalf("moves = %d", f.moves)
	}
	f.wantNothingTyped()
	f.wantRefused("not quit — draft in the input")
}

// --- after /exit -------------------------------------------------------------

func TestRotateWorkerRelaunchTypedAfterExit(t *testing.T) {
	f := newRotFixture(t)
	exited := false
	sendLine = func(ref, line string) error {
		f.typed = append(f.typed, ref+"|"+line)
		if line == "/exit" {
			exited = true
		}
		return nil
	}
	inner := f.listing
	f.listing = func() []peers.CmuxSession {
		if exited && len(f.typed) == 1 {
			return nil // Claude gone from cmux's sessions until relaunched
		}
		return inner()
	}
	f.ttys = func() (map[string]string, error) { return map[string]string{rotSurface: "ttys001"}, nil }
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if len(f.typed) != 2 || !strings.Contains(f.typed[1], "--resume "+rotSID) {
		t.Fatalf("typed = %v", f.typed)
	}
	if f.hasHook("relaunch-held", "") {
		t.Fatal("relaunch held")
	}
}

func TestRotateWorkerRelaunchSurfaceGone(t *testing.T) {
	for _, cs := range []struct {
		ttys func() (map[string]string, error)
		why  string
	}{
		{func() (map[string]string, error) { return map[string]string{rotOther: ""}, nil }, "surface gone"},
		{func() (map[string]string, error) { return nil, errors.New("cmux tree: timeout") }, "surface unreadable — cmux tree: timeout"},
	} {
		f := newRotFixture(t)
		f.ttys = cs.ttys
		if err := f.run(); err == nil {
			t.Fatal("want exit ≠ 0")
		}
		if len(f.typed) != 1 || f.typed[0] != "surface:1|/exit" {
			t.Fatalf("typed = %v, want /exit alone", f.typed)
		}
		if !f.hasHook("relaunch-held", "reason="+cs.why) {
			t.Fatalf("hooks = %+v", f.hooks())
		}
	}
}

func TestRotateWorkerRelaunchHeld(t *testing.T) {
	f := newRotFixture(t)
	exited := false
	sendLine = func(ref, line string) error {
		f.typed = append(f.typed, ref+"|"+line)
		exited = exited || line == "/exit"
		return nil
	}
	f.focused = func() string { return rotSurface }
	f.front = func() (string, error) {
		if exited {
			return "com.cmuxterm.app", nil
		}
		return "com.apple.Safari", nil
	}
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	if len(f.typed) != 1 {
		t.Fatalf("typed = %v, want /exit alone", f.typed)
	}
	if ws := f.waits(); len(ws) != 0 {
		t.Fatalf("a wait was written for a session that quit: %+v", ws)
	}
	if len(f.marks) != 1 || f.marks[0] != rotWS {
		t.Fatalf("marks = %v, want the session's workspace", f.marks)
	}
	run := cmux.ResumeLine("/cmux/bin/cmux-claude-wrapper", f.dst.ConfigDir, rotSID)
	if !f.hasHook("relaunch-held", "run="+run) || !f.hasHook("relaunch-held", "pane selected and cmux frontmost") {
		t.Fatalf("hooks = %+v", f.hooks())
	}
}

func TestRotateWorkerRelaunchHeldPillFails(t *testing.T) {
	f := newRotFixture(t)
	f.ttys = func() (map[string]string, error) { return map[string]string{}, nil }
	f.markErr = errors.New("daemon down")
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	if !f.hasHook("relaunch-held", "pill=failed: daemon down") {
		t.Fatalf("hooks = %+v", f.hooks())
	}
}

func TestRotateWorkerMoveFailsAfterExit(t *testing.T) {
	for _, cs := range []struct {
		stage string
		setup func()
	}{
		{"copy", func() {
			copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
				return claudesession.Transcript{}, errors.New("disk full")
			}
		}},
		{"confirm", func() {
			confirmRelaunch = func(relaunchCheck) (int, error) { return 0, errRelaunchNotSeen }
		}},
	} {
		f := newRotFixture(t)
		cs.setup()
		if err := f.run(); err == nil {
			t.Fatalf("%s: want exit ≠ 0", cs.stage)
		}
		if ws := f.waits(); len(ws) != 0 {
			t.Fatalf("%s: wait written: %+v", cs.stage, ws)
		}
		if len(f.marks) != 1 || !f.hasHook("move-failed", "stage="+cs.stage) || !f.hasHook("move-failed", "run=") {
			t.Fatalf("%s: marks=%v hooks=%+v", cs.stage, f.marks, f.hooks())
		}
	}
}

func TestRotateWorkerBindingFailsAfterRelaunch(t *testing.T) {
	f := newRotFixture(t)
	setResumeBinding = func(string, string, string, string, string) error { return errors.New("cmux refused") }
	if err := f.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := lastRotated(rotSID); !ok || len(f.sent) != 1 || len(f.marks) != 1 {
		t.Fatalf(".last/sent/marks = %v %v", f.sent, f.marks)
	}
	if !f.hasHook("moved", "warning=binding") {
		t.Fatalf("hooks = %+v", f.hooks())
	}
	if ws := f.waits(); len(ws) != 0 {
		t.Fatalf("waits = %+v, want none (resumed)", ws)
	}
}

func TestRotateWorkerRemoveFailsAfterRelaunch(t *testing.T) {
	f := newRotFixture(t)
	removeSource = func(claudesession.Transcript) error { return errors.New("busy") }
	if err := f.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.sent) != 1 || len(f.marks) != 0 || !f.hasHook("moved", "warning=remove") {
		t.Fatalf("sent=%v marks=%v hooks=%+v", f.sent, f.marks, f.hooks())
	}
}

// --- the resume prompt -------------------------------------------------------

// seedLimit is the hook's Limit wait, as the base writes it.
func (f *rotFixture) seedLimit() {
	u := f.until
	if _, err := sessionClient(f.getenv).SetSessionWait(context.Background(), rotSID, pigtail.SessionWaitRequest{Type: "limit", Source: "auto", Until: &u}); isNotFound(err) {
		registerSession(context.Background(), relayHookPayload{SessionID: rotSID}, f.getenv)
		_, _ = sessionClient(f.getenv).SetSessionWait(context.Background(), rotSID, pigtail.SessionWaitRequest{Type: "limit", Source: "auto", Until: &u})
	}
}

// moved is the hook for the resume stage only: passes /exit, then turns the
// guard to fail for the relaunched session via the given screens.
func (f *rotFixture) afterMove(fn func()) {
	inner := rotMove
	rotMove = func(out, warn io.Writer, r sessionRow, dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun, keepSource bool, guard func(string) error) error {
		err := inner(out, warn, r, dst, tbl, dryRun, keepSource, guard)
		fn()
		return err
	}
}

func (f *rotFixture) wantResumeFailed(why string) {
	f.t.Helper()
	w := f.limitWait()
	if w.Until != nil {
		f.t.Fatalf("until = %v, want none", w.Until)
	}
	if !strings.HasPrefix(w.Reason, "rotation: moved to j, resume prompt not sent — ") || !strings.Contains(w.Reason, why) {
		f.t.Fatalf("reason = %q", w.Reason)
	}
	if !f.hasHook("resume-failed", "replaced=limit") {
		f.t.Fatalf("hooks = %+v", f.hooks())
	}
}

func TestRotateWorkerSendsResumePrompt(t *testing.T) {
	f := newRotFixture(t)
	f.seedLimit()
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "là où tu t'es arrêté") ||
		!strings.Contains(f.sent[0], "compte m → compte j") {
		t.Fatalf("sent = %v", f.sent)
	}
	if ws := f.waits(); len(ws) != 0 {
		t.Fatalf("waits = %+v, want the Limit cleared", ws)
	}
	if !f.hasHook("resumed", "") {
		t.Fatalf("hooks = %+v", f.hooks())
	}
}

func TestRotateWorkerResumePromptWaitsForIdle(t *testing.T) {
	f := newRotFixture(t)
	moved := false
	var movedAt time.Time
	f.afterMove(func() { moved, movedAt = true, f.now })
	f.status = func(string) string {
		switch {
		case f.prompted:
			return "busy"
		case moved && f.now.Sub(movedAt) < 6*time.Second:
			return "busy" // still restoring
		}
		return "idle"
	}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if len(f.sent) != 1 || f.now.Sub(movedAt) < 6*time.Second {
		t.Fatalf("sent=%v after %s", f.sent, f.now.Sub(movedAt))
	}
}

func TestRotateWorkerResumePromptFocusedPane(t *testing.T) {
	f := newRotFixture(t)
	f.afterMove(func() {
		f.focused = func() string { return rotSurface }
		f.front = func() (string, error) { return "com.cmuxterm.app", nil }
	})
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent = %v", f.sent)
	}
	f.wantResumeFailed("pane selected and cmux frontmost")
}

func TestRotateWorkerResumePromptSelectedOtherAppFront(t *testing.T) {
	f := newRotFixture(t)
	f.afterMove(func() {
		f.focused = func() string { return rotSurface }
		f.front = func() (string, error) { return "com.apple.Safari", nil }
	})
	if err := f.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent = %v", f.sent)
	}
}

func TestRotateWorkerResumePromptFrontUnreadable(t *testing.T) {
	f := newRotFixture(t)
	f.afterMove(func() {
		f.focused = func() string { return rotSurface }
		f.front = func() (string, error) { return "", errors.New("lsappinfo: no answer") }
	})
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent = %v", f.sent)
	}
	f.wantResumeFailed("front app unreadable — ")
}

func TestRotateWorkerResumePromptFrontRecheckedBeforeSend(t *testing.T) {
	f := newRotFixture(t)
	reads := 0
	f.afterMove(func() {
		f.focused = func() string { return rotSurface }
		f.front = func() (string, error) {
			reads++
			if reads%2 == 0 {
				return "com.cmuxterm.app", nil // the (f) re-read sees cmux come to the front
			}
			return "com.apple.Safari", nil
		}
	})
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent = %v", f.sent)
	}
	f.wantResumeFailed("pane selected and cmux frontmost")
}

func TestRotateWorkerResumePromptDraft(t *testing.T) {
	f := newRotFixture(t)
	draft := readFixture(t, "screen-draft.txt")
	f.afterMove(func() { f.screen = func() string { return draft } })
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	if len(f.sent) != 0 {
		t.Fatalf("sent = %v", f.sent)
	}
	f.wantResumeFailed("draft in the input")
}

func TestRotateWorkerResumePromptHoldsDuringCmuxCheck(t *testing.T) {
	t.Run("whole wait", func(t *testing.T) {
		f := newRotFixture(t)
		f.afterMove(func() { f.running = func(int) bool { return true } })
		if err := f.run(); err == nil {
			t.Fatal("want exit ≠ 0")
		}
		if len(f.sent) != 0 {
			t.Fatalf("sent = %v", f.sent)
		}
		f.wantResumeFailed("pigtail cmux check running")
	})
	t.Run("re-read only", func(t *testing.T) {
		f := newRotFixture(t)
		base := 0
		f.afterMove(func() {
			base = f.runCalls
			// calls of the resume guard: 1 (a) free, 2 (f) running, then free.
			f.running = func(n int) bool { return n-base == 2 }
		})
		if err := f.run(); err != nil {
			t.Fatalf("run: %v\n%s", err, f.log.String())
		}
		if len(f.sent) != 1 || f.runCalls-base < 3 {
			t.Fatalf("sent=%v calls=%d: the prompt must wait for the next round", f.sent, f.runCalls-base)
		}
	})
}

func TestRotateWorkerResumePromptSurfaceNotVerified(t *testing.T) {
	for _, ls := range []func(pid int) []peers.CmuxSession{
		func(pid int) []peers.CmuxSession {
			return []peers.CmuxSession{{SessionID: rotSID, SurfaceID: rotOther, PID: pid, ActiveForSurface: true, ActiveSurfaceSessionID: rotSID}}
		},
		func(pid int) []peers.CmuxSession {
			return []peers.CmuxSession{{SessionID: rotSID, SurfaceID: rotSurface, PID: pid + 1, ActiveForSurface: true, ActiveSurfaceSessionID: rotSID}}
		},
	} {
		f := newRotFixture(t)
		f.afterMove(func() { f.listing = func() []peers.CmuxSession { return ls(f.pid) } })
		if err := f.run(); err == nil {
			t.Fatal("want exit ≠ 0")
		}
		if len(f.sent) != 0 {
			t.Fatalf("sent = %v", f.sent)
		}
		w := f.limitWait()
		if !strings.Contains(w.Reason, "another surface") && !strings.Contains(w.Reason, "pid differs") {
			t.Fatalf("reason = %q", w.Reason)
		}
	}
}

func TestRotateWorkerResumePromptSendFails(t *testing.T) {
	f := newRotFixture(t)
	f.sendErr = errors.New("cmux: killed")
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	f.wantResumeFailed("cmux send — cmux: killed")
}

func TestRotateWorkerResumePromptNoTurn(t *testing.T) {
	f := newRotFixture(t)
	f.turnAfter = false
	if err := f.run(); err == nil {
		t.Fatal("want exit ≠ 0")
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent = %v, want one prompt, never a second", f.sent)
	}
	f.wantResumeFailed("no turn started")
}

func TestRotateFocusOKCaseInsensitive(t *testing.T) {
	f := newRotFixture(t)
	f.focused = func() string { return strings.ToLower(rotSurface) }
	f.front = func() (string, error) { return "com.cmuxterm.app.nightly", nil }
	w := &rotateWorker{ctx: context.Background(), log: io.Discard}
	ok, why := w.rotateFocusOK(rotSurface)
	if ok || why != "pane selected and cmux frontmost" || f.frontHits != 1 {
		t.Fatalf("ok=%v why=%q front hits=%d", ok, why, f.frontHits)
	}
}

func TestRotateResumePromptFitsCmux(t *testing.T) {
	t.Parallel()
	p := rotateResumePrompt("other-0123abcd", "other-fedcba98")
	if err := cmux.CheckLine(p); err != nil {
		t.Fatalf("CheckLine: %v", err)
	}
	if n := utf8.RuneCountInString(p); n > 200 {
		t.Fatalf("%d runes", n)
	}
	if !strings.Contains(rotateResumePrompt("m", "j"), "là où tu t'es arrêté") {
		t.Fatal("prompt text")
	}
}

func TestRotateResumePromptLetterlessAccount(t *testing.T) {
	f := newRotFixture(t)
	pickCell = func(*claudeprofile.Table, string, map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		r := []claudeusage.Candidate{{Label: "other-fedcba98", AccountUUID: "acct-dst"}, {Label: "other-0123abcd", AccountUUID: "acct-src"}}
		return f.dst, claudeusage.Choice{Candidate: r[0], Ranking: r, Warnings: []string{}}, nil
	}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.sent) != 1 || utf8.RuneCountInString(f.sent[0]) > 200 || strings.Contains(f.sent[0], "mt") && strings.Contains(f.sent[0], " t ") {
		t.Fatalf("sent = %v", f.sent)
	}
	if !strings.Contains(f.sent[0], "other-0123abcd → compte other-fedcba98") {
		t.Fatalf("sent = %q", f.sent[0])
	}
}

func TestRotationReasonsFit(t *testing.T) {
	long := strings.Repeat("é", 150) // 300 bytes
	f := newRotFixture(t)
	for _, r := range []string{
		"rotation: not quit — front app unreadable — " + long,
		"rotation: moved to other-0123abcd, resume prompt not sent — " + long,
	} {
		got := rotateReason(r)
		if len(got) > 200 || !strings.HasPrefix(got, "rotation: ") || !utf8.ValidString(got) {
			t.Fatalf("rotateReason = %q (%d bytes)", got, len(got))
		}
		u := f.until
		w := &rotateWorker{ctx: context.Background(), opt: f.opts(), getenv: f.getenv, log: io.Discard}
		if err := w.setWait(pigtail.SessionWaitRequest{Type: "limit", Source: "auto", Until: &u, Reason: got}); err != nil {
			t.Fatalf("the daemon refused the reason: %v", err)
		}
		if w := f.limitWait(); w.Reason != got {
			t.Fatalf("stored %q", w.Reason)
		}
	}
	_ = fmt.Sprint
}

// --- retry of a transient refusal (sub_x000000000000013, Task 8) ------------

// journalKinds returns the kinds of the captured journal rows, in order.
func (f *rotFixture) journalKinds() []string {
	var out []string
	for _, e := range f.journal {
		out = append(out, e.Kind)
	}
	return out
}

func (f *rotFixture) wantEnded(summary string) {
	f.t.Helper()
	for _, e := range f.journal {
		if e.Kind == "rotate-retry-ended" {
			if !strings.Contains(e.Summary, summary) || !strings.HasSuffix(e.ID, ":ended") {
				f.t.Fatalf("ended row = %+v, want summary with %q", e, summary)
			}
			return
		}
	}
	f.t.Fatalf("no rotate-retry-ended row: %v", f.journalKinds())
}

func TestRotateRetriesTransientRefusal(t *testing.T) {
	f := newRotFixture(t)
	idle := readFixture(t, "screen-idle-empty.txt")
	noInput := readFixture(t, "screen-no-input.txt")
	// No input box for the first try (the guard polls for 2 min), a box after.
	start := f.now
	f.screen = func() string {
		if f.now.Sub(start) < 3*time.Minute {
			return noInput
		}
		return idle
	}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if f.moves != 1 || !f.typedExit() {
		t.Fatalf("moves=%d typed=%v", f.moves, f.typed)
	}
	var retries []pigtail.JournalEntry
	for _, e := range f.journal {
		if e.Kind == "rotate-retry" {
			retries = append(retries, e)
		}
	}
	if len(retries) != 1 {
		t.Fatalf("rotate-retry rows = %v", f.journalKinds())
	}
	e := retries[0]
	want := "rotate-retry:" + rotSID + ":" + strconv.FormatInt(f.until.Unix(), 10) + ":1"
	if e.ID != want || e.Summary != "7c1d2e3f: limit · retry 1 after: not quit — no input box on screen" {
		t.Fatalf("row = %+v, want id %s", e, want)
	}
}

func TestRotateRetryStopsAfterWindow(t *testing.T) {
	f := newRotFixture(t)
	noInput := readFixture(t, "screen-no-input.txt")
	f.screen = func() string { return noInput }
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantRefused("no input box on screen")
	f.wantNothingTyped()
	f.wantEnded("7c1d2e3f: limit · try ")
	f.wantEnded("window over")
	n := 0
	for _, k := range f.journalKinds() {
		if k == "rotate-retry" {
			n++
		}
	}
	// ~3 min per try (2 min guard + 1 min pause) in a 30 min window.
	if n < 2 || n > 10 {
		t.Fatalf("%d retries: %v", n, f.journalKinds())
	}
	if f.now.Sub(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)) > 40*time.Minute {
		t.Fatalf("ran until %v", f.now)
	}
}

func TestRotateRetryStopsWhenLimitChanged(t *testing.T) {
	f := newRotFixture(t)
	noInput := readFixture(t, "screen-no-input.txt")
	f.screen = func() string { return noInput }
	rotateSleepBase := rotateSleep
	rotateSleep = func(d time.Duration) {
		rotateSleepBase(d)
		if d == rotateRetryEvery { // the pause between two tries: the limit moves
			u := f.until.Add(time.Hour)
			if _, err := sessionClient(f.getenv).SetSessionWait(context.Background(), rotSID, pigtail.SessionWaitRequest{Type: "limit", Source: "auto", Until: &u}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantEnded("limit wait gone")
	f.wantNothingTyped()
}

func TestRotateRetryStopsOnStandby(t *testing.T) {
	// In Standby before the first try: nothing typed, no try.
	f := newRotFixture(t)
	if err := os.WriteFile(filepath.Join(f.standby, rotSID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	f.wantNothingTyped()
	if f.typedExit() || f.moves != 0 {
		t.Fatalf("typed=%v moves=%d", f.typed, f.moves)
	}
	f.wantEnded("standby")

	// Standby entered between two tries: the next one is not made.
	g := newRotFixture(t)
	noInput := readFixture(t, "screen-no-input.txt")
	g.screen = func() string { return noInput }
	base := rotateSleep
	rotateSleep = func(d time.Duration) {
		base(d)
		if d == rotateRetryEvery {
			_ = os.WriteFile(filepath.Join(g.standby, rotSID), nil, 0o600)
		}
	}
	if err := g.run(); err == nil {
		t.Fatal("want a refusal")
	}
	g.wantNothingTyped()
	g.wantEnded("standby")
	n := 0
	for _, k := range g.journalKinds() {
		if k == "rotate-retry" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("journal = %v, want exactly one retry before the Standby stop", g.journalKinds())
	}
}

func TestRotateNoRetryOnNonTransient(t *testing.T) {
	f := newRotFixture(t)
	f.withProto("2")
	slept := false
	base := rotateSleep
	rotateSleep = func(d time.Duration) {
		if d == rotateRetryEvery {
			slept = true
		}
		base(d)
	}
	if err := f.run(); err == nil {
		t.Fatal("want a refusal")
	}
	if slept || len(f.journal) != 0 {
		t.Fatalf("slept=%v journal=%v", slept, f.journalKinds())
	}

	g := newRotFixture(t)
	pickCell = func(*claudeprofile.Table, string, map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		r := []claudeusage.Candidate{{Label: "m", AccountUUID: "acct-src"}}
		return g.src, claudeusage.Choice{Candidate: r[0], Ranking: r}, nil
	}
	if err := g.run(); err == nil {
		t.Fatal("want a refusal")
	}
	if len(g.journal) != 0 {
		t.Fatalf("journal = %v", g.journalKinds())
	}
	g.wantRefused("no other account")
}

// The texts the worker writes are the ones the retry recognises; the proto
// refusal is not among them.
func TestRotationTransientMatchesWorkerTexts(t *testing.T) {
	f := newRotFixture(t)
	noInput := readFixture(t, "screen-no-input.txt")
	f.screen = func() string { return noInput }
	w := &rotateWorker{ctx: context.Background(), opt: f.opts(), getenv: f.getenv, log: &f.log}
	ok, why := w.rotateTypeGuard(rotSID, rotSurface, f.src.ConfigDir)
	if ok || !waittype.RotationTransient(why) {
		t.Fatalf("no-box refusal %q (ok=%v) not transient", why, ok)
	}
	rotReadNative = func(string, int) (claudesession.Native, bool) { return claudesession.Native{}, false }
	if _, why := (&rotateWorker{ctx: context.Background(), opt: f.opts(), getenv: f.getenv, log: &f.log}).rotateTypeGuard(rotSID, rotSurface, t.TempDir()); !waittype.RotationTransient(why) {
		t.Fatalf("no-native refusal %q not transient", why)
	}
	f.withProto("")
	if why := (&rotateWorker{ctx: context.Background(), opt: f.opts(), getenv: f.getenv, log: io.Discard}).protoRefusal(); why == "" || waittype.RotationTransient(why) {
		t.Fatalf("proto refusal %q must be final", why)
	}
}

// The window and the reset are judged again after the one-minute pause, so a
// pause that crosses either ends the loop without another try.
func TestRotateRetryRechecksAfterPause(t *testing.T) {
	for name, tc := range map[string]struct {
		until time.Duration // 0: the fixture's 3 h
		jump  time.Duration // extra time the first pause takes
		want  string
	}{
		"window":    {jump: 40 * time.Minute, want: "window over"},
		"the reset": {until: 150 * time.Second, want: "reset passed"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRotFixture(t)
			if tc.until != 0 {
				f.until = f.now.Add(tc.until)
			}
			noInput := readFixture(t, "screen-no-input.txt")
			slept, screens := false, 0
			f.screen = func() string {
				if slept {
					screens++
				}
				return noInput
			}
			base := rotateSleep
			rotateSleep = func(d time.Duration) {
				base(d)
				if d == rotateRetryEvery && !slept {
					slept = true
					base(tc.jump)
				}
			}
			if err := f.run(); err == nil {
				t.Fatal("want a refusal")
			}
			f.wantEnded(tc.want)
			if screens != 0 {
				t.Fatalf("%d screen reads after the pause: a try ran past the %s", screens, tc.want)
			}
		})
	}
}

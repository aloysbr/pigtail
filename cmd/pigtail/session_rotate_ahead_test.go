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
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The anticipatory mode of the rotation worker (sub_x000000000000023, Tâche 5):
// `pigtail session rotate --ahead`, started at a turn end by the Stop hook when
// the session's account reaches its switch threshold.

const aheadTurn = "p-ahead"

// aheadFixture is rotFixture in the anticipatory mode: the source transcript
// ends on turn aheadTurn, the pick ranks the source m at 7d 98 % over its
// {90, 98} thresholds and the target j fresh, and the seams of the mode are
// faked and counted.
type aheadFixture struct {
	*rotFixture
	trPath   string
	turn     string
	size     int64
	srcSeven float64
	group    claudeusage.PickGroup
	apiErr   func(call int) (claudesession.APIError, bool)
	apiCalls int
	bg       func() ([]string, error)
	starts   []time.Time
	lockFree []bool
	inbox    string
	standby  string
}

func newAheadFixture(t *testing.T) *aheadFixture {
	t.Helper()
	f := newRotFixture(t)
	a := &aheadFixture{rotFixture: f, turn: aheadTurn, srcSeven: 98, group: claudeusage.GroupFresh,
		inbox: t.TempDir(), standby: t.TempDir()}
	// The session runs in cell t (scope work): it reads its own waits at the
	// daemon as itself, which an unscoped session could not (D17).
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "cells:\n  t: { config_dir: " + f.src.ConfigDir + ", scope: globex }\n  mt: { config_dir: " + f.dst.ConfigDir + ", scope: globex }\n"
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv(envScope, "")
	prevScope := flagScope
	flagScope = ""
	t.Cleanup(func() { flagScope = prevScope })
	f.getenv = envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": f.logDir, envLaunchProto: "3",
		"CLAUDE_CONFIG_DIR": f.src.ConfigDir, "PIGTAIL_INBOX_DIR": a.inbox, "PIGTAIL_STANDBY_DIR": a.standby})
	a.trPath = filepath.Join(f.src.ConfigDir, "projects", "-w", rotSID+".jsonl")
	a.writeTurn(a.trPath, aheadTurn, f.now.Add(-time.Minute), false)
	st, err := os.Stat(a.trPath)
	if err != nil {
		t.Fatal(err)
	}
	a.size = st.Size()
	a.apiErr = func(int) (claudesession.APIError, bool) { return claudesession.APIError{}, false }
	a.bg = func() ([]string, error) { return nil, nil }

	savedAPI, savedBg, savedStart := rotLastAPIError, rotBackground, startRotation
	savedReady, savedRestore, savedCopy := rotateAheadReadyTimeout, rotateAheadRestoreTimeout, copyTranscript
	savedWaits := rotSessionWaits
	t.Cleanup(func() {
		rotLastAPIError, rotBackground, startRotation = savedAPI, savedBg, savedStart
		rotateAheadReadyTimeout, rotateAheadRestoreTimeout, copyTranscript = savedReady, savedRestore, savedCopy
		rotSessionWaits = savedWaits
	})
	rotLastAPIError = func(string, string) (claudesession.APIError, bool) {
		a.apiCalls++
		return a.apiErr(a.apiCalls)
	}
	rotBackground = func(func(string) string, string, string, int, string) ([]string, error) { return a.bg() }
	startRotation = func(sid, _ string, until time.Time, _ int) error {
		unlock, held, err := tryRotateLock(sid)
		free := err == nil && !held
		if free {
			unlock()
		}
		a.starts = append(a.starts, until)
		a.lockFree = append(a.lockFree, free)
		return nil
	}
	// The move copies the transcript for real: the worker reads the turn on dst.
	copyTranscript = func(src claudesession.Transcript, dstConfigDir string) (claudesession.Transcript, error) {
		dst := claudesession.Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}
		b, err := os.ReadFile(src.JSONLPath())
		if err != nil {
			return dst, err
		}
		if err := os.MkdirAll(filepath.Dir(dst.JSONLPath()), 0o700); err != nil {
			return dst, err
		}
		return dst, os.WriteFile(dst.JSONLPath(), b, 0o600)
	}
	pickCell = func(_ *claudeprofile.Table, _ string, l map[string]time.Time) (claudeprofile.Cell, claudeusage.Choice, error) {
		if len(l) != 0 {
			t.Fatalf("limited = %v, want none in the anticipatory mode", l)
		}
		return f.dst, a.choice(), nil
	}
	return a
}

func (a *aheadFixture) choice() claudeusage.Choice {
	r := []claudeusage.Candidate{
		{Label: "j", AccountUUID: "acct-dst", Group: a.group, SevenDay: &claudeusage.WindowView{RawPercent: 20}},
		{Label: "m", AccountUUID: "acct-src", Group: claudeusage.GroupOverMargin,
			SevenDay:   &claudeusage.WindowView{RawPercent: a.srcSeven},
			Thresholds: claudeusage.Thresholds{SelectPct: 90, SwitchPct: 98}},
	}
	return claudeusage.Choice{Candidate: r[0], Ranking: r, Warnings: []string{}}
}

// writeTurn writes a transcript that ends on turn id, started at start: its
// prompt and its answer (appended to the file when add).
func (a *aheadFixture) writeTurn(path, id string, start time.Time, add bool) {
	a.t.Helper()
	lines := fmt.Sprintf(`{"type":"user","promptId":%q,"entrypoint":"cli","timestamp":%q,"message":{"role":"user","content":"go"}}`+"\n"+
		`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":"done"}}`+"\n",
		id, start.UTC().Format(time.RFC3339Nano), start.Add(30*time.Second).UTC().Format(time.RFC3339Nano))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		a.t.Fatal(err)
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if add {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	fh, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	if _, err := fh.WriteString(lines); err != nil {
		a.t.Fatal(err)
	}
}

func (a *aheadFixture) appendLine(line string) {
	a.t.Helper()
	fh, err := os.OpenFile(a.trPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	if _, err := fh.WriteString(line + "\n"); err != nil {
		a.t.Fatal(err)
	}
}

func (a *aheadFixture) opts() rotateOpts {
	return rotateOpts{Session: rotSID, Surface: rotSurface, Ahead: true, Turn: a.turn, TranscriptSize: a.size}
}

func (a *aheadFixture) run() error {
	return runRotate(context.Background(), a.opts(), a.getenv, &a.log)
}

// seedWait sets one wait of the session at the test daemon, as the session.
func (a *aheadFixture) seedWait(req pigtail.SessionWaitRequest) {
	a.t.Helper()
	c := sessionClient(a.getenv)
	_, err := c.SetSessionWait(context.Background(), rotSID, req)
	if isNotFound(err) {
		registerSession(context.Background(), relayHookPayload{SessionID: rotSID, Cwd: "/w"}, a.getenv)
		_, err = c.SetSessionWait(context.Background(), rotSID, req)
	}
	if err != nil {
		a.t.Fatalf("seed %+v: %v", req, err)
	}
}

func (a *aheadFixture) seedDeclared(typ, reason string, details string) pigtail.SessionWait {
	a.t.Helper()
	req := pigtail.SessionWaitRequest{Type: typ, Reason: reason, Source: pigtail.WaitSourceDeclared}
	if details != "" {
		req.Details = json.RawMessage(details)
	}
	a.seedWait(req)
	for _, w := range a.waits() {
		if w.Type == typ && w.Source == pigtail.WaitSourceDeclared {
			return w
		}
	}
	a.t.Fatalf("seeded %s wait not found: %+v", typ, a.waits())
	return pigtail.SessionWait{}
}

// endSession replays the SessionEnd hook of the quit session (EndPeerClosing).
func (a *aheadFixture) endSession() {
	a.t.Helper()
	if _, err := sessionClient(a.getenv).PostPeers(context.Background(), pigtail.PeersRequest{
		Peers: []pigtail.PeerSession{{SessionID: rotSID}}, End: true}); err != nil {
		a.t.Fatalf("session end: %v", err)
	}
}

// afterInner wraps the move: the real guarded move, then fn.
func (a *aheadFixture) afterInner(fn func() error) {
	inner := rotMove
	rotMove = func(out, warn io.Writer, r sessionRow, dst claudeprofile.Cell, tbl *claudeprofile.Table, dryRun, keepSource bool, guard func(string) error) error {
		if err := inner(out, warn, r, dst, tbl, dryRun, keepSource, guard); err != nil {
			return err
		}
		return fn()
	}
}

func waitKey(ws []pigtail.SessionWait) string {
	var b strings.Builder
	for _, w := range ws {
		fmt.Fprintf(&b, "%s|%s|%s|%s;", w.ID, w.Type, w.Source, w.Reason)
	}
	return b.String()
}

func (a *aheadFixture) wantAheadRefused(err error, why string) {
	a.t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), rotateAheadPrefix) || !strings.Contains(err.Error(), why) {
		a.t.Fatalf("err = %v, want %q…%s…\n%s", err, rotateAheadPrefix, why, a.log.String())
	}
	if !a.hasHook("ahead-refused", why) {
		a.t.Fatalf("no ahead-refused line with %q: %+v", why, a.hooks())
	}
	if a.moves != 0 && a.typedExit() {
		a.t.Fatalf("/exit typed on a refusal")
	}
	a.wantNothingTyped()
}

func stampExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// --- the move ----------------------------------------------------------------

func TestRotateAheadMovesWithoutPromptOrWait(t *testing.T) {
	a := newAheadFixture(t)
	seeded := a.seedDeclared("review", "PR #12", "")
	before := waitKey(a.waits())
	if err := a.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, a.log.String())
	}
	if a.moves != 1 || !a.typedExit() {
		t.Fatalf("moves=%d typed=%v", a.moves, a.typed)
	}
	if len(a.sent) != 0 {
		t.Fatalf("sent %v, want no resume prompt", a.sent)
	}
	if after := waitKey(a.waits()); after != before || !strings.Contains(after, seeded.ID) {
		t.Fatalf("waits %s, want unchanged %s", after, before)
	}
	if _, ok := lastAhead(rotSID); !ok {
		t.Fatal(".ahead not written")
	}
	if _, ok := lastRotated(rotSID); ok {
		t.Fatal(".last written by the anticipatory mode")
	}
	if !a.hasHook("ahead-moved", "window=7d") || !a.hasHook("ahead-moved", "wait=kept") || !a.hasHook("ahead-moved", "ready=ok") {
		t.Fatalf("hooks = %+v", a.hooks())
	}
	if a.hasHook("moved", "") || a.hasHook("resumed", "") {
		t.Fatalf("limit-mode outcomes written: %+v", a.hooks())
	}
}

func TestRotateAheadRestoresDeclaredWaits(t *testing.T) {
	for _, cs := range []struct {
		name   string
		seed   bool
		after  func(a *aheadFixture)
		want   string
		verify func(t *testing.T, a *aheadFixture, seeded pigtail.SessionWait)
	}{
		{"not given back: redeclared", true, func(*aheadFixture) {}, "wait=redeclared", func(t *testing.T, a *aheadFixture, seeded pigtail.SessionWait) {
			ws := a.waits()
			if len(ws) != 1 || ws[0].Type != "review" || ws[0].Reason != "PR #12" || ws[0].Source != pigtail.WaitSourceDeclared {
				t.Fatalf("waits = %+v, want the review back", ws)
			}
			got, err := sessionClient(a.getenv).GetWait(context.Background(), ws[0].ID)
			if err != nil || got.Details == nil || got.Details.Lead != "PR #12 title" {
				t.Fatalf("details = %+v %v, want the seeded ones", got, err)
			}
		}},
		{"given back by SessionStart: kept", true, func(a *aheadFixture) {
			registerSession(context.Background(), relayHookPayload{SessionID: rotSID, Cwd: "/w"}, a.getenv)
		}, "wait=kept", func(t *testing.T, a *aheadFixture, seeded pigtail.SessionWait) {
			ws := a.waits()
			if len(ws) != 1 || ws[0].ID != seeded.ID {
				t.Fatalf("waits = %+v, want the one seeded (%s)", ws, seeded.ID)
			}
		}},
		{"another declared: superseded", true, func(a *aheadFixture) {
			a.seedWait(pigtail.SessionWaitRequest{Type: "question", Reason: "which lib?", Source: pigtail.WaitSourceDeclared})
		}, "wait=superseded", func(t *testing.T, a *aheadFixture, _ pigtail.SessionWait) {
			ws := a.waits()
			if len(ws) != 1 || ws[0].Type != "question" {
				t.Fatalf("waits = %+v, want the question alone", ws)
			}
		}},
		{"none declared", false, func(*aheadFixture) {}, "wait=none", func(t *testing.T, a *aheadFixture, _ pigtail.SessionWait) {
			for _, w := range a.waits() {
				if w.Source == pigtail.WaitSourceDeclared {
					t.Fatalf("waits = %+v, want no declared one", a.waits())
				}
			}
		}},
	} {
		t.Run(cs.name, func(t *testing.T) {
			a := newAheadFixture(t)
			var seeded pigtail.SessionWait
			if cs.seed {
				seeded = a.seedDeclared("review", "PR #12", `{"url":"https://example.invalid/pull/12","lead":"PR #12 title"}`)
			} else {
				registerSession(context.Background(), relayHookPayload{SessionID: rotSID, Cwd: "/w"}, a.getenv)
			}
			a.afterInner(func() error {
				a.endSession()
				cs.after(a)
				return nil
			})
			start := rotateNow()
			if err := a.run(); err != nil {
				t.Fatalf("run: %v\n%s", err, a.log.String())
			}
			if !a.hasHook("ahead-moved", cs.want) {
				t.Fatalf("hooks = %+v, want %s", a.hooks(), cs.want)
			}
			if cs.want == "wait=redeclared" && rotateNow().Sub(start) < rotateAheadRestoreTimeout {
				t.Fatalf("redeclared after %s, want after the %s probe", rotateNow().Sub(start), rotateAheadRestoreTimeout)
			}
			cs.verify(t, a, seeded)
		})
	}
}

func TestRotateAheadPicksWithoutLimited(t *testing.T) {
	a := newAheadFixture(t) // its pickCell fails on any limited account
	if err := a.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, a.log.String())
	}
	if a.moves != 1 {
		t.Fatalf("moves = %d", a.moves)
	}
}

func TestRotateAheadSystemLineIsNotATurn(t *testing.T) {
	a := newAheadFixture(t)
	a.appendLine(`{"type":"system","subtype":"stop_hook_summary"}`)
	if err := a.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, a.log.String())
	}
	if a.moves != 1 || !a.hasHook("ahead-moved", "") {
		t.Fatalf("moves=%d hooks=%+v", a.moves, a.hooks())
	}
}

// --- refusals ----------------------------------------------------------------

func TestRotateAheadRefusals(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(a *aheadFixture)
		why       string
		immediate bool // refused without the 30 s wait
		noStamp   bool
	}{
		{"over margin", func(a *aheadFixture) { a.group = claudeusage.GroupOverMargin }, "no fresh account under its ceiling", true, false},
		{"no reading", func(a *aheadFixture) { a.group = claudeusage.GroupNoReading }, "no fresh account under its ceiling", true, false},
		{"stale", func(a *aheadFixture) { a.group = claudeusage.GroupStale }, "no fresh account under its ceiling", true, false},
		{"source under", func(a *aheadFixture) { a.srcSeven = 97.9 }, "source under its switch threshold", true, false},
		{"standby", func(a *aheadFixture) {
			_ = os.WriteFile(filepath.Join(a.standby, rotSID), nil, 0o600)
		}, "standby", false, false},
		{"declared done", func(a *aheadFixture) { a.seedDeclared("done", "", "") }, "declared wait done", false, false},
		{"declared wrapped", func(a *aheadFixture) { a.seedDeclared("wrapped", "", "") }, "declared wait wrapped", false, false},
		{"declared working", func(a *aheadFixture) { a.seedDeclared("working", "ci", "") }, "declared wait working", false, false},
		{"held mac", func(a *aheadFixture) {
			a.seedWait(pigtail.SessionWaitRequest{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: os.Getpid()})
		}, "declared wait mac", false, false},
		{"waits unreadable", func(a *aheadFixture) {
			rotSessionWaits = func(context.Context, func(string) string, string) ([]pigtail.SessionWait, error) {
				return nil, errors.New("daemon down")
			}
		}, "waits unreadable — daemon down", false, false},
		{"relay message pending", func(a *aheadFixture) {
			_ = os.WriteFile(filepath.Join(a.inbox, rotSID), nil, 0o600)
		}, "relay message pending", false, false},
		{"assistant line after the offset", func(a *aheadFixture) {
			a.appendLine(`{"type":"assistant","message":{"role":"assistant","content":"more"}}`)
		}, "turn moved on", true, true},
		// B1: a turn taken up again leaves the session busy; the type guard
		// would fail on every poll and hide the final refusal for 30 s.
		{"assistant line and session busy", func(a *aheadFixture) {
			a.appendLine(`{"type":"assistant","message":{"role":"assistant","content":"more"}}`)
			a.status = func(string) string { return "busy" }
		}, "turn moved on", true, true},
		{"another turn id", func(a *aheadFixture) { a.turn = "p-other" }, "turn moved on", true, true},
		{"background work", func(a *aheadFixture) {
			a.bg = func() ([]string, error) { return []string{"cron x"}, nil }
		}, "background work — cron x", false, false},
		{"background unreadable", func(a *aheadFixture) {
			a.bg = func() ([]string, error) { return nil, errors.New("ps failed") }
		}, "background unreadable — ps failed", false, false},
		{"maintenance freeze", func(a *aheadFixture) {
			rotFrozen = func(func(string) string) error { return errors.New("frozen") }
		}, "maintenance freeze — frozen", true, false},
		{"switched ahead recently", func(a *aheadFixture) {
			_ = os.MkdirAll(rotateDir(), 0o700)
			_ = os.WriteFile(rotateAheadPath(rotSID), []byte(a.now.Add(-10*time.Minute).Format(time.RFC3339)), 0o600)
		}, "switched ahead 10 min ago", true, false},
		{"rotated recently", func(a *aheadFixture) {
			_ = os.MkdirAll(rotateDir(), 0o700)
			_ = os.WriteFile(rotateLastPath(rotSID), []byte(a.now.Add(-10*time.Minute).Format(time.RFC3339)), 0o600)
		}, "rotated 10 min ago", true, false},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			a := newAheadFixture(t)
			cs.setup(a)
			before := waitKey(a.waits())
			start := rotateNow()
			err := a.run()
			a.wantAheadRefused(err, cs.why)
			if after := waitKey(a.waits()); after != before {
				t.Fatalf("waits changed: %s → %s", before, after)
			}
			elapsed := rotateNow().Sub(start)
			if cs.immediate && elapsed != 0 {
				t.Fatalf("refused after %s, want at once", elapsed)
			}
			if !cs.immediate && elapsed < rotateAheadReadyTimeout {
				t.Fatalf("refused after %s, want after the %s guard", elapsed, rotateAheadReadyTimeout)
			}
			if got := stampExists(rotateAheadRefusedPath(rotSID)); got == cs.noStamp {
				t.Fatalf(".ahead-refused stamp = %t, want %t", got, !cs.noStamp)
			}
			if len(a.starts) != 0 {
				t.Fatalf("limit worker started: %v", a.starts)
			}
		})
	}
	t.Run("rotation off", func(t *testing.T) {
		a := newAheadFixture(t)
		a.getenv = envOf(map[string]string{"CLAUDE_HOOK_LOG_DIR": a.logDir, envRotation: "off"})
		err := a.run()
		a.wantAheadRefused(err, errRotationOff.Error())
		if a.moves != 0 {
			t.Fatal("moved while off")
		}
	})
}

func TestRotateAheadSkippedOnHeldLock(t *testing.T) {
	a := newAheadFixture(t)
	unlock, held, err := tryRotateLock(rotSID)
	if err != nil || held {
		t.Fatalf("lock: %v %v", held, err)
	}
	defer unlock()
	if err := a.run(); err != nil {
		t.Fatalf("run = %v, want a quiet exit", err)
	}
	if !a.hasHook("ahead-skipped", "") || a.moves != 0 {
		t.Fatalf("moves=%d hooks=%+v", a.moves, a.hooks())
	}
	a.wantNothingTyped()
}

// The session lock is free while the switch is read back (waits restored,
// readiness), so a real limit in that window starts its own worker.
func TestRotateAheadReleasesLockBeforeReadBack(t *testing.T) {
	a := newAheadFixture(t)
	a.seedDeclared("review", "PR #12", "")
	inner := rotSessionWaits
	var free []bool
	moved := false
	a.afterInner(func() error { moved = true; return nil })
	rotSessionWaits = func(ctx context.Context, g func(string) string, sid string) ([]pigtail.SessionWait, error) {
		if moved {
			unlock, held, err := tryRotateLock(sid)
			free = append(free, err == nil && !held)
			if err == nil && !held {
				unlock()
			}
		}
		return inner(ctx, g, sid)
	}
	if err := a.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, a.log.String())
	}
	if len(free) == 0 {
		t.Fatal("no wait read after the move: the probe saw nothing")
	}
	for _, f := range free {
		if !f {
			t.Fatalf("lock held during the read-back: %v", free)
		}
	}
}

func rateLimit(at, reset time.Time) claudesession.APIError {
	return claudesession.APIError{Error: "rate_limit", Status: 429, At: at, ResetsAt: reset}
}

func TestRotateAheadRefusesOnLimitWait(t *testing.T) {
	t.Run("rate limit of this turn: handed to the limit worker", func(t *testing.T) {
		a := newAheadFixture(t)
		reset := a.now.Add(3 * time.Hour)
		a.apiErr = func(int) (claudesession.APIError, bool) { return rateLimit(a.now, reset), true }
		before := waitKey(a.waits())
		start := rotateNow()
		err := a.run()
		a.wantAheadRefused(err, "usage limit hit")
		if rotateNow() != start {
			t.Fatal("not refused at once")
		}
		if len(a.starts) != 1 || !a.starts[0].Equal(reset) || !a.lockFree[0] {
			t.Fatalf("starts=%v lockFree=%v, want one at %v with the lock free", a.starts, a.lockFree, reset)
		}
		if !a.hasHook("ahead-refused", "handoff=limit") || stampExists(rotateAheadRefusedPath(rotSID)) {
			t.Fatalf("hooks=%+v stamp=%t", a.hooks(), stampExists(rotateAheadRefusedPath(rotSID)))
		}
		if waitKey(a.waits()) != before {
			t.Fatal("the worker wrote a wait")
		}
	})
	for _, typ := range []string{"limit", "login", "api_error"} {
		t.Run("auto wait "+typ, func(t *testing.T) {
			a := newAheadFixture(t)
			req := pigtail.SessionWaitRequest{Type: typ, Source: pigtail.WaitSourceAuto}
			if typ == "limit" {
				u := a.now.Add(time.Hour)
				req.Until = &u
			}
			a.seedWait(req)
			before := waitKey(a.waits())
			start := rotateNow()
			err := a.run()
			a.wantAheadRefused(err, "auto wait "+typ)
			if rotateNow() != start || len(a.starts) != 0 {
				t.Fatalf("elapsed=%s starts=%v, want at once and no handoff", rotateNow().Sub(start), a.starts)
			}
			if stampExists(rotateAheadRefusedPath(rotSID)) {
				t.Fatal("a final refusal wrote the backoff stamp")
			}
			if waitKey(a.waits()) != before {
				t.Fatal("the worker wrote a wait")
			}
		})
	}
	t.Run("rate limit already reset", func(t *testing.T) {
		a := newAheadFixture(t)
		a.apiErr = func(int) (claudesession.APIError, bool) { return rateLimit(a.now, a.now.Add(-time.Minute)), true }
		if err := a.run(); err != nil {
			t.Fatalf("run: %v\n%s", err, a.log.String())
		}
		if a.moves != 1 || len(a.starts) != 0 {
			t.Fatalf("moves=%d starts=%v", a.moves, a.starts)
		}
	})
}

// B1: the type guard fails on every poll (pane selected, cmux frontmost); a
// limit that shows up meanwhile is still caught and handed off.
func TestRotateAheadHandsOffWhileGuardHeld(t *testing.T) {
	polls := int(rotateAheadReadyTimeout/rotatePoll) + 1
	for _, cs := range []struct {
		name  string
		after int // rotLastAPIError calls without a limit
	}{
		{"limit at the second poll", 1},
		{"limit after the last poll", polls},
	} {
		t.Run(cs.name, func(t *testing.T) {
			a := newAheadFixture(t)
			a.focused = func() string { return rotSurface }
			a.front = func() (string, error) { return "com.cmuxterm.app", nil }
			reset := a.now.Add(3 * time.Hour)
			a.apiErr = func(n int) (claudesession.APIError, bool) {
				if n <= cs.after {
					return claudesession.APIError{}, false
				}
				return rateLimit(a.now, reset), true
			}
			err := a.run()
			if err == nil || !strings.HasPrefix(err.Error(), rotateAheadPrefix) {
				t.Fatalf("err = %v", err)
			}
			if a.typedExit() {
				t.Fatal("/exit typed")
			}
			if len(a.starts) != 1 || !a.starts[0].Equal(reset) || !a.lockFree[0] {
				t.Fatalf("starts=%v lockFree=%v, want one handoff with the lock free", a.starts, a.lockFree)
			}
			if !a.hasHook("ahead-refused", "handoff=limit") || stampExists(rotateAheadRefusedPath(rotSID)) {
				t.Fatalf("hooks=%+v stamp=%t", a.hooks(), stampExists(rotateAheadRefusedPath(rotSID)))
			}
		})
	}
}

func TestRotateAheadRefusedStamp(t *testing.T) {
	a := newAheadFixture(t)
	a.bg = func() ([]string, error) { return []string{"cron x"}, nil }
	_ = a.run()
	if !stampExists(rotateAheadRefusedPath(rotSID)) {
		t.Fatal("non-final refusal: no stamp")
	}
	if at, ok := lastAheadRefused(rotSID); !ok || !at.Equal(a.now) {
		t.Fatalf("stamp = %v %v", at, ok)
	}
	b := newAheadFixture(t)
	b.appendLine(`{"type":"user","promptId":"p-next","message":{"role":"user","content":"next"}}`)
	_ = b.run()
	if stampExists(rotateAheadRefusedPath(rotSID)) {
		t.Fatal("turn moved on: stamp written")
	}
	c := newAheadFixture(t)
	c.apiErr = func(int) (claudesession.APIError, bool) { return rateLimit(c.now, c.now.Add(time.Hour)), true }
	_ = c.run()
	if stampExists(rotateAheadRefusedPath(rotSID)) {
		t.Fatal("usage limit hit: stamp written")
	}
}

// M2: a rate limit of an earlier turn does not stop the switch.
func TestRotateAheadIgnoresOldRateLimit(t *testing.T) {
	a := newAheadFixture(t)
	a.apiErr = func(int) (claudesession.APIError, bool) {
		return rateLimit(a.now.Add(-2*time.Minute), a.now.Add(3*time.Hour)), true // the turn started at now-1min
	}
	if err := a.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, a.log.String())
	}
	if a.moves != 1 || len(a.starts) != 0 {
		t.Fatalf("moves=%d starts=%v", a.moves, a.starts)
	}
}

// --- after the move ----------------------------------------------------------

func TestRotateAheadRelaunchWarning(t *testing.T) {
	for _, stage := range []string{"binding", "verify", "remove"} {
		t.Run(stage, func(t *testing.T) {
			a := newAheadFixture(t)
			a.seedDeclared("review", "PR #12", "")
			a.afterInner(func() error { return &errAfterRelaunch{Stage: stage, Err: errors.New("cmux refused")} })
			if err := a.run(); err != nil {
				t.Fatalf("run: %v", err)
			}
			if _, ok := lastAhead(rotSID); !ok {
				t.Fatal(".ahead not written")
			}
			if !a.hasHook("ahead-moved", "warning="+stage+": cmux refused") || !a.hasHook("ahead-moved", "wait=kept") {
				t.Fatalf("hooks = %+v", a.hooks())
			}
			wantMarks := 0
			if stage == "binding" {
				wantMarks = 1
			}
			if len(a.marks) != wantMarks {
				t.Fatalf("marks = %v, want %d", a.marks, wantMarks)
			}
		})
	}
}

// m5: a turn started on dst, or a busy session, means the kept wait no longer
// holds: nothing is redeclared.
func TestRotateAheadRestoreSuperseded(t *testing.T) {
	for _, cs := range []struct {
		name  string
		after func(a *aheadFixture)
		want  string
	}{
		{"new turn on dst", func(a *aheadFixture) {
			p := filepath.Join(a.dst.ConfigDir, "projects", "-w", rotSID+".jsonl")
			a.writeTurn(p, "p-new", a.now, true)
		}, "wait=superseded"},
		{"busy on dst", func(a *aheadFixture) {
			a.status = func(cfg string) string {
				if cfg == a.dst.ConfigDir {
					return "busy"
				}
				return "idle"
			}
		}, "wait=superseded"},
		{"neither", func(*aheadFixture) {}, "wait=redeclared"},
	} {
		t.Run(cs.name, func(t *testing.T) {
			a := newAheadFixture(t)
			a.seedDeclared("review", "PR #12", "")
			a.afterInner(func() error {
				a.endSession()
				cs.after(a)
				return nil
			})
			if err := a.run(); err != nil {
				t.Fatalf("run: %v\n%s", err, a.log.String())
			}
			if !a.hasHook("ahead-moved", cs.want) {
				t.Fatalf("hooks = %+v, want %s", a.hooks(), cs.want)
			}
			declared := 0
			for _, w := range a.waits() {
				if w.Source == pigtail.WaitSourceDeclared {
					declared++
				}
			}
			if want := map[bool]int{true: 1, false: 0}[cs.want == "wait=redeclared"]; declared != want {
				t.Fatalf("declared waits = %d, want %d", declared, want)
			}
		})
	}
}

// B2: ready after the relaunch is the session read back and placed on its
// surface — neither the focus nor an idle input box.
func TestRotateAheadReadyAfterRelaunch(t *testing.T) {
	for _, cs := range []struct {
		name  string
		after func(a *aheadFixture)
		ready bool
	}{
		{"not placed on the surface", func(a *aheadFixture) {
			a.listing = func() []peers.CmuxSession { return nil }
		}, false},
		{"another session read back", func(a *aheadFixture) {
			rotReadNative = func(string, int) (claudesession.Native, bool) {
				return claudesession.Native{PID: a.pid, SessionID: rotOther, Status: "idle"}, true
			}
		}, false},
		{"placed and read back", func(*aheadFixture) {}, true},
		{"focus held", func(a *aheadFixture) {
			a.focused = func() string { return rotSurface }
			a.front = func() (string, error) { return "com.cmuxterm.app", nil }
		}, true},
		{"busy", func(a *aheadFixture) { a.status = func(string) string { return "busy" } }, true},
	} {
		t.Run(cs.name, func(t *testing.T) {
			a := newAheadFixture(t)
			a.afterInner(func() error { cs.after(a); return nil })
			start := rotateNow()
			if err := a.run(); err != nil {
				t.Fatalf("run: %v\n%s", err, a.log.String())
			}
			if cs.ready {
				if !a.hasHook("ahead-moved", "ready=ok") || len(a.marks) != 0 {
					t.Fatalf("hooks=%+v marks=%v, want ready=ok and no mark", a.hooks(), a.marks)
				}
				return
			}
			if !a.hasHook("ahead-moved", "ready=held: ") || len(a.marks) != 1 {
				t.Fatalf("hooks=%+v marks=%v, want ready=held and the respawn-failed mark", a.hooks(), a.marks)
			}
			if rotateNow().Sub(start) < rotateReadyTimeout {
				t.Fatalf("held after %s, want %s", rotateNow().Sub(start), rotateReadyTimeout)
			}
		})
	}
	t.Run("release once", func(t *testing.T) {
		n := 0
		w := &rotateWorker{unlock: func() { n++ }}
		w.release()
		w.release()
		if n != 1 {
			t.Fatalf("unlocked %d times", n)
		}
	})
}

func TestRotateAheadFailedAfterExitSaysAhead(t *testing.T) {
	t.Run("move failed", func(t *testing.T) {
		a := newAheadFixture(t)
		a.seedDeclared("review", "PR #12", "")
		before := waitKey(a.waits())
		copyTranscript = func(claudesession.Transcript, string) (claudesession.Transcript, error) {
			return claudesession.Transcript{}, errors.New("disk full")
		}
		err := a.run()
		if err == nil || !strings.HasPrefix(err.Error(), rotateAheadPrefix+"ahead-move-failed") {
			t.Fatalf("err = %v", err)
		}
		if !a.hasHook("ahead-move-failed", "stage=copy") || len(a.marks) != 1 {
			t.Fatalf("hooks=%+v marks=%v", a.hooks(), a.marks)
		}
		if waitKey(a.waits()) != before {
			t.Fatal("the worker wrote a wait")
		}
	})
	t.Run("relaunch held", func(t *testing.T) {
		a := newAheadFixture(t)
		a.ttys = func() (map[string]string, error) { return map[string]string{}, nil }
		err := a.run()
		if err == nil || !strings.HasPrefix(err.Error(), rotateAheadPrefix+"ahead-relaunch-held") {
			t.Fatalf("err = %v", err)
		}
		if !a.hasHook("ahead-relaunch-held", "surface gone") || len(a.marks) != 1 {
			t.Fatalf("hooks=%+v marks=%v", a.hooks(), a.marks)
		}
	})
}

func TestRotateAheadGivesUpAfterReadyTimeout(t *testing.T) {
	a := newAheadFixture(t)
	draft := readFixture(t, "screen-draft.txt")
	a.screen = func() string { return draft }
	start := rotateNow()
	err := a.run()
	a.wantAheadRefused(err, "draft in the input")
	if d := rotateNow().Sub(start); d < rotateAheadReadyTimeout || d >= rotateReadyTimeout {
		t.Fatalf("gave up after %s, want %s", d, rotateAheadReadyTimeout)
	}
	if a.moves != 0 {
		t.Fatalf("moves = %d", a.moves)
	}
}

func TestRotateLimitNotBlockedByRecentAhead(t *testing.T) {
	f := newRotFixture(t)
	_ = os.MkdirAll(rotateDir(), 0o700)
	if err := os.WriteFile(rotateAheadPath(rotSID), []byte(f.now.Add(-5*time.Minute).Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, f.log.String())
	}
	if f.moves != 1 || len(f.sent) != 1 {
		t.Fatalf("moves=%d sent=%v, want the limit rotation and its prompt", f.moves, f.sent)
	}
}

// After a Stop, the bgwork record names the turn that just ended: the worker
// reads it against the last turn, never the one before (errStaleBgRecord).
func TestRotateAheadBackgroundUsesLastTurn(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	bg := t.TempDir()
	getenv := envOf(map[string]string{"PIGTAIL_BGWORK_DIR": bg})
	path := filepath.Join(cfg, "projects", "-w", rotSID+".jsonl")
	a := &aheadFixture{rotFixture: &rotFixture{t: t}}
	now := time.Now()
	a.writeTurn(path, "p1", now.Add(-2*time.Minute), false)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fh.WriteString(`{"type":"system","subtype":"stop_hook_summary","promptId":"p1"}` + "\n")
	_ = fh.Close()
	a.writeTurn(path, "p2", now.Add(-time.Minute), true)
	const pid = 4242
	if err := recordStopBackground(bg, []byte(`{"session_crons":[]}`), rotSID, pid, "p2", nil, now); err != nil {
		t.Fatal(err)
	}
	d := rotBgDeps(getenv, cfg, pid, "")
	d.Native = func(int) (claudesession.Native, bool) {
		return claudesession.Native{PID: pid, SessionID: rotSID, StartedAt: now.Add(-time.Hour).UnixMilli()}, true
	}
	d.Shells = func(int) ([]int, error) { return nil, nil }
	work, err := standbyBackgroundWork(d, rotSID)
	if err != nil || len(work) != 0 {
		t.Fatalf("work=%v err=%v, want nothing", work, err)
	}
}

func TestRotateAheadDryRun(t *testing.T) {
	a := newAheadFixture(t)
	o := a.opts()
	o.DryRun = true
	if err := runRotate(context.Background(), o, a.getenv, &a.log); err != nil {
		t.Fatalf("dry run: %v\n%s", err, a.log.String())
	}
	a.wantNothingTyped()
	if a.moves != 0 || len(a.waits()) != 0 || stampExists(rotateAheadPath(rotSID)) || stampExists(rotateAheadRefusedPath(rotSID)) {
		t.Fatal("the dry run changed something")
	}
	if out := a.log.String(); !strings.Contains(out, "would not type a resume prompt (ahead)") || !strings.Contains(out, "would quit: guard ok") {
		t.Fatalf("dry run says %q", out)
	}
}

func TestSessionRotateAheadRejectsUntil(t *testing.T) {
	for _, args := range [][]string{
		{"--session", rotSID, "--ahead", "--turn", "p1", "--transcript-size", "10", "--until", "2026-10-20T10:00:00Z"},
		{"--session", rotSID, "--ahead", "--transcript-size", "10"},
		{"--session", rotSID, "--ahead", "--turn", "p1"},
	} {
		cmd := sessionRotateCmd()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--ahead") {
			t.Fatalf("%v: err = %v, want a usage error naming --ahead", args, err)
		}
	}
}

func TestRotateAheadArgv(t *testing.T) {
	t.Parallel()
	got := strings.Join(rotateAheadArgv(rotSID, rotSurface, 42, "p1", 1234), " ")
	want := "session rotate --ahead --session " + rotSID + " --surface-id " + rotSurface + " --after-pid 42 --turn p1 --transcript-size 1234"
	if got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

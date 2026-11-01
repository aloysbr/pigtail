package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/frontapp"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Account rotation at the limit (Spec 6–7 of sub_x000000000000008): the
// StopFailure rate_limit hook of an interactive main session starts
// `pigtail session rotate` detached. The worker picks the best account of the
// session's scope (the limited one excluded until its reset), moves the
// session there (moveOneGuarded: transcript kept, relaunched in its pane) and
// types a one-line resume prompt once the relaunched session is ready.
//
// Every keystroke is guarded (rotateTypeGuard: the relay's wake predicates,
// the focus of D11 aside), fail closed: a screen it does not recognise — no
// readable input box, a limit menu included — refuses, never types blind.
// Every refusal is said: the Limit wait's reason "rotation: …", hooks.jsonl
// (hook=rotate) and the worker's log, <state>/rotate/<sid>.log.

const (
	rotateOncePer     = 30 * time.Minute // at most one automatic rotation per session
	rotateAfterPIDMax = 5 * time.Second  // how long the hook's process may take to end
	rotateRetryEvery  = time.Minute      // pause between two tries after a transient refusal
	rotateLimitPrefix = waittype.RotationReasonPrefix
)

// Seams — tests replace them all; no real cmux, Claude or lsappinfo runs.
var (
	rotateReadyTimeout = 2 * time.Minute  // a guard that never passes gives up after this
	rotateTurnTimeout  = 15 * time.Second // the resume prompt must start a turn within this
	rotatePoll         = 2 * time.Second
	rotateTurnPoll     = time.Second
	rotateAfterPIDPoll = 100 * time.Millisecond
	rotateNow          = time.Now
	rotateSleep        = time.Sleep
	rotateCmux         = rotateCmuxOps{
		Sessions:    cmux.SessionsJSONListingBounded,
		SurfaceTTYs: cmux.SurfaceTTYs,
		SurfaceRefs: cmux.SurfaceRefsByID,
		Binding:     cmux.ResumeBinding,
		Focused:     cmux.FocusedSurfaceID,
		ReadScreen:  cmux.ReadScreenBounded,
		Send:        cmux.SendLineBounded,
	}
	frontApp        = frontapp.BundleID
	rotReadNative   = claudesession.ReadNative
	rotCheckRunning = func() bool {
		return cmuxcheck.Running(cmuxcheck.StateDir(), claudesession.PidAlive, time.Now())
	}
	rotPidAlive   = claudesession.PidAlive
	rotLoadTable  = func() (*claudeprofile.Table, error) { return claudeprofile.Load(setup.ConfigDir()) }
	rotFrozen     = spawnFrozen
	rotMove       = moveOneGuarded
	rotSidebar    = rwSidebar
	rotSidebarCli = newClient // the workspace layer is unscoped by decision
	// rotSourceAccount: the accountUuid of the source cell's .claude.json.
	rotSourceAccount = func(c claudeprofile.Cell) (string, error) {
		s := claudeusage.ReadCell(c.UsageInput(), os.ReadFile)
		if s.Err != nil {
			return "", s.Err
		}
		if s.AccountUUID == "" {
			return "", fmt.Errorf("cell %s: not logged in", c.ID)
		}
		return s.AccountUUID, nil
	}
)

// rotateCmuxOps: every cmux call the worker makes, each aimed at a surface by
// UUID except Binding (a surface:N ref, as session move reads it).
type rotateCmuxOps struct {
	Sessions    func() ([]byte, error)
	SurfaceTTYs func() (map[string]string, error)
	SurfaceRefs func() (map[string]string, error)
	Binding     func(ref string) (string, error)
	Focused     func() (string, error)
	ReadScreen  func(surfaceID string) (string, error)
	Send        func(surfaceID, line string) error
}

// rotateReason is every wait reason the worker writes: flattened and cut at
// 200 bytes on a rune boundary (waittype.CleanReason), "rotation: " kept.
func rotateReason(s string) string { return waittype.CleanReason(s) }

// rotateResumePrompt is the one line typed once the moved session is ready
// (Spec 7); from and to are Candidate labels (≤ 14 runes each).
func rotateResumePrompt(from, to string) string {
	return fmt.Sprintf("Reprise auto : la limite d'usage a coupé ton tour, compte %s → compte %s. Reprends là où tu t'es arrêté ; avant de refaire une action, vérifie si elle a déjà eu lieu.", from, to)
}

func rotateDir() string { return filepath.Join(filepath.Dir(spawnreg.DefaultPath()), "rotate") }

func rotateLogPath(sid string) string  { return filepath.Join(rotateDir(), sid+".log") }
func rotateLockPath(sid string) string { return filepath.Join(rotateDir(), sid+".lock") }
func rotateLastPath(sid string) string { return filepath.Join(rotateDir(), sid+".last") }

// tryRotateLock takes the per-session worker lock without waiting (flock
// LOCK_EX|LOCK_NB, model runtimeclaim's witness): held=true means another
// worker runs for this session; err means no lock could be taken at all.
func tryRotateLock(sid string) (unlock func(), held bool, err error) {
	if err := os.MkdirAll(rotateDir(), 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(rotateLockPath(sid), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, false, nil
}

// rotateOpts are the worker's flags.
type rotateOpts struct {
	Session  string
	Surface  string
	Until    time.Time
	AfterPID int
	DryRun   bool
	// Ahead: the anticipatory mode (session_rotate_ahead.go), started at a
	// turn end; Turn and TranscriptSize are that turn and the transcript's
	// size when it ended.
	Ahead          bool
	Turn           string
	TranscriptSize int64
}

// rotateWorker is one run of the worker.
type rotateWorker struct {
	ctx    context.Context
	opt    rotateOpts
	getenv func(string) string
	log    io.Writer
	cwd    string
	// Read by the guards: the workspace cmux places the session in, the
	// native pid the last guard verified, the last screen read.
	wsID   string
	pid    int
	screen string

	// The session lock, released once (release).
	unlock      func()
	locked      bool
	releaseOnce sync.Once
	// The anticipatory mode: the source's config dir, the finality of the
	// last refusal (read by refuse), the waits the last guard read and the
	// declared ones to give back after the switch.
	srcCfg string
	final  bool
	// The why of the last refuse(): the retry loop reads it.
	lastRefusal string
	waitsSeen   []pigtail.SessionWait
	keptWaits   []keptWait
}

// rotateGuardErr is a refusal of a keystroke guard: the reason, said as is.
type rotateGuardErr struct{ why string }

func (e *rotateGuardErr) Error() string { return e.why }

func (w *rotateWorker) logf(format string, a ...any) {
	fmt.Fprintf(w.log, "%s %s\n", rotateNow().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

func (w *rotateWorker) hook(outcome, detail string) {
	hookLogger{hook: "rotate", getenv: w.getenv, now: rotateNow, sid: w.opt.Session, cwd: w.cwd}.write(outcome, detail)
}

// setWait writes the session's Limit wait as the session (sessionClient); a
// session the daemon does not know is registered, then the call runs once more.
func (w *rotateWorker) setWait(req pigtail.SessionWaitRequest) error {
	ctx, cancel := context.WithTimeout(w.ctx, relayHookTimeout)
	defer cancel()
	c := sessionClient(w.getenv)
	_, err := c.SetSessionWait(ctx, w.opt.Session, req)
	if isNotFound(err) {
		registerSession(ctx, relayHookPayload{SessionID: w.opt.Session, Cwd: w.cwd}, w.getenv)
		_, err = c.SetSessionWait(ctx, w.opt.Session, req)
	}
	return err
}

func (w *rotateWorker) clearWait() error {
	ctx, cancel := context.WithTimeout(w.ctx, relayHookTimeout)
	defer cancel()
	c := sessionClient(w.getenv)
	_, err := c.ClearSessionWait(ctx, w.opt.Session, "")
	return err
}

// refuse ends a rotation that never typed /exit: the session is still live,
// under its account; its Limit wait keeps its reset and says why.
func (w *rotateWorker) refuse(why string) error {
	w.lastRefusal = why
	if w.opt.Ahead {
		return w.refuseAhead(why)
	}
	if w.opt.DryRun {
		fmt.Fprintf(w.log, "would refuse: %s\n", why)
		return errors.New(rotateLimitPrefix + why)
	}
	w.logf("refused: %s", why)
	req := pigtail.SessionWaitRequest{Type: string(waittype.Limit), Source: pigtail.WaitSourceAuto, Reason: rotateReason(rotateLimitPrefix + why)}
	if !w.opt.Until.IsZero() {
		u := w.opt.Until
		req.Until = &u
	}
	detail := "reason=" + why
	if err := w.setWait(req); err != nil {
		w.logf("limit wait not rewritten: %v", err)
		detail += " wait=failed: " + err.Error()
	}
	w.hook("refused", detail)
	return errors.New(rotateLimitPrefix + why)
}

// markFailed posts the workspace's respawn-failed mark (no run): the session
// has quit, so no wait of it shows; the pill "Blocked · respawn failed"
// holds until Claude comes back on the surface. "" when posted, else why not.
func (w *rotateWorker) markFailed() string {
	if w.wsID == "" {
		return "no workspace id"
	}
	ctx, cancel := context.WithTimeout(w.ctx, relayHookTimeout)
	defer cancel()
	if _, err := rotSidebar(ctx, rotSidebarCli(), w.wsID, &pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: w.opt.Surface}); err != nil {
		return err.Error()
	}
	return ""
}

// protoRefusal: why this tab's launcher cannot take a relaunch on another
// cell ("" when it can). Fail closed: an absent or unreadable proto refuses.
func (w *rotateWorker) protoRefusal() string {
	proto, text := launchProtoOf(envLaunchProto + "=" + w.getenv(envLaunchProto))
	const fix = "exec zsh in this tab, then relaunch with c"
	switch {
	case proto <= 0:
		w.logf("launcher proto %s", text)
		return fmt.Sprintf("not quit — this tab's launcher proto unreadable (switching account needs ≥ %d): %s", launchProtoFreshCell, fix)
	case proto < launchProtoFreshCell:
		return fmt.Sprintf("not quit — this tab's launcher speaks proto %d (switching account needs ≥ %d): %s", proto, launchProtoFreshCell, fix)
	}
	return ""
}

// lastRotated reads <sid>.last: when this session last rotated.
func lastRotated(sid string) (time.Time, bool) {
	b, err := os.ReadFile(rotateLastPath(sid))
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	return t, err == nil
}

// tooSoon is the 30-min guard against a ping-pong (Review Focus 2). The
// anticipatory mode also reads its own .ahead; the limit mode reads .last
// only (a recent switch ahead never blocks a limit).
func (w *rotateWorker) tooSoon() (string, bool) {
	if w.opt.Ahead {
		if at, ok := lastAhead(w.opt.Session); ok {
			if ago := rotateNow().Sub(at); ago < rotateOncePer {
				return fmt.Sprintf("switched ahead %d min ago", int(ago.Minutes())), true
			}
		}
	}
	last, ok := lastRotated(w.opt.Session)
	if !ok {
		return "", false
	}
	if ago := rotateNow().Sub(last); ago < rotateOncePer {
		return fmt.Sprintf("rotated %d min ago", int(ago.Minutes())), true
	}
	return "", false
}

// waitAfterPID waits for the hook's process to end (5 s at most): /exit typed
// during the StopFailure hook would land in a session still handling it.
func (w *rotateWorker) waitAfterPID() {
	if w.opt.AfterPID <= 0 {
		return
	}
	deadline := rotateNow().Add(rotateAfterPIDMax)
	for rotPidAlive(w.opt.AfterPID) {
		if !rotateNow().Before(deadline) {
			w.logf("hook pid %d still alive after %s, going on", w.opt.AfterPID, rotateAfterPIDMax)
			return
		}
		rotateSleep(rotateAfterPIDPoll)
	}
}

// hookRow is the inventory row of the hook's surface, never the first row of
// the session id: several surfaces may bind one sid, and every guard reads
// --surface-id.
func (w *rotateWorker) hookRow(tbl *claudeprofile.Table) (sessionRow, string) {
	refs, err := rotateCmux.SurfaceRefs()
	if err != nil {
		return sessionRow{}, "surface unreadable — " + err.Error()
	}
	ref := ""
	for id, r := range refs {
		if strings.EqualFold(id, w.opt.Surface) {
			ref = r
		}
	}
	if ref == "" {
		return sessionRow{}, "no cmux pane for this surface"
	}
	rows, unreadable, ambiguous := trackedInventory([]string{ref}, rotateCmux.Binding, tbl)
	for _, a := range ambiguous {
		w.logf("inventory: %v", a)
	}
	if len(unreadable) > 0 {
		return sessionRow{}, "surface unreadable — resume binding of " + ref
	}
	for _, r := range rows {
		if r.SessionID == w.opt.Session && r.SurfaceRef == ref {
			return r, ""
		}
	}
	return sessionRow{}, "no cmux pane for this surface"
}

// rotateFocusOK is D11: a pane cmux does not show selected may be typed into;
// the selected one only while cmux is not the frontmost app. An unreadable
// focus or front app refuses.
func (w *rotateWorker) rotateFocusOK(surfaceID string) (bool, string) {
	focused, err := rotateCmux.Focused()
	if err != nil {
		return false, "focus unreadable — " + err.Error()
	}
	if !strings.EqualFold(focused, surfaceID) {
		return true, ""
	}
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	b, err := frontApp(ctx)
	if err == nil && strings.TrimSpace(b) == "" {
		err = errors.New("empty bundle id")
	}
	if err != nil {
		w.logf("front app: unreadable — %v", err)
		return false, "front app unreadable — " + err.Error()
	}
	w.logf("front app: %s", b)
	if b == "com.cmuxterm.app" || strings.HasPrefix(b, "com.cmuxterm.") {
		return false, "pane selected and cmux frontmost"
	}
	return true, ""
}

// rotateTypeGuard runs, in the relay's order (typeGuarded, relay_wake.go),
// the predicates every keystroke into a live Claude session passes, the focus
// aside (D11): (a) no cmux check running, (b) the session's native pid, (c)
// cmux places it, with that pid, on this surface, (d) the focus and an empty
// input box, (e) still the same session, idle, (f) the cmux check and the
// focus read again right before the send.
func (w *rotateWorker) rotateTypeGuard(sid, surfaceID, cfgDir string) (bool, string) {
	if rotCheckRunning() {
		return false, "pigtail cmux check running"
	}
	n, ok := findNative(cfgDir, sid)
	if !ok {
		return false, waittype.RotationNoNativeFile
	}
	raw, err := rotateCmux.Sessions()
	if err != nil {
		return false, "cmux sessions list — " + err.Error()
	}
	ls, err := peers.ParseCmuxSessions(raw)
	if err != nil {
		return false, "cmux sessions list — " + err.Error()
	}
	for _, s := range ls {
		if s.SessionID == sid && s.WorkspaceID != "" {
			w.wsID = s.WorkspaceID
		}
	}
	if ok, why := relay.SurfaceVerified(ls, sid, surfaceID, n.PID); !ok {
		return false, why
	}
	if ok, why := w.rotateFocusOK(surfaceID); !ok {
		return false, why
	}
	screen, err := rotateCmux.ReadScreen(surfaceID)
	if err != nil {
		return false, "screen unreadable — " + err.Error()
	}
	w.screen = screen
	if ok, why := relay.ScreenAllowsIgnoringUsageLimit(screen, false); !ok {
		return false, why
	}
	re, ok := rotReadNative(cfgDir, n.PID)
	if !ok || re.SessionID != sid || re.Status != "idle" {
		return false, "left idle during the checks"
	}
	if rotCheckRunning() {
		return false, "pigtail cmux check running"
	}
	if ok, why := w.rotateFocusOK(surfaceID); !ok {
		return false, why
	}
	w.pid = n.PID
	return true, ""
}

// rotateSurfacePresent: after /exit Claude is gone from cmux's session
// listing, so the shell-route guard only checks the surface still exists.
func rotateSurfacePresent(surfaceID string) (bool, string) {
	m, err := rotateCmux.SurfaceTTYs()
	if err != nil {
		return false, "surface unreadable — " + err.Error()
	}
	if _, ok := m[strings.ToUpper(surfaceID)]; !ok {
		return false, "surface gone"
	}
	return true, ""
}

// relaunchGuard is the shell-route guard before the typed resume line: no
// cmux check, the focus of D11, the surface still there; the check and the
// focus read again right before the send.
func (w *rotateWorker) relaunchGuard(surfaceID string) (bool, string) {
	if rotCheckRunning() {
		return false, "pigtail cmux check running"
	}
	if ok, why := w.rotateFocusOK(surfaceID); !ok {
		return false, why
	}
	if ok, why := rotateSurfacePresent(surfaceID); !ok {
		return false, why
	}
	if rotCheckRunning() {
		return false, "pigtail cmux check running"
	}
	return w.rotateFocusOK(surfaceID)
}

// until runs check every rotatePoll until it passes or rotateReadyTimeout
// ends; the last refusal is returned.
func (w *rotateWorker) until(what string, check func() (bool, string)) (bool, string) {
	return w.untilWithin(what, rotateReadyTimeout, func() (bool, string, bool) {
		ok, why := check()
		return ok, why, false
	})
}

// moveGuard is the guard moveOneGuarded asks before each keystroke.
func (w *rotateWorker) moveGuard(src claudeprofile.Cell) func(string) error {
	return func(stage string) error {
		switch stage {
		case moveStageQuit:
			if w.opt.Ahead {
				if ok, why, final := w.quitGuard(src); !ok {
					w.final = final
					return &rotateGuardErr{why: "not quit — " + why}
				}
				return nil
			}
			if ok, why := w.rotateTypeGuard(w.opt.Session, w.opt.Surface, src.ConfigDir); !ok {
				return &rotateGuardErr{why: "not quit — " + why}
			}
		case moveStageRelaunch:
			if ok, why := w.until("relaunch", func() (bool, string) { return w.relaunchGuard(w.opt.Surface) }); !ok {
				return &rotateGuardErr{why: "relaunch held — " + why}
			}
		}
		return nil
	}
}

// labelOf is the Candidate label of an account in a ranking, never a cell id.
func labelOf(ranking []claudeusage.Candidate, uuid string) string {
	for _, c := range ranking {
		if c.AccountUUID == uuid {
			return c.Label
		}
	}
	short := uuid
	if len(short) > 8 {
		short = short[:8]
	}
	return "other-" + short
}

// runRotate is the worker: nil when the session moved and resumed, or when
// another worker already handles it; an error otherwise, each one said.
func runRotate(ctx context.Context, o rotateOpts, getenv func(string) string, log io.Writer) error {
	w := &rotateWorker{ctx: ctx, opt: o, getenv: getenv, log: log}
	if o.Ahead {
		w.logf("rotate ahead %s surface %q turn %s size %d dry-run=%t", o.Session, o.Surface, o.Turn, o.TranscriptSize, o.DryRun)
	} else {
		w.logf("rotate %s surface %q until %s dry-run=%t", o.Session, o.Surface, o.Until.UTC().Format(time.RFC3339), o.DryRun)
	}

	// 1. The hook's process ends first.
	w.waitAfterPID()

	// 2. Switched off, or frozen by a maintenance.
	if rotationOff(getenv) {
		w.logf("refused: %v", errRotationOff)
		if o.Ahead {
			w.hook("ahead-refused", "reason="+errRotationOff.Error())
			return errors.New(rotateAheadPrefix + errRotationOff.Error())
		}
		w.hook("refused", "reason="+errRotationOff.Error())
		return errors.New(rotateLimitPrefix + errRotationOff.Error())
	}
	frozenEnv := func(k string) string {
		if k == "CLAUDE_CODE_SESSION_ID" {
			return o.Session
		}
		return getenv(k)
	}
	if err := rotFrozen(frozenEnv); err != nil {
		return w.refuse("maintenance freeze — " + err.Error())
	}

	// 3. One worker per session, for its whole run; one rotation per 30 min.
	if !o.DryRun {
		unlock, held, err := tryRotateLock(o.Session)
		if err != nil {
			return w.refuse("worker lock — " + err.Error())
		}
		if held {
			w.logf("skipped: another worker runs for this session")
			if o.Ahead {
				w.hook("ahead-skipped", "reason=worker running")
			} else {
				w.hook("skipped", "reason=worker running")
			}
			return nil
		}
		w.unlock, w.locked = unlock, true
		defer w.release()
	}
	if why, soon := w.tooSoon(); soon {
		return w.refuse(why)
	}

	// 4-9, retried while a transient refusal lifts (retryRotate).
	attempt := func() error {
		// 4. The hook's surface and its inventory row.
		if o.Surface == "" {
			return w.refuse("no cmux surface id")
		}
		tbl, err := rotLoadTable()
		if err != nil {
			return w.refuse("cell table — " + err.Error())
		}
		row, why := w.hookRow(tbl)
		if why != "" {
			return w.refuse(why)
		}
		w.cwd = row.Cwd
		src, ok := tbl.Cell(row.CellID)
		if !ok {
			return w.refuse("session sits in an unknown config dir: " + row.ConfigDir)
		}
		w.srcCfg = src.ConfigDir
		if o.Ahead {
			return w.runAhead(tbl, row, src)
		}

		// 5. The pick, the limited account excluded until its reset.
		acct, err := rotSourceAccount(src)
		if err != nil {
			return w.refuse("source account unreadable — " + err.Error())
		}
		dst, ch, err := pickCell(tbl, src.Scope, map[string]time.Time{acct: o.Until})
		for _, l := range rotationWarnings(ch, err, "") {
			w.logf("%s", l)
		}
		if err != nil {
			return w.refuse("no account picked — " + err.Error())
		}
		if ch.AccountUUID == acct || dst.ID == src.ID {
			why := "no other account"
			if len(ch.Warnings) > 0 {
				why += " — " + strings.Join(ch.Warnings, "; ")
			}
			return w.refuse(why)
		}
		// The launcher of this tab must speak the cross-cell handoff, else the
		// relaunch on dst would be refused after the quit (respawn's guard, same
		// source: CLAUDE_LAUNCH_PROTO of the hook's Claude, inherited by the worker).
		if why := w.protoRefusal(); why != "" {
			return w.refuse(why)
		}
		from, to := labelOf(ch.Ranking, acct), ch.Label
		w.logf("picked %s (cell %s) for %s (cell %s)", to, dst.ID, from, src.ID)

		// 6. The guard before /exit, on the source session.
		if o.DryRun {
			ok, why := w.rotateTypeGuard(o.Session, o.Surface, src.ConfigDir)
			fmt.Fprintf(log, "would move %s from %s (%s) to %s (%s)\n", o.Session, from, src.ID, to, dst.ID)
			if ok {
				fmt.Fprintln(log, "would quit: guard ok")
			} else {
				fmt.Fprintf(log, "would not quit: %s\n", why)
			}
			fmt.Fprintf(log, "screen read:\n%s\n-- end of screen --\n", w.screen)
			fmt.Fprintln(log, "would type the resume prompt once ready")
			return nil
		}
		if ok, why := w.until("quit", func() (bool, string) { return w.rotateTypeGuard(o.Session, o.Surface, src.ConfigDir) }); !ok {
			return w.refuse("not quit — " + why)
		}

		// 7. The move, its keystrokes guarded; .last read once more right before.
		if why, soon := w.tooSoon(); soon {
			return w.refuse(why)
		}
		moveErr := rotMove(log, log, row, dst, tbl, false, false, w.moveGuard(src))
		var after *errAfterExit
		var relaunched *errAfterRelaunch
		switch {
		case errors.As(moveErr, &after):
			return w.failedAfterExit(after)
		case errors.As(moveErr, &relaunched):
			// The session runs on dst already: the rotation is done.
		case moveErr != nil:
			var g *rotateGuardErr
			if errors.As(moveErr, &g) {
				return w.refuse(g.why)
			}
			return w.refuse(moveErr.Error())
		}

		// 8. Moved.
		if err := os.WriteFile(rotateLastPath(o.Session), []byte(rotateNow().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
			w.logf("%s not written: %v", rotateLastPath(o.Session), err)
			// The 30-min ping-pong guard reads .last: without it, a second limit may
			// move the session again at once. Said in hooks.jsonl, not only the log.
			w.hook("last-write-failed", "30-min guard off: "+err.Error())
		}
		detail := "from=" + src.ID + " to=" + dst.ID
		if relaunched != nil {
			detail += " warning=" + relaunched.Stage + ": " + relaunched.Err.Error()
		}
		w.logf("moved %s → %s", src.ID, dst.ID)
		w.hook("moved", detail)

		// 9. The resume prompt.
		resumeErr := w.resume(o.Session, o.Surface, dst, from, to)
		if relaunched != nil && relaunched.Stage == "binding" {
			if why := w.markFailed(); why != "" {
				w.logf("respawn-failed mark not posted: %s", why)
				w.hook("moved", "pill=failed: "+why)
			}
		}
		return resumeErr
	}
	return w.retryRotate(attempt)
}

// failedAfterExit: the session quit and did not come back on dst — it is not
// live, so no wait shows; the workspace mark does, and the log names the line
// that resumes it.
func (w *rotateWorker) failedAfterExit(e *errAfterExit) error {
	outcome, detail := "move-failed", "stage="+e.Stage+" reason="+e.Err.Error()
	var g *rotateGuardErr
	if errors.As(e.Err, &g) {
		outcome, detail = "relaunch-held", "reason="+strings.TrimPrefix(g.why, "relaunch held — ")
	}
	prefix := rotateLimitPrefix
	if w.opt.Ahead {
		outcome, prefix = "ahead-"+outcome, rotateAheadPrefix
	}
	detail += " run=" + e.Run
	if why := w.markFailed(); why != "" {
		detail += " pill=failed: " + why
	}
	w.logf("%s: %s", outcome, detail)
	w.hook(outcome, detail)
	return fmt.Errorf("%s%s: %w — resume it with: %s", prefix, outcome, e.Err, e.Run)
}

// resume types the resume prompt once the relaunched session is ready, once,
// and checks a turn starts; every failure rewrites the Limit wait, without a
// reset (the new account is not limited).
func (w *rotateWorker) resume(sid, surfaceID string, dst claudeprofile.Cell, from, to string) error {
	ok, why := w.until("resume", func() (bool, string) { return w.rotateTypeGuard(sid, surfaceID, dst.ConfigDir) })
	if !ok {
		return w.resumeFailed(to, why)
	}
	if err := rotateCmux.Send(surfaceID, rotateResumePrompt(from, to)); err != nil {
		return w.resumeFailed(to, "cmux send — "+err.Error())
	}
	w.logf("resume prompt typed")
	deadline := rotateNow().Add(rotateTurnTimeout)
	for {
		if n, ok := rotReadNative(dst.ConfigDir, w.pid); ok && n.SessionID == sid && n.Status != "idle" {
			break
		}
		if !rotateNow().Before(deadline) {
			return w.resumeFailed(to, "no turn started")
		}
		rotateSleep(rotateTurnPoll)
	}
	detail := ""
	if err := w.clearWait(); err != nil {
		detail = "wait=failed: " + err.Error()
		w.logf("limit wait not cleared: %v", err)
	}
	w.logf("resumed")
	w.hook("resumed", detail)
	return nil
}

func (w *rotateWorker) resumeFailed(to, why string) error {
	msg := "moved to " + to + ", resume prompt not sent — " + why
	req := pigtail.SessionWaitRequest{Type: string(waittype.Limit), Source: pigtail.WaitSourceAuto, Reason: rotateReason(rotateLimitPrefix + msg)}
	replaced := "limit"
	if !w.opt.Until.IsZero() {
		replaced += " " + w.opt.Until.Local().Format("15:04")
	}
	detail := "reason=" + why + " replaced=" + replaced
	if err := w.setWait(req); err != nil {
		detail += " wait=failed: " + err.Error()
	}
	w.logf("resume-failed: %s", msg)
	w.hook("resume-failed", detail)
	return errors.New(rotateLimitPrefix + msg)
}

func sessionRotateCmd() *cobra.Command {
	var o rotateOpts
	var until string
	cmd := &cobra.Command{
		Use:    "rotate",
		Short:  "internal: move a session to another account — rate-limited, resumed with a prompt (StopFailure hook); or --ahead, past its account's switch threshold at a turn end, no prompt (Stop hook)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !validSessionID(o.Session) {
				return fmt.Errorf("invalid --session %q: expected a canonical UUID", o.Session)
			}
			if o.Ahead {
				switch {
				case until != "":
					return errors.New("--until cannot be used with --ahead: the anticipatory mode hits no limit")
				case o.Turn == "":
					return errors.New("--ahead needs --turn (the turn that just ended)")
				case !cmd.Flags().Changed("transcript-size") || o.TranscriptSize < 0:
					return errors.New("--ahead needs --transcript-size (the transcript's size at the turn end)")
				}
			}
			if until != "" {
				t, err := time.Parse(time.RFC3339, until)
				if err != nil {
					return fmt.Errorf("invalid --until %q: %w", until, err)
				}
				o.Until = t
			}
			pinWorkerLocale()
			signal.Ignore(syscall.SIGHUP)
			return runRotate(context.Background(), o, os.Getenv, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&o.Session, "session", "", "the session id")
	cmd.Flags().StringVar(&o.Surface, "surface-id", "", "the cmux surface UUID the hook ran in (CMUX_SURFACE_ID)")
	cmd.Flags().StringVar(&until, "until", "", "the limit's reset, RFC 3339")
	cmd.Flags().IntVar(&o.AfterPID, "after-pid", 0, "wait for this process (the hook) to end first, 5 s at most")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "run the checks, read the screen, say what would be done, change nothing")
	cmd.Flags().BoolVar(&o.Ahead, "ahead", false, "anticipatory mode: switch at a turn end, past the account's switch threshold, no resume prompt")
	cmd.Flags().StringVar(&o.Turn, "turn", "", "with --ahead: the turn that just ended (its promptId)")
	cmd.Flags().Int64Var(&o.TranscriptSize, "transcript-size", 0, "with --ahead: the transcript's size in bytes at the turn end")
	return cmd
}

// rotateWorkerArgv is the worker's argv after the executable (seam).
var rotateWorkerArgv = func(sid, surface string, until time.Time, afterPID int) []string {
	return []string{"session", "rotate", "--session", sid, "--surface-id", surface,
		"--until", until.UTC().Format(time.RFC3339), "--after-pid", strconv.Itoa(afterPID)}
}

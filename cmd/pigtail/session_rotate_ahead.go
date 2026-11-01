package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The anticipatory mode of the rotation worker (sub_x000000000000023):
// `pigtail session rotate --ahead`, started by the Stop hook when the
// session's account reaches its switch threshold. It moves the session at its
// turn end to an account under its ceiling — no resume prompt (the turn
// ended), no Limit wait (no limit was hit): every refusal is said in
// hooks.jsonl (ahead-refused) and the worker's log only. A real limit seen
// meanwhile is handed to the limit worker (aheadHandoff).

const (
	rotateAheadPrefix     = "rotation ahead: "
	rotateAheadRefusedFor = 10 * time.Minute // the hook's backoff after a non-final refusal
)

var (
	rotateAheadReadyTimeout   = 30 * time.Second // the quit guard of the anticipatory mode gives up after this
	rotateAheadRestoreTimeout = 20 * time.Second // how long the daemon may take to give the declared wait back
)

// aheadSkipWaits: the session said « leave me » — no switch.
var aheadSkipWaits = []waittype.Type{waittype.Done, waittype.Wrapped, waittype.Working, waittype.Mac}

// aheadLimitWaits: a real cut (spec 4b), the limit mode's business.
var aheadLimitWaits = []waittype.Type{waittype.Limit, waittype.Login, waittype.APIError}

func rotateAheadPath(sid string) string { return filepath.Join(rotateDir(), sid+".ahead") }
func rotateAheadRefusedPath(sid string) string {
	return filepath.Join(rotateDir(), sid+".ahead-refused")
}

func readStamp(path string) (time.Time, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	return t, err == nil
}

func writeStamp(path string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// lastAhead reads <sid>.ahead: when this session last switched ahead.
func lastAhead(sid string) (time.Time, bool) { return readStamp(rotateAheadPath(sid)) }

// lastAheadRefused reads <sid>.ahead-refused: the last non-final refusal of
// the anticipatory mode (worker) or ahead-held (hook).
func lastAheadRefused(sid string) (time.Time, bool) { return readStamp(rotateAheadRefusedPath(sid)) }

// writeAheadRefused writes or refreshes <sid>.ahead-refused.
func writeAheadRefused(sid string, now time.Time) error {
	return writeStamp(rotateAheadRefusedPath(sid), now)
}

// rotateAheadArgv is the anticipatory worker's argv after the executable (seam).
var rotateAheadArgv = func(sid, surface string, afterPID int, turn string, size int64) []string {
	return []string{"session", "rotate", "--ahead", "--session", sid, "--surface-id", surface,
		"--after-pid", strconv.Itoa(afterPID), "--turn", turn, "--transcript-size", strconv.FormatInt(size, 10)}
}

// Seams of the anticipatory mode — tests replace them.
var (
	// rotWaitType: the type in the daemon's wait marker of the session (its
	// last wait only); "" without a marker. The hook's cheap prefilter, never
	// the worker's guard.
	rotWaitType = func(getenv func(string) string, sid string) waittype.Type {
		if !validMarkerKey(sid) {
			return ""
		}
		b, err := os.ReadFile(filepath.Join(waitMarkerDir(getenv), sid))
		if err != nil {
			return ""
		}
		line, _, _ := strings.Cut(string(b), "\n")
		return waittype.Type(strings.TrimSpace(line))
	}
	// rotSessionWaits: every wait of the session, read at the daemon as the
	// session; an absent session has none.
	rotSessionWaits = func(ctx context.Context, getenv func(string) string, sid string) ([]pigtail.SessionWait, error) {
		ctx, cancel := context.WithTimeout(ctx, relayHookTimeout)
		defer cancel()
		ps, err := sessionClient(getenv).Peers(ctx, true)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			if p.SessionID == sid {
				return p.Waits, nil
			}
		}
		return nil, nil
	}
	// rotWaitDetails: the typed content of a wait record, to redeclare it whole.
	rotWaitDetails = func(ctx context.Context, getenv func(string) string, id string) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, relayHookTimeout)
		defer cancel()
		w, err := sessionClient(getenv).GetWait(ctx, id)
		if err != nil {
			return nil, err
		}
		if w.Details == nil {
			return nil, nil
		}
		return json.Marshal(w.Details)
	}
	rotLastAPIError = func(cfg, sid string) (claudesession.APIError, bool) {
		path, err := rotTranscriptPath(cfg, sid)
		if err != nil {
			return claudesession.APIError{}, false
		}
		return claudesession.LastAPIError(path)
	}
	rotConversationSince = func(cfg, sid string, offset int64) (bool, error) {
		path, err := rotTranscriptPath(cfg, sid)
		if err != nil {
			return false, err
		}
		return claudesession.ConversationSince(path, offset)
	}
	rotBackground = func(getenv func(string) string, cfg, sid string, pid int, screen string) ([]string, error) {
		return standbyBackgroundWork(rotBgDeps(getenv, cfg, pid, screen), sid)
	}
)

// rotTranscriptPath is the session's transcript under config dir cfg.
func rotTranscriptPath(cfg, sid string) (string, error) {
	tr, ok, err := claudesession.Find(sid, []string{cfg})
	if err != nil {
		return "", err
	}
	if !ok {
		return "", claudesession.ErrNoTranscript
	}
	return tr.JSONLPath(), nil
}

// rotLastTurn is the turn the session's transcript under cfg ends on.
func rotLastTurn(cfg, sid string) (claudesession.Turn, error) {
	path, err := rotTranscriptPath(cfg, sid)
	if err != nil {
		return claudesession.Turn{}, err
	}
	return claudesession.ReadLastTurn(path)
}

// rotBgDeps are the background readings of the worker: the caller is the
// session's pid (the detached worker descends from no Claude, as pigtaild's
// pause), the screen the one the guard read, and the record's turn is the
// LAST turn — after a Stop, the record names the turn that just ended
// (PreviousTurnID would name the one before, errStaleBgRecord every time).
func rotBgDeps(getenv func(string) string, cfg string, pid int, screen string) standbyBgDeps {
	return standbyBgDeps{
		Caller: func() (int, bool, error) { return pid, pid > 0, nil },
		Native: func(p int) (claudesession.Native, bool) { return claudesession.ReadNative(cfg, p) },
		Transcript: func(session string) (string, bool, error) {
			tr, ok, err := claudesession.Find(session, []string{cfg})
			if err != nil || !ok {
				return "", ok, err
			}
			return tr.JSONLPath(), true, nil
		},
		PrevTurn: func(path string) (string, bool) {
			t, err := claudesession.ReadLastTurn(path)
			return t.ID, err == nil
		},
		Record: func(session string, p int, turn string) (bgRecord, error) {
			return readBgRecord(bgWorkDir(getenv), session, p, turn)
		},
		Pending:   claudesession.PendingBackground,
		Ancestors: func() ([]int, error) { return nil, nil },
		Shells:    claudesession.BashToolShells,
		Screen:    func() (string, error) { return screen, nil },
	}
}

// keptWait is a declared wait read before the switch, to give back after it.
type keptWait struct {
	wait    pigtail.SessionWait
	details json.RawMessage
}

// release frees the session lock once (handoff, then the deferred call).
func (w *rotateWorker) release() {
	w.releaseOnce.Do(func() {
		if w.unlock != nil {
			w.unlock()
		}
		w.locked = false
	})
}

// srcConfigDir: the source cell's config dir once known, else the hook's.
func (w *rotateWorker) srcConfigDir() string {
	if w.srcCfg != "" {
		return w.srcCfg
	}
	return claudeConfigDir(w.getenv)
}

// currentRateLimit: a rate_limit of the current turn (At at or after the
// turn's start, M2) whose reset is to come.
func (w *rotateWorker) currentRateLimit(cfg string) (claudesession.APIError, bool) {
	e, ok := rotLastAPIError(cfg, w.opt.Session)
	if !ok || e.Error != "rate_limit" || !e.ResetsAt.After(rotateNow()) {
		return claudesession.APIError{}, false
	}
	t, err := rotLastTurn(cfg, w.opt.Session)
	if err != nil || e.At.Before(t.Start) {
		return claudesession.APIError{}, false
	}
	return e, true
}

// aheadLimit is the real-limit check, independent of the screen: final when
// a limit is there (the limit mode's business).
func (w *rotateWorker) aheadLimit(cfg string) (bool, string, bool) {
	if _, ok := w.currentRateLimit(cfg); ok {
		return false, "usage limit hit", true
	}
	ws, err := rotSessionWaits(w.ctx, w.getenv, w.opt.Session)
	if err != nil {
		return false, "waits unreadable — " + err.Error(), false
	}
	for _, sw := range ws {
		if slices.Contains(aheadLimitWaits, waittype.Type(sw.Type)) {
			return false, "auto wait " + sw.Type, true
		}
	}
	w.waitsSeen = ws
	return true, "", false
}

// aheadTurnCheck: the turn the hook saw is still the last one, with nothing
// said since. Final either way: a turn gone on is not waited out.
func (w *rotateWorker) aheadTurnCheck(cfg string) (bool, string, bool) {
	moved, err := rotConversationSince(cfg, w.opt.Session, w.opt.TranscriptSize)
	if err != nil {
		return false, "transcript unreadable — " + err.Error(), true
	}
	if moved {
		return false, "turn moved on", true
	}
	t, err := rotLastTurn(cfg, w.opt.Session)
	if err != nil {
		return false, "transcript unreadable — " + err.Error(), true
	}
	if t.ID != w.opt.Turn {
		return false, "turn moved on", true
	}
	return true, "", false
}

// aheadQuiet is the rest, from the most decisive to the dearest (after
// aheadLimit, whose waits it reads).
func (w *rotateWorker) aheadQuiet(cfg string) (bool, string, bool) {
	sid := w.opt.Session
	if validMarkerKey(sid) {
		if _, err := os.Stat(filepath.Join(inboxMarkerDir(w.getenv), sid)); err == nil {
			return false, "relay message pending", false
		}
	}
	if inStandby(w.getenv, sid) {
		return false, "standby", false
	}
	var kept []keptWait
	for _, sw := range w.waitsSeen {
		if slices.Contains(aheadSkipWaits, waittype.Type(sw.Type)) {
			return false, "declared wait " + sw.Type, false
		}
		if sw.Source == pigtail.WaitSourceDeclared {
			k := keptWait{wait: sw}
			if sw.ID != "" {
				d, err := rotWaitDetails(w.ctx, w.getenv, sw.ID)
				if err != nil {
					w.logf("wait %s: details unreadable, kept without them — %v", sw.Type, err)
				}
				k.details = d
			}
			kept = append(kept, k)
		}
	}
	w.keptWaits = kept
	work, err := rotBackground(w.getenv, cfg, sid, w.pid, w.screen)
	if err != nil {
		return false, "background unreadable — " + err.Error(), false
	}
	if len(work) > 0 {
		return false, "background work — " + strings.Join(work, ", "), false
	}
	return true, "", false
}

// quitGuard is the guard before /exit (B1, round 3): the limit, then the
// turn — both before the type guard, so a busy session or a held focus never
// hides them —, then the type guard (never final), then the rest.
func (w *rotateWorker) quitGuard(src claudeprofile.Cell) (bool, string, bool) {
	if ok, why, final := w.aheadLimit(src.ConfigDir); !ok {
		return false, why, final
	}
	if ok, why, final := w.aheadTurnCheck(src.ConfigDir); !ok {
		return false, why, final
	}
	if ok, why := w.rotateTypeGuard(w.opt.Session, w.opt.Surface, src.ConfigDir); !ok {
		return false, why, false
	}
	return w.aheadQuiet(src.ConfigDir)
}

// aheadHandoff is the one place the anticipatory mode hands a session to the
// limit worker (B1): with the session lock held, a rate_limit of the current
// turn → the lock is released first, then the limit worker started (the one
// the StopFailure started has exited « skipped » on this lock).
func (w *rotateWorker) aheadHandoff() (bool, string) {
	if !w.opt.Ahead || w.opt.DryRun || !w.locked {
		return false, ""
	}
	e, ok := w.currentRateLimit(w.srcConfigDir())
	if !ok {
		return false, ""
	}
	w.release()
	if err := startRotation(w.opt.Session, w.opt.Surface, e.ResetsAt, 0); err != nil {
		w.logf("limit worker not started: %v", err)
		return true, "handoff=failed: " + err.Error()
	}
	w.logf("handed to the limit worker (reset %s)", e.ResetsAt.UTC().Format(time.RFC3339))
	return true, "handoff=limit"
}

// refuseAhead ends an anticipatory switch that never typed /exit: no wait is
// written; the refusal is said, and a non-final one stamps the hook's backoff.
func (w *rotateWorker) refuseAhead(why string) error {
	final := w.final
	w.final = false
	if w.opt.DryRun {
		fmt.Fprintf(w.log, "would refuse: %s\n", why)
		return errors.New(rotateAheadPrefix + why)
	}
	handed, hd := w.aheadHandoff()
	w.logf("refused: %s", why)
	detail := "reason=" + why
	if hd != "" {
		detail += " " + hd
	}
	if !final && !handed {
		if err := writeAheadRefused(w.opt.Session, rotateNow()); err != nil {
			detail += " stamp=failed: " + err.Error()
		}
	}
	w.hook("ahead-refused", detail)
	return errors.New(rotateAheadPrefix + why)
}

// untilWithin runs check every rotatePoll until it passes, refuses for good
// (final), or d ends; the last refusal is returned and its finality kept in
// w.final.
func (w *rotateWorker) untilWithin(what string, d time.Duration, check func() (bool, string, bool)) (bool, string) {
	deadline := rotateNow().Add(d)
	last := ""
	for {
		ok, why, final := check()
		if ok {
			w.final = false
			return true, ""
		}
		if why != last {
			w.logf("%s: held — %s", what, why)
			last = why
		}
		if final {
			w.final = true
			return false, why
		}
		if !rotateNow().Before(deadline) {
			w.final = false
			return false, why
		}
		rotateSleep(rotatePoll)
	}
}

// restoreDeclared gives the session back the declared waits it had before
// the switch, once: the /exit's SessionEnd took them, the daemon usually
// gives them back at the SessionStart of the same id (restoreEndedWaits).
// Never two declarations: a wait the daemon gave back is not redeclared.
func (w *rotateWorker) restoreDeclared(dst claudeprofile.Cell) string {
	if len(w.keptWaits) == 0 {
		return "none"
	}
	deadline := rotateNow().Add(rotateAheadRestoreTimeout)
	for {
		ws, err := rotSessionWaits(w.ctx, w.getenv, w.opt.Session)
		if err == nil {
			switch declaredState(w.keptWaits, ws) {
			case "kept":
				return "kept"
			case "other":
				return "superseded"
			}
		} else {
			w.logf("restore: waits unreadable — %v", err)
		}
		if !rotateNow().Before(deadline) {
			break
		}
		rotateSleep(rotatePoll)
	}
	// m5: a turn already started on dst, or a busy session: the wait no
	// longer holds.
	if t, err := rotLastTurn(dst.ConfigDir, w.opt.Session); err == nil && t.ID != w.opt.Turn {
		return "superseded"
	}
	if n, ok := findNative(dst.ConfigDir, w.opt.Session); ok {
		if re, ok := rotReadNative(dst.ConfigDir, n.PID); ok && re.SessionID == w.opt.Session && re.Status == "busy" {
			return "superseded"
		}
	}
	for _, k := range w.keptWaits {
		req := pigtail.SessionWaitRequest{Type: k.wait.Type, Reason: k.wait.Reason, Source: pigtail.WaitSourceDeclared, Details: k.details}
		if err := w.setWait(req); err != nil {
			w.logf("declared wait %s not given back: %v", k.wait.Type, err)
			return "failed: " + err.Error()
		}
	}
	return "redeclared"
}

// declaredState: "kept" when every kept wait is declared again (type and
// reason), "other" when another declared wait stands, "" when none yet.
func declaredState(kept []keptWait, ws []pigtail.SessionWait) string {
	var declared []pigtail.SessionWait
	for _, sw := range ws {
		if sw.Source == pigtail.WaitSourceDeclared {
			declared = append(declared, sw)
		}
	}
	if len(declared) == 0 {
		return ""
	}
	match := func(k keptWait) bool {
		return slices.ContainsFunc(declared, func(sw pigtail.SessionWait) bool { return sw.Type == k.wait.Type && sw.Reason == k.wait.Reason })
	}
	for _, sw := range declared {
		if !slices.ContainsFunc(kept, func(k keptWait) bool { return sw.Type == k.wait.Type && sw.Reason == k.wait.Reason }) {
			return "other"
		}
	}
	for _, k := range kept {
		if !match(k) {
			return ""
		}
	}
	return "kept"
}

// postRelaunchReady: the relaunched session is read back under dst (any
// status) and cmux places it, with its pid, on the surface. Read only:
// neither the focus nor the input box — nothing is typed.
func (w *rotateWorker) postRelaunchReady(dst claudeprofile.Cell) (bool, string) {
	sid := w.opt.Session
	return w.until("ready", func() (bool, string) {
		n, ok := findNative(dst.ConfigDir, sid)
		if !ok {
			return false, "no native session file"
		}
		re, ok := rotReadNative(dst.ConfigDir, n.PID)
		if !ok || re.SessionID != sid {
			return false, "relaunched session not read back"
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
		return relay.SurfaceVerified(ls, sid, w.opt.Surface, n.PID)
	})
}

// aheadPick: the pick of the scope with no account limited, then the
// anticipatory target. "" with the target, else why there is none.
func (w *rotateWorker) aheadPick(tbl *claudeprofile.Table, src claudeprofile.Cell, acct string) (claudeprofile.Cell, claudeusage.Choice, string) {
	dst, ch, err := pickCell(tbl, src.Scope, nil)
	for _, l := range rotationWarnings(ch, err, "") {
		w.logf("%s", l)
	}
	if err != nil {
		return dst, ch, "no account picked — " + err.Error()
	}
	if _, why := claudeusage.AheadTarget(ch, acct); why != "" {
		return dst, ch, why
	}
	if dst.ID == src.ID {
		return dst, ch, claudeusage.AheadNoFreshTarget
	}
	return dst, ch, ""
}

// aheadSwitchDetail is window=… pct=… switch=… of the source in the ranking.
func aheadSwitchDetail(ch claudeusage.Choice, acct string) string {
	for _, c := range ch.Ranking {
		if c.AccountUUID == acct {
			win, pct, _ := c.OverSwitch()
			return fmt.Sprintf("window=%s pct=%s switch=%s", win, fmtPct(pct), fmtPct(c.Thresholds.SwitchAt()))
		}
	}
	return ""
}

func fmtPct(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// aheadMoved ends a switch that ran on dst: .ahead, the declared waits given
// back, the relaunch read back; no resume prompt, no wait cleared.
func (w *rotateWorker) aheadMoved(src, dst claudeprofile.Cell, sw string, relaunched *errAfterRelaunch) error {
	if err := writeStamp(rotateAheadPath(w.opt.Session), rotateNow()); err != nil {
		w.logf("%s not written: %v", rotateAheadPath(w.opt.Session), err)
		w.hook("ahead-write-failed", "30-min guard off: "+err.Error())
	}
	wait := w.restoreDeclared(dst)
	ready, why := w.postRelaunchReady(dst)
	detail := "from=" + src.ID + " to=" + dst.ID
	if sw != "" {
		detail += " " + sw
	}
	detail += " wait=" + wait
	if ready {
		detail += " ready=ok"
	} else {
		detail += " ready=held: " + why
	}
	if relaunched != nil {
		detail += " warning=" + relaunched.Stage + ": " + relaunched.Err.Error()
	}
	if !ready || (relaunched != nil && relaunched.Stage == "binding") {
		if why := w.markFailed(); why != "" {
			w.logf("respawn-failed mark not posted: %s", why)
			detail += " pill=failed: " + why
		}
	}
	w.logf("switched ahead %s → %s", src.ID, dst.ID)
	w.hook("ahead-moved", detail)
	return nil
}

// runAhead is runRotate's anticipatory branch, once the source cell is known:
// the pick (no account limited), the guard before /exit (30 s), the move,
// then aheadMoved.
func (w *rotateWorker) runAhead(tbl *claudeprofile.Table, row sessionRow, src claudeprofile.Cell) error {
	o := w.opt
	acct, err := rotSourceAccount(src)
	if err != nil {
		return w.refuse("source account unreadable — " + err.Error())
	}
	dst, ch, why := w.aheadPick(tbl, src, acct)
	if why != "" {
		return w.refuse(why)
	}
	if why := w.protoRefusal(); why != "" {
		return w.refuse(why)
	}
	sw := aheadSwitchDetail(ch, acct)
	from, to := labelOf(ch.Ranking, acct), ch.Label
	w.logf("picked %s (cell %s) for %s (cell %s), %s", to, dst.ID, from, src.ID, sw)

	if o.DryRun {
		ok, why, _ := w.quitGuard(src)
		fmt.Fprintf(w.log, "would move %s ahead from %s (%s) to %s (%s), %s\n", o.Session, from, src.ID, to, dst.ID, sw)
		if ok {
			fmt.Fprintln(w.log, "would quit: guard ok")
		} else {
			fmt.Fprintf(w.log, "would not quit: %s\n", why)
		}
		fmt.Fprintf(w.log, "screen read:\n%s\n-- end of screen --\n", w.screen)
		fmt.Fprintln(w.log, "would not type a resume prompt (ahead)")
		return nil
	}
	if ok, why := w.untilWithin("quit", rotateAheadReadyTimeout, func() (bool, string, bool) { return w.quitGuard(src) }); !ok {
		return w.refuse("not quit — " + why)
	}
	if why, soon := w.tooSoon(); soon {
		return w.refuse(why)
	}
	moveErr := rotMove(w.log, w.log, row, dst, tbl, false, false, w.moveGuard(src))
	var after *errAfterExit
	var relaunched *errAfterRelaunch
	switch {
	case errors.As(moveErr, &after):
		return w.failedAfterExit(after)
	case errors.As(moveErr, &relaunched):
		// The session runs on dst already: the switch is done (M4).
	case moveErr != nil:
		var g *rotateGuardErr
		if errors.As(moveErr, &g) {
			return w.refuse(g.why)
		}
		return w.refuse(moveErr.Error())
	}
	// The session runs on dst: the lock is not needed for the read-back (waits
	// restored ≤ 20 s, readiness), and a real limit in that window must reach
	// its own worker, not "skipped".
	w.release()
	return w.aheadMoved(src, dst, sw, relaunched)
}

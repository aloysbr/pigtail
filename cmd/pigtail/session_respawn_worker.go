package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Seams — tests replace them all; no real process is ever signalled in tests.
var (
	rwNative     = claudesession.ReadNative        // (cfg, pid) → status, sessionId
	rwInStandby  = inStandby                       // (getenv, session id) → in Standby (marker predicate, D16)
	rwReadScreen = cmux.ReadScreen                 // called with run.SurfaceID (UUID, stable over a 2 h wait), not surface:N
	rwBashShells = claudesession.BashToolShells    // (pid) → shells
	rwShellInfo  = claudesession.BashToolShellInfo // (pid) → shells with command and age
	rwPidAlive   = claudesession.PidAlive
	rwKill       = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
	rwProcs      = func() ([]claudesession.Proc, error) { return claudeProcs() }
	rwEnviron    = func(pid int) (string, error) { return processEnviron(pid) }
	rwSetBinding = cmux.SetResumeBindingMode
	rwWrapper    = resolveWrapper
	// rwSidebar posts the run's state as a pill on its workspace (D16); nil
	// clears it. Always the run's workspace UUID, never the caller's pane
	// variables the worker inherited (#36). A final state goes with its run
	// (EndCmuxRespawn) and returns what the daemon did about it (nil: a
	// daemon older than the notice).
	rwSidebar = func(ctx context.Context, c *pigtail.Client, wsID string, req *pigtail.CmuxRespawnRequest) (*pigtail.CmuxRespawnNotice, error) {
		switch {
		case req == nil:
			return nil, c.ClearCmuxRespawn(ctx, wsID)
		case req.State == pigtail.CmuxRespawnWaiting:
			return nil, c.MarkCmuxRespawn(ctx, wsID, *req)
		default:
			return c.EndCmuxRespawn(ctx, wsID, *req)
		}
	}
	// rwResolveAlerts tells the daemon a relaunch of the subject succeeded:
	// its open respawn alerts close (D5, subject « abandon respawn visible »).
	rwResolveAlerts = func(ctx context.Context, c *pigtail.Client, subjectID, cause string) error {
		_, err := c.ResolveSubjectAlerts(ctx, subjectID, cause)
		return err
	}
	rwAttach = func(ctx context.Context, c *pigtail.Client, subjectID, wsID string) error {
		_, err := c.AttachCmuxWorkspace(ctx, subjectID, pigtail.AttachCmuxWorkspaceRequest{
			CmuxWorkspaceID: wsID, Source: pigtail.CmuxSourcePtnew,
		})
		return err
	}
	// rwInterrupt installs the SIGTERM/SIGINT handler (notifyInterrupt: runs
	// cleanup, then exits). Tests capture the cleanup and call it.
	rwInterrupt   = notifyInterrupt
	rwNow         = time.Now
	rwPoll        = time.Second
	rwSettle      = 2 * time.Second
	rwExitTimeout = 20 * time.Second
	rwSeenTimeout = 30 * time.Second
	rwSidTimeout  = 15 * time.Second
	rwPillRefresh = time.Minute // re-post an unchanged waiting pill (a daemon restart forgets it)
)

// workerLocale is pinned at the worker's start and inherited by every ps it
// runs: macOS ps escapes non-ASCII argv bytes under LC_ALL=C (« où » →
// « oM-CM-9 »), and the verify compares the prompt byte for byte.
const workerLocale = "en_US.UTF-8"

func pinWorkerLocale() { _ = os.Setenv("LC_ALL", workerLocale) }

// rwWait is one unmet condition of the idle wait. key is the wait the
// sidebar pill shows (pigtail.CmuxRespawnWait*: draft, background, screen);
// "" shows none: the normal end of the agent's last turn, busy, a running
// foreground — a pill there would blink with every respawn.
type rwWait struct{ key, reason string }

// rwEnd is how the watched session stands, besides waiting.
type rwEnd int

const (
	rwLive    rwEnd = iota // running: wait or close it
	rwEnded                // gone on its own
	rwParked               // parked as a background job: it no longer hosts the conversation
	rwStandby              // in Standby (D4 A, D16): never closed, the respawn gives up
)

// rwWorker is one armed run being carried out.
type rwWorker struct {
	ctx context.Context
	c   *pigtail.Client
	reg *spawnreg.Registry
	run respawnRun
	log io.Writer

	mu          sync.Mutex
	killed      bool // SIGTERM sent to the old Claude
	interrupted bool
	final       *spawnreg.Entry
	// The sidebar pill (D16): the wait key posted ("" = none) and when, and
	// the first failure to post one — reported once, in the run's record.
	pillKey string
	pillAt  time.Time
	pillErr string
	// sid is the <key>.sid this run wrote and has not withdrawn yet (nil =
	// none); one claim per write (relaunch_files.go).
	sid *relaunchfile.SidClaim
}

func (w *rwWorker) logf(format string, a ...any) {
	fmt.Fprintf(w.log, "%s %s\n", rwNow().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

func (w *rwWorker) promptPath() string {
	return strings.TrimSuffix(w.run.RelaunchFile, ".sid") + ".prompt"
}
func (w *rwWorker) handoffPath() string { return handoffPath(w.run.RelaunchFile) }

// withdraw removes the launcher files: a vanished handoff is « pigtail gave
// up » to the launcher, which then shows its usual prompt. <key>.sid goes
// through its claim (relaunchfile.SidClaim.Release): only if pigtail wrote it and it still
// holds that id.
//
// In shell mode (D1) there are no launcher files (RelaunchFile is empty:
// promptPath() would be ".prompt", relative to the worker's cwd): only the
// relaunch script goes.
func (w *rwWorker) withdraw() {
	if w.run.Relaunch == relaunchShell {
		_ = os.Remove(respawnScriptPath(w.run.ID))
		return
	}
	_ = os.Remove(w.handoffPath())
	_ = os.Remove(w.promptPath())
	w.mu.Lock()
	sid := w.sid
	w.sid = nil
	w.mu.Unlock()
	sid.Release()
}

// entry is the record base every append of this run repeats.
func (w *rwWorker) entry(result string) spawnreg.Entry {
	r := w.run
	return spawnreg.Entry{TS: rwNow().UTC(), Kind: spawnreg.KindRespawn, ID: r.ID, SubjectID: r.SubjectID,
		WorkspaceID: r.WorkspaceID, Cell: r.Cell, FreshCell: r.FreshCell, Cwd: r.Cwd, Prompt: r.Prompt, Mode: r.Mode, Result: result,
		SurfaceID: r.SurfaceID, SurfaceRef: r.SurfaceRef, WorkerPID: os.Getpid(), OldSessionID: r.SessionID,
		Relaunch: r.Relaunch, Model: r.Model}
}

// postPill sends one sidebar call (req nil = clear) and returns the daemon's
// notice report for a final state; a failure is kept for the record, the
// first one only, on one line. Called under w.mu.
func (w *rwWorker) postPill(req *pigtail.CmuxRespawnRequest) (*pigtail.CmuxRespawnNotice, error) {
	n, err := rwSidebar(w.ctx, w.c, w.run.WorkspaceID, req)
	if err != nil && w.pillErr == "" {
		w.pillErr = "⚠ sidebar pill not posted (" + cmux.OneLine(err.Error(), 160) + ")"
	}
	return n, err
}

// syncPill makes the sidebar show wt's wait: a new key is posted, the same
// one re-posted every rwPillRefresh, none clears the pill. Under w.mu, and
// never once the run has its final result: the final pill always comes last.
func (w *rwWorker) syncPill(wt *rwWait) {
	key := ""
	if wt != nil {
		key = wt.key
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.final != nil {
		return
	}
	switch {
	case key != "" && (key != w.pillKey || rwNow().Sub(w.pillAt) >= rwPillRefresh):
		changed := key != w.pillKey
		_, err := w.postPill(&pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnWaiting, Wait: key, SurfaceID: w.run.SurfaceID, PID: os.Getpid()})
		w.pillKey, w.pillAt = key, rwNow() // a failure waits for the refresh too
		if err != nil && changed {
			w.logf("sidebar pill not posted: %v", err)
		}
	case key == "" && w.pillKey != "":
		if _, err := w.postPill(nil); err != nil {
			w.logf("sidebar pill not cleared: %v", err)
		}
		w.pillKey = ""
	}
}

// finish records the run's final result once (withdrawing the launcher files
// unless the respawn succeeded). abandoned posts its orange pill and failed its
// red one on the run's workspace first (D16), with their run: the daemon tells the
// session (D2) and its report goes into the record's detail; respawned posts
// none, and closes the subject's open alerts (D5).
func (w *rwWorker) finish(e spawnreg.Entry) spawnreg.Entry {
	w.mu.Lock()
	if w.final != nil {
		f := *w.final
		w.mu.Unlock()
		return f
	}
	var noticeWarn string
	if e.Result == respawnAbandoned || e.Result == respawnFailed {
		n, err := w.postPill(&pigtail.CmuxRespawnRequest{State: e.Result, SurfaceID: w.run.SurfaceID,
			RunID: w.run.ID, SubjectID: w.run.SubjectID, SessionID: w.currentSessionID(), Detail: e.Detail})
		switch {
		case err != nil:
			w.logf("sidebar pill not posted: %v", err)
			noticeWarn = "⚠ notice fate unknown (" + cmux.OneLine(err.Error(), 160) + ") — pigtail journal list --kind respawn-" + e.Result
		case n == nil:
			noticeWarn = "⚠ the daemon did not report a notice (pigtaild older than this worker?) — nobody was told"
		default:
			if p := n.Problems(); p != "" {
				noticeWarn = "⚠ notice incomplete (" + p + ")"
			}
			if p := n.PillProblem(); p != "" && w.pillErr == "" { // the pill apart (CA8b)
				w.pillErr = "⚠ sidebar pill not posted (" + p + ")"
				w.logf("sidebar pill not posted: %s", p)
			}
		}
	}
	if e.Result == respawnDone && w.run.SubjectID != "" {
		if err := rwResolveAlerts(w.ctx, w.c, w.run.SubjectID, "respawn "+w.run.ID); err != nil {
			warn := "⚠ alerts not resolved (" + cmux.OneLine(err.Error(), 160) + ") — pigtail alerts list"
			w.logf("%s", warn)
			e.Detail = joinDetail(e.Detail, warn)
		}
	}
	if w.pillErr != "" {
		e.Detail = joinDetail(e.Detail, w.pillErr)
	}
	if noticeWarn != "" {
		e.Detail = joinDetail(e.Detail, noticeWarn)
		w.logf("%s", noticeWarn)
	}
	w.final = &e
	w.mu.Unlock()
	if e.Result != respawnDone {
		w.withdraw()
	}
	if w.run.Relaunch == relaunchShell { // the script is no longer needed once the fresh Claude is seen
		_ = os.Remove(respawnScriptPath(w.run.ID))
	}
	if err := w.reg.Append(e); err != nil {
		w.logf("registry not written: %v", err)
	}
	w.logf("%s: %s", e.Result, e.Detail)
	return e
}

func (w *rwWorker) fail(detail string) spawnreg.Entry {
	e := w.entry(respawnFailed)
	e.Detail = detail
	return w.finish(e)
}

func (w *rwWorker) abandon(detail string) spawnreg.Entry {
	e := w.entry(respawnAbandoned)
	e.Detail = detail
	return w.finish(e)
}

// onInterrupt is what the SIGTERM/SIGINT handler runs.
func (w *rwWorker) onInterrupt() {
	w.mu.Lock()
	w.interrupted = true
	killed := w.killed
	w.mu.Unlock()
	if killed {
		w.fail("worker interrupted after closing the session — look at " + w.run.SurfaceRef)
		return
	}
	w.abandon("worker interrupted — the session was not touched")
}

// interruptedBeforeKill is the run's result once the handler has flagged an
// interrupt before the kill: the handler's own record when it is written,
// else the same one (finish records only the first).
func (w *rwWorker) interruptedBeforeKill() spawnreg.Entry {
	if e, ok := w.stopped(); ok {
		return e
	}
	return w.abandon("worker interrupted — the session was not touched")
}

func (w *rwWorker) stopped() (spawnreg.Entry, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.interrupted && w.final != nil {
		return *w.final, true
	}
	return spawnreg.Entry{}, false
}

// currentSessionID is the session the run was to close as it stands now:
// the live process's native id (/clear changes it), else the armed one.
func (w *rwWorker) currentSessionID() string {
	if rwPidAlive(w.run.ClaudePID) {
		if n, ok := rwNative(w.run.ConfigDir, w.run.ClaudePID); ok && n.SessionID != "" {
			return n.SessionID
		}
	}
	return w.run.SessionID
}

// joinDetail appends a warning to a record's detail.
func joinDetail(detail, warn string) string {
	if detail == "" {
		return warn
	}
	return detail + "; " + warn
}

// ready checks the idle conditions (2a–2d), unless the session ended on its
// own, was parked as a background job, or is in Standby (end). The Standby is
// read for the current session id (/clear changes it) at every call, the
// re-check right before the kill included.
func (w *rwWorker) ready() (wait *rwWait, end rwEnd) {
	r := w.run
	if !rwPidAlive(r.ClaudePID) {
		return nil, rwEnded
	}
	n, ok := rwNative(r.ConfigDir, r.ClaudePID)
	sid := r.SessionID
	if ok && n.SessionID != "" {
		sid = n.SessionID
	}
	if rwInStandby(os.Getenv, sid) {
		return nil, rwStandby
	}
	if rwPidAlive(r.ForegroundPID) {
		return &rwWait{"", "foreground still running"}, rwLive
	}
	if !ok {
		return &rwWait{"", "session state unreadable (sessions/<pid>.json)"}, rwLive
	}
	if n.ParkedJobID != "" {
		// #167: the process runs on, but the conversation went on in another
		// process under a new id — never close what is not the session.
		return nil, rwParked
	}
	if n.Status == "shell" {
		// Claude's own word for a turn over with a background shell alive
		// (measured 2026-10-13): name the shells (D1(d)), no pill key — the
		// waiting pill for this state is not decided.
		return &rwWait{"", w.shellReason()}, rwLive
	}
	if n.Status != "idle" {
		return &rwWait{"", "busy"}, rwLive
	}
	if n.StatusUpdatedAt > 0 && rwNow().Sub(time.UnixMilli(n.StatusUpdatedAt)) < rwSettle {
		return &rwWait{"", "busy"}, rwLive
	}
	screen, err := rwReadScreen(r.SurfaceID)
	if err != nil {
		w.logf("screen unreadable: %v", err)
		return &rwWait{pigtail.CmuxRespawnWaitScreen, "screen unreadable"}, rwLive
	}
	draft, found := claudesession.InputDraft(screen)
	if !found {
		return &rwWait{pigtail.CmuxRespawnWaitScreen, "screen unreadable (no input box)"}, rwLive
	}
	if draft != "" {
		return &rwWait{pigtail.CmuxRespawnWaitDraft, "draft in the input"}, rwLive
	}
	if claudesession.BackgroundWorkShown(screen) {
		return &rwWait{pigtail.CmuxRespawnWaitBackground, "background work"}, rwLive
	}
	shells, err := rwBashShells(r.ClaudePID)
	if err != nil {
		w.logf("Bash-tool shells unreadable: %v", err)
		return &rwWait{pigtail.CmuxRespawnWaitBackground, "background work (process listing failed)"}, rwLive
	}
	if len(shells) > 0 {
		return &rwWait{pigtail.CmuxRespawnWaitBackground, fmt.Sprintf("background work (Bash shell pid %v)", shells)}, rwLive
	}
	return nil, rwLive
}

// shellReason is the wait reason of native status "shell".
func (w *rwWorker) shellReason() string {
	shells, err := rwShellInfo(w.run.ClaudePID)
	if err != nil {
		w.logf("Bash-tool shells unreadable: %v", err)
		return "background shell (process listing failed)"
	}
	if len(shells) == 0 {
		return "background shell (none listed)"
	}
	return "background shell: " + describeShells(shells)
}

// describeShells renders shells as "`cmd` (age)", three at most then
// "+n more"; a command shaped like a secret is withheld (it reaches the
// journal, the session wait and a message).
func describeShells(ss []claudesession.ShellInfo) string {
	const shown = 3
	var parts []string
	for i, s := range ss {
		if i == shown {
			parts = append(parts, fmt.Sprintf("+%d more", len(ss)-shown))
			break
		}
		cmd := cmux.OneLine(s.Command, 60)
		if relay.SecretShape(s.Command) != "" {
			cmd = "(command withheld)"
		}
		parts = append(parts, fmt.Sprintf("`%s` (%s)", cmd, shortAge(s.Age)))
	}
	return strings.Join(parts, ", ")
}

// shortAge renders d as 45s, 12m or 1h30m.
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// gone ends a run whose session stopped being closable: ended on its own
// (failed: nothing to relaunch into), parked or in Standby (abandoned,
// nothing closed).
func (w *rwWorker) gone(end rwEnd) spawnreg.Entry {
	switch end {
	case rwParked:
		return w.abandon("session parked as a background job — nothing closed")
	case rwStandby:
		return w.abandon("standby (D16): respawn abandoned — the session was not touched")
	}
	return w.fail("the session ended on its own before the respawn — nothing relaunched")
}

// noteWait logs a wait.
func (w *rwWorker) noteWait(wt *rwWait) {
	w.logf("waiting: %s", wt.reason)
}

// screenTail returns the last 6 non-empty lines of the tab.
func (w *rwWorker) screenTail() string {
	s, err := rwReadScreen(w.run.SurfaceID)
	if err != nil {
		return "(screen unreadable: " + err.Error() + ")"
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, " ⏎ ")
}

// writeLauncherFiles writes <key>.prompt, <key>.sid, then <key>.handoff (all
// atomic, 0600); the handoff comes last and stays the signal:
// « fresh <cell> <sid> <worker pid> <mode> [<model>] » (the model only when set).
//
// <key>.sid is claimed before the kill (claimSidFile, relaunch_files.go): a
// SIGTERM closes Claude without its SessionEnd hooks (run
// 20261004T002742.681-8620). On a write failure, what was written is removed.
// Called under w.mu (w.sid).
func (w *rwWorker) writeLauncherFiles(sid string) error {
	if err := relaunchfile.WriteAtomic(w.promptPath(), []byte(w.run.Prompt+"\n")); err != nil {
		return fmt.Errorf("write prompt file: %w", err)
	}
	c, err := relaunchfile.ClaimSid(w.run.RelaunchFile, sid)
	if err != nil {
		_ = os.Remove(w.promptPath())
		return err
	}
	w.sid = c
	freshID, _ := w.run.fresh()
	line := fmt.Sprintf("fresh %s %s %d %s", freshID, sid, os.Getpid(), w.run.Mode)
	if w.run.Model != "" {
		line += " " + w.run.Model
	}
	line += "\n"
	if err := relaunchfile.WriteAtomic(w.handoffPath(), []byte(line)); err != nil {
		_ = os.Remove(w.promptPath())
		w.sid = nil
		c.Release()
		return fmt.Errorf("write handoff: %w", err)
	}
	return nil
}

// verifyFresh waits for the fresh Claude: a pid absent from before, in the
// same surface, under the same account and directory, started fresh with the
// mode and the prompt.
func (w *rwWorker) verifyFresh(before map[int]bool) (int, error) {
	r := w.run
	_, freshDir := r.fresh()
	modeRe := regexp.MustCompile(`(^|\s)--permission-mode(\s+|=)` + regexp.QuoteMeta(r.Mode) + `(\s|$)`)
	resumeRe := regexp.MustCompile(`(^|\s)--resume(\s|=|$)`)
	var modelRe *regexp.Regexp
	if r.Model != "" {
		modelRe = regexp.MustCompile(`(^|\s)--model(\s+|=)` + regexp.QuoteMeta(r.Model) + `(\s|$)`)
	}
	deadline := rwNow().Add(rwSeenTimeout)
	for {
		procs, _ := rwProcs()
		for _, p := range procs {
			if before[p.PID] {
				continue
			}
			env, err := rwEnviron(p.PID)
			if err != nil {
				continue
			}
			if s, _ := cmux.EnvValue(env, cmux.EnvSurfaceID); s != r.SurfaceID {
				continue
			}
			if cfg, _ := cmux.EnvValue(env, "CLAUDE_CONFIG_DIR"); canonicalPath(cfg) != canonicalPath(freshDir) {
				return 0, fmt.Errorf("relaunched under the wrong account: pid %d has CLAUDE_CONFIG_DIR=%q, not %s", p.PID, cfg, freshDir)
			}
			if pwd, _ := cmux.EnvValue(env, "PWD"); canonicalPath(pwd) != canonicalPath(r.Cwd) {
				return 0, fmt.Errorf("relaunched in the wrong directory: pid %d runs in %s, not %s", p.PID, pwd, r.Cwd)
			}
			if !modeRe.MatchString(p.Args) || resumeRe.MatchString(p.Args) || !strings.Contains(p.Args, r.Prompt) {
				return 0, fmt.Errorf("relaunched but not fresh (argv %s)", cmux.OneLine(p.Args, 200))
			}
			if modelRe != nil && !modelRe.MatchString(p.Args) {
				return 0, fmt.Errorf("relaunched but not on model %s (argv %s)", r.Model, cmux.OneLine(p.Args, 200))
			}
			return p.PID, nil
		}
		if rwNow().After(deadline) {
			return 0, errSpawnNotSeen
		}
		time.Sleep(rwPoll)
	}
}

// runRespawnWorker executes one armed run. It returns the final record it
// appended (result respawned|failed|abandoned) — the process exit code is 0
// for respawned, 1 otherwise; nothing reads it but the log.
func runRespawnWorker(ctx context.Context, c *pigtail.Client, reg *spawnreg.Registry, run respawnRun, log io.Writer) spawnreg.Entry {
	w := &rwWorker{ctx: ctx, c: c, reg: reg, run: run, log: log}
	// 1. The handshake, then the interrupt handler.
	if err := reg.Append(w.entry(respawnArmed)); err != nil {
		w.logf("armed record not written: %v", err)
	}
	w.logf("armed: surface %s (%s), claude pid %d, session %s, idle timeout %s", run.SurfaceRef, run.SurfaceID, run.ClaudePID, run.SessionID, run.IdleTimeout)
	stop := rwInterrupt(w.onInterrupt)
	defer stop()

	// 2–5. Wait idle, write the launcher files, re-check, close.
	deadline := rwNow().Add(run.IdleTimeout)
	var last string
	var before map[int]bool
	var sid string
	for {
		if e, ok := w.stopped(); ok {
			return e
		}
		wt, end := w.ready()
		if end != rwLive {
			return w.gone(end)
		}
		w.syncPill(wt)
		if wt == nil {
			// 3. The current session id (/clear changes it), the Claude
			// pids before the kill, then the launcher files.
			sid = run.SessionID
			if n, ok := rwNative(run.ConfigDir, run.ClaudePID); ok && n.SessionID != "" {
				sid = n.SessionID
			}
			procs, err := rwProcs()
			if err != nil {
				return w.fail("list Claude processes: " + err.Error() + " — nothing touched")
			}
			before = make(map[int]bool, len(procs))
			for _, p := range procs {
				before[p.PID] = true
			}
			// Under the lock the interrupt handler takes: it either sees
			// the files written (and withdraws them) or stops this write.
			w.mu.Lock()
			if w.interrupted {
				w.mu.Unlock()
				return w.interruptedBeforeKill()
			}
			if run.Relaunch == relaunchShell {
				err = w.writeShellScript(sid)
			} else {
				err = w.writeLauncherFiles(sid)
			}
			w.mu.Unlock()
			if err != nil {
				return w.fail(err.Error() + " — nothing touched")
			}
			// 4. Re-check right before the kill.
			wt, end = w.ready()
			if end != rwLive {
				return w.gone(end)
			}
			if wt == nil {
				w.mu.Lock()
				if w.interrupted {
					w.mu.Unlock()
					return w.interruptedBeforeKill()
				}
				w.killed = true
				w.mu.Unlock()
				break
			}
			w.withdraw()
			w.logf("handoff withdrawn: %s", wt.reason)
		}
		last = wt.reason
		w.noteWait(wt)
		if rwNow().After(deadline) {
			return w.abandon(fmt.Sprintf("gave up after %s: %s — the session was not touched", run.IdleTimeout, last))
		}
		time.Sleep(rwPoll)
	}

	// 5. SIGTERM, then wait for the exit.
	w.logf("closing Claude pid %d (session %s)", run.ClaudePID, sid)
	if err := rwKill(run.ClaudePID); err != nil {
		return w.fail(fmt.Sprintf("SIGTERM to Claude (pid %d) failed: %v — nothing relaunched", run.ClaudePID, err))
	}
	exitBy := rwNow().Add(rwExitTimeout)
	for rwPidAlive(run.ClaudePID) {
		if rwNow().After(exitBy) {
			return w.fail(fmt.Sprintf("Claude (pid %d) did not exit within %s after SIGTERM — it still runs; nothing relaunched", run.ClaudePID, rwExitTimeout))
		}
		time.Sleep(rwPoll)
	}

	resume := resumeHint(run, sid, run.Relaunch != relaunchShell)

	// 5b. Not started by c: the tab's shell takes the relaunch (D1).
	if run.Relaunch == relaunchShell {
		if err := w.typeRelaunch(); err != nil {
			return w.fail(fmt.Sprintf("%v — the tab shows: %s — to resume the old session: `%s`", err, w.screenTail(), resume))
		}
	}

	// 6. The fresh Claude.
	pid, err := w.verifyFresh(before)
	if err != nil {
		if errors.Is(err, errSpawnNotSeen) {
			return w.fail(fmt.Sprintf("no fresh Claude in %s after %s — the tab shows: %s — to resume the old session: `%s` (or Enter at the ↻ prompt if it shows)",
				run.SurfaceRef, rwSeenTimeout, w.screenTail(), resume))
		}
		return w.fail(fmt.Sprintf("%v — the tab shows: %s — to resume the old session: `%s`", err, w.screenTail(), resume))
	}
	w.logf("fresh Claude pid %d in %s", pid, run.SurfaceRef)

	// 7. Its session id, for the resume binding — under the fresh cell.
	_, freshDir := run.fresh()
	var warnings []string
	newSID := ""
	sidBy := rwNow().Add(rwSidTimeout)
	for {
		if n, ok := rwNative(freshDir, pid); ok && n.SessionID != "" {
			newSID = n.SessionID
			break
		}
		if rwNow().After(sidBy) {
			warnings = append(warnings, fmt.Sprintf("⚠ the new session id was not seen within %s — resume binding not rewritten", rwSidTimeout))
			break
		}
		time.Sleep(rwPoll)
	}
	if newSID != "" {
		wrapper := rwWrapper(log)
		if err := rwSetBinding(run.SurfaceID, run.Cwd, newSID, freshDir, wrapper, run.Mode); err != nil {
			launcher := "claude"
			if wrapper != "" {
				launcher = wrapper
			}
			warnings = append(warnings, fmt.Sprintf("⚠ resume binding not rewritten: %v — fix: cmux surface resume set --surface %s --cwd %s --kind agent --checkpoint %s -- /usr/bin/env CLAUDE_CONFIG_DIR=%s %s --resume %s --permission-mode %s",
				err, run.SurfaceRef, run.Cwd, newSID, freshDir, launcher, newSID, run.Mode))
		}
	}

	// 8. The subject binding.
	if !run.Bound {
		if err := rwAttach(ctx, c, run.SubjectID, run.WorkspaceID); err != nil {
			var ae *pigtail.APIError
			switch {
			case errors.As(err, &ae) && ae.Code == pigtail.ErrConflict:
				warnings = append(warnings, "⚠ workspace already bound to another subject — not rebound")
			default:
				cross := ""
				if run.CrossScope || (errors.As(err, &ae) && ae.Code == pigtail.ErrScopeMismatch) {
					cross = " --cross-scope"
				}
				warnings = append(warnings, fmt.Sprintf("⚠ not bound to the subject (%v) — by hand: pigtail subject attach %s --workspace %s --source ptnew%s", err, run.SubjectID, run.WorkspaceID, cross))
			}
		}
	}

	// 9. The record (finish adds a sidebar failure, if one).
	e := w.entry(respawnDone)
	e.PID, e.NewSessionID, e.Detail = pid, newSID, strings.Join(warnings, "; ")
	return w.finish(e)
}

// enterWorkerMode is the first thing `pigtail session respawn --worker <run>`
// does — the detached process armRespawn starts, whose output goes to the run
// log: pin the locale, survive a hangup, read the run file.
func enterWorkerMode(runPath string) (respawnRun, error) {
	pinWorkerLocale()
	signal.Ignore(syscall.SIGHUP)
	run, err := readRespawnRun(runPath)
	if err != nil {
		return run, respawnErr("worker: %v", err)
	}
	if run.IdleTimeout <= 0 {
		run.IdleTimeout = respawnIdleDefault
	}
	return run, nil
}

// runWorkerMode carries out the run with the worker's client (built by the
// command: --cross-scope from the run file).
func runWorkerMode(c *pigtail.Client, run respawnRun, stdout io.Writer) error {
	e := runRespawnWorker(context.Background(), c, spawnreg.Open(spawnreg.DefaultPath()), run, stdout)
	if e.Result != respawnDone {
		return respawnErr("worker: %s: %s", e.Result, e.Detail)
	}
	return nil
}

package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
)

// The relaunch contract with the `c` zsh launcher.
// The launcher exports CLAUDE_RELAUNCH_FILE (<state>/<tab>.sid, filled by the
// SessionEnd hook) and CLAUDE_RELAUNCH_PROTO into Claude's environment. When
// Claude exits, the launcher's loop takes the pane back and reads one key —
// so a line typed into that pane loses its first character. For such a pane
// session move never types: it writes <tab>.handoff, one line
// "<pending|ready> <cell> <sid> <pigtail pid>", and the launcher relaunches
// the session itself under <cell>. The pid lets the launcher stop waiting the
// moment this process is gone.
//
// The launcher reads the handoff only when <tab>.sid holds the handoff's sid:
// pigtail claims that .sid before the pending handoff and releases it on
// every abandon (internal/relaunchfile holds the contract).
const (
	handoffPending = "pending"
	handoffReady   = "ready"
)

// Seams: the process table and the clock session move reads. Tests replace
// them so no real process is ever inspected, and no real session ever moved.
var (
	findNative             = claudesession.FindNative
	pidAlive               = claudesession.PidAlive
	processEnviron         = cmux.ProcessEnviron
	claudeProcs            = claudesession.ClaudeProcesses
	quitTimeout            = 20 * time.Second
	quitInterval           = time.Second
	relaunchVerifyTimeout  = 15 * time.Second
	relaunchVerifyInterval = 250 * time.Millisecond
	interruptNotify        = notifyInterrupt
	confirmRelaunch        = verifyRelaunch
)

type relaunchKind int

const (
	// relaunchTyped types cmux.ResumeLine into the pane: a bare shell takes it.
	relaunchTyped relaunchKind = iota
	// relaunchHandoff hands the relaunch to the `c` launcher through its
	// handoff file; nothing is typed after /exit.
	relaunchHandoff
)

// relaunchRoute is how the session will come back, decided before /exit.
// PID and Surface describe the Claude process found serving the session
// (0 and "" when none was found): PID is what the quit wait watches, Surface
// (CMUX_SURFACE_ID) is what the relaunched process must carry.
type relaunchRoute struct {
	Kind        relaunchKind
	HandoffPath string
	SidPath     string // CLAUDE_RELAUNCH_FILE, the launcher's <tab>.sid (handoff route only)
	PID         int
	Surface     string
}

func (r relaunchRoute) describe() string {
	if r.Kind == relaunchHandoff {
		return "through the launcher handoff (" + r.HandoffPath + ")"
	}
	return "by typing the resume line in the pane"
}

// routeRelaunch decides, before anything is sent to the pane, how the
// session will be relaunched. It reads the environment of the Claude
// process serving sid under src:
//   - no live process: typed line — the pane may sit at a shell prompt, which
//     is what that route was built for; if the pane was in fact under `c`,
//     the post-relaunch check catches it and keeps the source.
//   - CLAUDE_RELAUNCH_FILE with CLAUDE_RELAUNCH_PROTO=1: handoff.
//   - CLAUDE_RELAUNCH_FILE without the proto: an old launcher that would eat
//     the typed line — refused, nothing touched.
//   - no CLAUDE_RELAUNCH_FILE: typed line, unchanged.
func routeRelaunch(src claudeprofile.Cell, sid string) (relaunchRoute, error) {
	n, ok := findNative(src.ConfigDir, sid)
	if !ok || !pidAlive(n.PID) {
		return relaunchRoute{Kind: relaunchTyped}, nil
	}
	env, err := processEnviron(n.PID)
	if err != nil {
		return relaunchRoute{}, fmt.Errorf("cannot tell how Claude (pid %d) was launched, refusing to quit it: %w", n.PID, err)
	}
	surface, _ := cmux.EnvValue(env, cmux.EnvSurfaceID)
	route := relaunchRoute{Kind: relaunchTyped, PID: n.PID, Surface: surface}
	file, hasFile := cmux.EnvValue(env, relaunchfile.EnvFile)
	if !hasFile || file == "" {
		return route, nil
	}
	proto, _ := cmux.EnvValue(env, relaunchfile.EnvProto)
	switch proto {
	case relaunchfile.Proto:
		route.Kind, route.HandoffPath, route.SidPath = relaunchHandoff, handoffPath(file), file
		return route, nil
	case "":
		return relaunchRoute{}, fmt.Errorf("session %s runs under an old `c` launcher that would swallow the relaunch line; re-source it first (quit Claude, `exec zsh` in that pane or open a new tab, relaunch), then retry", sid[:8])
	default:
		return relaunchRoute{}, fmt.Errorf("session %s runs under a `c` launcher speaking relaunch protocol %q, this pigtail knows %q — update pigtail", sid[:8], proto, relaunchfile.Proto)
	}
}

// handoffPath mirrors the launcher's ${CLAUDE_RELAUNCH_FILE%.sid}.handoff.
func handoffPath(relaunchFile string) string {
	return strings.TrimSuffix(relaunchFile, ".sid") + ".handoff"
}

// writeHandoff replaces the handoff file atomically (relaunchfile.WriteAtomic), so
// the launcher never reads a half-written line. The line ends with this
// process's pid.
func writeHandoff(path, state, cell, sid string) error {
	line := fmt.Sprintf("%s %s %s %d\n", state, cell, sid, os.Getpid())
	if err := relaunchfile.WriteAtomic(path, []byte(line)); err != nil {
		return fmt.Errorf("write handoff: %w", err)
	}
	return nil
}

// removeHandoff withdraws the handoff, best effort: the launcher treats a
// vanished file as "pigtail gave up" and shows its usual prompt.
func removeHandoff(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

// quitProbe is what the quit wait polls. The session still runs while a
// process names it in argv (runningProbe) OR the pid found before /exit is
// alive: a Claude started by a bare `c mt` carries no session id in argv, so
// runningProbe alone reports it gone at once and the copy would race a live
// writer — and the exit dialog would never be looked for.
func quitProbe(pid int) func(string) bool {
	return func(id string) bool {
		return runningProbe(id) || (pid > 0 && pidAlive(pid))
	}
}

// notifyInterrupt runs cleanup if pigtail gets SIGINT or SIGTERM before stop
// is called, then exits with the conventional 128+signal status. It is what
// keeps a Ctrl-C during the quit wait from leaving a pending handoff behind.
func notifyInterrupt(cleanup func()) (stop func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	stopWatch := watchInterrupt(sigs, cleanup, os.Exit)
	return func() {
		signal.Stop(sigs)
		stopWatch()
	}
}

// watchInterrupt is notifyInterrupt without the process plumbing, so it can
// be tested: the first signal on sigs runs cleanup then exit; stop ends the
// watch without running either, and returns once the watcher is gone.
func watchInterrupt(sigs <-chan os.Signal, cleanup func(), exit func(int)) (stop func()) {
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case sig := <-sigs:
			cleanup()
			code := 130
			if s, ok := sig.(syscall.Signal); ok {
				code = 128 + int(s)
			}
			exit(code)
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}

// claudePIDs is the set of Claude processes alive right now — taken before
// /exit, it is what tells the relaunched process apart from any older one.
func claudePIDs() (map[int]bool, error) {
	procs, err := claudeProcs()
	if err != nil {
		return nil, err
	}
	pids := make(map[int]bool, len(procs))
	for _, p := range procs {
		pids[p.PID] = true
	}
	return pids, nil
}

var (
	errRelaunchWrongConfig = errors.New("relaunched under the wrong account")
	errRelaunchNotSeen     = errors.New("relaunch not observed")
)

// relaunchCheck is what verifyRelaunch looks for.
type relaunchCheck struct {
	SessionID string
	Dst       claudeprofile.Cell
	Before    map[int]bool // Claude pids alive before /exit
	Surface   string       // CMUX_SURFACE_ID of the quit process; "" when unknown
}

// verifyRelaunch waits for the relaunch to be visible as a process: a Claude
// process absent from c.Before, whose argv resumes c.SessionID (both routes
// relaunch with `--resume <sid>`), in the same cmux surface when it is known,
// running with CLAUDE_CONFIG_DIR=c.Dst.ConfigDir. It never trusts
// sessions/<pid>.json: that file can lag behind a trust or login dialog, and
// a stale one can name a recycled pid.
//
// A matching process under another config dir fails at once with
// errRelaunchWrongConfig; nothing within relaunchVerifyTimeout fails with
// errRelaunchNotSeen. This is a wait for an observable state, not a sleep.
func verifyRelaunch(c relaunchCheck) (int, error) {
	want := filepath.Clean(c.Dst.ConfigDir)
	resumeRe := regexp.MustCompile(`(^|\s)--resume(\s+|=)` + regexp.QuoteMeta(c.SessionID) + `(\s|$)`)
	deadline := time.Now().Add(relaunchVerifyTimeout)
	for {
		procs, listErr := claudeProcs()
		var unreadable string
		for _, p := range procs {
			if c.Before[p.PID] || !resumeRe.MatchString(p.Args) {
				continue
			}
			env, err := processEnviron(p.PID)
			if err != nil {
				unreadable = fmt.Sprintf("pid %d resumes it but its environment is unreadable (%v)", p.PID, err)
				continue
			}
			if c.Surface != "" {
				if s, _ := cmux.EnvValue(env, cmux.EnvSurfaceID); s != c.Surface {
					continue // the same session resumed in another pane is not this relaunch
				}
			}
			got, _ := cmux.EnvValue(env, "CLAUDE_CONFIG_DIR")
			if filepath.Clean(got) == want {
				return p.PID, nil
			}
			return 0, fmt.Errorf("%w: session %s came back as pid %d with CLAUDE_CONFIG_DIR=%q, not %s (%s)",
				errRelaunchWrongConfig, c.SessionID[:8], p.PID, got, c.Dst.ID, c.Dst.ConfigDir)
		}
		if time.Now().After(deadline) {
			detail := "no new Claude process resuming it"
			switch {
			case listErr != nil:
				detail = "process listing failed: " + listErr.Error()
			case unreadable != "":
				detail = unreadable
			}
			return 0, fmt.Errorf("%w: session %s not seen running on %s (%s) after %s — %s",
				errRelaunchNotSeen, c.SessionID[:8], c.Dst.ID, c.Dst.ConfigDir, relaunchVerifyTimeout, detail)
		}
		time.Sleep(relaunchVerifyInterval)
	}
}

// relaunchRecovery is what the user is told to do when the relaunch could
// not be confirmed. The way to resume by hand depends on the route: under
// the `c` launcher a typed CLAUDE_CONFIG_DIR=… line would be eaten by its
// prompt again, so it is `c <cell> --resume <sid>` there.
func relaunchRecovery(err error, route relaunchRoute, r sessionRow, dst claudeprofile.Cell, srcPath, wrapper string) []string {
	resume := cmux.ResumeLine(wrapper, dst.ConfigDir, r.SessionID)
	if route.Kind == relaunchHandoff {
		resume = fmt.Sprintf("c %s --resume %s", dst.ID, r.SessionID)
	}
	finish := []string{
		"then finish the move by hand:",
		"  " + resumeBindingFixCommand(r, dst, wrapper),
		"  and delete the source transcript: " + srcPath,
	}
	if errors.Is(err, errRelaunchWrongConfig) {
		quit := "quit it (/exit)"
		if route.Kind == relaunchHandoff {
			quit = "quit it (/exit, then any other key at the c prompt)"
		}
		return append([]string{
			fmt.Sprintf("pane %s runs the session on the wrong account: %s and resume it on %s with:", r.SurfaceRef, quit, dst.ID),
			"  " + resume,
		}, finish...)
	}
	otherwise := "otherwise resume it there with:"
	if route.Kind == relaunchHandoff {
		// The launcher fell back to its one-key prompt (pigtail gone, deadline,
		// unknown cell, no transcript): a `c …` typed now would lose its `c`.
		otherwise = "otherwise, if the pane shows the c prompt (↻ …), press any other key first to leave it, then resume it there with:"
	}
	return append([]string{
		fmt.Sprintf("if pane %s does show the session running on %s, skip to the last two lines;", r.SurfaceRef, dst.ID),
		otherwise,
		"  " + resume,
	}, finish...)
}

// interruptRecovery is what pigtail prints when SIGINT/SIGTERM stops a move
// after /exit could have been sent: the source is always still there, and
// what to do next depends on whether the copy has landed (copied = its path)
// and on the route — under the launcher, never a typed line its prompt
// would eat.
func interruptRecovery(route relaunchRoute, r sessionRow, src, dst claudeprofile.Cell, srcPath, copied, wrapper string) []string {
	if copied != "" {
		return append([]string{
			fmt.Sprintf("✗ interrupted — source kept: %s — a copy already exists at %s", srcPath, copied),
		}, relaunchRecovery(errRelaunchNotSeen, route, r, dst, srcPath, wrapper)...)
	}
	lines := []string{
		fmt.Sprintf("✗ interrupted — source kept: %s, nothing copied to %s", srcPath, dst.ID),
		fmt.Sprintf("if pane %s no longer runs the session, resume it on %s with:", r.SurfaceRef, src.ID),
	}
	if route.Kind == relaunchHandoff {
		lines[1] = fmt.Sprintf("if pane %s no longer runs the session, press any other key at the c prompt (↻ …) if it shows, then resume it on %s with:", r.SurfaceRef, src.ID)
		return append(lines, fmt.Sprintf("  c %s --resume %s", src.ID, r.SessionID))
	}
	return append(lines, "  "+cmux.ResumeLine(wrapper, src.ConfigDir, r.SessionID))
}

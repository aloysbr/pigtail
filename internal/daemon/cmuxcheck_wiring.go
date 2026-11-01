package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxwatch"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// startCmuxWatchFn is what Server.Run calls; TestServerStartsCmuxWatch
// replaces it to prove the call and the mode reach it.
var startCmuxWatchFn = startCmuxWatch

// cmuxWatchDeps wires the watcher to the cmux sentinel and the pigtail
// subprocess (pulled out of Server.Run so the wiring test can check them).
func (s *Server) cmuxWatchDeps() cmuxwatch.Deps {
	return cmuxwatch.Deps{
		Sentinel: func() (int, error) { return cmuxcheck.ReadSentinel(cmuxcheck.DefaultSentinel()) },
		Run:      runCmuxCheck,
		// The log names the argv runCmuxCheck runs: the same list.
		Args: func(mode string, surfaces []string) []string {
			return cmuxCheckArgs(mode, s.cfg.CmuxCheckCtrlC, surfaces)
		},
		Logf: log.Printf,
		// D14: the pill needs the sidebar loop on; the check runs regardless.
		SidebarOn: func() bool { return s.cmuxSync != nil && s.cmuxSync.State().Enabled },
		// D6: the rows the first pass left busy, read from last.json (the
		// check's report); never cmux.
		Notice: func(pid int, failure string) {
			writeCmuxCheckNotice(cmuxcheck.StateDir(), pid, failure, func(e pigtail.Event) { s.bus.Publish(e) }, log.Printf)
		},
		BusyLeft: func(pid int, pilotOnly bool) ([]string, error) {
			r, err := cmuxcheck.LoadLast(cmuxcheck.StateDir())
			if err != nil {
				return nil, err
			}
			return r.BusyLeft(pid, pilotOnly), nil
		},
	}
}

// cmuxCheckArgs is the subprocess's argv after the binary — every scope (D7
// of the previous subject), no notification (D13), --unattended (D8: its
// --surface names nothing for Ctrl+C); pilot mode repairs the pilot only
// (D3), fix mode every session; --ctrl-c only when not the default;
// --surface for the second pass (D6).
func cmuxCheckArgs(mode, ctrlC string, surfaces []string) []string {
	args := []string{"cmux", "check", "--scope", "all", "--unattended"}
	switch mode {
	case cmuxwatch.ModePilot:
		args = append(args, "--fix-pilot")
	case cmuxwatch.ModeFix:
		args = append(args, "--fix")
	}
	if ctrlC != "" && ctrlC != string(cmuxcheck.QuitRescue) {
		args = append(args, "--ctrl-c", ctrlC)
	}
	for _, sf := range surfaces {
		args = append(args, "--surface", sf)
	}
	return args
}

// startCmuxWatch starts the post-relaunch watcher unless mode is off ("" is
// off: a Config built without the flag); it reports whether it did.
func startCmuxWatch(ctx context.Context, mode string, spawn func(func()), d cmuxwatch.Deps) bool {
	if mode == "" || mode == cmuxwatch.ModeOff {
		return false
	}
	w := cmuxwatch.New(cmuxwatch.Config{Mode: mode, Poll: 30 * time.Second, Settle: 90 * time.Second, Retries: 3,
		StatePath: filepath.Join(cmuxcheck.StateDir(), "last-cmux-pid"), SecondPass: cmuxcheck.SecondPassDelay}, d)
	spawn(func() { w.Run(ctx) })
	return true
}

// Seams: tests replace them — never a real cmux, never a real pigtail.
var (
	cmuxCheckSelf = os.Executable
	// cmuxCheckTimeout bounds the whole check subprocess (a --fix pass
	// relaunches sessions one at a time, each bounded by the repairer).
	cmuxCheckTimeout = 15 * time.Minute
	cmuxCheckExec    = func(ctx context.Context, bin string, args []string, out io.Writer) (int, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = cmuxCheckEnv(os.Environ())
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ee.ExitCode() < 0 { // killed by a signal: it never finished
				return -1, fmt.Errorf("pigtail cmux check killed: %w", err)
			}
			return ee.ExitCode(), nil
		}
		if err != nil {
			return 2, err
		}
		return 0, nil
	}
)

// cmuxCheckEnv is pigtaild's environment without the variables that would
// put the check in a Claude cell (CLAUDE_CONFIG_DIR) or override its scope
// (PIGTAIL_SCOPE): outside any cell, `--scope all` reads every scope (D7);
// and without the pane variables (cmux.PaneEnv) a pigtaild started from a
// pane inherits — CMUX_SURFACE_ID would become Options.Self (that session
// never repaired); the whole list goes, one list kept in internal/cmux (#36).
func cmuxCheckEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(kv, "PIGTAIL_SCOPE=") || cmux.IsPaneEnv(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// runCmuxCheck runs the pigtail CLI next to this pigtaild; its output goes
// to <StateDir>/daemon.log, its exit code back to the watcher (2 = could not
// look, retried). Bounded by cmuxCheckTimeout: past it the child is killed
// and the watcher gets an error naming the timeout (retried, never recorded).
func runCmuxCheck(ctx context.Context, args []string) (int, error) {
	self, err := cmuxCheckSelf()
	if err != nil {
		return 2, err
	}
	if err := os.MkdirAll(cmuxcheck.StateDir(), 0o700); err != nil {
		return 2, err
	}
	logf, err := os.OpenFile(filepath.Join(cmuxcheck.StateDir(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 2, err
	}
	defer func() { _ = logf.Close() }()
	fmt.Fprintf(logf, "--- %s pigtail %s\n", time.Now().Format(time.RFC3339), strings.Join(args, " "))
	tctx, cancel := context.WithTimeout(ctx, cmuxCheckTimeout)
	defer cancel()
	code, err := cmuxCheckExec(tctx, filepath.Join(filepath.Dir(self), "pigtail"), args, logf)
	if errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		err = fmt.Errorf("pigtail cmux check timed out after %s, killed", cmuxCheckTimeout)
		fmt.Fprintln(logf, "--- "+err.Error())
		return -1, err
	}
	return code, err
}

// writeCmuxCheckNotice (D2): the notice of launch pid — the last report's
// line when it is about that launch, else the failure; written, then the bar
// told. A write error is logged: the pill stays the fallback.
func writeCmuxCheckNotice(dir string, pid int, failure string, publish func(pigtail.Event), logf func(string, ...any)) {
	line := ""
	switch rep, err := cmuxcheck.LoadLast(dir); {
	case failure == cmuxwatch.FailSecondPass && err == nil && rep.Launch.PID == pid:
		// pass 1 started sessions: its line, plus the failed second pass
		base := strings.TrimSuffix(cmuxcheck.NoticeLine(rep), " — pigtail cmux check --last")
		if base == "" {
			base = "Reprise cmux"
		}
		line = base + " · seconde passe ratée — pigtail cmux check --last"
	case failure != "":
		line = "Reprise cmux impossible (" + failure + ") — pigtail cmux check --fix"
	case err != nil:
		line = "Reprise cmux : rapport illisible (" + err.Error() + ") — pigtail cmux check --last"
	case rep.Launch.PID != pid:
		line = "Reprise cmux : aucun rapport pour ce lancement — pigtail cmux check --fix"
	default:
		line = cmuxcheck.NoticeLine(rep)
	}
	n := cmuxcheck.Notice{LaunchPID: pid, At: time.Now().UTC(), Line: line}
	if err := cmuxcheck.SaveNotice(dir, n); err != nil {
		logf("cmuxwatch: cmux pid %d — notice not written: %v", pid, err)
		return
	}
	logf("cmuxwatch: cmux pid %d — notice %q", pid, line)
	publish(pigtail.Event{Type: pigtail.EventCmuxCheckNotice})
}

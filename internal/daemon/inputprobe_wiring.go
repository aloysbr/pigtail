package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/inputprobe"
)

// The daemon's dead-input probe (sub_x000000000000024, task 7): once a
// minute, at most inputProbeMax idle interactive sessions, each at most once
// every inputProbeEvery, the most overdue first, get a terminal status query
// (ESC [ 5 n) written to the tty of their claude; a session whose claude
// never reacts is dead, shown on the pigtail-chk pill (DeadInputs). Only that
// query is written, never a keystroke, never through cmux send, and claude is
// never signalled. It runs
// only while the sidebar loop is on, never in a session in Standby (D16),
// and skips its tick when cmux does not answer, a `pigtail cmux check --fix`
// types (cmuxcheck.Running) or a maintenance is active.
const (
	inputProbeEvery = 5 * time.Minute
	inputProbeTick  = time.Minute
	// inputProbeMax: 4 probes a minute keep the rotation under 5 min up to
	// ~20 idle sessions; beyond, it takes N/4 min (plan § Tradeoffs).
	inputProbeMax = 4
)

// inputProbeLoop is one tick's seams; tests replace every one, never a real
// cmux nor a real claude.
type inputProbeLoop struct {
	tracker      *inputprobe.Tracker
	max          int
	sidebarOn    func() bool
	ping         func() error
	checkRunning func() bool
	maintenance  func() bool // a maintenance is active (an unreadable state counts)
	inStandby    func(sid string) (bool, error)
	photo        func() (cmuxcheck.Photo, error)
	check        cmuxcheck.Deps
	probe        func(inputprobe.Target) inputprobe.Result
	now          func() time.Time
	logf         func(string, ...any)
	heldLogged   bool // the "dead states held" line was logged for the current hold
	trace        bool // PIGTAIL_INPUT_PROBE_TRACE=1: one line per probe
}

// inputProbeTraceEnv, read when the loop is built at pigtaild's start, turns
// on one log line per probe (a live pass, V3); absent by default.
const inputProbeTraceEnv = "PIGTAIL_INPUT_PROBE_TRACE"

const logDeadHeld = "input probe: no live answer seen in 30 min, dead states held"

// tick runs one pass; it reports whether it probed (false = skipped).
func (l *inputProbeLoop) tick() bool {
	if !l.sidebarOn() {
		return false
	}
	if l.maintenance() || l.checkRunning() {
		return false
	}
	if err := l.ping(); err != nil {
		return false
	}
	ph, err := l.photo()
	if err != nil {
		l.logf("input probe: cmux photo unreadable, nothing probed: %v", err)
		return false
	}
	live, err := cmuxcheck.Live(l.check)
	if err != nil {
		l.logf("input probe: claude processes unreadable, nothing probed: %v", err)
		return false
	}
	alive := map[string]bool{}
	for _, ls := range live {
		for _, lc := range ls {
			if lc.SessionID != "" {
				alive[lc.SessionID] = true
			}
		}
	}
	l.tracker.Forget(alive)
	now := l.now()
	opts := cmuxcheck.Options{InStandby: l.inStandby, Pick: func(c []inputprobe.Target) []inputprobe.Target {
		return l.tracker.Pick(c, now, l.max)
	}}
	targets, err := cmuxcheck.ProbeTargets(l.check, opts, ph, live)
	if err != nil {
		l.logf("input probe: targets unreadable, nothing probed: %v", err)
		return false
	}
	probe := l.probe
	if l.trace {
		probe = func(t inputprobe.Target) inputprobe.Result {
			res := l.probe(t)
			// Surface, tty and verdict only: no session id nor title (D23).
			l.logf("input probe: surface %s tty %s verdict %s", t.Surface, t.TTY, res.Verdict)
			return res
		}
	}
	for _, tr := range inputprobe.Pass(l.tracker, targets, probe, now, l.max) {
		// No session id nor title: the log may be read by any scope (D23).
		if tr.Dead {
			l.logf("input probe: input dead on surface %s", tr.Target.Surface)
		} else {
			l.logf("input probe: input back on surface %s", tr.Target.Surface)
		}
	}
	// The positive control: a confirmed dead state with no session answering
	// is held, not shown; said once per transition, never per tick.
	if held := l.tracker.DeadHeld(l.now()); held && !l.heldLogged {
		l.logf(logDeadHeld)
		l.heldLogged = true
	} else if !held {
		l.heldLogged = false
	}
	return true
}

// startInputProbeFn is what Server.Run calls; tests replace it.
var startInputProbeFn = startInputProbe

// startInputProbe starts the probe loop when the daemon has its tracker
// (--cmux-input-probe on), the sidebar loop and the relay (the one Standby
// predicate: without it no loop, never "not in Standby"); it reports
// whether it did. It starts whatever --cmux-sidebar says at boot: the
// sidebar can be switched on or off at run time (POST /cmux/sidebar), so the
// gate is per tick (sidebarOn reads the sidebar loop's live state) and a tick
// with the sidebar off makes no cmux call (TestInputProbeFollowsSidebarState).
func startInputProbe(ctx context.Context, s *Server, spawn func(func())) bool {
	if s.inputTracker == nil || s.cmuxSync == nil || s.relay == nil {
		return false
	}
	spawn(func() { s.runInputProbe(ctx) })
	return true
}

// runInputProbe ticks the loop every inputProbeTick until ctx ends.
func (s *Server) runInputProbe(ctx context.Context) {
	l := s.inputProbeLoop()
	t := time.NewTicker(inputProbeTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.tick()
		}
	}
}

// inputProbeCheckDeps: cmuxcheck's seams with the daemon's bounded cmux
// calls (3 s, no CMUX_* variable): Screen = ReadScreenBounded, not
// ReadScreen (10 s, inherited env).
func inputProbeCheckDeps() cmuxcheck.Deps {
	d := cmuxcheck.DefaultDeps()
	d.Screen = cmux.ReadScreenBounded
	d.TTYOf = inputprobe.TTYOf
	return d
}

// inputProbeDeps: the probe's seams, bounded the same way.
func inputProbeDeps() inputprobe.Deps {
	return inputprobe.Deps{
		Counter: inputprobe.DefaultCounter,
		TTYOf:   inputprobe.TTYOf,
		Write:   inputprobe.WriteTTY,
		Sleep:   time.Sleep,
	}
}

func (s *Server) inputProbeLoop() *inputProbeLoop {
	pd, params := inputProbeDeps(), inputprobe.DefaultParams()
	return &inputProbeLoop{
		tracker:      s.inputTracker,
		max:          inputProbeMax,
		sidebarOn:    func() bool { return s.cmuxSync != nil && s.cmuxSync.State().Enabled },
		ping:         cmux.PingBounded,
		checkRunning: func() bool { return cmuxcheck.Running(cmuxcheck.StateDir(), claudesession.PidAlive, time.Now()) },
		maintenance: func() bool {
			if s.maintenance == nil {
				return false
			}
			st, ok, err := s.maintenance.Current()
			return err != nil || (ok && st.On)
		},
		inStandby: s.relay.InStandby,
		photo: func() (cmuxcheck.Photo, error) {
			return cmuxcheck.LoadPhoto(filepath.Join(cmuxcheck.CmuxDir(), cmuxcheck.CurrentFile))
		},
		check: inputProbeCheckDeps(),
		probe: func(t inputprobe.Target) inputprobe.Result {
			return inputprobe.Probe(pd, params, t)
		},
		now:   time.Now,
		logf:  log.Printf,
		trace: os.Getenv(inputProbeTraceEnv) == "1",
	}
}

// deadInputs is the sidebar's DeadInputs seam: the surfaces whose input is
// dead (none without the probe).
func (s *Server) deadInputs() []string {
	if s.inputTracker == nil {
		return nil
	}
	now := time.Now
	if s.inputNow != nil {
		now = s.inputNow
	}
	return s.inputTracker.DeadSurfaces(now())
}

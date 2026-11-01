package cmuxcheck

import (
	"sort"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/inputprobe"
)

// Dead keyboard input (sub_x000000000000024, D3): a tab whose claude still
// draws its output but whose keystrokes never reach its pty. The check probes
// every idle interactive session (Deps.ProbeInput: a terminal status query
// on the session's own tty, never a keystroke), reports a dead one with the command that resumes it, and plans the
// resume in a new tab (ActResume) on a surface a human named.

// Visible lines of the dead-input rows (plan ## Goût, G2), verbatim.
const (
	inputDeadPrefix         = "input dead: keystrokes never reach the pty — resume in a new tab: `pigtail cmux check --fix --surface "
	infoNoBinding           = "no binding: resume by hand"
	infoCheckpointDiffer    = "binding checkpoint differs from the live session"
	infoProbeUnknown        = "input probe inconclusive: not reported as dead"
	infoNoControl           = "input probe inconclusive: no other idle session to prove the probe gets answers"
	infoPilotNoResume       = "pilot session: never resumed by the check, resume by hand"
	blockAccountDiffers     = "binding account differs from the live session's: resume by hand"
	noteProbeOnly           = "no photo before: relaunch repairs skipped, probe only"
	blockUnsafeModeRelaunch = "binding runs bypassPermissions: relaunch it by hand (D8)"
)

// SettleAfterStart: a claude younger than this is never probed (its counter
// is noisy while it starts: 338 measured, 2026-10-27).
const SettleAfterStart = 2 * time.Minute

// InputDeadProblem is the problem line of a dead-input row for surface (its
// full UUID).
func InputDeadProblem(surface string) string { return inputDeadPrefix + surface + "`" }

// ProbeTargets lists the sessions the input probe may touch: one live
// interactive claude alone on its surface, with a session id from its native
// file, wanted by o, not the caller's own pane, not in Standby (D16: an
// unreadable marker counts as Standby), idle (busySignals empty) with an
// empty input box, and (when o.Pick is set) picked by it. Sorted by surface
// without Pick. The current photo is not needed to
// pick them: a session born after the last relaunch is probed too.
func ProbeTargets(d Deps, o Options, _ Photo, live map[string][]LiveClaude) ([]inputprobe.Target, error) {
	if d.TTYListing == nil || d.TTYOf == nil {
		return nil, nil // a tty is never guessed
	}
	ttys, err := claudeTTYs(d, live)
	if err != nil {
		return nil, err
	}
	surfaces := make([]string, 0, len(live))
	for sf := range live {
		surfaces = append(surfaces, sf)
	}
	sort.Strings(surfaces)
	var cands []inputprobe.Target
	for _, sf := range surfaces {
		lives := live[sf]
		if len(lives) != 1 || sf == "" || sameSurface(sf, o.Self) || !o.wants(sf) {
			continue
		}
		lc := lives[0]
		if lc.SessionID == "" || (lc.Kind != "" && lc.Kind != "interactive") {
			continue
		}
		if lc.StartedAt.UnixMilli() <= 0 || d.Now().Sub(lc.StartedAt) < SettleAfterStart {
			continue // start unknown or too recent: not settled
		}
		if in, _ := o.standby(lc.SessionID); in {
			continue
		}
		if len(busySignals(d, sf, lc.PID, lc.ConfigDir, lc.SessionID)) > 0 {
			continue
		}
		sc, err := d.Screen(sf)
		if err != nil {
			continue // unreadable: a draft cannot be ruled out
		}
		if draft, found := claudesession.InputDraft(sc); found && draft != "" {
			continue
		}
		tty := ttys.byPID[lc.PID]
		if tty == "" || ttys.users[tty] > 1 {
			continue // no tty in the listing, or one tty carried by two claudes
		}
		cands = append(cands, inputprobe.Target{Surface: sf, PID: lc.PID, SessionID: lc.SessionID, TTY: tty})
	}
	// TTYOf (a ps and a libproc walk) runs only on what will be probed: the
	// caller's Pick narrows the candidates first.
	if o.Pick != nil {
		cands = o.Pick(cands)
	}
	var out []inputprobe.Target
	for _, c := range cands {
		info, err := d.TTYOf(c.PID)
		if err != nil || !info.Foreground || info.TTY != c.TTY {
			continue // claude does not read its tty alone: the answer could reach another process
		}
		out = append(out, c)
	}
	return out, nil
}

// claudeTTYMap: the tty of every live claude (all surfaces) and how many
// claudes carry each tty.
type claudeTTYMap struct {
	byPID map[int]string
	users map[string]int
}

// claudeTTYs reads them from one listing.
func claudeTTYs(d Deps, live map[string][]LiveClaude) (claudeTTYMap, error) {
	m := claudeTTYMap{byPID: map[int]string{}, users: map[string]int{}}
	if len(live) == 0 {
		return m, nil
	}
	raw, err := d.TTYListing()
	if err != nil {
		return m, err
	}
	col := map[int]string{}
	for _, r := range inputprobe.ParseProcs(raw) {
		col[r.PID] = inputprobe.TTYPath(r.TTY)
	}
	for _, lives := range live {
		for _, lc := range lives {
			if tty := col[lc.PID]; tty != "" {
				m.byPID[lc.PID] = tty
				m.users[tty]++
			}
		}
	}
	return m, nil
}

// panelOf finds a bound panel by surface id, without case.
func panelOf(ph Photo, surface string) (Panel, bool) {
	for _, p := range ph.Panels {
		if sameSurface(p.SurfaceID, surface) && p.Binding != nil {
			return p, true
		}
	}
	return Panel{}, false
}

// probeInput is Run's step after Classify: nothing unless o.Probe and the
// seam is wired.
func probeInput(rows []Row, before, now Photo, live map[string][]LiveClaude, l Launch, o Options, d Deps) []Row {
	if !o.Probe || d.ProbeInput == nil {
		return rows
	}
	targets, err := ProbeTargets(d, o, now, live)
	if err != nil {
		return rows
	}
	return probeRows(rows, targets, before, now, live, l, o, d)
}

// controlTarget picks the positive control of a pass restricted by
// --surface: the first session ProbeTargets would return without the
// filter, outside the named surfaces.
func controlTarget(d Deps, o Options, now Photo, live map[string][]LiveClaude) (inputprobe.Target, bool) {
	wide := o
	wide.Surfaces = nil
	all, err := ProbeTargets(d, wide, now, live)
	if err != nil {
		return inputprobe.Target{}, false
	}
	for _, t := range all {
		if !o.wants(t.Surface) {
			return t, true
		}
	}
	return inputprobe.Target{}, false
}

// probeVerdicts probes the targets in two rounds (a dead input needs two
// Dead in a row; round 2 re-probes only the round 1 Dead) and returns each
// target's outcome: Alive, Dead (confirmed, and an Alive was seen in the
// pass), or Unknown with the Info that says why. Under --surface a control
// outside the filter is probed first; the named targets are probed only if
// it is Alive.
func probeVerdicts(targets []inputprobe.Target, now Photo, live map[string][]LiveClaude, o Options, d Deps) (map[string]inputprobe.Verdict, map[string]string) {
	verdict := map[string]inputprobe.Verdict{}
	info := map[string]string{}
	if len(targets) == 0 {
		return verdict, info
	}
	sawAlive := false
	if len(o.Surfaces) > 0 {
		ctl, ok := controlTarget(d, o, now, live)
		switch {
		case !ok:
			for _, t := range targets {
				verdict[t.Surface], info[t.Surface] = inputprobe.VerdictUnknown, infoNoControl
			}
			return verdict, info
		case d.ProbeInput(ctl).Verdict != inputprobe.VerdictAlive:
			for _, t := range targets {
				verdict[t.Surface], info[t.Surface] = inputprobe.VerdictUnknown, infoProbeUnknown
			}
			return verdict, info
		}
		sawAlive = true
	}
	round1 := map[string]inputprobe.Verdict{}
	for _, t := range targets {
		round1[t.Surface] = d.ProbeInput(t).Verdict
		if round1[t.Surface] == inputprobe.VerdictAlive {
			sawAlive = true
		}
	}
	round2 := map[string]inputprobe.Verdict{}
	for _, t := range targets {
		if round1[t.Surface] == inputprobe.VerdictDead {
			round2[t.Surface] = d.ProbeInput(t).Verdict
			if round2[t.Surface] == inputprobe.VerdictAlive {
				sawAlive = true
			}
		}
	}
	for _, t := range targets {
		r1, r2 := round1[t.Surface], round2[t.Surface]
		switch {
		case r1 == inputprobe.VerdictAlive || r2 == inputprobe.VerdictAlive:
			verdict[t.Surface] = inputprobe.VerdictAlive
		case r1 == inputprobe.VerdictDead && r2 == inputprobe.VerdictDead && sawAlive:
			verdict[t.Surface] = inputprobe.VerdictDead
		default:
			verdict[t.Surface], info[t.Surface] = inputprobe.VerdictUnknown, infoProbeUnknown
		}
	}
	return verdict, info
}

// probeRows probes each target and records a dead input on its row: the row
// Classify built for the surface (skipped when it already carries a problem
// or a block), else a row built from the current photo's binding (a tab born
// after the relaunch has none). Every dead-input row takes its line, cwd and
// mode from the current photo's binding, else the photo before's.
func probeRows(rows []Row, targets []inputprobe.Target, before, now Photo, live map[string][]LiveClaude, l Launch, o Options, d Deps) []Row {
	var todo []inputprobe.Target
	for _, t := range targets {
		if sameSurface(t.Surface, o.Self) || !o.wants(t.Surface) {
			continue
		}
		idx := -1
		for i := range rows {
			if sameSurface(rows[i].SurfaceID, t.Surface) {
				idx = i
				break
			}
		}
		if idx >= 0 && (len(rows[idx].Problems) > 0 || rows[idx].Blocked != "") {
			continue
		}
		todo = append(todo, t)
	}
	verdicts, infos := probeVerdicts(todo, now, live, o, d)
	for _, t := range todo {
		res := verdicts[t.Surface]
		if res == inputprobe.VerdictAlive {
			continue // alive: nothing to say
		}
		idx := -1
		for i := range rows {
			if sameSurface(rows[i].SurfaceID, t.Surface) {
				idx = i
				break
			}
		}
		var lc LiveClaude
		for _, x := range live[t.Surface] {
			if x.PID == t.PID {
				lc = x
			}
		}
		p, bound := panelOf(now, t.Surface)
		fromNow := bound
		if !bound {
			p, bound = panelOf(before, t.Surface)
		}
		if idx < 0 {
			r := Row{SurfaceID: t.Surface, SessionID: t.SessionID, PID: t.PID, LiveConfigDir: lc.ConfigDir}
			if bound {
				r.Title, r.WorkspaceTitle = p.Title, p.WorkspaceTitle
				r.SessionID, r.ConfigDir = p.Binding.CheckpointID, p.Binding.ConfigDir()
			}
			rows = append(rows, r)
			idx = len(rows) - 1
		}
		if res == inputprobe.VerdictUnknown {
			// Never reported as dead, but never silent either: the row
			// (Classify's, or built above for a tab Classify did not see)
			// says the probe could not tell.
			rows[idx].Info = append(rows[idx].Info, infos[t.Surface])
			continue
		}
		inputDead(&rows[idx], t, lc, p, bound, l, o, d)
		if !fromNow {
			// Note (a) of task 5: the workspace only from the current photo;
			// the photo before's may be stale, so the resume asks WorkspaceOf
			// (before any signal).
			rows[idx].workspace = ""
		}
	}
	return rows
}

// inputDead fills a dead-input row and plans its resume when every guard
// holds (bloquant 3): a surface a human named, the binding's checkpoint is
// the live conversation, no bypass binding (D8), its transcript under the
// binding's account (D2), that account the live claude's.
func inputDead(r *Row, t inputprobe.Target, lc LiveClaude, p Panel, bound bool, l Launch, o Options, d Deps) {
	r.PID, r.InputDead, r.Remaining = t.PID, true, true
	if lc.ConfigDir != "" {
		r.LiveConfigDir = lc.ConfigDir
	}
	r.named = o.named(t.Surface)
	r.problem(InputDeadProblem(t.Surface))
	if !bound {
		r.Info = append(r.Info, infoNoBinding)
		return
	}
	b := *p.Binding
	mode, bypass := guardBypass(p.ExpectedMode())
	r.Mode = mode
	r.cwd = b.Cwd
	// The resume (--fix) re-checks its guards on these, then opens the new
	// tab in the panel's workspace.
	r.binding, r.bypass, r.workspace = &b, bypass, p.WorkspaceID
	r.Line = RelaunchLine(b, mode, accountDefault(d, b), l.PID)
	if !r.named {
		return
	}
	// The daemon's pilot is never resumed, even named (plan task 4).
	if o.Pilot != "" && sameSurface(t.Surface, o.Pilot) {
		r.Info = append(r.Info, infoPilotNoResume)
		return
	}
	cfg := b.ConfigDir()
	if cfg == "" {
		cfg = defaultConfigDir()
	}
	switch {
	case b.CheckpointID != t.SessionID:
		r.Info = append(r.Info, infoCheckpointDiffer)
		return
	case bypass:
		r.block(blockUnsafeModeRelaunch)
		return
	}
	if why := unresumable(d, b); why != "" {
		r.block(why)
		return
	}
	if lc.ConfigDir == "" || canon(cfg) != canon(lc.ConfigDir) {
		r.block(blockAccountDiffers)
		return
	}
	r.Actions = append(r.Actions, ActResume)
}

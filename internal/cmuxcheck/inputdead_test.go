package cmuxcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/inputprobe"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
)

// healthyA: a claude on S-A serving sid-a, relaunched by a previous --fix
// (marker of this cmux), idle for an hour: Classify finds nothing to do.
func healthyA(t *testing.T, w *world) {
	w.add(t, 60, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
}

// addControl puts an idle, eligible second session (S-CTL) in the world: the
// pass's positive control, whose probe answers Alive.
func addControl(t *testing.T, w *world) {
	t.Helper()
	for _, p := range w.procs {
		if p.PID == 90 {
			return
		}
	}
	w.add(t, 90, "S-CTL", "/cfg/a", "sid-ctl", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
}

// probeRunSeq runs Classify then the input probe step with a fake probe: the
// n-th probe of a surface answers seq[surface][n] (the last one repeats; S-CTL
// is Alive unless seq says otherwise; a surface with no sequence is Alive).
// all records every probed target, the control included.
func probeRunSeq(t *testing.T, w *world, before, now Photo, o Options, seq map[string][]inputprobe.Verdict) ([]Row, *[]inputprobe.Target) {
	t.Helper()
	var all []inputprobe.Target
	n := map[string]int{}
	d := w.deps()
	d.ProbeInput = func(tg inputprobe.Target) inputprobe.Result {
		all = append(all, tg)
		vs := seq[tg.Surface]
		if len(vs) == 0 {
			return inputprobe.Result{Verdict: inputprobe.VerdictAlive}
		}
		i := n[tg.Surface]
		n[tg.Surface]++
		if i >= len(vs) {
			i = len(vs) - 1
		}
		return inputprobe.Result{Verdict: vs[i]}
	}
	live, err := Live(d)
	if err != nil {
		t.Fatal(err)
	}
	rows := Classify(before, now, live, cleanLaunch(), o, d)
	return probeInput(rows, before, now, live, cleanLaunch(), o, d), &all
}

// probeRun: every surface but S-CTL answers verdict; the calls returned
// exclude the control. A second idle session (S-CTL, Alive) is in the world.
func probeRun(t *testing.T, w *world, before, now Photo, o Options, verdict inputprobe.Verdict) ([]Row, *[]inputprobe.Target) {
	t.Helper()
	addControl(t, w)
	seq := map[string][]inputprobe.Verdict{}
	for _, p := range w.procs {
		if sf := strings.ToUpper(strings.TrimPrefix(strings.Fields(w.env[p.PID])[1], "CMUX_SURFACE_ID=")); sf != "S-CTL" {
			seq[sf] = []inputprobe.Verdict{verdict}
		}
	}
	rows, all := probeRunSeq(t, w, before, now, o, seq)
	var calls []inputprobe.Target
	for _, c := range *all {
		if c.Surface != "S-CTL" {
			calls = append(calls, c)
		}
	}
	return rows, &calls
}

func probeOpts(surfaces ...string) Options { return Options{Probe: true, Surfaces: surfaces} }

func TestInputDeadRowReportsCommand(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, _ := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictDead)
	r := row(t, rows, "S-A")
	want := "input dead: keystrokes never reach the pty — resume in a new tab: `pigtail cmux check --fix --surface S-A`"
	if len(r.Problems) != 1 || r.Problems[0] != want {
		t.Fatalf("problems %q, want %q", r.Problems, want)
	}
	if !r.InputDead || !r.Remaining || r.Category() != CatReported || len(r.Actions) != 0 {
		t.Fatalf("row = %+v, category %s: want reported only, no action", r, r.Category())
	}
}

func TestInputDeadNamedPlansResume(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, _ := probeRun(t, w, before, now, probeOpts("S-A"), inputprobe.VerdictDead)
	r := row(t, rows, "S-A")
	if len(r.Actions) != 1 || r.Actions[0] != ActResume || !r.Fixable() || !r.named {
		t.Fatalf("row = %+v, want [resume-new-tab] fixable", r)
	}
}

func TestInputDeadNeverResumesUnattended(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	o := probeOpts("S-A")
	o.Unattended = true
	rows, _ := probeRun(t, w, before, now, o, inputprobe.VerdictDead)
	if r := row(t, rows, "S-A"); len(r.Actions) != 0 || r.named {
		t.Fatalf("row = %+v, want no resume under --unattended", r)
	}
}

// The pilot (pigtaild's, D8) is never resumed, even when the pass names its
// surface: the same options without Pilot plan the resume.
func TestInputDeadNeverResumesPilot(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	if rows, _ := probeRun(t, w, before, now, probeOpts("S-A"), inputprobe.VerdictDead); len(row(t, rows, "S-A").Actions) == 0 {
		t.Fatal("control: a named dead input plans its resume")
	}
	o := probeOpts("S-A")
	o.Pilot = "S-A"
	rows, _ := probeRun(t, w, before, now, o, inputprobe.VerdictDead)
	if r := row(t, rows, "S-A"); len(r.Actions) != 0 || !slices.Contains(r.Info, infoPilotNoResume) {
		t.Fatalf("row = %+v, want no resume on the pilot, and why", r)
	}
}

func TestInputDeadNoResumeWhenCheckpointDiffersFromLive(t *testing.T) {
	w := newWorld()
	w.add(t, 60, "S-N", "/cfg/a", "sid-live", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	now = withNewPanel(now, "S-N", "sid-old", "/w/n", "/cfg/a")
	rows, _ := probeRun(t, w, before, now, probeOpts("S-N"), inputprobe.VerdictDead)
	r := row(t, rows, "S-N")
	if len(r.Actions) != 0 || !strings.Contains(strings.Join(r.Info, "|"), "binding checkpoint differs from the live session") {
		t.Fatalf("row = %+v, want reported only with the info", r)
	}
}

// withNewPanel adds a bound pane to the photo (a tab born after the relaunch).
func withNewPanel(p Photo, surface, sid, cwd, cfg string) Photo {
	out := p
	out.Panels = append(append([]Panel(nil), p.Panels...), Panel{SurfaceID: surface, WorkspaceID: "W1", Title: "new-tab",
		Binding: &Binding{AutoResume: true, CheckpointID: sid, Cwd: cwd, Env: map[string]string{"CLAUDE_CONFIG_DIR": cfg},
			Command: "cd -- '" + cwd + "' && claude --resume " + sid}})
	return out
}

func TestInputDeadClassifyRowCarriesLineAndCwd(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	now = withPanel(now, "S-A", func(p *Panel) { p.Binding.Cwd = "/w/a-now" })
	// sanity: the healthy row of Classify carries neither
	w0 := newWorld()
	healthyA(t, w0)
	plain := row(t, classifyWith(t, w0, before, now, Options{}), "S-A")
	if plain.Line != "" || plain.cwd != "" || len(plain.Problems) != 0 {
		t.Fatalf("fixture: Classify's row = %+v, want healthy with no line", plain)
	}
	rows, _ := probeRun(t, w, before, now, probeOpts("S-A"), inputprobe.VerdictDead)
	r := row(t, rows, "S-A")
	if r.cwd != "/w/a-now" || !strings.Contains(r.Line, "CLAUDE_CONFIG_DIR=/cfg/a") || !strings.Contains(r.Line, "PIGTAIL_CMUX_RELAUNCH=7") {
		t.Fatalf("row cwd %q line %q: want the current photo's binding", r.cwd, r.Line)
	}
	// the binding gone from the current photo: the photo before's
	now2 := withPanel(now, "S-A", func(p *Panel) { p.Binding = nil })
	w2 := newWorld()
	healthyA(t, w2)
	rows, _ = probeRun(t, w2, before, now2, probeOpts("S-A"), inputprobe.VerdictDead)
	if r := row(t, rows, "S-A"); r.cwd != "/w/a" || r.Line == "" {
		t.Fatalf("fallback: cwd %q line %q, want the photo before's", r.cwd, r.Line)
	}
	// the workspace only from the current photo (note (a)): from the photo
	// before it may be stale, so the resume asks WorkspaceOf
	if r := row(t, rows, "S-A"); r.workspace != "" {
		t.Fatalf("fallback: workspace %q, want none from the photo before", r.workspace)
	}
}

func TestInputDeadRowForTabCreatedAfterRelaunch(t *testing.T) {
	w := newWorld()
	w.add(t, 61, "S-N", "/cfg/a", "sid-n", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	now = withNewPanel(now, "S-N", "sid-n", "/w/n", "/cfg/a")
	rows, calls := probeRun(t, w, before, now, probeOpts("S-N"), inputprobe.VerdictDead)
	r := row(t, rows, "S-N")
	if !r.InputDead || r.cwd != "/w/n" || r.Line == "" || r.SessionID != "sid-n" || r.PID != 61 || r.Title != "new-tab" {
		t.Fatalf("row = %+v", r)
	}
	if len(r.Actions) != 1 || r.Actions[0] != ActResume {
		t.Fatalf("actions %v, want resume", r.Actions)
	}
	if len(*calls) != 2 || (*calls)[0].PID != 61 || (*calls)[0].SessionID != "sid-n" {
		t.Fatalf("probed %+v, want two rounds", *calls)
	}
}

func TestInputDeadWithEmptyBeforePhoto(t *testing.T) {
	w := newWorld()
	w.add(t, 62, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow.Add(-time.Hour), "")
	addControl(t, w)
	o := runOpts(t)
	o.Before = &Photo{Source: "given"}
	o.Probe = true
	d := w.deps()
	d.ProbeInput = func(tg inputprobe.Target) inputprobe.Result {
		if tg.Surface == "S-CTL" {
			return inputprobe.Result{Verdict: inputprobe.VerdictAlive}
		}
		return inputprobe.Result{Verdict: inputprobe.VerdictDead}
	}
	rep := Run(d, repairer(w, &pane{t: t, w: w}), o)
	if rep.Fatal != "" {
		t.Fatalf("fatal %q, want the probe to run", rep.Fatal)
	}
	if !strings.Contains(strings.Join(rep.Notes, "|"), "no photo before: relaunch repairs skipped, probe only") {
		t.Fatalf("notes %q", rep.Notes)
	}
	if r := row(t, rep.Rows, "S-A"); !r.InputDead {
		t.Fatalf("row = %+v", r)
	}
}

func TestNoPhotoBeforeStillFatalWhenProbeOff(t *testing.T) {
	w := newWorld()
	o := runOpts(t)
	o.Before = &Photo{Source: "given"}
	rep := Run(w.deps(), repairer(w, &pane{t: t, w: w}), o)
	if rep.Fatal != "no photo of the sessions before the relaunch" {
		t.Fatalf("fatal %q", rep.Fatal)
	}
}

func TestInputDeadNoBindingNoResume(t *testing.T) {
	w := newWorld()
	w.add(t, 63, "S-X", "/cfg/a", "sid-x", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	rows, _ := probeRun(t, w, before, now, probeOpts("S-X"), inputprobe.VerdictDead)
	r := row(t, rows, "S-X")
	if len(r.Actions) != 0 || r.Line != "" || !strings.Contains(strings.Join(r.Info, "|"), "no binding: resume by hand") {
		t.Fatalf("row = %+v", r)
	}
}

func TestInputDeadRowCarriesModeFromGuardBypass(t *testing.T) {
	w := newWorld()
	w.add(t, 64, "S-N", "/cfg/a", "sid-n", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	now = withNewPanel(now, "S-N", "sid-n", "/w/n", "/cfg/a")
	now = withPanel(now, "S-N", func(p *Panel) { p.AgentMode = claudesession.ModeBypass })
	rows, _ := probeRun(t, w, before, now, probeOpts("S-N"), inputprobe.VerdictDead)
	r := row(t, rows, "S-N")
	if r.Mode != "" || len(r.Actions) != 0 || r.Blocked != "binding runs bypassPermissions: relaunch it by hand (D8)" {
		t.Fatalf("row = %+v", r)
	}
}

func TestInputDeadBlockedWhenTranscriptAbsent(t *testing.T) {
	w := newWorld()
	w.add(t, 65, "S-N", "/cfg/a", "sid-n", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	w.noTranscript["/cfg/a|sid-n"] = true
	before, now := photos(t)
	now = withNewPanel(now, "S-N", "sid-n", "/w/n", "/cfg/a")
	rows, _ := probeRun(t, w, before, now, probeOpts("S-N"), inputprobe.VerdictDead)
	if r := row(t, rows, "S-N"); len(r.Actions) != 0 || !strings.Contains(r.Blocked, "no transcript of sid-n") {
		t.Fatalf("row = %+v", r)
	}
}

func TestInputDeadBlockedWhenAccountDiffersFromLive(t *testing.T) {
	w := newWorld()
	w.add(t, 66, "S-N", "/cfg/b", "sid-n", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	now = withNewPanel(now, "S-N", "sid-n", "/w/n", "/cfg/a")
	rows, _ := probeRun(t, w, before, now, probeOpts("S-N"), inputprobe.VerdictDead)
	if r := row(t, rows, "S-N"); len(r.Actions) != 0 || r.Blocked != "binding account differs from the live session's: resume by hand" {
		t.Fatalf("row = %+v", r)
	}
}

func TestInputDeadCopyVerbatim(t *testing.T) {
	for got, want := range map[string]string{
		InputDeadProblem("S"): "input dead: keystrokes never reach the pty — resume in a new tab: `pigtail cmux check --fix --surface S`",
		infoNoBinding:         "no binding: resume by hand",
		infoCheckpointDiffer:  "binding checkpoint differs from the live session",
		infoProbeUnknown:      "input probe inconclusive: not reported as dead",
		infoNoControl:         "input probe inconclusive: no other idle session to prove the probe gets answers",
		blockAccountDiffers:   "binding account differs from the live session's: resume by hand",
		noteProbeOnly:         "no photo before: relaunch repairs skipped, probe only",
		infoPilotNoResume:     "pilot session: never resumed by the check, resume by hand",
		// the resume (task 5)
		resultInputBack:           "input back, nothing done",
		infoOldTabKept:            "old tab not closed",
		blockNoCwd:                "binding names no working directory: resume by hand",
		refusedNotNamed:           "resume refused: only for a surface named by --surface",
		TabNotOpenedMessage("L"):  "old claude stopped, new tab not opened, old tab kept: resume by hand with `L`",
		resultProbeInconclusive:   "input probe inconclusive, nothing done",
		HandResumeMessage("L"):    "new claude not seen in the new tab, old tab kept: resume by hand with `L`",
		StopFirstMessage(70, "L"): "old claude (pid 70) still running after SIGTERM, nothing opened: stop it first, then resume by hand with `L`",
	} {
		if got != want {
			t.Errorf("copy %q, want %q", got, want)
		}
	}
	// the dead row as printed
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, _ := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictDead)
	var buf bytes.Buffer
	Report{Rows: rows}.Write(&buf)
	if !strings.Contains(buf.String(), "reported only: input dead: keystrokes never reach the pty — resume in a new tab: `pigtail cmux check --fix --surface S-A`") {
		t.Fatalf("report:\n%s", buf.String())
	}
}

func TestInputAliveAddsNothing(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, calls := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictAlive)
	r := row(t, rows, "S-A")
	if len(*calls) != 1 || r.InputDead || len(r.Problems) != 0 || len(r.Info) != 0 || r.Remaining {
		t.Fatalf("row = %+v calls %d", r, len(*calls))
	}
}

func TestInputUnknownAddsNoProblem(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, _ := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictUnknown)
	r := row(t, rows, "S-A")
	if r.InputDead || len(r.Problems) != 0 || r.Remaining || strings.Join(r.Info, "|") != "input probe inconclusive: not reported as dead" {
		t.Fatalf("row = %+v", r)
	}
}

func TestInputBusySessionNotProbed(t *testing.T) {
	cases := map[string]func(w *world){
		"status busy": func(w *world) { n := w.native[60]; n.Status = "busy"; w.native[60] = n },
		"transcript written": func(w *world) {
			w.paths["sid-a"] = "/t/a.jsonl"
			w.mtimes["/t/a.jsonl"] = tNow.Add(-10 * time.Second)
		},
		"draft": func(w *world) {
			w.screens["S-A"] = strings.Replace(screenFile(t, "screen-idle-empty.txt"), "❯", "❯ half a sentence", 1)
		},
	}
	for name, f := range cases {
		w := newWorld()
		healthyA(t, w)
		f(w)
		before, now := photos(t)
		_, calls := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictDead)
		if len(*calls) != 0 {
			t.Fatalf("%s: probed %+v", name, *calls)
		}
	}
	// Self: the session running the check
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	o := probeOpts()
	o.Self = "S-A"
	if _, calls := probeRun(t, w, before, now, o, inputprobe.VerdictDead); len(*calls) != 0 {
		t.Fatalf("self probed %+v", *calls)
	}
}

func TestInputStandbySessionNotProbed(t *testing.T) {
	for name, f := range map[string]func(string) (bool, error){
		"standby":    func(string) (bool, error) { return true, nil },
		"unreadable": func(string) (bool, error) { return false, errors.New("marker unreadable") },
	} {
		w := newWorld()
		healthyA(t, w)
		before, now := photos(t)
		o := probeOpts()
		o.InStandby = f
		if _, calls := probeRun(t, w, before, now, o, inputprobe.VerdictDead); len(*calls) != 0 {
			t.Fatalf("%s: probed %+v", name, *calls)
		}
	}
}

func TestInputNotProbedWhenOptionsProbeFalse(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, calls := probeRun(t, w, before, now, Options{}, inputprobe.VerdictDead)
	if len(*calls) != 0 || row(t, rows, "S-A").InputDead {
		t.Fatalf("probed %+v", *calls)
	}
}

func TestInputNotProbedWhenTwoClaudesOnSurface(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.add(t, 67, "S-A", "/cfg/a", "sid-z", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	if _, calls := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictDead); len(*calls) != 0 {
		t.Fatalf("probed %+v", *calls)
	}
}

func TestProbeTargetsOnlyIdleInteractive(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.add(t, 68, "S-B", "/cfg/b", "sid-b", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	n := w.native[68]
	n.Status = "busy"
	w.native[68] = n
	w.add(t, 69, "S-E", "/cfg/a", "sid-e", "claude -p", cleanLaunch().Start.Add(2*time.Minute), "")
	n = w.native[69]
	n.Kind = "print"
	w.native[69] = n
	d := w.deps()
	live, err := Live(d)
	if err != nil {
		t.Fatal(err)
	}
	_, now := photos(t)
	got, err := ProbeTargets(d, Options{}, now, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (inputprobe.Target{Surface: "S-A", PID: 60, SessionID: "sid-a", TTY: "/dev/ttys060"}) {
		t.Fatalf("targets %+v, want S-A only", got)
	}
}

func TestRedactedMasksInputDeadRow(t *testing.T) {
	scopetest.Three(t)
	other := otherScope(t)
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, _ := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictDead)
	rep := Report{Launch: cleanLaunch(), Rows: rows}
	// /cfg/a resolves to no scope the reader can read: masked.
	red := rep.Redacted(other, func(string) (string, bool) { return "", false })
	var buf bytes.Buffer
	red.Write(&buf)
	b, _ := json.Marshal(red)
	out := buf.String() + string(b)
	if strings.Contains(out, "input dead") || strings.Contains(out, "S-A`") || strings.Contains(out, "sid-a") {
		t.Fatalf("another scope's dead row leaks:\n%s", out)
	}
	if !strings.Contains(out, string(CatReported)) {
		t.Fatalf("masked row lost its class:\n%s", out)
	}
	if m := rep.Redacted(scope.Master(), nil); !strings.Contains(strings.Join(row(t, m.Rows, "S-A").Problems, ""), "input dead") {
		t.Fatal("master lost the row")
	}
}

func TestInputDeadJSONCarriesProblem(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, _ := probeRun(t, w, before, now, probeOpts(), inputprobe.VerdictDead)
	b, err := json.Marshal(Report{Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Rows []struct {
			SurfaceID string   `json:"surface_id"`
			Problems  []string `json:"problems"`
			InputDead bool     `json:"input_dead"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for _, r := range back.Rows {
		if r.SurfaceID == "S-A" && r.InputDead && len(r.Problems) == 1 && strings.HasPrefix(r.Problems[0], "input dead: ") {
			return
		}
	}
	t.Fatalf("json %s", b)
}

// Review round 1: an Unknown verdict on a target Classify built no row for
// (a tab born after the relaunch, or every target with no photo before)
// still shows « inconclusive » on a row of its own, never vanishes.
func TestInputUnknownWithoutClassifyRowAddsInfo(t *testing.T) {
	w := newWorld()
	w.add(t, 61, "S-N", "/cfg/a", "sid-n", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	now = withNewPanel(now, "S-N", "sid-n", "/w/n", "/cfg/a")
	rows, calls := probeRun(t, w, before, now, probeOpts("S-N"), inputprobe.VerdictUnknown)
	if len(*calls) != 1 {
		t.Fatalf("probed %+v", *calls)
	}
	r := row(t, rows, "S-N")
	if r.InputDead || len(r.Problems) != 0 || r.Remaining || len(r.Actions) != 0 || strings.Join(r.Info, "|") != "input probe inconclusive: not reported as dead" {
		t.Fatalf("row = %+v", r)
	}
	if r.SessionID != "sid-n" || r.PID != 61 {
		t.Fatalf("row = %+v: want the probed session", r)
	}
}

func TestInputUnknownProbeOnlyAddsInfo(t *testing.T) {
	w := newWorld()
	w.add(t, 62, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", tNow.Add(-time.Hour), "")
	o := runOpts(t)
	o.Before = &Photo{Source: "given"}
	o.Probe = true
	d := w.deps()
	d.ProbeInput = func(inputprobe.Target) inputprobe.Result {
		return inputprobe.Result{Verdict: inputprobe.VerdictUnknown}
	}
	rep := Run(d, repairer(w, &pane{t: t, w: w}), o)
	if rep.Fatal != "" {
		t.Fatalf("fatal %q", rep.Fatal)
	}
	r := row(t, rep.Rows, "S-A")
	if r.InputDead || r.Remaining || strings.Join(r.Info, "|") != "input probe inconclusive: not reported as dead" {
		t.Fatalf("row = %+v", r)
	}
}

// probeTargetsOf runs ProbeTargets on a world with the A tab healthy.
func probeTargetsOf(t *testing.T, w *world) []inputprobe.Target {
	t.Helper()
	d := w.deps()
	live, err := Live(d)
	if err != nil {
		t.Fatal(err)
	}
	_, now := photos(t)
	got, err := ProbeTargets(d, Options{}, now, live)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestProbeTargetsCarriesTTYFromPid(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.ttyCol = map[int]string{60: "ttys037"}
	got := probeTargetsOf(t, w)
	if len(got) != 1 || got[0].TTY != "/dev/ttys037" {
		t.Fatalf("targets %+v, want the tty of pid 60's listing row", got)
	}
}

func TestProbeTargetsReadsOneListingForAllClaudes(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.add(t, 61, "S-B", "/cfg/b", "sid-b", "claude", cleanLaunch().Start.Add(2*time.Minute), "")
	w.add(t, 62, "S-C", "/cfg/b", "sid-c", "claude", cleanLaunch().Start.Add(2*time.Minute), "")
	n := w.native[62]
	n.Status = "busy" // a retained-out claude: its tty is in the listing, TTYOf is not asked
	w.native[62] = n
	got := probeTargetsOf(t, w)
	if len(got) != 2 {
		t.Fatalf("targets %+v, want S-A and S-B", got)
	}
	if w.listingCalls != 1 {
		t.Fatalf("%d listings, want one for all the claudes", w.listingCalls)
	}
	if len(w.ttyOfCalls) != 2 {
		t.Fatalf("TTYOf asked about %v, want only the two targets", w.ttyOfCalls)
	}
}

func TestProbeTargetsNoTargetsWithoutTTYListing(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.noTTYListing = true
	if got := probeTargetsOf(t, w); len(got) != 0 {
		t.Fatalf("targets %+v without a listing seam", got)
	}
	w2 := newWorld()
	healthyA(t, w2)
	w2.ttyCol = map[int]string{60: ""} // the claude is absent from the tty column ("??")
	if got := probeTargetsOf(t, w2); len(got) != 0 {
		t.Fatalf("targets %+v with no tty in the listing", got)
	}
}

func TestProbeTargetsRefusesSharedTTY(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.add(t, 61, "S-B", "/cfg/b", "sid-b", "claude", cleanLaunch().Start.Add(2*time.Minute), "")
	w.ttyCol = map[int]string{60: "ttys050", 61: "ttys050"}
	if got := probeTargetsOf(t, w); len(got) != 0 {
		t.Fatalf("targets %+v: two surfaces on one tty are probed by neither", got)
	}
	if len(w.ttyOfCalls) != 0 {
		t.Fatalf("TTYOf asked about %v for shared ttys", w.ttyOfCalls)
	}
}

func TestProbeTargetsSkipsUnreadableTTY(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.ttyOfErr = map[int]error{60: errors.New("ps failed")}
	if got := probeTargetsOf(t, w); len(got) != 0 {
		t.Fatalf("targets %+v with an unreadable tty", got)
	}
	w2 := newWorld()
	healthyA(t, w2)
	w2.ttyOfTTY = map[int]string{60: "/dev/ttys099"} // TTYOf disagrees with the listing
	if got := probeTargetsOf(t, w2); len(got) != 0 {
		t.Fatalf("targets %+v whose tty differs from the listing", got)
	}
}

func TestProbeTargetsNoTargetsWithoutTTYOf(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.noTTYOf = true
	if got := probeTargetsOf(t, w); len(got) != 0 {
		t.Fatalf("targets %+v without a TTYOf seam", got)
	}
}

func TestProbeTargetsSkipsBackgroundedClaude(t *testing.T) {
	for _, why := range []string{"its process group does not hold the terminal", "stopped or traced", "another process (pid 9) of the foreground group reads the tty"} {
		w := newWorld()
		healthyA(t, w)
		w.notForeground = map[int]string{60: why}
		if got := probeTargetsOf(t, w); len(got) != 0 {
			t.Fatalf("%s: targets %+v, want none", why, got)
		}
	}
}

func TestProbeTargetsSkipsUnsettledStart(t *testing.T) {
	for name, start := range map[string]time.Time{
		"null": time.UnixMilli(0), "1 min": tNow.Add(-time.Minute), "just under": tNow.Add(-SettleAfterStart + time.Second),
	} {
		w := newWorld()
		w.add(t, 60, "S-A", "/cfg/a", "sid-a", "claude", start, "")
		if name == "null" {
			n := w.native[60]
			n.StartedAt = 0
			w.native[60] = n
		}
		if got := probeTargetsOf(t, w); len(got) != 0 {
			t.Fatalf("%s: targets %+v, want none", name, got)
		}
	}
	w := newWorld()
	w.add(t, 60, "S-A", "/cfg/a", "sid-a", "claude", tNow.Add(-SettleAfterStart), "")
	if got := probeTargetsOf(t, w); len(got) != 1 {
		t.Fatalf("settled at exactly %s: targets %+v, want S-A", SettleAfterStart, got)
	}
}

func dead(n int) []inputprobe.Verdict {
	out := make([]inputprobe.Verdict, n)
	for i := range out {
		out[i] = inputprobe.VerdictDead
	}
	return out
}

func probes(calls []inputprobe.Target, surface string) int {
	n := 0
	for _, c := range calls {
		if c.Surface == surface {
			n++
		}
	}
	return n
}

func TestInputDeadNeedsAnAliveInSamePass(t *testing.T) {
	// A lone Dead target, no Alive anywhere in the pass: inconclusive.
	w := newWorld()
	healthyA(t, w)
	before, now := photos(t)
	rows, _ := probeRunSeq(t, w, before, now, probeOpts(), map[string][]inputprobe.Verdict{"S-A": dead(2)})
	r := row(t, rows, "S-A")
	if r.InputDead || len(r.Problems) != 0 || strings.Join(r.Info, "|") != infoProbeUnknown {
		t.Fatalf("lone Dead: row = %+v", r)
	}
	// Dead plus an Alive session: the problem.
	w = newWorld()
	healthyA(t, w)
	addControl(t, w)
	rows, _ = probeRunSeq(t, w, before, now, probeOpts(), map[string][]inputprobe.Verdict{"S-A": dead(2)})
	if r := row(t, rows, "S-A"); !r.InputDead || len(r.Problems) != 1 {
		t.Fatalf("Dead + Alive: row = %+v", r)
	}
}

func TestCheckDeadNeedsTwoRounds(t *testing.T) {
	cases := []struct {
		name  string
		seq   []inputprobe.Verdict
		dead  bool
		info  string
		count int
	}{
		{"dead then alive", []inputprobe.Verdict{inputprobe.VerdictDead, inputprobe.VerdictAlive}, false, "", 2},
		{"dead dead", dead(2), true, "", 2},
		{"dead then unknown", []inputprobe.Verdict{inputprobe.VerdictDead, inputprobe.VerdictUnknown}, false, infoProbeUnknown, 2},
		{"alive first", []inputprobe.Verdict{inputprobe.VerdictAlive, inputprobe.VerdictDead}, false, "", 1},
	}
	for _, c := range cases {
		w := newWorld()
		healthyA(t, w)
		addControl(t, w)
		before, now := photos(t)
		rows, calls := probeRunSeq(t, w, before, now, probeOpts(), map[string][]inputprobe.Verdict{"S-A": c.seq})
		r := row(t, rows, "S-A")
		if r.InputDead != c.dead || strings.Join(r.Info, "|") != c.info {
			t.Errorf("%s: row = %+v", c.name, r)
		}
		if got := probes(*calls, "S-A"); got != c.count {
			t.Errorf("%s: S-A probed %d times, want %d", c.name, got, c.count)
		}
		if got := probes(*calls, "S-CTL"); got != 1 {
			t.Errorf("%s: the Alive target probed %d times, want once", c.name, got)
		}
	}
}

func TestFixSurfaceDeadUsesControlOutsideFilter(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.add(t, 68, "S-B", "/cfg/b", "sid-b", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	rows, calls := probeRunSeq(t, w, before, now, probeOpts("S-A"), map[string][]inputprobe.Verdict{"S-A": dead(2), "S-B": {inputprobe.VerdictAlive}})
	if len(*calls) < 1 || (*calls)[0].Surface != "S-B" {
		t.Fatalf("probed %+v: the control S-B first", *calls)
	}
	if probes(*calls, "S-B") != 1 || probes(*calls, "S-A") != 2 {
		t.Fatalf("probed %+v: S-B once, S-A twice", *calls)
	}
	for _, r := range rows {
		if sameSurface(r.SurfaceID, "S-B") && (len(r.Problems) != 0 || len(r.Info) != 0) {
			t.Fatalf("the control has a line: %+v", r)
		}
	}
	if r := row(t, rows, "S-A"); !r.InputDead || len(r.Actions) != 1 || r.Actions[0] != ActResume {
		t.Fatalf("row = %+v, want the problem and a resume", r)
	}
}

func TestFixSurfaceNoControlIsUnknownAndDoesNothing(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.add(t, 68, "S-B", "/cfg/b", "sid-b", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	n := w.native[68]
	n.Status = "busy"
	w.native[68] = n
	w.add(t, 69, "S-C", "/cfg/c", "sid-c", "claude", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	before, now := photos(t)
	o := probeOpts("S-A")
	o.InStandby = func(sid string) (bool, error) { return sid == "sid-c", nil }
	rows, calls := probeRunSeq(t, w, before, now, o, map[string][]inputprobe.Verdict{"S-A": dead(2)})
	if len(*calls) != 0 {
		t.Fatalf("probed %+v, want nothing written", *calls)
	}
	r := row(t, rows, "S-A")
	if r.InputDead || len(r.Actions) != 0 || len(r.Problems) != 0 || strings.Join(r.Info, "|") != infoNoControl {
		t.Fatalf("row = %+v", r)
	}
}

func TestFixSurfaceControlNotAliveDoesNothing(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	addControl(t, w)
	before, now := photos(t)
	rows, calls := probeRunSeq(t, w, before, now, probeOpts("S-A"), map[string][]inputprobe.Verdict{"S-A": dead(2), "S-CTL": {inputprobe.VerdictUnknown}})
	if probes(*calls, "S-A") != 0 || probes(*calls, "S-CTL") != 1 {
		t.Fatalf("probed %+v: only the control, once", *calls)
	}
	r := row(t, rows, "S-A")
	if r.InputDead || len(r.Actions) != 0 || strings.Join(r.Info, "|") != infoProbeUnknown {
		t.Fatalf("row = %+v", r)
	}
}

// TestProbeTargetsSkipsSurfaceWhoseScreenFails: a surface whose read-screen
// fails (a claude of another cmux, seen through ps) is never a target.
func TestProbeTargetsSkipsSurfaceWhoseScreenFails(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	delete(w.screens, "S-A")
	if got := probeTargetsOf(t, w); len(got) != 0 {
		t.Fatalf("targets %+v, want none: its screen is unreadable", got)
	}
}

// Options.Pick narrows the candidates before TTYOf: it is asked only about
// the picked one, and a shared tty is still refused from the single listing.
func TestProbeTargetsAsksTTYOfOnlyForPicked(t *testing.T) {
	w := newWorld()
	healthyA(t, w)
	w.add(t, 61, "S-B", "/cfg/b", "sid-b", "claude", cleanLaunch().Start.Add(2*time.Minute), "")
	d := w.deps()
	live, err := Live(d)
	if err != nil {
		t.Fatal(err)
	}
	_, now := photos(t)
	var seen int
	got, err := ProbeTargets(d, Options{Pick: func(c []inputprobe.Target) []inputprobe.Target {
		seen = len(c)
		return c[:1]
	}}, now, live)
	if err != nil || len(got) != 1 || seen != 2 || len(w.ttyOfCalls) != 1 {
		t.Fatalf("targets %+v err %v candidates %d TTYOf %v; want 1 target from 2 candidates, one TTYOf", got, err, seen, w.ttyOfCalls)
	}
}

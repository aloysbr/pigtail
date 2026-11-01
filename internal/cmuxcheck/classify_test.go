package cmuxcheck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/inputprobe"
)

var tNow = ts("2026-10-08T10:40:00Z")

type world struct {
	procs       []claudesession.Proc
	env         map[int]string
	native      map[int]claudesession.Native
	screens     map[string]string
	shells      map[int][]int
	paths       map[string]string // sid → transcript path
	mtimes      map[string]time.Time
	idleSurface map[string]bool   // a pane shell on the surface; true = at its prompt (simPanes)
	argv        map[int]string    // a process whose environment is hidden: ps -E shows its argv only
	defaults    map[string]string // account → permissions.defaultMode
	badSettings map[string]bool   // account whose settings.json does not parse
	focused     string            // the surface holding cmux's keyboard focus ("" = none)
	// noTranscript: cfg + "|" + sid whose transcript the account lacks (default: found).
	noTranscript  map[string]bool
	transcriptErr error

	// The probe's tty readers (task 8): one tty per claude pid by default.
	ttyCol        map[int]string // pid → tty column of the listing ("" = none), default ttysNNN
	notForeground map[int]string // pid → why TTYOf says claude does not read its tty alone
	ttyOfErr      map[int]error  // pid → TTYOf failure
	ttyOfTTY      map[int]string // pid → tty TTYOf reports when it differs from the listing
	noTTYListing  bool           // Deps.TTYListing nil
	noTTYOf       bool           // Deps.TTYOf nil
	listingCalls  int            // how many times the listing was read
	ttyOfCalls    []int          // pids TTYOf was asked about
	listingErr    error
}

func (w *world) ttyColOf(pid int) string {
	if c, ok := w.ttyCol[pid]; ok {
		return c
	}
	return fmt.Sprintf("ttys%03d", pid)
}

func (w *world) deps() Deps {
	d := Deps{
		Procs: func() ([]claudesession.Proc, error) { return w.procs, nil },
		Environ: func(pid int) (string, error) {
			if e, ok := w.env[pid]; ok {
				return e, nil
			}
			if a, ok := w.argv[pid]; ok {
				return a, nil // like real cmux: a pane shell's environment is unreadable
			}
			return "", errors.New("gone")
		},
		Native: func(_ string, pid int) (claudesession.Native, bool) { n, ok := w.native[pid]; return n, ok },
		Screen: func(s string) (string, error) {
			sc, ok := w.screens[s]
			if !ok {
				return "", errors.New("no surface")
			}
			return sc, nil
		},
		TranscriptFound: func(cfg, sid string) (bool, error) {
			return !w.noTranscript[cfg+"|"+sid], w.transcriptErr
		},
		Shells:     func(pid int) ([]int, error) { return w.shells[pid], nil },
		Transcript: func(_, sid string) (string, bool) { p, ok := w.paths[sid]; return p, ok },
		ModTime: func(p string) (time.Time, error) {
			if m, ok := w.mtimes[p]; ok {
				return m, nil
			}
			return tNow.Add(-time.Hour), nil
		},
		PS:          func() (string, error) { return w.simPanes(), nil },
		SurfaceTTYs: func() (map[string]string, error) { return w.simTTYs(), nil },
		DefaultMode: func(cfg string) (string, error) {
			if w.badSettings[cfg] {
				return "", errors.New("parse settings.json")
			}
			return w.defaults[cfg], nil
		},
		Now:     func() time.Time { return tNow },
		Focused: func() (string, error) { return w.focused, nil },
	}
	if !w.noTTYListing {
		d.TTYListing = func() ([]byte, error) {
			w.listingCalls++
			if w.listingErr != nil {
				return nil, w.listingErr
			}
			var b strings.Builder
			for _, p := range w.procs {
				col := w.ttyColOf(p.PID)
				if col == "" {
					col = "??"
				}
				fmt.Fprintf(&b, "%d %d %d S+ %s\n", p.PID, p.PID, p.PID, col)
			}
			return []byte(b.String()), nil
		}
	}
	if !w.noTTYOf {
		d.TTYOf = func(pid int) (inputprobe.TTYInfo, error) {
			w.ttyOfCalls = append(w.ttyOfCalls, pid)
			if err := w.ttyOfErr[pid]; err != nil {
				return inputprobe.TTYInfo{}, err
			}
			tty := "/dev/" + w.ttyColOf(pid)
			if o, ok := w.ttyOfTTY[pid]; ok {
				tty = o
			}
			if why, bg := w.notForeground[pid]; bg {
				return inputprobe.TTYInfo{TTY: tty, Why: why}, nil
			}
			return inputprobe.TTYInfo{TTY: tty, Foreground: true}, nil
		}
	}
	return d
}

// simCmuxPID is the running cmux of the simulated world (cleanLaunch, runOpts).
const simCmuxPID = 7

// simPanes renders the pane shells of idleSurface the way real cmux 0.64.24
// runs them (D22): cmux → /usr/bin/login → -/bin/zsh, one tty per surface,
// the shell's environment hidden (argv only) — so every test that starts a
// pane exercises the tie by cmux tree, never a faked shell env. A pane not at
// its prompt runs a foreground job in its own group.
func (w *world) simPanes() string {
	var b strings.Builder
	for i, s := range sortedKeys(w.idleSurface) {
		login, sh, job := 5000+10*i, 5001+10*i, 5002+10*i
		tty := simTTY(i)
		fg := sh
		if !w.idleSurface[s] {
			fg = job
		}
		fmt.Fprintf(&b, "%d %d %d %d %s /usr/bin/login\n", login, simCmuxPID, login, fg, tty)
		fmt.Fprintf(&b, "%d %d %d %d %s -/bin/zsh\n", sh, login, sh, fg, tty)
		w.argv[sh] = "-/bin/zsh"
		if fg == job {
			fmt.Fprintf(&b, "%d %d %d %d %s sleep\n", job, sh, job, fg, tty)
		}
	}
	return b.String()
}

func (w *world) simTTYs() map[string]string {
	out := map[string]string{}
	for i, s := range sortedKeys(w.idleSurface) {
		out[strings.ToUpper(s)] = simTTY(i)
	}
	return out
}

func simTTY(i int) string { return fmt.Sprintf("ttys%03d", 100+i) }

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func screenFile(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// add puts a live claude on a surface, idle for an hour, started at start.
func (w *world) add(t *testing.T, pid int, surface, cfg, sid, args string, start time.Time, extraEnv string) {
	w.procs = append(w.procs, claudesession.Proc{PID: pid, Args: args})
	w.env[pid] = "claude CMUX_SURFACE_ID=" + surface + " CLAUDE_CONFIG_DIR=" + cfg + " " + extraEnv
	w.native[pid] = claudesession.Native{PID: pid, SessionID: sid, Status: "idle", Kind: "interactive",
		StatusUpdatedAt: tNow.Add(-time.Hour).UnixMilli(), StartedAt: start.UnixMilli()}
	w.screens[surface] = screenFile(t, "screen-idle-empty.txt")
}

func newWorld() *world {
	return &world{env: map[int]string{}, native: map[int]claudesession.Native{}, screens: map[string]string{},
		shells: map[int][]int{}, paths: map[string]string{}, mtimes: map[string]time.Time{},
		idleSurface: map[string]bool{}, argv: map[int]string{}, defaults: map[string]string{}, badSettings: map[string]bool{}, noTranscript: map[string]bool{}}
}

func photos(t *testing.T) (Photo, Photo) {
	b, err := LoadPhoto("testdata/session-before.json")
	if err != nil {
		t.Fatal(err)
	}
	n, err := LoadPhoto("testdata/session-now.json")
	if err != nil {
		t.Fatal(err)
	}
	return b, n
}

func row(t *testing.T, rows []Row, surface string) Row {
	for _, r := range rows {
		if r.SurfaceID == surface {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", surface, rows)
	return Row{}
}

func classify(t *testing.T, w *world, o Options) []Row {
	before, now := photos(t)
	return classifyWith(t, w, before, now, o)
}

func classifyWith(t *testing.T, w *world, before, now Photo, o Options) []Row {
	live, err := Live(w.deps())
	if err != nil {
		t.Fatal(err)
	}
	return Classify(before, now, live, cleanLaunch(), o, w.deps())
}

// withPanel returns a copy of the photo with surface's panel changed by f.
func withPanel(p Photo, surface string, f func(*Panel)) Photo {
	out := p
	out.Panels = append([]Panel(nil), p.Panels...)
	for i := range out.Panels {
		if out.Panels[i].SurfaceID == surface {
			pn := out.Panels[i]
			if pn.Binding != nil {
				b := *pn.Binding
				pn.Binding = &b
			}
			f(&pn)
			out.Panels[i] = pn
		}
	}
	return out
}

// withoutPanel returns a copy of the photo without surface's panel (closed).
func withoutPanel(p Photo, surface string) Photo {
	out := p
	out.Panels = nil
	for _, pn := range p.Panels {
		if pn.SurfaceID != surface {
			out.Panels = append(out.Panels, pn)
		}
	}
	return out
}

// noModeInCommand: S-A as cmux writes most bindings (36/41, F4) — the mode
// only in agent.permissionMode, none in the command nor the launch args.
func noModeInCommand(pn *Panel) {
	pn.Binding.Command = strings.Replace(pn.Binding.Command, ` '\''--permission-mode'\'' '\''auto'\''`, "", 1)
	pn.Binding.LaunchArgs = []string{"/bin/claude"}
}

func hasAction(r Row, a Action) bool {
	for _, x := range r.Actions {
		if x == a {
			return true
		}
	}
	return false
}

func TestClassifyResumedSessionIsRelaunched(t *testing.T) {
	w := newWorld()
	w.add(t, 11, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(20*time.Second), "")
	r := row(t, classify(t, w, Options{}), "S-A")
	if !hasAction(r, ActRelaunch) || r.Blocked != "" || r.Mode != "auto" {
		t.Fatalf("row = %+v, want a relaunch in auto", r)
	}
	if !strings.Contains(r.Line, "PIGTAIL_CMUX_RELAUNCH=7") || !strings.Contains(r.Line, "CLAUDE_CONFIG_DIR=/cfg/a") || !strings.HasPrefix(r.Line, " export ") {
		t.Fatalf("line = %q", r.Line)
	}
}

func TestClassifyIdempotentAfterFix(t *testing.T) {
	w := newWorld()
	// relaunched by a previous --fix: carries the marker of this cmux pid, no cut turn
	w.add(t, 12, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	r := row(t, classify(t, w, Options{}), "S-A")
	if len(r.Problems) != 0 || len(r.Actions) != 0 {
		t.Fatalf("row = %+v, want nothing to do", r)
	}
	// The continue already sent by that --fix ends the transcript, unanswered
	// (a cut "prompt"), after the launch: the bound c.At.Before(l.Start) keeps
	// it from being read as a cut of this relaunch — no second continue.
	tp := filepath.Join(t.TempDir(), "a.jsonl")
	sent := `{"type":"user","timestamp":"` + cleanLaunch().Start.Add(150*time.Second).UTC().Format(time.RFC3339) + `","message":{"role":"user","content":"` + ContinueText + `"}}`
	if err := os.WriteFile(tp, []byte(sent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, isCut := claudesession.TurnCut(tp); !isCut {
		t.Fatal("fixture: the unanswered continue must read as a cut turn, or the bound is not exercised")
	}
	w.paths["sid-a"] = tp
	r = row(t, classify(t, w, Options{}), "S-A")
	if hasAction(r, ActContinue) || len(r.Actions) != 0 {
		t.Fatalf("row = %+v, want no second continue after the launch", r)
	}
}

func TestClassifyModeLostWithKnownMode(t *testing.T) {
	w := newWorld()
	w.add(t, 13, "S-E", "/cfg/a", "sid-e", "claude --resume sid-e", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	r := row(t, classify(t, w, Options{}), "S-E")
	if !hasAction(r, ActRelaunch) || r.Mode != "acceptEdits" {
		t.Fatalf("row = %+v, want a relaunch in acceptEdits (D3)", r)
	}
}

func TestClassifyModeUnknownIsInfoOnly(t *testing.T) {
	w := newWorld()
	w.add(t, 14, "S-B", "/cfg/b", "sid-b", "claude --resume sid-b", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	r := row(t, classify(t, w, Options{}), "S-B")
	if len(r.Problems) != 0 || len(r.Actions) != 0 || !strings.Contains(strings.Join(r.Info, " "), "permission mode unknown") {
		t.Fatalf("row = %+v, want info only (D3)", r)
	}
}

func TestClassifyWrongAccount(t *testing.T) {
	w := newWorld()
	w.add(t, 15, "S-B", "/cfg/a", "sid-b", "claude", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	r := row(t, classify(t, w, Options{}), "S-B")
	if !hasAction(r, ActRelaunch) || !strings.Contains(strings.Join(r.Problems, " "), "wrong account") {
		t.Fatalf("row = %+v", r)
	}
	if r.frozen {
		t.Fatal("frozen on a healthy width")
	}
}

func TestClassifyRestoreLineCleared(t *testing.T) {
	w := newWorld()
	w.add(t, 16, "S-B", "/cfg/b", "sid-b", "claude", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	w.screens["S-B"] = strings.Replace(screenFile(t, "screen-idle-empty.txt"), "❯", "❯ cmux restore claude sid-b", 1)
	r := row(t, classify(t, w, Options{}), "S-B")
	if len(r.Actions) != 1 || r.Actions[0] != ActClearRestore || r.Blocked != "" {
		t.Fatalf("row = %+v", r)
	}
}

func TestClassifyDraftBlocks(t *testing.T) {
	for _, f := range []string{"screen-draft.txt", "screen-draft-history.txt", "screen-draft-multiline.txt", "screen-no-input.txt"} {
		w := newWorld()
		w.add(t, 17, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", cleanLaunch().Start, "")
		w.screens["S-A"] = screenFile(t, f)
		r := row(t, classify(t, w, Options{}), "S-A")
		if r.Blocked == "" {
			t.Errorf("%s: row = %+v, want blocked", f, r)
		}
	}
}

func TestClassifyBusyBlocks(t *testing.T) {
	cases := map[string]func(t *testing.T, w *world){
		// The spinner line sits just above the box's opening rule (screen.go),
		// never inside the box: the box stays found, so the row blocks on busy,
		// not on "no input box". The fixture's first rule is the opening one.
		"esc to interrupt": func(t *testing.T, w *world) {
			s := strings.Replace(w.screens["S-A"], "\n─", "\n✻ Working… (esc to interrupt)\n─", 1)
			if _, found := claudesession.InputDraft(s); !found || s == w.screens["S-A"] {
				t.Fatal("the spinner line was not inserted above the box, or broke it")
			}
			w.screens["S-A"] = s
		},
		"status busy": func(_ *testing.T, w *world) { n := w.native[18]; n.Status = "busy"; w.native[18] = n },
		"status just moved": func(_ *testing.T, w *world) {
			n := w.native[18]
			n.StatusUpdatedAt = tNow.Add(-time.Second).UnixMilli()
			w.native[18] = n
		},
		"bash shell":                     func(_ *testing.T, w *world) { w.shells[18] = []int{99} },
		"background agent row on screen": func(t *testing.T, w *world) { w.screens["S-A"] = screenFile(t, "screen-bg-agent.txt") },
		"transcript fresh": func(_ *testing.T, w *world) {
			w.paths["sid-a"] = "/t/a.jsonl"
			w.mtimes["/t/a.jsonl"] = tNow.Add(-10 * time.Second)
		},
	}
	for name, mutate := range cases {
		w := newWorld()
		w.add(t, 18, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", cleanLaunch().Start, "")
		mutate(t, w)
		r := row(t, classify(t, w, Options{}), "S-A")
		if !strings.HasPrefix(r.Blocked, "busy") {
			t.Errorf("%s: row = %+v, want blocked busy", name, r)
		}
	}
}

// F13 (12:46): turn finished, nothing on screen, a subagent in the
// background — busy, nothing typed. Ended or older than the process: not.
func TestClassifyPendingBackgroundBlocks(t *testing.T) {
	start := cleanLaunch().Start
	at := func(d time.Duration) string { return start.Add(d).UTC().Format(time.RFC3339) }
	launch := `{"type":"user","timestamp":"` + at(time.Minute) + `","toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a1"}}`
	old := `{"type":"user","timestamp":"` + at(-time.Hour) + `","toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a0"}}`
	done := `{"type":"queue-operation","timestamp":"` + at(2*time.Minute) + `","content":"<task-notification>\n<task-id>a1</task-id>"}`
	cases := []struct {
		name  string
		lines []string
		busy  bool
	}{
		{"subagent running", []string{launch}, true},
		{"subagent ended", []string{launch, done}, false},
		{"launched before this process: dead", []string{old}, false},
	}
	for _, c := range cases {
		w := newWorld()
		w.add(t, 18, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", start, "")
		p := filepath.Join(t.TempDir(), "a.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(c.lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.paths["sid-a"] = p
		r := row(t, classify(t, w, Options{}), "S-A")
		if got := strings.HasPrefix(r.Blocked, "busy") && strings.Contains(r.Blocked, "background work"); got != c.busy {
			t.Errorf("%s: row = %+v, want busy on background work = %v", c.name, r, c.busy)
		}
	}
}

func TestClassifyCutTurnGetsContinue(t *testing.T) {
	w := newWorld()
	w.add(t, 19, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", cleanLaunch().Start.Add(10*time.Second), "")
	p := filepath.Join(t.TempDir(), "a.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-10-08T10:29:00Z","message":{"content":[{"type":"tool_result"}]}}`+"\n"), 0o644)
	w.paths["sid-a"] = p
	r := row(t, classify(t, w, Options{}), "S-A")
	if len(r.Actions) != 2 || r.Actions[0] != ActRelaunch || r.Actions[1] != ActContinue {
		t.Fatalf("actions = %v, want relaunch then continue", r.Actions)
	}
}

func TestClassifyOldCutIsNotThisRelaunch(t *testing.T) {
	w := newWorld()
	w.add(t, 20, "S-B", "/cfg/b", "sid-b", "claude", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	p := filepath.Join(t.TempDir(), "b.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"content":"vieux prompt"}}`+"\n"), 0o644)
	w.paths["sid-b"] = p
	if r := row(t, classify(t, w, Options{}), "S-B"); hasAction(r, ActContinue) {
		t.Fatalf("a turn cut three days ago is not this relaunch's: %+v", r)
	}
}

func TestClassifyNotResumed(t *testing.T) {
	w := newWorld()
	w.idleSurface["S-B"] = true
	r := row(t, classify(t, w, Options{}), "S-B")
	if len(r.Actions) != 1 || r.Actions[0] != ActStart || !strings.Contains(r.Line, "sid-b") {
		t.Fatalf("row = %+v", r)
	}
	w.idleSurface["S-B"] = false
	if r := row(t, classify(t, w, Options{}), "S-B"); r.Blocked == "" {
		t.Fatalf("pane not at its prompt must block: %+v", r)
	}
}

func TestClassifyAutoResumeFalseAndSelf(t *testing.T) {
	w := newWorld()
	w.add(t, 21, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", cleanLaunch().Start, "")
	rows := classify(t, w, Options{Self: "S-A"})
	if r := row(t, rows, "S-C"); len(r.Problems) != 0 || len(r.Info) == 0 {
		t.Fatalf("autoResume=false: %+v", r)
	}
	if r := row(t, rows, "S-A"); !r.Self || r.Blocked == "" {
		t.Fatalf("self must be blocked: %+v", r)
	}
}

func TestClassifySurfacesFilter(t *testing.T) {
	w := newWorld()
	rows := classify(t, w, Options{Surfaces: []string{"S-B"}})
	if len(rows) != 1 || rows[0].SurfaceID != "S-B" {
		t.Fatalf("rows = %+v", rows)
	}
}

// F10: argv without a mode, account defaultMode = the expected one → nothing lost.
func TestClassifyDefaultModeIsNotLost(t *testing.T) {
	before, now := photos(t)
	before = withPanel(before, "S-A", noModeInCommand)
	w := newWorld()
	w.defaults["/cfg/a"] = "auto"
	w.add(t, 70, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	r := row(t, classifyWith(t, w, before, now, Options{}), "S-A")
	if len(r.Problems) != 0 || len(r.Actions) != 0 {
		t.Fatalf("row = %+v, want nothing to do: auto comes from the account default", r)
	}
	w.defaults["/cfg/a"] = "" // the default no longer gives auto: lost
	if r := row(t, classifyWith(t, w, before, now, Options{}), "S-A"); !hasAction(r, ActRelaunch) {
		t.Fatalf("row = %+v, want a relaunch (mode lost)", r)
	}
}

// Same session, but resumed by cmux (frozen width): relaunched with the
// binding's own command, verbatim (F5) — the account default supplies the mode.
func TestClassifyDefaultModeResumedUsesBindingVerbatim(t *testing.T) {
	before, now := photos(t)
	before = withPanel(before, "S-A", noModeInCommand)
	a, _ := before.Panel("S-A")
	w := newWorld()
	w.defaults["/cfg/a"] = "auto"
	w.add(t, 71, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a", cleanLaunch().Start.Add(20*time.Second), "")
	r := row(t, classifyWith(t, w, before, now, Options{}), "S-A")
	if !hasAction(r, ActRelaunch) || strings.Contains(strings.Join(r.Problems, " "), "permission mode") {
		t.Fatalf("row = %+v, want a relaunch for the width only", r)
	}
	if !r.frozen {
		t.Fatal("frozen not set")
	}
	if !strings.HasSuffix(r.Line, "; "+GatePassedEnv+"=cmux-fix "+a.Binding.Command) {
		t.Fatalf("line = %q, want the binding command verbatim", r.Line)
	}
}

func TestClassifyDefaultModeUnreadableBlocks(t *testing.T) {
	before, now := photos(t)
	before = withPanel(before, "S-A", noModeInCommand)
	w := newWorld()
	w.badSettings["/cfg/a"] = true
	w.add(t, 72, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	if r := row(t, classifyWith(t, w, before, now, Options{}), "S-A"); r.Blocked == "" || !r.Remaining {
		t.Fatalf("row = %+v, want blocked: an unreadable default is never guessed", r)
	}
}

// F9: after a cmux relaunch three surfaces showed the same tty. Each
// session is classified on its own surface (process env); the photo's
// ttyName is never read. A pane shell with a hidden env is tied by the
// tty cmux tree gives (D22, simPanes) only when no other surface claims it.
func TestClassifySameTTYTwoSurfaces(t *testing.T) {
	before, _ := photos(t)
	now, err := LoadPhoto("testdata/session-now-same-tty.json") // every ttyName = ttys034
	if err != nil {
		t.Fatal(err)
	}
	w := newWorld()
	w.add(t, 60, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	w.add(t, 61, "S-E", "/cfg/a", "sid-e", "claude --resume sid-e --permission-mode acceptEdits", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	w.idleSurface["S-B"] = true // S-B: no claude, its own shell at the prompt
	rows := classifyWith(t, w, before, now, Options{})
	if r := row(t, rows, "S-A"); r.PID != 60 || len(r.Actions) != 0 {
		t.Fatalf("S-A = %+v, want pid 60, nothing to do", r)
	}
	if r := row(t, rows, "S-E"); r.PID != 61 || len(r.Actions) != 0 {
		t.Fatalf("S-E = %+v, want pid 61, nothing to do", r)
	}
	if r := row(t, rows, "S-B"); r.PID != 0 || len(r.Actions) != 1 || r.Actions[0] != ActStart || !strings.Contains(r.Line, "sid-b") {
		t.Fatalf("S-B = %+v, want a start of sid-b on S-B only", r)
	}
}

// T-g: the surface runs another conversation than the photo's checkpoint —
// blocked and signalled, nothing typed.
func TestClassifyOtherConversationBlocks(t *testing.T) {
	for _, sid := range []string{"sid-other", ""} { // another one, or unreadable
		w := newWorld()
		w.add(t, 73, "S-A", "/cfg/a", sid, "claude --permission-mode auto", cleanLaunch().Start.Add(20*time.Second), "")
		r := row(t, classify(t, w, Options{}), "S-A")
		if r.Blocked == "" || !r.Remaining || len(r.Actions) != 0 || r.Line != "" {
			t.Errorf("sid %q: row = %+v, want blocked, no action, no line", sid, r)
		}
	}
}

// D8 (decided 2026-10-08): a bypassPermissions binding is never relaunched
// nor started by the check, only signalled.
func TestClassifyBypassNeverRestored(t *testing.T) {
	before, now := photos(t)
	before = withPanel(before, "S-A", func(pn *Panel) { pn.AgentMode = claudesession.ModeBypass })
	w := newWorld()
	w.add(t, 74, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a", cleanLaunch().Start.Add(20*time.Second), "")
	r := row(t, classifyWith(t, w, before, now, Options{}), "S-A")
	if hasAction(r, ActRelaunch) || r.Line != "" || !strings.Contains(r.Blocked, "D8") || !strings.Contains(strings.Join(r.Info, " "), "bypassPermissions") {
		t.Fatalf("resumed: row = %+v, want signalled and blocked", r)
	}
	w = newWorld() // not resumed at all, shell at its prompt
	w.idleSurface["S-A"] = true
	r = row(t, classifyWith(t, w, before, now, Options{}), "S-A")
	if hasAction(r, ActStart) || r.Line != "" || !strings.Contains(r.Blocked, "D8") {
		t.Fatalf("not resumed: row = %+v, want never started", r)
	}
}

// T-h / F11: a claude stopped on the folder-trust dialog after the resume is
// blocked and remaining (exit 1); no action, no line — never answered. Claude
// writes its native file only after that dialog: the session id may be
// unknown, the row still says "dialog", not "conversation unknown".
func TestClassifyDialogBlocks(t *testing.T) {
	for _, sid := range []string{"sid-a", ""} {
		w := newWorld()
		w.add(t, 75, "S-A", "/cfg/a", sid, "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(20*time.Second), "")
		w.screens["S-A"] = screenFile(t, "screen-trust-dialog.txt")
		r := row(t, classify(t, w, Options{}), "S-A")
		if r.Blocked == "" || !r.Remaining || len(r.Actions) != 0 || r.Line != "" ||
			!strings.Contains(strings.Join(r.Problems, " "), "dialog") {
			t.Errorf("sid %q: row = %+v, want blocked on a dialog, remaining, nothing to type", sid, r)
		}
		// Round 6: the row says what to do once the dialog is answered.
		if !strings.Contains(r.Blocked, "answer it, then run `pigtail cmux check --fix`") {
			t.Errorf("sid %q: blocked %q, want the next step named", sid, r.Blocked)
		}
	}
}

// T-k / F15 (13:24): once the trust dialog is answered, the `cmux restore`
// line left in the input is cleared (then the resumed session relaunched);
// while the dialog shows, nothing is planned (TestClassifyDialogBlocks).
func TestClassifyRestoreLineAfterDialog(t *testing.T) {
	w := newWorld()
	w.add(t, 76, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(20*time.Second), "")
	w.screens["S-A"] = screenFile(t, "screen-trust-dialog.txt")
	if r := row(t, classify(t, w, Options{}), "S-A"); hasAction(r, ActClearRestore) || len(r.Actions) != 0 {
		t.Fatalf("dialog shown: row = %+v, want nothing planned", r)
	}
	w.screens["S-A"] = screenFile(t, "screen-restore-after-trust.txt") // answered by hand
	r := row(t, classify(t, w, Options{}), "S-A")
	if r.Blocked != "" || len(r.Actions) < 1 || r.Actions[0] != ActClearRestore ||
		strings.Contains(strings.Join(r.Problems, " "), "dialog") {
		t.Fatalf("dialog answered: row = %+v, want clear-restore-line first, not blocked", r)
	}
}

// T-j / F14 (13:24, measured): one conversation bound to two surfaces. cmux
// typed its restore in the second one; claude answered "already running in
// process N". That pane is a stale duplicate: never started, nothing typed,
// never reported "no live claude". D11 (decided 2026-10-08, A): alive
// elsewhere = info with the hint, not a defect; bound to several open panes
// and alive nowhere = a remaining defect.
func TestClassifyStaleDuplicate(t *testing.T) {
	before, now := photos(t)
	a, _ := before.Panel("S-A")
	dup := func(pn *Panel) { *pn.Binding = *a.Binding } // S-B bound to sid-a too
	before = withPanel(before, "S-B", dup)
	stale, err := os.ReadFile("testdata/screen-stale-duplicate.txt")
	if err != nil {
		t.Fatal(err)
	}
	infoRow := func(t *testing.T, r Row, want string) {
		t.Helper()
		i := strings.Join(r.Info, " ")
		if len(r.Actions) != 0 || r.Line != "" || r.Remaining || len(r.Problems) != 0 ||
			!strings.Contains(i, want) || !strings.Contains(i, "close or rebind this pane") {
			t.Fatalf("row = %+v, want info %q with the hint, no problem, no action, no line", r, want)
		}
	}
	defectRow := func(t *testing.T, r Row, want string) {
		t.Helper()
		p := strings.Join(r.Problems, " ")
		if len(r.Actions) != 0 || r.Line != "" || r.Blocked == "" || !r.Remaining ||
			!strings.Contains(p, want) || strings.Contains(p, "no live claude") {
			t.Fatalf("row = %+v, want %q, blocked, remaining, no action, no line", r, want)
		}
	}

	// (1) the conversation runs on S-A: S-B is the stale one (info), S-A untouched.
	w := newWorld()
	w.add(t, 80, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	w.idleSurface["S-B"] = true
	w.screens["S-B"] = string(stale)
	rows := classifyWith(t, w, before, now, Options{})
	infoRow(t, row(t, rows, "S-B"), "stale duplicate binding: conversation alive in S-A")
	if i := strings.Join(row(t, rows, "S-B").Info, " "); strings.Contains(i, "alpha") {
		t.Fatalf("info %q names the twin's title (É31, D23)", i)
	}
	if r := row(t, rows, "S-A"); r.PID != 80 || len(r.Actions) != 0 || r.Blocked != "" {
		t.Fatalf("S-A = %+v, want the live one left alone", r)
	}

	// É31: a twin with a full UUID is named by its 8 first characters only.
	const twin = "AAAAAAAA-0000-4000-8000-00000000000a"
	w = newWorld()
	w.add(t, 81, twin, "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	w.idleSurface["S-B"] = true
	w.screens["S-B"] = string(stale)
	rows = classifyWith(t, w, before, now, Options{})
	infoRow(t, row(t, rows, "S-B"), "stale duplicate binding: conversation alive in AAAAAAAA — close or rebind this pane")
	if i := strings.Join(row(t, rows, "S-B").Info, " "); strings.Contains(i, twin) {
		t.Fatalf("info %q shows the full surface id", i)
	}

	// (2) alive on no surface the check sees, claude said so on screen (info).
	w = newWorld()
	w.idleSurface["S-A"], w.idleSurface["S-B"] = true, true
	w.screens["S-B"] = string(stale)
	w.screens["S-A"] = "~/w/a % \n"
	// claude 48213 is listed, on no cmux surface (no env: Live drops it).
	w.procs = append(w.procs, claudesession.Proc{PID: 48213, Args: "claude --resume sid-a"})
	rows = classifyWith(t, w, before, now, Options{})
	infoRow(t, row(t, rows, "S-B"), "already running in process 48213")

	// (2) round 7: the line outlived process 48213 — no claude 48213 listed
	// now, so not alive elsewhere: T-j falls through to (3) (both panes
	// still bound, both blocked) or, S-A closed, to a start of S-B.
	w.procs = nil
	rows = classifyWith(t, w, before, now, Options{})
	for _, s := range []string{"S-A", "S-B"} {
		defectRow(t, row(t, rows, s), "duplicate binding")
	}
	rows = classifyWith(t, w, before, withoutPanel(now, "S-A"), Options{})
	if r := row(t, rows, "S-B"); !hasAction(r, ActStart) || r.Blocked != "" || !strings.Contains(r.Line, "sid-a") {
		t.Fatalf("process gone, S-A closed: S-B = %+v, want a start of sid-a", r)
	}

	// (3) duplicate binding on two open panes, alive nowhere, nothing on
	// screen: which surface to start it on would be a guess — both blocked,
	// a remaining defect (D11).
	w.screens["S-B"] = "~/w/a % \n"
	rows = classifyWith(t, w, before, now, Options{})
	for _, s := range []string{"S-A", "S-B"} {
		defectRow(t, row(t, rows, s), "duplicate binding")
	}

	// (3) round 6: S-B closed since — a closed duplicate is no duplicate,
	// S-A is started, not blocked.
	rows = classifyWith(t, w, before, withoutPanel(now, "S-B"), Options{})
	if r := row(t, rows, "S-A"); !hasAction(r, ActStart) || r.Blocked != "" || !strings.Contains(r.Line, "sid-a") {
		t.Fatalf("S-B closed: S-A = %+v, want a start of sid-a", r)
	}

	// A single binding, alive nowhere: still started (TestClassifyNotResumed).
	w = newWorld()
	w.idleSurface["S-B"] = true
	if r := row(t, classify(t, w, Options{}), "S-B"); !hasAction(r, ActStart) {
		t.Fatalf("single binding: row = %+v, want a start", r)
	}
}

// D10 (decided 2026-10-08, A): a bound surface closed since the relaunch is
// info, never a defect; ClosedOrphan only when its conversation runs on no
// surface the check sees (the report's header line counts those).
func TestClassifyClosedSurface(t *testing.T) {
	before, now := photos(t)
	now = withoutPanel(now, "S-B")
	w := newWorld()
	r := row(t, classifyWith(t, w, before, now, Options{}), "S-B")
	if r.Remaining || len(r.Problems) != 0 || len(r.Actions) != 0 || !r.ClosedOrphan ||
		!strings.Contains(strings.Join(r.Info, " "), "surface closed since the relaunch, conversation alive nowhere") {
		t.Fatalf("row = %+v, want info only, ClosedOrphan", r)
	}
	w.add(t, 82, "S-A", "/cfg/b", "sid-b", "claude --resume sid-b", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7") // sid-b runs on S-A
	r = row(t, classifyWith(t, w, before, now, Options{}), "S-B")
	if r.Remaining || r.ClosedOrphan || !strings.Contains(strings.Join(r.Info, " "), "conversation alive in S-A") {
		t.Fatalf("row = %+v, want info naming S-A, not an orphan", r)
	}
}

// Round 6: a session dead in a pane created after the last quit (absent from
// the photo before, its tab kept) is read from the current photo once the
// relaunch is no longer recent; while cmux may still be resuming (launch +
// 9 min here) it is not read. D12 (decided 2026-10-08, B): reported, never
// started — no action, no line, not a remaining defect; T-j held.
func TestClassifyCurrentPanelDeadSession(t *testing.T) {
	before, now := photos(t)
	nb := &Binding{AutoResume: true, CheckpointID: "sid-n", Cwd: "/w/n", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/cfg/b"}}
	now.Panels = append(append([]Panel(nil), now.Panels...), Panel{SurfaceID: "S-N", Title: "new", Binding: nb})
	late := func(w *world) Deps {
		d := w.deps()
		d.Now = func() time.Time { return cleanLaunch().Start.Add(time.Hour) }
		return d
	}
	run := func(d Deps) []Row {
		live, err := Live(d)
		if err != nil {
			t.Fatal(err)
		}
		return Classify(before, now, live, cleanLaunch(), Options{}, d)
	}
	w := newWorld()
	w.idleSurface["S-N"] = true
	r := row(t, run(late(w)), "S-N")
	if len(r.Actions) != 0 || r.Line != "" || len(r.Problems) != 0 || r.Remaining || r.Blocked != "" ||
		!strings.Contains(strings.Join(r.Info, " "), "not started: session dead in a surviving tab (run it by hand)") {
		t.Fatalf("late: row = %+v, want info \"not started\", no action, not remaining (D12)", r)
	}
	for _, r := range run(w.deps()) { // tNow = launch + 9 min: recent
		if r.SurfaceID == "S-N" {
			t.Fatalf("recent relaunch: row %+v, want S-N not read", r)
		}
	}
	nb.CheckpointID = "sid-a" // T-j there too: sid-a alive on S-A → info, never started
	w = newWorld()
	w.idleSurface["S-N"] = true
	w.add(t, 81, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	r = row(t, run(late(w)), "S-N")
	if hasAction(r, ActStart) || r.Line != "" || r.Remaining || !strings.Contains(strings.Join(r.Info, " "), "stale duplicate binding") {
		t.Fatalf("alive on S-A: row = %+v, want T-j info, never started", r)
	}
}

func actionNames(as []Action) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = string(a)
	}
	return out
}

// F17, D9 (decided 2026-10-08, A): background work the relaunch killed is a
// problem on the row, reported only — the planned actions are those of the
// same row without it.
func TestClassifyLostBackgroundReportedOnly(t *testing.T) {
	start := cleanLaunch().Start.Add(20 * time.Second) // resumed by cmux: a relaunch is planned
	plain := func(t *testing.T) *world {
		w := newWorld()
		w.add(t, 83, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", start, "")
		return w
	}
	base := row(t, classify(t, plain(t), Options{}), "S-A")
	if strings.Contains(strings.Join(base.Problems, " "), "background work lost") {
		t.Fatalf("base row = %+v, want no lost work", base)
	}
	prevRun := `{"type":"user","timestamp":"2026-10-08T10:10:00Z","toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"aprev"}}`
	ended := `{"type":"assistant","timestamp":"2026-10-08T10:11:00Z","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}}`
	cases := map[string]func(t *testing.T, w *world){
		"transcript: launched in the previous run, never ended": func(t *testing.T, w *world) {
			p := filepath.Join(t.TempDir(), "a.jsonl")
			if err := os.WriteFile(p, []byte(prevRun+"\n"+ended+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			w.paths["sid-a"] = p
		},
		"screen: claude's didn't-finish line (fixture)": func(t *testing.T, w *world) {
			w.screens["S-A"] = screenFile(t, "screen-bg-lost.txt")
		},
	}
	for name, mutate := range cases {
		w := plain(t)
		mutate(t, w)
		r := row(t, classify(t, w, Options{}), "S-A")
		if !strings.Contains(strings.Join(r.Problems, " "), "background work lost by the relaunch") || !r.Remaining {
			t.Errorf("%s: row = %+v, want the lost work reported", name, r)
		}
		if strings.Join(actionNames(r.Actions), ",") != strings.Join(actionNames(base.Actions), ",") {
			t.Errorf("%s: actions %v, want %v — reported only, no action (D9)", name, r.Actions, base.Actions)
		}
		if r.Blocked != base.Blocked {
			t.Errorf("%s: blocked %q, want %q — the lost line is no running work (D9, É11)", name, r.Blocked, base.Blocked)
		}
	}
}

func TestRelaunchLine(t *testing.T) {
	before, _ := photos(t)
	a, _ := before.Panel("S-A")
	b, _ := before.Panel("S-B")
	// binding command already carries the expected mode: used verbatim (F5)
	if got := RelaunchLine(*a.Binding, "auto", "", 7); !strings.HasSuffix(got, "; "+GatePassedEnv+"=cmux-fix "+a.Binding.Command) {
		t.Fatalf("S-A line = %q", got)
	}
	// command without a mode, but the account default is the expected one (F10): verbatim too
	if got := RelaunchLine(*b.Binding, "auto", "auto", 7); !strings.HasSuffix(got, "; "+GatePassedEnv+"=cmux-fix "+b.Binding.Command) {
		t.Fatalf("S-B line with account default auto = %q, want the binding command verbatim", got)
	}
	// mode to add: composed line, shim kept, mode added, quoted
	got := RelaunchLine(*b.Binding, "auto", "", 7)
	for _, want := range []string{" export CLAUDE_CONFIG_DIR=/cfg/b PIGTAIL_CMUX_RELAUNCH=7; ", "cd -- '/w/b' && ", `${CMUX_CLAUDE_WRAPPER_SHIM:-}`, "--resume 'sid-b' --permission-mode 'auto'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("S-B line = %q, missing %q", got, want)
		}
	}
	// a peers-rebuilt binding has no command: composed, no mode
	if got := RelaunchLine(Binding{CheckpointID: "p1", Cwd: "/w/p 1", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/c d"}}, "", "", 7); !strings.Contains(got, "CLAUDE_CONFIG_DIR='/c d'") || !strings.Contains(got, "cd -- '/w/p 1'") || strings.Contains(got, "--permission-mode") {
		t.Fatalf("peers line = %q", got)
	}
}

// The session gate's exemption is a prefix of the relaunch command
// alone: never in the export list, and in the composed `cd -- <cwd> && <claude>`
// form on the command after the &&, since a leading prefix would apply to cd only.
func TestRelaunchLineGatePassedIsPrefixNotExported(t *testing.T) {
	before, _ := photos(t)
	a, _ := before.Panel("S-A")
	b, _ := before.Panel("S-B")
	const prefix = "PIGTAIL_GATE_PASSED=cmux-fix "
	for name, c := range map[string]struct {
		line     string
		composed bool // the cd -- <cwd> && <claude> form
	}{
		"verbatim": {RelaunchLine(*a.Binding, "auto", "", 7), false},
		"composed": {RelaunchLine(*b.Binding, "auto", "", 7), true},
		"no cwd":   {RelaunchLine(Binding{CheckpointID: "p1"}, "auto", "", 7), false},
	} {
		export, cmd, ok := strings.Cut(c.line, "; ")
		if !ok || strings.Contains(export, "PIGTAIL_GATE_PASSED") {
			t.Fatalf("%s: the export list must not carry the gate variable: %q", name, c.line)
		}
		if strings.Count(c.line, "PIGTAIL_GATE_PASSED") != 1 {
			t.Fatalf("%s: want exactly one prefix: %q", name, c.line)
		}
		if c.composed {
			i := strings.Index(cmd, " && ")
			if i < 0 || !strings.HasPrefix(cmd[i+len(" && "):], prefix) || strings.HasPrefix(cmd, prefix) {
				t.Fatalf("%s: the prefix belongs on the command after &&, not on cd: %q", name, cmd)
			}
		} else if !strings.HasPrefix(cmd, prefix) {
			t.Fatalf("%s: the prefix must lead the relaunch command: %q", name, cmd)
		}
	}
}

// cutTurn writes a transcript whose last turn the relaunch cut, for sid.
func cutTurn(t *testing.T, w *world, sid string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), sid+".jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-10-08T10:29:00Z","message":{"content":[{"type":"tool_result"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.paths[sid] = p
}

// standbyOf answers InStandby from a fixed set of sessions, and counts the
// sessions asked.
func standbyOf(asked *[]string, err error, in ...string) func(string) (bool, error) {
	return func(sid string) (bool, error) {
		*asked = append(*asked, sid)
		if err != nil {
			return true, err
		}
		for _, s := range in {
			if s == sid {
				return true, nil
			}
		}
		return false, nil
	}
}

// D16: a turn cut on a session in Standby gets no continue — the reprise
// waits for the user; the reason is in Info. An unreadable marker counts as in
// Standby. Out of Standby the continue stays (control).
func TestClassifyStandbyCutTypesNoContinue(t *testing.T) {
	mk := func() *world {
		w := newWorld()
		w.add(t, 19, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", cleanLaunch().Start.Add(10*time.Second), "")
		cutTurn(t, w, "sid-a")
		return w
	}
	var asked []string
	r := row(t, classify(t, mk(), Options{InStandby: standbyOf(&asked, nil, "sid-a")}), "S-A")
	if hasAction(r, ActContinue) || !strings.Contains(strings.Join(r.Info, "; "), "standby (D16): continue not typed") {
		t.Fatalf("in Standby: row = %+v", r)
	}
	r = row(t, classify(t, mk(), Options{InStandby: standbyOf(&asked, nil)}), "S-A")
	if !hasAction(r, ActContinue) {
		t.Fatalf("out of Standby: row = %+v, want the continue", r)
	}
	r = row(t, classify(t, mk(), Options{InStandby: standbyOf(&asked, errors.New("permission denied"))}), "S-A")
	if hasAction(r, ActContinue) || !strings.Contains(strings.Join(r.Info, "; "), "standby marker unreadable: permission denied; continue not typed") {
		t.Fatalf("marker unreadable: row = %+v", r)
	}
}

// D24: the relaunch of a session in Standby stays — it types at the shell,
// never a prompt, and the relaunched session keeps its Standby.
func TestClassifyStandbyRelaunchKept(t *testing.T) {
	mk := func() *world {
		w := newWorld()
		w.add(t, 11, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(20*time.Second), "")
		return w
	}
	var asked []string
	out := row(t, classify(t, mk(), Options{}), "S-A")
	in := row(t, classify(t, mk(), Options{InStandby: standbyOf(&asked, nil, "sid-a")}), "S-A")
	if !hasAction(in, ActRelaunch) || in.Line == "" || in.Line != out.Line {
		t.Fatalf("in Standby: row = %+v, want the relaunch of %+v", in, out)
	}
}

// The Standby is read for every classified row that has a session (cut or
// not, the pilot included) — never for a row without one.
func TestClassifyStandbySetOnEveryRow(t *testing.T) {
	w := newWorld()
	w.add(t, 19, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", cleanLaunch().Start.Add(10*time.Second), "")
	cutTurn(t, w, "sid-a")                                                                            // cut
	w.add(t, 20, "S-B", "/cfg/b", "sid-b", "claude", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7") // idle, not cut
	before, now := photos(t)                                                                          // S-E: no claude, to start
	before = withPanel(before, "S-C", func(pn *Panel) { pn.Binding.CheckpointID = "" })               // no session
	var asked []string
	rows := classifyWith(t, w, before, now, Options{InStandby: standbyOf(&asked, nil, "sid-a", "sid-b", "sid-e")})
	for _, s := range []string{"S-A", "S-B", "S-E"} {
		if r := row(t, rows, s); !r.standby {
			t.Errorf("%s: standby not set: %+v", s, r)
		}
	}
	for _, sid := range asked {
		if sid == "" {
			t.Fatalf("InStandby asked for a row without session: %q", asked)
		}
	}
	if r := row(t, rows, "S-C"); r.standby {
		t.Fatalf("S-C (no session) set in Standby: %+v", r)
	}
}

// lateLaunchDeps: the world's deps with the clock an hour after the launch.
func lateLaunchDeps(w *world) Deps {
	d := w.deps()
	d.Now = func() time.Time { return cleanLaunch().Start.Add(time.Hour) }
	return d
}

func classifyLate(t *testing.T, w *world) []Row {
	before, now := photos(t)
	d := lateLaunchDeps(w)
	live, err := Live(d)
	if err != nil {
		t.Fatal(err)
	}
	return Classify(before, now, live, cleanLaunch(), Options{}, d)
}

func TestClassifyAccountChangedAfterResumeWindowIsInfo(t *testing.T) {
	w := newWorld()
	w.add(t, 90, "S-B", "/cfg/a", "sid-b", "claude", cleanLaunch().Start.Add(30*time.Minute), "")
	r := row(t, classifyLate(t, w), "S-B")
	if len(r.Problems) != 0 || len(r.Actions) != 0 || r.Line != "" || r.Blocked != "" || r.Remaining {
		t.Fatalf("row = %+v, want nothing to do", r)
	}
	want := "account changed since the relaunch: runs on /cfg/a, binding says /cfg/b (started after cmux's resume window: taken as wanted)"
	if !containsStr(r.Info, want) {
		t.Fatalf("info = %q, want %q", r.Info, want)
	}
}

func TestClassifyModeChangedAfterResumeWindowIsInfo(t *testing.T) {
	w := newWorld()
	w.add(t, 91, "S-E", "/cfg/a", "sid-e", "claude --resume sid-e", cleanLaunch().Start.Add(30*time.Minute), "")
	r := row(t, classifyLate(t, w), "S-E")
	if len(r.Problems) != 0 || len(r.Actions) != 0 || r.Remaining {
		t.Fatalf("row = %+v, want nothing to do", r)
	}
	want := `permission mode changed since the relaunch: runs with "", binding says acceptEdits (started after cmux's resume window: taken as wanted)`
	if !containsStr(r.Info, want) {
		t.Fatalf("info = %q, want %q", r.Info, want)
	}
}

func TestClassifyAccountSwitchResumeWindowBoundary(t *testing.T) {
	w := newWorld()
	w.add(t, 94, "S-B", "/cfg/a", "sid-b", "claude", cleanLaunch().Start.Add(ResumeWindow), "PIGTAIL_CMUX_RELAUNCH=7")
	r := row(t, classifyLate(t, w), "S-B")
	if len(r.Problems) != 0 || len(r.Actions) != 0 || len(r.Info) == 0 || !strings.Contains(strings.Join(r.Info, " "), "account changed since the relaunch") {
		t.Fatalf("at the edge: row = %+v, want the D1 info", r)
	}
	w = newWorld()
	w.add(t, 94, "S-B", "/cfg/a", "sid-b", "claude", cleanLaunch().Start.Add(ResumeWindow-time.Second), "PIGTAIL_CMUX_RELAUNCH=7")
	r = row(t, classifyLate(t, w), "S-B")
	if !strings.Contains(strings.Join(r.Problems, " "), "wrong account") || !hasAction(r, ActRelaunch) {
		t.Fatalf("one second before the edge: row = %+v", r)
	}
}

func TestClassifyAccountSwitchNoStartedAtKeepsRelaunch(t *testing.T) {
	w := newWorld()
	w.add(t, 92, "S-B", "/cfg/a", "sid-b", "claude", cleanLaunch().Start.Add(30*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	n := w.native[92]
	n.StartedAt = 0
	w.native[92] = n
	r := row(t, classifyLate(t, w), "S-B")
	if !strings.Contains(strings.Join(r.Problems, " "), "wrong account") || !hasAction(r, ActRelaunch) {
		t.Fatalf("row = %+v, want today's wrong account + relaunch", r)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func noTranscriptWhy(sid, cfg string) string {
	return "no transcript of " + sid + " under " + cfg + " (the binding's account): a relaunch would resume nothing — relaunch it by hand"
}

func TestClassifyRelaunchWithoutTranscriptBlocks(t *testing.T) {
	w := newWorld()
	w.add(t, 11, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(20*time.Second), "")
	w.noTranscript["/cfg/a|sid-a"] = true
	r := row(t, classify(t, w, Options{}), "S-A")
	if !strings.Contains(r.Blocked, noTranscriptWhy("sid-a", "/cfg/a")) || !r.Stuck() || busyBlocked(r.Blocked) {
		t.Fatalf("row = %+v, want the missing-transcript block", r)
	}
}

func TestClassifyWrongAccountWithoutBindingTranscriptBlocks(t *testing.T) {
	w := newWorld()
	w.add(t, 15, "S-B", "/cfg/a", "sid-b", "claude", tNow.Add(-time.Hour), "PIGTAIL_CMUX_RELAUNCH=7")
	w.noTranscript["/cfg/b|sid-b"] = true
	r := row(t, classify(t, w, Options{}), "S-B")
	if !strings.Contains(r.Blocked, noTranscriptWhy("sid-b", "/cfg/b")) || !r.Stuck() {
		t.Fatalf("row = %+v, want the missing-transcript block", r)
	}
}

func TestClassifyTranscriptLookupErrorBlocks(t *testing.T) {
	w := newWorld()
	w.add(t, 11, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(20*time.Second), "")
	w.transcriptErr = errors.New("bad pattern")
	r := row(t, classify(t, w, Options{}), "S-A")
	if !strings.Contains(r.Blocked, "transcript of sid-a under /cfg/a unreadable (bad pattern): never relaunched blind") {
		t.Fatalf("row = %+v, want the unreadable block", r)
	}
}

func TestClassifyNoTranscriptHealthyRowUntouched(t *testing.T) {
	w := newWorld()
	w.add(t, 12, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a --permission-mode auto", cleanLaunch().Start.Add(2*time.Minute), "PIGTAIL_CMUX_RELAUNCH=7")
	w.noTranscript["/cfg/a|sid-a"] = true
	r := row(t, classify(t, w, Options{}), "S-A")
	if r.Blocked != "" || len(r.Problems) != 0 {
		t.Fatalf("row = %+v, want a healthy row untouched", r)
	}
}

func TestClassifyStartWithoutTranscriptBlocks(t *testing.T) {
	w := newWorld()
	w.idleSurface["S-B"] = true
	w.noTranscript["/cfg/b|sid-b"] = true
	r := row(t, classify(t, w, Options{}), "S-B")
	if hasAction(r, ActStart) || r.Line != "" || !strings.Contains(r.Blocked, noTranscriptWhy("sid-b", "/cfg/b")) || !r.Stuck() || busyBlocked(r.Blocked) {
		t.Fatalf("row = %+v, want the missing-transcript block and no start line", r)
	}
}

// D1 (sub_x000000000000037): with cmux's resume off, a claude started without
// the marker inside the resume window was started by hand — never "resumed by
// cmux", never relaunched; an account or mode that differs is a change made on
// purpose.
func TestClassifyResumeOffHandStartNotFrozen(t *testing.T) {
	w := newWorld()
	w.defaults["/cfg/a"] = "auto"
	w.add(t, 71, "S-A", "/cfg/a", "sid-a", "claude --resume sid-a", cleanLaunch().Start.Add(20*time.Second), "")
	r := row(t, classify(t, w, Options{CmuxResumeOff: true}), "S-A")
	if hasAction(r, ActRelaunch) || r.frozen || strings.Contains(strings.Join(r.Problems, " "), "resumed by cmux") {
		t.Fatalf("row = %+v, want no frozen inference with cmux's resume off", r)
	}
	// Control: the same world with the resume on is still a frozen relaunch.
	if r := row(t, classify(t, w, Options{}), "S-A"); !r.frozen {
		t.Fatalf("control: row = %+v, want frozen with cmux's resume on", r)
	}
}

// D1: resume off, no claude anywhere, every pane at its shell — each
// autoResume binding gets its start line (non-regression of the path pigtail
// now relies on).
func TestClassifyResumeOffStartsEveryBinding(t *testing.T) {
	w := newWorld()
	w.idleSurface["S-A"], w.idleSurface["S-B"], w.idleSurface["S-E"] = true, true, true
	for _, r := range classify(t, w, Options{CmuxResumeOff: true}) {
		if strings.Contains(strings.Join(r.Info, " "), "autoResume=false") {
			continue
		}
		if !hasAction(r, ActStart) || !strings.Contains(r.Line, MarkerEnv+"=") {
			t.Fatalf("row %s = %+v, want a start line with the marker", r.SurfaceID, r)
		}
	}
}

// D1, task 1 = case A (cmux keeps autoResume:true when its resume is off): a
// binding cmux set to false on purpose stays "not resumed, by design", never
// started — with the setting on or off. Green on the base: it holds the door.
func TestClassifyResumeOffCaseAInfoOnly(t *testing.T) {
	for _, o := range []Options{{}, {CmuxResumeOff: true}} {
		w := newWorld()
		w.idleSurface["S-C"] = true
		r := row(t, classify(t, w, o), "S-C")
		if hasAction(r, ActStart) || !strings.Contains(strings.Join(r.Info, " "), "not resumed, by design") {
			t.Fatalf("opts %+v: S-C = %+v, want info only, never started", o, r)
		}
	}
}

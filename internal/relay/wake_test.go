package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const surf = "3A23D5DD-59EE-471D-9270-32F0FB06E099"

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

const liveHolder = 4242 // the only pid the fake PidAlive reports alive

func ready() Candidate {
	return Candidate{
		Native:    claudesession.Native{SessionID: "s1", Status: "idle", Kind: "interactive"},
		NativeOK:  true,
		Alive:     true,
		SurfaceID: surf,
		Now:       t0,
		PidAlive:  func(pid int) bool { return pid == liveHolder },
	}
}

func wait(typ, src string) []pigtail.SessionWait {
	return []pigtail.SessionWait{{Type: typ, Source: src, Since: t0.Add(-time.Minute)}}
}

// D38: a wait with its own end (until) or its holder (held).
func waitEnd(typ, src string, until time.Time, holder int) []pigtail.SessionWait {
	w := wait(typ, src)
	if !until.IsZero() {
		w[0].Until = &until
	}
	w[0].HolderPID = holder
	return w
}

type eligibleCase struct {
	name string
	edit func(*Candidate)
	want bool
}

func eligibleCases() []eligibleCase {
	return []eligibleCase{
		{"B1 idle, no watcher, no wait", func(*Candidate) {}, true},
		{"B7 shell = idle (D31)", func(c *Candidate) { c.Native.Status = "shell" }, true},
		{"kind empty = interactive (older claude)", func(c *Candidate) { c.Native.Kind = "" }, true},
		{"busy", func(c *Candidate) { c.Native.Status = "busy" }, false},
		{"waiting", func(c *Candidate) { c.Native.Status = "waiting" }, false},
		{"B5 non interactive", func(c *Candidate) { c.Native.Kind = "sdk-cli" }, false},
		{"native unreadable", func(c *Candidate) { c.NativeOK = false }, false},
		{"pid dead", func(c *Candidate) { c.Alive = false }, false},
		{"B8 watched", func(c *Candidate) { c.Watched = true }, false},
		{"B5 freeze", func(c *Candidate) { c.Frozen = true }, false},
		{"B6 no surface", func(c *Candidate) { c.SurfaceID = "" }, false},
		// CA11 (characterisation, green on the base): a SDK session has no cmux
		// surface and a non-interactive kind, so the idle wake never types into it.
		{"CA11 sdk peer", func(c *Candidate) { c.SurfaceID = ""; c.Native.Kind = "sdk-ts" }, false},
		{"B4 review", func(c *Candidate) { c.Waits = wait("review", pigtail.WaitSourceDeclared) }, false},
		{"permission", func(c *Candidate) { c.Waits = wait("permission", pigtail.WaitSourceDeclared) }, false},
		{"blocked", func(c *Candidate) { c.Waits = wait("blocked", pigtail.WaitSourceDeclared) }, false},
		{"question", func(c *Candidate) { c.Waits = wait("question", pigtail.WaitSourceDeclared) }, false},
		{"arbitrage", func(c *Candidate) { c.Waits = wait("arbitrage", pigtail.WaitSourceDeclared) }, false},
		{"done", func(c *Candidate) { c.Waits = wait("done", pigtail.WaitSourceDeclared) }, false},
		{"wrapped", func(c *Candidate) { c.Waits = wait("wrapped", pigtail.WaitSourceDeclared) }, false},
		{"D30 working", func(c *Candidate) { c.Waits = wait("working", pigtail.WaitSourceDeclared) }, false},
		{"D30 mac held", func(c *Candidate) { c.Waits = wait("mac", pigtail.WaitSourceHeld) }, false},
		{"B5 limit", func(c *Candidate) { c.Waits = wait("limit", pigtail.WaitSourceAuto) }, false},
		{"B5 login", func(c *Candidate) { c.Waits = wait("login", pigtail.WaitSourceAuto) }, false},
		{"B5 api_error", func(c *Candidate) { c.Waits = wait("api_error", pigtail.WaitSourceAuto) }, false},
		// D38: blocks only what the sidebar would still show.
		{"D38 review declared blocks", func(c *Candidate) { c.Waits = wait("review", pigtail.WaitSourceDeclared) }, false},
		{"D38 mac held, holder alive", func(c *Candidate) { c.Waits = waitEnd("mac", pigtail.WaitSourceHeld, time.Time{}, liveHolder) }, false},
		{"D38 mac held, holder dead", func(c *Candidate) { c.Waits = waitEnd("mac", pigtail.WaitSourceHeld, time.Time{}, 999) }, true},
		{"D38 mac held, until passed", func(c *Candidate) { c.Waits = waitEnd("mac", pigtail.WaitSourceHeld, t0.Add(-time.Second), liveHolder) }, true},
		{"D38 limit before its reset", func(c *Candidate) { c.Waits = waitEnd("limit", pigtail.WaitSourceAuto, t0.Add(time.Hour), 0) }, false},
		{"D38 limit after its reset", func(c *Candidate) { c.Waits = waitEnd("limit", pigtail.WaitSourceAuto, t0.Add(-time.Second), 0) }, true},
	}
}

func TestEligible(t *testing.T) {
	for _, tc := range eligibleCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := ready()
			tc.edit(&c)
			ok, reason := Eligible(c)
			if ok != tc.want {
				t.Fatalf("Eligible = %v (%s), want %v", ok, reason, tc.want)
			}
			if !ok && reason == "" {
				t.Fatal("a refusal must name its reason (daemon log)")
			}
		})
	}
}

func TestSurfaceVerified(t *testing.T) {
	ok := peers.CmuxSession{SessionID: "s1", SurfaceID: surf, PID: 7, ActiveForSurface: true, ActiveSurfaceSessionID: "s1"}
	for _, tc := range []struct {
		name string
		ls   []peers.CmuxSession
		want bool
	}{
		{"active on its surface, same pid", []peers.CmuxSession{ok}, true},
		{"surface id case differs", []peers.CmuxSession{func() peers.CmuxSession { c := ok; c.SurfaceID = "3a23d5dd-59ee-471d-9270-32f0fb06e099"; return c }()}, true},
		{"B6 another session active there", []peers.CmuxSession{func() peers.CmuxSession {
			c := ok
			c.ActiveForSurface = false
			c.ActiveSurfaceSessionID = "s2"
			return c
		}()}, false},
		{"other surface", []peers.CmuxSession{func() peers.CmuxSession { c := ok; c.SurfaceID = "AAAAAAAA-0000-4000-8000-000000000000"; return c }()}, false},
		{"pid moved", []peers.CmuxSession{func() peers.CmuxSession { c := ok; c.PID = 8; return c }()}, false},
		{"session unknown to cmux", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, reason := SurfaceVerified(tc.ls, "s1", surf, 7); got != tc.want {
				t.Fatalf("SurfaceVerified = %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}

func TestScreenAllows(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, tc := range []struct {
		name    string
		screen  string
		focused bool
		want    bool
	}{
		{"B1 empty input", read("screen-idle-empty.txt"), false, true},
		{"B7 background work shown, input empty", read("screen-bg-agent.txt"), false, true},
		{"B2 draft", read("screen-draft.txt"), false, false},
		{"B2 multi-line", read("screen-draft-multiline.txt"), false, false},
		{"B2 history recalled", read("screen-draft-history.txt"), false, false},
		{"B2 dialog / no input box", read("screen-no-input.txt"), false, false},
		{"B3 pane has focus", read("screen-idle-empty.txt"), true, false},
		// Verify round 2: a fresh session's dimmed suggestion is not a draft;
		// anything else after "❯ " still is (B2/B11 absolute).
		{"fresh placeholder (filepath)", withInput(read("screen-idle-empty.txt"), `❯ Try "how does <filepath> work?"`), false, true},
		{"fresh placeholder (util)", withInput(read("screen-idle-empty.txt"), `❯ Try "create a util logging.py that..."`), false, true},
		{"placeholder shape, focused", withInput(read("screen-idle-empty.txt"), `❯ Try "how does <filepath> work?"`), true, false},
		{"Try without closing quote", withInput(read("screen-idle-empty.txt"), `❯ Try "foo`), false, false},
		{"Try then more text", withInput(read("screen-idle-empty.txt"), `❯ Try "x" and more`), false, false},
		{"two-line draft starting with Try", withInput(read("screen-idle-empty.txt"), "❯ Try \"how does <filepath> work?\"\n  and then this"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, reason := ScreenAllows(tc.screen, tc.focused); got != tc.want {
				t.Fatalf("ScreenAllows = %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}

// A usage-limit wait looks idle (empty input, status idle, no typed wait) but
// the keystroke cancels Claude Code's auto-continue and it exits (verify
// round 3, 2026-10-21): never typed into, whatever the line of the wait.
func TestScreenAllowsRefusesUsageLimitWait(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-idle-empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	free := string(b)
	if ok, why := ScreenAllows(free, false); !ok {
		t.Fatalf("positive control: free prompt refused: %s", why)
	}
	for _, line := range []string{
		"  ⎿  You've hit your session limit · resets 11:40pm (Europe/Paris)",
		"     /usage-credits to finish what you’re working on.",
		"⏺ Usage limit reached · continuing automatically at 11:40pm · esc to cancel",
	} {
		// In the status area: just above the box, or in the footer below it.
		ls := strings.Split(free, "\n")
		for i, l := range ls {
			if strings.HasPrefix(l, "──────────") {
				ls = append(ls[:i], append([]string{line}, ls[i:]...)...)
				break
			}
		}
		above := strings.Join(ls, "\n")
		below := strings.TrimRight(free, "\n") + "\n" + line + "\n"
		for name, screen := range map[string]string{"above the box": above, "below the box": below} {
			if screen == free {
				t.Fatalf("%s: fixture not changed", name)
			}
			ok, why := ScreenAllows(screen, false)
			if ok || why != "usage limit wait (not idle)" {
				t.Errorf("%q %s: ScreenAllows = %v (%q), want refused as a usage limit wait", line, name, ok, why)
			}
		}
	}
}

// D10 follow-up: markers match whatever the case of the screen.
func TestScreenAllowsUsageMarkersCaseInsensitive(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-idle-empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	free := string(b)
	for _, line := range []string{
		"claude usage limit reached. your limit will reset at 3pm",
		"you've hit your limit",
	} {
		screen := strings.TrimRight(free, "\n") + "\n  " + line + "\n"
		if ok, why := ScreenAllows(screen, false); ok || why != "usage limit wait (not idle)" {
			t.Errorf("%q: ScreenAllows = %v (%q), want refused", line, ok, why)
		}
	}
}

// ScreenAllowsIgnoringUsageLimit (account rotation) keeps the box, draft and
// focus checks but not the usage-limit refusal.
func TestScreenAllowsIgnoringUsageLimit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-idle-empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	free := string(b)
	limited := strings.TrimRight(free, "\n") + "\n  Usage limit reached · continuing automatically at 3pm\n"
	if ok, _ := ScreenAllows(limited, false); ok {
		t.Fatal("positive control: ScreenAllows must refuse")
	}
	if ok, why := ScreenAllowsIgnoringUsageLimit(limited, false); !ok {
		t.Errorf("ignoring usage limit: refused (%s)", why)
	}
	if ok, _ := ScreenAllowsIgnoringUsageLimit(limited, true); ok {
		t.Error("focused pane must still refuse")
	}
	if ok, _ := ScreenAllowsIgnoringUsageLimit("no box here", false); ok {
		t.Error("no box must still refuse")
	}
}

// Review gap 3: a marker quoted in the conversation (scrollback above the
// box) is no usage-limit wait: it would hold message delivery, the Standby
// /compact and the idle pause forever.
func TestScreenAllowsIgnoresUsageMarkersInScrollback(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-idle-empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range usageLimitMarkers {
		quoted := "  la ligne « " + m + " » est citée dans la réponse précédente\n\n" + string(b)
		if ok, why := ScreenAllows(quoted, false); !ok {
			t.Errorf("marker %q quoted above the box: ScreenAllows = false (%s), want allowed", m, why)
		}
	}
	// No recognisable input box: the whole screen is searched, as before.
	if ok, _ := ScreenAllows("Usage limit reached · continuing automatically", false); ok {
		t.Error("no input box and a marker: allowed")
	}
}

// withInput replaces the empty input line of a fixture screen by line.
func withInput(screen, line string) string {
	lines := strings.Split(screen, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "❯" { // the fixture pads it with a no-break space
			lines[i] = line
			return strings.Join(lines, "\n")
		}
	}
	panic("fixture has no empty input line")
}

// D16: a session in Standby is never woken; Eligible alone still says yes
// (the compaction reads it, Tâche 4).
func TestWakeAllowedRefusesStandby(t *testing.T) {
	c := Candidate{NativeOK: true, Alive: true, SurfaceID: "S", Native: claudesession.Native{Status: "idle", Kind: "interactive"}}
	if ok, why := WakeAllowed(c); !ok || why != "" {
		t.Fatalf("not in Standby: %v %q", ok, why)
	}
	c.Standby = true
	if ok, why := WakeAllowed(c); ok || why != "standby (D16)" {
		t.Fatalf("in Standby: %v %q", ok, why)
	}
	if ok, _ := Eligible(c); !ok {
		t.Fatal("Eligible changed with Standby")
	}
	c.StandbyReason = "standby unreadable: x"
	if ok, why := WakeAllowed(c); ok || why != "standby unreadable: x" {
		t.Fatalf("unreadable: %v %q", ok, why)
	}
	c.Standby, c.Watched = false, true
	if ok, why := WakeAllowed(c); ok || why != "watched" {
		t.Fatalf("watched: %v %q", ok, why)
	}
}

// D-A (verify N2 of PR #210): the compaction of a session in Standby ignores
// its Stop watcher (held by the Standby, it delivers nothing) and nothing
// else: every other reason of Eligible refuses it as before; out of Standby,
// or with an unreadable Standby, it refuses; the wake keeps "watched".
func TestCompactAllowed(t *testing.T) {
	for _, tc := range eligibleCases() {
		if tc.name == "B8 watched" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			c := ready()
			c.Standby = true
			tc.edit(&c)
			ok, reason := CompactAllowed(c)
			if ok != tc.want {
				t.Fatalf("CompactAllowed = %v (%s), want %v", ok, reason, tc.want)
			}
			c.Watched = true
			if ok2, reason2 := CompactAllowed(c); ok2 != ok || reason2 != reason {
				t.Fatalf("a held watcher changed the verdict: %v %q, then %v %q", ok, reason, ok2, reason2)
			}
		})
	}
	c := ready()
	c.Standby, c.Watched = true, true
	if ok, why := CompactAllowed(c); !ok {
		t.Fatalf("watched in Standby: refused (%s)", why)
	}
	if ok, why := WakeAllowed(c); ok || why != "standby (D16)" {
		t.Fatalf("wake in Standby: %v %q", ok, why)
	}
	c.Standby = false
	if ok, why := CompactAllowed(c); ok || why != "standby ended" {
		t.Fatalf("out of Standby: %v %q", ok, why)
	}
	if ok, why := WakeAllowed(c); ok || why != "watched" {
		t.Fatalf("wake, watched out of Standby: %v %q", ok, why)
	}
	c.Standby, c.StandbyReason = true, "standby unreadable: x"
	if ok, why := CompactAllowed(c); ok || why != "standby unreadable: x" {
		t.Fatalf("unreadable: %v %q", ok, why)
	}
}

// D8: the status area is the last message block above the box (from the last
// "⏺" line) plus the footer. The real shape of a usage-limit screen has the
// marker on the block's first lines and other lines between it and the box.
func TestScreenAllowsUsageLimitBlockWithLinesBetween(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-idle-empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	free := string(b)
	i := strings.Index(free, "──────────")
	if i < 0 {
		t.Fatal("fixture has no box")
	}
	box := free[i:]
	for name, block := range map[string]string{
		"waiting line":                  "⏺ Usage limit reached · resets 3pm\n  ⎿  You've hit your limit · resets 3pm\n\n  ⎿  Waiting… continuing automatically at 3pm\n",
		"plain text":                    "⏺ Usage limit reached\n  ⎿  You've hit your limit\n\n  some plain text line\n",
		"marker only on the first line": "⏺ Usage limit reached · continuing automatically\n  ⎿  details\n  ⎿  more details\n\n",
	} {
		ok, why := ScreenAllows("  earlier history\n\n"+block+box, false)
		if ok || why != "usage limit wait (not idle)" {
			t.Errorf("%s: ScreenAllows = %v (%q), want refused as a usage limit wait", name, ok, why)
		}
	}
	// A marker in an OLDER block, then a newer block without marker: history.
	older := "⏺ Usage limit reached · continuing automatically\n  ⎿  You've hit your limit\n\n⏺ Reprise : tout est fini.\n  rien de spécial ici\n\n"
	if ok, why := ScreenAllows(older+box, false); !ok {
		t.Errorf("marker in an older block: ScreenAllows = false (%s), want allowed", why)
	}
}

// D10: a marker counts ANYWHERE in the status area (substring match). A false
// negative types into a usage-limit wait and ends the session; a false
// positive only holds the session until the user's next prompt. So a real
// "5-hour limit reached · continuing automatically" line (marker mid-line) is
// refused, and a reply quoting a marker is an ACCEPTED false positive.
func TestScreenAllowsUsageMarkerAnywhereInTheStatusArea(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "claudesession", "testdata", "screen-idle-empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	free := string(b)
	i := strings.Index(free, "──────────")
	if i < 0 {
		t.Fatal("fixture has no box")
	}
	box := free[i:]
	for name, block := range map[string]string{
		"real 5-hour line":      "⏺ Compte\n  ⎿  5-hour limit reached · continuing automatically at 3pm\n\n",
		"quoted mid-sentence":   "⏺ Le message « Usage limit reached » vient de l'ancienne session.\n\n",
		"quoted continuing":     "⏺ Il affichait continuing automatically puis plus rien.\n\n",
		"quoted hit your limit": "⏺ Tu disais que You've hit your session limit hier.\n\n",
		"quoted in a bullet":    "⏺ Résumé :\n  - la ligne Usage limit reached est un marqueur\n\n",
		"bullet glyph":          "⏺ Usage limit reached · resets 3pm\n\n",
		"result glyph":          "⏺ Compte\n  ⎿  You've hit your limit · resets 3pm\n\n",
		"curly apostrophe":      "⏺ Compte\n  ⎿  You’ve hit your session limit · resets 3pm\n\n",
		"credits":               "⏺ Compte\n     /usage-credits to finish what you’re working on.\n\n",
		"waiting":               "⏺ Compte\n  ⎿  continuing automatically at 3pm\n\n",
	} {
		if ok, why := ScreenAllows("  earlier history\n\n"+block+box, false); ok || why != "usage limit wait (not idle)" {
			t.Errorf("%s: ScreenAllows = %v (%q), want refused", name, ok, why)
		}
	}
	// Quoted marker in an OLDER block: still history, allowed.
	older := "⏺ Usage limit reached · continuing automatically\n\n⏺ Reprise : tout est fini.\n\n"
	if ok, why := ScreenAllows(older+box, false); !ok {
		t.Errorf("older block: ScreenAllows = false (%s), want allowed", why)
	}
	footer := strings.TrimRight(free, "\n") + "\n  Usage limit reached · continuing automatically at 3pm\n"
	if ok, why := ScreenAllows(footer, false); ok || why != "usage limit wait (not idle)" {
		t.Errorf("footer line: ScreenAllows = %v (%q), want refused", ok, why)
	}
}

// The idle pause (sub_x000000000000036, D11-D16): a readable Standby is
// accepted (D16, the inverse of the close), the shell status refused (D12).
func TestPauseAllowed(t *testing.T) {
	const threshold = time.Hour
	idleFor := func(d time.Duration) func(*Candidate) {
		return func(c *Candidate) { c.Native.StatusUpdatedAt = t0.Add(-d).UnixMilli() }
	}
	with := func(edits ...func(*Candidate)) func(*Candidate) {
		return func(c *Candidate) {
			idleFor(2 * time.Hour)(c)
			for _, e := range edits {
				e(c)
			}
		}
	}
	for _, tc := range []struct {
		name      string
		threshold time.Duration
		edit      func(*Candidate)
		want      bool
		reason    string
	}{
		{"idle 2h", threshold, with(), true, ""},
		{"off", 0, with(), false, "idle pause off"},
		{"standby readable", threshold, with(func(c *Candidate) { c.Standby = true }), true, ""},
		{"standby unreadable", threshold, with(func(c *Candidate) { c.Standby = true; c.StandbyReason = "standby unreadable: x" }), false, "standby unreadable: x"},
		{"parked", threshold, with(func(c *Candidate) { c.Native.ParkedJobID = "j" }), false, "parked as a background job"},
		{"shell", threshold, with(func(c *Candidate) { c.Native.Status = "shell" }), false, "background task running (status shell)"},
		{"watched", threshold, with(func(c *Candidate) { c.Watched = true }), true, ""},
		{"frozen", threshold, with(func(c *Candidate) { c.Frozen = true }), false, "frozen"},
		{"declared done, done command set", threshold, with(func(c *Candidate) { c.Waits = wait("done", pigtail.WaitSourceDeclared); c.DoneCommand = true }), false, "done: left to the done command"},
		{"declared done, no done command", threshold, with(func(c *Candidate) { c.Waits = wait("done", pigtail.WaitSourceDeclared) }), true, ""},
		{"declared wrapped", threshold, with(func(c *Candidate) { c.Waits = wait("wrapped", pigtail.WaitSourceDeclared) }), true, ""},
		{"declared review", threshold, with(func(c *Candidate) { c.Waits = wait("review", pigtail.WaitSourceDeclared) }), false, "wait review (declared)"},
		{"mac held, holder alive", threshold, with(func(c *Candidate) {
			c.Waits = waitEnd("mac", pigtail.WaitSourceHeld, time.Time{}, liveHolder)
		}), false, "wait mac (held)"},
		{"mac held, holder dead", threshold, with(func(c *Candidate) {
			c.Waits = waitEnd("mac", pigtail.WaitSourceHeld, time.Time{}, liveHolder+1)
		}), true, ""},
		{"idle 30m", threshold, idleFor(30 * time.Minute), false, "idle 30m0s < 1h0m0s"},
		{"no status time", threshold, func(c *Candidate) {}, false, "no status time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ready()
			tc.edit(&c)
			orig := append([]pigtail.SessionWait(nil), c.Waits...)
			ok, reason := PauseAllowed(c, tc.threshold)
			if ok != tc.want || reason != tc.reason {
				t.Fatalf("PauseAllowed = %v %q, want %v %q", ok, reason, tc.want, tc.reason)
			}
			if len(orig) != len(c.Waits) {
				t.Fatal("PauseAllowed must not alter the caller's waits")
			}
		})
	}
}

// The idle wrap: a declared done wait only, Standby never typed in (D16, D3),
// a Stop watcher holds it (the wrap waits for the watcher).
func TestWrapAllowed(t *testing.T) {
	const threshold = time.Hour
	done := func(c *Candidate) { c.Waits = wait("done", pigtail.WaitSourceDeclared) }
	with := func(edits ...func(*Candidate)) func(*Candidate) {
		return func(c *Candidate) {
			c.Native.StatusUpdatedAt = t0.Add(-2 * time.Hour).UnixMilli()
			done(c)
			for _, e := range edits {
				e(c)
			}
		}
	}
	for _, tc := range []struct {
		name      string
		threshold time.Duration
		edit      func(*Candidate)
		want      bool
		reason    string
	}{
		{"idle done", threshold, with(), true, ""},
		{"no done wait", threshold, with(func(c *Candidate) { c.Waits = nil }), false, "no declared done wait"},
		{"wrapped only", threshold, with(func(c *Candidate) { c.Waits = wait("wrapped", pigtail.WaitSourceDeclared) }), false, "no declared done wait"},
		{"standby readable", threshold, with(func(c *Candidate) { c.Standby = true }), false, "standby (D16)"},
		{"standby unreadable", threshold, with(func(c *Candidate) { c.Standby = true; c.StandbyReason = "standby unreadable: x" }), false, "standby unreadable: x"},
		{"parked", threshold, with(func(c *Candidate) { c.Native.ParkedJobID = "j" }), false, "parked as a background job"},
		{"shell", threshold, with(func(c *Candidate) { c.Native.Status = "shell" }), false, "background task running (status shell)"},
		{"question wait", threshold, with(func(c *Candidate) {
			c.Waits = append(wait("done", pigtail.WaitSourceDeclared), wait("question", pigtail.WaitSourceDeclared)...)
		}), false, "wait question (declared)"},
		{"idle below threshold", threshold, with(func(c *Candidate) { c.Native.StatusUpdatedAt = t0.Add(-30 * time.Minute).UnixMilli() }), false, "idle 30m0s < 1h0m0s"},
		{"threshold 0", 0, with(), false, "idle pause off"},
		{"watched", threshold, with(func(c *Candidate) { c.Watched = true }), false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ready()
			tc.edit(&c)
			orig := append([]pigtail.SessionWait(nil), c.Waits...)
			ok, reason := WrapAllowed(c, tc.threshold)
			if ok != tc.want || (tc.reason != "" && reason != tc.reason) || (!ok && reason == "") {
				t.Fatalf("WrapAllowed = %v %q, want %v %q", ok, reason, tc.want, tc.reason)
			}
			if len(orig) != len(c.Waits) {
				t.Fatal("WrapAllowed must not alter the caller's waits")
			}
		})
	}
}

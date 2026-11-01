package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sub_x000000000000036, task 10: `pigtail session resume <target>` types the
// resume of a paused session into its tab and verifies it like session move.

const (
	resumeSID     = "0b1c2d3e-0000-4000-8000-0000000000aa"
	resumeSurface = "AAAA0000-0000-4000-8000-0000000000C1"
)

func pausedPeer() pigtail.PeerSession {
	at := time.Now().Add(-time.Hour)
	return pigtail.PeerSession{
		SessionID: resumeSID, ConfigDir: "/cfg/mp", CellID: "mp", Scope: "perso", Cwd: "/wt/x",
		CmuxSurfaceID: resumeSurface, SubjectShortTitle: "Pause", Label: "Pause · 0b1c2d3e (mp)",
		EndedAt: &at, PausedAt: &at,
	}
}

type resumeFake struct {
	screen    string
	screenErr error
	ready     bool
	readyWhy  string
	enters    []string
	texts     []string
	confirm   func(relaunchCheck) (int, error)
	checks    []relaunchCheck
}

func stubResume(t *testing.T, f *resumeFake) {
	t.Helper()
	testutil.SerialOnly(t) // swaps resumeReadScreen
	saved := []any{resumeReadScreen, resumeSendEnter, resumeSendText, resumeShellReady, resumeConfirm, resumeBefore, resumeWrapper}
	t.Cleanup(func() {
		resumeReadScreen = saved[0].(func(string) (string, error))
		resumeSendEnter = saved[1].(func(string) error)
		resumeSendText = saved[2].(func(string, string) error)
		resumeShellReady = saved[3].(func(string) (bool, string))
		resumeConfirm = saved[4].(func(relaunchCheck) (int, error))
		resumeBefore = saved[5].(func() (map[int]bool, error))
		resumeWrapper = saved[6].(func() string)
	})
	resumeReadScreen = func(s string) (string, error) { return f.screen, f.screenErr }
	resumeSendEnter = func(s string) error { f.enters = append(f.enters, s); return nil }
	resumeSendText = func(s, text string) error { f.texts = append(f.texts, text); return nil }
	resumeShellReady = func(string) (bool, string) { return f.ready, f.readyWhy }
	resumeConfirm = func(c relaunchCheck) (int, error) {
		f.checks = append(f.checks, c)
		if f.confirm != nil {
			return f.confirm(c)
		}
		return 4242, nil
	}
	resumeBefore = func() (map[int]bool, error) { return map[int]bool{1: true}, nil }
	resumeWrapper = func() string { return "/opt/cmux/cmux-claude-wrapper" }
}

const (
	launcherScreen = "some output\n\n↻ Entrée = relancer (hb) · q = quitter\n"
	newSessScreen  = "some output\n↻ Entrée = nouvelle session (hb) · q = quitter\n"
	shellScreen    = "~/wt/x\n❯ \n"
)

func TestRouteResume(t *testing.T) {
	t.Parallel()
	ready := func() (bool, string) { return true, "" }
	notReady := func() (bool, string) { return false, "something else runs in it" }
	for _, c := range []struct {
		name   string
		screen string
		shell  func() (bool, string)
		want   resumeRoute
		errHas string
	}{
		{"launcher", launcherScreen, notReady, resumeLauncher, ""},
		{"launcher would start a new session", newSessScreen, ready, 0, "would start a new session"},
		{"shell at its prompt", shellScreen, ready, resumeShell, ""},
		{"Claude's box on screen", "╭────╮\n│ > hi │\n╰────╯\n", notReady, 0, "not at its prompt"},
		{"typed text", "❯ foo\n", notReady, 0, "not at its prompt"},
	} {
		got, _, err := routeResume(c.screen, c.shell)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.errHas)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: route %v err %v, want %v", c.name, got, err, c.want)
		}
	}
}

func runResumeT(t *testing.T, target string, ps []pigtail.PeerSession, caller string, cross bool) (string, string, error) {
	t.Helper()
	var out, warn bytes.Buffer
	err := runResume(&out, &warn, target, ps, caller, cross)
	return out.String(), warn.String(), err
}

func TestSessionResumeLauncherRoute(t *testing.T) {
	f := &resumeFake{screen: launcherScreen}
	stubResume(t, f)
	out, _, err := runResumeT(t, "Pause", []pigtail.PeerSession{pausedPeer()}, "perso", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.enters) != 1 || f.enters[0] != resumeSurface || len(f.texts) != 0 {
		t.Fatalf("enters %v texts %q, want one Enter and no text", f.enters, f.texts)
	}
	if len(f.checks) != 1 || f.checks[0].SessionID != resumeSID || f.checks[0].Surface != resumeSurface || f.checks[0].Dst.ConfigDir != "/cfg/mp" || !f.checks[0].Before[1] {
		t.Fatalf("confirm checks %+v", f.checks)
	}
	if !strings.Contains(out, "✓ Pause · 0b1c2d3e (mp) resumed (pid 4242) — still in Standby until your prompt") {
		t.Fatalf("out = %q", out)
	}
}

func TestSessionResumeShellRoute(t *testing.T) {
	f := &resumeFake{screen: shellScreen, ready: true}
	stubResume(t, f)
	if _, _, err := runResumeT(t, resumeSID, []pigtail.PeerSession{pausedPeer()}, "perso", false); err != nil {
		t.Fatal(err)
	}
	want := cmux.ResumeLine("/opt/cmux/cmux-claude-wrapper", "/cfg/mp", resumeSID)
	if !strings.HasSuffix(want, "--permission-mode auto") {
		t.Fatalf("ResumeLine %q lost --permission-mode auto", want)
	}
	if len(f.texts) != 2 || f.texts[0] != "\x15" || f.texts[1] != want {
		t.Fatalf("texts = %q, want [\\x15, %q]", f.texts, want)
	}
	if len(f.enters) != 1 {
		t.Fatalf("enters = %v, want one Enter after the line", f.enters)
	}
}

// The resume line with NIGHTLY-length paths is over the 200-rune cap of
// SendLineBounded: it is sent whole.
func TestSessionResumeShellRouteLongLine(t *testing.T) {
	f := &resumeFake{screen: shellScreen, ready: true}
	stubResume(t, f)
	resumeWrapper = func() string {
		return "/Applications/cmux NIGHTLY.app/Contents/Resources/bin/cmux-claude-wrapper-nightly-build-with-a-long-name"
	}
	p := pausedPeer()
	p.ConfigDir = "/Users/ada/.claude-team-cell-with-a-long-cell-directory-name"
	if _, _, err := runResumeT(t, resumeSID, []pigtail.PeerSession{p}, "perso", false); err != nil {
		t.Fatal(err)
	}
	want := cmux.ResumeLine(resumeWrapper(), p.ConfigDir, resumeSID)
	if n := len([]rune(want)); n < 203 {
		t.Fatalf("fixture line is %d runes, want ≥ 203", n)
	}
	if len(f.texts) != 2 || f.texts[1] != want {
		t.Fatalf("texts = %q, want the whole line", f.texts)
	}
}

// Review Focus 4: the launcher's ↻ was quit (q): the tab is a shell.
func TestSessionResumeAfterLauncherQuit(t *testing.T) {
	f := &resumeFake{screen: launcherScreen + "❯ \n", ready: true}
	stubResume(t, f)
	if _, _, err := runResumeT(t, resumeSID, []pigtail.PeerSession{pausedPeer()}, "perso", false); err != nil {
		t.Fatal(err)
	}
	if len(f.texts) != 2 {
		t.Fatalf("texts = %q, want the shell route", f.texts)
	}
}

// Review Focus 5: a failed confirmation shows the recovery line.
func TestSessionResumeShellRouteFailureShowsRecovery(t *testing.T) {
	f := &resumeFake{screen: shellScreen, ready: true,
		confirm: func(relaunchCheck) (int, error) { return 0, errRelaunchNotSeen }}
	stubResume(t, f)
	out, warn, err := runResumeT(t, resumeSID, []pigtail.PeerSession{pausedPeer()}, "perso", false)
	if !errors.Is(err, errRelaunchNotSeen) {
		t.Fatalf("err = %v", err)
	}
	if all := out + warn + err.Error(); !strings.Contains(all, "cd -- '/wt/x' && CLAUDE_CONFIG_DIR=") {
		t.Fatalf("no recovery line in %q", all)
	}
}

func TestSessionResumeRefusesBusyTab(t *testing.T) {
	for _, screen := range []string{newSessScreen, "❯ foo\n"} {
		f := &resumeFake{screen: screen, readyWhy: "no empty prompt line on screen"}
		stubResume(t, f)
		_, _, err := runResumeT(t, resumeSID, []pigtail.PeerSession{pausedPeer()}, "perso", false)
		if err == nil || len(f.enters)+len(f.texts) != 0 || len(f.checks) != 0 {
			t.Fatalf("%q: err %v, typed %v %q", screen, err, f.enters, f.texts)
		}
		if !strings.Contains(err.Error(), "c mp --resume "+resumeSID) {
			t.Fatalf("%q: no recovery line in %v", screen, err)
		}
	}
}

func TestSessionResumeNotPaused(t *testing.T) {
	f := &resumeFake{screen: launcherScreen}
	stubResume(t, f)
	live := pausedPeer()
	live.PausedAt, live.EndedAt, live.Status = nil, nil, "idle"
	_, _, err := runResumeT(t, "Pause", []pigtail.PeerSession{live}, "perso", false)
	if err == nil || err.Error() != "session Pause · 0b1c2d3e (mp) is not paused (idle) — nothing done" {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runResumeT(t, "nope", []pigtail.PeerSession{live}, "perso", false)
	if err == nil || err.Error() != `no paused session matches "nope"` {
		t.Fatalf("unknown: err = %v", err)
	}
	if len(f.enters)+len(f.texts) != 0 {
		t.Fatal("typed into a tab")
	}
}

// A non-master caller's scoped list hides another scope's session: the
// answer is the one of an unknown target.
func TestSessionResumeOtherScopeLooksUnknown(t *testing.T) {
	f := &resumeFake{screen: launcherScreen}
	stubResume(t, f)
	_, _, hidden := runResumeT(t, "Pause", nil, "globex", false)
	_, _, unknown := runResumeT(t, "Pause", []pigtail.PeerSession{}, "globex", false)
	if hidden == nil || unknown == nil || hidden.Error() != unknown.Error() || hidden.Error() != `no paused session matches "Pause"` {
		t.Fatalf("hidden %v, unknown %v", hidden, unknown)
	}
}

func TestSessionResumeCrossScopeNeedsOptIn(t *testing.T) {
	other := pausedPeer()
	other.Scope = "globex"
	f := &resumeFake{screen: launcherScreen}
	stubResume(t, f)
	_, _, err := runResumeT(t, "Pause", []pigtail.PeerSession{other}, "perso", false)
	if err == nil || err.Error() != "session Pause · 0b1c2d3e (mp) is of scope globex — --cross-scope to resume it" || len(f.enters) != 0 {
		t.Fatalf("err %v, enters %v", err, f.enters)
	}
	if _, _, err := runResumeT(t, "Pause", []pigtail.PeerSession{other}, "perso", true); err != nil || len(f.enters) != 1 {
		t.Fatalf("--cross-scope: err %v, enters %v", err, f.enters)
	}
	g := &resumeFake{screen: launcherScreen}
	stubResume(t, g)
	if _, _, err := runResumeT(t, "Pause", []pigtail.PeerSession{pausedPeer()}, "perso", false); err != nil || len(g.enters) != 1 {
		t.Fatalf("same scope: err %v, enters %v", err, g.enters)
	}
}

func TestSessionResumeTabGone(t *testing.T) {
	f := &resumeFake{screenErr: errors.New("surface not found")}
	stubResume(t, f)
	_, _, err := runResumeT(t, "Pause", []pigtail.PeerSession{pausedPeer()}, "perso", false)
	want := "the tab of Pause · 0b1c2d3e (mp) is gone or unreadable: surface not found — resume by hand: cd -- '/wt/x' && c mp --resume " + resumeSID
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

func TestPeerStatePaused(t *testing.T) {
	t.Parallel()
	p := pausedPeer()
	if got := peerState(p, true); got != "paused" {
		t.Fatalf("peerState = %q, want paused (before ended)", got)
	}
	p.PausedAt = nil
	if got := peerState(p, true); got != "ended" {
		t.Fatalf("peerState without pause = %q", got)
	}
}

// `session ls` shows a paused session's tab as paused: its peer is ended and
// no snapshot posts it, so the paused rows of GET /peers?all=1 join the map.
func TestMergePausedPeers(t *testing.T) {
	t.Parallel()
	live := pigtail.PeerSession{SessionID: "s-live", Status: "idle"}
	byID := map[string]pigtail.PeerSession{"s-live": live}
	ended := pigtail.PeerSession{SessionID: "s-ended", EndedAt: pausedPeer().EndedAt}
	mergePausedPeers(byID, []pigtail.PeerSession{live, pausedPeer(), ended, {SessionID: "s-live", PausedAt: pausedPeer().PausedAt}})
	if len(byID) != 2 || peerState(byID[resumeSID], true) != "paused" || byID["s-live"].PausedAt != nil {
		t.Fatalf("byID = %+v", byID)
	}
}

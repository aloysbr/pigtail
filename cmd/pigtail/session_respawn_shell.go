package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/relaunchfile"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
)

// Relaunch through the tab's shell (D1, sub_x000000000000022). A Claude the c
// launcher did not start — cmux resumed it after a restart — has no launcher
// waiting under it, but its tab's login zsh (which defined c from ~/.zshrc)
// gets its prompt back once Claude exits. The worker then types one ASCII line
// into it, ` source '<run>.relaunch.zsh'`, a 0600 file holding the guarded c
// call. The prompt and the directory stay in the file, never on the typed
// line: cmux mangles the non-ASCII bytes it types (session_spawn_line.go) and
// internal/cmux refuses a line over 200 runes.
const (
	relaunchLauncher = "launcher" // proto ≥ 2: the launcher's handoff files
	relaunchShell    = "shell"    // not started by c: a line typed into the tab's shell
)

func respawnScriptPath(id string) string { return filepath.Join(respawnDir(), id+".relaunch.zsh") }

// shellLine is what the worker types: the leading space keeps it out of the
// shell history (HIST_IGNORE_SPACE).
func shellLine(path string) string { return " source " + shQuote(path) }

// shellPathOK: the typed line must stay plain ASCII, quote-free and within
// cmux's 200 runes.
func shellPathOK(path string) error {
	for i := 0; i < len(path); i++ {
		if c := path[i]; c < 0x20 || c > 0x7e || c == '\'' {
			return fmt.Errorf("the relaunch file path %q is not plain ASCII — it cannot be typed into the tab", path)
		}
	}
	if n := utf8.RuneCountInString(shellLine(path)); n > 200 {
		return fmt.Errorf("the relaunch line would be %d characters (cmux types at most 200) — shorten $XDG_STATE_HOME", n)
	}
	return nil
}

var (
	rwShellAtPrompt = cmuxcheck.ShellAtPrompt // ps-based: the pane's login shell owns its terminal (D22)
	rwSendText      = cmux.SendText           // \x15 clears the shell's input line
	rwSendLine      = cmux.SendLineBounded    // the source line + Enter, no CMUX_* in its env
	rwShellTimeout  = 10 * time.Second
)

// shellRelaunchScript is the file the typed line sources. It runs c in a
// shell whose launcher honours C_CWD (C_LAUNCH_PROTO ≥ 1) and defines c;
// otherwise it runs claude itself (sessionlaunch.PickClaude) after a cd into
// the session's directory, on the fresh cell's config dir, with no gate
// prefix (only the launcher reads it). A failed cd says so on one line and
// launches nothing — the worker then sees no fresh Claude and records failed
// with the tab's last lines.
func shellRelaunchScript(r respawnRun, sid string) string {
	freshID, freshDir := r.fresh()
	model := ""
	if r.Model != "" {
		model = " --model " + shQuote(r.Model)
	}
	return fmt.Sprintf("# pigtail session respawn %s: typed into %s by its worker, removed by it\n"+
		"if (( ${C_LAUNCH_PROTO:-0} >= 1 )) && (( $+functions[c] )); then\n"+
		"  %s=respawn C_CWD=%s c %s --permission-mode %s%s %s\n"+
		"else\n"+
		"  %s\n"+
		"  if builtin cd -- %s 2>/dev/null; then\n"+
		"    CLAUDE_CONFIG_DIR=%s command \"$__pt_c\" --permission-mode %s%s %s\n"+
		"  else\n"+
		"    print -u2 -r -- %s\n"+
		"  fi\n"+
		"  unset __pt_c\n"+
		"fi\n",
		r.ID, r.SurfaceRef, sessionlaunch.GatePassedVar, shQuote(r.Cwd), freshID, shQuote(r.Mode), model, shQuote(r.Prompt),
		sessionlaunch.PickClaude, shQuote(r.Cwd), shQuote(freshDir), shQuote(r.Mode), model, shQuote(r.Prompt),
		shQuote("pigtail session respawn: cannot cd to "+r.Cwd+" - nothing launched; to resume the old session: "+resumeHint(r, sid, false)))
}

// resumeHint is the command that resumes the old session sid on its own
// cell: c's form when the session was started by the launcher, else
// claude under the old cell's config dir. The shell relaunch script and the
// worker's failure messages share it.
func resumeHint(r respawnRun, sid string, viaLauncher bool) string {
	if viaLauncher {
		return fmt.Sprintf("c %s --resume %s", r.Cell, sid)
	}
	return fmt.Sprintf("CLAUDE_CONFIG_DIR=%s claude --resume %s", r.ConfigDir, sid)
}

// writeShellScript writes the script (atomic, 0600) before the kill.
func (w *rwWorker) writeShellScript(sid string) error {
	if err := os.MkdirAll(respawnDir(), 0o700); err != nil {
		return fmt.Errorf("write relaunch script: %w", err)
	}
	if err := relaunchfile.WriteAtomic(respawnScriptPath(w.run.ID), []byte(shellRelaunchScript(w.run, sid))); err != nil {
		return fmt.Errorf("write relaunch script: %w", err)
	}
	return nil
}

// shellScreenReady is the screen half of the gate (the relay wake's shape:
// read the pane, fail closed, relay.ScreenAllows). ShellAtPrompt (ps) cannot
// tell a shell at its prompt from a shell function waiting for a key in the
// shell's own group (_c_run's read -k1, incident 2026-10-03): the screen can.
// Ready = no Claude input box (claudesession.InputDraft), and the last
// non-blank line is an empty prompt: it does not start with the launcher's
// ↻, and after the first glyph of shellPromptMarks it holds (emptyPromptLine,
// D9, go ahead):
//   - only blanks to the end of the line, or
//   - a run of at least 2 blanks, then a right-aligned segment (the right
//     prompt: powerlevel10k's « ✘ TERM 1m 45s » after a SIGTERM or a long
//     command, task 5 capture).
//
// Text typed after the glyph (« ❯ foo … », one blank then text) is refused.
// The first glyph is the left prompt's: a mark inside the right prompt (« > »)
// never makes a typed line read as empty; a mark in the path comes first and
// fails closed. Anything else is not ready — never a guess, never widened to
// « anything ». Blank = unicode.IsSpace: cmux read-screen renders the space
// after a prompt glyph as U+00A0 (TestShellScreenReady).
func shellScreenReady(screen string) (bool, string) {
	if _, found := claudesession.InputDraft(screen); found {
		return false, "Claude's input box is still on screen"
	}
	last := ""
	for _, l := range strings.Split(screen, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			last = t
		}
	}
	switch {
	case strings.HasPrefix(last, "↻"):
		return false, "the launcher waits for a key (↻ prompt)"
	case !emptyPromptLine(last):
		return false, "no empty prompt line on screen"
	}
	return true, ""
}

// shellPromptMarks: the glyphs that end a left prompt. Set from the screen
// captured in task 5 (its left prompt ends with ❯); never widened beyond
// prompt glyphs.
const shellPromptMarks = "❯%$#>"

// emptyPromptLine applies D9 to one trimmed line (see shellScreenReady).
func emptyPromptLine(line string) bool {
	i := strings.IndexAny(line, shellPromptMarks)
	if i < 0 {
		return false
	}
	_, n := utf8.DecodeRuneInString(line[i:])
	tail := line[i+n:]
	rest := strings.TrimLeftFunc(tail, unicode.IsSpace)
	if rest == "" {
		return true
	}
	return utf8.RuneCountInString(tail[:len(tail)-len(rest)]) >= 2
}

// shellReadyNow is one look at the run's tab (shellReadyOn).
func (w *rwWorker) shellReadyNow() (bool, string) { return shellReadyOn(w.run.SurfaceID) }

// shellReadyOn is one look at a tab: ps (ShellAtPrompt) and the screen
// (shellScreenReady), both in the same pass. why says what failed. Shared by
// the respawn worker and `session resume`.
func shellReadyOn(surfaceID string) (bool, string) {
	ok, err := rwShellAtPrompt(surfaceID)
	switch {
	case err != nil:
		return false, err.Error()
	case !ok:
		return false, "something else runs in it"
	}
	screen, err := rwReadScreen(surfaceID)
	if err != nil {
		return false, "screen unreadable: " + err.Error()
	}
	return shellScreenReady(screen)
}

// typeRelaunch waits for the tab's shell to be back at its prompt — ps and
// screen both — then clears its input line and types the source line.
// Nothing is typed otherwise. A failed source line says what may be in the
// tab (D2); the worker then records failed and looks for no fresh Claude (D1).
func (w *rwWorker) typeRelaunch() error {
	deadline := rwNow().Add(rwShellTimeout)
	for {
		ok, why := w.shellReadyNow()
		if ok {
			break
		}
		if rwNow().After(deadline) {
			return fmt.Errorf("the tab's shell is not at its prompt %s after Claude exited (%s) — nothing typed", rwShellTimeout, cmux.OneLine(why, 160))
		}
		time.Sleep(rwPoll)
	}
	if err := rwSendText(w.run.SurfaceID, "\x15"); err != nil {
		return fmt.Errorf("clear the shell's input line: %w — nothing typed", err)
	}
	if err := rwSendLine(w.run.SurfaceID, shellLine(respawnScriptPath(w.run.ID))); err != nil {
		w.logf("type the relaunch line: %v", err) // whole; the detail keeps relaunchErrRunes of it
		return fmt.Errorf("type the relaunch line: %s — %s", cmux.OneLine(err.Error(), relaunchErrRunes), relaunchTypedState(err))
	}
	return nil
}

// relaunchErrRunes bounds the cmux error in a failed typing's detail so that
// D2's warning stays whole in the 159 runes `--status` shows
// (cmux.OneLine(detail, 160)): 24 (« type the relaunch line: ») + 43 + 92
// (the warning) = 159. TestRespawnWorkerShellLineFailWarningFitsStatus.
const relaunchErrRunes = 43

// relaunchTypedState says what a failed SendLineBounded may have typed (D2):
// nothing when it refused the line before any cmux call (cmux.ErrInvalidArg);
// otherwise cmux ran and reports no count, so any part of the line may be in
// the tab.
func relaunchTypedState(err error) string {
	if errors.Is(err, cmux.ErrInvalidArg) {
		return "nothing typed"
	}
	return "it may be typed in part or in full (cmux gives no count); look at the tab before resuming"
}

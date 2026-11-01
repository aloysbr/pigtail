package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
)

// The launch contract lives in internal/sessionlaunch (shared with
// pigtaild's launcher); these names keep the CLI's call sites.
const (
	launcherProtoVar = sessionlaunch.LauncherProtoVar
	launcherCwdVar   = sessionlaunch.LauncherCwdVar
	launchTail       = sessionlaunch.LaunchTail
	envSpawnPrompt   = sessionlaunch.EnvSpawnPrompt
	envSpawnCwd      = sessionlaunch.EnvSpawnCwd
	maxPromptRunes   = 500
)

// validatePrompt keeps the initial prompt one plain line: it is the only text
// handed to the new session, and the launch line's only free-form input.
func validatePrompt(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return errors.New("--prompt is empty — pass the new session's first message, e.g. --prompt '/example-skill resume <id>'")
	case utf8.RuneCountInString(p) > maxPromptRunes:
		return fmt.Errorf("--prompt is longer than %d characters — keep it to one short line; put longer context in a file the prompt points to", maxPromptRunes)
	case strings.HasPrefix(p, "-"):
		return errors.New("--prompt starts with '-': claude would read it as a flag — rephrase it so it starts with another character")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("--prompt contains a control character (%U) — pass one plain line, without newlines or tabs", r)
		}
	}
	return nil
}

type permDecision struct {
	Mode    string // effective mode: the stricter of Launch and Current; always passed to c
	Launch  string // the caller's launch mode (argv, else the cell default, else Claude's default)
	Origin  string // where Launch comes from: caller argv | cell default | Claude default
	Current string // the mode the caller's hook recorded for this very spawn call
}

// resolvePermissionMode applies — inherit, never bypass — to both modes a
// session has: the one it was launched with (visible in ps) and the one it
// runs in now (only in hook payloads: shift-tab). The spawned session gets the
// stricter of the two, passed explicitly so a relaunch keeps it (launcher
// contract). Without the current mode, nothing is launched.
func resolvePermissionMode(callerArgs, cellDefault, current string) (permDecision, error) {
	d := permDecision{Launch: claudesession.PermissionMode(callerArgs), Origin: "caller argv", Current: current}
	if d.Launch == "" {
		d.Launch, d.Origin = cellDefault, "cell default"
	}
	if d.Launch == "" {
		d.Launch, d.Origin = "default", "Claude default"
	}
	if d.Launch == claudesession.ModeBypass {
		return permDecision{}, fmt.Errorf("the calling session was launched with bypassPermissions (%s) — a spawned session never runs bypassed", d.Origin)
	}
	if current == claudesession.ModeBypass {
		return permDecision{}, errors.New("the calling session runs in bypassPermissions now — a spawned session never runs bypassed; leave that mode first")
	}
	mode, ok := claudesession.Stricter(d.Launch, current)
	if !ok {
		return permDecision{}, fmt.Errorf("unknown permission mode (launch %q, current %q) — refusing rather than guessing", d.Launch, current)
	}
	d.Mode = mode
	return d, nil
}

// permissionStampMaxAge bounds how old the hook's record may be. It is written
// by the PreToolUse hook of the Bash call running this very command — before
// the permission dialog, so the window covers a human taking time to say yes.
const permissionStampMaxAge = 10 * time.Minute

// permissionStampDir mirrors the permission-stamp hook.
func permissionStampDir() string {
	if d := os.Getenv("PIGTAIL_PERMISSION_STAMP_DIR"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pigtail", "permission-mode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pigtail", "permission-mode")
}

// readPermissionStamp returns the current mode the session's permission-stamp
// hook recorded ("<mode> <unix seconds>"). Missing, stale or corrupt is an
// error naming the fix: the caller refuses, it never falls back. When the
// record is missing but other records were written in the last minute, the
// error says so with their count only — never a foreign session's id: the hook
// keys by the payload's session_id, pigtail by sessions/<pid>.json, and the
// two can diverge (after /clear, for instance), but another session may just
// be spawning too.
func readPermissionStamp(sessionID string, now time.Time) (string, error) {
	const fix = "re-run the command (relance la commande) as a plain `pigtail session spawn …` Bash call; if it keeps failing, install the permission-stamp hook"
	dir := permissionStampDir()
	p := filepath.Join(dir, sessionID)
	b, err := os.ReadFile(p)
	if err != nil {
		if n := len(freshStamps(dir, now, recentStampWindow)); n > 0 {
			return "", fmt.Errorf("no permission record for session %s (from sessions/<pid>.json); %d other record(s) written in the last %s — possibly this session under another id (after /clear?); %s", sessionID, n, recentStampWindow, fix)
		}
		return "", fmt.Errorf("no record of the session's current permission mode (%s) — %s", p, fix)
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return "", fmt.Errorf("unreadable permission record %s (%q) — %s", p, strings.TrimSpace(string(b)), fix)
	}
	at, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("unreadable permission record %s (%q) — %s", p, strings.TrimSpace(string(b)), fix)
	}
	if age := now.Sub(time.Unix(at, 0)); age > permissionStampMaxAge || age < -permissionStampMaxAge {
		return "", fmt.Errorf("the permission record %s is %s old (max %s): it is not this call's — %s", p, age.Round(time.Second), permissionStampMaxAge, fix)
	}
	return f[0], nil
}

// recentStampWindow is how recent another record must be to hint at a
// diverged session id: the hook ran for this very call, seconds ago.
const recentStampWindow = time.Minute

// consumePermissionStamp deletes the record once a real spawn has read it, so
// it cannot vouch for a later call that escaped the hook's pre-filter.
func consumePermissionStamp(sessionID string) error {
	err := os.Remove(filepath.Join(permissionStampDir(), sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// freshStamps lists the session ids whose record is younger than window.
func freshStamps(dir string, now time.Time, window time.Duration) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) <= window {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

// shQuote single-quotes s for a POSIX shell.
func shQuote(s string) string { return sessionlaunch.ShQuote(s) }

// launchEnv is the environment launchLine reads its free-form inputs from.
func launchEnv(cwd, prompt string) []string { return sessionlaunch.Env(cwd, prompt) }

// launchLine is the new terminal's initial command (sessionlaunch.Line).
func launchLine(cell, modeFlag, gate string) string { return sessionlaunch.Line(cell, modeFlag, gate) }

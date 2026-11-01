// Package sessionlaunch holds what `pigtail session spawn` and pigtaild's
// launcher share to start a Claude session in a new cmux tab: the launch line
// and its environment, the launch burst, the live-session read and the tab
// listing (D1 of the web launch subject: one line, one env, one burst).
package sessionlaunch

import (
	"fmt"
	"strings"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
)

// The launch contract with the optional `c` zsh launcher: sourcing it sets
// C_LAUNCH_PROTO=1, and `c` cds into $C_CWD (failing loudly on a missing dir)
// instead of its own home dir when it is set. A shell without it (no
// C_LAUNCH_PROTO, or below 1) runs claude itself, after a cd into the
// session's directory: the account comes from the tab's CLAUDE_CONFIG_DIR
// (SpawnEnv).
const (
	LauncherProtoVar = "C_LAUNCH_PROTO"
	LauncherCwdVar   = "C_CWD"
	LaunchTail       = "exec /bin/zsh -i"
	// EnvSpawnPrompt and EnvSpawnCwd carry the free-form inputs into the new
	// terminal. cmux types --command into it and mangles non-ASCII bytes
	// (é arrives as c3 83 c2 a9), while --env values arrive intact: the
	// command line stays pure ASCII and only references them.
	EnvSpawnPrompt = "PIGTAIL_SPAWN_PROMPT"
	EnvSpawnCwd    = "PIGTAIL_SPAWN_CWD"
)

// GatePassedVar names the exemption the launcher reads (plan « Contrat pour le lanceur »).
const GatePassedVar = "PIGTAIL_GATE_PASSED" //nolint:gosec // an env var name, not a credential

// PickClaude sets the zsh variable __pt_c to the claude a direct launch runs:
// cmux's claude wrapper, next to its bundled CLI, when this shell is in cmux
// and the wrapper is executable — it binds the session to its tab (resume,
// sidebar status, session ls/move) — else the claude on PATH. ASCII, one line.
const PickClaude = `__pt_c=claude; [[ -n ${CMUX_BUNDLED_CLI_PATH:-} && -x ${CMUX_BUNDLED_CLI_PATH:h}/cmux-claude-wrapper ]] && __pt_c=${CMUX_BUNDLED_CLI_PATH:h}/cmux-claude-wrapper`

// ShQuote single-quotes s for a POSIX shell.
func ShQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Env is the environment Line reads its free-form inputs from.
func Env(cwd, prompt string) []string {
	return []string{EnvSpawnCwd + "=" + cwd, EnvSpawnPrompt + "=" + prompt}
}

// SpawnEnv is the whole --env of a new session's tab: the cell's config dir,
// kept by cmux's auth-selection preservation, then Env.
func SpawnEnv(cell claudeprofile.Cell, cwd, prompt string) []string {
	return append([]string{
		"CLAUDE_CONFIG_DIR=" + cell.ConfigDir,
		"CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV=1",
		"CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV_KEYS=CLAUDE_CONFIG_DIR",
	}, Env(cwd, prompt)...)
}

// Line is the new terminal's initial command. It goes through an
// interactive zsh so ~/.zshrc defines `c`; it ends in an interactive shell so
// the tab never vanishes with an error on it. The prompt and the cwd come from
// Env, copied into locals and unset before c runs: left in the
// environment they would show in the new Claude's ps -E line, where a prompt
// containing "PWD=…" could be misread as its directory. cell is a cell id
// ([a-z]+) and modeFlag a known permission mode: the line is pure ASCII.
// gate is the PIGTAIL_GATE_PASSED reason (spawn | relay): a one-command prefix
// of the c call, never exported, so the launcher does not gate the tab again.
// An empty gate emits no prefix at all (a daemon launch is a human one, Q1).
// Without the launcher, the line cds into the directory and runs claude
// (PickClaude) with the mode and the prompt, no gate prefix (only the
// launcher reads it); a failed cd prints one line and launches nothing.
func Line(cell, modeFlag, gate string) string {
	mode := ""
	if modeFlag != "" {
		mode = " --permission-mode " + ShQuote(modeFlag)
	}
	prefix := ""
	if gate != "" {
		prefix = GatePassedVar + "=" + gate + " "
	}
	direct := fmt.Sprintf(`%s; if builtin cd -- "$__pt_d" 2>/dev/null; then command "$__pt_c"%s "$__pt_p"; else print -u2 -r -- "pigtail session spawn: cannot cd to $__pt_d - nothing was launched"; fi`,
		PickClaude, mode)
	inner := fmt.Sprintf(`__pt_p="$%s"; __pt_d="$%s"; unset %s %s; if (( ${%s:-0} >= 1 )); then %s%s="$__pt_d" c %s%s "$__pt_p"; else %s; fi; %s`,
		EnvSpawnPrompt, EnvSpawnCwd, EnvSpawnPrompt, EnvSpawnCwd,
		LauncherProtoVar, prefix, LauncherCwdVar, cell, mode, direct, LaunchTail)
	return "/bin/zsh -i -c " + ShQuote(inner)
}

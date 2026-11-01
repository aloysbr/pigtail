// Package cmux integrates pigtail with the cmux terminal multiplexer.
//
// When pigtail runs inside a cmux session, a daemonized dev server is invisible
// to cmux: the process is reparented outside any pane's tty tree, so cmux never
// attributes its listening port to the workspace and the branch/PR/port badges
// stay dark. Launching the server in the foreground of a dedicated cmux
// "sidecar" surface instead lets cmux discover the process and light those
// badges up. This package wraps the small slice of the cmux CLI needed to spawn
// such a sidecar.
package cmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// EnvWorkspaceID is the environment variable cmux exports with the current
// workspace id. Its presence is how we detect that we run inside cmux.
const EnvWorkspaceID = "CMUX_WORKSPACE_ID"

// EnvBundledCLI, when set, points at the cmux CLI shipped inside the cmux app
// bundle. It takes precedence over a `cmux` binary found on PATH.
const EnvBundledCLI = "CMUX_BUNDLED_CLI_PATH"

// surfaceRefRe extracts the `surface:N` reference cmux prints when creating a
// surface, e.g. `OK surface:33 pane:15 workspace:15`.
var surfaceRefRe = regexp.MustCompile(`surface:\d+`)

// InCmux reports whether pigtail is running inside a cmux session.
func InCmux() bool {
	return os.Getenv(EnvWorkspaceID) != ""
}

// WorkspaceID returns the current cmux workspace id (empty when not in cmux).
func WorkspaceID() string {
	return os.Getenv(EnvWorkspaceID)
}

// bundledCLIFallback is the CLI shipped inside the cmux app. A variable so
// tests can point it elsewhere.
var bundledCLIFallback = "/Applications/cmux.app/Contents/Resources/bin/cmux"

// cliPath resolves the cmux CLI: the bundled path wins, else a `cmux` on
// PATH, else the CLI inside the installed app bundle. The last one is what
// pigtaild finds under launchd, where neither the cmux variable nor a cmux
// PATH entry exists (the socket is found without them, spec C4).
//
// CMUX_BUNDLED_CLI_PATH set but empty means "no cmux here" and also disables
// the bundle fallback: hermetic tests rely on it.
func cliPath() (string, error) {
	p, set := os.LookupEnv(EnvBundledCLI)
	if p != "" {
		return p, nil
	}
	lp, err := exec.LookPath("cmux")
	if err == nil {
		return lp, nil
	}
	if !set && isExecutableFile(bundledCLIFallback) {
		return bundledCLIFallback, nil
	}
	return "", fmt.Errorf("cmux CLI not found (set %s or add cmux to PATH): %w", EnvBundledCLI, err)
}

func isExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// parseSurfaceRef extracts the `surface:N` reference from cmux new-surface
// output. It returns an error when no reference is present so callers can fall
// back instead of sending to a bogus surface.
func parseSurfaceRef(out string) (string, error) {
	ref := surfaceRefRe.FindString(out)
	if ref == "" {
		return "", fmt.Errorf("no surface ref in cmux output: %q", strings.TrimSpace(out))
	}
	return ref, nil
}

// newSurfaceArgs builds the argument list for `cmux new-surface`. The surface
// is created unfocused (--focus false): ports still propagate to the workspace
// badges without stealing the user's view.
func newSurfaceArgs(workspaceID, wtDir string) []string {
	return []string{
		"new-surface",
		"--workspace", workspaceID,
		"--working-directory", wtDir,
		"--focus", "false",
	}
}

// sendArgs builds the argument list for `cmux send` to a surface.
//
// The payload is prefixed with an explicit `cd <wtDir>` because the sidecar
// shell sources the user's rc and may not land in the worktree on its own. The
// trailing `\n` is a literal backslash-n, which cmux's `send` interprets as
// Enter (a real newline would not submit the line).
func sendArgs(surfaceRef, wtDir, wrappedCmd string) []string {
	payload := fmt.Sprintf("cd %s && %s\\n", shellQuote(wtDir), wrappedCmd)
	return []string{"send", "--surface", surfaceRef, payload}
}

// shellQuote single-quotes a string for safe interpolation into the shell
// command line typed into the sidecar (worktree paths may contain spaces).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SpawnForegroundSidecar creates an unfocused cmux sidecar surface anchored on
// wtDir and runs wrappedCmd inside it in the foreground. Because the server
// inherits the sidecar's tty, cmux discovers it and surfaces the workspace's
// branch/PR/port badges.
//
// Any error (missing CLI, new-surface failure, send failure) is returned so the
// caller can fall back to the historical background daemon.
func SpawnForegroundSidecar(workspaceID, wtDir, wrappedCmd string) error {
	cli, err := cliPath()
	if err != nil {
		return err
	}
	out, err := exec.Command(cli, newSurfaceArgs(workspaceID, wtDir)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cmux new-surface: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	ref, err := parseSurfaceRef(string(out))
	if err != nil {
		return err
	}
	if out, err := exec.Command(cli, sendArgs(ref, wtDir, wrappedCmd)...).CombinedOutput(); err != nil {
		return fmt.Errorf("cmux send to %s: %w (output: %s)", ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnvSurfaceID is the environment variable cmux exports with the current
// surface id. It is how a command running inside a pane identifies itself.
const EnvSurfaceID = "CMUX_SURFACE_ID"

// uuidRe extracts a Claude Code session id from the --resume argument in a
// resume binding. It anchors on --resume to avoid confusing a UUID-shaped
// worktree path segment with the actual session id.
var uuidRe = regexp.MustCompile(`--resume.*?'\\''([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})'\\''`)

// cdRe extracts the working directory from the `cd -- '<dir>'` prefix cmux
// puts in front of every resume binding.
var cdRe = regexp.MustCompile(`^cd -- '([^']*)'`)

// surfaceLineRe matches the surface refs printed by `cmux tree`.
var surfaceLineRe = regexp.MustCompile(`surface (surface:\d+)`)

// callerRefRe is the shape of a short surface ref, as `cmux tree` prints it.
var callerRefRe = regexp.MustCompile(`^surface:\d+$`)

// parseCallerSurfaceRef extracts caller.surface_ref from `cmux identify`
// output. A caller cmux could not place (null ref, no caller block, output
// that is not the expected JSON) is an error, never an empty ref: an empty
// ref matches no pane, which to a caller looks exactly like "not running in
// a pane" — and for `session move --all` that means selecting itself.
func parseCallerSurfaceRef(out []byte) (string, error) {
	var id struct {
		Caller *struct {
			SurfaceRef *string `json:"surface_ref"`
		} `json:"caller"`
	}
	if err := json.Unmarshal(out, &id); err != nil {
		return "", fmt.Errorf("cmux identify: unexpected output: %q", strings.TrimSpace(string(out)))
	}
	if id.Caller == nil || id.Caller.SurfaceRef == nil {
		return "", errors.New("cmux identify: cmux does not know the calling surface")
	}
	ref := *id.Caller.SurfaceRef
	if !callerRefRe.MatchString(ref) {
		return "", fmt.Errorf("cmux identify: unexpected caller surface ref %q", ref)
	}
	return ref, nil
}

// CurrentSurfaceRef returns the short ref (`surface:N`) of the surface the
// caller runs in, or "" outside cmux.
//
// $CMUX_SURFACE_ID alone is not enough: it holds the surface's UUID, while
// every ref pigtail compares against (SurfaceRefs, resume bindings) is the
// short `surface:N` form, so comparing the two never matches. `cmux identify`
// resolves the caller — from that same variable — onto its short ref.
func CurrentSurfaceRef() (string, error) {
	if os.Getenv(EnvSurfaceID) == "" {
		return "", nil
	}
	cli, err := cliPath()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(cli, "identify").Output()
	if err != nil {
		return "", fmt.Errorf("cmux identify: %w", err)
	}
	return parseCallerSurfaceRef(out)
}

// SessionIDFrom extracts the Claude session id from a resume binding. Empty
// when the surface has no binding, or one that is not a Claude session.
func SessionIDFrom(binding string) string {
	m := uuidRe.FindStringSubmatch(binding)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// CwdFrom extracts the working directory a resume binding restores into.
func CwdFrom(binding string) string {
	m := cdRe.FindStringSubmatch(strings.TrimSpace(binding))
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// parseSurfaceRefs pulls every `surface:N` ref out of `cmux tree` output, in
// order. The tty column of that output is deliberately ignored: it keeps the
// value a surface had when created, and tty numbers are recycled across a cmux
// restart, so it points at the wrong pane.
func parseSurfaceRefs(tree string) []string {
	var out []string
	for _, m := range surfaceLineRe.FindAllStringSubmatch(tree, -1) {
		out = append(out, m[1])
	}
	return out
}

// SurfaceRefs lists every surface known to cmux, across all workspaces.
func SurfaceRefs() ([]string, error) {
	cli, err := cliPath()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(cli, "tree", "--all").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("cmux tree: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return parseSurfaceRefs(string(out)), nil
}

// ResumeBinding returns the restart command stored for a surface. ("", nil)
// means the surface genuinely has no binding. An error means cmux could not
// answer the question: the CLI may not exist, the surface ref may be unknown,
// or the CLI version may be too old.
func ResumeBinding(ref string) (string, error) {
	cli, err := cliPath()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(cli, "surface", "resume", "show", "--surface", ref).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		// cmux could not answer: not the same thing as "this surface has no binding".
		return "", fmt.Errorf("cmux surface resume show %s: %w (output: %s)", ref, err, s)
	}
	if strings.Contains(s, "No resume binding") {
		return "", nil // the surface genuinely has no binding
	}
	return s, nil
}

// resumeSetArgs builds `cmux surface resume set` for a Claude session pinned to
// an explicit config dir. Without this rewrite, cmux would restore the pane on
// the account the session left — where the transcript no longer is.
//
// With a wrapper path, the binding launches cmux-claude-wrapper directly, so a
// restored pane does not depend on its claude shim (see shim.go); with an
// empty one it falls back to a plain `claude`. This is an argv, not a shell
// line: the path goes in as is. mode is the --permission-mode the restored
// session runs in.
func resumeSetArgs(ref, cwd, sessionID, configDir, wrapper, mode string) []string {
	launcher := "claude"
	if wrapper != "" {
		launcher = wrapper
	}
	return []string{
		"surface", "resume", "set",
		"--surface", ref,
		"--cwd", cwd,
		"--kind", "agent",
		"--checkpoint", sessionID,
		"--",
		"/usr/bin/env", "CLAUDE_CONFIG_DIR=" + configDir,
		launcher, "--resume", sessionID, "--permission-mode", mode,
	}
}

// SetResumeBinding pins a surface's restart command to the given config dir,
// launched through wrapper (cmux-claude-wrapper) when non-empty, in auto mode
// (session move).
func SetResumeBinding(ref, cwd, sessionID, configDir, wrapper string) error {
	return SetResumeBindingMode(ref, cwd, sessionID, configDir, wrapper, "auto")
}

// SetResumeBindingMode is SetResumeBinding with an explicit permission mode:
// a respawned session is restored in the mode it was relaunched with.
func SetResumeBindingMode(ref, cwd, sessionID, configDir, wrapper, mode string) error {
	cli, err := cliPath()
	if err != nil {
		return err
	}
	args := resumeSetArgs(ref, cwd, sessionID, configDir, wrapper, mode)
	if out, err := exec.Command(cli, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("cmux surface resume set: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sendLineArgs builds `cmux send` for one line of input. The trailing `\n` is a
// literal backslash-n, which cmux interprets as Enter.
func sendLineArgs(ref, line string) []string {
	return []string{"send", "--surface", ref, line + `\n`}
}

// SendLine types one line into a surface and presses Enter.
func SendLine(ref, line string) error {
	cli, err := cliPath()
	if err != nil {
		return err
	}
	if out, err := exec.Command(cli, sendLineArgs(ref, line)...).CombinedOutput(); err != nil {
		return fmt.Errorf("cmux send to %s: %w (output: %s)", ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sendKeyArgs builds `cmux send-key` for one key event. The `--` keeps a key
// name from ever being parsed as a flag.
func sendKeyArgs(ref, key string) []string {
	return []string{"send-key", "--surface", ref, "--", key}
}

// SendKey sends one key event (e.g. "Escape") to a surface.
func SendKey(ref, key string) error {
	cli, err := cliPath()
	if err != nil {
		return err
	}
	if out, err := exec.Command(cli, sendKeyArgs(ref, key)...).CombinedOutput(); err != nil {
		return fmt.Errorf("cmux send-key %s to %s: %w (output: %s)", key, ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// readScreenArgs builds `cmux read-screen` for the visible viewport only: no
// --scrollback, no --lines, so what it returns is what is on screen now.
func readScreenArgs(ref string) []string {
	return []string{"read-screen", "--surface", ref}
}

// ReadScreen returns the text currently visible in a surface.
func ReadScreen(ref string) (string, error) {
	out, err := runListing(readScreenArgs(ref)...) // bounded: a mute cmux never holds the reader
	if err != nil {
		return "", fmt.Errorf("%w (surface %s)", err, ref)
	}
	return string(out), nil
}

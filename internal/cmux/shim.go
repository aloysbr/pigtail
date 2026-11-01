package cmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
)

// How cmux hooks Claude Code into a pane
//
// cmux puts a per-surface directory, $TMPDIR/cmux-cli-shims/<SURFACE_ID>/,
// first on the pane's PATH. It normally holds a `claude` shim whose only job is
// to exec cmux-claude-wrapper, which in turn launches the real claude with
// cmux's --settings (the hooks that register the session, set its resume
// binding, and drive notifications).
//
// Some panes end up with that directory present but empty. `claude` typed
// there falls through PATH to the bare binary: the session runs, but cmux
// never hears of it — no resume binding, so `pigtail session ls/move` cannot
// see it either. Invoking the wrapper by its absolute path sidesteps the shim
// entirely, which is why pigtail relaunches through it.

// ClaudeWrapperName is the wrapper cmux ships next to its CLI.
const ClaudeWrapperName = "cmux-claude-wrapper"

// ManagedSettingsMarker appears in the --settings path cmux-claude-wrapper
// passes to claude (…/cmux-claude-settings.XXXXXX). A claude process whose
// argv lacks it was not launched through the wrapper.
const ManagedSettingsMarker = "cmux-claude-settings"

// EnvClaudeWrapperShimRoot is the environment variable cmux exports with the
// surface's shim directory.
const EnvClaudeWrapperShimRoot = "CMUX_CLAUDE_WRAPPER_SHIM_ROOT"

// shimRootsDir is the directory, under $TMPDIR, holding one shim root per
// surface.
const shimRootsDir = "cmux-cli-shims"

// ShimUsable reports whether a surface's shim root holds a claude shim that
// can actually run: a regular, non-empty, executable file. Anything else —
// no directory, an empty directory, a zero-byte or non-executable file —
// means `claude` in that pane resolves past the shim to the bare binary.
func ShimUsable(root string) bool {
	if root == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(root, "claude"))
	if err != nil {
		return false
	}
	return fi.Mode().IsRegular() && fi.Size() > 0 && fi.Mode().Perm()&0o111 != 0
}

// ClaudeWrapper resolves cmux-claude-wrapper next to the cmux CLI — the same
// place cmux's own shim looks for it. It errors when the CLI cannot be found,
// or when no executable wrapper sits beside it (a cmux too old to ship one).
func ClaudeWrapper() (string, error) {
	cli, err := cliPath()
	if err != nil {
		return "", err
	}
	p := filepath.Join(filepath.Dir(cli), ClaudeWrapperName)
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("%s not found next to the cmux CLI: %w", ClaudeWrapperName, err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", p)
	}
	return p, nil
}

// Surface is the cmux surface a process runs in, read from its environment.
type Surface struct {
	ID       string // CMUX_SURFACE_ID; empty when the process is not in cmux
	ShimRoot string // that surface's claude shim directory
}

// Both regexes accept a space or a NUL before and after: `ps -E` renders the
// environment space-separated after argv, /proc/<pid>/environ NUL-separated.
var (
	envSurfaceRe  = regexp.MustCompile(`(?:^|[\s\x00])` + EnvSurfaceID + `=([0-9A-Fa-f-]{36})(?:[\s\x00]|$)`)
	envShimRootRe = regexp.MustCompile(`(?:^|[\s\x00])` + EnvClaudeWrapperShimRoot + `=([^\s\x00]+)`)
)

// SurfaceFromEnviron extracts a process's cmux surface from its rendered
// environment. The last occurrence of each variable wins: the environment
// follows argv, and argv is free text that may contain anything.
//
// When the surface is known but the shim root is not exported (an older cmux),
// the root is derived from cmux's layout under $TMPDIR.
func SurfaceFromEnviron(environ string) Surface {
	var s Surface
	if m := envSurfaceRe.FindAllStringSubmatch(environ, -1); len(m) > 0 {
		s.ID = m[len(m)-1][1]
	}
	if s.ID == "" {
		return Surface{}
	}
	if m := envShimRootRe.FindAllStringSubmatch(environ, -1); len(m) > 0 {
		s.ShimRoot = m[len(m)-1][1]
	} else {
		s.ShimRoot = filepath.Join(os.TempDir(), shimRootsDir, s.ID)
	}
	return s
}

// procEnviron renders a process's environment: /proc/<pid>/environ on Linux,
// `ps -E` (argv followed by the environment) elsewhere. Injected so tests
// never depend on real processes.
var procEnviron = func(pid int) (string, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
		return string(b), err
	}
	out, err := exec.Command("ps", "-Eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return string(out), err
}

// ProcessEnviron renders pid's environment the way procEnviron does. It is
// exported for callers that need one variable of a process they did not
// start (session move reads how a Claude process was launched), and stays a
// thin wrapper so tests keep substituting procEnviron.
func ProcessEnviron(pid int) (string, error) {
	return procEnviron(pid)
}

// EnvValue returns the value of name in a rendered environment. The last
// occurrence wins, for the same reason as in SurfaceFromEnviron: on macOS
// the rendering starts with argv, which is free text. A value ends at the
// first whitespace or NUL — paths with spaces are not supported (the
// space-splitting of `ps -E` is a known limit).
func EnvValue(environ, name string) (string, bool) {
	re := regexp.MustCompile(`(?:^|[\s\x00])` + regexp.QuoteMeta(name) + `=([^\s\x00]*)`)
	m := re.FindAllStringSubmatch(environ, -1)
	if len(m) == 0 {
		return "", false
	}
	return m[len(m)-1][1], true
}

// ProcessSurface returns the cmux surface a process runs in. A zero Surface
// with a nil error means the process is not in cmux; an error means its
// environment could not be read (the process is gone, or not ours).
func ProcessSurface(pid int) (Surface, error) {
	env, err := procEnviron(pid)
	if err != nil {
		return Surface{}, fmt.Errorf("read environment of pid %d: %w", pid, err)
	}
	return SurfaceFromEnviron(env), nil
}

// ResumeLine is the line typed into a pane to resume sessionID on the account
// whose config dir is configDir. With a wrapper path it launches through
// cmux-claude-wrapper directly, so the relaunch does not depend on the pane's
// claude shim; with an empty one it falls back to a plain `claude`.
func ResumeLine(wrapper, configDir, sessionID string) string {
	launcher := "claude"
	if wrapper != "" {
		launcher = shellQuote(wrapper)
	}
	return fmt.Sprintf("CLAUDE_CONFIG_DIR=%s %s --resume %s --permission-mode auto", shellQuote(configDir), launcher, sessionID)
}

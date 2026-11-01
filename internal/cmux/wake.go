package cmux

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// Calls of the daemon's idle wake (lot 2 of the sidebar subject):
// send/read-screen/identify bounded by relayCallTimeout (3 s), the sweep's
// listing by listingCallTimeout (10 s) through SessionsJSONListing, the
// send-time listing by relayCallTimeout. Each one is aimed at a surface
// by UUID and runs with no CMUX_* variable in its environment (but the aimed
// socket, -cmux-socket, envWithoutCmux) — cmux reads
// CMUX_SURFACE_ID before a target in some commands (journal correction of
// cmux notify), and pigtaild must never type where a variable points.

// EnvSocketPath is the variable through which the cmux CLI reaches a given
// cmux instance's socket. pigtaild sets it only when -cmux-socket aims it at
// one instance (a second cmux, NIGHTLY, for a live pass).
const EnvSocketPath = "CMUX_SOCKET_PATH"

// aimedSocket is the socket every cmux call is aimed at; "" = not aimed (the
// CLI's default socket, the main cmux).
var aimedSocket atomic.Pointer[string]

// SetSocketPath aims every cmux call of the process at socket p ("" = none).
func SetSocketPath(p string) { aimedSocket.Store(&p) }

// SocketPath is the socket set by SetSocketPath, "" when not aimed.
func SocketPath() string {
	if p := aimedSocket.Load(); p != nil {
		return *p
	}
	return ""
}

// envWithoutCmux is the process env minus every CMUX_* variable, plus
// CMUX_SOCKET_PATH=<SocketPath()> when -cmux-socket aims the daemon: the one
// CMUX_* variable a wake call may carry, and only with the aimed value.
func envWithoutCmux() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CMUX_") {
			out = append(out, kv)
		}
	}
	if p := SocketPath(); p != "" {
		out = append(out, EnvSocketPath+"="+p)
	}
	return out
}

func runWake(args ...string) ([]byte, error) {
	return execTimeout(relayCallTimeout, envWithoutCmux(), args...)
}

// SendLineBounded types one line into a surface (UUID), then presses Enter in
// a second call: a long text sent with its newline in one burst is read by
// Claude Code as a paste and never submitted (measured 2026-10-21); the \r escape is cmux's Enter.
func SendLineBounded(surfaceID, line string) error {
	if err := checkUUID("surface id", surfaceID); err != nil {
		return err
	}
	if err := CheckLine(line); err != nil {
		return err
	}
	if _, err := runWake("send", "--surface", surfaceID, "--", line); err != nil {
		return err
	}
	if _, err := runWake("send", "--surface", surfaceID, "--", `\r`); err != nil {
		return fmt.Errorf("enter after the line: %w", err)
	}
	return nil
}

// SendEnterBounded presses Enter alone in a surface (UUID): `pigtail session
// resume` answers the launcher's ↻ prompt, which reads one key (a typed
// line would lose its first character). Same bound and environment as
// SendLineBounded.
func SendEnterBounded(surfaceID string) error {
	if err := checkUUID("surface id", surfaceID); err != nil {
		return err
	}
	_, err := runWake("send", "--surface", surfaceID, "--", `\r`)
	return err
}

// CheckLine is the validation SendLineBounded runs on a line: one non-empty
// line, at most 200 runes, no control character, not taken for a flag.
func CheckLine(line string) error { return checkText("line", line) }

// ReadScreenBounded returns the visible viewport of a surface (UUID).
func ReadScreenBounded(surfaceID string) (string, error) {
	if err := checkUUID("surface id", surfaceID); err != nil {
		return "", err
	}
	out, err := runWake("read-screen", "--surface", surfaceID)
	return string(out), err
}

// FocusedSurfaceID returns the UUID (upper case) of the surface holding cmux's
// keyboard focus, "" when cmux reports none.
func FocusedSurfaceID() (string, error) {
	out, err := runWake("identify", "--no-caller", "--id-format", "both")
	if err != nil {
		return "", err
	}
	return parseFocusedSurfaceID(out)
}

// SessionsJSONListingBounded is the sessions listing made at send time:
// bounded by relayCallTimeout (3 s), not listingCallTimeout, because it runs
// inside POST /messages, whose CLI context is 10 s (msgTimeout+5s).
func SessionsJSONListingBounded() ([]byte, error) { return runWake(sessionsListArgs()...) }

func parseFocusedSurfaceID(out []byte) (string, error) {
	var id struct {
		Focused *struct {
			SurfaceID string `json:"surface_id"`
		} `json:"focused"`
	}
	if err := json.Unmarshal(out, &id); err != nil {
		return "", fmt.Errorf("cmux identify: unexpected output: %q", OneLine(string(out), stderrMaxRunes))
	}
	if id.Focused == nil {
		return "", nil
	}
	return strings.ToUpper(strings.TrimSpace(id.Focused.SurfaceID)), nil
}

// PingBounded is Ping through the wake calls: bounded at 3 s, no CMUX_*
// variable (but the aimed socket) — the daemon's input probe skips its tick
// when cmux does not answer.
func PingBounded() error {
	out, err := runWake("ping")
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "PONG") {
		return fmt.Errorf("cmux ping: no PONG (output: %s)", OneLine(string(out), stderrMaxRunes))
	}
	return nil
}

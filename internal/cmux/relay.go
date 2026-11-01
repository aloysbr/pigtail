package cmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Calls used by the session-message relay. All bounded in time: they run on
// a hook's or a `pigtail msg send`'s critical path, and a wedged cmux must
// cost seconds, not the session.

// relayCallTimeout bounds every call. A variable only so tests driving a
// stand-in script under a loaded machine (make check, -race) can widen it.
var relayCallTimeout = 3 * time.Second

// SetCallTimeoutForTest widens relayCallTimeout and returns the function that
// restores it: for tests of other packages that drive a stand-in cmux script
// under a loaded machine (make check, -race), where spawning a shell can
// exceed the production bound.
func SetCallTimeoutForTest(d time.Duration) (restore func()) {
	old := relayCallTimeout
	relayCallTimeout = d
	return func() { relayCallTimeout = old }
}

// listingCallTimeout bounds the sidebar loop's read-only listings
// (list-windows, workspace list, tree, sessions list): under machine load
// they outlast the 3 s of a write, and a listing killed at 3 s used to pause
// every pill (D1·B, 2026-10-04). Writes and the hook-path calls keep
// relayCallTimeout.
var listingCallTimeout = 10 * time.Second

// SetListingTimeoutForTest sets listingCallTimeout and returns its restore.
func SetListingTimeoutForTest(d time.Duration) (restore func()) {
	old := listingCallTimeout
	listingCallTimeout = d
	return func() { listingCallTimeout = old }
}

func runListing(args ...string) ([]byte, error) { return runTimeout(listingCallTimeout, args...) }

// InboxStatusKey is the sidebar status key the relay sets on a recipient's
// workspace ("2 messages") and clears once the messages are claimed.
const InboxStatusKey = "pigtail-inbox"

func run(args ...string) ([]byte, error) { return runTimeout(relayCallTimeout, args...) }

// runTimeout is run with an explicit bound: creating a workspace legitimately
// takes longer than the relay's 3 s.
func runTimeout(d time.Duration, args ...string) ([]byte, error) {
	return execTimeout(d, nil, args...)
}

// execTimeout is the one door every bounded cmux call goes through (run,
// runListing, the folders' and the wake's runners). A variable only so a test
// can record the bound each call is handed, in memory, without a process
// (TestEachCallTakesItsBound).
var execTimeout = runTimeoutEnv

// runTimeoutEnv is runTimeout with an explicit environment (nil = inherit).
// Inherited, the process env already carries CMUX_SOCKET_PATH when pigtaild's
// -cmux-socket aims it (aimCmuxSocket sets it after stripCmuxEnv): the sidebar
// calls reach the aimed instance with no change here.
func runTimeoutEnv(d time.Duration, env []string, args ...string) ([]byte, error) {
	cli, err := cliPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, args...) //nolint:gosec // cli is the cmux binary; args are built here
	cmd.Env = env
	// A cmux killed at its bound may leave a child holding stdout open:
	// WaitDelay closes the pipes a second later instead of waiting for it.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	// cmux exited 0 but a child kept stdout open past WaitDelay: the answer is
	// complete, the call succeeded. A kill by the bound never reaches here
	// (ctx.Err() is set, the exit code is not 0).
	if errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil &&
		cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
		return out, nil
	}
	if err != nil {
		return out, runError(args[0], err)
	}
	return out, nil
}

// stderrMaxRunes bounds the cmux explanation carried by an error: enough for
// cmux's one-line "Error: …" plus its usage hint, not a screenful.
const stderrMaxRunes = 240

// runError names the failed command and, when cmux explained itself on
// stderr, appends that explanation on one line, truncated. Output() captures
// stderr into the *exec.ExitError for us. The wrapped error stays reachable
// with errors.As.
func runError(cmd string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := OneLine(string(ee.Stderr), stderrMaxRunes); msg != "" {
			if strings.Contains(msg, "Error: not_found") || strings.Contains(msg, "Tab not found") {
				return fmt.Errorf("cmux %s: %w: %s: %w", cmd, err, msg, ErrWorkspaceNotFound)
			}
			return fmt.Errorf("cmux %s: %w: %s", cmd, err, msg)
		}
	}
	return fmt.Errorf("cmux %s: %w", cmd, err)
}

// SessionsJSON returns `cmux sessions list --agent claude --json`.
func SessionsJSON() ([]byte, error) { return run(sessionsListArgs()...) }

// SessionsJSONListing is SessionsJSON with the listing bound: the sidebar
// loop's registry snapshot, never on a hook's critical path.
func SessionsJSONListing() ([]byte, error) { return runListing(sessionsListArgs()...) }

func sessionsListArgs() []string {
	return []string{"sessions", "list", "--agent", "claude", "--json"}
}

// WorkspaceList returns `cmux workspace list --id-format both`.
func WorkspaceList() (string, error) {
	out, err := run(workspaceListArgs()...)
	return string(out), err
}

func workspaceListArgs() []string { return []string{"workspace", "list", "--id-format", "both"} }

// SetInboxStatus shows the pending count as a sidebar pill on a workspace.
func SetInboxStatus(workspaceID string, pending int) error {
	_, err := run(setStatusArgs(workspaceID, pending)...)
	return err
}

func setStatusArgs(workspaceID string, pending int) []string {
	args := []string{"set-status", InboxStatusKey, inboxValue(pending), "--icon", "envelope", "--priority", "70"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	return args
}

// inboxValue is the inbox pill's text (D12): no glyph, cmux draws the
// envelope.
func inboxValue(pending int) string {
	if pending == 1 {
		return "1 message"
	}
	return fmt.Sprintf("%d messages", pending)
}

// ClearInboxStatus removes the pill (messages claimed). An empty workspace id
// is a no-op rather than cmux's "current workspace" default: outside a cmux
// pane there is no pill to clear, and a test must never touch a real one.
func ClearInboxStatus(workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	_, err := run("clear-status", InboxStatusKey, "--workspace", workspaceID)
	return err
}

// OneLine flattens text for a notification body.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if max > 0 && len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

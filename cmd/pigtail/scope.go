package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Work/perso isolation, CLI side: which scope this invocation speaks for.
// The daemon enforces the rule (see internal/scope); the CLI only says who is
// asking, from the Claude cell it runs under.

// envScope overrides the caller scope like --scope does.
const envScope = "PIGTAIL_SCOPE"

// flagScope is the persistent --scope flag (work|perso|all). On listings
// (subject list/next, theme list, board) it is the view asked for. It changes
// the caller's identity only outside any cell — see resolveScope.
var flagScope string

// scopeOverride is --scope, else $PIGTAIL_SCOPE, else "".
func scopeOverride() string {
	if flagScope != "" {
		return flagScope
	}
	return os.Getenv(envScope)
}

// claudeConfigDir is the Claude config dir of the calling session:
// $CLAUDE_CONFIG_DIR, else the default ~/.claude.
func claudeConfigDir(getenv func(string) string) string {
	if d := getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude")
	}
	return ""
}

// resolveScope decides the caller's identity scope — the X-Pigtail-Scope
// header. override is --scope / $PIGTAIL_SCOPE (a scope name or alias, or all).
//
//   - Inside a cell, the cell alone decides. From the master cell, override is
//     a view (sent as ?scope= on listings) and never an identity: `--scope
//     <scope>` does not let a write onto another scope's subject skip
//     --cross-scope. From any other cell, anything but its own scope is
//     refused outright (the alias of its scope is its scope).
//   - Outside any cell (the bare ~/.claude, a human terminal, no table), the
//     default is the table's default scope — the same fail-closed side as a
//     request without the header — and override is the identity: all speaks
//     for the master.
func resolveScope(cells *claudeprofile.Table, configDir, override string) (string, error) {
	o := strings.ToLower(strings.TrimSpace(override))
	// The cell first: an error about the override names only what this
	// caller may name (D19) — its own scope in a cell, every scope outside.
	var cell scope.Scope
	sc, inCell := cells.ScopeOf(configDir)
	if inCell {
		c, err := scope.Parse(sc)
		if err != nil {
			return "", fmt.Errorf("cell table: %s: %w", configDir, err)
		}
		cell = c
	}
	var want scope.Scope
	if o != "" && o != "all" {
		sc, err := scope.Parse(o)
		if err != nil {
			hint := scope.Hint(scope.Master())
			if inCell {
				hint = scope.Hint(cell)
			}
			return "", fmt.Errorf("--scope / %s: %w%s", envScope, err, hint)
		}
		want = sc
	}
	if inCell {
		if !scope.SeesAll(cell) && o != "" && want != cell {
			return "", fmt.Errorf("--scope %s refused: this session runs on a %s cell (%s), which reads its own scope only", o, cell, configDir)
		}
		return string(cell), nil
	}
	switch {
	case o == "all":
		return string(scope.Master()), nil
	case want != "":
		return string(want), nil
	}
	return string(scope.Default()), nil
}

// callerScope resolves the scope of this invocation from the environment.
func callerScope() (string, error) {
	cells, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "warning: %v — subject scope falls back to the default scope\n", err)
	}
	return resolveScope(cells, claudeConfigDir(os.Getenv), scopeOverride())
}

// newScopedClient is newClient speaking for the caller's scope: every
// theme/subject/phase/log/board call goes through it, and so does every read
// of the relay (peers, messages), whose answers the daemon filters by scope.
func newScopedClient() (*pigtail.Client, error) {
	sc, err := callerScope()
	if err != nil {
		return nil, err
	}
	return newClient().WithScope(sc), nil
}

// parseSince reads a --since value: a Go duration or Nd (days) back from now,
// a YYYY-MM-DD date (local midnight), or unix milliseconds.
func parseSince(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, fmt.Errorf("--since is empty")
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil && len(v) >= 10 {
		return time.UnixMilli(ms), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", v, now.Location()); err == nil {
		return d, nil
	}
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	} else if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("--since %q: want a duration (24h, 7d), a date (2026-09-22) or unix millis", v)
}

// scopeAccessKey is the cobra annotation every command that calls
// newScopedClient must carry, set through scopedWrite or scopedRead. A write
// command must also declare --cross-scope (addCrossScopeFlag + WithCrossScopeWrite);
// TestScopedCommandsDeclareAccess enforces both, so a new scoped command cannot
// land without deciding which it is.
const scopeAccessKey = "pigtail/scope-access"

// scopedWrite annotates a command that may write a theme/subject/phase/log
// (even if it can also read, like `subject log`).
func scopedWrite() map[string]string { return map[string]string{scopeAccessKey: "write"} }

// scopedRead annotates a command that only reads scoped routes. A relay
// command that reads peers or messages is one, even when it also claims its
// own inbox: the relay is not a subject-layer write, so it takes no
// --cross-scope.
func scopedRead() map[string]string { return map[string]string{scopeAccessKey: "read"} }

// sessionWrite annotates a relay command that acts AS the calling session
// (sessionClient) and may cross scopes with --cross-scope: `msg send`, whose
// notice the master cell may send to another scope (D2). Not a subject-layer
// write: it never calls newScopedClient. TestScopedCommandsDeclareAccess
// checks both halves.
func sessionWrite() map[string]string { return map[string]string{scopeAccessKey: "session-write"} }

// sessionScope is the scope the relay registry holds for the Claude session
// running under this environment: its cell's (selfPeer and peers.Build look
// the cell up the same way, ByConfigDir), work outside any cell. It is the
// X-Pigtail-Scope of every relay call that acts AS a session — claim, wait,
// ack, send, end — which the daemon honours only when it equals the session's
// registered scope. Never --scope / PIGTAIL_SCOPE: an override would name a
// scope the registry does not hold, and the session would lose its own inbox.
func sessionScope(getenv func(string) string) string {
	cells, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "warning: %v — relay session scope falls back to the default scope\n", err)
	}
	if c, ok := cells.ByConfigDir(claudeConfigDir(getenv)); ok {
		if sc, err := scope.Parse(c.Scope); err == nil {
			return string(sc)
		}
	}
	return string(scope.Default())
}

// sessionClient is newClient speaking as the calling Claude session on the
// relay's session-bound routes (and on POST /peers, which a session's own
// hooks use to register and end it). See sessionScope.
func sessionClient(getenv func(string) string) *pigtail.Client {
	return newClient().WithScope(sessionScope(getenv))
}

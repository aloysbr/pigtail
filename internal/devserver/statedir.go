package devserver

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Per-server runtime state — the ownership marker, the background log — used to
// live at the worktree root, as `.pigtail-<server>.owner` and
// `.pigtail-<server>.log`. That put pigtail's own bookkeeping one `git add -A`
// away from being committed, and it was: twice in a consumer repo, swept into
// unrelated commits. The marker then became a *tracked* file, so the day
// `pigtail stop --all` removed its own marker (which is correct — a stopped
// server must not keep claiming ownership), two other sessions found a deleted
// tracked file in their worktree.
//
// The .gitignore route was already tried four times in that same repo, one line
// per artifact name pigtail invented (`.wt-dev.log`, then `.pigtail-*.log`,
// then the rotated `.log.prev` the earlier pattern missed, then
// `.pigtail-*.owner`). Every consumer repo pays that tax again, and each new
// artifact is committable until someone notices. Moving the state out of the
// worktree is what makes the class stop growing: the worktree keeps only what
// the user owns.
//
// Two properties this file must preserve, both load-bearing:
//
//   - **No daemon required.** The state is a plain file tree, not daemon
//     memory: `pigtail dev` works with pigtaild down.
//   - **Two processes, one path.** The CLI writes markers, the daemon reads
//     them (GET /servers). They do not necessarily hold the same *spelling* of
//     the worktree path — the daemon has whatever was registered, the CLI has
//     os.Getwd() — so the key is the absolute, symlink-resolved path, which
//     both compute identically from their own spelling.

// stateRoot is pigtail's private state directory: $XDG_STATE_HOME/pigtail, or
// ~/.local/state/pigtail. The last resort (no home at all — a daemon started
// in an environment without HOME) is the temp dir rather than an error: losing
// ownership markers degrades `stop --all` into its pre-identity behaviour,
// while failing to resolve a path would break `pigtail dev` itself.
func stateRoot() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pigtail")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "pigtail")
	}
	return filepath.Join(os.TempDir(), "pigtail-state")
}

// canonicalPath is the one spelling of a directory every caller must agree on:
// absolute, cleaned, and symlink-resolved. EvalSymlinks is what makes the CLI
// and the daemon agree on macOS, where /tmp and /var are themselves symlinks
// (/private/…): the same worktree reached through either spelling must key the
// same state. Each step degrades to the previous one rather than failing — a
// path that no longer exists (a worktree just removed) still resolves to a
// stable, if unresolved, key.
func canonicalPath(dir string) string {
	p := filepath.Clean(dir)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return p
}

// worktreeKey names one worktree's state directory. The hash is the identity
// (basenames repeat across projects — every project has a `main`); the
// basename prefix is legibility, so `ls ~/.local/state/pigtail/servers` stays
// readable by a human debugging a stuck marker.
func worktreeKey(wtDir string) string {
	canon := canonicalPath(wtDir)
	sum := sha256.Sum256([]byte(canon))
	return sanitizeSegment(filepath.Base(canon)) + "-" + hex.EncodeToString(sum[:])[:12]
}

// sanitizeSegment reduces an arbitrary string to one safe path segment. Server
// names come from a project's pigtail.yaml — a file pigtail does not control —
// so a name carrying a separator must not be able to place a file outside the
// state dir.
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "_"
	}
	return out
}

// StateDir is pigtail's private directory for one worktree's runtime state.
// Pure: it neither creates nor touches anything (see EnsureStateDir).
func StateDir(wtDir string) string {
	return filepath.Join(stateRoot(), "servers", worktreeKey(wtDir))
}

// EnsureStateDir creates a worktree's state directory if needed and returns it.
// Created lazily on first write, so a worktree whose servers were never started
// leaves no trace at all.
func EnsureStateDir(wtDir string) (string, error) {
	dir := StateDir(wtDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// stateFile is the path of one runtime-state file of one server.
func stateFile(wtDir, serverName, ext string) string {
	return filepath.Join(StateDir(wtDir), sanitizeSegment(serverName)+ext)
}

// Tearing state down is the caller's job (the cleanup handler removes
// StateDir(ws.Path) once the worktree is gone). No helper here on purpose: the
// path must be resolved BEFORE the worktree disappears — canonicalPath can
// only resolve symlinks on a path that still exists — and a helper taking the
// worktree path would invite resolving it after.

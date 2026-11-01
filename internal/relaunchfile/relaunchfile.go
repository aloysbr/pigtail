// Package relaunchfile holds the files pigtail writes for the `c` launcher
// (the user's launcher script), shared by the CLI (session move, session
// respawn) and pigtaild (the idle pause).
package relaunchfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The launcher's environment contract: CLAUDE_RELAUNCH_FILE names the tab's
// <key>.sid, CLAUDE_RELAUNCH_PROTO the protocol it speaks.
const (
	EnvFile  = "CLAUDE_RELAUNCH_FILE"
	EnvProto = "CLAUDE_RELAUNCH_PROTO"
	Proto    = "1"
)

// Files pigtail writes for the `c` launcher, shared by session move, session
// respawn and the idle pause.
//
// The launcher honours <key>.handoff only when <key>.sid exists and holds the
// handoff's session id. The SessionEnd hook (the user's
// relaunch hook) normally writes that .sid, but a SIGTERM
// skips the hook and a cell without it, a failing hook or one that outlives
// its timeout leave nothing: so pigtail writes the .sid itself, in the hook's
// format, before the handoff (ClaimSid), and takes it back on every
// abandon (SidClaim.Release) — only while the file is still its own.

// WriteAtomic replaces path with data (temp file in the same dir, 0600,
// then rename): a reader never sees a half-written file.
func WriteAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SidClaim is one <key>.sid write by pigtail: release removes the file only
// while it still holds that id, and only once. A caller that writes the .sid
// again (respawn's re-check withdraws, then rewrites on the next pass) takes
// a new claim per write. Safe from an interrupt goroutine.
type SidClaim struct {
	mu   sync.Mutex
	path string
	sid  string // "" once released
}

// ClaimSid writes <key>.sid = « <sid>\n », the SessionEnd hook's format,
// atomically and 0600.
func ClaimSid(path, sid string) (*SidClaim, error) {
	if err := WriteAtomic(path, []byte(sid+"\n")); err != nil {
		return nil, fmt.Errorf("write session id file: %w", err)
	}
	return &SidClaim{path: path, sid: sid}, nil
}

// Release withdraws the .sid, once, and only if it still holds the id
// pigtail wrote: the hook (for another id after a /clear, or the old Claude
// exiting on its own) or a new Claude may have rewritten it, and that file is
// theirs. The hook writing the same id cannot be told apart; the launcher
// reads the .sid the moment Claude exits, so a later withdrawal costs at most
// the « Entrée = relancer » prompt inside a window of a few syscalls.
// Read-then-remove is not atomic. A nil claim (nothing written) is a no-op.
func (c *SidClaim) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sid == "" {
		return
	}
	if b, err := os.ReadFile(c.path); err == nil && strings.TrimSpace(string(b)) == c.sid {
		_ = os.Remove(c.path)
	}
	c.sid = ""
}

// ReleaseSid removes path only while it holds sid (trimmed): the stateless
// form of SidClaim.Release, for a caller that kept only the path and the id
// (pigtaild's pause row, across a daemon restart). A missing file and a file
// holding another id are both nil (the latter is kept); a read or remove
// error is returned.
func ReleaseSid(path, sid string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != sid {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Dir is the launcher's directory: ${XDG_STATE_HOME:-$HOME/.local/state}/claude-relaunch.
func Dir(getenv func(string) string) string {
	base := getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, "claude-relaunch")
}

// SidPathAllowed accepts path only when it names a .sid file directly under
// dir (after cleaning, no ".." escape, no sub-directory): pigtaild never
// writes a file whose path came from another process without this check.
func SidPathAllowed(dir, path string) error {
	if path == "" || dir == "" {
		return fmt.Errorf("relaunch file %q outside %s", path, dir)
	}
	cp, cd := filepath.Clean(path), filepath.Clean(dir)
	if strings.Contains(path, "..") || filepath.Dir(cp) != cd || filepath.Ext(cp) != ".sid" || filepath.Base(cp) == ".sid" {
		return fmt.Errorf("relaunch file %q outside %s", path, dir)
	}
	return nil
}

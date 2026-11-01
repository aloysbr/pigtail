// Package claudesession locates and relocates Claude Code transcripts.
//
// Claude Code persists one conversation per file, inside the config dir that
// selects the account:
//
//	<config-dir>/projects/<cwd-slug>/<id>.jsonl
//	<config-dir>/projects/<cwd-slug>/<id>/      (sidecar: sub-agent transcripts)
//
// Because the transcript lives inside the profile, a session relaunched under
// another account cannot find it — which is what this package exists to fix.
package claudesession

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// There is deliberately no Slug(cwd) helper here. Deriving the project
// directory name from a working directory ('/' and '.' both become '-') is
// only ever a guess: the mapping is not injective, and a session whose cmux
// binding does not expose a cwd would silently get the wrong path. The real
// slug is read off the transcript found on disk (see Find) and carried from
// there. The helper existed, was replaced by exactly that, and is not coming
// back.

// Transcript identifies one conversation on disk.
type Transcript struct {
	ID        string
	ConfigDir string
	Slug      string
}

// JSONLPath is the conversation file.
func (t Transcript) JSONLPath() string {
	return filepath.Join(t.ConfigDir, "projects", t.Slug, t.ID+".jsonl")
}

// SidecarDir is the sibling directory holding sub-agent transcripts. It does
// not always exist.
func (t Transcript) SidecarDir() string {
	return filepath.Join(t.ConfigDir, "projects", t.Slug, t.ID)
}

// FindAll returns one transcript per config dir hosting id, in the order the
// config dirs are given. Zero, one or several: all three are real states.
//
// Several is what a hand-migrated session looks like — copied to the new
// account without the old copy being removed — and it has no correct answer,
// only a choice the user has to make. FindAll is the primitive that lets a
// caller see it; Find is the one-answer convenience on top.
func FindAll(id string, configDirs []string) ([]Transcript, error) {
	var found []Transcript
	for _, cfg := range configDirs {
		matches, err := filepath.Glob(filepath.Join(cfg, "projects", "*", id+".jsonl"))
		if err != nil {
			// Only ErrBadPattern reaches here (Glob ignores I/O errors), which
			// means the id carried glob metacharacters — report it rather than
			// letting it read as "no such session".
			return nil, fmt.Errorf("look up %s in %s: %w", id, cfg, err)
		}
		if len(matches) == 0 {
			continue
		}
		found = append(found, Transcript{
			ID:        id,
			ConfigDir: cfg,
			Slug:      filepath.Base(filepath.Dir(matches[0])),
		})
	}
	return found, nil
}

// Find returns the transcript for id in the first config dir that hosts it.
// The hosting config dir IS the session's account.
//
// "First" is only meaningful because the config dir order is deterministic
// (claudeprofile.Table.ConfigDirs sorts by cell id) — but a session hosted by
// two accounts at once has no single account, so Find refuses to pick one and
// returns an error naming every config dir that holds it. That is a state the
// user can genuinely be in (eight sessions were left exactly there by the
// 2026-08-12 hand migration), and the only thing the command can honestly do
// is say so: guessing would report a different account run to run.
//
// The three outcomes are distinct: (_, false, nil) no such session,
// (tr, true, nil) exactly one, (_, false, err) ambiguous or unusable id.
func Find(id string, configDirs []string) (Transcript, bool, error) {
	found, err := FindAll(id, configDirs)
	if err != nil {
		return Transcript{}, false, err
	}
	switch len(found) {
	case 0:
		return Transcript{}, false, nil
	case 1:
		return found[0], true, nil
	}
	paths := make([]string, 0, len(found))
	for _, tr := range found {
		paths = append(paths, tr.JSONLPath())
	}
	return Transcript{}, false, fmt.Errorf(
		"session %s exists in %d accounts at once (%s): delete the copy you do not want to keep, then retry",
		id, len(found), strings.Join(paths, ", "))
}

// Copy duplicates a transcript into dstConfigDir, under the same slug, and
// checks that the copy has the same SIZE as the source — not the same
// content. Size catches a truncated or interrupted copy, which is the failure
// this move can actually produce; it would not catch same-length corruption,
// and nothing here claims otherwise (a checksum is the recorded follow-up).
//
// It refuses to overwrite an existing target: two reachable copies of one
// conversation diverge silently as soon as either is resumed.
//
// Nothing partial ever wears the destination's real name: the file is
// streamed to "<id>.jsonl.partial" and renamed into place only once verified,
// and every failure removes that temporary file. A failed Copy therefore
// leaves the destination exactly as it found it, so a retry is a retry and
// not a conflict — and `claude --resume <id>` under the destination account
// can never load a half-written transcript.
//
// Copy never touches the source. Removal is RemoveSource, called only once the
// session has been relaunched successfully.
//
// The sidecar is copied and verified FIRST, the .jsonl renamed into place LAST: a
// visible destination transcript always has its complete sub-agent transcripts. A sidecar
// directory this call created is removed on any failure. Every caller holds
// LockSession(id) (handover, session move): two concurrent Copy of one session would share the
// destination sidecar, and one's cleanup would remove the other's files.
func Copy(src Transcript, dstConfigDir string) (Transcript, error) {
	dst := Transcript{ID: src.ID, ConfigDir: dstConfigDir, Slug: src.Slug}

	if _, err := os.Stat(src.JSONLPath()); err != nil {
		return Transcript{}, fmt.Errorf("source transcript: %w", err)
	}
	if _, err := os.Stat(dst.JSONLPath()); err == nil {
		return Transcript{}, fmt.Errorf("target already has a transcript for %s: %s", src.ID, dst.JSONLPath())
	}
	if err := os.MkdirAll(filepath.Dir(dst.JSONLPath()), 0o700); err != nil {
		return Transcript{}, err
	}
	_, statErr := os.Lstat(dst.SidecarDir())
	sidecarPreexisted := statErr == nil
	dropSidecar := func(cause error) error {
		if sidecarPreexisted {
			return cause
		}
		if err := os.RemoveAll(dst.SidecarDir()); err != nil {
			return fmt.Errorf("%w (and the partial sidecar %s could not be removed: %s)", cause, dst.SidecarDir(), err.Error())
		}
		return cause
	}
	if err := copySidecar(src, dst); err != nil {
		return Transcript{}, dropSidecar(err)
	}
	if err := copyFile(src.JSONLPath(), dst.JSONLPath()); err != nil {
		return Transcript{}, dropSidecar(err) // visible last
	}
	return dst, nil
}

// RemoveSource deletes the transcript and its sidecar. Call it last.
func RemoveSource(src Transcript) error {
	if err := os.Remove(src.JSONLPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(src.SidecarDir()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// copySidecar copies the sibling <id>/ directory of sub-agent transcripts,
// when there is one, and verifies the result.
//
// "When there is one" has to mean exactly that. A single Stat used to decide
// it, and ANY error — a permission problem, a symlink loop, an I/O error —
// read as "no sidecar": the copy was skipped silently and RemoveSource still
// deleted the directory with RemoveAll, printing ✓ source removed over lost
// sub-agent transcripts. Only os.IsNotExist means there is nothing to copy;
// anything else fails the Copy, before deletion is ever reached.
//
// The verification is the same standard the main .jsonl is held to, applied
// to a tree: recursive file count and total bytes. It is not a content check,
// but it catches the failure that can actually
// happen here — a copy that stopped partway.
func copySidecar(src, dst Transcript) error {
	switch _, err := os.Stat(src.SidecarDir()); {
	case os.IsNotExist(err):
		return nil // no sidecar to copy
	case err != nil:
		return fmt.Errorf("sidecar %s: %w", src.SidecarDir(), err)
	}
	if err := copyTree(src.SidecarDir(), dst.SidecarDir()); err != nil {
		return fmt.Errorf("sidecar: %w", err)
	}
	srcFiles, srcBytes, err := treeStats(src.SidecarDir())
	if err != nil {
		return fmt.Errorf("sidecar: measure source: %w", err)
	}
	dstFiles, dstBytes, err := treeStats(dst.SidecarDir())
	if err != nil {
		return fmt.Errorf("sidecar: measure copy: %w", err)
	}
	if srcFiles != dstFiles || srcBytes != dstBytes {
		return fmt.Errorf("sidecar copy of %s at %s holds %d file(s)/%d bytes, source at %s holds %d/%d",
			src.ID, dst.SidecarDir(), dstFiles, dstBytes, src.SidecarDir(), srcFiles, srcBytes)
	}
	return nil
}

// treeStats counts the regular files under root and sums their sizes.
func treeStats(root string) (files int, bytes int64, err error) {
	err = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

// copyFile copies from → to atomically, and streams it.
//
// Atomically: the bytes go to "<to>.partial", created with O_EXCL so two
// copies of the same transcript cannot silently interleave, and the file is
// renamed onto `to` only after it has been flushed to disk and its size
// checked against the source. Nothing partial ever wears the final name — a
// truncated transcript sitting under the canonical <id>.jsonl in a Claude
// config dir is a file `claude --resume` would happily load. Every failure
// path removes the temporary file, so a failed copy leaves the destination as
// it found it and the next attempt is not refused as a conflict.
//
// Streamed: real transcripts reach 21 MB, and os.ReadFile/os.WriteFile held
// all of that in memory twice over.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // read-only handle
	si, err := in.Stat()
	if err != nil {
		return err
	}

	tmp := to + ".partial"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// A leftover .partial can only come from a killed process; name it, so
		// the user knows exactly which file to delete to unstick the move.
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	// discard removes the temporary file on any failure below. Close is called
	// first (and its own error ignored) so the descriptor is released before
	// the unlink, and so the deferred second Close is a no-op.
	discard := func(cause error) error {
		_ = out.Close()
		if rmErr := os.Remove(tmp); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("%w (and %s could not be removed: %s)", cause, tmp, rmErr.Error())
		}
		return cause
	}

	if _, err := io.Copy(out, in); err != nil {
		return discard(fmt.Errorf("copy %s: %w", from, err))
	}
	// Sync before verifying: a size read back from a dirty page cache proves
	// the write was accepted, not that it reached the disk.
	if err := out.Sync(); err != nil {
		return discard(fmt.Errorf("sync %s: %w", tmp, err))
	}
	ti, err := out.Stat()
	if err != nil {
		return discard(fmt.Errorf("stat %s: %w", tmp, err))
	}
	if err := out.Close(); err != nil {
		return discard(fmt.Errorf("close %s: %w", tmp, err))
	}
	if ti.Size() != si.Size() {
		return discard(fmt.Errorf("copy of %s is %d bytes, source is %d", from, ti.Size(), si.Size()))
	}
	if err := os.Rename(tmp, to); err != nil {
		return discard(fmt.Errorf("rename %s: %w", tmp, err))
	}
	return nil
}

// copyTree is a var, in the same style as the psRun seam below, so a test can
// make it copy only part of a tree — the "copy stopped partway" failure the
// sidecar verification exists to catch, which no real filesystem produces on
// demand.
var copyTree = func(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(p, target)
	})
}

// psRun is injected so Running can be tested without spawning processes.
// Production code calls the real ps; tests may override it. -ww disables the
// output-width truncation `ps` otherwise applies to args=: a truncated line
// would silently drop the id or the claude token and turn into a false
// negative — which matters on any Linux runtime, where procps truncates to
// the terminal width (or to 80 columns with no tty) far more eagerly than
// macOS does. Not a CI concern: CI never executes this code, every test
// injects the seam.
var psRun = func() ([]byte, error) {
	return exec.Command("ps", "-Awwo", "pid=,args=").Output()
}

// psLineRe splits one `ps -Awwo pid=,args=` line into its pid and the rest
// of the command line.
var psLineRe = regexp.MustCompile(`^\s*(\d+)\s+(.*)$`)

// claudeProcRe matches a command line carrying a "claude" path token: either
// a bare `claude`, or any path ending in `/claude`, as a whitespace-delimited
// word anywhere in the line — not just at argv[0].
//
// argv[0] alone is not enough: Claude Code's documented local-installer
// layout (`claude migrate-installer`, under ~/.claude/local/) launches as
//
//	node /Users/x/.claude/local/node_modules/.bin/claude --resume <id>
//
// where argv[0] is "node", not a claude path — anchoring on argv[0] alone
// made that shape a false negative (a live Claude reported as gone).
//
// The session id appearing somewhere in argv is not sufficient on its own:
// a shell running `pigtail session move --session <id>` carries the id in
// its own argv too, and must not count as "the session is running". This
// regex is what rules that out — verified against this machine's real `ps
// -Awwo pid=,args=` output: the `pigtail session move` command line and the
// cmux zsh resume wrapper (which carries the literal text "printf claude)"
// with no trailing word boundary, and CLAUDE_CONFIG_DIR=... with a dot, not
// a slash, before "claude") both correctly do not match, while every live
// claude process — homebrew (/opt/homebrew/bin/claude) and nvm/node-shim
// (~/.nvm/versions/node/*/bin/claude) — does.
var claudeProcRe = regexp.MustCompile(`(^|\s)(\S*/claude|claude)(\s|$)`)

// Running reports whether a Claude process is currently serving this session.
//
// This cannot be pgrep: pgrep -f excludes not just its own pid but every
// ancestor of the calling process by default (see `man pgrep`'s description
// of -a). `pigtail session move` invoked from inside the very pane it is
// moving has its own Claude process as an ancestor of the pigtail process —
// pgrep would silently report that Claude as "not running", so WaitExit would
// return immediately, right after /exit was typed and long before Claude
// could act on it. Enumerating every process directly, instead of asking a
// filtering tool to do it, is the only way to still see that ancestor.
//
// A match requires BOTH a session flag naming the id (namesSession) AND the
// command to carry a claude path token (claudeProcRe) — id-only matching
// would also fire on `pigtail session move --session <id>` itself, and on a
// session whose prompt merely mentions the id (a parent's scratchpad path).
//
// Our own pid is excluded explicitly, on top of the argv0 check, as a second
// line of defence.
//
// Running fails closed: only a clean process listing with no match means
// "not running". Any error obtaining the listing — ps missing, permission
// denied, etc. — means "I don't know", treated as "still running" to prevent
// copying a transcript that may still be written to.
func Running(id string) bool {
	out, err := psRun()
	if err != nil {
		return true // could not obtain the listing — assume busy rather than risk copying a live transcript
	}
	self := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		m := psLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid == self {
			continue
		}
		args := m[2]
		if namesSession(args, id) && claudeProcRe.MatchString(args) {
			return true
		}
	}
	return false
}

// namesSession reports whether a command line gives id as the value of a flag
// that selects the session a claude process serves. ps flattens argv, so a
// prompt quoting `--resume <id>` word for word still matches: that errs towards
// "running", the safe side.
func namesSession(args, id string) bool {
	re, err := regexp.Compile(`(^|\s)(--resume|-r|--session-id)(=|\s+)` + regexp.QuoteMeta(id) + `(\s|$)`)
	return err != nil || re.MatchString(args)
}

// WaitExit blocks until no process serves the session any more, polling every
// interval. It returns an error when timeout elapses first — which is how a
// busy session (one that ignored /exit because it was mid-tool-use) is refused
// before anything on disk has been touched.
//
// probe is injected so the behaviour is testable without spawning processes;
// production callers pass Running.
func WaitExit(id string, timeout, interval time.Duration, probe func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if !probe(id) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("session %s still running after %s (busy? quit it by hand first)", id, timeout)
		}
		time.Sleep(interval)
	}
}

// ExitDialogShown reports whether a screen shows Claude Code's "Background
// work is running" dialog — what /exit opens, instead of quitting, when the
// session has subagents or background shells. That dialog preselects "Exit and
// stop tasks": a single Enter kills the session's background work.
//
// It needs the title and the destructive option together, so the title merely
// quoted in the conversation above the prompt does not count as an open
// dialog. The strings are Claude Code's own (2.1.281); should they change, the
// check fails closed — no dialog seen, nothing pressed.
func ExitDialogShown(screen string) bool {
	return strings.Contains(screen, "Background work is running") &&
		strings.Contains(screen, "Exit and stop tasks")
}

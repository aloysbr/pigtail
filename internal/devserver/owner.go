package devserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aloysbr/pigtail/internal/envlocal"
)

// Ownership marks which session started a dev server, so a blanket
// `pigtail stop --all` from one session stops its own servers and forgotten
// orphans without killing another session's work. Recorded as a small file
// next to the log — in pigtail's own state directory, never in the worktree
// (see statedir.go) — so `pigtail dev` keeps working with no daemon running.

// EnvOwner lets a non-cmux session declare its identity. An alias of
// envlocal.Owner rather than a second literal: the name is also written into
// .env.local by `pigtail new` (internal/backend/git), and two copies of a wire
// name drift.
const EnvOwner = envlocal.Owner

// envCmuxWorkspace is cmux's workspace id — the natural session identity here.
const envCmuxWorkspace = "CMUX_WORKSPACE_ID"

// OwnerID returns this session's identity for the given worktree, or "" when
// it has none. Four sources, in order: cmux's workspace id, an explicit
// PIGTAIL_OWNER in the real environment, PIGTAIL_OWNER recorded in the
// worktree's .env.local by `pigtail new`, then the derived POSIX session.
//
// The third cran exists because the fourth one does not survive an agent: a
// Bash-tool invocation setsid-detaches, so the session leader is the
// per-call shell and dies when the call returns — a server it started would
// carry a dead owner within a second, reclaimable by any other session's
// `--all`. The worktree is the one container stable across an agent
// session's invocations, so `pigtail new` writes PIGTAIL_OWNER there and this
// is what reads it back. An explicit env export always wins over both file
// sources — it is the only way to name a session legibly, or to share one
// deliberately across worktrees.
func OwnerID(wtDir string) string {
	if id := os.Getenv(envCmuxWorkspace); id != "" {
		return id
	}
	if id := os.Getenv(EnvOwner); id != "" {
		return id
	}
	if id := envLocalOwner(wtDir); id != "" {
		return id
	}
	return sessionIdentity()
}

// envLocalOwner reads the PIGTAIL_OWNER assignment from a worktree's
// .env.local, or "" when the file is missing, unreadable, or carries no such
// assignment — never an error, since OwnerID must fall through to its next
// cran rather than fail.
//
// The parsing itself lives in internal/envlocal, shared with ReadPort and with
// the worktree-creation path that writes this file: the same shell-faithful
// rules (comments, `export NAME=`, quoting, last assignment wins) apply
// wherever pigtail reads a .env.local.
func envLocalOwner(wtDir string) string {
	data, err := os.ReadFile(filepath.Join(wtDir, ".env.local"))
	if err != nil {
		return ""
	}
	owner, _ := envlocal.Lookup(data, EnvOwner)
	return owner
}

// ownedByMe reports whether a server bearing `owner` is mine to stop in a
// scoped sweep. An empty owner is an orphan — a server nobody claims, which is
// exactly the "forgotten server" a cleanup is meant to reap. Pure.
func ownedByMe(owner, me string) bool {
	return owner == "" || owner == me
}

// OwnerPath is the marker file for one server of a worktree, in pigtail's
// state directory. Exported alongside ReadOwner so the daemon side (and its
// tests) can name what it reads; writing it goes through writeOwner, which is
// the only atomic path.
func OwnerPath(wtDir, serverName string) string {
	return stateFile(wtDir, serverName, ".owner")
}

// legacyOwnerPath is where markers lived before they moved out of the
// worktree: `<worktree>/.pigtail-<server>.owner`. Still READ, as a fallback,
// so a server already running at upgrade time does not turn into an orphan the
// next `stop --all` reaps.
//
// Never written, and — deliberately — never removed. In the repo where this
// bit, the marker had been committed on four branches: removing it is exactly
// the deletion of a tracked file that made this PR necessary. A new marker in
// the state dir shadows the legacy one on read, which is all a migration
// actually needs; the leftover file disappears on its own when the branch is
// next rebased on a main that ignores it, or under one `rm` from the user.
func legacyOwnerPath(wtDir, serverName string) string {
	return filepath.Join(wtDir, fmt.Sprintf(".pigtail-%s.owner", serverName))
}

// writeOwner records the owner of a freshly started server. It always asserts
// the truth about the server it just started, including when that truth is
// "nobody claims it": an empty owner REMOVES any marker instead of leaving the
// previous one in place. Skipping the removal is what let a marker outlive its
// server — a session with no identity (a plain terminal, outside cmux and
// without PIGTAIL_OWNER) could never correct a marker left behind by a kill
// path, so a stale one stayed stale for good.
//
// Only ever reached when the port was free, since every start path
// short-circuits on an already-running server: the marker being cleared here
// can only belong to a server that is already gone.
//
// Written via a temp file + rename rather than os.WriteFile, which truncates
// the target then writes its bytes — a reader landing inside that window
// (the daemon's rammon sweep, or another session's triageScope, both running
// concurrently in a separate process) sees "", which ownedByMe reads as
// *unclaimed* and therefore stoppable. os.Rename is atomic within a
// filesystem: any reader sees either the old content or the new, never
// neither. The temp file is therefore created in the DESTINATION directory —
// the state dir, not the worktree it used to sit in: a rename across
// filesystems is not atomic (and, with the state dir under $HOME and worktrees
// anywhere, crossing one is a realistic accident, not a theoretical one).
func writeOwner(wtDir, serverName, owner string) error {
	if owner == "" {
		clearOwner(wtDir, serverName)
		return nil
	}
	dir, err := EnsureStateDir(wtDir)
	if err != nil {
		return err
	}
	target := OwnerPath(wtDir, serverName)
	// os.CreateTemp creates the file 0o600 already (O_RDWR|O_CREATE|O_EXCL),
	// matching the permissions the old os.WriteFile call used.
	tmp, err := os.CreateTemp(dir, filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(owner + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// ReadOwner returns the recorded owner of one server, or "" when absent or
// unreadable. Exported because the daemon reads markers too (GET /servers),
// while writing them stays a CLI concern.
// An unreadable marker is treated as an orphan rather than as a hard error:
// refusing to stop a server because we cannot read a metadata file would be
// worse than the ambiguity.
//
// Falls back to the pre-move location inside the worktree when the state dir
// has nothing for this server. That fallback is the whole migration path: a
// server started before the upgrade keeps its owner, so the first `stop --all`
// after upgrading spares it instead of reaping it as an orphan. The state dir
// always wins once it holds a marker, so the first write after the upgrade
// supersedes the legacy file without touching it.
func ReadOwner(wtDir, serverName string) string {
	b, err := os.ReadFile(OwnerPath(wtDir, serverName))
	if err != nil {
		b, err = os.ReadFile(legacyOwnerPath(wtDir, serverName))
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(string(b))
}

// clearOwner drops the marker once a server is stopped. Best-effort, and
// scoped to pigtail's own state dir: the legacy in-worktree marker is left
// alone on purpose (see legacyOwnerPath) — removing a file that may be tracked
// is the incident this whole move exists to prevent.
func clearOwner(wtDir, serverName string) {
	_ = os.Remove(OwnerPath(wtDir, serverName))
}

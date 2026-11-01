package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A mounted network share was the first offsite destination, and the first real
// copy took **38 minutes** for 768 KiB because the mount had to wake up — with
// nothing anywhere able to say what was happening (see offsite.go and
// internal/daemon/offsite.go). The same file, to the same NAS, over rsync/ssh:
// **0.46 seconds**.
//
// Speed is not really the point. A remote destination has no mount to keep
// alive: nothing to remount after a reboot, nothing to wake on first touch, and
// over a mesh VPN it keeps working away from the LAN. What it costs is a shell
// command on the far side, which is the whole of this file.
//
// The rsync/scp `[user@]host:/path` spelling is used rather than a second flag:
// the destination stays one notion, and its shape picks the transport.

// RemoteTimeout bounds a single rsync. A dead link must fail rather than hang
// the way the mount did — one attempt at a time is in flight, so a copy that
// never returns stops every later one.
//
// Exported: internal/daemon/offsite_fallback_test.go
// (TestExerciseFallbackProbeContextIsTighterThanRemoteTimeout) compares the
// fallback probe's own timeout against this value directly, across the
// package boundary — see offsiteFallbackDueInterval's doc comment
// (internal/daemon/offsite_fallback.go) for the sibling constant that used
// to be copied instead of exported, and drifted.
const RemoteTimeout = 10 * time.Minute

// runner executes a command under a context. A field rather than a direct exec
// call so the transport can be tested without an SSH server.
type runner func(ctx context.Context, name string, args ...string) error

// execRunner runs one command and kills it when ctx expires.
//
// The context matters here in a way it does not for the mounted-volume path.
// That one blocks inside the kernel on a hung mount, where cancellation buys
// nothing — but ssh and rsync are *subprocesses*, and CommandContext kills
// them. Without it, a NAS that accepts a TCP connection and then says nothing
// hangs this call forever, and since only one offsite attempt is ever in
// flight, forever means no offsite copy will ever run again until the daemon
// restarts. Nothing logs that: the state file just keeps an attempt_started_at
// that never resolves.
func execRunner(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // name is a package constant, args are built here
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s timed out after %s: %w", name, RemoteTimeout, ctx.Err())
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

// sshHardening keeps a stalled link from becoming an unbounded wait.
//
// ConnectTimeout bounds the handshake, which the rsync --timeout below does
// not cover: that one is an I/O timeout and only starts counting once there is
// a transfer. BatchMode refuses to prompt — a daemon under launchd has no
// terminal, so a password or host-key prompt does not fail, it waits for an
// answer that can never come.
var sshHardening = []string{"-o", "ConnectTimeout=10", "-o", "BatchMode=yes"}

// ParseRemoteDest splits an rsync-style `[user@]host:/path` destination.
//
// A leading slash settles the ambiguity with local paths that contain a colon:
// a remote spec never starts with one, and `/Volumes/weird:name` is a directory
// on this machine. Both halves must be non-empty, so `host:` and `:/path` are
// not remote specs either — they are far more likely to be a typo than an
// intent, and treating them as remote would send a backup nowhere.
func ParseRemoteDest(dest string) (host, path string, ok bool) {
	if dest == "" || strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, ".") {
		return "", "", false
	}
	i := strings.Index(dest, ":")
	if i <= 0 || i == len(dest)-1 {
		return "", "", false
	}
	// A slash before the colon means the colon is inside a path segment.
	if strings.Contains(dest[:i], "/") {
		return "", "", false
	}
	return dest[:i], dest[i+1:], true
}

// shellQuote wraps s for a POSIX shell, escaping embedded single quotes.
// The destination comes from a flag rather than from an attacker, but a NAS
// share called "my drive" is ordinary and would otherwise split the command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// remoteRotateScript builds the far-side rotation: drop the oldest generation,
// shift each one down, leave slot 0 free for the copy. Same contract as the
// local rotateBackups.
//
// Deliberately POSIX — no `[[`, no arrays, no `local`. The far side is a NAS
// whose /bin/sh is dash or ash, and this repo has already been bitten by a
// script that was green on macOS's bash and red on Linux's dash.
//
// A missing generation is normal on the first runs, so every move is guarded
// with `[ -f ]` rather than allowed to fail under `set -e`.
func remoteRotateScript(dir, base string, keep int) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "d=%s\n", shellQuote(dir))
	fmt.Fprintf(&b, "rm -f \"$d\"/%s.%d\n", base, keep-1)
	for gen := keep - 2; gen >= 0; gen-- {
		fmt.Fprintf(&b, "if [ -f \"$d\"/%s.%d ]; then mv \"$d\"/%s.%d \"$d\"/%s.%d; fi\n",
			base, gen, base, gen, base, gen+1)
	}
	return b.String()
}

// backupOffsiteRemote rotates the far-side generations and sends the newest
// local one, in exactly two round trips.
//
// rsync writes to a temporary name and renames into place, so the atomicity the
// local path buys with an explicit `.partial` file comes for free here.
func backupOffsiteRemote(dbPath, host, path string, keep int, run runner) error {
	// One deadline per command rather than one for the pair: each is bounded,
	// and a slow-but-progressing rotation does not eat the copy's budget.
	withTimeout := func(f func(context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), RemoteTimeout)
		defer cancel()
		return f(ctx)
	}
	src := backupPath(dbPath, 0)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil // nothing backed up locally yet; nothing to send
		}
		return fmt.Errorf("stat the newest local generation: %w", err)
	}
	base := filepath.Base(dbPath)

	// Rotation first, and its failure stops the sequence: copying into a
	// directory whose generations were not shifted would overwrite .0 while .1
	// still holds a snapshot older than it should.
	rotateArgs := append(append([]string{}, sshHardening...), host, remoteRotateScript(path, base, keep))
	if err := withTimeout(func(ctx context.Context) error {
		return run(ctx, "ssh", rotateArgs...)
	}); err != nil {
		return fmt.Errorf("rotate offsite generations on %s: %w", host, err)
	}

	// The remote path is passed **raw**, and -s (--protect-args) is what makes
	// that safe. rsync otherwise hands the remote path to a shell of its own,
	// so quoting it here does not protect anything — the quotes arrive as
	// literal characters. Shipped that way in #79 and caught in production on
	// a first real copy:
	//
	//	change_dir "/srv/homes/user//'/srv/backups"
	//
	// The leading quote turned an absolute path into a relative one, resolved
	// against the login home. With -s there is no remote shell to defend
	// against, so a path with a space needs no quoting at all.
	dest := fmt.Sprintf("%s:%s", host, path+"/"+base+".0")
	if err := withTimeout(func(ctx context.Context) error {
		return run(ctx, "rsync", "-a", "-s",
			// --timeout only starts counting once bytes move; the ssh it
			// spawns needs its own bound on the handshake.
			"-e", "ssh "+strings.Join(sshHardening, " "),
			fmt.Sprintf("--timeout=%d", int(RemoteTimeout.Seconds())), src, dest)
	}); err != nil {
		return fmt.Errorf("copy to %s: %w", host, err)
	}
	return nil
}

//go:build unix

package devserver

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// An ordinary session — a plain terminal, an agent outside cmux — has no env
// var to identify it, so every server it started counted as an orphan any
// other session's `stop --all` could reap. The POSIX session id closes that:
// the kernel creates it at login, every descendant inherits it (sub-shells,
// scripts, an agent's shell tool), it lasts as long as the session does, and
// it can be interrogated — the SID *is* the PID of the session leader.
//
// PPID was rejected (it changes with whoever spawns pigtail) and the TTY too
// (absent without a terminal, lost by a detached process).

// sessionPrefix namespaces a derived identity, so OwnerAlive can tell one
// apart from an opaque identity (cmux, PIGTAIL_OWNER) it must not interrogate.
const sessionPrefix = "sid:"

// Syscall seams, as package vars so tests can script them without owning a
// real session leader. Same pattern as the timing seams in devserver.go.
//
// getsid uses golang.org/x/sys/unix rather than the standard syscall
// package: syscall.Getsid is only implemented for the BSD family (darwin,
// freebsd, netbsd, openbsd, dragonfly) — the standard library has no
// zsyscall_linux_*.go defining it, so a build tagged "unix" that called
// syscall.Getsid would fail to compile on Linux, our CI target.
// golang.org/x/sys/unix wraps the raw syscall on every unix GOOS, Linux
// included, and was already an indirect dependency of this module.
var (
	getsid   = func() (int, error) { return unix.Getsid(0) }
	hostname = os.Hostname
	// pidAlive tests session LEADERSHIP, not mere existence. Existence
	// (syscall.Kill(pid, 0) == nil) was the original check, and it is wrong
	// here: an agent's shell tool setsid-detaches every command, so the sid
	// recorded in a marker is a short-lived pid from the fast-churning end
	// of the pid space, and macOS wraps pids at ~99998 — a recycled pid is
	// plausible within hours, and a recycled pid existing again proves
	// nothing about the session it once named. getsid(pid) == pid asks the
	// question that matters: is this pid a session leader at all. A
	// recycled pid is almost never itself a session leader, so this rejects
	// nearly all false "alive" verdicts at no cost.
	pidAlive = func(pid int) bool {
		sid, err := unix.Getsid(pid)
		if err != nil {
			// Verified with a C program against macOS's getsid(2): it has no
			// permission check at all — it returns the real sid regardless of
			// caller/target ownership, and its man page lists only ESRCH.
			// Modern Linux behaves the same, so in practice this branch is
			// unreachable on either of this package's tested platforms. It is
			// kept, not because EPERM is expected here, but as a defensive
			// fallback for a getsid(2) on some other unix in the build tag
			// (freebsd, netbsd, openbsd, dragonfly) that does document one:
			// read the process as alive rather than let an unexpected errno
			// masquerade as ESRCH and wrongly declare a live session dead.
			return errors.Is(err, syscall.EPERM)
		}
		return sid == pid
	}
)

// sessionIdentity returns this session's derived identity, or "" when the
// kernel or the hostname cannot answer. Returning "" is deliberate: it is the
// behaviour of a session with no identity, which is what this code replaces —
// so a failure here can never be worse than the status quo.
func sessionIdentity() string {
	sid, err := getsid()
	if err != nil || sid <= 0 {
		return ""
	}
	host, err := hostname()
	if err != nil || host == "" {
		return ""
	}
	return fmt.Sprintf("%s%d@%s", sessionPrefix, sid, host)
}

// OwnerAlive reports whether an owner marker still names a live session.
//
// Only a derived sid:<n>@<host> identity can be interrogated. Everything else
// — a cmux workspace id, an explicit PIGTAIL_OWNER, a marker written before
// this existed — is opaque and reported alive, which keeps a spared server
// spared: the safe direction when we cannot know.
//
// A dead owner matters because giving every session an identity would empty
// the "unclaimed" bucket, and with it strip `stop --all` of its power to reap
// forgotten servers. Liveness is what keeps that bucket populated: a server
// whose session is gone is exactly a forgotten server.
func OwnerAlive(owner string) bool {
	sid, host, ok := parseSessionIdentity(owner)
	if !ok {
		return true
	}
	me, err := hostname()
	if err != nil || me != host {
		// Another machine's session: neither interrogable nor stoppable from
		// here, so it is not ours to reclaim.
		return true
	}
	return pidAlive(sid)
}

// parseSessionIdentity splits a derived identity. Pure. ok is false for every
// other shape, including a malformed one — an owner we cannot parse is an
// owner we must not interrogate.
func parseSessionIdentity(owner string) (sid int, host string, ok bool) {
	rest, found := strings.CutPrefix(owner, sessionPrefix)
	if !found {
		return 0, "", false
	}
	num, host, found := strings.Cut(rest, "@")
	if !found || host == "" {
		return 0, "", false
	}
	sid, err := strconv.Atoi(num)
	if err != nil || sid <= 0 {
		return 0, "", false
	}
	return sid, host, true
}

//go:build unix

package devserver

import (
	"errors"
	"os/exec"
	"testing"
)

// TestSessionIdentityFormat: a session identity is the POSIX session id and
// the host, in a form OwnerAlive can parse back.
func TestSessionIdentityFormat(t *testing.T) {
	restore := stubSession(t, 4711, "laptop", nil)
	defer restore()
	if got := sessionIdentity(); got != "sid:4711@laptop" {
		t.Fatalf("sessionIdentity = %q, want sid:4711@laptop", got)
	}
}

// TestSessionIdentityEmptyOnFailure: when the kernel or the hostname cannot
// answer, we return "" — the exact pre-existing behaviour of a session with
// no identity, so this fallback can never be worse than what it replaces.
func TestSessionIdentityEmptyOnFailure(t *testing.T) {
	restore := stubSession(t, 0, "laptop", errors.New("boom"))
	defer restore()
	if got := sessionIdentity(); got != "" {
		t.Fatalf("sessionIdentity on getsid error = %q, want empty", got)
	}
	restore()

	restore = stubSession(t, 4711, "", nil)
	defer restore()
	if got := sessionIdentity(); got != "" {
		t.Fatalf("sessionIdentity on empty hostname = %q, want empty", got)
	}
}

// TestOwnerIDPrefersEnvOverSession: the session fallback is the fourth and
// last notch, never a competitor to an explicit identity or a worktree's
// recorded one. The worktree here has no .env.local, so the third cran stays
// empty and falls through to the stubbed session.
func TestOwnerIDPrefersEnvOverSession(t *testing.T) {
	restore := stubSession(t, 4711, "laptop", nil)
	defer restore()
	dir := t.TempDir()

	t.Setenv("CMUX_WORKSPACE_ID", "ws-cmux")
	t.Setenv("PIGTAIL_OWNER", "ws-env")
	if got := OwnerID(dir); got != "ws-cmux" {
		t.Fatalf("OwnerID = %q, want ws-cmux", got)
	}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	if got := OwnerID(dir); got != "ws-env" {
		t.Fatalf("OwnerID = %q, want ws-env", got)
	}

	t.Setenv("PIGTAIL_OWNER", "")
	if got := OwnerID(dir); got != "sid:4711@laptop" {
		t.Fatalf("OwnerID = %q, want sid:4711@laptop", got)
	}
}

// TestOwnerAlive covers the three forms of the design's liveness table.
func TestOwnerAlive(t *testing.T) {
	origHostname, origPidAlive := hostname, pidAlive
	defer func() { hostname, pidAlive = origHostname, origPidAlive }()
	hostname = func() (string, error) { return "laptop", nil }
	pidAlive = func(pid int) bool { return pid == 4711 }

	cases := []struct {
		name  string
		owner string
		want  bool
	}{
		{"live session on this host", "sid:4711@laptop", true},
		{"dead session on this host", "sid:9999@laptop", false},
		{"another host is never interrogated", "sid:9999@desktop", true},
		{"a cmux identity is opaque", "ws_e9fbd5", true},
		{"an empty owner is not a session", "", true},
		{"a malformed sid is opaque", "sid:notanumber@laptop", true},
		{"a sid with no host is opaque", "sid:4711", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnerAlive(tc.owner); got != tc.want {
				t.Fatalf("OwnerAlive(%q) = %v, want %v", tc.owner, got, tc.want)
			}
		})
	}
}

// TestPidAlive exercises the real (unstubbed) pidAlive seam — the piece
// finding 1 of the 2026-07-27 final review replaced. It used to be
// syscall.Kill(pid, 0) != ESRCH, i.e. mere existence. That is wrong for this
// package's purpose: an agent's shell tool setsid-detaches every command, so
// the sid recorded in a marker is a short-lived pid from the fast-churning
// end of the pid space, and macOS recycles pids within hours. A recycled pid
// existing is not evidence the *session* it once identified still exists.
// pidAlive now asks getsid(pid) == pid — is this pid a session leader at
// all — which a recycled pid almost never satisfies.
func TestPidAlive(t *testing.T) {
	t.Run("this session's own leader is alive", func(t *testing.T) {
		sid, err := getsid()
		if err != nil {
			t.Fatalf("getsid: %v", err)
		}
		if !pidAlive(sid) {
			t.Fatalf("pidAlive(%d) = false, want true (this session's own leader)", sid)
		}
	})

	t.Run("a reaped child pid is dead", func(t *testing.T) {
		cmd := exec.Command("true")
		if err := cmd.Run(); err != nil {
			t.Fatalf("run: %v", err)
		}
		pid := cmd.Process.Pid // cmd.Run already waited: reaped, guaranteed gone
		if pidAlive(pid) {
			t.Fatalf("pidAlive(%d) = true, want false (process exited and was reaped)", pid)
		}
	})

	t.Run("pid 1 is alive", func(t *testing.T) {
		// Does NOT exercise the EPERM branch: on macOS (and modern Linux)
		// Getsid(1) has no permission check and succeeds, so this goes
		// through the ordinary sid == pid path, same as any other live
		// session leader — pid 1 is its own. See the comment on the EPERM
		// branch in session.go. Kept as a smoke test that a well-known,
		// always-present pid reads alive.
		if !pidAlive(1) {
			t.Fatal("pidAlive(1) = false, want true (pid 1 always exists)")
		}
	})

	t.Run("a live process that never became a session leader is dead", func(t *testing.T) {
		// This is the case the fix exists for. The child below is genuinely
		// running — cmd.Start, no Wait yet — but it was forked without
		// setsid/Setpgid, so it inherits this test binary's session rather
		// than leading its own: getsid(child) != child. A recycled marker
		// pid is in exactly this shape (alive, not a leader), which is why
		// existence alone (the old syscall.Kill-based check) was the wrong
		// test: it would report this live-but-non-leader process alive too.
		cmd := exec.Command("sleep", "5")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}()
		if pidAlive(cmd.Process.Pid) {
			t.Fatalf("pidAlive(%d) = true, want false (running but not a session leader)", cmd.Process.Pid)
		}
	})
}

// stubSession replaces the syscall seams for one test and returns a restore
// func. Idempotent: calling the restore twice is harmless.
func stubSession(t *testing.T, sid int, host string, err error) func() {
	t.Helper()
	origGetsid, origHostname := getsid, hostname
	getsid = func() (int, error) { return sid, err }
	hostname = func() (string, error) { return host, nil }
	return func() { getsid, hostname = origGetsid, origHostname }
}

package store

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// This file is the probe that offsite_fallback.go's doc comment promises: it
// is the only thing in the package that actually reaches the far side over
// the pinned route, and it proves three things a ping never would —
// the route, the authentication, and a real, writable directory — by
// creating and removing one marker file there. See that file for the
// classification built on top of what this reports, and offsite_state.go's
// FallbackDest..FallbackVia fields for where the result is kept.

// outputRunner executes a command under a context and returns its stdout.
// A field rather than a direct exec call so the probe can be tested without
// an SSH server — the same seam as offsite_remote.go's runner, except this
// one carries a return value: the probe's result *is* a value read back
// from the far side, not just a pass/fail.
type outputRunner func(ctx context.Context, name string, args ...string) (string, error)

// execOutputRunner runs one command and captures stdout alone.
//
// Standard error is collected separately and only ever feeds the error
// message. Folding it into the same buffer, the way offsite_remote.go's
// execRunner does with CombinedOutput, would let a stray line on stderr —
// a MOTD, a warning from the remote shell — corrupt the address this probe
// reports as `via`, a value that ends up rendered to a person
// (RenderOffsiteFallbackDetail), not just logged.
func execOutputRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is a package constant, args are built here
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s timed out after %s: %w", name, offsiteFallbackProbeTimeout, ctx.Err())
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, msg)
	}
	return string(out), nil
}

// offsiteFallbackProbeScript builds the far-side probe: clear any debris,
// write the marker, remove it, then report the address the connection
// arrived from.
//
// Deliberately POSIX, on the same model as offsite_remote.go's
// remoteRotateScript: this repo has already been bitten by a script that
// was green on macOS's bash and red on Linux's dash. Not because of what
// this particular NAS's /bin/sh happens to be — measured once, it is
// itself a symlink to bash, so a bashism would run here without ever
// failing — but because the pinned fallback alias is not the only
// destination this script has to survive, on this NAS or a future one, and
// nothing here can tell in advance which /bin/sh a given destination
// actually resolves to.
//
// The `rm -f` in front, before anything is written, is what makes running
// this twice safe. Without it, a marker orphaned by an exercise killed
// mid-run would sit there forever — nothing else on this route ever removes
// it — and every later exercise would fail on a file that isn't debris from
// *this* run, it's debris from the last one.
//
// The script never creates dir itself: no `mkdir -p`. That mirrors
// property 1 in offsite.go (same file, near the top): `mkdir -p` on an
// absent mount succeeds by creating the directory on whatever local disk the
// alias actually resolves to, which would make a probe over a dead route
// report success. Requiring dir to pre-exist is what makes "unreachable"
// read as unreachable.
func offsiteFallbackProbeScript(dir string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "d=%s\n", shellQuote(dir))
	fmt.Fprintf(&b, "f=\"$d\"/%s\n", offsiteFallbackProbeFile)
	b.WriteString("rm -f \"$f\"\n")
	b.WriteString(": > \"$f\"\n")
	b.WriteString("rm -f \"$f\"\n")
	b.WriteString(`printf %s "${SSH_CLIENT%% *}"` + "\n")
	return b.String()
}

// ExerciseOffsiteFallback runs the probe against dest — a
// [user@]host:/path destination, the same shape ParseRemoteDest accepts for
// the primary offsite route, and in practice a pinned SSH alias that
// ~/.ssh/config refuses to fall back to the LAN with — and reports the
// address the far side saw the connection arrive from.
//
// The returned via is validated as an IP address before it is handed back;
// see exerciseOffsiteFallback for why, and see OffsiteState.FallbackVia's
// doc comment (offsite_state.go) for why "127.0.0.1" is the value a healthy
// exercise over a userspace-networking VPN actually reports, not a bug.
func ExerciseOffsiteFallback(ctx context.Context, dest string) (string, error) {
	return exerciseOffsiteFallback(ctx, dest, execOutputRunner)
}

// exerciseOffsiteFallback is ExerciseOffsiteFallback with the transport
// injected, on the same model as offsite_remote.go's
// backupOffsiteRemote/runner: it lets the probe be driven and timed without
// an SSH server.
func exerciseOffsiteFallback(ctx context.Context, dest string, run outputRunner) (string, error) {
	host, path, ok := ParseRemoteDest(dest)
	if !ok {
		return "", fmt.Errorf("exercise offsite fallback: %q is not a [user@]host:/path destination", dest)
	}

	// One deadline for the one round trip this makes. A probe with no bound
	// could hang exactly the way the remote backup's unbounded ssh did
	// before offsite_remote.go's ConnectTimeout/BatchMode hardening: since
	// only one exercise is ever in flight, a hang here means no exercise
	// ever runs again until the daemon restarts.
	ctx, cancel := context.WithTimeout(ctx, offsiteFallbackProbeTimeout)
	defer cancel()

	args := append(append([]string{}, sshHardening...), host, offsiteFallbackProbeScript(path))
	out, err := run(ctx, "ssh", args...)
	if err != nil {
		return "", fmt.Errorf("exercise fallback route via %s: %w", host, err)
	}

	// The far side's stdout is trusted only as far as net.ParseIP trusts
	// it: this value ends up rendered to a person
	// (RenderOffsiteFallbackDetail), so a malformed SSH_CLIENT, an empty
	// one, or an unrelated line on stdout reads as "nothing usable" rather
	// than passing through verbatim. "127.0.0.1" is not a case that rule
	// singles out — it parses like any other address and is returned like
	// any other address; it just happens to be the *healthy* reading on a
	// NAS whose VPN never binds a kernel interface.
	via := strings.TrimSpace(out)
	if net.ParseIP(via) == nil {
		return "", nil
	}
	return via, nil
}

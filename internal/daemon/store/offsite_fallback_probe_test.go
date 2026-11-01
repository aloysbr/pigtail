package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The probe is the one part of the fallback exercise that can be proven
// without a NAS: it is a shell script, and /bin/sh will run it against a
// real directory exactly as ssh would run it on the far side. These tests
// exercise the script directly — no network, no fake runner — because that
// is the only way to catch a script bug that a mocked ssh call would hide.

func TestOffsiteFallbackProbeScriptRunsForReal(t *testing.T) {
	dir := t.TempDir()
	script := offsiteFallbackProbeScript(dir)

	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("script failed on a real, writable directory: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, offsiteFallbackProbeFile)); !os.IsNotExist(err) {
		t.Fatalf("probe marker survived the script (stat err = %v); the exercise must leave nothing behind", err)
	}
}

func TestOffsiteFallbackProbeScriptFailsOnMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	script := offsiteFallbackProbeScript(missing)

	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err == nil {
		t.Fatalf("script succeeded against a directory nobody created: %s\nan mkdir -p would make an absent mount read as reachable", out)
	}
}

func TestOffsiteFallbackProbeScriptFailsOnReadOnlyDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // let TempDir's own cleanup remove it
	script := offsiteFallbackProbeScript(dir)

	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err == nil {
		t.Fatalf("script succeeded against a read-only directory: %s", out)
	}
}

// TestOffsiteFallbackProbeScriptClearsDebrisFromAKilledRun is the regression
// test for the leading rm -f: without it, a marker orphaned by an exercise
// that was killed mid-run would block every later one. A read-only leftover
// file makes that concrete — ": > \"$f\"" alone cannot touch it, only an rm
// first can.
func TestOffsiteFallbackProbeScriptClearsDebrisFromAKilledRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, offsiteFallbackProbeFile)
	if err := os.WriteFile(marker, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	script := offsiteFallbackProbeScript(dir)

	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("script failed to clear debris left by a killed exercise: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("probe marker survived the script (stat err = %v)", err)
	}
}

// The far side's /bin/sh is dash or ash, not bash — this repo has already
// been bitten by that split (remoteRotateScript's own tests, offsite_remote_test.go:58).
func TestOffsiteFallbackProbeScriptIsPOSIX(t *testing.T) {
	script := offsiteFallbackProbeScript("/srv/backups")
	for _, bashism := range []string{"[[", "]]", "local ", "function ", "==", "&>"} {
		if strings.Contains(script, bashism) {
			t.Errorf("probe script contains the bashism %q; the NAS runs dash/ash:\n%s", bashism, script)
		}
	}
}

// A destination with a space or a quote must not break the remote shell
// apart. Escaped with shellQuote — the package's own utility, not a
// bespoke one — same contract as remoteRotateScript's own quoting test.
func TestOffsiteFallbackProbeScriptQuotesThePath(t *testing.T) {
	script := offsiteFallbackProbeScript(`/srv/my backups/back'ups`)
	if strings.Contains(script, `/srv/my backups/back'ups`) {
		t.Fatalf("path interpolated raw into the remote shell:\n%s", script)
	}
	if !strings.Contains(script, `'/srv/my backups/back'"'"'ups'`) {
		t.Fatalf("path is not single-quote escaped:\n%s", script)
	}
}

// The probe writes and removes exactly one marker file. It must never touch
// anything shaped like a backup generation, and never create the directory
// itself — that second rule is what TestOffsiteFallbackProbeScriptFailsOnMissingDir
// pins down at runtime; this one pins the script text so an mkdir cannot
// sneak back in as a no-op-looking edit.
func TestOffsiteFallbackProbeScriptTouchesNothingElse(t *testing.T) {
	script := offsiteFallbackProbeScript("/srv/backups")
	for _, forbidden := range []string{"mkdir", "rsync", "pigtail.db.0", "pigtail.db.1", "pigtail.db.2"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("probe script mentions %q; it must only touch its own marker file:\n%s", forbidden, script)
		}
	}
}

// One ssh round trip, hardened the same way the remote backup's ssh calls
// are (offsite_remote.go's sshHardening), and bounded by a context that
// actually carries a deadline — same motif as
// TestRemoteBackupBoundsEverySubprocess (offsite_remote_test.go:215): a hung
// ssh with no deadline would stall every later exercise, silently.
func TestExerciseOffsiteFallbackMakesOneHardenedSSHCall(t *testing.T) {
	var calls [][]string
	run := func(ctx context.Context, name string, args ...string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("%s ran on a context with no deadline: it could hang forever", name)
		}
		calls = append(calls, append([]string{name}, args...))
		return "100.64.0.7", nil
	}

	if _, err := exerciseOffsiteFallback(context.Background(), "backup:/srv/backups", run); err != nil {
		t.Fatalf("exerciseOffsiteFallback: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("made %d calls, want exactly one ssh round trip: %v", len(calls), calls)
	}
	if calls[0][0] != "ssh" {
		t.Errorf("call is %q, want ssh", calls[0][0])
	}
	joined := strings.Join(calls[0], " ")
	if !strings.Contains(joined, "ConnectTimeout=") {
		t.Errorf("ssh has no ConnectTimeout: %s", joined)
	}
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Errorf("ssh may prompt, and a daemon under launchd cannot answer: %s", joined)
	}
}

func TestExerciseOffsiteFallbackRejectsMalformedDest(t *testing.T) {
	called := false
	run := func(context.Context, string, ...string) (string, error) {
		called = true
		return "", nil
	}
	if _, err := exerciseOffsiteFallback(context.Background(), "/not/a/remote/spec", run); err == nil {
		t.Fatal("a malformed destination was not reported")
	}
	if called {
		t.Fatal("ssh was invoked against a destination that never parsed")
	}
}

// TestExerciseOffsiteFallbackExtractsViaFromSSHClient runs the script for
// real (no ssh, no fake runner) with SSH_CLIENT set the way sshd sets it on
// the far side — "<client-ip> <client-port> <server-port>" — to prove the
// script's own ${SSH_CLIENT%% *} keeps only the address, not the whole
// triplet.
func TestExerciseOffsiteFallbackExtractsViaFromSSHClient(t *testing.T) {
	dir := t.TempDir()
	script := offsiteFallbackProbeScript(dir)

	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "SSH_CLIENT=100.64.0.7 51234 22")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	if got := string(out); got != "100.64.0.7" {
		t.Errorf("script printed %q, want just the client address", got)
	}
}

func TestExerciseOffsiteFallbackRejectsNonIPOutput(t *testing.T) {
	run := func(context.Context, string, ...string) (string, error) { return "not-an-ip", nil }
	via, err := exerciseOffsiteFallback(context.Background(), "nas:/x", run)
	if err != nil {
		t.Fatalf("exerciseOffsiteFallback: %v", err)
	}
	if via != "" {
		t.Errorf("via = %q for a non-IP output, want empty: arbitrary remote text must not reach a rendered field", via)
	}
}

func TestExerciseOffsiteFallbackEmptyWhenSSHClientIsAbsent(t *testing.T) {
	run := func(context.Context, string, ...string) (string, error) { return "", nil }
	via, err := exerciseOffsiteFallback(context.Background(), "nas:/x", run)
	if err != nil {
		t.Fatalf("exerciseOffsiteFallback: %v", err)
	}
	if via != "" {
		t.Errorf("via = %q with no SSH_CLIENT, want empty", via)
	}
}

// 127.0.0.1 is not a degenerate case to reject — see
// OffsiteState.FallbackVia's doc comment (offsite_state.go): on a NAS
// running its VPN in userspace networking, the loopback address is exactly
// what a probe that genuinely went over the pinned route reports.
func TestExerciseOffsiteFallbackAcceptsLoopbackAsValid(t *testing.T) {
	run := func(context.Context, string, ...string) (string, error) { return "127.0.0.1", nil }
	via, err := exerciseOffsiteFallback(context.Background(), "nas:/x", run)
	if err != nil {
		t.Fatalf("exerciseOffsiteFallback: %v", err)
	}
	if via != "127.0.0.1" {
		t.Errorf("via = %q, want 127.0.0.1 accepted as a valid, healthy reading", via)
	}
}

// execOutputRunner is the one place stdout and stderr must stay apart: the
// former becomes `via`, a value that gets rendered to a person, and the
// latter only ever feeds an error string.
func TestExecOutputRunnerCapturesStdoutOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := execOutputRunner(ctx, "/bin/sh", "-c", "echo out; echo err >&2")
	if err != nil {
		t.Fatalf("execOutputRunner: %v", err)
	}
	if strings.TrimSpace(out) != "out" {
		t.Errorf("stdout = %q, want just the stdout line: stderr must not be merged in", out)
	}
}

func TestExecOutputRunnerReportsStderrOnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := execOutputRunner(ctx, "/bin/sh", "-c", "echo boom >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("execOutputRunner error = %v, want the stderr text surfaced", err)
	}
}

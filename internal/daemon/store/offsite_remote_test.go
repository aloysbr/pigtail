package store

import (
	"context"
	"strings"
	"testing"
)

// A mounted network share was the first offsite destination, and it cost 38
// minutes to copy 768 KiB the first time because the mount had to
// wake up — during which nothing could say what was happening. The same file
// over rsync/ssh to the same NAS took **0.46 seconds**.
//
// The gain is not speed for its own sake. A remote destination has no mount to
// keep alive: nothing to remount after a reboot, nothing to wake, and over a
// mesh VPN it works away from the LAN too. What it costs is a shell command on
// the far side, which is what these tests pin down.

func TestParseRemoteDestRecognisesTheRsyncForm(t *testing.T) {
	cases := []struct {
		in         string
		wantRemote bool
		host, path string
	}{
		{"backup:/srv/backups/backups", true, "backup", "/srv/backups/backups"},
		{"ada@nas:/srv/x", true, "ada@nas", "/srv/x"},
		{"nas:relative/path", true, "nas", "relative/path"},

		// Local paths, including the ones that contain a colon. A leading
		// slash settles it: a remote spec never starts with one.
		{"/Volumes/drive/pigtail-backups", false, "", ""},
		{"/Volumes/weird:name/backups", false, "", ""},
		{"./relative", false, "", ""},
		{"", false, "", ""},

		// Degenerate forms that must not be mistaken for a host.
		{":/no/host", false, "", ""},
		{"host:", false, "", ""},
	}
	for _, c := range cases {
		host, path, ok := ParseRemoteDest(c.in)
		if ok != c.wantRemote {
			t.Errorf("ParseRemoteDest(%q) remote = %v, want %v", c.in, ok, c.wantRemote)
			continue
		}
		if ok && (host != c.host || path != c.path) {
			t.Errorf("ParseRemoteDest(%q) = (%q, %q), want (%q, %q)", c.in, host, path, c.host, c.path)
		}
	}
}

// The rotation runs on the NAS, whose /bin/sh is not bash. The repo has already
// been bitten by that (a test green on macOS, red on CI's dash), so the script
// stays POSIX: no [[ ]], no arrays, no `local`.
func TestRemoteRotateScriptIsPOSIX(t *testing.T) {
	script := remoteRotateScript("/srv/backups/backups", "pigtail.db", 3)

	for _, bashism := range []string{"[[", "]]", "local ", "function ", "==", "&>"} {
		if strings.Contains(script, bashism) {
			t.Errorf("rotation script contains the bashism %q; the NAS runs dash/ash:\n%s", bashism, script)
		}
	}
	// Oldest dropped, each generation shifted down, slot 0 left free — the
	// same contract as the local rotateBackups.
	for _, want := range []string{"pigtail.db.2", "pigtail.db.1", "pigtail.db.0"} {
		if !strings.Contains(script, want) {
			t.Errorf("rotation script never mentions %s:\n%s", want, script)
		}
	}
	if !strings.Contains(script, "rm -f") {
		t.Errorf("rotation script never drops the oldest generation:\n%s", script)
	}
	// A missing generation is normal on the first runs and must not fail the
	// script, exactly as os.IsNotExist is tolerated locally.
	if !strings.Contains(script, "[ -f ") {
		t.Errorf("rotation script does not guard against a missing generation:\n%s", script)
	}
}

// A path with a space or a quote must not break the remote shell apart.
func TestRemoteRotateScriptQuotesThePath(t *testing.T) {
	script := remoteRotateScript("/srv/my backups/back'ups", "pigtail.db", 3)
	if strings.Contains(script, "/srv/my backups/back'ups") {
		t.Fatalf("path interpolated raw into the remote shell:\n%s", script)
	}
	if !strings.Contains(script, `'/srv/my backups/back'"'"'ups'`) {
		t.Fatalf("path is not single-quote escaped:\n%s", script)
	}
}

// One round trip for the rotation, one for the copy. The point of moving off a
// mount is to stop paying per-operation latency, so a rotation that shelled out
// three times would give some of that back.
func TestRemoteBackupRunsOneRotationThenOneCopy(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)

	var calls [][]string
	runner := func(_ context.Context, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	if err := backupOffsiteRemote(db, "backup", "/srv/backups/backups", BackupGenerations, runner); err != nil {
		t.Fatalf("backupOffsiteRemote: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("made %d calls, want 2 (one ssh rotation, one rsync):\n%v", len(calls), calls)
	}
	if calls[0][0] != "ssh" {
		t.Errorf("first call is %q, want ssh (rotation before copy)", calls[0][0])
	}
	if calls[1][0] != "rsync" {
		t.Errorf("second call is %q, want rsync", calls[1][0])
	}

	rsyncArgs := strings.Join(calls[1], " ")
	// The destination generation is .0, like the local rotation leaves free,
	// and the path is passed **raw**. Shell-quoting it here was a real bug
	// shipped in #79 and caught in production: rsync hands the remote path to a
	// shell of its own, so the quotes arrived as literal characters and the
	// path was then read as relative to the login home —
	//   change_dir "/srv/homes/user//'/srv/backups"
	// The first version of this test asserted the quoted form, so it encoded
	// the bug instead of catching it.
	if !strings.Contains(rsyncArgs, "backup:/srv/backups/backups/pigtail.db.0") {
		t.Errorf("rsync does not target the freed .0 slot: %s", rsyncArgs)
	}
	if strings.Contains(rsyncArgs, "'") {
		t.Errorf("rsync destination is shell-quoted; the quotes reach the remote path verbatim: %s", rsyncArgs)
	}
	// --protect-args (-s) is what actually makes spaces safe: it stops rsync
	// from running the remote path through a shell at all, which is why no
	// quoting is needed — or wanted.
	if !strings.Contains(rsyncArgs, " -s ") {
		t.Errorf("rsync lacks --protect-args (-s); a path with a space would split remotely: %s", rsyncArgs)
	}
	// A copy must not hang forever on a dead link the way the mount did.
	if !strings.Contains(rsyncArgs, "--timeout") {
		t.Errorf("rsync has no timeout; a dead link would hang the attempt: %s", rsyncArgs)
	}
	if !strings.Contains(rsyncArgs, db+".0") {
		t.Errorf("rsync does not send the newest local generation: %s", rsyncArgs)
	}
}

// A failing rotation must stop the sequence: copying into a directory whose
// generations were not shifted would overwrite .0 while .1 still holds an older
// snapshot than it should.
func TestRemoteBackupStopsWhenRotationFails(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)

	var calls int
	runner := func(_ context.Context, name string, args ...string) error {
		calls++
		if name == "ssh" {
			return errNoHost
		}
		return nil
	}
	if err := backupOffsiteRemote(db, "nas", "/x", BackupGenerations, runner); err == nil {
		t.Fatal("a failed rotation was not reported")
	}
	if calls != 1 {
		t.Fatalf("made %d calls after a failed rotation, want 1 (no copy)", calls)
	}
}

// Same rule as the local path: nothing backed up yet is not an error to report
// every hour, it is simply nothing to send.
func TestRemoteBackupIsQuietWithNoLocalGeneration(t *testing.T) {
	dir := t.TempDir()
	db := seededStore(t, dir+"/pigtail.db").path
	var calls int
	runner := func(context.Context, string, ...string) error { calls++; return nil }

	if err := backupOffsiteRemote(db, "nas", "/x", BackupGenerations, runner); err != nil {
		t.Fatalf("backupOffsiteRemote with no local generation: %v", err)
	}
	if calls != 0 {
		t.Fatalf("made %d calls with nothing to copy, want 0", calls)
	}
}

// BackupOffsite is the single entry point; the transport is chosen from the
// destination's shape so the daemon and the flag stay unaware of it.
func TestBackupOffsiteRoutesRemoteDestinations(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)

	// A remote destination must not be treated as a local directory — which
	// would report it as unreachable, since "nas:/srv" does not exist here.
	err := BackupOffsite(db, "backup:/srv/backups/backups", BackupGenerations)
	if err != nil && strings.Contains(err.Error(), ErrOffsiteUnreachable.Error()) {
		t.Fatalf("a remote destination was stat'ed as a local path: %v", err)
	}
}

var errNoHost = &permanentErr{"no route to host"}

type permanentErr struct{ msg string }

func (e *permanentErr) Error() string { return e.msg }

// TestRemoteBackupBoundsEverySubprocess is the regression test for "the offsite
// ssh has no timeout".
//
// The mounted-volume path genuinely cannot be cancelled — it blocks inside the
// kernel — and the comment in daemon/offsite.go generalised that to the whole
// feature. It does not hold for ssh and rsync: those are subprocesses, and a
// context kills them. The consequence of not bounding them is not one slow
// hour: only one attempt is ever in flight, so an ssh that hangs means no
// offsite copy ever runs again until the daemon restarts, silently.
func TestRemoteBackupBoundsEverySubprocess(t *testing.T) {
	dir := t.TempDir()
	db := seededWithGeneration(t, dir)

	var calls [][]string
	runner := func(ctx context.Context, name string, args ...string) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("%s ran on a context with no deadline: it could hang forever", name)
		}
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	if err := backupOffsiteRemote(db, "nas", "/x", BackupGenerations, runner); err != nil {
		t.Fatalf("backupOffsiteRemote: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("want one ssh and one rsync, got %v", calls)
	}

	// The handshake needs its own bound: rsync's --timeout is an I/O timeout
	// and only starts counting once bytes move, so a host that accepts the
	// connection and then goes quiet is not covered by it.
	ssh := strings.Join(calls[0], " ")
	if !strings.Contains(ssh, "ConnectTimeout=") {
		t.Errorf("the rotation ssh has no ConnectTimeout: %s", ssh)
	}
	// Under launchd there is no terminal, so a password or host-key prompt
	// does not fail — it waits for an answer that can never arrive.
	if !strings.Contains(ssh, "BatchMode=yes") {
		t.Errorf("the rotation ssh may prompt, and a daemon cannot answer: %s", ssh)
	}
	if rsync := strings.Join(calls[1], " "); !strings.Contains(rsync, "ConnectTimeout=") {
		t.Errorf("rsync's ssh transport is unbounded on connect: %s", rsync)
	}
}

package devserver

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// worktreeEntries lists what a worktree directory contains, so a test can
// assert on the ONE property this whole change exists for: pigtail adds
// nothing to it.
func worktreeEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestStartingAServerLeavesTheWorktreeUntouched is the regression this PR
// exists to make impossible. A background launch used to drop
// `.pigtail-<server>.log` (plus its rotated `.prev`) and
// `.pigtail-<server>.owner` — and, during the atomic write, a
// `.pigtail-<server>.owner.tmp-*` — at the worktree root, where `git add -A`
// commits them. Two consumer-repo commits did exactly that.
//
// Asserted on the directory listing rather than on known filenames: the point
// is not that today's three artifacts moved, it is that the class stopped
// growing. A fourth artifact added at the worktree root fails this test.
func TestStartingAServerLeavesTheWorktreeUntouched(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".env.local"), []byte("PORT=1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	// Two rounds: the second exercises the log rotation (.log → .log.prev),
	// the artifact an earlier .gitignore pattern missed.
	for range 2 {
		if err := runBackground(wt, "dev", "sleep 0.2", 1234); err != nil {
			t.Fatal(err)
		}
		if err := writeOwner(wt, "dev", "ws-a"); err != nil {
			t.Fatal(err)
		}
	}
	clearOwner(wt, "dev")

	if got := worktreeEntries(t, wt); len(got) != 1 || got[0] != ".env.local" {
		t.Fatalf("pigtail wrote into the worktree: %v (want only .env.local)", got)
	}
	// …and the state actually went somewhere.
	if _, err := os.Stat(LogPath(wt, "dev")); err != nil {
		t.Fatalf("the log must exist in the state dir: %v", err)
	}
	if _, err := os.Stat(LogPath(wt, "dev") + ".prev"); err != nil {
		t.Fatalf("the rotated log must exist in the state dir: %v", err)
	}
}

// TestWriteOwnerWorksInAReadOnlyWorktree pins constraint 3 from the other
// side. The atomic write is a temp file + os.Rename, which is only atomic
// WITHIN one filesystem — so the temp file has to be created in the
// destination directory (the state dir), never in the worktree and moved
// across. A worktree the process cannot write to at all proves it: the write
// must still succeed, which it cannot if anything is staged there first.
func TestWriteOwnerWorksInAReadOnlyWorktree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	wt := t.TempDir()
	if err := os.Chmod(wt, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(wt, 0o700) }()

	if err := writeOwner(wt, "dev", "ws-a"); err != nil {
		t.Fatalf("writeOwner must not need the worktree to be writable: %v", err)
	}
	if got := ReadOwner(wt, "dev"); got != "ws-a" {
		t.Fatalf("ReadOwner = %q, want ws-a", got)
	}
	if dir := filepath.Dir(OwnerPath(wt, "dev")); dir != StateDir(wt) {
		t.Fatalf("marker lives in %q, want the state dir %q", dir, StateDir(wt))
	}
	// No temp file survives a successful write.
	leftovers, _ := filepath.Glob(filepath.Join(StateDir(wt), "*.tmp-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// TestLegacyMarkerIsReadWhenTheStateDirIsEmpty covers the upgrade moment: a
// server started by the previous version has its marker at the worktree root
// and nothing in the state dir. Without this fallback it would read as an
// orphan — i.e. as a forgotten server — and the first `pigtail stop --all`
// after the upgrade would reap a colleague's running dev server.
func TestLegacyMarkerIsReadWhenTheStateDirIsEmpty(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(legacyOwnerPath(wt, "dev"), []byte("ws-legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadOwner(wt, "dev"); got != "ws-legacy" {
		t.Fatalf("ReadOwner = %q, want ws-legacy — a running server just became an orphan", got)
	}
}

// TestNewMarkerShadowsTheLegacyOneWithoutTouchingIt is the migration, and the
// deliberate deviation from a move: the legacy marker is superseded on read,
// never deleted.
//
// Deleting it is precisely the incident. In the consumer repo the marker had
// been committed on four branches, so `pigtail stop --all` removing it showed
// up as a deleted TRACKED file in two other sessions' worktrees. A migration
// that deletes would reproduce that on the very first `pigtail dev` after the
// upgrade. Shadowing gives the same observable behaviour (the state dir always
// wins) with pigtail never writing to, nor deleting from, the worktree again;
// the leftover file goes away on its own when the branch is next rebased.
func TestNewMarkerShadowsTheLegacyOneWithoutTouchingIt(t *testing.T) {
	wt := t.TempDir()
	legacy := legacyOwnerPath(wt, "dev")
	if err := os.WriteFile(legacy, []byte("ws-legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(legacy, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeOwner(wt, "dev", "ws-new"); err != nil {
		t.Fatal(err)
	}
	if got := ReadOwner(wt, "dev"); got != "ws-new" {
		t.Fatalf("ReadOwner = %q, want ws-new — the state dir must win once it holds a marker", got)
	}

	// A stop clears the state-dir marker; the worktree is still not touched.
	clearOwner(wt, "dev")

	after, err := os.Stat(legacy)
	if err != nil {
		t.Fatalf("the legacy marker must be left in place, stat err = %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("the legacy marker was modified — pigtail must not write in the worktree")
	}
	b, err := os.ReadFile(legacy)
	if err != nil || strings.TrimSpace(string(b)) != "ws-legacy" {
		t.Fatalf("legacy marker content = %q (err %v), want ws-legacy", b, err)
	}
}

// TestStopAllSparesAServerClaimedByALegacyMarker is the migration path end to
// end, on the sweep that matters: a real listener, a marker written only at
// the old location by the previous version, and a blanket `stop --all` from
// another session. The server must be spared exactly as if the marker were in
// the state dir — "a running server whose marker moves must not become an
// orphan that the next --all reaps".
func TestStopAllSparesAServerClaimedByALegacyMarker(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()
	port := freeTestPort(t)
	dir := mkFakeWorktreeDir(t, container, "wt-legacy", port)
	if err := os.WriteFile(legacyOwnerPath(dir, "dev"), []byte("ws-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	spawnPortListener(t, port, dir)
	waitPortBusyForTest(t, port)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeOwned); err != nil {
		t.Fatal(err)
	}

	if busy, _, _ := IsPortBusy(port); !busy {
		t.Fatal("a server claimed by a legacy marker must be spared by a ScopeOwned StopAll")
	}
	if report := buf.String(); !strings.Contains(report, "kept dev (owned by ws-b)") {
		t.Fatalf("expected the legacy-claimed server to be reported as kept, got: %s", report)
	}
	if _, err := os.Stat(legacyOwnerPath(dir, "dev")); err != nil {
		t.Fatalf("the legacy marker of a spared server must remain untouched, stat err = %v", err)
	}
}

// TestStopEverywhereStillSeizesALegacyClaimedServer: --everywhere is the
// unguarded form, and the migration must not accidentally harden it. Same
// setup as above, one scope up.
func TestStopEverywhereStillSeizesALegacyClaimedServer(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}

	container := t.TempDir()
	port := freeTestPort(t)
	dir := mkFakeWorktreeDir(t, container, "wt-legacy-everywhere", port)
	if err := os.WriteFile(legacyOwnerPath(dir, "dev"), []byte("ws-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	spawnPortListener(t, port, dir)
	waitPortBusyForTest(t, port)

	cfg := &pigtail.ProjectConfig{
		Servers: map[string]pigtail.ServerSpec{"dev": {PortKind: "dev"}},
		Ports:   map[string]pigtail.PortSpec{"dev": {Env: "PORT"}},
	}
	ctx := &Context{ContainerDir: container, Config: cfg}

	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("PIGTAIL_OWNER", "ws-a")

	origOut := out
	var buf bytes.Buffer
	out = &buf
	defer func() { out = origOut }()

	if err := StopAll(ctx, ScopeEverywhere); err != nil {
		t.Fatal(err)
	}
	waitPortFreeForTest(t, port)

	if report := buf.String(); !strings.Contains(report, "ws-b") {
		t.Fatalf("--everywhere must announce whose server it seized, got: %s", report)
	}
}

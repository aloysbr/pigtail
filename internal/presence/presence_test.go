package presence

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sid = "a3b30bdc-8192-4f0d-8c4a-fbad8cee4900"

func write(t *testing.T, dir, name, body string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestScanKeepsNewestPerSessionWithoutTTL(t *testing.T) {
	dir, wtA, wtB := t.TempDir(), t.TempDir(), t.TempDir()
	old := time.Now().Add(-72 * time.Hour) // far beyond the hooks' 15-min TTL
	write(t, dir, "claude-wt-aaaaaaaa-"+sid, wtA+"\nname=globex-f5\n", old.Add(-time.Hour))
	write(t, dir, "claude-wt-bbbbbbbb-"+sid, wtB+"\nname=globex-f5\n", old)
	r, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := r.Markers[sid]
	if !ok || m.Path != wtB || m.Gone || m.At.Sub(old).Abs() > time.Second {
		t.Fatalf("marker = %+v, ok=%v; want %s at %v", m, ok, wtB, old)
	}
}

func TestScanFoldsSubagentMarkers(t *testing.T) {
	dir, wt := t.TempDir(), t.TempDir()
	write(t, dir, "claude-wt-aaaaaaaa-"+sid+".a61fe560197ecf5f", wt+"\n", time.Now())
	r, _ := Scan(dir)
	if r.Markers[sid].Path != wt {
		t.Fatalf("subagent marker not folded into its session: %+v", r.Markers)
	}
}

// R1: the newest marker names a deleted worktree. It must be KEPT (flagged) so
// the store learns the session left its previous worktree; the display and the
// addressing hide it. Never fall back to the older, existing one.
func TestScanNewestGoneIsKeptAndFlagged(t *testing.T) {
	dir, wtA := t.TempDir(), t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-worktree")
	write(t, dir, "claude-wt-aaaaaaaa-"+sid, wtA+"\n", time.Now().Add(-time.Hour))
	write(t, dir, "claude-wt-bbbbbbbb-"+sid, gone+"\n", time.Now())
	r, _ := Scan(dir)
	m, ok := r.Markers[sid]
	if !ok || m.Path != gone || !m.Gone || r.Gone != 1 {
		t.Fatalf("marker = %+v ok=%v Gone=%d; want %s flagged gone, count 1", m, ok, r.Gone, gone)
	}
}

// R10: only the newest file per session is opened. The older one here has a
// broken first line: if it were read it would count as malformed.
func TestScanReadsOnlyNewestPerSession(t *testing.T) {
	dir, wt := t.TempDir(), t.TempDir()
	write(t, dir, "claude-wt-aaaaaaaa-"+sid, "not-an-absolute-path\n", time.Now().Add(-time.Hour))
	write(t, dir, "claude-wt-bbbbbbbb-"+sid, wt+"\n", time.Now())
	r, _ := Scan(dir)
	if r.Malformed != 0 || r.Markers[sid].Path != wt {
		t.Fatalf("older file was read: malformed=%d markers=%+v", r.Malformed, r.Markers)
	}
}

func TestScanCountsMalformed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "claude-wt-aaaaaaaa-"+sid, "relative/path\n", time.Now())
	write(t, dir, "claude-wt-nothex00-"+sid, "/x\n", time.Now())
	write(t, dir, "claude-wt-aaaaaaaa-", "/x\n", time.Now())
	write(t, dir, `claude-wt-e7ad2368-a b"c`, "/x\n", time.Now())  // the real leftover of 2026-09-28
	write(t, dir, "claude-fileclaim-whatever", "/x\n", time.Now()) // not ours: ignored, not counted
	r, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Markers) != 0 || r.Malformed != 4 {
		t.Fatalf("markers=%v malformed=%d, want none and 4", r.Markers, r.Malformed)
	}
}

// R4: the report dates the newest valid and the newest malformed file, so the
// CLI can tell leftover junk (older) from a format change (newer).
func TestScanTracksNewestValidAndMalformed(t *testing.T) {
	dir, wt := t.TempDir(), t.TempDir()
	junk := time.Now().Add(-48 * time.Hour)
	valid := time.Now().Add(-time.Minute)
	write(t, dir, `claude-wt-e7ad2368-a b"c`, "x", junk)
	write(t, dir, "claude-wt-aaaaaaaa-"+sid, wt+"\n", valid)
	r, _ := Scan(dir)
	if r.NewestMalformed.Sub(junk).Abs() > time.Second || r.NewestValid.Sub(valid).Abs() > time.Second {
		t.Fatalf("NewestMalformed=%v NewestValid=%v; want %v and %v", r.NewestMalformed, r.NewestValid, junk, valid)
	}
}

func TestScanMissingDirIsEmptyNotError(t *testing.T) {
	r, err := Scan(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(r.Markers) != 0 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestScanUnreadableDirIsAnError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(f, nil, 0o644)
	if _, err := Scan(f); err == nil {
		t.Fatal("scanning a file as a directory must fail loudly")
	}
}

func TestDirMirrorsTheHook(t *testing.T) {
	if got := Dir(func(string) string { return "" }); got != "/tmp" {
		t.Fatalf("Dir without TMPDIR = %q, want /tmp (the hook's ${TMPDIR:-/tmp})", got)
	}
	if got := Dir(func(k string) string { return map[string]string{"TMPDIR": "/var/x/T/"}[k] }); got != "/var/x/T/" {
		t.Fatalf("Dir = %q", got)
	}
}

// Decision 4 (review round 2): a subagent working in an isolated worktree that
// has since been cleaned up must not mask the worktree of its parent session.
// A gone SUBAGENT marker is skipped when picking the newest; a gone MAIN
// session marker is still kept (R1).
func TestScanSkipsGoneSubagentMarker(t *testing.T) {
	dir, wtA := t.TempDir(), t.TempDir()
	gone := filepath.Join(t.TempDir(), "agent-isolated-worktree")
	write(t, dir, "claude-wt-aaaaaaaa-"+sid, wtA+"\n", time.Now().Add(-time.Hour))
	write(t, dir, "claude-wt-bbbbbbbb-"+sid+".a61fe560197ecf5f", gone+"\n", time.Now())
	r, _ := Scan(dir)
	m, ok := r.Markers[sid]
	if !ok || m.Path != wtA || m.Gone || r.Gone != 0 {
		t.Fatalf("marker = %+v ok=%v Gone=%d; want parent worktree %s, not the cleaned subagent one", m, ok, r.Gone, wtA)
	}

	// Only gone subagent markers: the session has no marker at all.
	dir2 := t.TempDir()
	write(t, dir2, "claude-wt-bbbbbbbb-"+sid+".a61fe560197ecf5f", gone+"\n", time.Now())
	r2, _ := Scan(dir2)
	if _, ok := r2.Markers[sid]; ok || r2.Gone != 0 {
		t.Fatalf("gone subagent-only session got a marker: %+v", r2)
	}
}

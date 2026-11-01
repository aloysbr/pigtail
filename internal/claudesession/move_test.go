package claudesession

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyMovesJSONLAndSidecar(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const id = "abc"
	seed(t, src, "-p", id, true)
	body := []byte("{\"a\":1}\n{\"b\":2}\n")
	if err := os.WriteFile(filepath.Join(src, "projects", "-p", id+".jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "projects", "-p", id, "subagents", "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got.ConfigDir != dst {
		t.Fatalf("ConfigDir = %q, want %q", got.ConfigDir, dst)
	}
	b, err := os.ReadFile(got.JSONLPath())
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if string(b) != string(body) {
		t.Fatalf("copied body = %q, want %q", b, body)
	}
	if _, err := os.Stat(filepath.Join(got.SidecarDir(), "subagents", "s.jsonl")); err != nil {
		t.Fatalf("sidecar not copied: %v", err)
	}
	// The source is untouched by Copy.
	if _, err := os.Stat(filepath.Join(src, "projects", "-p", id+".jsonl")); err != nil {
		t.Fatalf("Copy must not remove the source: %v", err)
	}
}

func TestCopyRefusesExistingTarget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const id = "abc"
	seed(t, src, "-p", id, false)
	seed(t, dst, "-p", id, false)

	if _, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst); err == nil {
		t.Fatal("expected a conflict error when the target already exists")
	}
}

func TestCopyMissingSource(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if _, err := Copy(Transcript{ID: "ghost", ConfigDir: src, Slug: "-p"}, dst); err == nil {
		t.Fatal("expected an error for a missing source transcript")
	}
}

// failingSource makes a source transcript that opens but cannot be read: the
// path is a DIRECTORY named <id>.jsonl, so os.Stat and os.Open both succeed
// and the failure lands mid-stream, inside io.Copy (EISDIR) — which is the
// only place that exercises the temporary file's cleanup. A source that
// simply doesn't exist would fail before anything was created and would prove
// nothing about partial debris.
func failingSource(t *testing.T, configDir, slug, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(configDir, "projects", slug, id+".jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestCopyLeavesNoDebrisWhenItFails is the property the atomic rename exists
// for: a failed copy must leave NOTHING at the destination — neither a
// truncated file under the canonical <id>.jsonl name (which
// `claude --resume <id>` would load as if it were the conversation), nor the
// temporary file, which would make every later attempt fail on O_EXCL.
func TestCopyLeavesNoDebrisWhenItFails(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	failingSource(t, src, "-p", id)

	if _, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst); err == nil {
		t.Fatal("expected the copy of an unreadable source to fail")
	}

	dstPath := filepath.Join(dst, "projects", "-p", id+".jsonl")
	if _, err := os.Stat(dstPath); !os.IsNotExist(err) {
		t.Fatalf("a failed copy left a file at the destination transcript path (%v)", err)
	}
	if _, err := os.Stat(dstPath + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("a failed copy left its temporary file behind (%v)", err)
	}
	// Nothing at all should be left in the destination project dir.
	entries, err := os.ReadDir(filepath.Dir(dstPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("destination project dir holds %d entrie(s) after a failed copy, want 0", len(entries))
	}
}

// TestCopyRetriesCleanlyAfterAFailure is the other half: before the atomic
// rename, a failed copy poisoned every later attempt — the debris tripped
// Copy's own conflict check ("target already has a transcript") and both
// pre-flights, with nothing telling the user which file to delete. The retry
// must simply work once the source is readable again.
func TestCopyRetriesCleanlyAfterAFailure(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	failingSource(t, src, "-p", id)

	if _, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst); err == nil {
		t.Fatal("expected the first copy to fail")
	}

	// Replace the unreadable source with a real transcript and retry.
	srcPath := filepath.Join(src, "projects", "-p", id+".jsonl")
	if err := os.Remove(srcPath); err != nil {
		t.Fatal(err)
	}
	body := []byte("{\"a\":1}\n{\"b\":2}\n")
	if err := os.WriteFile(srcPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst)
	if err != nil {
		t.Fatalf("retry after a failed copy was refused: %v", err)
	}
	b, err := os.ReadFile(got.JSONLPath())
	if err != nil {
		t.Fatalf("read the retried copy: %v", err)
	}
	if string(b) != string(body) {
		t.Fatalf("retried copy = %q, want %q", b, body)
	}
}

// TestCopyRefusesAndKeepsALeftoverPartial covers the one piece of debris Copy
// must NOT clean up: a "<id>.jsonl.partial" it did not create (a process
// killed mid-copy). Silently overwriting it would destroy whatever it holds,
// so O_EXCL refuses and the error names the file to delete.
func TestCopyRefusesAndKeepsALeftoverPartial(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, src, "-p", id, false)

	dstDir := filepath.Join(dst, "projects", "-p")
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dstDir, id+".jsonl.partial")
	if err := os.WriteFile(leftover, []byte("half a transcript"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst)
	if err == nil {
		t.Fatal("expected Copy to refuse rather than overwrite a leftover partial file")
	}
	if !strings.Contains(err.Error(), leftover) {
		t.Fatalf("error %q should name the file to delete (%s)", err, leftover)
	}
	if b, readErr := os.ReadFile(leftover); readErr != nil || string(b) != "half a transcript" {
		t.Fatalf("the leftover partial must be left untouched, got %q (%v)", b, readErr)
	}
}

// TestCopyFailsWhenTheSidecarCannotBeStatted covers the asymmetry that made
// the sidecar the one thing in this move held to a lower standard than the
// main transcript: Copy decided "there is no sidecar" from a single Stat, so
// ANY error other than NotExist skipped the copy silently — and RemoveSource,
// called later from a separate decision, still deleted the directory with
// RemoveAll. No error, no warning, and ✓ source removed printed over lost
// sub-agent transcripts. The fixture is a self-referential symlink, whose
// Stat fails with ELOOP: a real, non-NotExist error.
func TestCopyFailsWhenTheSidecarCannotBeStatted(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, src, "-p", id, false)

	loop := filepath.Join(src, "projects", "-p", id)
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(loop); os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: the sidecar path must fail to stat with something other than NotExist, got %v", err)
	}

	if _, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst); err == nil {
		t.Fatal("expected Copy to fail when the sidecar cannot be statted, rather than treat it as absent")
	}
	dstPath := filepath.Join(dst, "projects", "-p", id+".jsonl")
	if _, err := os.Stat(dstPath); !os.IsNotExist(err) {
		t.Fatalf("a failed Copy must not leave the transcript at the destination (%v)", err)
	}
}

// TestCopyFailsWhenTheSidecarCopyIsIncomplete is the verification itself: a
// sidecar copy that stops partway must fail the Copy, so the caller never
// reaches RemoveSource — which deletes the source sidecar with RemoveAll.
// copyTree is stubbed to copy nothing, standing in for a copy interrupted
// after the first file.
func TestCopyFailsWhenTheSidecarCopyIsIncomplete(t *testing.T) {
	saved := copyTree
	t.Cleanup(func() { copyTree = saved })
	copyTree = func(_, to string) error { return os.MkdirAll(to, 0o700) }

	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, src, "-p", id, true)
	if err := os.WriteFile(filepath.Join(src, "projects", "-p", id, "subagents", "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst)
	if err == nil {
		t.Fatal("expected Copy to fail when the sidecar copy is short of the source")
	}
	if !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("error %q should name the sidecar", err)
	}
	dstPath := filepath.Join(dst, "projects", "-p", id+".jsonl")
	if _, statErr := os.Stat(dstPath); !os.IsNotExist(statErr) {
		t.Fatalf("a failed Copy must not leave the transcript at the destination (%v)", statErr)
	}
}

// TestCopyVerifiesTheSidecarByCountAndBytes pins the other direction of the
// comparison: a destination subtree holding MORE than the source (debris from
// an earlier interrupted move) is not a faithful copy either, and must be
// refused rather than passed off as a clean move.
func TestCopyVerifiesTheSidecarByCountAndBytes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, src, "-p", id, true)
	if err := os.WriteFile(filepath.Join(src, "projects", "-p", id, "subagents", "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(dst, "projects", "-p", id, "subagents")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "orphan.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst); err == nil {
		t.Fatal("expected Copy to refuse a destination sidecar that does not match the source")
	}
}

func TestRemoveSourceDropsBoth(t *testing.T) {
	src := t.TempDir()
	const id = "abc"
	seed(t, src, "-p", id, true)
	tr := Transcript{ID: id, ConfigDir: src, Slug: "-p"}

	if err := RemoveSource(tr); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if _, err := os.Stat(tr.JSONLPath()); !os.IsNotExist(err) {
		t.Fatal("jsonl still present")
	}
	if _, err := os.Stat(tr.SidecarDir()); !os.IsNotExist(err) {
		t.Fatal("sidecar still present")
	}
}

func TestWaitExitReturnsWhenProcessGoes(t *testing.T) {
	calls := 0
	probe := func(string) bool {
		calls++
		return calls < 3 // alive twice, then gone
	}
	if err := WaitExit("abc", time.Second, time.Millisecond, probe); err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if calls != 3 {
		t.Fatalf("probe called %d times, want 3", calls)
	}
}

func TestWaitExitTimesOutOnBusySession(t *testing.T) {
	alive := func(string) bool { return true }
	err := WaitExit("abc", 20*time.Millisecond, time.Millisecond, alive)
	if err == nil {
		t.Fatal("expected a timeout error for a session that never exits")
	}
}

// TestRunningMatchesLiveClaudeProcess covers the core positive case: a
// process whose command line is a claude binary carrying the session id.
func TestRunningMatchesLiveClaudeProcess(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	psRun = func() ([]byte, error) {
		return []byte("  1234 claude --resume abc-123 --permission-mode auto\n"), nil
	}

	if !Running("abc-123") {
		t.Fatal("Running should return true for a live claude process carrying the session id")
	}
}

// TestRunningMatchesClaudeWithPathPrefix covers argv0 given as a full path
// (e.g. /usr/local/bin/claude), not just the bare command name.
func TestRunningMatchesClaudeWithPathPrefix(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	psRun = func() ([]byte, error) {
		return []byte("  1234 /usr/local/bin/claude --resume abc-123 --permission-mode auto\n"), nil
	}

	if !Running("abc-123") {
		t.Fatal("Running should match a claude binary invoked via an absolute path")
	}
}

// TestRunningMatchesLocalInstallerNodeShape is NEW-1's core regression:
// Claude Code's documented local-installer layout (`claude migrate-
// installer`, under ~/.claude/local/) launches as `node <path>/.bin/claude
// ...` — argv[0] is "node", not a claude path. The old argv[0]-anchored
// regex reported this as not running: a live Claude going undetected, which
// is CRITICAL 1's failure mode returning by another door. Without a pane to
// fall back on (moveDetached's only gate), this would have gone straight to
// copying a live transcript.
func TestRunningMatchesLocalInstallerNodeShape(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	psRun = func() ([]byte, error) {
		return []byte("  4242 node /Users/x/.claude/local/node_modules/.bin/claude --resume abc-123 --permission-mode auto\n"), nil
	}

	if !Running("abc-123") {
		t.Fatal("Running should match the local-installer node-shim shape (argv[0]=node, claude path elsewhere in argv)")
	}
}

// TestRunningMatchesNativeHomebrewPath covers the other real installer
// layout observed on this machine (/opt/homebrew/bin/claude), where argv[0]
// is already the claude path.
func TestRunningMatchesNativeHomebrewPath(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	psRun = func() ([]byte, error) {
		return []byte("  4243 /opt/homebrew/bin/claude --resume abc-123 --permission-mode auto\n"), nil
	}

	if !Running("abc-123") {
		t.Fatal("Running should match a native homebrew-installed claude")
	}
}

// TestRunningIgnoresNonClaudeProcessWithIDInArgv is the regression this fix
// exists for: pigtail session move --session <id> carries the id in its own
// argv, exactly like this fixture. It must never count as "the session is
// running", or every detached move and every wait would see itself.
func TestRunningIgnoresNonClaudeProcessWithIDInArgv(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	psRun = func() ([]byte, error) {
		return []byte("  4242 pigtail session move --session abc-123\n"), nil
	}

	if Running("abc-123") {
		t.Fatal("Running should ignore a non-claude process line even if the id appears in its argv")
	}
}

// TestRunningIgnoresCmuxResumeWrapper is a real process line captured with
// `ps -Awwo pid=,args=` on this machine (a cmux surface's resume wrapper,
// before it execs into claude): it carries CLAUDE_CONFIG_DIR=.../.claude-
// work-max — "claude" preceded by a dot, not a slash — and later
// `printf claude)"`, where "claude" is immediately followed by `)`, not
// whitespace or end of string. Neither should match, and this fixes NEW-1
// specifically by widening the match to "anywhere in argv" rather than
// argv[0] only — this case is what confirms that widening didn't turn into
// "anywhere in the text, including inside other tokens."
func TestRunningIgnoresCmuxResumeWrapper(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	const wrapperLine = `  1036 /bin/zsh -lic '/usr/bin/env' 'CLAUDE_CONFIG_DIR=/Users/ada/.claude-two-max' 'CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV=1' 'CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV_KEYS=CLAUDE_CONFIG_DIR' '/bin/zsh' '-lc' 'cd -- '\''/Users/ada/Developer'\'' 2>/dev/null || [ ! -d '\''/Users/ada/Developer'\'' ] && /bin/sh -c '\''"$([ -x "${CMUX_CLAUDE_WRAPPER_SHIM:-}" ] && printf '\''\'\'''\''%s'\''\'\'''\'' "$CMUX_CLAUDE_WRAPPER_SHIM" || printf claude)" '\''\'\'''\''--resume'\''\'\'''\'' '\''\'\'''\''3b744869-f2a7-4c4a-b411-ef4e4d3942b3'\''\'\'''\'' '\''\'\'''\''--permission-mode'\''\'\'''\'' '\''\'\'''\''auto'\''\'\'''\'''\'''` + "\n"

	psRun = func() ([]byte, error) {
		return []byte(wrapperLine), nil
	}

	if Running("3b744869-f2a7-4c4a-b411-ef4e4d3942b3") {
		t.Fatal("Running should ignore the cmux resume wrapper even though the session id and CLAUDE_CONFIG_DIR both appear in its argv")
	}
}

// TestRunningExcludesOwnPID is the second line of defence beyond the argv0
// check: even a line that looks like a claude process is ignored if its pid
// is our own.
func TestRunningExcludesOwnPID(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	self := os.Getpid()
	psRun = func() ([]byte, error) {
		return []byte(fmt.Sprintf("  %d claude --resume abc-123 --permission-mode auto\n", self)), nil
	}

	if Running("abc-123") {
		t.Fatal("Running must exclude its own pid even if the line looks like a claude process")
	}
}

// TestRunningFailsClosedWhenListingErrors covers the fail-closed contract: an
// unreadable process listing must never be read as "not running", since that
// would let a live transcript get copied out from under a running Claude.
func TestRunningFailsClosedWhenListingErrors(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	psRun = func() ([]byte, error) {
		return nil, os.ErrNotExist
	}

	if !Running("abc-123") {
		t.Fatal("Running should return true when the process listing cannot be obtained (fail closed)")
	}
}

// A visible <id>.jsonl at the destination implies a complete sidecar.
func TestCopyMakesTranscriptVisibleLast(t *testing.T) {
	saved := copyTree
	t.Cleanup(func() { copyTree = saved })
	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, src, "-p", id, true)
	sawJSONL := false
	copyTree = func(from, to string) error {
		if _, err := os.Stat(filepath.Join(dst, "projects", "-p", id+".jsonl")); err == nil {
			sawJSONL = true
		}
		return saved(from, to)
	}
	if _, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst); err != nil || sawJSONL {
		t.Fatalf("Copy: %v; transcript visible before its sidecar: %v", err, sawJSONL)
	}
}

// A sidecar copy that fails leaves no debris the call created.
func TestCopyFailedSidecarLeavesNoDebris(t *testing.T) {
	saved := copyTree
	t.Cleanup(func() { copyTree = saved })
	copyTree = func(_, to string) error { return os.MkdirAll(to, 0o700) } // copies nothing
	src, dst := t.TempDir(), t.TempDir()
	const id = "1bb0480e-a291-4dab-bb69-8190b97b6824"
	seed(t, src, "-p", id, true)
	if err := os.WriteFile(filepath.Join(src, "projects", "-p", id, "subagents", "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(Transcript{ID: id, ConfigDir: src, Slug: "-p"}, dst); err == nil {
		t.Fatal("want the incomplete sidecar to fail the Copy")
	}
	for _, p := range []string{filepath.Join(dst, "projects", "-p", id+".jsonl"), filepath.Join(dst, "projects", "-p", id)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind (%v)", p, err)
		}
	}
}

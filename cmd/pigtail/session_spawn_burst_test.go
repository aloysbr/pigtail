package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// appendLaunches writes n spawn records aged age, on workspaces that run no Claude.
func appendLaunches(t *testing.T, n int, age time.Duration, mod func(*spawnreg.Entry)) {
	t.Helper()
	reg := spawnreg.Open(spawnreg.DefaultPath())
	for i := 0; i < n; i++ {
		e := spawnreg.Entry{TS: time.Now().UTC().Add(-age), SubjectID: "sub_other", WorkspaceID: fmt.Sprintf("L%d", i), Result: "launched"}
		if mod != nil {
			mod(&e)
		}
		if err := reg.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

// A1: the 11th launch within 10 min is refused (exit 1, a rule refusal never retried); the 10th passes.
func TestSpawnBurstRefusesTheEleventh(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	appendLaunches(t, 9, 5*time.Minute, nil)
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err != nil {
		t.Fatalf("9 recent launches refused the 10th: %v", err)
	}
	appendLaunches(t, 1, time.Minute, nil)
	for _, extra := range [][]string{{"--dry-run"}, nil} {
		args := append([]string{"--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir()}, extra...)
		_, _, err := runSpawnCLI(t, s, args...)
		if err == nil {
			t.Fatalf("%v: the 11th launch passed", extra)
		}
		for _, want := range []string{"10 sessions launched in the last 10 min", "nothing launched", "Respawns do not count", "not retried"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: refusal lacks %q: %v", extra, want, err)
			}
		}
		if exitCode(err) != 1 {
			t.Fatalf("%v: burst refusal exits %d, want 1 (a rule refusal, never the retryable 75)", extra, exitCode(err))
		}
	}
	if len(f.created) != 0 {
		t.Fatal("a refused burst created a workspace")
	}
}

// A2: live sessions no longer refuse — 12 old spawned sessions, alive.
func TestSpawnBurstIgnoresLiveSessions(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	appendLaunches(t, 12, time.Hour, nil)
	for i := 0; i < 12; i++ {
		f.addProc(7000+i, "/x/bin/claude", fmt.Sprintf("CMUX_WORKSPACE_ID=L%d", i))
	}
	if _, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run"); err != nil {
		t.Fatalf("12 live spawned sessions refused a spawn: %v", err)
	}
}

// A3: respawn records and launches older than the window do not count; the dry-run says the count.
func TestSpawnBurstSkipsRespawnsAndOldLaunches(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	appendLaunches(t, 10, 11*time.Minute, nil)
	appendLaunches(t, 10, time.Minute, func(e *spawnreg.Entry) { e.Kind, e.ID, e.Result = spawnreg.KindRespawn, e.WorkspaceID, "respawned" })
	appendLaunches(t, 3, time.Minute, func(e *spawnreg.Entry) { e.WorkspaceID = "N" + e.WorkspaceID })
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if want := "  launches  3 in the last 10 min — the 11th within 10 min is refused (burst detector)"; !strings.Contains(out, want) {
		t.Fatalf("dry-run lacks %q:\n%s", want, out)
	}
	for _, gone := range []string{"  live      ", "  breaker   ", "  starting  "} {
		if strings.Contains(out, gone) {
			t.Errorf("dry-run still prints %q", gone)
		}
	}
}

// A3: a relayed launch and an unverified one count.
func TestSpawnBurstCountsRelayAndUnverified(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	appendLaunches(t, 5, 2*time.Minute, func(e *spawnreg.Entry) { e.RelayFrom = "R" })
	appendLaunches(t, 5, 3*time.Minute, func(e *spawnreg.Entry) { e.WorkspaceID, e.Result = "U"+e.WorkspaceID, "unverified" })
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "10 sessions launched in the last 10 min") {
		t.Fatalf("relayed + unverified launches = %v, want the burst refusal", err)
	}
}

// A1: --json carries the refusal as a plain error: no retryable, no retry_after_s.
func TestSpawnBurstJSONRefusal(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	appendLaunches(t, 1, 9*time.Minute, nil)
	appendLaunches(t, 9, time.Minute, func(e *spawnreg.Entry) { e.WorkspaceID = "N" + e.WorkspaceID })
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--json")
	if err == nil {
		t.Fatal("the 11th launch passed")
	}
	var v struct {
		Error      string `json:"error"`
		Retryable  bool   `json:"retryable"`
		RetryAfter *int   `json:"retry_after_s"`
	}
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("refusal is not one JSON object: %v\n%s", jerr, out)
	}
	if v.Retryable || v.RetryAfter != nil || !strings.Contains(v.Error, "reads as a runaway") || !strings.Contains(v.Error, "not retried") {
		t.Fatalf("json = %+v, want a non-retryable error (no retry_after_s) that says it is not retried", v)
	}
}

// A4: an unreadable registry refuses, names the file and the line, launches nothing (fail-safe).
func TestSpawnBurstRegistryUnreadable(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	path := spawnreg.DefaultPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir())
	if err == nil {
		t.Fatal("a corrupt registry let the spawn through")
	}
	for _, want := range []string{path, "line 1", "cannot be counted", "nothing is launched (fail-safe)", "repair or remove that line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %v", want, err)
		}
	}
	if len(f.created) != 0 {
		t.Fatal("a corrupt registry created a workspace")
	}
}

// freesIn: the oldest launch leaves the window in 1m13s when it is 8m47.3s old (rounded up to the second); an
// already-expired oldest launch still says at least 1 s; the refusal prints the delay it computed.
func TestBurstFreesIn(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	b := spawnBurst{Recent: 10, Oldest: now.Add(-8*time.Minute - 47300*time.Millisecond)}
	if got := b.freesIn(now); got != 73*time.Second {
		t.Fatalf("freesIn = %v, want 1m13s (1m12.7s rounded up)", got)
	}
	if got := (spawnBurst{Recent: 10, Oldest: now.Add(-11 * time.Minute)}).freesIn(now); got != time.Second {
		t.Fatalf("an oldest launch already out of the window: freesIn = %v, want the 1s minimum", got)
	}
	if err := b.refusal(now); err == nil || !strings.Contains(err.Error(), "(in 1m13s)") {
		t.Fatalf("refusal = %v, want it to carry (in 1m13s)", err)
	}
}

// A2: --max-live no longer limits; passing it warns on stderr and --json stdout stays one object.
func TestSpawnMaxLiveIsIgnoredWithAWarning(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	appendLaunches(t, 2, time.Hour, nil)
	f.addProc(7000, "/x/bin/claude", "CMUX_WORKSPACE_ID=L0")
	f.addProc(7001, "/x/bin/claude", "CMUX_WORKSPACE_ID=L1")
	for _, n := range []string{"1", "17"} { // 17: the old 1..16 range is gone too
		out, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--max-live", n, "--dry-run", "--json")
		if err != nil {
			t.Fatalf("--max-live %s with 2 live spawned sessions refused: %v", n, err)
		}
		if !strings.Contains(stderr, "warning: --max-live is ignored: the burst detector replaced the live caps") {
			t.Errorf("--max-live %s: stderr lacks the deprecation warning: %q", n, stderr)
		}
		var v map[string]any
		if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
			t.Fatalf("--max-live %s: stdout is not one JSON object: %v\n%s", n, jerr, out)
		}
	}
}

// The env is no longer read; set, it says so.
func TestSpawnMaxLiveEnvWarns(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	installSpawnFake(t)
	t.Setenv("PIGTAIL_SPAWN_MAX_LIVE", "1")
	_, stderr, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "warning: PIGTAIL_SPAWN_MAX_LIVE is ignored") {
		t.Errorf("stderr lacks the env warning: %q", stderr)
	}
}

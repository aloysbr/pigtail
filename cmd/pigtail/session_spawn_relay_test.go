package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
)

func TestRelaySlot(t *testing.T) {
	t.Parallel()
	live := []spawnreg.Entry{{WorkspaceID: "ME"}}
	env := "CMUX_WORKSPACE_ID=me CLAUDE_CONFIG_DIR=/c"
	for _, c := range []struct {
		name                   string
		relay                  bool
		env, envWS             string
		pids                   map[string][]int
		live                   []spawnreg.Entry
		ws, nothing, errSubstr string
	}{
		{"no flag", false, env, "ME", map[string][]int{"ME": {42}}, live, "", "", ""},
		{"passes", true, env, "ME", map[string][]int{"ME": {42}}, live, "ME", "", ""},
		{"spoofed env", true, env, "OTHER", map[string][]int{"ME": {42}}, live, "", "", "is not the calling Claude's"},
		{"caller outside cmux", true, "CLAUDE_CONFIG_DIR=/c", "ME", nil, live, "", "", "runs outside cmux"},
		{"not spawned", true, env, "ME", map[string][]int{"ME": {42}}, nil, "", "was not opened by pigtail session spawn", ""},
		{"two Claudes", true, env, "ME", map[string][]int{"ME": {42, 43}}, live, "", "runs 2 Claude processes", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws, nothing, err := relaySlot(c.relay, c.env, 42, c.envWS, c.pids, c.live)
			if ws != c.ws {
				t.Errorf("ws = %q, want %q", ws, c.ws)
			}
			if (c.nothing == "") != (nothing == "") || !strings.Contains(nothing, c.nothing) {
				t.Errorf("nothing = %q, want %q", nothing, c.nothing)
			}
			if (c.errSubstr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.errSubstr)) {
				t.Errorf("err = %v, want %q", err, c.errSubstr)
			}
		})
	}
}

func relayFixture(t *testing.T) (*stubDaemon, *spawnFake) {
	t.Helper()
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	reg := spawnreg.Open(spawnreg.DefaultPath())
	old := time.Now().Add(-time.Hour)
	_ = reg.Append(spawnreg.Entry{TS: old, SubjectID: "sub_prev", WorkspaceID: spawnCallerWS, Result: "launched"})
	_ = reg.Append(spawnreg.Entry{TS: old, SubjectID: "sub_other", WorkspaceID: "B1", Result: "launched"})
	f.addProc(7500, "/x/bin/claude", "CMUX_WORKSPACE_ID=B1")
	return s, f
}

// End to end: the caller is a spawned session, alone in its workspace; --relay
// records relay_from and the result tells the caller to end.
func TestSpawnRelayRecordsRelayFrom(t *testing.T) {
	s, f := relayFixture(t)
	cwd := t.TempDir()
	f.stamp = "auto"
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--relay", "--dry-run")
	if err != nil {
		t.Fatalf("relay dry-run refused: %v", err)
	}
	if want := "  relay     from " + spawnCallerWS; !strings.Contains(out, want) {
		t.Errorf("dry-run lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "relaying 1 excluded") {
		t.Errorf("dry-run still counts the relay against a cap:\n%s", out)
	}
	f.launchesAs(persoCellDir(t), cwd)
	out, _, err = runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", cwd, "--relay")
	if err != nil {
		t.Fatalf("relay refused: %v", err)
	}
	if !strings.Contains(out, "relay: workspace:42 takes over from this session — end this one now\n") {
		t.Errorf("result lacks the relay line:\n%s", out)
	}
	raw, _ := os.ReadFile(spawnreg.DefaultPath())
	if !strings.Contains(string(raw), `"relay_from":"`+spawnCallerWS+`"`) {
		t.Fatalf("registry has no relay_from:\n%s", raw)
	}
}

// Review focus 3: a forged $CMUX_WORKSPACE_ID cannot relay from another tab.
func TestSpawnRelayRefusesSpoofedWorkspace(t *testing.T) {
	s, _ := relayFixture(t)
	t.Setenv("CMUX_WORKSPACE_ID", "B1") // this pigtail claims B1; the calling Claude (4242) is in spawnCallerWS
	_, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--relay", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "is not the calling Claude's") {
		t.Fatalf("spoofed relay = %v", err)
	}
}

// Review focus 4: from a session spawn did not open, --relay passes nothing and says so — a warning, not a refusal.
func TestSpawnRelayPassedNothingIsAWarning(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, _ := realSubjectDaemon(t)
	f := installSpawnFake(t)
	_ = spawnreg.Open(spawnreg.DefaultPath()).Append(spawnreg.Entry{TS: time.Now().Add(-time.Hour), SubjectID: "sub_other", WorkspaceID: "B1", Result: "launched"})
	f.addProc(7500, "/x/bin/claude", "CMUX_WORKSPACE_ID=B1")
	out, _, err := runSpawnCLI(t, s, "--subject", "sub_p", "--prompt", spawnPrompt, "--cwd", t.TempDir(), "--relay", "--dry-run")
	if err != nil || !strings.Contains(out, "⚠ --relay passed nothing: workspace "+spawnCallerWS+" was not opened by pigtail session spawn") {
		t.Fatalf("relay from an unspawned caller: err %v\n%s", err, out)
	}
}

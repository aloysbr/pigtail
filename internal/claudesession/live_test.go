package claudesession

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writePidFile writes the <config-dir>/sessions/<pid>.json Claude Code keeps
// for every running interactive session.
func writePidFile(t *testing.T, configDir string, pid int, sessionID, cwd string) {
	t.Helper()
	dir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"kind":"interactive"}`, pid, sessionID, cwd)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLiveSessionsJoinsProcessesWithPidFiles: a bare `claude` typed in a pane
// carries no session id in argv, so the id comes from the pid file Claude
// writes in whichever config dir it runs under.
func TestLiveSessionsJoinsProcessesWithPidFiles(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	cfgA, cfgB := t.TempDir(), t.TempDir()
	writePidFile(t, cfgA, 100, "aaaaaaaa-0000-0000-0000-000000000001", "/w/a")
	writePidFile(t, cfgB, 200, "bbbbbbbb-0000-0000-0000-000000000002", "/w/b")
	// A pid file for a process that is gone: never reported.
	writePidFile(t, cfgA, 999, "dddddddd-0000-0000-0000-000000000009", "/w/gone")
	// A pid file whose content names another pid: not trusted.
	writePidFile(t, cfgB, 400, "eeeeeeee-0000-0000-0000-000000000004", "/w/e")
	if err := os.WriteFile(filepath.Join(cfgB, "sessions", "400.json"),
		[]byte(`{"pid":401,"sessionId":"eeeeeeee-0000-0000-0000-000000000004"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	psRun = func() ([]byte, error) {
		return []byte(
			"  100 /Users/x/.nvm/versions/node/v22/bin/claude\n" +
				"  200 /opt/homebrew/bin/claude --settings /tmp/cmux-claude-settings.X --resume bbbbbbbb-0000-0000-0000-000000000002\n" +
				"  300 /opt/homebrew/bin/claude\n" + // no pid file anywhere: id unknown, skipped
				"  400 claude\n" +
				"  500 /bin/zsh -l\n"), nil // not claude, even though nothing else rules it out
	}

	got, err := LiveSessions([]string{cfgA, cfgB})
	if err != nil {
		t.Fatalf("LiveSessions: %v", err)
	}
	want := []Live{
		{PID: 100, SessionID: "aaaaaaaa-0000-0000-0000-000000000001", Cwd: "/w/a", ConfigDir: cfgA,
			Args: "/Users/x/.nvm/versions/node/v22/bin/claude"},
		{PID: 200, SessionID: "bbbbbbbb-0000-0000-0000-000000000002", Cwd: "/w/b", ConfigDir: cfgB,
			Args: "/opt/homebrew/bin/claude --settings /tmp/cmux-claude-settings.X --resume bbbbbbbb-0000-0000-0000-000000000002"},
	}
	if len(got) != len(want) {
		t.Fatalf("LiveSessions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("LiveSessions[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestLiveSessionsFailsOnListingError: no listing is not "nothing running".
func TestLiveSessionsFailsOnListingError(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })
	psRun = func() ([]byte, error) { return nil, errors.New("ps: permission denied") }

	if _, err := LiveSessions([]string{t.TempDir()}); err == nil {
		t.Fatal("expected an error when the process listing cannot be obtained")
	}
}

// TestLiveSessionsSkipsParkedPidFile: when Claude Code parks a conversation as
// a background job, the interactive process keeps running but its pid file is
// frozen on the old sessionId and gains a parkedJobId; the conversation goes
// on in a `claude bg-spare` process (kind "bg") under a new id. The parked
// file no longer hosts its sessionId, so that id is not reported live.
func TestLiveSessionsSkipsParkedPidFile(t *testing.T) {
	saved := psRun
	t.Cleanup(func() { psRun = saved })

	cfg := t.TempDir()
	dir := filepath.Join(cfg, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parked := `{"pid":100,"sessionId":"aaaaaaaa-0000-0000-0000-00000000000a","cwd":"/w","kind":"interactive","status":"busy","parkedJobId":"job-1"}`
	bg := `{"pid":200,"sessionId":"bbbbbbbb-0000-0000-0000-00000000000b","cwd":"/w","kind":"bg","status":"busy"}`
	for name, body := range map[string]string{"100.json": parked, "200.json": bg} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	psRun = func() ([]byte, error) {
		return []byte("  100 /opt/homebrew/bin/claude\n  200 /opt/homebrew/bin/claude bg-spare\n"), nil
	}

	got, err := LiveSessions([]string{cfg})
	if err != nil {
		t.Fatalf("LiveSessions: %v", err)
	}
	var ids []string
	for _, l := range got {
		ids = append(ids, l.SessionID)
	}
	if len(got) != 1 || got[0].SessionID != "bbbbbbbb-0000-0000-0000-00000000000b" || got[0].PID != 200 {
		t.Fatalf("LiveSessions ids = %v, want only the bg job's bbbbbbbb-…b (pid 200)", ids)
	}
}

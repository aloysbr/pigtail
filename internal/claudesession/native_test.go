package claudesession

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writePid(t *testing.T, cfg string, pid int, sid, extra string) {
	t.Helper()
	dir := filepath.Join(cfg, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/w"%s}`, pid, sid, extra)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadAndFindNative(t *testing.T) {
	cfg := t.TempDir()
	self := os.Getpid()
	writePid(t, cfg, self, "sess-live", `,"name":"alpha","status":"idle","kind":"interactive"`)
	writePid(t, cfg, 999999, "sess-dead", `,"name":"beta"`)
	// A file whose content names another pid is not trusted.
	_ = os.WriteFile(filepath.Join(cfg, "sessions", "12345.json"), []byte(`{"pid":1,"sessionId":"sess-lie"}`), 0o600)

	n, ok := ReadNative(cfg, self)
	if !ok || n.Name != "alpha" || n.Kind != "interactive" || n.ConfigDir != cfg {
		t.Fatalf("ReadNative = %+v %v", n, ok)
	}
	if _, ok := FindNative(cfg, "sess-lie"); ok {
		t.Fatal("untrusted file accepted")
	}
	if n, ok := FindNative(cfg, "sess-dead"); !ok || n.PID != 999999 {
		t.Fatalf("FindNative dead = %+v %v", n, ok)
	}
	if !SessionAlive(cfg, self, "sess-live") {
		t.Fatal("live session reported dead")
	}
	if SessionAlive(cfg, self, "other") {
		t.Fatal("pid alive but file names another session: must be dead")
	}
	if SessionAlive(cfg, 999999, "sess-dead") {
		t.Fatal("dead pid reported alive")
	}
}

func TestReadNativeWaitingFields(t *testing.T) {
	cfg := t.TempDir()
	self := os.Getpid()
	writePid(t, cfg, self, "sess-wait", `,"status":"waiting","waitingFor":"input needed","statusUpdatedAt":1759240000000,"kind":"interactive"`)
	n, ok := ReadNative(cfg, self)
	if !ok || n.Status != "waiting" || n.WaitingFor != "input needed" || n.StatusUpdatedAt != 1759240000000 {
		t.Fatalf("ReadNative = %+v %v", n, ok)
	}
}

// startedAt (ms) is when the process started or resumed the session: read
// as is (spec « Your turn »).
func TestReadNativeStartedAt(t *testing.T) {
	cfg := t.TempDir()
	self := os.Getpid()
	writePid(t, cfg, self, "sess-start", `,"status":"idle","startedAt":1791040383742,"statusUpdatedAt":1791043176556`)
	n, ok := ReadNative(cfg, self)
	if !ok || n.StartedAt != 1791040383742 {
		t.Fatalf("ReadNative = %+v %v", n, ok)
	}
}

// A partial file (seen in the wild: only a peerToken, no pid, no sessionId)
// is ignored, never read as a session with an empty id.
func TestReadNativeIgnoresPartialFile(t *testing.T) {
	cfg := t.TempDir()
	self := os.Getpid()
	dir := filepath.Join(cfg, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", self)), []byte(`{"peerToken":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, ok := ReadNative(cfg, self); ok {
		t.Fatalf("partial file accepted: %+v", n)
	}
	withPid := fmt.Sprintf(`{"pid":%d,"peerToken":"x"}`, self)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", self)), []byte(withPid), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, ok := ReadNative(cfg, self); ok {
		t.Fatalf("file without sessionId accepted: %+v", n)
	}
}

// TestSessionAliveFalseForParkedPidFile: a running process whose pid file
// carries a parkedJobId no longer serves the sessionId it still names.
func TestSessionAliveFalseForParkedPidFile(t *testing.T) {
	cfg := t.TempDir()
	self := os.Getpid()
	writePid(t, cfg, self, "sess-parked", `,"status":"busy","kind":"interactive","parkedJobId":"job-1"`)
	if SessionAlive(cfg, self, "sess-parked") {
		t.Fatal("parked pid file reported as still hosting its session")
	}
	writePid(t, cfg, self, "sess-parked", `,"status":"busy","kind":"interactive","parkedJobId":""`)
	if !SessionAlive(cfg, self, "sess-parked") {
		t.Fatal("empty parkedJobId must not count as parked")
	}
}

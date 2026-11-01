package sdksession

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

// fakes holds the injected world of a Registry.
type fakes struct {
	alive  map[int]bool
	procs  []claudesession.Proc
	native map[int]claudesession.Native
	killed []int
	now    time.Time
}

func newFakes() *fakes {
	return &fakes{alive: map[int]bool{}, native: map[int]claudesession.Native{}, now: t0.Add(time.Minute)}
}

func (f *fakes) registry(dir string) *Registry {
	return &Registry{
		Dir:   dir,
		Alive: func(pid int) bool { return f.alive[pid] },
		Procs: func() ([]claudesession.Proc, error) { return f.procs, nil },
		Native: func(cfg string, pid int) (claudesession.Native, bool) {
			n, ok := f.native[pid]
			return n, ok
		},
		Kill: func(pid int) error { f.killed = append(f.killed, pid); f.alive[pid] = false; return nil },
		Now:  func() time.Time { return f.now },
	}
}

const (
	sid  = "sdk_0123456789abcdef"
	sub  = "sub_x000000000000001"
	uuid = "1da9ff5f-4306-4adf-aa01-d368f0e3e006"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedMeta(t *testing.T, dir, id, subject string, hostPID int, created time.Time) Meta {
	t.Helper()
	m := Meta{Version: 1, ID: id, SubjectID: subject, Cwd: "/tmp/work", ConfigDir: "/tmp/cell", Claude: "/bin/claude",
		Mode: "default", Prompt: "p", SessionUUID: uuid, HostPID: hostPID, CreatedAt: created}
	writeJSON(t, filepath.Join(dir, id, "meta.json"), m)
	return m
}

func seedHost(t *testing.T, dir, id string, h HostFile) {
	t.Helper()
	h.Version = 1
	writeJSON(t, filepath.Join(dir, id, "host.json"), h)
}

func TestHostFileContract(t *testing.T) {
	for name, v := range map[string]any{"meta.json": &Meta{}, "host.json": &HostFile{}} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "clients", "sdkhost", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(raw)) != string(out) {
			t.Errorf("%s does not round-trip:\n%s\n%s", name, raw, out)
		}
	}
}

func TestSessionFromFiles(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	f.alive[4242] = true
	seedMeta(t, dir, sid, sub, 4242, t0)
	seedHost(t, dir, sid, HostFile{HostPID: 4242, ClaudePIDs: []int{4243}, ClaudeSessionID: uuid, State: "waiting",
		Requests: []Request{{Rid: "r1", Kind: "question", Tool: "AskUserQuestion", Input: json.RawMessage(`{}`), OpenedAt: t0}}})
	s, err := f.registry(dir).Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "waiting" || !s.Live || s.SubjectID != sub || s.ClaudeSessionID != uuid || len(s.Pending) != 1 || s.ResumeCommand != "" {
		t.Fatalf("unexpected session: %+v", s)
	}
	if s.Cwd != "/tmp/work" || s.ConfigDir != "/tmp/cell" || s.HostPID != 4242 {
		t.Fatalf("meta fields lost: %+v", s)
	}
}

func TestGetRejectsBadID(t *testing.T) {
	f := newFakes()
	if _, err := f.registry(t.TempDir()).Get("../etc"); err == nil {
		t.Fatal("expected error for a path-like id")
	}
	if _, err := f.registry(t.TempDir()).Get("sdk_nope"); err == nil {
		t.Fatal("expected error for an unknown id")
	}
}

func TestReservationIsStartingAndLive(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	f.now = t0.Add(10 * time.Second)
	seedMeta(t, dir, sid, sub, 0, t0)
	r := f.registry(dir)
	s, err := r.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "starting" || !s.Live {
		t.Fatalf("want starting+live, got %+v", s)
	}
	if id, ok := r.LiveForSubject(sub); !ok || id != sid {
		t.Fatalf("LiveForSubject = %q %v", id, ok)
	}
}

func TestDeadHostNoHostFileFailsWithStderr(t *testing.T) {
	dir := t.TempDir()
	f := newFakes() // now = t0+60s, meta older than 30 s
	seedMeta(t, dir, sid, sub, 4242, t0)
	if err := os.WriteFile(filepath.Join(dir, sid, "host.err"), []byte("line1\nboom: cannot find module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.registry(dir)
	s, err := r.Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "failed" || s.Live || !strings.Contains(s.Reason, "boom: cannot find module") {
		t.Fatalf("want failed with stderr tail, got %+v", s)
	}
	if _, ok := r.LiveForSubject(sub); ok {
		t.Fatal("a failed session must not be live")
	}
}

func TestSlowHostWithoutHostFileStaysStarting(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	f.alive[4242] = true
	seedMeta(t, dir, sid, sub, 4242, t0)
	s, err := f.registry(dir).Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "starting" || !s.Live {
		t.Fatalf("want starting+live, got %+v", s)
	}
}

func TestDeadHostRunningOrIdleIsLost(t *testing.T) {
	for _, st := range []string{"running", "idle"} {
		dir := t.TempDir()
		f := newFakes()
		seedMeta(t, dir, sid, sub, 4242, t0)
		seedHost(t, dir, sid, HostFile{HostPID: 4242, ClaudeSessionID: uuid, State: st})
		s, err := f.registry(dir).Settle(sid)
		if err != nil {
			t.Fatal(err)
		}
		if s.State != "lost" || s.Live || s.ResumeCommand == "" {
			t.Fatalf("%s: want lost with resume, got %+v", st, s)
		}
		want := "cd /tmp/work && CLAUDE_CONFIG_DIR=/tmp/cell claude --resume " + uuid
		if s.ResumeCommand != want {
			t.Fatalf("resume = %q, want %q", s.ResumeCommand, want)
		}
	}
}

func orphanSetup(t *testing.T) (*Registry, *fakes) {
	dir := t.TempDir()
	f := newFakes()
	f.alive[4243] = true
	seedMeta(t, dir, sid, sub, 4242, t0)
	seedHost(t, dir, sid, HostFile{HostPID: 4242, ClaudePIDs: []int{4243}, ClaudeSessionID: uuid, State: "running"})
	return f.registry(dir), f
}

func TestOrphanProvedByNativeIsKilledOnce(t *testing.T) {
	r, f := orphanSetup(t)
	f.procs = []claudesession.Proc{{PID: 4243, Args: "claude --foo"}}
	f.native[4243] = claudesession.Native{PID: 4243, SessionID: uuid, Cwd: "/tmp/work"}
	s, err := r.Settle(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.killed) != 1 || f.killed[0] != 4243 {
		t.Fatalf("killed = %v", f.killed)
	}
	if s.Live || s.State != "lost" || s.ResumeCommand == "" {
		t.Fatalf("want lost, not live, with resume: %+v", s)
	}
}

// SIGTERM is asked, not obeyed: a claude still finishing its tool call after
// the kill keeps the session live, and no resume command is offered before it
// is gone (CA7). Found by spikes/sdk/g18-e2e.sh.
func TestOrphanStillAliveAfterTermKeepsSessionLive(t *testing.T) {
	r, f := orphanSetup(t)
	f.procs = []claudesession.Proc{{PID: 4243, Args: "claude --session-id " + uuid + " --print"}}
	r.Kill = func(pid int) error { f.killed = append(f.killed, pid); return nil } // the process outlives the signal
	s, err := r.Settle(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.killed) != 1 || !s.Live || s.ResumeCommand != "" {
		t.Fatalf("want live with no resume while claude lives: killed=%v %+v", f.killed, s)
	}
	f.alive[4243] = false // it exits
	s, _ = r.Settle(sid)
	if s.Live || s.ResumeCommand == "" {
		t.Fatalf("want resume once claude is gone: %+v", s)
	}
}

func TestOrphanProvedByArgvIsKilled(t *testing.T) {
	r, f := orphanSetup(t)
	f.procs = []claudesession.Proc{{PID: 4243, Args: "claude --session-id " + uuid + " --print"}}
	s, err := r.Settle(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.killed) != 1 || s.ResumeCommand == "" || s.Live {
		t.Fatalf("killed=%v session=%+v", f.killed, s)
	}
}

func TestUnprovedClaudeIsNeverKilled(t *testing.T) {
	r, f := orphanSetup(t)
	f.procs = []claudesession.Proc{{PID: 4243, Args: "claude"}} // no native, no --session-id
	s, err := r.Settle(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.killed) != 0 || s.ResumeCommand != "" || !s.Live || s.State != "lost" || s.Reason == "" {
		t.Fatalf("killed=%v session=%+v", f.killed, s)
	}
	// B3: it still counts as a writer for the subject.
	if id, ok := r.LiveForSubject(sub); !ok || id != sid {
		t.Fatalf("LiveForSubject = %q %v", id, ok)
	}
}

func TestRecycledPidNotWriter(t *testing.T) {
	// Case 1: the pid is alive but is not a claude process.
	r, f := orphanSetup(t)
	f.procs = nil
	s, _ := r.Settle(sid)
	if s.Live || s.ResumeCommand == "" || s.State != "lost" || len(f.killed) != 0 {
		t.Fatalf("not-claude pid: %+v killed=%v", s, f.killed)
	}
	if _, ok := r.LiveForSubject(sub); ok {
		t.Fatal("a recycled pid is not a writer")
	}
	// Case 2: claude, but its native file names another cwd.
	r, f = orphanSetup(t)
	f.procs = []claudesession.Proc{{PID: 4243, Args: "claude"}}
	f.native[4243] = claudesession.Native{PID: 4243, SessionID: uuid, Cwd: "/elsewhere"}
	s, _ = r.Settle(sid)
	if s.Live || s.ResumeCommand == "" || len(f.killed) != 0 {
		t.Fatalf("other cwd: %+v killed=%v", s, f.killed)
	}
	// Case 3: claude, but another session id.
	r, f = orphanSetup(t)
	f.procs = []claudesession.Proc{{PID: 4243, Args: "claude"}}
	f.native[4243] = claudesession.Native{PID: 4243, SessionID: "other", Cwd: "/tmp/work"}
	s, _ = r.Settle(sid)
	if s.Live || len(f.killed) != 0 {
		t.Fatalf("other session: %+v killed=%v", s, f.killed)
	}
}

func TestLiveWriterHasNoResumeCommand(t *testing.T) {
	for _, st := range []string{"running", "idle", "waiting"} {
		dir := t.TempDir()
		f := newFakes()
		f.alive[4242] = true
		seedMeta(t, dir, sid, sub, 4242, t0)
		seedHost(t, dir, sid, HostFile{HostPID: 4242, ClaudeSessionID: uuid, State: st})
		s, err := f.registry(dir).Settle(sid)
		if err != nil {
			t.Fatal(err)
		}
		if s.ResumeCommand != "" || !s.Live || s.State != st {
			t.Fatalf("%s: %+v", st, s)
		}
	}
}

func TestEndedHostGivesResume(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	seedMeta(t, dir, sid, sub, 4242, t0)
	seedHost(t, dir, sid, HostFile{HostPID: 4242, ClaudeSessionID: uuid, State: "ended"})
	s, _ := f.registry(dir).Get(sid)
	if s.State != "ended" || s.Live || s.ResumeCommand == "" {
		t.Fatalf("%+v", s)
	}
}

func TestEventsAfter(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	seedMeta(t, dir, sid, sub, 0, t0)
	body := `{"seq":1,"at":"x","kind":"a","data":{}}` + "\n" + `{"seq":2,"at":"x","kind":"b","data":{}}` + "\n" +
		`{"seq":3,"at":"x","kind":"c","data":{}}` + "\n" + `{"seq":4,"at":"x","ki` // torn last line
	if err := os.WriteFile(filepath.Join(dir, sid, "events.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ev, err := f.registry(dir).Events(sid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 || !strings.Contains(string(ev[0]), `"seq":2`) {
		t.Fatalf("events = %s", ev)
	}
	ev, err = f.registry(dir).Events(sid, 0)
	if err != nil || len(ev) != 3 {
		t.Fatalf("after 0: %d %v", len(ev), err)
	}
}

func TestLiveForSubjectOnlyLiveStates(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	seedMeta(t, dir, "sdk_aaaa", sub, 1, t0)
	seedHost(t, dir, "sdk_aaaa", HostFile{HostPID: 1, State: "ended"})
	seedMeta(t, dir, "sdk_bbbb", sub, 2, t0)
	seedHost(t, dir, "sdk_bbbb", HostFile{HostPID: 2, State: "idle"})
	f.alive[2] = true
	r := f.registry(dir)
	if id, ok := r.LiveForSubject(sub); !ok || id != "sdk_bbbb" {
		t.Fatalf("LiveForSubject = %q %v", id, ok)
	}
	if _, ok := r.LiveForSubject("sub_other"); ok {
		t.Fatal("another subject must not match")
	}
}

func TestSettleSubjectTouchesOnlyItsNonTerminalSessions(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	// Subject's non-terminal orphan session.
	seedMeta(t, dir, "sdk_aaaa", sub, 1, t0)
	seedHost(t, dir, "sdk_aaaa", HostFile{HostPID: 1, ClaudePIDs: []int{11}, ClaudeSessionID: uuid, State: "running"})
	f.alive[11] = true
	f.procs = append(f.procs, claudesession.Proc{PID: 11, Args: "claude --session-id " + uuid})
	// Subject's terminal session: left alone.
	seedMeta(t, dir, "sdk_bbbb", sub, 2, t0)
	seedHost(t, dir, "sdk_bbbb", HostFile{HostPID: 2, ClaudePIDs: []int{22}, State: "ended"})
	f.alive[22] = true
	f.procs = append(f.procs, claudesession.Proc{PID: 22, Args: "claude --session-id " + uuid})
	// Another subject's orphan.
	seedMeta(t, dir, "sdk_cccc", "sub_other", 3, t0)
	seedHost(t, dir, "sdk_cccc", HostFile{HostPID: 3, ClaudePIDs: []int{33}, ClaudeSessionID: uuid, State: "running"})
	f.alive[33] = true
	f.procs = append(f.procs, claudesession.Proc{PID: 33, Args: "claude --session-id " + uuid})
	if err := f.registry(dir).SettleSubject(sub); err != nil {
		t.Fatal(err)
	}
	if len(f.killed) != 1 || f.killed[0] != 11 {
		t.Fatalf("killed = %v, want only [11]", f.killed)
	}
}

func TestListFiltersBySubject(t *testing.T) {
	dir := t.TempDir()
	f := newFakes()
	seedMeta(t, dir, "sdk_aaaa", sub, 0, t0)
	seedMeta(t, dir, "sdk_bbbb", "sub_other", 0, t0.Add(time.Second))
	r := f.registry(dir)
	all, err := r.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d %v", len(all), err)
	}
	one, _ := r.List(sub)
	if len(one) != 1 || one[0].ID != "sdk_aaaa" {
		t.Fatalf("one = %+v", one)
	}
	// A missing dir is an empty list, not an error.
	empty, err := (&Registry{Dir: filepath.Join(dir, "nope")}).List("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing dir: %v %v", empty, err)
	}
}

// CA3 fail-safe: a meta.json that cannot be read or parsed is a session whose
// state is unknown, never "not live".
func TestCorruptMetaReadsAsLive(t *testing.T) {
	dir := t.TempDir()
	r := newFakes().registry(dir)
	if err := os.MkdirAll(filepath.Join(dir, "sdk_bad"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sdk_bad", "meta.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.List(""); err == nil || !strings.Contains(err.Error(), "sdk_bad") {
		t.Fatalf("List must report the unreadable session, got %v", err)
	}
	id, ok := r.LiveForSubject(sub)
	if !ok || !strings.Contains(id, "unreadable") {
		t.Fatalf("LiveForSubject = %q %v, want live with a reason", id, ok)
	}
}

func TestUnreadableDirReadsAsLive(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newFakes().registry(f) // ReadDir on a file: ENOTDIR, not "absent"
	id, ok := r.LiveForSubject(sub)
	if !ok || !strings.Contains(id, "unreadable") {
		t.Fatalf("LiveForSubject = %q %v, want live with a reason", id, ok)
	}
}

func TestAbsentDirIsNotLive(t *testing.T) {
	r := newFakes().registry(filepath.Join(t.TempDir(), "nope"))
	if id, ok := r.LiveForSubject(sub); ok {
		t.Fatalf("an absent dir has no session: %q", id)
	}
}

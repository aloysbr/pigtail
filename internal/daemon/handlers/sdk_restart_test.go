package handlers

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// restartHost is a fake SDK host: the host.sock of one session directory, the
// NDJSON line protocol of clients/sdkhost, and the commands it received. It is
// the only thing that outlives the "restart" below, like the real host does.
type restartHost struct {
	mu   sync.Mutex
	got  []map[string]any
	ln   net.Listener
	done chan struct{}
}

func startRestartHost(t *testing.T, sock string) *restartHost {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	h := &restartHost{ln: ln, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, err := bufio.NewReader(c).ReadBytes('\n')
			if err == nil {
				var cmd map[string]any
				if json.Unmarshal(line, &cmd) == nil {
					h.mu.Lock()
					h.got = append(h.got, cmd)
					h.mu.Unlock()
				}
				_, _ = c.Write([]byte(`{"ok":true}` + "\n"))
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-h.done })
	return h
}

func (h *restartHost) commands() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.got...)
}

// writeHostFiles lays out what the host leaves on disk for session id.
func writeHostFiles(t *testing.T, dir, id, subject, state string, turn int, requests []sdksession.Request) {
	t.Helper()
	sd := filepath.Join(dir, id)
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(name string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("meta.json", sdksession.Meta{Version: 1, ID: id, SubjectID: subject, Cwd: "/work/p", ConfigDir: "/cell/mp",
		Claude: "/bin/claude", Mode: "default", Prompt: "go", SessionUUID: "csid-" + id, HostPID: os.Getpid(), CreatedAt: sdkT0})
	put("host.json", sdksession.HostFile{Version: 1, HostPID: os.Getpid(), ClaudePIDs: []int{}, ClaudeSessionID: "csid-" + id,
		State: state, Turn: turn, Requests: requests, UpdatedAt: sdkT0.Add(time.Minute)})
	events := `{"seq":1,"at":"2026-10-29T10:00:01Z","kind":"init","data":{"session_id":"csid-` + id + `"}}` + "\n" +
		`{"seq":2,"at":"2026-10-29T10:00:05Z","kind":"state","data":{"state":"` + state + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sd, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
}

// restart is pigtaild going down and coming back: the Registry of the old
// process is thrown away, a new one is built over the same directory, and the
// routes now run on it. Nothing but the files and the live host carries over.
func (f *sdkFixture) restart(dir string) {
	f.svc.SDK.Registry = &sdksession.Registry{Dir: dir}
}

// CA4, CA6: pigtaild restarted while the session was `waiting` re-reads the
// state, the pending request and the journal from the host's files, and an
// answer given after the restart reaches the host that kept waiting.
func TestSDKRestartDuringWaitingServesTheAnswer(t *testing.T) {
	f := newSDKFixture(t)
	dir := testutil.MkdirTemp(t, "sdkr-")
	req := []sdksession.Request{{Rid: "r1", Kind: "question", Tool: "AskUserQuestion", Input: json.RawMessage(reqInput), OpenedAt: sdkT0.Add(5 * time.Second)}}
	writeHostFiles(t, dir, "sdk_p1", "sub_p", "waiting", 0, req)
	host := startRestartHost(t, (&sdksession.Registry{Dir: dir}).SockPath("sdk_p1"))

	f.restart(dir)

	rec := sdkDo(t, f.r, "", true, "", "GET", "/sdk-sessions/sdk_p1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get after restart = %d %s", rec.Code, rec.Body)
	}
	got := struct{ Data pigtail.SDKSession }{decodeSDKSession(t, rec.Body.Bytes())}
	if got.Data.State != "waiting" || !got.Data.Live || len(got.Data.Pending) != 1 || got.Data.Pending[0].Rid != "r1" || got.Data.ResumeCommand != "" {
		t.Fatalf("state re-read from the files = %+v", got.Data)
	}
	if rec := sdkDo(t, f.r, "", true, "", "GET", "/sdk-sessions/sdk_p1/events?after=1", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"seq":2`) {
		t.Fatalf("journal after restart = %d %s", rec.Code, rec.Body)
	}

	rec = sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p1/requests/r1/answer", `{"behavior":"allow","answers":{"Which fruits?":["Apple"]}}`)
	cmds := host.commands()
	if rec.Code != http.StatusOK || len(cmds) != 1 || cmds[0]["op"] != "answer" || cmds[0]["rid"] != "r1" || cmds[0]["behavior"] != "allow" {
		t.Fatalf("answer after restart = %d %s, host got %v", rec.Code, rec.Body, cmds)
	}
}

// The same restart while the session was `idle`: it stays idle, offers no
// resume command (the host is the only writer), and a message is served as the
// next turn.
func TestSDKRestartDuringIdleServesTheMessage(t *testing.T) {
	f := newSDKFixture(t)
	dir := testutil.MkdirTemp(t, "sdkr-")
	writeHostFiles(t, dir, "sdk_t1", "sub_t", "idle", 1, nil)
	host := startRestartHost(t, (&sdksession.Registry{Dir: dir}).SockPath("sdk_t1"))

	f.restart(dir)

	rec := sdkDo(t, f.r, "", true, "", "GET", "/sdk-sessions/sdk_t1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get after restart = %d %s", rec.Code, rec.Body)
	}
	s := decodeSDKSession(t, rec.Body.Bytes())
	if s.State != "idle" || !s.Live || s.ResumeCommand != "" || s.Turn != 1 {
		t.Fatalf("state re-read from the files = %+v", s)
	}

	rec = sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_t1/messages", map[string]string{"text": "second turn"})
	cmds := host.commands()
	if rec.Code != http.StatusOK || len(cmds) != 1 || cmds[0]["op"] != "say" || cmds[0]["text"] != "second turn" {
		t.Fatalf("message after restart = %d %s, host got %v", rec.Code, rec.Body, cmds)
	}
}

// decodeSDKSession reads a session out of the body of a GET route.
func decodeSDKSession(t *testing.T, body []byte) pigtail.SDKSession {
	t.Helper()
	var s pigtail.SDKSession
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return s
}

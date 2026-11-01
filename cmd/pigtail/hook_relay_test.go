package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// envResponder mirrors the daemon's {data}/{error} envelope.
type envResponder struct{}

func (envResponder) JSON(w http.ResponseWriter, _ *http.Request, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (envResponder) Error(w http.ResponseWriter, _ *http.Request, status int, code pigtail.ErrorCode, msg string, _ map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

// startRelayDaemon serves the relay routes on a unix socket and points this
// CLI at it (flagSocket). The marker dir is <socket dir>/inbox, as in pigtaild.
func startRelayDaemon(t *testing.T) (*pigtail.Client, string) {
	t.Helper()
	c, dir, _ := startRelayDaemonStore(t)
	return c, dir
}

// startRelayDaemonStore is startRelayDaemon that also hands back the test
// daemon's store, for what no route reads back.
func startRelayDaemonStore(t *testing.T) (*pigtail.Client, string, *store.Store) {
	t.Helper()
	testutil.SerialOnly(t)                   // swaps flagSocket
	dir := testutil.MkdirTemp(t, "ptrelay-") // short: unix socket paths are capped near 104 bytes
	sock := filepath.Join(dir, "p.sock")
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rl := handlers.NewRelay(&handlers.Services{Store: st, Logf: t.Logf}, handlers.RelayConfig{
		MarkerDir:     filepath.Join(dir, "inbox"),
		WaitMarkerDir: filepath.Join(dir, "wait"),
		Alive:         func(p pigtail.PeerSession) bool { return p.EndedAt == nil },
	})
	r := chi.NewRouter()
	handlers.RegisterRelayRoutes(r, rl, envResponder{})
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	old := flagSocket
	flagSocket = sock
	t.Cleanup(func() { flagSocket = old })
	return pigtail.NewSocketClient(sock), dir, st
}

func noEnv(string) string { return "" }

func TestSessionHookRegistersAndEnds(t *testing.T) {
	c, _ := startRelayDaemon(t)
	c = c.WithScope(string(scope.Master())) // no cell: an unscoped session, the master's only (D17)
	runSessionHook(strings.NewReader(`{"session_id":"s-hook","cwd":"/w","hook_event_name":"SessionStart"}`), noEnv, false)
	ps, err := c.Peers(context.Background(), false)
	if err != nil || len(ps) != 1 || ps[0].SessionID != "s-hook" || ps[0].Cwd != "/w" || ps[0].Source != "hook" {
		t.Fatalf("peers = %+v %v", ps, err)
	}
	runSessionHook(strings.NewReader(`{"session_id":"s-hook"}`), noEnv, true)
	ps, _ = c.Peers(context.Background(), false)
	if len(ps) != 0 {
		t.Fatalf("ended session still live: %+v", ps)
	}
}

func seedTwoPeers(t *testing.T, c *pigtail.Client) {
	t.Helper()
	_, err := c.PostPeers(context.Background(), pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: "s-a", CmuxTitle: "Alpha", Name: "globex-a1", Scope: "globex", CellID: "mt"},
		{SessionID: "s-b", CmuxTitle: "Beta", Name: "globex-b2", Scope: "globex", CellID: "it"},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInboxHookFastPathAndInjection(t *testing.T) {
	c, dir := startRelayDaemon(t)
	seedTwoPeers(t, c)
	payload := `{"session_id":"s-b","hook_event_name":"UserPromptSubmit"}`

	var out bytes.Buffer
	runInboxHook(strings.NewReader(payload), &out, noEnv)
	if out.Len() != 0 {
		t.Fatalf("empty inbox printed: %s", out.String())
	}
	if _, err := c.SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-a", To: "beta", Kind: "request", Body: "releasing port 3030"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "inbox", "s-b")); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	runInboxHook(strings.NewReader(payload), &out, noEnv)
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	ctxt := got.HookSpecificOutput.AdditionalContext
	if got.HookSpecificOutput.HookEventName != "UserPromptSubmit" || !strings.Contains(ctxt, "releasing port 3030") ||
		!strings.Contains(ctxt, "NOT from the user") || !strings.Contains(ctxt, `from="Alpha · globex-a1 (mt)"`) {
		t.Fatalf("context = %s", ctxt)
	}
	ms, _ := c.Messages(context.Background(), pigtail.MessageFilter{To: "s-b"})
	if len(ms) != 1 || ms[0].DeliveredVia != "prompt" {
		t.Fatalf("delivery = %+v", ms)
	}
	out.Reset()
	runInboxHook(strings.NewReader(payload), &out, noEnv)
	if out.Len() != 0 {
		t.Fatalf("delivered twice: %s", out.String())
	}
	// Kill switch.
	_, _ = c.SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-a", To: "beta", Body: "again"})
	runInboxHook(strings.NewReader(payload), &out, func(k string) string {
		if k == "PIGTAIL_RELAY" {
			return "off"
		}
		return ""
	})
	if out.Len() != 0 {
		t.Fatal("PIGTAIL_RELAY=off still injected")
	}
}

// firstWait returns a waitDeps.waiting seam and a channel it closes the
// first time waitLoop sends its long poll.
func firstWait() (func(), <-chan struct{}) {
	ch := make(chan struct{})
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }, ch
}

func TestWaitLoopWakesOnMessage(t *testing.T) {
	c, _ := startRelayDaemon(t)
	seedTwoPeers(t, c)
	waiting, inWait := firstWait()
	deps := waitDeps{client: c, alive: func() bool { return true }, sleep: func(time.Duration) {}, ownerOK: func() bool { return true }, waiting: waiting}
	go func() {
		<-inWait
		_, _ = c.SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-a", To: "beta", Body: "server restarted"})
	}()
	var stderr bytes.Buffer
	code := waitLoop("s-b", &stderr, deps, time.Now, time.Now().Add(20*time.Second))
	if code != 2 || !strings.Contains(stderr.String(), "server restarted") || !strings.Contains(stderr.String(), "while you were idle") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	ms, _ := c.Messages(context.Background(), pigtail.MessageFilter{To: "s-b"})
	if len(ms) != 1 || ms[0].DeliveredVia != "rewake" {
		t.Fatalf("delivery = %+v", ms)
	}
}

func TestWaitLoopExitsQuietly(t *testing.T) {
	c, _ := startRelayDaemon(t)
	seedTwoPeers(t, c)
	var stderr bytes.Buffer
	dead := waitDeps{client: c, alive: func() bool { return false }, sleep: func(time.Duration) {}, ownerOK: func() bool { return true }}
	if code := waitLoop("s-b", &stderr, dead, time.Now, time.Now().Add(time.Minute)); code != 0 {
		t.Fatalf("dead claude: code %d", code)
	}
	replaced := waitDeps{client: c, alive: func() bool { return true }, sleep: func(time.Duration) {}, ownerOK: func() bool { return false }}
	if code := waitLoop("s-b", &stderr, replaced, time.Now, time.Now().Add(time.Minute)); code != 0 {
		t.Fatalf("replaced watcher: code %d", code)
	}
	// Superseded by a newer waiter on the daemon side.
	done := make(chan int, 1)
	waiting, inWait := firstWait()
	live := waitDeps{client: c, alive: func() bool { return true }, sleep: func(time.Duration) {}, ownerOK: func() bool { return true }, waiting: waiting}
	go func() { done <- waitLoop("s-b", &stderr, live, time.Now, time.Now().Add(time.Minute)) }()
	// The newer waiter leaves once the loop's poll is sent. Both polls race
	// to the daemon: when the newer one registers first, the loop's
	// supersedes it, and it comes back until the loop has left.
	left := make(chan struct{})
	defer close(left)
	go func() {
		<-inWait
		for {
			_, _ = c.WaitMessages(context.Background(), "s-b", time.Second)
			select {
			case <-left:
				return
			default:
			}
		}
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("superseded: code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("superseded watcher did not leave")
	}
	if stderr.Len() != 0 {
		t.Fatalf("quiet exits wrote: %s", stderr.String())
	}
}

func TestWaitLoopLeavesPromptlyWhenClaudeDies(t *testing.T) {
	c, _ := startRelayDaemon(t)
	seedTwoPeers(t, c)
	old := livenessCheck
	livenessCheck = 50 * time.Millisecond
	t.Cleanup(func() { livenessCheck = old })
	var mu sync.Mutex
	alive := true
	var diedAt time.Time
	waiting, inWait := firstWait()
	deps := waitDeps{client: c, sleep: func(time.Duration) {}, ownerOK: func() bool { return true },
		alive: func() bool { mu.Lock(); defer mu.Unlock(); return alive }, waiting: waiting}
	// claude dies while the loop waits on the daemon.
	go func() {
		<-inWait
		mu.Lock()
		alive = false
		diedAt = time.Now()
		mu.Unlock()
	}()
	if code := waitLoop("s-b", &bytes.Buffer{}, deps, time.Now, time.Now().Add(time.Minute)); code != 0 {
		t.Fatalf("code %d", code)
	}
	mu.Lock()
	died := diedAt
	mu.Unlock()
	if died.IsZero() {
		t.Fatal("the loop left before claude died")
	}
	if el := time.Since(died); el > 3*time.Second {
		t.Fatalf("took %s to notice the dead claude", el)
	}
}

func TestRunInboxWaitSkipsWithoutClaude(t *testing.T) {
	startRelayDaemon(t)
	env := map[string]string{"CLAUDE_CONFIG_DIR": t.TempDir()}
	code := runInboxWait(strings.NewReader(`{"session_id":"s-x"}`), &bytes.Buffer{}, func(k string) string { return env[k] }, time.Now)
	if code != 0 {
		t.Fatalf("no claude process: code %d", code)
	}
	env["PIGTAIL_RELAY_REWAKE"] = "off"
	if code := runInboxWait(strings.NewReader(`{"session_id":"s-x"}`), &bytes.Buffer{}, func(k string) string { return env[k] }, time.Now); code != 0 {
		t.Fatalf("kill switch: code %d", code)
	}
}

func TestWatcherOwnership(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ".wait-s")
	if err := claimWatcher(path); err != nil || !ownsWatcher(path) {
		t.Fatalf("claim: %v", err)
	}
	_ = os.WriteFile(path, []byte("1"), 0o600) // a newer watcher took over
	if ownsWatcher(path) {
		t.Fatal("still owner after takeover")
	}
	releaseWatcher(path) // must not remove the newer watcher's file
	if _, err := os.Stat(path); err != nil {
		t.Fatal("released someone else's ownership")
	}
}

// D16 (Tâche 10): the Stop watcher never wakes a session in Standby — the
// long poll claims nothing, the watcher leaves at its budget with nothing on
// stderr; out of Standby it wakes it (exit 2).
func TestInboxWaitNoWakeInStandby(t *testing.T) {
	c, _, st := startRelayDaemonStore(t)
	seedTwoPeers(t, c)
	if err := st.SetStandby("s-b", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-a", To: "beta", Body: "server restarted"}); err != nil {
		t.Fatal(err)
	}
	deps := waitDeps{client: c, alive: func() bool { return true }, sleep: func(time.Duration) {}, ownerOK: func() bool { return true }}
	var stderr bytes.Buffer
	if code := waitLoop("s-b", &stderr, deps, time.Now, time.Now().Add(2500*time.Millisecond)); code != 0 || stderr.Len() != 0 {
		t.Fatalf("in Standby: code=%d stderr=%q", code, stderr.String())
	}
	if _, err := st.ClearStandby("s-b", time.Now()); err != nil {
		t.Fatal(err)
	}
	code := waitLoop("s-b", &stderr, deps, time.Now, time.Now().Add(20*time.Second))
	if code != 2 || !strings.Contains(stderr.String(), "server restarted") {
		t.Fatalf("out of Standby: code=%d stderr=%q", code, stderr.String())
	}
}

// D16 (Tâche 10): claude-inbox claims nothing for a session in Standby
// except on a prompt Ada typed — not on the /compact pigtail types, not on
// a tool call, not on a turnless command; an unreadable marker holds too.
func TestInboxHookHoldsInStandby(t *testing.T) {
	withWaitCommands(t, waittype.Commands{Turnless: []*regexp.Regexp{regexp.MustCompile(`(?i)^\s*/(?:show|clip|rename|pin)(?:\s|$)`)}})
	s := startStandbyServer(t)
	c := pigtail.NewSocketClient(flagSocket)
	seedTwoPeers(t, c)
	putStandby(t, "s-b")
	send := func(body string) {
		t.Helper()
		if _, err := c.SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-a", To: "beta", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	claims := func() int { return s.count("POST /messages/claim") }
	hook := func(event, prompt string, env func(string) string) string {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"session_id": "s-b", "hook_event_name": event, "prompt": prompt})
		var out bytes.Buffer
		runInboxHook(bytes.NewReader(b), &out, env)
		return out.String()
	}
	send("one")
	for _, cs := range []struct{ event, prompt string }{
		{"UserPromptSubmit", "/compact"}, {"PostToolUse", ""}, {"UserPromptSubmit", "/show 2"},
	} {
		if out := hook(cs.event, cs.prompt, noEnv); out != "" || claims() != 0 {
			t.Fatalf("%s %q: out=%q claims=%d", cs.event, cs.prompt, out, claims())
		}
	}
	out := hook("UserPromptSubmit", "reprends", noEnv)
	if claims() != 1 || !strings.Contains(out, "additionalContext") || !strings.Contains(out, "one") {
		t.Fatalf("reprends: out=%q claims=%d", out, claims())
	}
	ms, _ := c.Messages(context.Background(), pigtail.MessageFilter{To: "s-b"})
	if len(ms) != 1 || ms[0].DeliveredVia != "prompt" {
		t.Fatalf("delivery = %+v", ms)
	}
	send("two")
	notADir := filepath.Join(s.dir, "p.sock") // a file: the marker cannot be read
	if out := hook("PostToolUse", "", envOf(map[string]string{"PIGTAIL_STANDBY_DIR": filepath.Join(notADir, "x")})); out != "" || claims() != 1 {
		t.Fatalf("unreadable marker: out=%q claims=%d", out, claims())
	}
	if err := runSessionStandby(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-b"}), true, false); err != nil {
		t.Fatal(err)
	}
	if out := hook("PostToolUse", "", noEnv); claims() != 2 || !strings.Contains(out, "two") {
		t.Fatalf("out of Standby: out=%q claims=%d", out, claims())
	}
}

// SessionEnd removes the session's Stop record (D42); a missing one is no
// error, and an id that climbs out of the directory removes nothing. Under a
// test daemon: the end branch posts to it, never to the real one.
func TestSessionEndRemovesBgRecord(t *testing.T) {
	_, sockDir := startRelayDaemon(t)
	dir := filepath.Join(sockDir, "bgwork")
	writeBgRecordFile(t, dir, "s-end", `{"session_id":"s-end","pid":1,"turn":"p","session_crons":[]}`)
	runSessionHook(strings.NewReader(`{"session_id":"s-end","hook_event_name":"SessionEnd"}`), noEnv, true)
	if _, err := os.Stat(filepath.Join(dir, "s-end")); err == nil {
		t.Fatal("SessionEnd left the record")
	}
	runSessionHook(strings.NewReader(`{"session_id":"s-end","hook_event_name":"SessionEnd"}`), noEnv, true) // already gone: no panic
	witness := filepath.Join(sockDir, "x")
	if err := os.WriteFile(witness, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSessionHook(strings.NewReader(`{"session_id":"../x","hook_event_name":"SessionEnd"}`), noEnv, true)
	if _, err := os.Stat(witness); err != nil {
		t.Fatalf("../x removed a file outside the directory: %v", err)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The daemon lets a caller act as a relay session (claim, wait, ack, send as)
// only when X-Pigtail-Scope equals the scope the registry holds for that
// session. The registry takes that scope from the session's cell, so every
// session-bound call of the CLI and its hooks must send the same: the cell of
// CLAUDE_CONFIG_DIR, work outside any cell. A perso session sending nothing
// (= work) would lose its own inbox — these tests are the proof it does not.

func TestSessionScopeFollowsTheCellOnly(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	workDir := os.Getenv("XDG_CONFIG_HOME") + "/work"
	persoDir := os.Getenv("CLAUDE_CONFIG_DIR")
	for dir, want := range map[string]string{
		persoDir:               "perso",
		persoDir + "/":         "perso", // same cell, spelt with a trailing slash
		workDir:                "globex",
		t.TempDir():            "globex", // no cell
		"/nonexistent/.claude": "globex",
	} {
		env := map[string]string{"CLAUDE_CONFIG_DIR": dir, envScope: "perso"} // the override never counts
		if got := sessionScope(func(k string) string { return env[k] }); got != want {
			t.Errorf("sessionScope(%s) = %s, want %s", dir, got, want)
		}
	}
}

// seedPersoPair registers two perso sessions through their own SessionStart
// hook, and sends a message from s-pb to s-pa as s-pb would.
func seedPersoPair(t *testing.T, c *pigtail.Client, persoEnv func(string) string) string {
	t.Helper()
	for _, id := range []string{"s-pa", "s-pb"} {
		runSessionHook(strings.NewReader(`{"session_id":"`+id+`","cwd":"/w/`+id+`"}`), persoEnv, false)
	}
	res, err := c.WithScope("perso").SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-pb", To: "s-pa", Body: "perso inbox works"})
	if err != nil {
		t.Fatalf("perso send: %v", err)
	}
	return res.Message.ID
}

func TestPersoHooksKeepTheirInbox(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	c, _ := startRelayDaemon(t)
	persoEnv := func(k string) string { return os.Getenv(k) }
	id := seedPersoPair(t, c, persoEnv)

	// The registry holds the sessions as perso, from their own cell.
	if p, err := c.WithScope("perso").Peers(context.Background(), false); err != nil || len(p) != 2 || p[0].Scope != "perso" {
		t.Fatalf("perso peers = %+v %v", p, err)
	}
	// A work hook claiming s-pa's inbox gets nothing and consumes nothing.
	workEnv := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return os.Getenv("XDG_CONFIG_HOME") + "/work"
		}
		return os.Getenv(k)
	}
	var out bytes.Buffer
	runInboxHook(strings.NewReader(`{"session_id":"s-pa","hook_event_name":"UserPromptSubmit"}`), &out, workEnv)
	if out.Len() != 0 {
		t.Fatalf("a work hook read a perso inbox: %s", out.String())
	}
	if m, err := c.WithScope("perso").Message(context.Background(), id); err != nil || m.State != pigtail.MessageStatePending {
		t.Fatalf("work hook touched the perso message: %+v %v", m, err)
	}
	// The perso session's own hook injects it.
	runInboxHook(strings.NewReader(`{"session_id":"s-pa","hook_event_name":"UserPromptSubmit"}`), &out, persoEnv)
	if !strings.Contains(out.String(), "perso inbox works") {
		t.Fatalf("perso hook did not inject its own message: %q", out.String())
	}
	// SessionEnd from a work env does not end it; from its own cell it does.
	runSessionHook(strings.NewReader(`{"session_id":"s-pb"}`), workEnv, true)
	if p, _ := c.WithScope("perso").Peers(context.Background(), false); len(p) != 2 {
		t.Fatalf("a work SessionEnd ended a perso session: %+v", p)
	}
	runSessionHook(strings.NewReader(`{"session_id":"s-pb"}`), persoEnv, true)
	if p, _ := c.WithScope("perso").Peers(context.Background(), false); len(p) != 1 {
		t.Fatalf("perso SessionEnd did not end its session: %+v", p)
	}
}

func TestPersoWatcherWakesOnItsMessage(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	c, _ := startRelayDaemon(t)
	persoEnv := func(k string) string { return os.Getenv(k) }
	for _, id := range []string{"s-pa", "s-pb"} {
		runSessionHook(strings.NewReader(`{"session_id":"`+id+`"}`), persoEnv, false)
	}
	waiting, inWait := firstWait()
	deps := waitDeps{client: sessionClient(persoEnv), alive: func() bool { return true }, sleep: func(time.Duration) {}, ownerOK: func() bool { return true }, waiting: waiting}
	go func() {
		<-inWait
		_, _ = c.WithScope("perso").SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-pb", To: "s-pa", Body: "wake, perso"})
	}()
	var stderr bytes.Buffer
	if code := waitLoop("s-pa", &stderr, deps, time.Now, time.Now().Add(20*time.Second)); code != 2 || !strings.Contains(stderr.String(), "wake, perso") {
		t.Fatalf("perso watcher: code=%d stderr=%s", code, stderr.String())
	}
}

// A watcher whose session the daemon does not know (SessionStart ran while the
// daemon was down, a restored database) registers it and keeps waiting.
func TestWatcherRegistersAnUnknownSession(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	c, _ := startRelayDaemon(t)
	persoEnv := func(k string) string { return os.Getenv(k) }
	runSessionHook(strings.NewReader(`{"session_id":"s-pb"}`), persoEnv, false)
	registered := 0
	deps := waitDeps{client: sessionClient(persoEnv), alive: func() bool { return true }, sleep: func(time.Duration) {}, ownerOK: func() bool { return true },
		register: func() {
			registered++
			runSessionHook(strings.NewReader(`{"session_id":"s-pa"}`), persoEnv, false)
		}}
	go func() {
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if _, err := c.WithScope("perso").SendMessage(context.Background(), pigtail.SendMessageRequest{From: "s-pb", To: "s-pa", Body: "found you"}); err == nil {
				return
			}
		}
	}()
	var stderr bytes.Buffer
	if code := waitLoop("s-pa", &stderr, deps, time.Now, time.Now().Add(20*time.Second)); code != 2 || registered == 0 {
		t.Fatalf("code=%d registered=%d stderr=%s", code, registered, stderr.String())
	}
}

// `pigtail msg inbox|ack|send|reply` from a perso cell act as the perso session.
func TestMsgCommandsFromAPersoCell(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	c, _ := startRelayDaemon(t)
	persoEnv := func(k string) string { return os.Getenv(k) }
	id := seedPersoPair(t, c, persoEnv)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-pa")
	t.Setenv("PIGTAIL_RELAY_NOTIFY", "off")
	// The test is about which session each command acts as, not about the
	// 5 s budget: under load the snapshot (a process listing) can eat it.
	oldTimeout := msgTimeout
	msgTimeout = time.Minute
	t.Cleanup(func() { msgTimeout = oldTimeout })

	run := func(args ...string) (string, error) {
		cmd := msgCmd()
		var out bytes.Buffer
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := run("inbox"); err != nil || !strings.Contains(out, "perso inbox works") {
		t.Fatalf("msg inbox = %q %v", out, err)
	}
	if out, err := run("ack", id); err != nil || !strings.Contains(out, "acked "+id) {
		t.Fatalf("msg ack = %q %v", out, err)
	}
	if out, err := run("send", "s-pb", "back to you"); err != nil || !strings.Contains(out, "sent msg_") {
		t.Fatalf("msg send = %q %v", out, err)
	}
	if out, err := run("reply", id, "thanks"); err != nil || !strings.Contains(out, "sent msg_") {
		t.Fatalf("msg reply = %q %v", out, err)
	}

	// From a work cell, the same session id is unknown.
	t.Setenv("CLAUDE_CONFIG_DIR", os.Getenv("XDG_CONFIG_HOME")+"/work")
	_, err := run("ack", id)
	var apiErr *pigtail.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrNotFound {
		t.Fatalf("work msg ack on a perso session = %v, want NOT_FOUND", err)
	}
}

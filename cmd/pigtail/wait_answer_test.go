package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail wait answer` (sub_x000000000000026, Task 10): refused in an agent
// session before any call; the answer's forms and the cross-scope opt-in go
// to POST /waits/{id}/answer.

type answerStub struct {
	mu    sync.Mutex
	calls int
	path  string
	cross string
	body  pigtail.WaitAnswerRequest
}

func (s *answerStub) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls++
	s.path = r.Method + " " + r.URL.Path
	s.cross = r.Header.Get(pigtail.HeaderCrossScope)
	_ = json.Unmarshal(raw, &s.body)
	s.mu.Unlock()
	writeData(w, http.StatusOK, pigtail.WaitAnswerResponse{
		Wait:      pigtail.Wait{ID: "wait_1", SessionID: "s-arb", SessionLabel: "Pigtail · alpha", State: pigtail.WaitStateAnswered},
		MessageID: "msg_1", Delivery: pigtail.DeliveryWaiter, StandbyEnded: true,
	})
}

func runWaitAnswerCmd(t *testing.T, s *answerStub, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	var err error
	withStubDaemon(t, &stubDaemon{srv: srv}, func() {
		cmd := waitCmd()
		cmd.SetArgs(append([]string{"answer"}, args...))
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	return out.String(), err
}

func TestWaitAnswerRefusedInAgentSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-agent")
	s := &answerStub{}
	_, err := runWaitAnswerCmd(t, s, "wait_1", "--seen")
	if err == nil || err.Error() != "pigtail wait answer runs from your terminal, not from an agent session" {
		t.Fatalf("err = %v", err)
	}
	if s.calls != 0 {
		t.Fatalf("the daemon was called %d time(s)", s.calls)
	}
}

func TestWaitAnswerSendsItemsAndReason(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	s := &answerStub{}
	out, err := runWaitAnswerCmd(t, s, "wait_1", "--item", "1=ok", "--item", "2=ko:button hidden, left side", "--reason", "seen on 390 px")
	if err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 || s.path != "POST /waits/wait_1/answer" || s.cross != "" {
		t.Fatalf("call = %d %q cross=%q", s.calls, s.path, s.cross)
	}
	if len(s.body.Items) != 2 || s.body.Items[0] != "1=ok" || s.body.Items[1] != "2=ko:button hidden, left side" ||
		s.body.Reason != "seen on 390 px" || s.body.Choice != "" || s.body.Text != "" || s.body.Seen {
		t.Fatalf("body = %+v", s.body)
	}
	if strings.TrimSpace(out) != "answered wait_1 → Pigtail · alpha (delivery: waiter, standby ended)" {
		t.Fatalf("out = %q", out)
	}
}

func TestWaitAnswerCrossScopeHeader(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	s := &answerStub{}
	if _, err := runWaitAnswerCmd(t, s, "wait_1", "--choice", "reco", "--cross-scope"); err != nil {
		t.Fatal(err)
	}
	if s.cross != pigtail.CrossScopeWrite || s.body.Choice != "reco" {
		t.Fatalf("cross = %q, body = %+v", s.cross, s.body)
	}
}

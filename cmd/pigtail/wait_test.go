package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail session wait --details`, the hook's dialog details, `pigtail wait
// ls` and `pigtail wait show` (sub_x000000000000026, Task 6), end to end
// against the relay.

const cliArbitrage = `{"title":"Sidebar order","options":[{"id":"A","label":"Keep","detail":"as is","reco":true},{"id":"B","label":"Drop"}],"why":"cheaper"}`
const cliReview = `{"items":[{"label":"Header aligned"},{"label":"Dark mode","detail":"check contrast"}]}`

// openWait is the open record of session's source, nil when none.
func openWait(t *testing.T, st *store.Store, session, source string) *store.WaitRecord {
	t.Helper()
	ids, err := st.OpenWaitRecordIDs(session)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		r, err := st.GetWaitRecord(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Source == source {
			return r
		}
	}
	return nil
}

func TestSessionWaitSendsDetailsFromFileAndStdin(t *testing.T) {
	_, _, st := startRelayDaemonStore(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-det"})
	path := filepath.Join(t.TempDir(), "details.json")
	if err := os.WriteFile(path, []byte(cliArbitrage), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runSessionWaitOpts(context.Background(), &out, env, []string{"arbitrage", "Sidebar"}, sessionWaitOpts{Details: path}); err != nil {
		t.Fatal(err)
	}
	r := openWait(t, st, "s-det", pigtail.WaitSourceDeclared)
	if r == nil || r.Details == nil || len(r.Details.Options) != 2 || !r.Details.Options[0].Reco {
		t.Fatalf("record = %+v", r)
	}
	if !strings.Contains(out.String(), "(wait "+r.ID+")") {
		t.Fatalf("output %q does not name the wait %s", out.String(), r.ID)
	}

	out.Reset()
	if err := runSessionWaitOpts(context.Background(), &out, env, []string{"review", "PR ready"},
		sessionWaitOpts{Details: "-", Stdin: strings.NewReader(cliReview)}); err != nil {
		t.Fatal(err)
	}
	r = openWait(t, st, "s-det", pigtail.WaitSourceDeclared)
	if r == nil || r.Type != "review" || r.Details == nil || len(r.Details.Items) != 2 {
		t.Fatalf("record from stdin = %+v", r)
	}

	// An unreadable file: an error naming it, and no call at all.
	missing := filepath.Join(t.TempDir(), "nope.json")
	env2 := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-none"})
	err := runSessionWaitOpts(context.Background(), &bytes.Buffer{}, env2, []string{"review"}, sessionWaitOpts{Details: missing})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("unreadable file: %v", err)
	}
	if _, err := st.GetPeer("s-none"); err == nil {
		t.Fatal("the daemon was called (the session got registered)")
	}
}

// Review Focus 1: the declaration reaches a daemon that does not know the
// session yet; the retry after registerSession carries the same details, and
// one wait opens.
func TestSessionWaitDetailsRetriesAfterRegister(t *testing.T) {
	_, _, st := startRelayDaemonStore(t)
	if _, err := st.GetPeer("s-new"); err == nil {
		t.Fatal("precondition: s-new must be unknown")
	}
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-new"})
	path := filepath.Join(t.TempDir(), "d.json")
	_ = os.WriteFile(path, []byte(cliArbitrage), 0o600)
	if err := runSessionWaitOpts(context.Background(), &bytes.Buffer{}, env, []string{"arbitrage"}, sessionWaitOpts{Details: path}); err != nil {
		t.Fatal(err)
	}
	ids, _ := st.OpenWaitRecordIDs("s-new")
	r := openWait(t, st, "s-new", pigtail.WaitSourceDeclared)
	if len(ids) != 1 || r == nil || r.Details == nil || r.Details.Title != "Sidebar order" || len(r.Details.Options) != 2 {
		t.Fatalf("records %v, %+v", ids, r)
	}
}

// seedWaits poses a dialog permission (hook) on s-perm and a declared
// arbitrage with details on s-arb; it returns a master client.
func seedWaits(t *testing.T) (*pigtail.Client, *store.Store) {
	t.Helper()
	c, _, st := startRelayDaemonStore(t)
	runWaitHook(strings.NewReader(`{"session_id":"s-perm","cwd":"/w","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make"}}`), &bytes.Buffer{}, noEnv)
	path := filepath.Join(t.TempDir(), "d.json")
	_ = os.WriteFile(path, []byte(cliArbitrage), 0o600)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-arb"})
	if err := runSessionWaitOpts(context.Background(), &bytes.Buffer{}, env, []string{"arbitrage", "Sidebar"}, sessionWaitOpts{Details: path}); err != nil {
		t.Fatal(err)
	}
	// Sessions registered outside a cell have no scope: the master's (D17).
	return c.WithScope(string(scope.Master())), st
}

func TestWaitLsTableAndJSON(t *testing.T) {
	c, _ := seedWaits(t)
	var out bytes.Buffer
	if err := runWaitLs(context.Background(), &out, c, false, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "ID AGE TYPE SESSION SUBJECT REASON" {
		t.Fatalf("table:\n%s", out.String())
	}
	if !strings.Contains(lines[1], "permission (dialog)") || !strings.Contains(lines[1], "Bash") || !strings.HasPrefix(lines[1], pigtail.PrefixWait) {
		t.Fatalf("dialog row (oldest first): %q", lines[1])
	}
	if !strings.Contains(lines[2], "arbitrage") || strings.Contains(lines[2], "(dialog)") || !strings.Contains(lines[2], "Sidebar") {
		t.Fatalf("declared row: %q", lines[2])
	}

	out.Reset()
	if err := runWaitLs(context.Background(), &out, c, false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	var ws []pigtail.Wait
	if err := json.Unmarshal(out.Bytes(), &ws); err != nil || len(ws) != 2 || ws[0].Source != pigtail.WaitSourceAuto {
		t.Fatalf("--json = %s (%v)", out.String(), err)
	}

	// Nothing open: an empty array in JSON.
	empty, _, _ := startRelayDaemonStore(t)
	out.Reset()
	if err := runWaitLs(context.Background(), &out, empty.WithScope(string(scope.Master())), false, true, time.Now()); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty --json = %q, %v", out.String(), err)
	}
}

func TestWaitShowRendersOptionsAndItems(t *testing.T) {
	c, st := seedWaits(t)
	arb := openWait(t, st, "s-arb", pigtail.WaitSourceDeclared)
	var out bytes.Buffer
	if err := runWaitShow(context.Background(), &out, c, arb.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{arb.ID, "arbitrage", "Sidebar order", "options (* = reco):", "* A  Keep", "B  Drop", "as is", "why: cheaper"} {
		if !strings.Contains(s, want) {
			t.Fatalf("show arbitrage lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "* B") || strings.Contains(s, "why (* = reco)") || strings.Contains(s, "answer it in the terminal") {
		t.Fatalf("show arbitrage:\n%s", s)
	}

	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-rev"})
	if err := runSessionWaitOpts(context.Background(), &bytes.Buffer{}, env, []string{"review", "UI"},
		sessionWaitOpts{Details: "-", Stdin: strings.NewReader(cliReview)}); err != nil {
		t.Fatal(err)
	}
	rev := openWait(t, st, "s-rev", pigtail.WaitSourceDeclared)
	out.Reset()
	if err := runWaitShow(context.Background(), &out, c, rev.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	s = out.String()
	for _, want := range []string{"1. Header aligned", "2. Dark mode", "check contrast"} {
		if !strings.Contains(s, want) {
			t.Fatalf("show review lacks %q:\n%s", want, s)
		}
	}

	perm := openWait(t, st, "s-perm", pigtail.WaitSourceAuto)
	out.Reset()
	if err := runWaitShow(context.Background(), &out, c, perm.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	s = out.String()
	for _, want := range []string{"permission (dialog)", "make", "/w", "answer it in the terminal"} {
		if !strings.Contains(s, want) {
			t.Fatalf("show permission lacks %q:\n%s", want, s)
		}
	}

	out.Reset()
	if err := runWaitShow(context.Background(), &out, c, perm.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	var w pigtail.Wait
	if err := json.Unmarshal(out.Bytes(), &w); err != nil || w.ID != perm.ID || w.Details == nil || w.Details.Cmd != "make" {
		t.Fatalf("show --json = %s (%v)", out.String(), err)
	}
	if err := runWaitShow(context.Background(), &bytes.Buffer{}, c, "wait_unknown", false, time.Now()); err == nil || !strings.Contains(err.Error(), "no such wait") {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestWaitHelpNamesFullScopeView(t *testing.T) {
	t.Parallel()
	long := waitCmd().Long
	for _, want := range []string{"--scope all", "PIGTAIL_SCOPE=all", "--cross-scope"} {
		if !strings.Contains(long, want) {
			t.Fatalf("pigtail wait help lacks %q:\n%s", want, long)
		}
	}
	for _, name := range []string{"ls", "show"} {
		sub, _, err := waitCmd().Find([]string{name})
		if err != nil || sub.Name() != name || sub.Annotations[scopeAccessKey] != "read" {
			t.Fatalf("wait %s: %v %+v", name, err, sub)
		}
	}
}

// `pigtail wait snooze <id> --until …` (Task 11): HH:MM (a time already past
// today means tomorrow), RFC 3339, or a duration.
func TestParseSnoozeUntil(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("T", 2*3600)
	now := time.Date(2026, 10, 20, 14, 30, 0, 0, loc)
	cases := []struct {
		in   string
		want time.Time
		bad  bool
	}{
		{"16:45", time.Date(2026, 10, 20, 16, 45, 0, 0, loc), false},
		{"9:05", time.Date(2026, 10, 21, 9, 5, 0, 0, loc), false}, // past today → tomorrow
		{"14:30", time.Date(2026, 10, 21, 14, 30, 0, 0, loc), false},
		{"2026-10-22T08:00:00Z", time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC), false},
		{"90m", now.Add(90 * time.Minute), false},
		{"2h", now.Add(2 * time.Hour), false},
		{"", time.Time{}, true},
		{"25:00", time.Time{}, true},
		{"-5m", time.Time{}, true},
		{"tomorrow", time.Time{}, true},
	}
	for _, c := range cases {
		got, err := parseSnoozeUntil(c.in, now)
		if c.bad {
			if err == nil {
				t.Errorf("%q: want an error, got %v", c.in, got)
			}
			continue
		}
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%q = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

func TestWaitSnoozeSendsUntil(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	var path, cross string
	var body pigtail.WaitSnoozeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		cross = r.Header.Get(pigtail.HeaderCrossScope)
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeData(w, http.StatusOK, pigtail.Wait{ID: "wait_1", SessionID: "s", SessionLabel: "Pigtail · alpha", State: pigtail.WaitStateOpen, SnoozedUntil: &body.Until})
	}))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	var err error
	withStubDaemon(t, &stubDaemon{srv: srv}, func() {
		cmd := waitCmd()
		cmd.SetArgs([]string{"snooze", "wait_1", "--until", "2h", "--cross-scope"})
		cmd.SetOut(&out)
		err = cmd.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "POST /waits/wait_1/snooze" || cross == "" || time.Until(body.Until) < time.Hour+50*time.Minute {
		t.Fatalf("call %q cross=%q until=%v", path, cross, body.Until)
	}
	if !strings.Contains(out.String(), "snoozed wait_1") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestWaitSnoozeRefusedFromAgentSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-agent")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	var err error
	withStubDaemon(t, &stubDaemon{srv: srv}, func() {
		cmd := waitCmd()
		cmd.SetArgs([]string{"snooze", "wait_1", "--until", "2h"})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err = cmd.Execute()
	})
	if err == nil || !strings.Contains(err.Error(), "pigtail wait snooze runs from your terminal, not from an agent session") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("the daemon was called")
	}
}

// fakeWaits serves GET /waits, GET /waits/{id} and POST /waits/{id}/answer
// from fixed values, and runs `pigtail wait <args>` against it.
func runWaitAgainst(t *testing.T, list []pigtail.Wait, one pigtail.Wait, answer pigtail.WaitAnswerResponse, args ...string) string {
	t.Helper()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/waits":
			writeData(w, http.StatusOK, list)
		case r.Method == http.MethodGet:
			writeData(w, http.StatusOK, one)
		default:
			writeData(w, http.StatusOK, answer)
		}
	}))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	var err error
	withStubDaemon(t, &stubDaemon{srv: srv}, func() {
		cmd := waitCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		err = cmd.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestWaitShowOptionsWithoutRecoHaveNoLegend(t *testing.T) {
	w := pigtail.Wait{ID: "wait_1", SessionID: "s", Type: "question", Source: pigtail.WaitSourceDeclared, State: pigtail.WaitStateOpen, OpenedAt: time.Now(),
		Details: &pigtail.WaitDetails{Options: []pigtail.WaitOption{{ID: "A", Label: "Yes"}, {ID: "B", Label: "No"}}, Why: "both fine"}}
	s := runWaitAgainst(t, nil, w, pigtail.WaitAnswerResponse{}, "show", "wait_1")
	if !strings.Contains(s, "options:\n") || strings.Contains(s, "* = reco") || !strings.Contains(s, "why: both fine") {
		t.Fatalf("show:\n%s", s)
	}
}

// D29-A, D29-C: an ended emitter is named, then where the answer went.
func TestWaitShowNamesEndedSessionAndDelivery(t *testing.T) {
	at := time.Now()
	w := pigtail.Wait{ID: "wait_1", SessionID: "s-a", SessionLabel: "Pigtail · s-a", SessionEnded: true, Type: "arbitrage", Source: pigtail.WaitSourceDeclared,
		State: pigtail.WaitStateAnswered, OpenedAt: at, ClosedAt: &at, Answer: &pigtail.WaitAnswer{Choice: "A", At: at},
		DeliveredTo: "s-b", DeliveredToLabel: "Pigtail · s-b", DeliveredAt: &at}
	s := runWaitAgainst(t, nil, w, pigtail.WaitAnswerResponse{}, "show", "wait_1")
	if !strings.Contains(s, "session: Pigtail · s-a (ended)") || !strings.Contains(s, "delivered to: Pigtail · s-b") {
		t.Fatalf("show:\n%s", s)
	}
	w.DeliveredTo, w.DeliveredToLabel, w.DeliveredAt = "", "", nil
	w.SubjectID = "sub_1"
	s = runWaitAgainst(t, nil, w, pigtail.WaitAnswerResponse{}, "show", "wait_1")
	if !strings.Contains(s, "answer kept: no live session on its subject") || strings.Contains(s, "delivered to:") {
		t.Fatalf("kept show:\n%s", s)
	}
}

func TestWaitLsMarksEndedSession(t *testing.T) {
	ws := []pigtail.Wait{
		{ID: "wait_1", SessionID: "s-a", SessionLabel: "alpha", SessionEnded: true, Type: "arbitrage", Source: pigtail.WaitSourceDeclared, State: pigtail.WaitStateOpen, OpenedAt: time.Now()},
		{ID: "wait_2", SessionID: "s-b", SessionLabel: "beta", Type: "review", Source: pigtail.WaitSourceDeclared, State: pigtail.WaitStateOpen, OpenedAt: time.Now()},
	}
	s := runWaitAgainst(t, ws, pigtail.Wait{}, pigtail.WaitAnswerResponse{}, "ls")
	if !strings.Contains(s, "alpha (ended)") || strings.Contains(s, "beta (ended)") {
		t.Fatalf("ls:\n%s", s)
	}
}

func TestWaitAnswerKeptOutput(t *testing.T) {
	w := pigtail.Wait{ID: "wait_1", SessionID: "s-a", SessionLabel: "alpha", SessionEnded: true, SubjectID: "sub_1", State: pigtail.WaitStateAnswered}
	s := runWaitAgainst(t, nil, pigtail.Wait{}, pigtail.WaitAnswerResponse{Wait: w, Delivery: pigtail.DeliveryKept}, "answer", "wait_1", "--choice", "reco")
	if want := "answered wait_1 — kept: no live session on its subject; it goes to the next session of the subject\n"; s != want {
		t.Fatalf("output = %q, want %q", s, want)
	}
	s = runWaitAgainst(t, nil, pigtail.Wait{}, pigtail.WaitAnswerResponse{Wait: w, Delivery: pigtail.DeliveryWake, MessageID: "msg_1",
		DeliveredTo: "s-b", DeliveredToLabel: "beta", StandbyEnded: true}, "answer", "wait_1", "--choice", "reco")
	if want := "answered wait_1 → beta (delivery: wake, standby ended)\n"; s != want {
		t.Fatalf("output = %q, want %q", s, want)
	}
}

func TestWaitShowJSONCarriesEnvelope(t *testing.T) {
	at := time.Now()
	w := pigtail.Wait{ID: "wait_1", SessionID: "s-a", Type: "arbitrage", Source: pigtail.WaitSourceDeclared, State: pigtail.WaitStateAnswered, OpenedAt: at,
		Details:     &pigtail.WaitDetails{Kind: "arbitrage", Schema: pigtail.WaitDetailsSchema, Options: []pigtail.WaitOption{{ID: "A", Label: "Keep", Reco: true}}},
		Answer:      &pigtail.WaitAnswer{Kind: "arbitrage", Schema: pigtail.WaitAnswerSchema, Choice: "A", At: at},
		DeliveredTo: "s-b"}
	s := runWaitAgainst(t, nil, w, pigtail.WaitAnswerResponse{}, "show", "wait_1", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatal(err)
	}
	d, _ := got["details"].(map[string]any)
	a, _ := got["answer"].(map[string]any)
	if d["kind"] != "arbitrage" || d["schema"] != float64(1) || a["kind"] != "arbitrage" || a["schema"] != float64(1) || got["delivered_to"] != "s-b" {
		t.Fatalf("--json = %s", s)
	}
}

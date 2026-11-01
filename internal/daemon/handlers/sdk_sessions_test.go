package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var sdkT0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

// fakeSDKReg is the SDK view in memory; Dial records the commands.
type fakeSDKReg struct {
	mu       sync.Mutex
	sessions map[string]sdksession.Session
	events   map[string][]string
	sent     []map[string]any
	dialErr  error
	reply    sdksession.HostReply
}

func (f *fakeSDKReg) List(subjectID string) ([]sdksession.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []sdksession.Session{}
	for _, s := range f.sessions {
		if subjectID == "" || s.SubjectID == subjectID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeSDKReg) Settle(id string) (sdksession.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return sdksession.Session{}, sdksession.ErrNotFound
	}
	return s, nil
}

func (f *fakeSDKReg) Events(id string, after int) ([]json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[id]; !ok {
		return nil, sdksession.ErrNotFound
	}
	out := []json.RawMessage{}
	for _, l := range f.events[id] {
		var h struct {
			Seq int `json:"seq"`
		}
		_ = json.Unmarshal([]byte(l), &h)
		if h.Seq > after {
			out = append(out, json.RawMessage(l))
		}
	}
	return out, nil
}

func (f *fakeSDKReg) Dial(id string, cmd any) (sdksession.HostReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialErr != nil {
		return sdksession.HostReply{}, f.dialErr
	}
	b, _ := json.Marshal(cmd)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.sent = append(f.sent, m)
	if f.reply == (sdksession.HostReply{}) {
		return sdksession.HostReply{OK: true}, nil
	}
	return f.reply, nil
}

func (f *fakeSDKReg) commands() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

// fakeGate runs fn like WithSDKReservation does once the checks passed.
type fakeGate struct {
	err  error
	subs []string
}

func (g *fakeGate) WithSDKReservation(sub pigtail.Subject, fn func(launch.Resolved) (string, error)) (string, error) {
	g.subs = append(g.subs, sub.ID)
	if g.err != nil {
		return "", g.err
	}
	return fn(launch.Resolved{Cell: claudeprofile.Cell{ID: "mp", ConfigDir: "/cell/mp"}, Mode: "default", Cwd: "/work/wt"})
}

type sdkFixture struct {
	r    *chi.Mux
	svc  *Services
	st   *store.Store
	reg  *fakeSDKReg
	gate *fakeGate

	metas   []sdksession.Meta
	begun   chan struct{}
	release chan struct{}
}

const (
	reqInput = `{"questions":[{"question":"Which fruits?","header":"Fruit","multiSelect":true,"options":[{"label":"Apple","description":"a"},{"label":"Banana","description":"b"}]}]}`
)

func newSDKFixture(t *testing.T) *sdkFixture {
	t.Helper()
	r, st, svc := scopedFixtureSvc(t)
	f := &sdkFixture{r: r, svc: svc, st: st, gate: &fakeGate{}, begun: make(chan struct{}), release: make(chan struct{})}
	f.reg = &fakeSDKReg{
		sessions: map[string]sdksession.Session{
			"sdk_p1": {ID: "sdk_p1", SubjectID: "sub_p", Cwd: "/work/p", ConfigDir: "/cell/mp", ClaudeSessionID: "csid-p", State: "waiting",
				HostPID: 4242, ClaudePIDs: []int{4243}, Turn: 0, Live: true, StartedAt: sdkT0,
				Pending: []sdksession.Request{{Rid: "r1", Kind: "question", Tool: "AskUserQuestion", Input: json.RawMessage(reqInput), OpenedAt: sdkT0.Add(5 * time.Second)}}},
			"sdk_p2": {ID: "sdk_p2", SubjectID: "sub_p", Cwd: "/work/p", ConfigDir: "/cell/mp", ClaudeSessionID: "csid-p2", State: "ended",
				ResumeCommand: "cd /work/p && CLAUDE_CONFIG_DIR=/cell/mp claude --resume csid-p2", ClaudePIDs: []int{}, Pending: []sdksession.Request{}, StartedAt: sdkT0.Add(-time.Hour)},
			"sdk_t1": {ID: "sdk_t1", SubjectID: "sub_t", Cwd: "/work/t", ConfigDir: "/cell/mt", ClaudeSessionID: "csid-t", State: "idle",
				HostPID: 5252, ClaudePIDs: []int{5253}, Turn: 1, Live: true, Pending: []sdksession.Request{}, StartedAt: sdkT0},
		},
		events: map[string][]string{
			"sdk_p1": {
				`{"seq":1,"at":"2026-10-29T10:00:01Z","kind":"init","data":{"session_id":"csid-p"}}`,
				`{"seq":2,"at":"2026-10-29T10:00:02Z","kind":"state","data":{"state":"running"}}`,
				`{"seq":3,"at":"2026-10-29T10:00:05Z","kind":"request_opened","data":{"rid":"r1","kind":"question","tool":"AskUserQuestion"}}`,
				`{"seq":4,"at":"2026-10-29T10:00:05Z","kind":"state","data":{"state":"waiting"}}`,
			},
			"sdk_p2": {
				`{"seq":1,"at":"2026-10-29T09:00:01Z","kind":"request_opened","data":{"rid":"r9","kind":"permission","tool":"Bash"}}`,
				`{"seq":2,"at":"2026-10-29T09:00:02Z","kind":"request_answered","data":{"rid":"r9","behavior":"allow"}}`,
			},
		},
	}
	svc.SDK = &SDKService{
		Registry: f.reg, Gate: f.gate, LaunchEnabled: true,
		NewID: func() string { return "sdk_new1" }, NewUUID: func() string { return "11111111-2222-4333-8444-555555555555" },
		Reserve: func(m sdksession.Meta) (func(), error) {
			f.metas = append(f.metas, m)
			return func() { close(f.begun); <-f.release }, nil
		},
	}
	return f
}

// sdkDo serves one request. cookie marks it as the web front's (the cookie
// path reads as the master and strips X-Pigtail-Session before the handler, so
// the header here is only set by the tests that prove the handler refuses it).
func sdkDo(t *testing.T, r http.Handler, sc string, cookie bool, session, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if sc != "" {
		req.Header.Set(pigtail.HeaderScope, sc)
	}
	if session != "" {
		req.Header.Set(pigtail.HeaderSession, session)
	}
	if cookie {
		req = req.WithContext(WithWeb(req.Context()))
		req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
		req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func sdkRoutes() [][2]string {
	return [][2]string{
		{"POST", "/subjects/sub_p/sdk-sessions"},
		{"GET", "/sdk-sessions"},
		{"GET", "/sdk-sessions/sdk_p1"},
		{"GET", "/sdk-sessions/sdk_p1/events"},
		{"POST", "/sdk-sessions/sdk_p1/requests/r1/answer"},
		{"POST", "/sdk-sessions/sdk_p1/messages"},
		{"POST", "/sdk-sessions/sdk_p1/end"},
	}
}

// CA1: without the option every route answers 503 SDK_OFF.
func TestSDKRoutesOffAnswer503(t *testing.T) {
	_, _, svc := scopedFixtureSvc(t)
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, detailResponder{})
	for _, rt := range sdkRoutes() {
		rec := sdkDo(t, r, "perso", true, "", rt[0], rt[1], map[string]any{"behavior": "allow", "text": "x"})
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"SDK_OFF"`) {
			t.Errorf("%s %s = %d %s, want 503 SDK_OFF", rt[0], rt[1], rec.Code, rec.Body)
		}
	}
}

// B5
func TestLaunchSDKRoute(t *testing.T) {
	f := newSDKFixture(t)
	rec := sdkDo(t, f.r, "perso", false, "", "POST", "/subjects/sub_p/sdk-sessions", nil)
	// The fake host start is still blocked: the answer came before it ended.
	select {
	case <-f.begun:
	case <-time.After(2 * time.Second):
		t.Fatal("the host start never began")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got pigtail.SDKLaunched
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID != "sdk_new1" || got.State != "starting" {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	if len(f.gate.subs) != 1 || f.gate.subs[0] != "sub_p" || len(f.metas) != 1 {
		t.Fatalf("gate=%v metas=%v", f.gate.subs, f.metas)
	}
	m := f.metas[0]
	if m.ID != "sdk_new1" || m.SubjectID != "sub_p" || m.ConfigDir != "/cell/mp" || m.Cwd != "/work/wt" || m.Mode != "default" ||
		m.Prompt != usersettings.Defaults().LaunchPrompt("sub_p", "perso subject") || m.SessionUUID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("meta = %+v", m)
	}
	close(f.release)
}

func TestLaunchSDKRouteRefusals(t *testing.T) {
	f := newSDKFixture(t)
	f.gate.err = &launch.Refusal{Code: pigtail.ErrSessionLive, Status: http.StatusConflict, Msg: "live", Details: map[string]any{"subject_id": "sub_p", "sdk_session_id": "sdk_p1"}}
	rec := sdkDo(t, f.r, "perso", false, "", "POST", "/subjects/sub_p/sdk-sessions", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "SESSION_LIVE") {
		t.Fatalf("SDK live: %d %s", rec.Code, rec.Body)
	}
	f.gate.err = &launch.Refusal{Code: pigtail.ErrSessionLive, Status: http.StatusConflict, Msg: "live", Details: map[string]any{"workspace_id": "WS"}}
	rec = sdkDo(t, f.r, "perso", false, "", "POST", "/subjects/sub_p/sdk-sessions", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("TUI live: %d %s", rec.Code, rec.Body)
	}
	if len(f.metas) != 0 {
		t.Fatal("a refused launch reserved a session")
	}
	// A launch with the option read-only: SDK_LAUNCH_OFF, the gate is not asked.
	f.gate.err, f.svc.SDK.LaunchEnabled = nil, false
	n := len(f.gate.subs)
	rec = sdkDo(t, f.r, "perso", false, "", "POST", "/subjects/sub_p/sdk-sessions", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SDK_LAUNCH_OFF") || len(f.gate.subs) != n {
		t.Fatalf("launch off: %d %s", rec.Code, rec.Body)
	}
}

// CA14: with the registry wired but without -sdk-launch nothing is launched
// (the reservation is never asked, no session directory appears) and the reads answer.
func TestSDKLaunchOffByDefault(t *testing.T) {
	f := newSDKFixture(t)
	f.svc.SDK.LaunchEnabled = false
	reserved := 0
	f.svc.SDK.Reserve = func(sdksession.Meta) (func(), error) { reserved++; return func() {}, nil }
	rec := sdkDo(t, f.r, "perso", false, "", "POST", "/subjects/sub_p/sdk-sessions", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SDK_LAUNCH_OFF") {
		t.Fatalf("launch = %d %s", rec.Code, rec.Body)
	}
	if reserved != 0 || len(f.gate.subs) != 0 {
		t.Fatalf("reserved %d, gate asked %v", reserved, f.gate.subs)
	}
	rec = sdkDo(t, f.r, "perso", false, "", "GET", "/sdk-sessions", nil)
	var l pigtail.SDKSessionList
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &l) != nil || l.LaunchEnabled || len(l.Sessions) == 0 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
}

// The off answer carries the reason pigtaild could not wire the option.
func TestSDKOffCarriesItsReason(t *testing.T) {
	_, _, svc := scopedFixtureSvc(t)
	svc.SDKOffReason = "claude not found on PATH"
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, detailResponder{})
	rec := sdkDo(t, r, "perso", true, "", "GET", "/sdk-sessions", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "claude not found on PATH") {
		t.Fatalf("off = %d %s", rec.Code, rec.Body)
	}
}

// CA9: an out-of-scope caller gets the unknown-id body, everywhere.
func TestSDKRoutesOutOfScope(t *testing.T) {
	f := newSDKFixture(t)
	unknown := sdkDo(t, f.r, "gx", false, "", "GET", "/sdk-sessions/sdk_nope", nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", unknown.Code)
	}
	for _, rt := range [][2]string{{"GET", "/sdk-sessions/sdk_p1"}, {"GET", "/sdk-sessions/sdk_p1/events"}} {
		rec := sdkDo(t, f.r, "gx", false, "", rt[0], rt[1], nil)
		if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
			t.Errorf("%s %s = %d %q, want the unknown-id body %q", rt[0], rt[1], rec.Code, rec.Body, unknown.Body)
		}
	}
	rec := sdkDo(t, f.r, "gx", false, "", "POST", "/subjects/sub_p/sdk-sessions", nil)
	unk := sdkDo(t, f.r, "gx", false, "", "POST", "/subjects/sub_nope/sdk-sessions", nil)
	if rec.Code != http.StatusNotFound || rec.Body.String() != unk.Body.String() {
		t.Errorf("launch on another scope = %d %q", rec.Code, rec.Body)
	}
	if len(f.gate.subs) != 0 {
		t.Fatal("the gate was reached by an out-of-scope caller")
	}
}

// B9
func TestListSDKSessionsFiltersByScope(t *testing.T) {
	f := newSDKFixture(t)
	ids := func(rec *httptest.ResponseRecorder) []string {
		t.Helper()
		var l pigtail.SDKSessionList
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil {
			t.Fatalf("list = %d %s", rec.Code, rec.Body)
		}
		var out []string
		for _, s := range l.Sessions {
			out = append(out, s.ID)
		}
		if !l.LaunchEnabled {
			t.Error("launch_enabled should be true")
		}
		return out
	}
	if got := strings.Join(ids(sdkDo(t, f.r, "gx", false, "", "GET", "/sdk-sessions", nil)), ","); got != "sdk_t1" {
		t.Errorf("work sees %q", got)
	}
	if got := strings.Join(ids(sdkDo(t, f.r, "perso", false, "", "GET", "/sdk-sessions", nil)), ","); got != "sdk_p1,sdk_p2,sdk_t1" { // perso is the master scope
		t.Errorf("perso sees %q", got)
	}
	if got := strings.Join(ids(sdkDo(t, f.r, string(scope.Master()), false, "", "GET", "/sdk-sessions", nil)), ","); got != "sdk_p1,sdk_p2,sdk_t1" {
		t.Errorf("master sees %q", got)
	}
	if got := strings.Join(ids(sdkDo(t, f.r, "", true, "", "GET", "/sdk-sessions", nil)), ","); got != "sdk_p1,sdk_p2,sdk_t1" {
		t.Errorf("cookie sees %q", got)
	}
	if got := strings.Join(ids(sdkDo(t, f.r, "perso", false, "", "GET", "/sdk-sessions?subject=sub_p", nil)), ","); got != "sdk_p1,sdk_p2" {
		t.Errorf("subject filter: %q", got)
	}
	// A subject of another scope reads as unknown, not as an empty list.
	if rec := sdkDo(t, f.r, "gx", false, "", "GET", "/sdk-sessions?subject=sub_p", nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign subject filter = %d", rec.Code)
	}
}

func TestGetSDKSessionAndEvents(t *testing.T) {
	f := newSDKFixture(t)
	rec := sdkDo(t, f.r, "perso", false, "", "GET", "/sdk-sessions/sdk_p1", nil)
	var s pigtail.SDKSession
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.ClaudeSessionID != "csid-p" || s.State != "waiting" || s.SubjectID != "sub_p" {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	rec = sdkDo(t, f.r, "perso", false, "", "GET", "/sdk-sessions/sdk_p1/events?after=2", nil)
	var ev pigtail.SDKEvents
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &ev) != nil || len(ev.Events) != 2 || ev.LastSeq != 4 {
		t.Fatalf("events = %d %s", rec.Code, rec.Body)
	}
	if rec := sdkDo(t, f.r, "perso", false, "", "GET", "/sdk-sessions/sdk_p1/events?after=x", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad after = %d", rec.Code)
	}
	// Nothing new: last_seq is the caller's own cursor.
	rec = sdkDo(t, f.r, "perso", false, "", "GET", "/sdk-sessions/sdk_p1/events?after=9", nil)
	if json.Unmarshal(rec.Body.Bytes(), &ev) != nil || len(ev.Events) != 0 || ev.LastSeq != 9 {
		t.Errorf("empty page = %s", rec.Body)
	}
}

// The three writes take the cookie alone.
func TestSDKWritesRefuseSessionHeaderAndBearer(t *testing.T) {
	f := newSDKFixture(t)
	for _, rt := range sdkRoutes()[4:] {
		body := map[string]any{"behavior": "allow", "text": "hi"}
		// The cookie context and the agent's header together.
		rec := sdkDo(t, f.r, "", true, "s-agent", rt[0], rt[1], body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with X-Pigtail-Session = %d, want 403", rt[0], rt[1], rec.Code)
		}
		// A bearer caller (no cookie).
		for _, sc := range []string{"perso", string(scope.Master())} {
			rec = sdkDo(t, f.r, sc, false, "", rt[0], rt[1], body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s without the cookie (%s) = %d, want 403", rt[0], rt[1], sc, rec.Code)
			}
		}
	}
	if n := len(f.reg.commands()); n != 0 {
		t.Fatalf("%d commands reached the host", n)
	}
}

func TestAnswerWritesToHostAndClearsAutoWaits(t *testing.T) {
	f := newSDKFixture(t)
	if err := f.st.SetSessionWait("csid-p", pigtail.SessionWait{Type: "question", Source: pigtail.WaitSourceAuto, Since: sdkT0}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetSessionWait("csid-p", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: sdkT0}); err != nil {
		t.Fatal(err)
	}
	body := `{"behavior":"allow","answers":{"Which fruits?":["Apple","Banana"]}}`
	rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p1/requests/r1/answer", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body)
	}
	cmds := f.reg.commands()
	if len(cmds) != 1 || cmds[0]["op"] != "answer" || cmds[0]["rid"] != "r1" || cmds[0]["behavior"] != "allow" {
		t.Fatalf("commands = %v", cmds)
	}
	ans, _ := cmds[0]["answers"].(map[string]any)
	if list, _ := ans["Which fruits?"].([]any); len(list) != 2 || list[0] != "Apple" || list[1] != "Banana" {
		t.Fatalf("answers = %v", cmds[0]["answers"])
	}
	waits, err := f.st.SessionWaits("csid-p")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range waits {
		if w.Source == pigtail.WaitSourceAuto {
			t.Fatalf("the auto wait survived the answer: %+v", waits)
		}
	}
	if len(waits) != 1 {
		t.Fatalf("the declared wait must stay: %+v", waits)
	}
}

func TestAnswerDeny(t *testing.T) {
	f := newSDKFixture(t)
	rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p1/requests/r1/answer", `{"behavior":"deny","message":"pas maintenant"}`)
	cmds := f.reg.commands()
	if rec.Code != 200 || len(cmds) != 1 || cmds[0]["behavior"] != "deny" || cmds[0]["message"] != "pas maintenant" {
		t.Fatalf("deny = %d %v", rec.Code, cmds)
	}
}

func TestAnswerRefusals(t *testing.T) {
	f := newSDKFixture(t)
	for name, tc := range map[string]struct {
		path, body string
		want       int
	}{
		"unknown rid":      {"/sdk-sessions/sdk_p1/requests/r404/answer", `{"behavior":"allow"}`, 404},
		"already answered": {"/sdk-sessions/sdk_p2/requests/r9/answer", `{"behavior":"allow"}`, 409},
		"bad behavior":     {"/sdk-sessions/sdk_p1/requests/r1/answer", `{"behavior":"maybe"}`, 400},
		"bad json":         {"/sdk-sessions/sdk_p1/requests/r1/answer", `{`, 400},
		"bad answers":      {"/sdk-sessions/sdk_p1/requests/r1/answer", `{"behavior":"allow","answers":{"q":3}}`, 400},
		"unknown session":  {"/sdk-sessions/sdk_nope/requests/r1/answer", `{"behavior":"allow"}`, 404},
	} {
		if rec := sdkDo(t, f.r, "", true, "", "POST", tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
	if n := len(f.reg.commands()); n != 0 {
		t.Fatalf("%d commands reached the host", n)
	}
	// The host says no after the registry said pending: a lost race, 409.
	f.reg.reply = sdksession.HostReply{OK: false, Error: "unknown or already answered request"}
	if rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p1/requests/r1/answer", `{"behavior":"allow"}`); rec.Code != http.StatusConflict {
		t.Errorf("lost race = %d", rec.Code)
	}
	// An unreachable host.
	f.reg.reply, f.reg.dialErr = sdksession.HostReply{}, errors.New("dial unix: no such file")
	if rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p1/requests/r1/answer", `{"behavior":"allow"}`); rec.Code != http.StatusConflict {
		t.Errorf("unreachable host = %d", rec.Code)
	}
}

func TestMessagesOnlyWhenIdle(t *testing.T) {
	f := newSDKFixture(t)
	rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_t1/messages", map[string]string{"text": "continue"})
	cmds := f.reg.commands()
	if rec.Code != 200 || len(cmds) != 1 || cmds[0]["op"] != "say" || cmds[0]["text"] != "continue" {
		t.Fatalf("say = %d %s %v", rec.Code, rec.Body, cmds)
	}
	rec = sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p1/messages", map[string]string{"text": "continue"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "NOT_IDLE") || len(f.reg.commands()) != 1 {
		t.Fatalf("not idle = %d %s", rec.Code, rec.Body)
	}
	if rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_t1/messages", map[string]string{"text": "  "}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty text = %d", rec.Code)
	}
	if len(f.reg.commands()) != 1 {
		t.Fatal("a refused message reached the host")
	}
}

func TestEndWritesEnd(t *testing.T) {
	f := newSDKFixture(t)
	rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p1/end", nil)
	cmds := f.reg.commands()
	if rec.Code != 200 || len(cmds) != 1 || cmds[0]["op"] != "end" {
		t.Fatalf("end = %d %s %v", rec.Code, rec.Body, cmds)
	}
	if rec := sdkDo(t, f.r, "", true, "", "POST", "/sdk-sessions/sdk_p2/end", nil); rec.Code != http.StatusConflict {
		t.Errorf("end of an ended session = %d", rec.Code)
	}
}

// Binding invariant: no route changes or relinks a session's subject.
func TestSDKRoutesNeverRebind(t *testing.T) {
	f := newSDKFixture(t)
	allowed := map[string]bool{
		"POST /subjects/{id}/sdk-sessions": true, "GET /sdk-sessions": true, "GET /sdk-sessions/{id}": true,
		"GET /sdk-sessions/{id}/events": true, "POST /sdk-sessions/{id}/requests/{rid}/answer": true,
		"POST /sdk-sessions/{id}/messages": true, "POST /sdk-sessions/{id}/end": true,
	}
	seen := 0
	err := chi.Walk(f.r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(route, "sdk-sessions") {
			return nil
		}
		seen++
		if !allowed[method+" "+route] {
			t.Errorf("unexpected sdk route %s %s", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != len(allowed) {
		t.Fatalf("saw %d sdk routes, want %d", seen, len(allowed))
	}
}

// The wire types of pkg/pigtail decode the daemon's session exactly.
func TestSDKWireTypesMatch(t *testing.T) {
	f := newSDKFixture(t)
	for _, s := range f.reg.sessions {
		b, _ := json.Marshal(s)
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var w pigtail.SDKSession
		if err := dec.Decode(&w); err != nil {
			t.Fatalf("pkg/pigtail.SDKSession drifted from sdksession.Session: %v", err)
		}
	}
}

// fixtureResponder is the daemon's envelope with a fixed request id, for the
// web fixtures.
type fixtureResponder struct{}

func (fixtureResponder) JSON(w http.ResponseWriter, _ *http.Request, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": map[string]any{"request_id": "req_fixture"}})
}

func (fixtureResponder) Error(w http.ResponseWriter, _ *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": pigtail.APIError{Code: code, Message: msg, Details: details}, "meta": map[string]any{"request_id": "req_fixture"}})
}

// B8: the web client's fixtures are the daemon's real answers. With
// UPDATE_FIXTURES=1 they are rewritten; otherwise a difference fails.
func TestWriteWebFixtures(t *testing.T) {
	f := newSDKFixture(t)
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, f.svc, fixtureResponder{})
	cases := []struct {
		name string
		r    http.Handler
		path string
	}{
		{"list.json", r, "/sdk-sessions"},
		{"session-waiting.json", r, "/sdk-sessions/sdk_p1"},
		{"session-ended.json", r, "/sdk-sessions/sdk_p2"},
		{"events.json", r, "/sdk-sessions/sdk_p1/events?after=0"},
		{"off.json", nil, "/sdk-sessions"},
	}
	dir := filepath.Join("..", "..", "..", "web", "src", "sdk", "fixtures")
	for _, c := range cases {
		sdk := f.svc.SDK
		h := c.r
		if h == nil { // the option off: the same routes with no SDK service
			h, f.svc.SDK = r, nil
		}
		rec := sdkDo(t, h, string(scope.Master()), false, "", "GET", c.path, nil)
		f.svc.SDK = sdk
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, rec.Body.Bytes(), "", "  "); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		pretty.WriteByte('\n')
		path := filepath.Join(dir, c.name)
		if os.Getenv("UPDATE_FIXTURES") == "1" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with UPDATE_FIXTURES=1)", c.name, err)
		}
		if !bytes.Equal(have, pretty.Bytes()) {
			t.Errorf("%s differs from the daemon's answer (run with UPDATE_FIXTURES=1):\n%s", c.name, pretty.String())
		}
	}
}

func TestSDKServicePromptTemplate(t *testing.T) {
	s := &SDKService{}
	if got := s.prompt("sub_1", "T"); !strings.HasPrefix(got, "Work on pigtail subject sub_1: T.") {
		t.Fatalf("default = %q", got)
	}
	s.Prompt = func(id, _ string) string { return "/x " + id }
	if got := s.prompt("sub_1", "T"); got != "/x sub_1" {
		t.Fatalf("custom = %q", got)
	}
}

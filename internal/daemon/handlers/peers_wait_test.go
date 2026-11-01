package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func (f *relayFixture) waitMarker(session string) bool {
	_, err := os.Stat(filepath.Join(f.waitMk, session))
	return err == nil
}

func decodePeer(t *testing.T, body []byte) pigtail.PeerSession {
	t.Helper()
	var p pigtail.PeerSession
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return p
}

// The declarative path end to end on the API: record, read back on GET
// /peers, marker posed; the next declaration replaces it; clear removes both.
func TestSessionWaitSetReadClear(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionWaitChanged))
	defer sub.Close()
	// A globex session, like the headerless caller: an unscoped one would
	// be the master's only (D17), out of this caller's GET /peers.
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "alpha", CmuxWorkspaceID: "W1", Scope: "globex"})

	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "arbitrage", Reason: "  Sidebar\n pigtail "})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST wait = %d %s", rec.Code, rec.Body.String())
	}
	p := decodePeer(t, rec.Body.Bytes())
	if len(p.Waits) != 1 || p.Waits[0].Type != "arbitrage" || p.Waits[0].Source != pigtail.WaitSourceDeclared ||
		p.Waits[0].Reason != "Sidebar pigtail" || !p.Waits[0].Since.Equal(f.clock()) {
		t.Fatalf("waits = %+v", p.Waits)
	}
	if !f.waitMarker("s1") {
		t.Fatal("no wait marker")
	}
	select {
	case e := <-sub.C:
		if e.Data["session_id"] != "s1" || e.Data["type"] != "arbitrage" || e.Data["source"] != "declared" ||
			e.Data["cmux_workspace_id"] != "W1" || e.Scope != "globex" {
			t.Fatalf("event %+v", e)
		}
		if strings.Contains(strings.ToLower(jsonString(e.Data)), "sidebar") {
			t.Fatalf("event carries the reason: %+v", e.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("no session.wait_changed")
	}

	// "decision" is the label's word; the type stays arbitrage. An automatic
	// wait sits next to the declared one.
	doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "permission", Reason: "Bash", Source: "auto"})
	rec = doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "decision"})
	p = decodePeer(t, rec.Body.Bytes())
	if len(p.Waits) != 2 || p.Waits[0].Source != "auto" || p.Waits[1].Type != "arbitrage" || p.Waits[1].Reason != "" {
		t.Fatalf("waits = %+v", p.Waits)
	}
	got := getPeers(t, f, "")
	if len(got) != 1 || len(got[0].Waits) != 2 {
		t.Fatalf("GET /peers waits = %+v", got)
	}

	rec = doJSON(t, f.r, "DELETE", "/peers/s1/wait?source=auto", nil)
	if p := decodePeer(t, rec.Body.Bytes()); rec.Code != http.StatusOK || len(p.Waits) != 1 || p.Waits[0].Source != "declared" {
		t.Fatalf("DELETE auto = %d %s", rec.Code, rec.Body.String())
	}
	if !f.waitMarker("s1") {
		t.Fatal("marker removed while a wait remains")
	}
	rec = doJSON(t, f.r, "DELETE", "/peers/s1/wait", nil)
	if p := decodePeer(t, rec.Body.Bytes()); rec.Code != http.StatusOK || len(p.Waits) != 0 {
		t.Fatalf("DELETE all = %d %s", rec.Code, rec.Body.String())
	}
	if f.waitMarker("s1") {
		t.Fatal("marker left after the last wait")
	}
	// Clearing nothing is fine (the prompt hook clears blindly).
	if rec := doJSON(t, f.r, "DELETE", "/peers/s1/wait", nil); rec.Code != http.StatusOK {
		t.Fatalf("idempotent DELETE = %d", rec.Code)
	}
}

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }

func getPeers(t *testing.T, f *relayFixture, query string) []pigtail.PeerSession {
	t.Helper()
	rec := doJSON(t, f.r, "GET", "/peers"+query, nil)
	var out []pigtail.PeerSession
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET /peers: %s", rec.Body.String())
	}
	return out
}

func TestSessionWaitRefusals(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1"})
	cases := []struct {
		name string
		path string
		req  pigtail.SessionWaitRequest
		code int
	}{
		{"type off the list", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "fini"}, http.StatusBadRequest},
		{"no type", "/peers/s1/wait", pigtail.SessionWaitRequest{}, http.StatusBadRequest},
		{"unknown source", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "review", Source: "guess"}, http.StatusBadRequest},
		{"reason too long", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "review", Reason: strings.Repeat("x", 201)}, http.StatusUnprocessableEntity},
		{"reason with a secret", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "blocked", Reason: "use ghp_abcdefghijklmnop"}, http.StatusUnprocessableEntity},
		{"unknown session", "/peers/nope/wait", pigtail.SessionWaitRequest{Type: "review"}, http.StatusNotFound},
	}
	for _, c := range cases {
		if rec := doJSON(t, f.r, "POST", c.path, c.req); rec.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := doJSON(t, f.r, "DELETE", "/peers/s1/wait?source=guess", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("DELETE unknown source = %d", rec.Code)
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("a refused wait was stored: %+v", ws)
	}
}

// A23: the API takes done and wrapped like the other types, and refuses the
// display-only states (your_turn, dead) as unknown types.
func TestSessionWaitDoneWrappedAndDisplayStates(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1"})
	for _, ty := range []string{"done", "wrapped"} {
		if rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: ty}); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s, want 200", ty, rec.Code, rec.Body.String())
		}
	}
	for _, ty := range []string{"your_turn", "dead"} {
		rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: ty})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), string(pigtail.ErrInvalidRequest)) {
			t.Errorf("%s: %d %s, want 400 %s", ty, rec.Code, rec.Body.String(), pigtail.ErrInvalidRequest)
		}
	}
}

// SessionEnd drops the waits and their marker.
func TestSessionEndClearsWait(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1"})
	doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "blocked", Reason: "/login"})
	rec := doJSON(t, f.r, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s1"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("end = %d", rec.Code)
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 || f.waitMarker("s1") {
		t.Fatalf("ended session still waits: %+v marker=%v", ws, f.waitMarker("s1"))
	}
}

// The markers are rebuilt from the store at start: a wiped directory must
// not stop the next prompt from clearing a wait.
func TestWaitMarkersRebuiltAtStart(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1"})
	if err := f.st.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Source: "declared", Since: f.clock()}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.waitMk, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(f.waitMk, "stale"), nil, 0o600)
	f.rl.syncMarkers()
	if !f.waitMarker("s1") || f.waitMarker("stale") {
		t.Fatalf("markers: s1=%v stale=%v", f.waitMarker("s1"), f.waitMarker("stale"))
	}
}

// D3: the bare permission_prompt that follows an AskUserQuestion does not
// turn the question into a permission; a permission naming a tool does.
// Red on 9b4158c: the last told always won.
func TestAutoWaitKeepsTheDialogType(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "alpha", CmuxWorkspaceID: "W1"})
	post := func(typ, reason string) pigtail.PeerSession {
		t.Helper()
		rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: typ, Reason: reason, Source: pigtail.WaitSourceAuto})
		if rec.Code != http.StatusOK {
			t.Fatalf("POST wait %s = %d %s", typ, rec.Code, rec.Body.String())
		}
		return decodePeer(t, rec.Body.Bytes())
	}
	post("question", "D1 Gel")
	if p := post("permission", ""); len(p.Waits) != 1 || p.Waits[0].Type != "question" || p.Waits[0].Reason != "D1 Gel" {
		t.Fatalf("bare permission_prompt replaced the question: %+v", p.Waits)
	}
	if p := post("permission", "Bash"); len(p.Waits) != 1 || p.Waits[0].Type != "permission" || p.Waits[0].Reason != "Bash" {
		t.Fatalf("a named permission must replace the question: %+v", p.Waits)
	}
}

// The failure states pass with source auto only; until is kept for Limit only.
func TestSetSessionWaitFailureStatesAutoOnly(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "alpha"})
	until := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Millisecond)
	if rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "limit"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("declared limit = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "limit", Source: "auto", Until: &until}); rec.Code != http.StatusOK {
		t.Fatalf("auto limit = %d %s", rec.Code, rec.Body.String())
	}
	if w, _ := f.rl.svc.Store.SessionWaits("s1"); len(w) != 1 || w[0].Type != "limit" || w[0].Until == nil || !w[0].Until.Equal(until) {
		t.Fatalf("waits = %+v", w)
	}
	// Only the limit has a reset: an until on login is ignored.
	if rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "login", Source: "auto", Until: &until}); rec.Code != http.StatusOK {
		t.Fatalf("auto login = %d %s", rec.Code, rec.Body.String())
	}
	if w, _ := f.rl.svc.Store.SessionWaits("s1"); len(w) != 1 || w[0].Type != "login" || w[0].Until != nil {
		t.Fatalf("waits = %+v", w)
	}
}

// Held waits (the Mac-hold helper, D1): posted with a holder pid, spared by a
// bare clear, cleared by source=held, conditionally by holder_pid.
func TestHeldWaitAPI(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionWaitChanged))
	defer sub.Close()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "alpha", CmuxWorkspaceID: "W1", Scope: "globex"})

	until := f.clock().Add(time.Hour)
	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "mac", Source: "held", Until: &until, HolderPID: 4242})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST held = %d %s", rec.Code, rec.Body.String())
	}
	p := decodePeer(t, rec.Body.Bytes())
	if len(p.Waits) != 1 || p.Waits[0].Type != "mac" || p.Waits[0].Source != "held" ||
		p.Waits[0].HolderPID != 4242 || p.Waits[0].Until == nil || !p.Waits[0].Until.Equal(until) {
		t.Fatalf("waits = %+v", p.Waits)
	}
	if !f.waitMarker("s1") {
		t.Fatal("no marker for a held wait")
	}
	wantEvent := func(src string) {
		t.Helper()
		select {
		case e := <-sub.C:
			if e.Data["source"] != src {
				t.Fatalf("event source = %v, want %q", e.Data["source"], src)
			}
		case <-time.After(time.Second):
			t.Fatalf("no event (want source %q)", src)
		}
	}
	wantEvent("held")

	noEvent := func() {
		t.Helper()
		select {
		case e := <-sub.C:
			t.Fatalf("unexpected event %+v", e)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Refusals.
	for name, req := range map[string]pigtail.SessionWaitRequest{
		"held without holder_pid": {Type: "mac", Source: "held"},
		"mac declared":            {Type: "mac"},
		"mac auto":                {Type: "mac", Source: "auto"},
		"working held":            {Type: "working", Source: "held", HolderPID: 1},
	} {
		if rec := doJSON(t, f.r, "POST", "/peers/s1/wait", req); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}

	// A bare clear (and auto, declared) spares the held wait.
	for _, q := range []string{"", "?source=auto", "?source=declared"} {
		rec = doJSON(t, f.r, "DELETE", "/peers/s1/wait"+q, nil)
		if p := decodePeer(t, rec.Body.Bytes()); rec.Code != http.StatusOK || len(p.Waits) != 1 || p.Waits[0].Source != "held" {
			t.Fatalf("DELETE %q = %d %s", q, rec.Code, rec.Body.String())
		}
	}
	noEvent()
	if !f.waitMarker("s1") {
		t.Fatal("marker removed while a held wait remains")
	}

	// holder_pid: only with source=held, a positive integer.
	for _, q := range []string{"?holder_pid=4242", "?source=all&holder_pid=4242", "?source=held&holder_pid=abc", "?source=held&holder_pid=0", "?source=held&holder_pid=-3"} {
		if rec := doJSON(t, f.r, "DELETE", "/peers/s1/wait"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %q = %d, want 400", q, rec.Code)
		}
	}

	// Conditional clear: another pid leaves it, no event.
	rec = doJSON(t, f.r, "DELETE", "/peers/s1/wait?source=held&holder_pid=5151", nil)
	if p := decodePeer(t, rec.Body.Bytes()); rec.Code != http.StatusOK || len(p.Waits) != 1 {
		t.Fatalf("DELETE other pid = %d %s", rec.Code, rec.Body.String())
	}
	noEvent()
	rec = doJSON(t, f.r, "DELETE", "/peers/s1/wait?source=held&holder_pid=4242", nil)
	if p := decodePeer(t, rec.Body.Bytes()); rec.Code != http.StatusOK || len(p.Waits) != 0 {
		t.Fatalf("DELETE own pid = %d %s", rec.Code, rec.Body.String())
	}
	wantEvent("held")
	if f.waitMarker("s1") {
		t.Fatal("marker left after the held wait went")
	}

	// source=held clears it whatever the pid; the event carries the real list.
	doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "mac", Source: "held", HolderPID: 7})
	wantEvent("held")
	rec = doJSON(t, f.r, "DELETE", "/peers/s1/wait?source=held", nil)
	if p := decodePeer(t, rec.Body.Bytes()); rec.Code != http.StatusOK || len(p.Waits) != 0 {
		t.Fatalf("DELETE held = %d %s", rec.Code, rec.Body.String())
	}
	wantEvent("held")

	// A bare clear reports auto,declared; all reports the three.
	doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "review"})
	wantEvent("declared")
	doJSON(t, f.r, "DELETE", "/peers/s1/wait", nil)
	wantEvent("auto,declared")
	doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "mac", Source: "held", HolderPID: 8})
	wantEvent("held")
	rec = doJSON(t, f.r, "DELETE", "/peers/s1/wait?source=all", nil)
	if p := decodePeer(t, rec.Body.Bytes()); rec.Code != http.StatusOK || len(p.Waits) != 0 {
		t.Fatalf("DELETE all = %d %s", rec.Code, rec.Body.String())
	}
	wantEvent("auto,declared,held")
}

// The sidebar loop ends a held wait through Relay.ClearHeldWait: the same
// effects as DELETE ?source=held&holder_pid=N (row, marker, event "held"),
// and nothing at all for another holder.
func TestRelayClearHeldWait(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionWaitChanged))
	defer sub.Close()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "alpha", CmuxWorkspaceID: "W1", Scope: "globex"})
	until := f.clock().Add(time.Hour)
	doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "mac", Source: "held", Until: &until, HolderPID: 4242})
	<-sub.C // the event of the set

	if gone, err := f.rl.ClearHeldWait("s1", 5151); err != nil || gone {
		t.Fatalf("other pid = %v %v, want false", gone, err)
	}
	select {
	case e := <-sub.C:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	if !f.waitMarker("s1") {
		t.Fatal("marker removed by another holder's clear")
	}

	gone, err := f.rl.ClearHeldWait("s1", 4242)
	if err != nil || !gone {
		t.Fatalf("own pid = %v %v, want true", gone, err)
	}
	if f.waitMarker("s1") {
		t.Fatal("marker left after the held wait went")
	}
	select {
	case e := <-sub.C:
		if e.Data["session_id"] != "s1" || e.Data["source"] != "held" || e.Scope != "globex" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no session.wait_changed")
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("waits = %+v", ws)
	}
}

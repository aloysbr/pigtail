package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Declaring a wait with typed details (sub_x000000000000026, Task 4): the
// details are validated, the record opens beside its session_waits row, and
// every transition of a record is a wait.* event — never its reason nor its
// content.

const arbitrageDetails = `{"options":[{"id":"A","label":"Keep","reco":true},{"id":"B","label":"Drop"}],"why":"cheaper"}`

// newWaitFixture is the relay fixture with a bus and one work session s1.
func newWaitFixture(t *testing.T) *relayFixture {
	t.Helper()
	f := newRelayFixture(t)
	f.rl.svc.Bus = events.New()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "alpha", CmuxWorkspaceID: "W1", Scope: "gx"})
	return f
}

// waitEvents is every wait.* event published so far, oldest first.
func waitEvents(f *relayFixture) []pigtail.Event {
	return f.rl.svc.Bus.Replay("", []string{"wait.*"})
}

func postWait(t *testing.T, f *relayFixture, session string, req pigtail.SessionWaitRequest) {
	t.Helper()
	if rec := doJSON(t, f.r, "POST", "/peers/"+session+"/wait", req); rec.Code != http.StatusOK {
		t.Fatalf("POST wait = %d %s", rec.Code, rec.Body.String())
	}
}

func openRecord(t *testing.T, f *relayFixture, session, source string) *store.WaitRecord {
	t.Helper()
	all, err := f.st.ListOpenWaitRecords(store.WaitRecordFilter{Scope: scope.Any, IncludeSnoozed: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range all {
		if all[i].SessionID == session && all[i].Source == source {
			return &all[i]
		}
	}
	return nil
}

func TestDeclareWaitWithDetails(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "arbitrage", Reason: "Sidebar order",
		Details: json.RawMessage(arbitrageDetails)})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	p := decodePeer(t, rec.Body.Bytes())
	if len(p.Waits) != 1 || !strings.HasPrefix(p.Waits[0].ID, pigtail.PrefixWait) {
		t.Fatalf("waits = %+v", p.Waits)
	}
	r, err := f.st.GetWaitRecord(p.Waits[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Details == nil || len(r.Details.Options) != 2 || !r.Details.Options[0].Reco || r.Details.Why != "cheaper" || r.Scope != "globex" {
		t.Fatalf("record = %+v details %+v", r, r.Details)
	}
	evs := waitEvents(f)
	if len(evs) != 1 || evs[0].Type != pigtail.EventWaitOpened || evs[0].Data["wait_id"] != p.Waits[0].ID ||
		evs[0].Data["session_id"] != "s1" || evs[0].Data["state"] != pigtail.WaitStateOpen || evs[0].Scope != "globex" {
		t.Fatalf("events = %+v", evs)
	}
	if s := strings.ToLower(jsonString(evs[0].Data)); strings.Contains(s, "sidebar") || strings.Contains(s, "keep") {
		t.Fatalf("event carries the reason or the content: %s", s)
	}
}

func TestDeclareWaitInvalidDetailsWritesNothing(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	bad := `{"options":[{"id":"A","label":"a","reco":true},{"id":"B","label":"b","reco":true}],"why":"w"}`
	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "arbitrage", Details: json.RawMessage(bad)})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "details.options[1].reco") {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("waits = %+v", ws)
	}
	if evs := f.rl.svc.Bus.Replay("", []string{"wait.*", string(pigtail.EventSessionWaitChanged)}); len(evs) != 0 {
		t.Fatalf("events = %+v", evs)
	}
	if f.waitMarker("s1") {
		t.Fatal("marker posed for a refused wait")
	}
}

// The old form, without --details, answers as before but for the id: same
// type, reason, source; session.wait_changed still goes; the marker is the
// same bytes; GET /peers shows the same wait once its id is blanked. The
// reference is what base 5a6c2f7 answered for this request.
func TestDeclareWaitWithoutDetailsUnchanged(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	p := decodePeer(t, rec.Body.Bytes())
	if len(p.Waits) != 1 || p.Waits[0].Type != "review" || p.Waits[0].Reason != "x" || p.Waits[0].Source != pigtail.WaitSourceDeclared ||
		!strings.HasPrefix(p.Waits[0].ID, pigtail.PrefixWait) {
		t.Fatalf("waits = %+v", p.Waits)
	}
	if evs := f.rl.svc.Bus.Replay("", []string{string(pigtail.EventSessionWaitChanged)}); len(evs) != 1 {
		t.Fatalf("session.wait_changed = %+v", evs)
	}
	b, err := os.ReadFile(filepath.Join(f.waitMk, "s1"))
	if err != nil || string(b) != "review\n" {
		t.Fatalf("marker = %q, %v", b, err)
	}
	got := getPeers(t, f, "")
	if len(got) != 1 || len(got[0].Waits) != 1 {
		t.Fatalf("GET /peers = %+v", got)
	}
	w := got[0].Waits[0]
	w.ID = ""
	const base = `{"type":"review","reason":"x","source":"declared","since":"2026-09-28T12:00:00Z"}`
	if s := jsonString(w); s != base {
		t.Fatalf("GET /peers waits[0] = %s, want %s", s, base)
	}
}

func TestWaitClosedEventOnReplaceClearAndEnd(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "arbitrage", Details: json.RawMessage(arbitrageDetails)})
	first := openRecord(t, f, "s1", pigtail.WaitSourceDeclared)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "x"})
	second := openRecord(t, f, "s1", pigtail.WaitSourceDeclared)
	if first == nil || second == nil || first.ID == second.ID {
		t.Fatalf("records %+v %+v", first, second)
	}
	closed := closedEvents(f)
	if len(closed) != 1 || closed[0].Data["wait_id"] != first.ID || closed[0].Data["state"] != pigtail.WaitStateReplaced {
		t.Fatalf("after replace: %+v", closed)
	}

	doJSON(t, f.r, "DELETE", "/peers/s1/wait", nil)
	closed = closedEvents(f)
	if len(closed) != 2 || closed[1].Data["wait_id"] != second.ID || closed[1].Data["state"] != pigtail.WaitStateCleared {
		t.Fatalf("after DELETE: %+v", closed)
	}

	// One declared and one auto open: the end of the session closes both.
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "review", Reason: "y"})
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Type: "permission", Reason: "Bash", Source: pigtail.WaitSourceAuto})
	ids := map[string]bool{}
	for _, src := range []string{pigtail.WaitSourceDeclared, pigtail.WaitSourceAuto} {
		r := openRecord(t, f, "s1", src)
		if r == nil {
			t.Fatalf("no open %s record", src)
		}
		ids[r.ID] = true
	}
	rec := doJSON(t, f.r, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s1"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("end = %d", rec.Code)
	}
	closed = closedEvents(f)[2:]
	if len(closed) != 2 {
		t.Fatalf("after end: %+v", closed)
	}
	for _, e := range closed {
		if !ids[e.Data["wait_id"].(string)] || e.Data["state"] != pigtail.WaitStateCleared || e.Scope != "globex" {
			t.Fatalf("after end: %+v", closed)
		}
	}

	// D29-A: a session bound to a subject ends; its answerable wait stays
	// its subject's — no wait.closed for it, only for its dialog.
	f.seedSubjectWorktree(t, "Web front", "/wt/webfront")
	f.peers(t, pigtail.PeerSession{SessionID: "s2", Name: "beta", Scope: "gx", Cwd: "/wt/webfront"})
	postWait(t, f, "s2", pigtail.SessionWaitRequest{Type: "arbitrage", Details: json.RawMessage(arbitrageDetails)})
	postWait(t, f, "s2", pigtail.SessionWaitRequest{Type: "permission", Reason: "Bash", Source: pigtail.WaitSourceAuto})
	subj := openRecord(t, f, "s2", pigtail.WaitSourceDeclared)
	dialog := openRecord(t, f, "s2", pigtail.WaitSourceAuto)
	if subj == nil || subj.SubjectID == "" || dialog == nil {
		t.Fatalf("records of s2: %+v %+v", subj, dialog)
	}
	before := len(closedEvents(f))
	doJSON(t, f.r, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s2"}}})
	closed = closedEvents(f)[before:]
	if len(closed) != 1 || closed[0].Data["wait_id"] != dialog.ID {
		t.Fatalf("after the end of s2: %+v, want the dialog's only", closed)
	}
	if r, _ := f.st.GetWaitRecord(subj.ID); r == nil || r.State != pigtail.WaitStateOpen {
		t.Fatalf("subject's wait after its emitter's end: %+v", r)
	}
}

func closedEvents(f *relayFixture) []pigtail.Event {
	return f.rl.svc.Bus.Replay("", []string{string(pigtail.EventWaitClosed)})
}

func TestDeclareAutoDialogWithDetails(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	req := pigtail.SessionWaitRequest{Source: pigtail.WaitSourceAuto, Type: "permission", Reason: "Bash",
		Details: json.RawMessage(`{"cmd":"make","where":"/w"}`)}
	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	p := decodePeer(t, rec.Body.Bytes())
	if len(p.Waits) != 1 || p.Waits[0].Source != pigtail.WaitSourceAuto || !strings.HasPrefix(p.Waits[0].ID, pigtail.PrefixWait) {
		t.Fatalf("waits = %+v", p.Waits)
	}
	r, _ := f.st.GetWaitRecord(p.Waits[0].ID)
	if r == nil || r.Details == nil || r.Details.Cmd != "make" || r.Details.Where != "/w" {
		t.Fatalf("record = %+v", r)
	}
	if evs := f.rl.svc.Bus.Replay("", []string{string(pigtail.EventWaitOpened)}); len(evs) != 1 {
		t.Fatalf("wait.opened = %+v", evs)
	}
	// The same dialog told again (its permission_prompt notification): no
	// new record, no new event.
	req.Details = nil
	doJSON(t, f.r, "POST", "/peers/s1/wait", req)
	if evs := waitEvents(f); len(evs) != 1 {
		t.Fatalf("events after the same dialog = %+v", evs)
	}
	if r2 := openRecord(t, f, "s1", pigtail.WaitSourceAuto); r2 == nil || r2.ID != r.ID || r2.Details == nil || r2.Details.Cmd != "make" {
		t.Fatalf("record after the same dialog = %+v", r2)
	}
}

// An MCP tool's PermissionRequest names it mcp__srv__tool, its notification
// with another spelling: two reasons, so two dialogs — the record is
// replaced (wait.closed replaced, a new wait.opened), never merged.
func TestAutoDialogOtherToolNameReplacesRecord(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Source: pigtail.WaitSourceAuto, Type: "permission", Reason: "mcp__srv__tool",
		Details: json.RawMessage(`{"cmd":"mcp__srv__tool","where":"/w"}`)})
	first := openRecord(t, f, "s1", pigtail.WaitSourceAuto)
	postWait(t, f, "s1", pigtail.SessionWaitRequest{Source: pigtail.WaitSourceAuto, Type: "permission", Reason: "srv - tool (MCP)"})
	second := openRecord(t, f, "s1", pigtail.WaitSourceAuto)
	if first == nil || second == nil || first.ID == second.ID || second.Details != nil || second.Reason != "srv - tool (MCP)" {
		t.Fatalf("records %+v / %+v", first, second)
	}
	old, err := f.st.GetWaitRecord(first.ID)
	if err != nil || old.State != pigtail.WaitStateReplaced || old.Details == nil || old.Details.Cmd != "mcp__srv__tool" {
		t.Fatalf("old record = %+v, %v", old, err)
	}
	evs := waitEvents(f)
	if len(evs) != 3 || evs[1].Type != pigtail.EventWaitClosed || evs[1].Data["wait_id"] != first.ID ||
		evs[1].Data["state"] != pigtail.WaitStateReplaced || evs[2].Type != pigtail.EventWaitOpened || evs[2].Data["wait_id"] != second.ID {
		t.Fatalf("events = %+v", evs)
	}
}

func TestAutoWaitInvalidDetailsKeepsWait(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Source: pigtail.WaitSourceAuto, Type: "permission", Reason: "Bash",
		Details: json.RawMessage(`{"cmd":"curl -H ghp_0123456789abcdefghij0123456789abcd"}`)})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s (the hook fails open)", rec.Code, rec.Body.String())
	}
	ws, _ := f.st.SessionWaits("s1")
	if len(ws) != 1 || ws[0].Source != pigtail.WaitSourceAuto || ws[0].ID == "" {
		t.Fatalf("waits = %+v", ws)
	}
	r, err := f.st.GetWaitRecord(ws[0].ID)
	if err != nil || r.Details != nil {
		t.Fatalf("record = %+v, %v (details must be dropped)", r, err)
	}
}

func TestHeldWaitRefusesDetails(t *testing.T) {
	t.Parallel()
	f := newWaitFixture(t)
	rec := doJSON(t, f.r, "POST", "/peers/s1/wait", pigtail.SessionWaitRequest{Source: pigtail.WaitSourceHeld, Type: "mac", HolderPID: 42,
		Details: json.RawMessage(`{"summary":"x"}`)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if ws, _ := f.st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("waits = %+v", ws)
	}
}

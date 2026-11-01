package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// hasWaiter reports whether a long poll of session is registered right now,
// read under the relay's lock (watched also answers true during the grace).
func (f *relayFixture) hasWaiter(session string) bool {
	f.rl.mu.Lock()
	defer f.rl.mu.Unlock()
	return f.rl.waiters[session] != nil
}

type relayFixture struct {
	r      *chi.Mux
	st     *store.Store
	rl     *Relay
	marker string
	waitMk string
	mu     sync.Mutex
	now    time.Time
}

func (f *relayFixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *relayFixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()
	return newRelayFixtureAt(t, ":memory:")
}

// newRelayFixtureAt is newRelayFixture on the store at dsn (a file, when a
// test must reach the database from the side, e.g. a trigger).
func newRelayFixtureAt(t *testing.T, dsn string) *relayFixture {
	t.Helper()
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &relayFixture{st: st, marker: t.TempDir(), waitMk: filepath.Join(t.TempDir(), "wait"), now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	svc := &Services{Store: st, Logf: t.Logf}
	f.rl = NewRelay(svc, RelayConfig{
		MarkerDir:     f.marker,
		WaitMarkerDir: f.waitMk,
		Now:           f.clock,
		Alive:         func(p pigtail.PeerSession) bool { return p.EndedAt == nil },
	})
	f.r = chi.NewRouter()
	RegisterSubjectRoutes(f.r, svc, testResponder{})
	RegisterRelayRoutes(f.r, f.rl, testResponder{})
	return f
}

// seedSubjectWorktree creates a subject whose implement phase runs in a
// workspace at path, and returns the subject.
func (f *relayFixture) seedSubjectWorktree(t *testing.T, title, path string) pigtail.Subject {
	t.Helper()
	th := seedThemeAPI(t, f.r)
	rec := doJSON(t, f.r, "POST", "/subjects", pigtail.Subject{ThemeID: th, Title: title})
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	_ = f.st.CreateProject(&pigtail.Project{ID: "prj_" + sub.Slug, Name: sub.Slug, RootPath: "/r", DefaultBackend: "git", PortRanges: map[string]pigtail.PortRange{}})
	ws := &pigtail.Workspace{ID: "ws_" + sub.Slug, ProjectID: "prj_" + sub.Slug, State: pigtail.StateBuilding, Path: path, BackendKind: "git"}
	if err := f.st.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_" + sub.Slug, SubjectID: sub.ID, Kind: pigtail.PhaseImplement,
		State: pigtail.PhaseActive, WorkspaceID: ws.ID, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return sub
}

func (f *relayFixture) peers(t *testing.T, ps ...pigtail.PeerSession) {
	t.Helper()
	rec := doJSON(t, f.r, "POST", "/peers", pigtail.PeersRequest{Peers: ps})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /peers = %d %s", rec.Code, rec.Body.String())
	}
}

func (f *relayFixture) send(t *testing.T, req pigtail.SendMessageRequest) (*httptest.ResponseRecorder, pigtail.SendMessageResponse) {
	t.Helper()
	rec := doJSON(t, f.r, "POST", "/messages", req)
	var out pigtail.SendMessageResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func errCode(rec *httptest.ResponseRecorder) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Code
}

func standardPeers(f *relayFixture, t *testing.T) pigtail.Subject {
	sub := f.seedSubjectWorktree(t, "Session relay", "/wt/feat-session-relay")
	f.peers(t,
		pigtail.PeerSession{SessionID: "s-alpha", Cwd: "/wt/feat-session-relay/cmd", Name: "globex-a1", Scope: "gx", CellID: "w2"},
		pigtail.PeerSession{SessionID: "s-beta", CmuxTitle: "Panel UX", Name: "globex-b2", Scope: "gx", CellID: "h1"},
		pigtail.PeerSession{SessionID: "s-perso", CmuxTitle: "Garden", Scope: "perso", CellID: "w1"},
	)
	return sub
}

func TestPeersEnrichedWithWorktreeAndSubject(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	sub := standardPeers(f, t)
	rec := doJSON(t, f.r, "GET", "/peers", nil)
	var ps []pigtail.PeerSession
	_ = json.Unmarshal(rec.Body.Bytes(), &ps)
	var alpha *pigtail.PeerSession
	for i := range ps {
		if ps[i].SessionID == "s-alpha" {
			alpha = &ps[i]
		}
	}
	if alpha == nil || alpha.WorkspaceName != "feat-session-relay" || alpha.SubjectID != sub.ID ||
		alpha.SubjectSlug != sub.Slug || alpha.Label != pigtail.ShortTitle(sub)+" · globex-a1 (w2)" {
		t.Fatalf("alpha = %+v", alpha)
	}
	// SessionEnd drops it from the live list.
	doJSON(t, f.r, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s-beta"}}})
	rec = doJSON(t, f.r, "GET", "/peers", nil)
	if strings.Contains(rec.Body.String(), "s-beta") {
		t.Fatalf("ended peer listed: %s", rec.Body.String())
	}
}

func TestSendClaimAuditAndMarker(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	sub := standardPeers(f, t)

	// beta → alpha, addressed by subject slug.
	rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: sub.Slug, Kind: "request", Body: "About to touch go.mod\nhold on"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("send = %d %s", rec.Code, rec.Body.String())
	}
	if out.Message.ToSession != "s-alpha" || out.Pending != 1 || !strings.HasPrefix(out.Message.ThreadID, "thr_") ||
		out.Message.SubjectID != sub.ID || out.Message.FromLabel != "Panel UX · globex-b2 (h1)" ||
		out.Message.ToLabel != pigtail.ShortTitle(sub)+" · globex-a1 (w2)" {
		t.Fatalf("out = %+v", out)
	}
	marker := filepath.Join(f.marker, "s-alpha")
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("marker = %q %v", b, err)
	}
	logs, _ := f.st.ListLog(sub.ID, time.Time{})
	if len(logs) != 1 || logs[0].Kind != "message" || !strings.HasPrefix(logs[0].Text, "← Panel UX · globex-b2 (h1) [request msg_") {
		t.Fatalf("subject log = %+v", logs)
	}

	rec = doJSON(t, f.r, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-alpha", Via: "prompt"})
	var cl pigtail.ClaimResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &cl)
	if len(cl.Messages) != 1 || cl.Messages[0].DeliveredVia != "prompt" {
		t.Fatalf("claim = %s", rec.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker not cleared: %v", err)
	}

	// Reply from alpha, back to beta, same thread; alpha's subject logs "→".
	rec, rep := f.send(t, pigtail.SendMessageRequest{From: "s-alpha", InReplyTo: out.Message.ID, Body: "go ahead"})
	if rec.Code != http.StatusCreated || rep.Message.ToSession != "s-beta" || rep.Message.ThreadID != out.Message.ThreadID || rep.Message.Kind != "reply" {
		t.Fatalf("reply = %d %s", rec.Code, rec.Body.String())
	}
	logs, _ = f.st.ListLog(sub.ID, time.Time{})
	if len(logs) != 2 {
		t.Fatalf("logs = %+v", logs)
	}
	// Self-reply refused: beta cannot reply to its own message.
	rec, _ = f.send(t, pigtail.SendMessageRequest{From: "s-beta", InReplyTo: out.Message.ID, Body: "me again"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self-reply = %d %s", rec.Code, rec.Body.String())
	}
	// Only the recipient acks.
	rec = doJSON(t, f.r, "POST", "/messages/"+rep.Message.ID+"/ack", pigtail.AckRequest{Session: "s-alpha"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ack by sender = %d", rec.Code)
	}
	rec = doJSON(t, f.r, "POST", "/messages/"+rep.Message.ID+"/ack", pigtail.AckRequest{Session: "s-beta"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"acked"`) {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSendRefusals(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-gamma", CmuxTitle: "Panel review", Scope: "gx"})

	cases := []struct {
		name string
		req  pigtail.SendMessageRequest
		code int
		err  string
	}{
		{"unknown sender", pigtail.SendMessageRequest{From: "nobody", To: "s-beta", Body: "x"}, 400, "INVALID_REQUEST"},
		// A perso session is no candidate for a work sender (the perso → work
		// refusal is in TestWorkSenderDoesNotSeePersoSessions).
		{"perso recipient", pigtail.SendMessageRequest{From: "s-beta", To: "Garden", Body: "x"}, 404, "ADDRESS_UNRESOLVED"},
		{"secret", pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "use ghp_abcdefghijklmnop123"}, 422, "RELAY_REFUSED"},
		{"too big", pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: strings.Repeat("y", 3000)}, 422, "RELAY_REFUSED"},
		{"ambiguous", pigtail.SendMessageRequest{From: "s-alpha", To: "panel", Body: "x"}, 409, "ADDRESS_AMBIGUOUS"},
		{"unresolved", pigtail.SendMessageRequest{From: "s-alpha", To: "zzz-none", Body: "x"}, 404, "ADDRESS_UNRESOLVED"},
		{"self", pigtail.SendMessageRequest{From: "s-beta", To: "Panel UX", Body: "x"}, 404, "ADDRESS_UNRESOLVED"},
		{"bad kind", pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Kind: "reply", Body: "x"}, 400, "INVALID_REQUEST"},
	}
	for _, c := range cases {
		rec, _ := f.send(t, c.req)
		if rec.Code != c.code || errCode(rec) != c.err {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body.String(), c.code, c.err)
		}
	}
	// The ambiguity names each candidate by its unique label.
	rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-alpha", To: "panel", Body: "x"})
	if !strings.Contains(rec.Body.String(), "Panel UX · globex-b2 (h1) [s-beta]") ||
		!strings.Contains(rec.Body.String(), "Panel review · s-gamma [s-gamma]") {
		t.Fatalf("candidates = %s", rec.Body.String())
	}
}

// Two sessions in one cmux workspace share its title. Their labels differ by
// the native name, and either form of the label addresses one of them.
func TestSendDisambiguatesOneWorkspace(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	const title = "Agents avec playwright en environnement virtuel"
	f.peers(t,
		pigtail.PeerSession{SessionID: "s-d4", CmuxTitle: title, Name: "globex-d4", Scope: "gx", CellID: "w2"},
		pigtail.PeerSession{SessionID: "s-e5", CmuxTitle: title, Name: "globex-e5", Scope: "gx", CellID: "h1"},
		pigtail.PeerSession{SessionID: "s-x", CmuxTitle: "Elsewhere", Name: "globex-x", Scope: "gx", CellID: "h1"},
	)
	for addr, want := range map[string]string{
		"globex-d4": "s-d4",
		"Agents avec playwright en envir… · globex-e5 (h1)": "s-e5",
		"agents avec playwright (w2)":                       "s-d4",
	} {
		rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-x", To: addr, Body: "hi"})
		if rec.Code != http.StatusCreated || out.Message.ToSession != want {
			t.Fatalf("to %q = %d %s", addr, rec.Code, rec.Body.String())
		}
	}
	_, out := f.send(t, pigtail.SendMessageRequest{From: "s-d4", To: "globex-e5", Body: "hi"})
	if out.Message.FromLabel != "Agents avec playwright en envir… · globex-d4 (w2)" ||
		out.Message.ToLabel != "Agents avec playwright en envir… · globex-e5 (h1)" {
		t.Fatalf("labels = %q → %q", out.Message.FromLabel, out.Message.ToLabel)
	}
}

func TestPendingCapAndRateLimit(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-gamma", Scope: "gx"})

	for i := 0; i < 5; i++ {
		if rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "n"}); rec.Code != 201 {
			t.Fatalf("send %d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-gamma", To: "s-alpha", Body: "one more"})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "unread") {
		t.Fatalf("pending cap: %d %s", rec.Code, rec.Body.String())
	}
	doJSON(t, f.r, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-alpha"})
	rec, _ = f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "sixth"})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "rate limit") {
		t.Fatalf("rate limit: %d %s", rec.Code, rec.Body.String())
	}
	f.advance(11 * time.Minute)
	if rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "later"}); rec.Code != 201 {
		t.Fatalf("after window: %d %s", rec.Code, rec.Body.String())
	}
}

func TestThreadCap(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	_, first := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Kind: "request", Body: "q"})
	last := first.Message
	senders := []string{"s-alpha", "s-beta"}
	for i := 0; i < 5; i++ {
		rec, out := f.send(t, pigtail.SendMessageRequest{From: senders[i%2], InReplyTo: last.ID, Body: "r"})
		if rec.Code != 201 {
			t.Fatalf("reply %d = %d %s", i, rec.Code, rec.Body.String())
		}
		last = out.Message
		f.advance(3 * time.Minute) // stay under the rate limit
	}
	rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-beta", InReplyTo: last.ID, Body: "seventh"})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "escalate to the user") {
		t.Fatalf("thread cap: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWaitWakesAndSupersedes(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)

	type result struct {
		code int
		body pigtail.ClaimResponse
	}
	wait := func() <-chan result {
		ch := make(chan result, 1)
		go func() {
			rec := doJSON(t, f.r, "GET", "/messages/wait?session=s-alpha&timeout=5", nil)
			var cr pigtail.ClaimResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &cr)
			ch <- result{rec.Code, cr}
		}()
		return ch
	}
	first := wait()
	testutil.Eventually(t, 30*time.Second, "first waiter registered", func() bool { return f.rl.watched("s-alpha") })
	second := wait()
	select {
	case r := <-first:
		if !r.body.Superseded {
			t.Fatalf("first waiter not superseded: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first waiter never superseded")
	}
	// register supersedes the first waiter and records the second under the
	// same lock: the second is in place once the first is superseded.
	testutil.Eventually(t, 30*time.Second, "second waiter registered", func() bool { return f.hasWaiter("s-alpha") })
	f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "wake up"})
	select {
	case r := <-second:
		if len(r.body.Messages) != 1 || r.body.Messages[0].DeliveredVia != "rewake" {
			t.Fatalf("second waiter = %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter never woke")
	}
}

func TestExpiryNotifiesSender(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	_, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "anyone?", TTLSeconds: 60})
	f.advance(2 * time.Minute)
	f.rl.expire()
	m, _ := f.st.GetMessage(out.Message.ID)
	if m.State != "expired" {
		t.Fatalf("state = %s", m.State)
	}
	if _, err := os.Stat(filepath.Join(f.marker, "s-alpha")); !os.IsNotExist(err) {
		t.Fatal("recipient marker survived expiry")
	}
	rec := doJSON(t, f.r, "POST", "/messages/claim", pigtail.ClaimRequest{Session: "s-beta"})
	var cl pigtail.ClaimResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &cl)
	if len(cl.Messages) != 1 || cl.Messages[0].FromSession != pigtail.SystemSender || !strings.Contains(cl.Messages[0].Body, "expired unread") {
		t.Fatalf("notice = %s", rec.Body.String())
	}
	// A system notice takes no reply.
	rec, _ = f.send(t, pigtail.SendMessageRequest{From: "s-beta", InReplyTo: cl.Messages[0].ID, Body: "ok"})
	if rec.Code != 422 {
		t.Fatalf("reply to notice = %d", rec.Code)
	}
}

func namedDir(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSendByLastWorktreeAndGonePath(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	live := namedDir(t, "feat-tws-error-pages-ds")
	at := f.now.Add(-3 * time.Hour)
	f.peers(t,
		pigtail.PeerSession{SessionID: "s-from", Name: "globex-a1", Scope: "gx", CellID: "w2"},
		pigtail.PeerSession{SessionID: "s-to", Name: "globex-21", Scope: "gx", CellID: "h1",
			LastWorktree: live, LastWorktreeAt: &at},
		pigtail.PeerSession{SessionID: "s-gone", Name: "globex-d6", Scope: "gx", CellID: "h1",
			LastWorktree: "/definitely/deleted/feat-cal-ds-39-slot-hover", LastWorktreeAt: &at},
	)
	rec, res := f.send(t, pigtail.SendMessageRequest{From: "s-from", To: "feat-tws-error-pages-ds", Body: "hi"})
	if rec.Code != http.StatusCreated || res.To.SessionID != "s-to" || res.MatchedBy != "last worktree" {
		t.Fatalf("send by last worktree = %d %s (matched_by %q)", rec.Code, rec.Body.String(), res.MatchedBy)
	}
	rec, _ = f.send(t, pigtail.SendMessageRequest{From: "s-from", To: "feat-cal-ds-39-slot-hover", Body: "hi"})
	if rec.Code != http.StatusNotFound || errCode(rec) != string(pigtail.ErrAddressUnresolved) {
		t.Fatalf("a deleted last worktree must not be addressable: %d %s", rec.Code, rec.Body.String())
	}
}

// R1 end to end: A stored at t1, then the session moves to B (t2), B is
// deleted. The store keeps B (newest), which is hidden; A must NOT come back.
// Non-regression guard: green before the "last worktree" level exists (nothing
// resolves by marker then); it pins that the level never revives A.
func TestSendIgnoresOlderWorktreeAfterMoveToDeleted(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	a := namedDir(t, "feat-first-place")
	b := filepath.Join(t.TempDir(), "feat-second-place") // never created = deleted
	t1, t2 := f.now.Add(-2*time.Hour), f.now.Add(-time.Hour)
	f.peers(t,
		pigtail.PeerSession{SessionID: "s-from", Name: "globex-a1", Scope: "gx", CellID: "w2"},
		pigtail.PeerSession{SessionID: "s-x", Name: "globex-x1", Scope: "gx", CellID: "h1", LastWorktree: a, LastWorktreeAt: &t1},
	)
	f.peers(t, pigtail.PeerSession{SessionID: "s-x", LastWorktree: b, LastWorktreeAt: &t2})
	for _, addr := range []string{"feat-first-place", "feat-second-place"} {
		rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-from", To: addr, Body: "hi"})
		if rec.Code != http.StatusNotFound || errCode(rec) != string(pigtail.ErrAddressUnresolved) {
			t.Fatalf("send %s after move to a deleted worktree = %d %s", addr, rec.Code, rec.Body.String())
		}
	}
}

// setStatus merges a native status into a registered peer (UpsertPeer keeps
// every other field: an empty value never erases a known one).
func (f *relayFixture) setStatus(t *testing.T, session, status string) {
	t.Helper()
	f.peers(t, pigtail.PeerSession{SessionID: session, Status: status})
}

func TestSendDeliveryReportsWhetherRecipientWakes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"idle", pigtail.DeliveryNone},
		{"busy", pigtail.DeliveryBusy},
		{"waiting", pigtail.DeliveryBusy},
		{"Busy ", pigtail.DeliveryBusy}, // case and padding do not matter
		{"", pigtail.DeliveryUnknown},
		{"compacting", pigtail.DeliveryUnknown}, // a status we do not know
	} {
		t.Run("status="+tc.status, func(t *testing.T) {
			f := newRelayFixture(t)
			standardPeers(f, t)
			if tc.status != "" {
				f.setStatus(t, "s-alpha", tc.status)
			}
			rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "hello"})
			if rec.Code != http.StatusCreated {
				t.Fatalf("send = %d %s", rec.Code, rec.Body.String())
			}
			if out.Delivery != tc.want {
				t.Fatalf("delivery = %q, want %q", out.Delivery, tc.want)
			}
			// Whatever the delivery, the message is queued and flagged.
			if out.Message.State != pigtail.MessageStatePending || out.Pending != 1 {
				t.Fatalf("message not queued: state=%s pending=%d", out.Message.State, out.Pending)
			}
			if _, err := os.Stat(filepath.Join(f.marker, "s-alpha")); err != nil {
				t.Fatalf("inbox marker missing: %v", err)
			}
		})
	}
}

func TestSendDeliveryWithLiveWaiter(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.setStatus(t, "s-alpha", "idle") // the resting state of a watched session
	done := make(chan pigtail.ClaimResponse, 1)
	go func() {
		rec := doJSON(t, f.r, "GET", "/messages/wait?session=s-alpha&timeout=5", nil)
		var cr pigtail.ClaimResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &cr)
		done <- cr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !f.rl.watched("s-alpha") {
		if time.Now().After(deadline) {
			t.Fatal("waiter never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "wake up"})
	if out.Delivery != pigtail.DeliveryWaiter {
		t.Fatalf("delivery = %q, want waiter", out.Delivery)
	}
	select {
	case cr := <-done:
		if len(cr.Messages) != 1 || cr.Messages[0].DeliveredVia != "rewake" {
			t.Fatalf("waiter = %+v", cr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter never woke")
	}
}

func TestSendDeliveryWithinWatchGrace(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.setStatus(t, "s-alpha", "idle")
	// A poll that returns at once: register then unregister, as between two
	// polls of the Stop watcher.
	if rec := doJSON(t, f.r, "GET", "/messages/wait?session=s-alpha&timeout=0", nil); rec.Code != http.StatusOK {
		t.Fatalf("wait = %d", rec.Code)
	}
	_, out := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "between polls"})
	if out.Delivery != pigtail.DeliveryWaiter {
		t.Fatalf("within grace: delivery = %q, want waiter", out.Delivery)
	}
	f.advance(watchGrace + time.Second)
	_, out = f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "watcher gone"})
	if out.Delivery != pigtail.DeliveryNone {
		t.Fatalf("past grace: delivery = %q, want none", out.Delivery)
	}
	// The sweep forgets sessions seen longer ago than the grace.
	f.rl.expire()
	f.rl.mu.Lock()
	n := len(f.rl.lastSeen)
	f.rl.mu.Unlock()
	if n != 0 {
		t.Fatalf("lastSeen not pruned: %d entries", n)
	}
}

func TestReplyDeliveryWarnsToo(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.setStatus(t, "s-alpha", "busy")
	_, first := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: "s-alpha", Body: "question"})
	if first.Delivery != pigtail.DeliveryBusy {
		t.Fatalf("first: delivery = %q, want busy", first.Delivery)
	}
	f.setStatus(t, "s-beta", "idle") // the asker went idle and its watcher died
	rec, back := f.send(t, pigtail.SendMessageRequest{From: "s-alpha", InReplyTo: first.Message.ID, Body: "answer"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply = %d %s", rec.Code, rec.Body.String())
	}
	if back.Delivery != pigtail.DeliveryNone || !back.WillNotWake() {
		t.Fatalf("reply: delivery = %q, want none", back.Delivery)
	}
}

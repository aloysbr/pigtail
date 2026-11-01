package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/space/spacetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Work/perso isolation of the relay's read routes: the peer registry and the
// message audit trail must not carry perso subjects (nor perso conversations)
// to a work caller. A work session's row stays listed with the subject it works
// on stripped when that subject is perso; a perso session's row is not listed
// to a work caller at all (relay_session_scope_test.go).

// seedScopedSubjectWorktree is seedSubjectWorktree for a theme of either
// scope: c creates the theme (vault) and the subject, whose implement phase
// runs in a workspace at path.
func (f *relayFixture) seedScopedSubjectWorktree(t *testing.T, c caller, vault, title, path string) pigtail.Subject {
	t.Helper()
	rec := doAs(t, f.r, c, "POST", "/themes", pigtail.Theme{Name: title + " theme", Kind: "project", VaultLink: vault})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create theme %s = %d %s", vault, rec.Code, rec.Body.String())
	}
	var th pigtail.Theme
	_ = json.Unmarshal(rec.Body.Bytes(), &th)
	rec = doAs(t, f.r, c, "POST", "/subjects", pigtail.Subject{ThemeID: th.ID, Title: title})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subject = %d %s", rec.Code, rec.Body.String())
	}
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

// scopedPeers seeds a work subject and a perso subject, and five sessions.
// The perso worktree is not named after its subject on purpose: worktree
// names belong to the workspace layer, which is not scoped (GET /workspaces
// lists every worktree) — what is asserted here is the subject layer.
//
//	s-work     work cell, in the work subject's worktree
//	s-mate     work cell, no worktree (a work peer to message from)
//	s-onperso  work cell, in the perso subject's worktree
//	s-perso    perso cell, in the perso subject's worktree
//	s-perso2   perso cell, no worktree
func scopedPeers(f *relayFixture, t *testing.T) (workSub, persoSub pigtail.Subject) {
	t.Helper()
	workSub = f.seedScopedSubjectWorktree(t, work, "Globex/Projects/relay", "Work relay work", "/wt/work-relay")
	persoSub = f.seedScopedSubjectWorktree(t, perso, "Perso/Areas/garden", "Garden glazings", "/wt/feat-glazings")
	f.peers(t,
		pigtail.PeerSession{SessionID: "s-work", Cwd: "/wt/work-relay", Name: "work-a", Scope: "gx", CellID: "mt"},
		pigtail.PeerSession{SessionID: "s-mate", CmuxTitle: "Mate", Name: "work-m", Scope: "gx", CellID: "it"},
		pigtail.PeerSession{SessionID: "s-onperso", Cwd: "/wt/feat-glazings/api", Name: "work-p", Scope: "gx", CellID: "mt"},
		pigtail.PeerSession{SessionID: "s-perso", Cwd: "/wt/feat-glazings", Name: "perso-a", Scope: "perso", CellID: "mp"},
		pigtail.PeerSession{SessionID: "s-perso2", CmuxTitle: "Perso two", Name: "perso-b", Scope: "perso", CellID: "mp"},
	)
	return workSub, persoSub
}

func peersAs(t *testing.T, f *relayFixture, c caller, method string, body any) map[string]pigtail.PeerSession {
	t.Helper()
	rec := doAs(t, f.r, c, method, "/peers", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s /peers as %s = %d %s", method, c, rec.Code, rec.Body.String())
	}
	var ps []pigtail.PeerSession
	if err := json.Unmarshal(rec.Body.Bytes(), &ps); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]pigtail.PeerSession, len(ps))
	for _, p := range ps {
		out[p.SessionID] = p
	}
	return out
}

func TestPeersRedactPersoSubjectsForWork(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	workSub, persoSub := scopedPeers(f, t)
	snapshot := pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: "s-work"}, {SessionID: "s-onperso"}, {SessionID: "s-perso"},
	}}

	for _, c := range []caller{none, work} {
		for _, method := range []string{"GET", "POST"} {
			var body any
			if method == "POST" {
				body = snapshot
			}
			ps := peersAs(t, f, c, method, body)
			for _, id := range []string{"s-work", "s-onperso"} {
				if _, ok := ps[id]; !ok {
					t.Fatalf("%s %s: work peer %s hidden — only its subject should be stripped", method, c, id)
				}
			}
			if p := ps["s-work"]; p.SubjectID != workSub.ID || p.SubjectSlug != workSub.Slug {
				t.Errorf("%s %s: work subject stripped from s-work: %+v", method, c, p)
			}
			for _, id := range []string{"s-onperso"} {
				p := ps[id]
				if p.SubjectID != "" || p.SubjectSlug != "" || strings.Contains(p.Label, persoSub.Slug) ||
					p.SubjectShortTitle != "" || p.CmuxBound || strings.Contains(p.Label, persoSub.Title) {
					t.Errorf("%s %s: perso subject leaks on %s: %+v", method, c, id, p)
				}
			}
		}
	}

	ps := peersAs(t, f, perso, "GET", nil)
	for _, id := range []string{"s-onperso", "s-perso"} {
		if p := ps[id]; p.SubjectID != persoSub.ID || p.SubjectSlug != persoSub.Slug || !strings.HasPrefix(p.Label, pigtail.ShortTitle(persoSub)) {
			t.Errorf("perso caller must see %s's subject: %+v", id, p)
		}
	}
	if p := ps["s-work"]; p.SubjectID != workSub.ID {
		t.Errorf("perso caller must see work subjects too: %+v", p)
	}

	// Redaction is a view: the registry keeps the subject.
	if got, err := f.st.GetPeer("s-perso"); err != nil || got.SubjectID != persoSub.ID {
		t.Fatalf("stored peer lost its subject: %+v %v", got, err)
	}
}

func TestPeersOrphanSubjectFailsClosed(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-orphan", SubjectID: "sub_gone", Name: "o", Scope: "gx"})
	if p := peersAs(t, f, work, "GET", nil)["s-orphan"]; p.SubjectID != "" {
		t.Fatalf("unknown subject must read as perso for a work caller: %+v", p)
	}
	if p := peersAs(t, f, perso, "GET", nil)["s-orphan"]; p.SubjectID != "sub_gone" {
		t.Fatalf("perso caller sees everything: %+v", p)
	}
}

func TestPeersInvalidScopeHeader(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	if rec := doAs(t, f.r, caller{scope: "everything"}, "GET", "/peers", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope = %d, want 400", rec.Code)
	}
}

// A work sender cannot address a session by a perso subject, and a message it
// sends to a work session working on one neither carries nor logs that subject.
func TestSendFromWorkIgnoresPersoSubjects(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	_, persoSub := scopedPeers(f, t)

	rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-mate", To: persoSub.Slug, Body: "hi"})
	if rec.Code != http.StatusNotFound || errCode(rec) != string(pigtail.ErrAddressUnresolved) {
		t.Fatalf("work sender resolved a perso subject slug: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = f.send(t, pigtail.SendMessageRequest{From: "s-mate", To: persoSub.ID, Body: "hi"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("work sender resolved a perso subject id: %d %s", rec.Code, rec.Body.String())
	}

	rec, out := f.send(t, pigtail.SendMessageRequest{From: "s-mate", To: "work-p", Body: "hold on"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("send = %d %s", rec.Code, rec.Body.String())
	}
	if out.Message.SubjectID != "" || strings.Contains(out.Message.ToLabel, persoSub.Slug) {
		t.Fatalf("work message carries the perso subject: %+v", out.Message)
	}
	if logs, _ := f.st.ListLog(persoSub.ID, time.Time{}); len(logs) != 0 {
		t.Fatalf("work message logged onto a perso subject: %+v", logs)
	}

	// Perso ↔ perso still names the subject.
	rec, out = f.sendAs(t, perso, pigtail.SendMessageRequest{From: "s-perso2", To: "perso-a", Body: "perso note"})
	if rec.Code != http.StatusCreated || out.Message.SubjectID != persoSub.ID {
		t.Fatalf("perso send = %d %+v", rec.Code, out.Message)
	}
}

func TestMessagesHidePersoConversationsFromWork(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	workSub, _ := scopedPeers(f, t)

	rec, workMsg := f.send(t, pigtail.SendMessageRequest{From: "s-mate", To: workSub.Slug, Body: "work body"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("work send = %d %s", rec.Code, rec.Body.String())
	}
	rec, persoMsg := f.sendAs(t, perso, pigtail.SendMessageRequest{From: "s-perso2", To: "perso-a", Body: "perso secret"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("perso send = %d %s", rec.Code, rec.Body.String())
	}

	for _, c := range []caller{none, work} {
		rec := doAs(t, f.r, c, "GET", "/messages", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /messages as %s = %d", c, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "perso secret") || strings.Contains(body, persoMsg.Message.ID) {
			t.Errorf("GET /messages as %s lists a perso conversation: %s", c, body)
		}
		if !strings.Contains(body, workMsg.Message.ID) {
			t.Errorf("GET /messages as %s lost the work message: %s", c, body)
		}
		if rec := doAs(t, f.r, c, "GET", "/messages/"+persoMsg.Message.ID, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET /messages/{perso} as %s = %d, want 404", c, rec.Code)
		}
		if rec := doAs(t, f.r, c, "GET", "/messages?limit=1", nil); !strings.Contains(rec.Body.String(), workMsg.Message.ID) {
			t.Errorf("limit must apply after the scope filter (as %s): %s", c, rec.Body.String())
		}
	}

	rec = doAs(t, f.r, perso, "GET", "/messages", nil)
	if body := rec.Body.String(); !strings.Contains(body, persoMsg.Message.ID) || !strings.Contains(body, workMsg.Message.ID) {
		t.Fatalf("perso caller sees every conversation: %s", body)
	}
	if rec := doAs(t, f.r, perso, "GET", "/messages/"+persoMsg.Message.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /messages/{perso} as perso = %d", rec.Code)
	}
}

// Every message event the relay publishes carries the scope of its
// conversation, so the SSE stream can filter it; a work conversation's event
// never names a perso subject.
func TestRelayEventsCarryTheConversationScope(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	bus := events.New()
	f.rl.svc.Bus = bus
	workSub, _ := scopedPeers(f, t)

	if rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-mate", To: workSub.Slug, Body: "gx"}); rec.Code != http.StatusCreated {
		t.Fatalf("work send = %d", rec.Code)
	}
	if rec, _ := f.sendAs(t, perso, pigtail.SendMessageRequest{From: "s-perso2", To: "perso-a", Body: "perso"}); rec.Code != http.StatusCreated {
		t.Fatalf("perso send = %d", rec.Code)
	}
	var got []string
	for _, e := range bus.Replay("", []string{"message.*"}) {
		got = append(got, e.Data["from_session"].(string)+"="+e.Scope)
	}
	if strings.Join(got, ",") != "s-mate=globex,s-perso2=perso" {
		t.Fatalf("event scopes = %v", got)
	}
}

// A conversation between two sessions of a third scope is tagged with that
// scope, not with the master's (base: anything not work was tagged perso).
func TestRelayEventsCarryAThirdScope(t *testing.T) {
	scopetest.Three(t)
	f := newRelayFixture(t)
	bus := events.New()
	f.rl.svc.Bus = bus
	for _, p := range []pigtail.PeerSession{
		{SessionID: "s-a1", Name: "acme-1", Scope: "acme", CellID: "ma"},
		{SessionID: "s-a2", Name: "acme-2", Scope: "acme", CellID: "ma"},
	} {
		p := p
		if err := f.st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
	}
	if rec, _ := f.sendAs(t, caller{scope: "acme"}, pigtail.SendMessageRequest{From: "s-a1", To: "acme-2", Body: "acme"}); rec.Code != http.StatusCreated {
		t.Fatalf("acme send = %d %s", rec.Code, rec.Body.String())
	}
	var got []string
	for _, e := range bus.Replay("", []string{"message.*"}) {
		got = append(got, e.Data["from_session"].(string)+"="+e.Scope)
	}
	if strings.Join(got, ",") != "s-a1=acme" {
		t.Fatalf("event scopes = %v, want [s-a1=acme]", got)
	}
}

// A session whose cell scope the table does not declare is seen by the
// master only (base: read as work, so every work caller saw it).
func TestUndeclaredScopeSessionIsMasterOnly(t *testing.T) {
	scopetest.Three(t)
	f := newRelayFixture(t)
	if err := f.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s-z", Name: "zeta-a", Scope: "zeta", CellID: "mz"}); err != nil {
		t.Fatal(err)
	}
	for c, want := range map[string]bool{"globex": false, "acme": false, "perso": true} {
		if _, seen := peersAs(t, f, caller{scope: c}, "GET", nil)["s-z"]; seen != want {
			t.Errorf("GET /peers as %s shows the zeta session = %v, want %v", c, seen, want)
		}
	}
}

// a session with no scope is seen by the master only, on every path —
// /peers, GET /messages, its message and wait events, and the reason it set
// in the cmux sidebar. One answer per caller, equal on the four paths.
func TestUnscopedSessionMasterOnlyOnEveryPath(t *testing.T) {
	scopetest.Three(t)
	f := newRelayFixture(t)
	bus := events.New()
	f.rl.svc.Bus = bus
	for _, p := range []pigtail.PeerSession{
		{SessionID: "s-none", Name: "none-a"},
		{SessionID: "s-t", Name: "wk-a", Scope: "globex", CellID: "mt"},
	} {
		p := p
		if err := f.st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
	}
	m := &pigtail.Message{ID: "msg_none", ThreadID: "msg_none", FromSession: "s-none", ToSession: "s-t",
		Kind: "info", Body: "from nowhere", State: "pending", CreatedAt: f.now, ExpiresAt: f.now.Add(time.Hour)}
	if err := f.st.CreateMessage(m); err != nil {
		t.Fatal(err)
	}
	f.rl.publish(pigtail.EventMessageCreated, *m)
	none, err := f.st.GetPeer("s-none")
	if err != nil {
		t.Fatal(err)
	}
	f.rl.publishWait(*none, "review", pigtail.WaitSourceDeclared)

	sr, _, ctl := sidebarFixture(t)
	ctl.state.Workspaces = []pigtail.CmuxSidebarWorkspace{
		{CmuxWorkspaceID: cmuxWS, Attention: "waiting", WaitingCount: 1, WaitType: "review", WaitReason: "PR #9", WaitScope: "", WaitFromSession: true},
	}

	for _, name := range []string{"globex", "acme", "perso"} {
		c := caller{scope: name}
		want := scope.SeesAll(scope.Scope(name))
		paths := map[string]bool{}
		_, paths["GET /peers"] = peersAs(t, f, c, "GET", nil)["s-none"]
		paths["GET /messages"] = strings.Contains(doAs(t, f.r, c, "GET", "/messages", nil).Body.String(), "msg_none")
		for _, e := range bus.Replay("", []string{"message.*", "session.*"}) {
			paths["event "+string(e.Type)] = scope.CanRead(scope.Scope(name), scope.Scope(e.Scope))
		}
		st := decodeState(t, doAs(t, sr, c, "GET", "/cmux/sidebar", nil).Body.String())
		paths["GET /cmux/sidebar"] = len(st.Workspaces) == 1 && st.Workspaces[0].WaitReason == "PR #9"
		if len(paths) != 5 {
			t.Fatalf("as %s: %d paths measured, want 5: %v", name, len(paths), paths)
		}
		for path, seen := range paths {
			if seen != want {
				t.Errorf("as %s: %s sees the unscoped session = %v, want %v (master only)", name, path, seen, want)
			}
		}
	}
}

// Zone already regressed once (cmuxsync printed the
// workspace title of another scope's subject to a non-master cell): the
// workspace of a theme filed under a perso area (space Allotment) is bound to its
// subject, and its title — the subject's short title — never reaches a work
// caller, whatever the area name is. The perso caller still reads it.
// the same holds for the perso area Pond.
func TestPeersHideBoundTitleOfPersoAreaTheme(t *testing.T) {
	t.Parallel()
	for _, sp := range []string{"Allotment", "Pond"} {
		t.Run(sp, func(t *testing.T) { peersHideBoundTitleOfPersoArea(t, sp) })
	}
}

func peersHideBoundTitleOfPersoArea(t *testing.T, area string) {
	f := newRelayFixture(t)
	tbl, err := space.Parse([]byte(spacetest.FormerYAML), "spacetest", scopetest.TwoTable(t))
	if err != nil {
		t.Fatal(err)
	}
	f.rl.svc.Spaces = tbl
	rec := doAs(t, f.r, perso, "POST", "/themes", pigtail.Theme{Name: area + " theme", Kind: "project", VaultLink: "Perso/Areas/" + strings.ToLower(area), Space: area})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create theme = %d %s", rec.Code, rec.Body.String())
	}
	var th pigtail.Theme
	_ = json.Unmarshal(rec.Body.Bytes(), &th)
	rec = doAs(t, f.r, perso, "POST", "/subjects", pigtail.Subject{ThemeID: th.ID, Title: "Tune the daemon"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subject = %d %s", rec.Code, rec.Body.String())
	}
	var sub pigtail.Subject
	_ = json.Unmarshal(rec.Body.Bytes(), &sub)
	const leak = "leak-system-subject-title"
	_ = f.st.CreateProject(&pigtail.Project{ID: "prj_sys", Name: "sys", RootPath: "/r", DefaultBackend: "git", PortRanges: map[string]pigtail.PortRange{}})
	if err := f.st.CreateWorkspace(&pigtail.Workspace{ID: "ws_sys", ProjectID: "prj_sys", State: pigtail.StateBuilding, Path: "/wt/system", BackendKind: "git"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_sys", SubjectID: sub.ID, Kind: pigtail.PhaseImplement,
		State: pigtail.PhaseActive, WorkspaceID: "ws_sys", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: "W-SYS", SubjectID: sub.ID, Source: "attach", BoundAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.peers(t, pigtail.PeerSession{SessionID: "s-sys", Name: "work-s", Scope: "gx", CellID: "mt",
		Cwd: "/wt/system", CmuxWorkspaceID: "W-SYS", CmuxTitle: leak})

	p := peersAs(t, f, work, "GET", nil)["s-sys"]
	if p.SessionID == "" {
		t.Fatal("the work peer must stay listed, only its subject's title is stripped")
	}
	if p.CmuxTitle != "" || p.CmuxBound || p.SubjectID != "" || p.SubjectShortTitle != "" || strings.Contains(p.Label, leak) {
		t.Errorf("a %s-area subject's workspace title leaks to a work caller: %+v", area, p)
	}
	if p := peersAs(t, f, perso, "GET", nil)["s-sys"]; p.CmuxTitle != leak {
		t.Errorf("perso caller must read the title: %+v", p)
	}
}

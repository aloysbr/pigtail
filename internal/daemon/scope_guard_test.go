package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Work/perso guard over the whole daemon surface. Two halves:
//
//  1. Classification, checked against the real router (chi.Walk over
//     buildRouter): every route must be listed in routeScopeClasses, and a
//     route classified scoped must actually sit behind scopeMW
//     (handlers.IsScopeMW on its middleware chain). A new route fails the
//     test until someone decides what it exposes; a stale entry fails too.
//  2. Probe: with perso data seeded through the API (a perso theme and
//     subject, a session working on it, a perso conversation that also feeds
//     the SSE ring), every GET route is called as a work caller — no header,
//     the fail-closed default — and no response may contain a perso subject
//     id, slug, title, theme or message. This catches a route classified
//     "no subject data" that in fact returns some, whatever its response type.
//  3. Session probe: every session-bound route is called as work naming a
//     perso session, and must answer like an unknown session with nothing
//     sent, claimed, acked or superseded (sessionBoundProbes; a new
//     session-bound route fails until it has a probe).
//
// Other POST/PATCH/DELETE routes are covered by the classification only:
// probing them would mutate state or reach the backend.

type routeScopeClass string

const (
	// Behind scopeMW; the handler filters with the caller's scope.
	classScoped routeScopeClass = "scoped"
	// GET /events: reads X-Pigtail-Scope itself and filters every event
	// (sse.go eventVisible; TestSSEFiltersMessageEventsByScope).
	classStream routeScopeClass = "stream-filtered"
	// GET /board: public HTML (no header in a browser), view picked by
	// ?scope=, work by default (board_scope_test.go).
	classPublicView routeScopeClass = "public-view"
	// The relay's session-addressed routes: the caller names a session id and
	// acts as it (send, claim, wait, ack). Behind scopeMW; the handler answers
	// as for an unknown session unless X-Pigtail-Scope equals the scope the
	// registry holds for that session (relay_scope.go actingAs) — see
	// docs/wire-protocol.md § Subject scope.
	classSessionBound routeScopeClass = "session-bound"
	// Returns nothing from the subject layer (workspaces, servers, admission,
	// …). The workspace layer is deliberately unscoped: worktree names and
	// paths are shared for port/server coordination.
	classNoSubject routeScopeClass = "no-subject-data"
)

var routeScopeClasses = map[string]routeScopeClass{
	// subject layer (RegisterSubjectRoutes)
	"GET /themes":                    classScoped,
	"GET /spaces":                    classScoped, // filtered by the caller's scope, like GET /themes
	"POST /themes":                   classScoped,
	"PATCH /themes/{id}":             classScoped,
	"DELETE /themes/{id}":            classScoped,
	"GET /subjects":                  classScoped,
	"POST /subjects":                 classScoped,
	"POST /subjects/suggest":         classScoped, // a read: candidates of a readable scope, the others only counted
	"POST /subjects/find":            classScoped, // a read that records the search: candidates of a readable scope, the others only counted
	"POST /subject-finds/{fid}/pick": classScoped, // writes the caller's own search: the find's scope authorised for a write
	"POST /subjects/ranks":           classScoped, // atomic rank changes, each id authorised for a write
	"GET /subjects/next":             classScoped,
	"GET /subjects/{id}":             classScoped,
	"POST /subjects/{id}":            classScoped,
	"GET /subjects/{id}/worktree":    classScoped,
	"GET /subjects/{id}/phases":      classScoped,
	"POST /subjects/{id}/phases":     classScoped,
	"POST /phases/{id}":              classScoped,
	"GET /subjects/{id}/log":         classScoped,
	"POST /subjects/{id}/log":        classScoped,
	// subject outputs (handlers/subject_outputs.go): scope derives from the subject
	"GET /subjects/{id}/outputs":        classScoped,
	"GET /subjects/{id}/outputs/groups": classScoped,
	"POST /subjects/{id}/outputs":       classScoped,
	"POST /subjects/{id}/outputs/purge": classScoped,
	// session launch (handlers/launch.go): own scope only, a launch's scope is its subject's
	"POST /subjects/{id}/launch": classScoped,
	"GET /launches/{id}":         classScoped,
	// SDK sessions (handlers/sdk_sessions.go): the subject's scope decides, one 404 body
	"POST /subjects/{id}/sdk-sessions":              classScoped,
	"GET /sdk-sessions":                             classScoped,
	"GET /sdk-sessions/{id}":                        classScoped,
	"GET /sdk-sessions/{id}/events":                 classScoped,
	"POST /sdk-sessions/{id}/requests/{rid}/answer": classScoped,
	"POST /sdk-sessions/{id}/messages":              classScoped,
	"POST /sdk-sessions/{id}/end":                   classScoped,
	// staffing scheduler (handlers/staffing.go): places, queues and decisions
	// name subjects; another scope's are masked for a non-master caller
	"GET /staffing":           classScoped,
	"POST /staffing":          classScoped,
	"GET /staffing/decisions": classScoped,
	// system journal (handlers/journal.go): rows carry their scope
	"GET /journal":  classScoped,
	"POST /journal": classScoped,
	// captures (handlers/captures.go): scope derives from the theme (A3)
	"POST /captures":         classScoped,
	"GET /captures":          classScoped,
	"POST /captures/promote": classScoped,
	"DELETE /captures/{id}":  classScoped,
	// session budget (handlers/session_budget.go): sample rows name a session's
	// repo, cwd and subject; another scope's rows are dropped for a non-master caller
	"GET /session-budget/samples": classScoped,
	// the gate's state is aggregate counts; a lease belongs to its caller's scope
	"GET /session-budget":                 classScoped,
	"POST /session-budget/leases":         classScoped,
	"POST /session-budget/leases/release": classScoped,
	// focus spaces (handlers/focus.go): a focus carries its scope (D6)
	"POST /focus":              classScoped,
	"GET /focus":               classScoped,
	"POST /focus/{id}/members": classScoped,
	"DELETE /focus/{id}/members/{kind}/{member}": classScoped,
	"POST /focus/{id}/end":                       classScoped,
	// subject ↔ cmux workspace link (subjects_cmux.go)
	"POST /subjects/{id}/cmux-workspaces":          classScoped,
	"GET /subjects/{id}/cmux-workspaces":           classScoped,
	"DELETE /subjects/{id}/cmux-workspaces/{wsid}": classScoped,
	"GET /cmux-workspaces/{wsid}":                  classScoped,
	// off-topic declaration (handlers/off_topic.go): state hides a link the caller may not read
	"GET /cmux-workspaces/{wsid}/state":             classScoped,
	"POST /cmux-workspaces/{wsid}/off-topic":        classScoped,
	"DELETE /cmux-workspaces/{wsid}/off-topic":      classScoped,
	"PATCH /cmux-workspaces/{wsid}/off-topic/space": classScoped, // A8: master only, space rewrite
	"GET /off-topic-workspaces":                     classScoped,
	// cmux sidebar loop (cmux_sidebar.go): the state names bound subjects
	// (null for a scope the caller may not read), retitle writes one.
	"POST /cmux-workspaces/{wsid}/retitle": classScoped,
	// behind scopeMW with the sidebar routes; writes no subject data,
	// returns nothing
	"POST /cmux-workspaces/{wsid}/trust-pending": classScoped,
	// same, for a respawn run's state (D16)
	// a final state with its run names a session and poses a wait and a
	// message on it (subject « abandon respawn visible »): session-bound.
	"POST /cmux-workspaces/{wsid}/respawn":   classSessionBound,
	"DELETE /cmux-workspaces/{wsid}/respawn": classScoped,
	"GET /cmux/sidebar":                      classScoped,
	"POST /cmux/sidebar":                     classScoped,
	// the D12 key: folds or unfolds pigtail's own cmux folders, names no subject
	"POST /cmux/folders/toggle": classScoped,
	"GET /board":                classPublicView,
	// web front (web.go): login primitives and the embedded front;
	// the front reads subjects through the cookie path of authMW (master scope
	// forced, GET allowlist), not through these routes.
	"POST /web/login-code": classNoSubject,
	"GET /web/login":       classNoSubject,
	"GET /":                classNoSubject,
	"GET /assets/*":        classNoSubject,

	// relay
	"GET /peers":               classScoped,
	"POST /peers":              classScoped,
	"GET /messages":            classScoped,
	"GET /messages/{id}":       classScoped,
	"POST /messages":           classSessionBound,
	"POST /messages/broadcast": classSessionBound,
	"POST /messages/claim":     classSessionBound,
	"GET /messages/wait":       classSessionBound,
	"POST /messages/{id}/ack":  classSessionBound,
	// typed waits (peers_wait.go): the caller records or clears what the
	// named session waits for — acting as it.
	"POST /peers/{session}/wait":   classSessionBound,
	"DELETE /peers/{session}/wait": classSessionBound,
	// Standby (peers_standby.go, sub_x000000000000041): the session sets or
	// ends its own Standby — acting as it.
	"PUT /peers/{session}/standby":    classSessionBound,
	"DELETE /peers/{session}/standby": classSessionBound,
	// turn ends by skill (session_handbacks.go, D11/D12): the hooks record
	// as the named session; the count is read over the caller's scopes.
	"POST /peers/{session}/skill": classSessionBound,
	"POST /peers/{session}/turn":  classSessionBound,
	"GET /session-handbacks":      classScoped,
	// « Aller à la session » (reveal.go, sub_x000000000000025): the user brings
	// a session's cmux tab to the front; a session the caller may not read
	// answers as an unknown one, and cmux is never called. Not acting as the
	// session: no sessionBoundProbes entry.
	"POST /peers/{session}/reveal": classScoped,
	// enriched waits (waits.go, sub_x000000000000026): the waits the caller
	// may read, one wait of another scope answering as an unknown id.
	"GET /waits":      classScoped,
	"GET /waits/{id}": classScoped,
	// the user answers a wait from his terminal (waits_answer.go): the write
	// is checked with scope.CanWrite; another scope's wait reads as unknown.
	"POST /waits/{id}/answer": classScoped,
	// the user snoozes a wait (waits.go): the same write rules as the answer.
	"POST /waits/{id}/snooze": classScoped,
	"GET /gates":              classScoped, // a gate carries a title and a subject id: subject-layer data (D19)
	"GET /cmux/check/notice":  classScoped, // names sessions of every scope: the master scope only (403 otherwise)
	"GET /usage/overview":     classScoped, // every account of every cell: the master scope only (403 otherwise)
	"GET /alerts":             classScoped, // an alert carries a subject id and title (D19)
	// roles.go (role supervision): a role is its subject's, in its subject's scope
	"GET /roles": classScoped, "GET /roles/status": classScoped,
	"POST /roles": classScoped, "DELETE /roles/{name}": classScoped,
	// alerts.go (subject « abandon respawn visible », D5): the ack acts as the
	// named session; a resolve closes the alerts of a subject the caller writes.
	"POST /alerts/{id}/ack": classSessionBound,
	"POST /alerts/resolve":  classScoped,
	"GET /events":           classStream,

	// no subject-layer data
	"GET /health":                       classNoSubject,
	"GET /version":                      classNoSubject,
	"GET /backup":                       classNoSubject,
	"POST /backup/offsite/run":          classNoSubject,
	"POST /dispatch":                    classNoSubject,
	"POST /workspaces":                  classNoSubject,
	"POST /workspaces/new":              classNoSubject,
	"GET /workspaces":                   classNoSubject,
	"GET /workspaces/{id}":              classNoSubject,
	"DELETE /workspaces/{id}":           classNoSubject,
	"POST /workspaces/gc":               classNoSubject,
	"POST /workspaces/{id}/rename":      classNoSubject,
	"PATCH /workspaces/{id}/ports":      classNoSubject,
	"POST /workspaces/{id}/run":         classNoSubject,
	"GET /workspaces/{id}/preflight":    classNoSubject,
	"POST /workspaces/{id}/hooks/run":   classNoSubject,
	"POST /projects":                    classNoSubject,
	"GET /projects":                     classNoSubject,
	"POST /agents/{id}/reply":           classNoSubject,
	"GET /static-servers":               classNoSubject,
	"POST /static-servers":              classNoSubject,
	"DELETE /static-servers/{name}":     classNoSubject,
	"POST /static-servers/{name}/start": classNoSubject,
	"POST /static-servers/{name}/stop":  classNoSubject,
	"POST /static-servers/stop-all":     classNoSubject,
	"GET /servers":                      classNoSubject,
	"POST /servers/{ws}/{server}/pin":   classNoSubject,
	"DELETE /servers/{ws}/{server}/pin": classNoSubject,
	"POST /servers/{ws}/{server}/stop":  classNoSubject,
	"POST /servers/stop-all":            classNoSubject,
	"POST /servers/stop-idle":           classNoSubject,
	"POST /servers/stopped":             classNoSubject,
	"GET /battery":                      classNoSubject,
	"POST /battery":                     classNoSubject,
	"GET /lid":                          classNoSubject,
	"POST /lid":                         classNoSubject,
	"POST /lid/probe":                   classNoSubject,
	"GET /purge":                        classNoSubject,
	"POST /purge":                       classNoSubject,
	"GET /cpu/slots":                    classNoSubject,
	"POST /cpu/slots":                   classNoSubject,
	"GET /cpu/slots/history":            classNoSubject,
	"GET /admission":                    classNoSubject,
	"GET /maintenance":                  classNoSubject,
	"GET /runtime/claim":                classNoSubject, // the lease file: ids, branch, sha — never label nor reason (D23)
	"POST /runtime/claim":               classNoSubject,
	"POST /runtime/release":             classNoSubject,
	"GET /admission/history":            classNoSubject,
	"POST /admission/leases":            classNoSubject,
	"POST /admission/leases/{id}/exit":  classNoSubject,
}

type walkedRoute struct {
	key    string
	method string
	path   string
	scoped bool
}

func walkRoutes(t *testing.T, r chi.Routes) []walkedRoute {
	t.Helper()
	var out []walkedRoute
	err := chi.Walk(r, func(method, path string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		wr := walkedRoute{key: method + " " + path, method: method, path: path}
		for _, mw := range mws {
			if handlers.IsScopeMW(mw) {
				wr.scoped = true
			}
		}
		out = append(out, wr)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func TestRoutesExposingSubjectsAreScoped(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	routes := walkRoutes(t, srv.buildRouter())
	if len(routes) < 20 {
		t.Fatalf("walked only %d routes — the guard is not looking at the real router", len(routes))
	}
	seen := map[string]bool{}
	for _, r := range routes {
		seen[r.key] = true
		class, ok := routeScopeClasses[r.key]
		switch {
		case !ok:
			t.Errorf("%s is not classified in routeScopeClasses: decide whether it returns subject-layer data "+
				"(subject/theme ids, slugs, titles, logs, peers' subjects, messages) and, if so, put it behind scopeMW", r.key)
		case (class == classScoped || class == classSessionBound) && !r.scoped:
			t.Errorf("%s is classified %s but is not behind scopeMW", r.key, class)
		case class != classScoped && class != classSessionBound && r.scoped:
			t.Errorf("%s sits behind scopeMW but is classified %q — reclassify it scoped", r.key, class)
		}
	}
	for key := range routeScopeClasses {
		if !seen[key] {
			t.Errorf("routeScopeClasses lists %s, which the router no longer serves", key)
		}
	}
}

// Perso markers the probe must never find in a work caller's response.
const (
	probeThemeName    = "Zqx perso theme"
	probeSubjectTitle = "Zqx perso secret subject"
	probeBody         = "zqx perso message body"
	probeFocusName    = "Zqx perso focus"
	// A cmux workspace bound to the perso subject. The UUID itself is not a
	// marker: cmux workspaces are the (unscoped) layer every cell shares.
	probeCmuxWS = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"
)

func TestWorkCallerNeverReadsPersoThroughAnyGET(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.buildRouter()
	call := func(method, path, scopeHeader string, body any, timeout time.Duration) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req := httptest.NewRequest(method, path, &buf).WithContext(ctx)
		req.RemoteAddr = "" // the unix socket: no token needed
		if scopeHeader != "" {
			req.Header.Set(pigtail.HeaderScope, scopeHeader)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder, into any) {
		t.Helper()
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || json.Unmarshal(env.Data, into) != nil {
			t.Fatalf("decode %d %s", rec.Code, rec.Body.String())
		}
	}

	// Seed perso through the API, as a perso caller.
	var th pigtail.Theme
	decode(call("POST", "/themes", "perso", pigtail.Theme{Name: probeThemeName, Kind: "area", VaultLink: "Perso/Areas/zqx"}, time.Second), &th)
	var sub pigtail.Subject
	decode(call("POST", "/subjects", "perso", pigtail.Subject{ThemeID: th.ID, Title: probeSubjectTitle}, time.Second), &sub)
	if err := st.CreateProject(&pigtail.Project{ID: "prj_neutral", Name: "neutral", RootPath: "/r", DefaultBackend: "git", PortRanges: map[string]pigtail.PortRange{}}); err != nil {
		t.Fatal(err)
	}
	// The worktree has a neutral name: worktree names are the workspace
	// layer, unscoped on purpose.
	if err := st.CreateWorkspace(&pigtail.Workspace{ID: "ws_neutral", ProjectID: "prj_neutral", State: pigtail.StateBuilding, Path: "/wt/neutral", BackendKind: "git"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePhase(&pigtail.SubjectPhase{ID: "phs_zqx", SubjectID: sub.ID, Kind: pigtail.PhaseImplement,
		State: pigtail.PhaseActive, WorkspaceID: "ws_neutral", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if rec := call("POST", "/subjects/"+sub.ID+"/log", "perso", pigtail.SubjectLogEntry{Kind: "note", Text: "zqx log line"}, time.Second); rec.Code >= 300 {
		t.Fatalf("log = %d %s", rec.Code, rec.Body.String())
	}
	for _, e := range []pigtail.JournalEntry{
		{ID: "zqx-j1", Kind: "note", SubjectID: sub.ID, Summary: "zqx journal line", Source: "vault:Perso/Areas/zqx/index.md"},
		{ID: "zqx-j2", Kind: "meeting", Summary: "zqx journal free line", Source: "vault:Perso/Areas/zqx/free.md"},
	} {
		if rec := call("POST", "/journal", "perso", e, time.Second); rec.Code != http.StatusCreated {
			t.Fatalf("seed journal = %d %s", rec.Code, rec.Body.String())
		}
	}
	// A perso output on the perso subject: GET /subjects/{id}/outputs must not
	// show it to a work caller.
	if rec := call("POST", "/subjects/"+sub.ID+"/outputs", "perso", pigtail.SubjectOutput{Kind: "capture", Path: "/zqx/zqx-output.png", Title: "zqx output title"}, time.Second); rec.Code != http.StatusCreated {
		t.Fatalf("seed output = %d %s", rec.Code, rec.Body.String())
	}
	// A perso role on the perso subject and its alert row: GET /roles, /roles/status
	// and /alerts must not show them to a work caller.
	if rec := call("POST", "/roles", "perso", pigtail.RoleRequest{Name: "zqx-role", SubjectID: sub.ID, CadenceMS: 1800000}, time.Second); rec.Code != http.StatusOK {
		t.Fatalf("seed role = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "role-silent:zqx-role:1793359800000", Timestamp: time.Now().UTC(),
		Kind: "role-alert", Scope: "perso", SubjectID: sub.ID,
		Summary: "zqx role detail", Source: "pigtail:roles/zqx-role?state=silent"}); err != nil {
		t.Fatal(err)
	}
	// A perso respawn alert on the perso subject: GET /alerts must not show it,
	// nor its run, detail or the subject's title, to a work caller.
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:zqx-run:abandoned", Timestamp: time.Now().UTC(),
		Kind: "respawn-abandoned", Scope: "perso", SubjectID: sub.ID, // the test file names scopes literally, as its headers do
		Summary: "respawn abandoned zqx-run: zqx respawn detail", Source: "pigtail:respawn/zqx-run"}); err != nil {
		t.Fatal(err)
	}
	call("POST", "/peers", "perso", pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: "s-work-on-perso", Cwd: "/wt/neutral", Name: "tp", Scope: "gx", CellID: "mt"},
		{SessionID: "s-perso-a", Cwd: "/wt/neutral", Name: "pa", Scope: "perso", CellID: "mp"},
		{SessionID: "s-perso-b", Name: "pb", Scope: "perso", CellID: "mp"},
		// A work session in a cmux workspace bound to the perso subject:
		// its subject comes from the binding alone.
		{SessionID: "s-work-bound", Name: "tb", Scope: "gx", CellID: "mt", CmuxWorkspaceID: probeCmuxWS},
	}}, time.Second)
	if rec := call("POST", "/subjects/"+sub.ID+"/cmux-workspaces", "perso", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: probeCmuxWS}, time.Second); rec.Code != http.StatusCreated {
		t.Fatalf("seed binding = %d %s", rec.Code, rec.Body.String())
	}
	var sent pigtail.SendMessageResponse
	rec := call("POST", "/messages", "perso", pigtail.SendMessageRequest{From: "s-perso-b", To: "pa", Body: probeBody}, time.Second)
	decode(rec, &sent)
	if sent.Message.SubjectID != sub.ID {
		t.Fatalf("seed: the perso message should carry the perso subject: %+v", sent.Message)
	}

	// A cross-scope notice from the master to a work session working on the
	// perso subject (D1): the work caller reads it, and still no perso marker
	// (sender id, the perso subject in the recipient's label) — D23.
	{
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(pigtail.SendMessageRequest{From: "s-perso-b", To: "tp", Kind: pigtail.MessageKindNotice, Body: "zzz service notice"})
		req := httptest.NewRequest("POST", "/messages", &buf)
		req.RemoteAddr = ""
		req.Header.Set(pigtail.HeaderScope, "perso")
		req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed notice = %d %s", rec.Code, rec.Body.String())
		}
		if body := call("GET", "/messages", "", nil, time.Second).Body.String(); !strings.Contains(body, "zzz service notice") {
			t.Fatalf("positive control: the work caller must see its notice: %s", body)
		}
	}

	// A perso turn handed back under a perso skill (GET /session-handbacks).
	if rec := call("POST", "/peers/s-perso-a/skill", "perso", pigtail.SessionSkillRequest{Skill: "zqx-perso-skill"}, time.Second); rec.Code != http.StatusNoContent {
		t.Fatalf("seed skill = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/peers/s-perso-a/turn", "perso", pigtail.SessionTurnRequest{TurnID: "zqx-turn", TurnStart: time.Now().Add(-time.Minute), Event: "stop"}, time.Second); rec.Code != http.StatusNoContent {
		t.Fatalf("seed turn = %d %s", rec.Code, rec.Body.String())
	}
	if body := call("GET", "/session-handbacks", "perso", nil, time.Second).Body.String(); !strings.Contains(body, "zqx-perso-skill") {
		t.Fatalf("handbacks seed not visible to perso: %s", body)
	}

	// A perso focus (focus D15/D23): its id and name must never reach a
	// work caller, GET /focus included.
	var focus pigtail.Focus
	decode(call("POST", "/focus", "perso", map[string]any{"name": probeFocusName,
		"members": []pigtail.FocusMember{{Kind: pigtail.FocusMemberTheme, ID: th.ID}}}, time.Second), &focus)
	if body := call("GET", "/focus", "perso", nil, time.Second).Body.String(); !strings.Contains(body, focus.ID) || !strings.Contains(body, probeFocusName) {
		t.Fatalf("focus seed not visible to perso: %s", body)
	}

	// A perso wait with a typed content (GET /waits, GET /waits/{id}); and a
	// work session working on the perso subject declares one too: work lists
	// it, never with the perso subject's id or title (D23).
	if rec := call("POST", "/peers/s-perso-b/wait", "perso", pigtail.SessionWaitRequest{Type: "blocked",
		Details: json.RawMessage(`{"summary":"zqx perso wait summary"}`)}, time.Second); rec.Code != http.StatusOK {
		t.Fatalf("seed wait = %d %s", rec.Code, rec.Body.String())
	}
	persoWaits, err := st.OpenWaitRecordIDs("s-perso-b")
	if err != nil || len(persoWaits) != 1 {
		t.Fatalf("seed wait record = %v, %v", persoWaits, err)
	}
	if rec := call("POST", "/peers/s-work-on-perso/wait", "gx", pigtail.SessionWaitRequest{Type: "review", Reason: "work wait"}, time.Second); rec.Code != http.StatusOK {
		t.Fatalf("seed work wait = %d %s", rec.Code, rec.Body.String())
	}
	if body := call("GET", "/waits", "perso", nil, time.Second).Body.String(); !strings.Contains(body, "zqx perso wait summary") {
		t.Fatalf("wait seed not visible to perso: %s", body)
	}
	if body := call("GET", "/waits", "", nil, time.Second).Body.String(); !strings.Contains(body, "work wait") {
		t.Fatalf("positive control: the work caller must list its own wait: %s", body)
	}
	// the user (the master, --cross-scope) answers that work wait: the answer's
	// journal pointer must not carry the perso subject into work's GET /journal.
	workWaits, err := st.OpenWaitRecordIDs("s-work-on-perso")
	if err != nil || len(workWaits) != 1 {
		t.Fatalf("seed work wait record = %v, %v", workWaits, err)
	}
	{
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(pigtail.WaitAnswerRequest{Seen: true})
		req := httptest.NewRequest("POST", "/waits/"+workWaits[0]+"/answer", &buf)
		req.RemoteAddr = ""
		req.Header.Set(pigtail.HeaderScope, "perso")
		req.Header.Set(pigtail.HeaderCrossScope, pigtail.CrossScopeWrite)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("seed answer = %d %s", rec.Code, rec.Body.String())
		}
		if body := call("GET", "/journal", "", nil, time.Second).Body.String(); !strings.Contains(body, "wait-answer-"+workWaits[0]) {
			t.Fatalf("positive control: the work caller must read its wait's answer pointer: %s", body)
		}
	}

	// Perso session ids are markers too: knowing one is what the
	// session-bound routes would need to act as it.
	markers := []string{sub.ID, sub.Slug, probeSubjectTitle, th.ID, probeThemeName, probeBody, sent.Message.ID, "zqx log line",
		"s-perso-a", "s-perso-b", "zqx journal line", "zqx journal free line", "zqx-j1", "zqx-j2", "zqx-perso-skill",
		"zqx-run", "zqx respawn detail", focus.ID, probeFocusName, "zqx perso wait summary", persoWaits[0],
		"zqx-output.png", "zqx output title", "zqx-role", "zqx role detail"}
	// Sanity: a perso caller does see the markers, so the probe is looking
	// at data that exists.
	if body := call("GET", "/peers", "perso", nil, time.Second).Body.String(); !strings.Contains(body, sub.ID) {
		t.Fatalf("seed not visible to perso: %s", body)
	}
	if body := call("GET", "/journal", "perso", nil, time.Second).Body.String(); !strings.Contains(body, "zqx journal free line") {
		t.Fatalf("journal seed not visible to perso: %s", body)
	}
	if body := call("GET", "/alerts", "perso", nil, time.Second).Body.String(); !strings.Contains(body, "zqx respawn detail") || !strings.Contains(body, sub.ID) {
		t.Fatalf("alert seed not visible to perso: %s", body)
	}
	for _, p := range []string{"/roles", "/roles/status", "/alerts"} {
		if body := call("GET", p, "perso", nil, time.Second).Body.String(); !strings.Contains(body, "zqx-role") {
			t.Fatalf("role seed not visible to perso on %s: %s", p, body)
		}
	}

	params := map[string][]string{
		"id":     {sub.ID, th.ID, sent.Message.ID, "phs_zqx", "ws_neutral", focus.ID, persoWaits[0]},
		"ws":     {"ws_neutral"},
		"server": {"dev"},
		"name":   {"x", "zqx-role"},
		"wsid":   {probeCmuxWS},
	}
	for _, r := range walkRoutes(t, router) {
		if r.method != http.MethodGet {
			continue
		}
		if routeScopeClasses[r.key] == classSessionBound {
			continue // acts as a named session: TestWorkCallerNeverActsAsAPersoSession
		}
		timeout := 2 * time.Second
		if r.path == "/events" {
			timeout = 200 * time.Millisecond // a stream: read what the replay sends
		}
		for _, path := range expandParams(r.path, params) {
			body := call("GET", path, "", nil, timeout).Body.String()
			for _, m := range markers {
				// An error echoing the id the caller itself sent is no leak.
				if strings.Contains(path, m) {
					continue
				}
				if strings.Contains(body, m) {
					t.Errorf("GET %s as work leaks perso %q:\n%s", path, m, body)
				}
			}
		}
	}
}

// expandParams fills every {param} of a chi pattern with each candidate.
func expandParams(pattern string, params map[string][]string) []string {
	start := strings.Index(pattern, "{")
	if start < 0 {
		return []string{pattern}
	}
	end := strings.Index(pattern[start:], "}") + start
	name := pattern[start+1 : end]
	cands := params[name]
	if len(cands) == 0 {
		cands = []string{"x"}
	}
	var out []string
	for _, c := range cands {
		out = append(out, expandParams(pattern[:start]+c+pattern[end+1:], params)...)
	}
	return out
}

// sessionBoundProbe calls one session-bound route as a work caller (no
// header) naming a perso session. unknown is the same call naming a session
// or message nobody registered: the answers must be the same, byte for byte
// once the id is swapped. Each route of classSessionBound must have one.
type sessionBoundProbe struct {
	method, path, unknownPath string
	body, unknownBody         any
	swap                      [2]string // perso id in the answer → unknown id
	// success: the route answers a 2xx report for an unknown session (the
	// respawn route: "skipped: …"), not an error — the data parts compare.
	success bool
}

func TestWorkCallerNeverActsAsAPersoSession(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := srv.buildRouter()
	call := func(method, path, scopeHeader string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.RemoteAddr = ""
		if scopeHeader != "" {
			req.Header.Set(pigtail.HeaderScope, scopeHeader)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("POST", "/peers", "perso", pigtail.PeersRequest{Peers: []pigtail.PeerSession{
		{SessionID: "s-perso-a", Name: "pa", Scope: "perso", CellID: "mp"},
		{SessionID: "s-perso-b", Name: "pb", Scope: "perso", CellID: "mp"},
	}}); rec.Code != http.StatusOK {
		t.Fatalf("seed peers = %d %s", rec.Code, rec.Body.String())
	}
	rec := call("POST", "/messages", "perso", pigtail.SendMessageRequest{From: "s-perso-b", To: "pa", Body: probeBody})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed message = %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data pigtail.SendMessageResponse `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	msgID := env.Data.Message.ID

	// A respawn alert a work caller reads: without it, both ack calls would
	// answer the same 404 of an unknown run and the probe would prove nothing.
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "respawn:r-probe:abandoned", Timestamp: time.Now().UTC(),
		Kind: "respawn-abandoned", Scope: "globex", Summary: "respawn abandoned r-probe: d", Source: "pigtail:respawn/r-probe"}); err != nil {
		t.Fatal(err)
	}
	probes := map[string]sessionBoundProbe{
		"POST /messages": {
			method: "POST", path: "/messages", unknownPath: "/messages",
			body:        pigtail.SendMessageRequest{From: "s-perso-a", To: "pb", Body: "work speaking for perso"},
			unknownBody: pigtail.SendMessageRequest{From: "s-unknown", To: "pb", Body: "work speaking for perso"},
			swap:        [2]string{"s-perso-a", "s-unknown"},
		},
		"POST /messages/broadcast": {
			method: "POST", path: "/messages/broadcast", unknownPath: "/messages/broadcast",
			body:        pigtail.BroadcastRequest{From: "s-perso-a", Body: "work speaking for perso"},
			unknownBody: pigtail.BroadcastRequest{From: "s-unknown", Body: "work speaking for perso"},
			swap:        [2]string{"s-perso-a", "s-unknown"},
		},
		"POST /messages/claim": {
			method: "POST", path: "/messages/claim", unknownPath: "/messages/claim",
			body: pigtail.ClaimRequest{Session: "s-perso-a"}, unknownBody: pigtail.ClaimRequest{Session: "s-unknown"},
			swap: [2]string{"s-perso-a", "s-unknown"},
		},
		"GET /messages/wait": {
			method: "GET", path: "/messages/wait?session=s-perso-a&timeout=1", unknownPath: "/messages/wait?session=s-unknown&timeout=1",
			swap: [2]string{"s-perso-a", "s-unknown"},
		},
		"POST /messages/{id}/ack": {
			method: "POST", path: "/messages/" + msgID + "/ack", unknownPath: "/messages/msg_unknown/ack",
			body: pigtail.AckRequest{Session: "s-perso-a"}, unknownBody: pigtail.AckRequest{Session: "s-perso-a"},
			swap: [2]string{msgID, "msg_unknown"},
		},
		"POST /peers/{session}/wait": {
			method: "POST", path: "/peers/s-perso-a/wait", unknownPath: "/peers/s-unknown/wait",
			body:        pigtail.SessionWaitRequest{Type: "review", Reason: "work speaking for perso"},
			unknownBody: pigtail.SessionWaitRequest{Type: "review", Reason: "work speaking for perso"},
			swap:        [2]string{"s-perso-a", "s-unknown"},
		},
		"DELETE /peers/{session}/wait": {
			method: "DELETE", path: "/peers/s-perso-a/wait", unknownPath: "/peers/s-unknown/wait",
			swap: [2]string{"s-perso-a", "s-unknown"},
		},
		"PUT /peers/{session}/standby": {
			method: "PUT", path: "/peers/s-perso-a/standby", unknownPath: "/peers/s-unknown/standby",
			swap: [2]string{"s-perso-a", "s-unknown"},
		},
		"DELETE /peers/{session}/standby": {
			method: "DELETE", path: "/peers/s-perso-a/standby", unknownPath: "/peers/s-unknown/standby",
			swap: [2]string{"s-perso-a", "s-unknown"},
		},
		"POST /peers/{session}/skill": {
			method: "POST", path: "/peers/s-perso-a/skill", unknownPath: "/peers/s-unknown/skill",
			body:        pigtail.SessionSkillRequest{Skill: "work-skill"},
			unknownBody: pigtail.SessionSkillRequest{Skill: "work-skill"},
			swap:        [2]string{"s-perso-a", "s-unknown"},
		},
		"POST /cmux-workspaces/{wsid}/respawn": {
			method: "POST", path: "/cmux-workspaces/" + probeCmuxWS + "/respawn", unknownPath: "/cmux-workspaces/" + probeCmuxWS + "/respawn",
			body:        pigtail.CmuxRespawnRequest{State: "failed", RunID: "r-probe-perso", SessionID: "s-perso-a", Detail: "work speaking for perso"},
			unknownBody: pigtail.CmuxRespawnRequest{State: "failed", RunID: "r-probe-unknown", SessionID: "s-unknown", Detail: "work speaking for perso"},
			swap:        [2]string{"s-perso-", "s-unknow"}, // the report names a session by its first 8 bytes (wakeShort)
			success:     true,
		},
		"POST /alerts/{id}/ack": {
			method: "POST", path: "/alerts/r-probe/ack", unknownPath: "/alerts/r-probe/ack",
			body: pigtail.AlertAckRequest{SessionID: "s-perso-a"}, unknownBody: pigtail.AlertAckRequest{SessionID: "s-unknown"},
			swap: [2]string{"s-perso-a", "s-unknown"},
		},
		"POST /peers/{session}/turn": {
			method: "POST", path: "/peers/s-perso-a/turn", unknownPath: "/peers/s-unknown/turn",
			body:        pigtail.SessionTurnRequest{TurnID: "work-turn", TurnStart: time.Now(), Event: "stop"},
			unknownBody: pigtail.SessionTurnRequest{TurnID: "work-turn", TurnStart: time.Now(), Event: "stop"},
			swap:        [2]string{"s-perso-a", "s-unknown"},
		},
	}
	if rec := call("POST", "/peers/s-perso-a/wait", "perso", pigtail.SessionWaitRequest{Type: "arbitrage", Reason: "perso decision"}); rec.Code != http.StatusOK {
		t.Fatalf("seed wait = %d %s", rec.Code, rec.Body.String())
	}
	covered := 0
	for _, r := range walkRoutes(t, router) {
		if routeScopeClasses[r.key] != classSessionBound {
			continue
		}
		p, ok := probes[r.key]
		if !ok {
			t.Errorf("%s is session-bound but has no probe in TestWorkCallerNeverActsAsAPersoSession", r.key)
			continue
		}
		covered++
		for _, hdr := range []string{"", "gx"} {
			got := call(p.method, p.path, hdr, p.body)
			want := call(p.method, p.unknownPath, hdr, p.unknownBody)
			if p.success {
				// A report is due: two empty answers (204) would compare equal
				// and prove nothing.
				if got.Code >= 300 || got.Code != want.Code || strings.HasPrefix(dataPart(want), "not a data envelope") ||
					strings.ReplaceAll(dataPart(got), p.swap[0], p.swap[1]) != dataPart(want) {
					t.Errorf("%s as work(%q) naming a perso session = %d %s, want the unknown answer %d %s",
						r.key, hdr, got.Code, got.Body.String(), want.Code, want.Body.String())
				}
				continue
			}
			// The error object alone: meta.request_id differs per request.
			if got.Code < 400 || got.Code != want.Code ||
				strings.ReplaceAll(errorPart(got), p.swap[0], p.swap[1]) != errorPart(want) {
				t.Errorf("%s as work(%q) naming a perso session = %d %s, want the unknown answer %d %s",
					r.key, hdr, got.Code, got.Body.String(), want.Code, want.Body.String())
			}
		}
	}
	if covered != len(probes) {
		t.Errorf("probed %d session-bound routes, %d probes defined: a probe names a route that is gone or reclassified", covered, len(probes))
	}

	// Nothing happened to the perso conversation.
	m, err := st.GetMessage(msgID)
	if err != nil || m.State != pigtail.MessageStatePending {
		t.Fatalf("perso message touched: %+v %v", m, err)
	}
	if n, _ := st.CountThread(m.ThreadID); n != 1 {
		t.Fatalf("perso thread holds %d messages, want 1", n)
	}
	// Nor to the perso session's wait.
	if ws, _ := st.SessionWaits("s-perso-a"); len(ws) != 1 || ws[0].Type != "arbitrage" || ws[0].Reason != "perso decision" {
		t.Fatalf("perso wait touched: %+v", ws)
	}
	// Nor did the respawn route tell it anything: no respawn wait (checked
	// above: the seeded one is alone), no message from pigtail, and the
	// journal line names nobody told.
	ms, err := st.ListMessages(pigtail.MessageFilter{To: "s-perso-a"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.FromSession == pigtail.SystemSender {
			t.Fatalf("pigtail told the perso session: %+v", m)
		}
	}
	js, err := st.ListJournal(store.JournalFilter{Kind: "respawn-failed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		if j.ID == "respawn:r-probe-perso:failed" && strings.Contains(j.Source, "?session=") {
			t.Fatalf("the respawn line names a session told: %+v", j)
		}
	}
	// Nor did a work caller close the alert as the perso session.
	if rs, err := st.ListJournal(store.JournalFilter{Kind: "alert-resolved", IDs: []string{"alert-resolved:r-probe"}}); err != nil || len(rs) != 0 {
		t.Fatalf("a work caller acked an alert as a perso session: %+v %v", rs, err)
	}
	// Control: the perso session itself still claims and acks it.
	rec = call("POST", "/messages/claim", "perso", pigtail.ClaimRequest{Session: "s-perso-a"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), msgID) {
		t.Fatalf("perso claim = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/messages/"+msgID+"/ack", "perso", pigtail.AckRequest{Session: "s-perso-a"}); rec.Code != http.StatusOK {
		t.Fatalf("perso ack = %d %s", rec.Code, rec.Body.String())
	}
}

// errorPart is the "error" object of an error envelope, as raw JSON.
func errorPart(rec *httptest.ResponseRecorder) string {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Error) == 0 {
		return "not an error envelope: " + rec.Body.String()
	}
	return string(env.Error)
}

// dataPart is the "data" object of a success envelope, as raw JSON.
func dataPart(rec *httptest.ResponseRecorder) string {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Data) == 0 {
		return "not a data envelope: " + rec.Body.String()
	}
	return string(env.Data)
}

func TestFrozenRoutesSitBehindFreezeMW(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	want := map[string]bool{
		"POST /themes": true, "PATCH /themes/{id}": true, "DELETE /themes/{id}": true,
		// role supervision: the writes are held by a freeze, the reads are not
		"POST /roles": true, "DELETE /roles/{name}": true, "GET /roles": false, "GET /roles/status": false,
		"POST /subjects": true, "POST /subjects/{id}": true, "POST /subjects/ranks": true, "POST /subjects/{id}/phases": true,
		"POST /phases/{id}": true, "POST /subjects/{id}/log": true, "POST /journal": true, "POST /workspaces/new": true,
		// subject outputs: writes held by a freeze, the read is not
		"POST /subjects/{id}/outputs": true, "POST /subjects/{id}/outputs/purge": true, "GET /subjects/{id}/outputs": false,
		"POST /dispatch": true, // D10: `pigtail dispatch`, the other Backend.Create path
		// session launch: the launch is held by a freeze, its read is not
		"POST /subjects/{id}/launch": true, "GET /launches/{id}": false,
		// SDK sessions: the launch and the three writes are held by a freeze, the reads are not
		"POST /subjects/{id}/sdk-sessions": true, "POST /sdk-sessions/{id}/requests/{rid}/answer": true,
		"POST /sdk-sessions/{id}/messages": true, "POST /sdk-sessions/{id}/end": true,
		"GET /sdk-sessions": false, "GET /sdk-sessions/{id}": false, "GET /sdk-sessions/{id}/events": false,
		// staffing: the settings write is held by a freeze, the reads are not
		"POST /staffing": true, "GET /staffing": false, "GET /staffing/decisions": false,
		// captures: writes held by a freeze, the read is not
		"POST /captures": true, "POST /captures/promote": true, "DELETE /captures/{id}": true, "GET /captures": false,
		// focus spaces: every write is held by a freeze (focus D26)
		"POST /focus": true, "POST /focus/{id}/members": true, "DELETE /focus/{id}/members/{kind}/{member}": true, "POST /focus/{id}/end": true,
		// Out of the freeze, decided (D10): `pigtail run` restarts an agent in an existing workspace.
		"POST /workspaces/{id}/run": false,
		// Out of the freeze, decided: the session<->subject link. Listed so that
		// putting them behind FreezeMW breaks this test like a forgotten route above.
		"POST /subjects/{id}/cmux-workspaces":           false,
		"DELETE /subjects/{id}/cmux-workspaces/{wsid}":  false,
		"POST /cmux-workspaces/{wsid}/off-topic":        false,
		"DELETE /cmux-workspaces/{wsid}/off-topic":      false,
		"PATCH /cmux-workspaces/{wsid}/off-topic/space": false, // A8: out of the freeze like declare/lift
		// Out of the freeze, decided: an acknowledgement is never held by a maintenance.
		"POST /alerts/{id}/ack": false,
		"POST /alerts/resolve":  false,
		// Out of the freeze, decided: a human answer never waits for a maintenance.
		"POST /waits/{id}/answer": false,
		// A snooze is the user's too, and touches no session: no maintenance gate.
		"POST /waits/{id}/snooze": false,
		// Out of the freeze, decided: a read (subject suggest).
		"POST /subjects/suggest": false,
		// Out of the freeze, decided (T1 of sub_x000000000000031): a search, like suggest.
		"POST /subjects/find": false,
		// Out of the freeze, decided: the choice of a search, like the link.
		"POST /subject-finds/{fid}/pick": false,
	}
	for _, k := range []string{"POST /subjects/{id}/cmux-workspaces", "DELETE /subjects/{id}/cmux-workspaces/{wsid}", "POST /workspaces/{id}/run",
		"POST /cmux-workspaces/{wsid}/off-topic", "DELETE /cmux-workspaces/{wsid}/off-topic", "PATCH /cmux-workspaces/{wsid}/off-topic/space"} {
		if _, ok := want[k]; !ok {
			t.Fatalf("%s must stay listed as out of the freeze", k)
		}
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	_ = chi.Walk(srv.buildRouter(), func(method, path string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		frozen := false
		for _, mw := range mws {
			if handlers.IsFreezeMW(mw) {
				frozen = true
			}
		}
		key := method + " " + path
		seen[key] = true
		if frozen != want[key] {
			t.Errorf("%s: behind FreezeMW = %v, want %v", key, frozen, want[key])
		}
		return nil
	})
	for k := range want {
		if !seen[k] {
			t.Errorf("%s: route not found in the router (renamed?) — update this list", k)
		}
	}
}

// TestServerWiresMaintenance breaks if server.go stops building the Watch,
// stops giving it to the handlers, or cmuxsync_wiring.go stops giving it to
// the sidebar loop: a frozen state on the daemon's own dir must refuse a
// write through the REAL router, and the loop's deps must read it.
func TestServerWiresMaintenance(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "") // the daemon dir must come from the socket, whatever verify exports
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.cfg.SocketPath)
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_w", On: true, Freeze: true, Reason: "wiring", Since: time.Now()})
	r := s.buildRouter()
	q := httptest.NewRequest(http.MethodPost, "/journal", strings.NewReader(`{}`))
	q.Header.Set(pigtail.HeaderScope, "perso")
	q.Header.Set(pigtail.HeaderSession, "other")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, q)
	if rec.Code != http.StatusLocked {
		t.Fatalf("real router did not freeze (wiring lost in server.go?): %d %s", rec.Code, rec.Body)
	}
}

// TestCaptureRoutesClassified: the four capture routes are classified as
// scoped (A3), so TestRoutesExposingSubjectsAreScoped covers them.
func TestCaptureRoutesClassified(t *testing.T) {
	for _, k := range []string{"POST /captures", "GET /captures", "POST /captures/promote", "DELETE /captures/{id}"} {
		if routeScopeClasses[k] != classScoped {
			t.Errorf("%s must be classified classScoped in routeScopeClasses, got %q", k, routeScopeClasses[k])
		}
	}
}

// Every /session-budget route is scoped (behind scopeMW), unlike
// POST /admission/leases: the samples name sessions, and a lease belongs to
// its caller's scope.
func TestScopeGuardClassifiesSessionBudgetRoutes(t *testing.T) {
	for _, k := range []string{"GET /session-budget", "GET /session-budget/samples", "POST /session-budget/leases", "POST /session-budget/leases/release"} {
		if routeScopeClasses[k] != classScoped {
			t.Errorf("%s must be classified classScoped in routeScopeClasses, got %q", k, routeScopeClasses[k])
		}
	}
}

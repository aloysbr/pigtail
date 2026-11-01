package handlers

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeSidebarCtl records what the handlers ask of the loop.
type fakeSidebarCtl struct {
	enabled  []bool
	nudges   int
	released []pigtail.SubjectCmuxBinding
	trust    []string
	respawn  []string // "mark <ws>|<state>|<wait>|<surface>|<pid>" or "clear <ws>"
	state    pigtail.CmuxSidebarState
	states   int // State() calls
}

func (f *fakeSidebarCtl) State() pigtail.CmuxSidebarState {
	f.states++
	st := f.state
	st.Workspaces = nil
	for _, w := range f.state.Workspaces {
		c := w
		if w.Subject != nil {
			s := *w.Subject
			c.Subject = &s
		}
		st.Workspaces = append(st.Workspaces, c)
	}
	return st
}
func (f *fakeSidebarCtl) SetEnabled(on bool)                    { f.enabled = append(f.enabled, on); f.state.Enabled = on }
func (f *fakeSidebarCtl) Nudge()                                { f.nudges++ }
func (f *fakeSidebarCtl) Released(b pigtail.SubjectCmuxBinding) { f.released = append(f.released, b) }
func (f *fakeSidebarCtl) TrustPending(ws string, pid int) {
	f.trust = append(f.trust, ws+"|"+strconv.Itoa(pid))
}
func (f *fakeSidebarCtl) RespawnMark(ws string, req pigtail.CmuxRespawnRequest) {
	f.respawn = append(f.respawn, "mark "+strings.Join([]string{ws, req.State, req.Wait, req.SurfaceID, strconv.Itoa(req.PID)}, "|"))
}
func (f *fakeSidebarCtl) RespawnClear(ws string) { f.respawn = append(f.respawn, "clear "+ws) }

// sidebarFixture is scopedFixture with a sidebar loop stand-in wired in.
func sidebarFixture(t *testing.T) (*chi.Mux, *store.Store, *fakeSidebarCtl) {
	t.Helper()
	_, st := scopedFixture(t)
	ctl := &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Enabled: true, CmuxAvailable: true, Warnings: []string{}, Workspaces: []pigtail.CmuxSidebarWorkspace{
		{CmuxWorkspaceID: cmuxWS, Attention: "waiting", WaitingCount: 1, Title: "perso subject", TitleOwner: "pigtail",
			Subject: &pigtail.CmuxSidebarSubject{ID: "sub_p", ShortTitle: "perso subject", Scope: "perso"}},
		{CmuxWorkspaceID: cmuxWS2, Attention: "dead", Title: "fait", TitleOwner: "pigtail",
			Subject: &pigtail.CmuxSidebarSubject{ID: "sub_t", ShortTitle: "work subject", Scope: "globex"}},
		{CmuxWorkspaceID: cmuxWS3, Attention: "waiting", WaitingCount: 1, WaitType: "review", WaitReason: "perso reason", WaitScope: "perso", WaitFromSession: true},
		{CmuxWorkspaceID: cmuxWS4, Attention: "waiting", WaitingCount: 1, WaitType: "blocked", WaitReason: "work reason", WaitScope: "globex", WaitFromSession: true},
		{CmuxWorkspaceID: cmuxWS5, Title: "orphelin", TitleOwner: "pigtail"}, // bound, subject unread by the loop
	}}}
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, &Services{Store: st, CmuxSidebar: ctl}, testResponder{})
	return r, st, ctl
}

func decodeState(t *testing.T, body string) pigtail.CmuxSidebarState {
	t.Helper()
	var st pigtail.CmuxSidebarState
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return st
}

// GET /cmux/sidebar: a work caller sees every workspace's attention but not
// the perso subject; perso sees both.
func TestCmuxSidebarStateFiltersSubjectsByScope(t *testing.T) {
	t.Parallel()
	r, _, _ := sidebarFixture(t)
	for _, c := range []caller{none, work, workCross} {
		rec := doAs(t, r, c, "GET", "/cmux/sidebar", nil)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "sub_p") || strings.Contains(rec.Body.String(), "perso subject") {
			t.Fatalf("%s sees the perso subject: %s", c, rec.Body)
		}
		st := decodeState(t, rec.Body.String())
		if len(st.Workspaces) != 5 || st.Workspaces[0].Attention != "waiting" || st.Workspaces[0].Subject != nil || st.Workspaces[1].Subject == nil {
			t.Fatalf("%s: %+v", c, st.Workspaces)
		}
		// The wait type is attention, shown to all; a perso session's reason is not.
		if w := st.Workspaces[2]; w.WaitType != "review" || w.WaitReason != "" || strings.Contains(rec.Body.String(), "perso reason") {
			t.Fatalf("%s sees a perso reason: %+v", c, w)
		}
		if w := st.Workspaces[3]; w.WaitReason != "work reason" {
			t.Fatalf("%s: work reason hidden: %+v", c, w)
		}
	}
	st := decodeState(t, doAs(t, r, perso, "GET", "/cmux/sidebar", nil).Body.String())
	if st.Workspaces[0].Subject == nil || st.Workspaces[0].Subject.ID != "sub_p" || st.Workspaces[2].WaitReason != "perso reason" {
		t.Fatalf("perso: %+v", st.Workspaces)
	}
}

func TestCmuxSidebarToggle(t *testing.T) {
	t.Parallel()
	r, _, ctl := sidebarFixture(t)
	rec := doAs(t, r, work, "POST", "/cmux/sidebar", pigtail.CmuxSidebarToggle{Enabled: false})
	if rec.Code != 200 || len(ctl.enabled) != 1 || ctl.enabled[0] {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, ctl.enabled)
	}
	if st := decodeState(t, rec.Body.String()); st.Enabled || strings.Contains(rec.Body.String(), "sub_p") {
		t.Fatalf("answer %s", rec.Body)
	}
	if rec := doAs(t, r, work, "POST", "/cmux/sidebar", "nope"); rec.Code != 400 {
		t.Fatalf("bad body: %d", rec.Code)
	}
}

// Without a loop, GET answers "off" and POST 503.
func TestCmuxSidebarWithoutLoop(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	st := decodeState(t, doAs(t, r, perso, "GET", "/cmux/sidebar", nil).Body.String())
	if st.Enabled || st.Workspaces == nil || st.Warnings == nil {
		t.Fatalf("%+v", st)
	}
	if rec := doAs(t, r, perso, "POST", "/cmux/sidebar", pigtail.CmuxSidebarToggle{Enabled: true}); rec.Code != 503 {
		t.Fatalf("%d", rec.Code)
	}
}

// Retitle is a write on the bound subject: same matrix as the link routes,
// and an unbound workspace answers like an invisible one.
func TestCmuxRetitleScopeMatrix(t *testing.T) {
	t.Parallel()
	for _, c := range []caller{none, work, workCross, perso, persoCross} {
		for _, target := range []string{"t", "p"} {
			r, st, ctl := sidebarFixture(t)
			seedBinding(t, st, cmuxWS, "sub_"+target)
			if _, err := st.GetCmuxBinding(cmuxWS); err != nil {
				t.Fatal(err)
			}
			_ = st.SetCmuxOwner(cmuxWS, store.CmuxFieldTitle, pigtail.CmuxOwnerUser)
			want := 200
			switch {
			case target == "p" && c.scope != "perso":
				want = 404
			case target == "t" && c.scope == "perso" && !c.cross:
				want = 403
			}
			rec := doAs(t, r, c, "POST", "/cmux-workspaces/"+cmuxWS+"/retitle", nil)
			if rec.Code != want {
				t.Errorf("retitle as %s on %s: %d, want %d (%s)", c, target, rec.Code, want, rec.Body)
			}
			b, _ := st.GetCmuxBinding(cmuxWS)
			if want == 200 && (b.TitleOwner != pigtail.CmuxOwnerPigtail || ctl.nudges != 1) {
				t.Errorf("retitle as %s on %s: owner %s nudges %d", c, target, b.TitleOwner, ctl.nudges)
			}
			if want != 200 && (b.TitleOwner != pigtail.CmuxOwnerUser || ctl.nudges != 0) {
				t.Errorf("refused retitle as %s on %s changed something", c, target)
			}
			if want == 404 && strings.Contains(rec.Body.String(), "sub_p") {
				t.Errorf("404 names the perso subject: %s", rec.Body)
			}
		}
	}
	r, _, _ := sidebarFixture(t)
	unbound := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS2+"/retitle", nil)
	invisible := func() string {
		r, st, _ := sidebarFixture(t)
		seedBinding(t, st, cmuxWS2, "sub_p")
		return doAs(t, r, work, "POST", "/cmux-workspaces/"+cmuxWS2+"/retitle", nil).Body.String()
	}()
	if unbound.Code != 404 || errMessage(t, unbound.Body.String()) != errMessage(t, invisible) {
		t.Fatalf("unbound %d %s vs invisible %s", unbound.Code, unbound.Body, invisible)
	}
}

// Retitle hands back a description edited by hand: since D5 the title is
// always pigtail's, and the note says only the description.
func TestCmuxRetitleNotesDescription(t *testing.T) {
	t.Parallel()
	r, st, _ := sidebarFixture(t)
	seedBinding(t, st, cmuxWS, "sub_p")
	_ = st.SetCmuxOwner(cmuxWS, store.CmuxFieldDescription, pigtail.CmuxOwnerUser)
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/retitle", nil); rec.Code != 200 {
		t.Fatalf("retitle: %d %s", rec.Code, rec.Body)
	}
	es, err := st.ListLog("sub_p", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := "description cmux de " + cmuxWS + " rendue à pigtail (retitle)"
	if len(es) == 0 || es[0].Text != want {
		t.Fatalf("log %+v, want %q", es, want)
	}
}

// GET /cmux/sidebar serves the shown title and the session's one name
// (D3/D5), under the same scope redaction as the subject (D23).
func TestCmuxSidebarDisplayName(t *testing.T) {
	t.Parallel()
	r, _, _ := sidebarFixture(t)
	byID := func(st pigtail.CmuxSidebarState) map[string]pigtail.CmuxSidebarWorkspace {
		m := map[string]pigtail.CmuxSidebarWorkspace{}
		for _, w := range st.Workspaces {
			m[w.CmuxWorkspaceID] = w
		}
		return m
	}
	ws := byID(decodeState(t, doAs(t, r, perso, "GET", "/cmux/sidebar", nil).Body.String()))
	if w := ws[cmuxWS]; w.DisplayName != "perso subject" || w.Title != "perso subject" {
		t.Fatalf("perso, own workspace: %+v", w)
	}
	if w := ws[cmuxWS2]; w.DisplayName != "work subject" || w.Title != "fait" {
		t.Fatalf("bound, shown title not yet put back: the name is the short title: %+v", w)
	}
	if w := ws[cmuxWS5]; w.Title != "orphelin" || w.DisplayName != "orphelin" {
		t.Fatalf("master, subject unread by the loop: %+v", w)
	}
	for _, c := range []caller{none, work} {
		rec := doAs(t, r, c, "GET", "/cmux/sidebar", nil)
		ws := byID(decodeState(t, rec.Body.String()))
		for _, id := range []string{cmuxWS, cmuxWS5} {
			if w := ws[id]; w.Title != "" || w.DisplayName != "" {
				t.Fatalf("%s sees the title of %s: %+v", c, id, w)
			}
		}
		if strings.Contains(rec.Body.String(), "perso subject") || strings.Contains(rec.Body.String(), "orphelin") {
			t.Fatalf("%s: %s", c, rec.Body)
		}
	}
}

func errMessage(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Code + ":" + e.Message
}

// Detach hands the binding as it was to the loop (to give the title back);
// attach nudges it.
func TestCmuxLinkRoutesTellTheLoop(t *testing.T) {
	t.Parallel()
	r, st, ctl := sidebarFixture(t)
	rec := doAs(t, r, perso, "POST", "/subjects/sub_p/cmux-workspaces", pigtail.AttachCmuxWorkspaceRequest{CmuxWorkspaceID: cmuxWS})
	if rec.Code != 201 || ctl.nudges != 1 {
		t.Fatalf("attach %d nudges %d", rec.Code, ctl.nudges)
	}
	_ = st.SetCmuxApplied(cmuxWS, store.CmuxFieldTitle, "perso subject")
	_ = st.CaptureCmuxPrev(cmuxWS, store.CmuxFieldTitle, "avant", true)
	rec = doAs(t, r, perso, "DELETE", "/subjects/sub_p/cmux-workspaces/"+cmuxWS, nil)
	if rec.Code != 200 || len(ctl.released) != 1 {
		t.Fatalf("detach %d released %+v", rec.Code, ctl.released)
	}
	if b := ctl.released[0]; b.AppliedTitle != "perso subject" || b.CmuxWorkspaceID != cmuxWS || b.BoundAt.After(time.Now()) {
		t.Fatalf("released %+v", b)
	}
}

// D14: POST /cmux-workspaces/{wsid}/trust-pending hands the loop a spawned
// Claude stopped on "trust this folder"; 400 on a bad UUID or pid, 503
// without a loop.
func TestCmuxTrustPending(t *testing.T) {
	t.Parallel()
	r, _, ctl := sidebarFixture(t)
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/trust-pending", pigtail.CmuxTrustPendingRequest{PID: 5000}); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(ctl.trust) != 1 || ctl.trust[0] != cmuxWS+"|5000" {
		t.Fatalf("trust = %v", ctl.trust)
	}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/nope/trust-pending", pigtail.CmuxTrustPendingRequest{PID: 5000}); rec.Code != 400 {
		t.Fatalf("bad ws: %d", rec.Code)
	}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/trust-pending", pigtail.CmuxTrustPendingRequest{}); rec.Code != 400 {
		t.Fatalf("no pid: %d", rec.Code)
	}
	r2, _ := scopedFixture(t) // no loop
	if rec := doAs(t, r2, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/trust-pending", pigtail.CmuxTrustPendingRequest{PID: 1}); rec.Code != 503 {
		t.Fatalf("no loop: %d", rec.Code)
	}
}

const respawnSurface = "5e5e5e5e-0000-4000-8000-0000000000a1"

// D16: POST /cmux-workspaces/{wsid}/respawn hands the loop a respawn run's
// state; workspace and surface reach it as upper-case UUIDs.
func TestCmuxRespawnMark(t *testing.T) {
	t.Parallel()
	r, _, ctl := sidebarFixture(t)
	lower := strings.ToLower(cmuxWS)
	for _, req := range []pigtail.CmuxRespawnRequest{
		{State: pigtail.CmuxRespawnWaiting, Wait: pigtail.CmuxRespawnWaitDraft, SurfaceID: respawnSurface, PID: 7000},
		{State: pigtail.CmuxRespawnFailed, SurfaceID: respawnSurface},
		{State: pigtail.CmuxRespawnAbandoned},
	} {
		if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+lower+"/respawn", req); rec.Code != 204 {
			t.Fatalf("%+v: %d %s", req, rec.Code, rec.Body)
		}
	}
	up := strings.ToUpper(respawnSurface)
	want := []string{
		"mark " + cmuxWS + "|waiting|draft|" + up + "|7000",
		"mark " + cmuxWS + "|failed||" + up + "|0",
		"mark " + cmuxWS + "|abandoned|||0",
	}
	if strings.Join(ctl.respawn, "\n") != strings.Join(want, "\n") {
		t.Fatalf("respawn = %q, want %q", ctl.respawn, want)
	}
}

// A closed contract: every malformed request is a 400 and reaches no loop.
func TestCmuxRespawnMarkRejects(t *testing.T) {
	t.Parallel()
	r, _, ctl := sidebarFixture(t)
	ok := pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnWaiting, Wait: pigtail.CmuxRespawnWaitDraft, SurfaceID: respawnSurface, PID: 7000}
	cases := map[string]struct {
		ws  string
		req any
	}{
		"bad workspace":            {"nope", ok},
		"bad JSON":                 {cmuxWS, "not an object"},
		"unknown state":            {cmuxWS, pigtail.CmuxRespawnRequest{State: "respawned", SurfaceID: respawnSurface}},
		"no state":                 {cmuxWS, pigtail.CmuxRespawnRequest{SurfaceID: respawnSurface}},
		"waiting, no pid":          {cmuxWS, pigtail.CmuxRespawnRequest{State: "waiting", Wait: "draft", SurfaceID: respawnSurface}},
		"waiting, no surface":      {cmuxWS, pigtail.CmuxRespawnRequest{State: "waiting", Wait: "draft", PID: 7000}},
		"waiting, unknown wait":    {cmuxWS, pigtail.CmuxRespawnRequest{State: "waiting", Wait: "a free reason", SurfaceID: respawnSurface, PID: 7000}},
		"waiting, no wait":         {cmuxWS, pigtail.CmuxRespawnRequest{State: "waiting", SurfaceID: respawnSurface, PID: 7000}},
		"surface not a UUID":       {cmuxWS, pigtail.CmuxRespawnRequest{State: "failed", SurfaceID: "surface:7"}},
		"surface read as a flag":   {cmuxWS, pigtail.CmuxRespawnRequest{State: "failed", SurfaceID: "--workspace"}},
		"final with a wait":        {cmuxWS, pigtail.CmuxRespawnRequest{State: "failed", Wait: "draft", SurfaceID: respawnSurface}},
		"final with a pid":         {cmuxWS, pigtail.CmuxRespawnRequest{State: "abandoned", SurfaceID: respawnSurface, PID: 7000}},
		"waiting with a run id":    {cmuxWS, pigtail.CmuxRespawnRequest{State: "waiting", Wait: "draft", SurfaceID: respawnSurface, PID: 7000, RunID: "r1"}},
		"waiting with a detail":    {cmuxWS, pigtail.CmuxRespawnRequest{State: "waiting", Wait: "draft", SurfaceID: respawnSurface, PID: 7000, Detail: "x"}},
		"run id with a space":      {cmuxWS, pigtail.CmuxRespawnRequest{State: "abandoned", RunID: "r 1"}},
		"session id with a slash":  {cmuxWS, pigtail.CmuxRespawnRequest{State: "abandoned", RunID: "r1", SessionID: "../x"}},
		"subject id not a subject": {cmuxWS, pigtail.CmuxRespawnRequest{State: "abandoned", RunID: "r1", SubjectID: "../sub_p"}},
		"subject id uppercase":     {cmuxWS, pigtail.CmuxRespawnRequest{State: "failed", RunID: "r1", SubjectID: "sub_P"}},
		"subject id no prefix":     {cmuxWS, pigtail.CmuxRespawnRequest{State: "failed", RunID: "r1", SubjectID: "thm_p"}},
	}
	for name, c := range cases {
		if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+c.ws+"/respawn", c.req); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(ctl.respawn) != 0 {
		t.Fatalf("a refused request reached the loop: %q", ctl.respawn)
	}
}

// DELETE /cmux-workspaces/{wsid}/respawn drops the workspace's mark.
func TestCmuxRespawnClear(t *testing.T) {
	t.Parallel()
	r, _, ctl := sidebarFixture(t)
	if rec := doAs(t, r, perso, "DELETE", "/cmux-workspaces/"+strings.ToLower(cmuxWS)+"/respawn", nil); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(ctl.respawn) != 1 || ctl.respawn[0] != "clear "+cmuxWS {
		t.Fatalf("respawn = %q", ctl.respawn)
	}
	if rec := doAs(t, r, perso, "DELETE", "/cmux-workspaces/nope/respawn", nil); rec.Code != 400 {
		t.Fatalf("bad ws: %d", rec.Code)
	}
}

// Without a loop, both answer 503: the worker says so once in its record.
func TestCmuxRespawnWithoutLoop(t *testing.T) {
	t.Parallel()
	r, _ := scopedFixture(t)
	req := pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, SurfaceID: respawnSurface}
	if rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", req); rec.Code != 503 {
		t.Fatalf("POST: %d", rec.Code)
	}
	if rec := doAs(t, r, perso, "DELETE", "/cmux-workspaces/"+cmuxWS+"/respawn", nil); rec.Code != 503 {
		t.Fatalf("DELETE: %d", rec.Code)
	}
}

// A caller does not learn the cell of a session it may not see, like its
// reason; a cell with no scope is the master's only (D17).
func TestCmuxSidebarCellsFollowScope(t *testing.T) {
	scopetest.Three(t)
	ctl := &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Workspaces: []pigtail.CmuxSidebarWorkspace{{
		CmuxWorkspaceID: "W1", Cells: []string{"w1", "w2", "w3", "w4"}, CellScopes: []string{"perso", "globex", "acme", ""},
		WaitType: "login", WaitReason: "w1", WaitScope: "perso", WaitFromSession: true, Attention: "waiting",
	}}}}
	svc := &Services{CmuxSidebar: ctl}
	for _, c := range []struct {
		caller scope.Scope
		cells  []string
		reason string
	}{
		{"globex", []string{"w2"}, ""},
		{"acme", []string{"w3"}, ""},
		{"perso", []string{"w1", "w2", "w3", "w4"}, "w1"},
	} {
		w := cmuxSidebarState(svc, callerScope{scope: c.caller}).Workspaces[0]
		if !reflect.DeepEqual(w.Cells, c.cells) || w.WaitReason != c.reason || w.WaitType != "login" {
			t.Fatalf("%s view = %+v", c.caller, w)
		}
	}
	if c := ctl.state.Workspaces[0].Cells; !reflect.DeepEqual(c, []string{"w1", "w2", "w3", "w4"}) {
		t.Fatalf("the masking wrote through to the loop's state: %v", c)
	}
}

// pigtail's own reason (trust folder, respawn) names no session: it is shown
// to whoever sees the workspace, as before D17. A session's reason with no
// scope is the master's only.
func TestSidebarSystemReasonVisibleToItsWorkspaceScope(t *testing.T) {
	t.Parallel()
	r, _, ctl := sidebarFixture(t)
	ctl.state.Workspaces = []pigtail.CmuxSidebarWorkspace{
		{CmuxWorkspaceID: cmuxWS, Attention: "waiting", WaitingCount: 1, WaitType: "blocked", WaitReason: "trust folder", WaitScope: "", WaitFromSession: false,
			Subject: &pigtail.CmuxSidebarSubject{ID: "sub_t", ShortTitle: "work subject", Scope: "globex"}},
		{CmuxWorkspaceID: cmuxWS2, Attention: "waiting", WaitingCount: 1, WaitType: "review", WaitReason: "PR #9", WaitScope: "", WaitFromSession: true},
	}
	for c, want := range map[string][2]bool{"globex": {true, false}, "perso": {true, true}} {
		body := doAs(t, r, caller{scope: c}, "GET", "/cmux/sidebar", nil).Body.String()
		if got := [2]bool{strings.Contains(body, "trust folder"), strings.Contains(body, "PR #9")}; got != want {
			t.Errorf("as %s: trust folder / PR #9 shown = %v, want %v\n%s", c, got, want, body)
		}
	}
}

func TestNormalizeRespawnRequestFlattensDetail(t *testing.T) {
	t.Parallel()
	req := pigtail.CmuxRespawnRequest{State: "failed", RunID: "20261004T002742.681-8620", Detail: "a\nb  " + strings.Repeat("é", 1000)}
	if msg := normalizeRespawnRequest(&req); msg != "" {
		t.Fatal(msg)
	}
	if strings.ContainsAny(req.Detail, "\r\n") || len(req.Detail) > 1500 || !utf8.ValidString(req.Detail) || !strings.HasPrefix(req.Detail, "a b ") {
		t.Fatalf("detail = %q (%d bytes)", req.Detail[:20], len(req.Detail))
	}
}

type fakeNotifier struct {
	calls []string
	ans   pigtail.CmuxRespawnNotice
}

func (f *fakeNotifier) NotifyRespawnEnd(c scope.Scope, cross bool, ws string, req pigtail.CmuxRespawnRequest) pigtail.CmuxRespawnNotice {
	f.calls = append(f.calls, fmt.Sprintf("%s|%t|%s|%s|%s|%s", c, cross, ws, req.State, req.RunID, req.SessionID))
	return f.ans
}

func TestCmuxRespawnFinalNotifies(t *testing.T) {
	t.Parallel()
	_, st := scopedFixture(t)
	ctl := &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Enabled: true}} // a running loop: the pill is posted
	n := &fakeNotifier{ans: pigtail.CmuxRespawnNotice{Journal: "ok", Wait: "ok", Notice: "ok"}}
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, &Services{Store: st, CmuxSidebar: ctl, RespawnNotifier: n}, testResponder{})
	req := pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnAbandoned, SurfaceID: respawnSurface, RunID: "r1", SubjectID: "sub_p", SessionID: "s1", Detail: "d"}
	rec := doAs(t, r, persoCross, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", req)
	var got pigtail.CmuxRespawnNotice // testResponder writes the bare object, no {data:…}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	want := n.ans
	want.Pill = pigtail.CmuxRespawnNoticeOK
	if rec.Code != 200 || got != want || len(n.calls) != 1 || n.calls[0] != "perso|true|"+cmuxWS+"|abandoned|r1|s1" || len(ctl.respawn) != 1 {
		t.Fatalf("code %d body %s calls %q marks %q", rec.Code, rec.Body, n.calls, ctl.respawn)
	}
	// Without run_id (an older worker): the pill alone, 204, no notifier.
	rec = doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed})
	if rec.Code != 204 || len(n.calls) != 1 {
		t.Fatalf("no run: code %d, calls %q", rec.Code, n.calls)
	}
	// No sidebar loop (--cmux-sidebar off, the default): the session is still
	// told, and the route says so — 200 and the report, the pill apart (CA8b).
	r2 := chi.NewRouter()
	RegisterSubjectRoutes(r2, &Services{Store: st, RespawnNotifier: n}, testResponder{})
	rec = doAs(t, r2, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", req)
	got = pigtail.CmuxRespawnNotice{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(n.calls) != 2 || got.Notice != "ok" || got.Pill != "skipped: this daemon runs no cmux sidebar loop" {
		t.Fatalf("no loop: code %d body %s, calls %q", rec.Code, rec.Body, n.calls)
	}
	// Without a run and without the loop: 503, as before.
	if rec := doAs(t, r2, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed}); rec.Code != 503 {
		t.Fatalf("no run, no loop: code %d, want 503", rec.Code)
	}
}

// The route poses a wait and a message on a NAMED session: a caller naming a
// session of another scope gets the unknown-session answer (skipped), with
// nothing posed and nothing of that session in the body — its tab title
// (CmuxTitle "Garden") above all.
func TestCmuxRespawnFinalOtherScopeSession(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t) // s-perso: perso, CmuxTitle "Garden"
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, &Services{Store: f.st, RespawnNotifier: f.rl}, testResponder{})
	post := func(run, session string) (int, pigtail.CmuxRespawnNotice, string) {
		req := pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed, RunID: run, SessionID: session, Detail: "d"}
		rec := doAs(t, r, work, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", req)
		var got pigtail.CmuxRespawnNotice
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		return rec.Code, got, rec.Body.String()
	}
	code, got, body := post("r-other", "s-perso")
	if code != 200 || got.Wait != "skipped: session s-perso unknown to this scope" || got.Notice != got.Wait {
		t.Fatalf("work naming a perso session: %d %s", code, body)
	}
	if strings.Contains(body, "Garden") {
		t.Fatalf("the answer leaks the perso session: %s", body)
	}
	// The same answer as for a session nobody registered.
	_, unknown, _ := post("r-nobody", "s-nobod")
	if unknown.Wait != strings.ReplaceAll(got.Wait, "s-perso", "s-nobod") || unknown.Journal != got.Journal {
		t.Fatalf("perso %+v vs unknown %+v", got, unknown)
	}
	ws, err := f.st.SessionWaits("s-perso")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if strings.HasPrefix(w.Reason, RespawnWaitReasonPrefix) {
			t.Fatalf("a wait was posed on the perso session: %+v", w)
		}
	}
	if ms, _ := f.st.ListMessages(pigtail.MessageFilter{To: "s-perso"}); len(ms) != 0 {
		t.Fatalf("a message reached the perso session: %+v", ms)
	}
}

// CA8b on a real daemon: the sidebar loop is always constructed
// (cmuxsync_wiring.go), and --cmux-sidebar off (the default) leaves it
// disabled — not nil. A final state with its run still tells the session and
// says the pill was skipped; nothing is marked in the loop. Without a run,
// the disabled loop keeps today's 204 (older workers, CA8).
func TestCmuxRespawnFinalLoopDisabled(t *testing.T) {
	t.Parallel()
	_, st := scopedFixture(t)
	n := &fakeNotifier{ans: pigtail.CmuxRespawnNotice{Journal: "ok", Wait: "ok", Notice: "ok"}}
	for _, c := range []struct {
		name  string
		state pigtail.CmuxSidebarState
	}{
		{"off", pigtail.CmuxSidebarState{Enabled: false}},
		{"stopped by a panic", pigtail.CmuxSidebarState{Enabled: true, DisabledByPanic: true}},
	} {
		ctl := &fakeSidebarCtl{state: c.state}
		r := chi.NewRouter()
		RegisterSubjectRoutes(r, &Services{Store: st, CmuxSidebar: ctl, RespawnNotifier: n}, testResponder{})
		req := pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnAbandoned, SurfaceID: respawnSurface, RunID: "r1", SubjectID: "sub_p", SessionID: "s1", Detail: "d"}
		rec := doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", req)
		var got pigtail.CmuxRespawnNotice
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != 200 || got.Notice != "ok" || got.Pill != "skipped: this daemon runs no cmux sidebar loop" || len(ctl.respawn) != 0 {
			t.Fatalf("%s: code %d body %s, marks %q", c.name, rec.Code, rec.Body, ctl.respawn)
		}
		// Without a run: unchanged, the mark goes to the loop and 204.
		rec = doAs(t, r, perso, "POST", "/cmux-workspaces/"+cmuxWS+"/respawn", pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnFailed})
		if rec.Code != 204 || len(ctl.respawn) != 1 {
			t.Fatalf("%s, no run: code %d, marks %q", c.name, rec.Code, ctl.respawn)
		}
	}
}

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// withStandbyMarkers gives the fixture's relay a Standby marker dir.
func (f *relayFixture) withStandbyMarkers(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "standby")
	f.rl.cfg.StandbyMarkerDir = dir
	return dir
}

func standbyMarker(dir, session string) bool {
	_, err := os.Stat(filepath.Join(dir, session))
	return err == nil
}

// nextStandbyEvent reads one session.standby_changed, or fails.
func nextStandbyEvent(t *testing.T, sub *events.Subscription) pigtail.Event {
	t.Helper()
	select {
	case e := <-sub.C:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no session.standby_changed")
	}
	return pigtail.Event{}
}

func noStandbyEvent(t *testing.T, sub *events.Subscription) {
	t.Helper()
	select {
	case e := <-sub.C:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// PUT poses the row and its marker and publishes standby:true with the
// session's scope; DELETE drops both and publishes standby:false.
func TestSessionStandbyPutDelete(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir := f.withStandbyMarkers(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionStandbyChanged))
	defer sub.Close()
	f.peers(t, pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: "W1", Scope: "globex"})

	rec := doJSON(t, f.r, "PUT", "/peers/s1/standby", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	var sb pigtail.SessionStandby
	if err := json.Unmarshal(rec.Body.Bytes(), &sb); err != nil || sb.SessionID != "s1" || !sb.Since.Equal(f.clock()) {
		t.Fatalf("PUT body %s: %+v %v", rec.Body.String(), sb, err)
	}
	if got, _ := f.st.GetStandby("s1"); got == nil {
		t.Fatal("no row after PUT")
	}
	if !standbyMarker(dir, "s1") {
		t.Fatal("no standby marker")
	}
	e := nextStandbyEvent(t, sub)
	if e.Data["session_id"] != "s1" || e.Data["cmux_workspace_id"] != "W1" || e.Data["standby"] != true || e.Scope != "globex" {
		t.Fatalf("event %+v", e)
	}

	rec = doJSON(t, f.r, "DELETE", "/peers/s1/standby", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := f.st.GetStandby("s1"); got != nil {
		t.Fatalf("row after DELETE: %+v", got)
	}
	if standbyMarker(dir, "s1") {
		t.Fatal("marker left after DELETE")
	}
	e = nextStandbyEvent(t, sub)
	if e.Data["standby"] != false || e.Scope != "globex" {
		t.Fatalf("event %+v", e)
	}
	// Clearing what is not there is a quiet success, with no event.
	if rec := doJSON(t, f.r, "DELETE", "/peers/s1/standby", nil); rec.Code != http.StatusOK {
		t.Fatalf("second DELETE = %d", rec.Code)
	}
	noStandbyEvent(t, sub)
}

// Acting as a session of another scope, or an unknown one, is the
// unknown-session answer (actingAs).
func TestSessionStandbyRefusals(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.withStandbyMarkers(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-perso", Scope: "perso", CellID: "mp"})
	for _, c := range []struct{ method, path string }{
		{"PUT", "/peers/s-perso/standby"}, {"DELETE", "/peers/s-perso/standby"},
		{"PUT", "/peers/s-unknown/standby"}, {"DELETE", "/peers/s-unknown/standby"},
	} {
		rec := doJSON(t, f.r, c.method, c.path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if got, _ := f.st.GetStandby("s-perso"); got != nil {
		t.Fatalf("a refused PUT set the row: %+v", got)
	}
}

// D24: the end of a session keeps its Standby and its marker (a relaunch
// resumes the same id); no standby:false is published.
func TestSessionEndKeepsStandbyMarker(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir := f.withStandbyMarkers(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1"})
	if rec := doJSON(t, f.r, "PUT", "/peers/s1/standby", nil); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionStandbyChanged))
	defer sub.Close()
	rec := doJSON(t, f.r, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s1"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("end = %d", rec.Code)
	}
	if got, _ := f.st.GetStandby("s1"); got == nil || !standbyMarker(dir, "s1") {
		t.Fatalf("end dropped the Standby: row=%+v marker=%v", got, standbyMarker(dir, "s1"))
	}
	noStandbyEvent(t, sub)
}

// The markers are rebuilt from the table at start: a wiped directory must not
// let a hook read a standby session as not in Standby.
func TestStandbyMarkersRebuiltAtStart(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir := f.withStandbyMarkers(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1"})
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "stale"), nil, 0o600)
	f.rl.syncMarkers()
	if !standbyMarker(dir, "s1") || standbyMarker(dir, "stale") {
		t.Fatalf("markers: s1=%v stale=%v", standbyMarker(dir, "s1"), standbyMarker(dir, "stale"))
	}
}

// The one daemon predicate (§ Prédicat Standby): a row → in Standby; none →
// not; a read error → in Standby, never "not in Standby".
func TestInStandbyPredicate(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	if in, why := f.rl.inStandby("s1"); in || why != "" {
		t.Fatalf("absent = %v %q", in, why)
	}
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	if in, why := f.rl.inStandby("s1"); !in || why != "standby (D16)" {
		t.Fatalf("present = %v %q", in, why)
	}
	f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) { return nil, errors.New("disk gone") }
	in, why := f.rl.inStandby("s2")
	if !in || !strings.HasPrefix(why, "standby unreadable: ") || !strings.Contains(why, "disk gone") {
		t.Fatalf("unreadable = %v %q", in, why)
	}
}

const standbySF = "5F2B7C1E-0000-4000-8000-00000000000A"

// standbyOnSurface registers s1 (scope perso) on the surface standbySF and
// puts it in Standby, with a bus subscribed afterwards.
func standbyOnSurface(t *testing.T, f *relayFixture) (string, *events.Subscription) {
	t.Helper()
	dir := f.withStandbyMarkers(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Name: "one", Scope: "perso", CmuxWorkspaceID: "W1", CmuxSurfaceID: standbySF, Source: "hook"})
	if rec := doAs(t, f.r, perso, "PUT", "/peers/s1/standby", nil); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionStandbyChanged))
	t.Cleanup(sub.Close)
	return dir, sub
}

func postPeer(t *testing.T, f *relayFixture, p pigtail.PeerSession) {
	t.Helper()
	if rec := doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{p}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /peers = %d %s", rec.Code, rec.Body.String())
	}
}

func keepsStandby(t *testing.T, f *relayFixture, dir string, sub *events.Subscription) {
	t.Helper()
	if sb, _ := f.st.GetStandby("s1"); sb == nil || !standbyMarker(dir, "s1") {
		t.Fatalf("s1 lost its Standby: row=%+v marker=%v", sb, standbyMarker(dir, "s1"))
	}
	noStandbyEvent(t, sub)
}

// P16c: the SessionStart of a /compact keeps the id — its registration keeps
// the Standby.
func TestCompactRegistrationKeepsStandby(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir, sub := standbyOnSurface(t, f)
	postPeer(t, f, pigtail.PeerSession{SessionID: "s1", CmuxSurfaceID: standbySF, Source: "hook"})
	keepsStandby(t, f, dir, sub)
}

// D24: a relaunch (end, then the same id registered again) keeps the Standby.
func TestRelaunchKeepsStandby(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir, sub := standbyOnSurface(t, f)
	if rec := doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s1"}}}); rec.Code != http.StatusOK {
		t.Fatalf("end = %d", rec.Code)
	}
	postPeer(t, f, pigtail.PeerSession{SessionID: "s1", CmuxSurfaceID: standbySF, Source: "hook"})
	keepsStandby(t, f, dir, sub)
}

// D24: another conversation registered by its own SessionStart on the
// surface (/clear, a fresh claude, a respawn) ends s1's Standby — whether s1
// ended or not — with standby:false in s1's scope.
func TestOtherSessionOnSurfaceEndsStandby(t *testing.T) {
	t.Parallel()
	for _, ended := range []bool{false, true} {
		f := newRelayFixture(t)
		dir, sub := standbyOnSurface(t, f)
		if ended {
			if rec := doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s1"}}}); rec.Code != http.StatusOK {
				t.Fatalf("end = %d", rec.Code)
			}
		}
		postPeer(t, f, pigtail.PeerSession{SessionID: "s2", Name: "two", Scope: "perso", CmuxSurfaceID: strings.ToLower(standbySF), Source: "hook"})
		if sb, _ := f.st.GetStandby("s1"); sb != nil || standbyMarker(dir, "s1") {
			t.Fatalf("ended=%v: s1 kept its Standby: row=%+v marker=%v", ended, sb, standbyMarker(dir, "s1"))
		}
		e := nextStandbyEvent(t, sub)
		if e.Data["session_id"] != "s1" || e.Data["standby"] != false || e.Scope != "perso" {
			t.Fatalf("ended=%v: event %+v", ended, e)
		}
	}
}

// A snapshot (Source ≠ hook) never ends a Standby: its tty attribution can be stale.
func TestSnapshotOnSurfaceKeepsStandby(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"snapshot", "self", ""} {
		f := newRelayFixture(t)
		dir, sub := standbyOnSurface(t, f)
		postPeer(t, f, pigtail.PeerSession{SessionID: "s2", Name: "two", Scope: "perso", CmuxSurfaceID: standbySF, Source: src})
		keepsStandby(t, f, dir, sub)
	}
}

// Never by workspace: s2 on another surface of the same workspace leaves s1 alone.
func TestOtherSurfaceKeepsStandby(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir, sub := standbyOnSurface(t, f)
	postPeer(t, f, pigtail.PeerSession{SessionID: "s2", Name: "two", Scope: "perso", CmuxWorkspaceID: "W1", CmuxSurfaceID: "5F2B7C1E-0000-4000-8000-00000000000B", Source: "hook"})
	keepsStandby(t, f, dir, sub)
}

// Task 16: EndStandbyGone drops the row and the marker and publishes
// standby:false with the session's scope; a row already gone publishes nothing.
func TestEndStandbyGone(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir := f.withStandbyMarkers(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: "W1", Scope: "perso"})
	if rec := doAs(t, f.r, perso, "PUT", "/peers/s1/standby", nil); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionStandbyChanged))
	defer sub.Close()
	if err := f.rl.EndStandbyGone("s1", "W1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetStandby("s1"); got != nil || standbyMarker(dir, "s1") {
		t.Fatalf("kept: row=%+v marker=%v", got, standbyMarker(dir, "s1"))
	}
	e := nextStandbyEvent(t, sub)
	if e.Data["session_id"] != "s1" || e.Data["standby"] != false || e.Data["cmux_workspace_id"] != "W1" || e.Scope != "perso" {
		t.Fatalf("event %+v", e)
	}
	if err := f.rl.EndStandbyGone("s1", "W1"); err != nil { // already gone
		t.Fatal(err)
	}
	noStandbyEvent(t, sub)
}

// D29 of sub_x000000000000014: a pin by hand on a tab in Standby lifts the
// Standby — the same four gestures as EndStandbyGone (row, marker,
// session.standby_changed standby:false, a log line naming the pin).
func TestStandbyHandPinEmitsEvent(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	dir := f.withStandbyMarkers(t)
	var logged []string
	f.rl.svc.Logf = func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }
	f.peers(t, pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: "W1", Scope: "perso"})
	if rec := doAs(t, f.r, perso, "PUT", "/peers/s1/standby", nil); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionStandbyChanged))
	defer sub.Close()
	if err := f.rl.EndStandbyPinned("s1", "W1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetStandby("s1"); got != nil || standbyMarker(dir, "s1") {
		t.Fatalf("kept: row=%+v marker=%v", got, standbyMarker(dir, "s1"))
	}
	e := nextStandbyEvent(t, sub)
	if e.Data["session_id"] != "s1" || e.Data["standby"] != false || e.Data["cmux_workspace_id"] != "W1" || e.Scope != "perso" {
		t.Fatalf("event %+v", e)
	}
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "standby cleared by pin: s1 (workspace W1)") }) {
		t.Fatalf("no log line: %q", logged)
	}
	if err := f.rl.EndStandbyPinned("s1", "W1"); err != nil { // already gone
		t.Fatal(err)
	}
	noStandbyEvent(t, sub)
}

// The PUT answer says why the folder arrangement will not file the tab in
// « Standby »: folders off, or the sidebar loop off; nothing when the folders
// act, and nothing when no loop is wired (an unknown state is no guess).
func TestSessionStandbyPutFoldersOff(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		ctl  CmuxSidebarControl
		want string
	}{
		{"folders off", &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Enabled: true, Folders: false}}, "pigtaild --cmux-folders=false"},
		{"sidebar off", &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Enabled: false, Folders: false}}, "cmux sidebar off"},
		{"folders on", &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Enabled: true, Folders: true}}, ""},
		{"no loop", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRelayFixture(t)
			if c.ctl != nil {
				f.rl.svc.CmuxSidebar = c.ctl
			}
			f.peers(t, pigtail.PeerSession{SessionID: "s1", Scope: "globex"})
			rec := doJSON(t, f.r, "PUT", "/peers/s1/standby", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
			}
			var sb pigtail.SessionStandby
			if err := json.Unmarshal(rec.Body.Bytes(), &sb); err != nil {
				t.Fatal(err)
			}
			if sb.FoldersOff != c.want {
				t.Fatalf("folders_off = %q, want %q (body %s)", sb.FoldersOff, c.want, rec.Body.String())
			}
			if c.want == "" && strings.Contains(rec.Body.String(), "folders_off") {
				t.Fatalf("folders_off sent empty: %s", rec.Body.String())
			}
		})
	}
}

func TestRelayInStandbyWrapsPredicate(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	if in, err := f.rl.InStandby("s1"); in || err != nil {
		t.Fatalf("no row = (%v, %v), want (false, nil)", in, err)
	}
	if err := f.st.SetStandby("s1", f.clock()); err != nil {
		t.Fatal(err)
	}
	if in, err := f.rl.InStandby("s1"); !in || err != nil {
		t.Fatalf("row = (%v, %v), want (true, nil)", in, err)
	}
	f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) { return nil, errors.New("disk gone") }
	if in, err := f.rl.InStandby("s2"); !in || err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Fatalf("unreadable = (%v, %v), want (true, err)", in, err)
	}
}

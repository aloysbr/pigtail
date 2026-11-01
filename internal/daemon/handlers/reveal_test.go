package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const revealWS = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"

// revealFixture: one perso session in a cmux tab, one work session with none;
// every cmux reveal goes to a recorder (never the real cmux).
func revealFixture(t *testing.T) (*relayFixture, *[]string) {
	t.Helper()
	f := newRelayFixture(t)
	f.peers(t,
		pigtail.PeerSession{SessionID: "s-perso", CmuxWorkspaceID: revealWS, Name: "perso-a", Scope: "perso", CellID: "mp"},
		pigtail.PeerSession{SessionID: "s-work", Name: "work-a", Scope: "gx", CellID: "mt"},
	)
	var calls []string
	f.rl.cfg.Reveal = func(ws string) error { calls = append(calls, ws); return nil }
	return f, &calls
}

// TestRevealRouteScopeAndMissingWorkspace: work → a perso session is 404 and
// cmux is never called; an unknown session the same; a session without
// cmux_workspace_id 409; an agent session 403; the session's own scope
// reveals its workspace.
func TestRevealRouteScopeAndMissingWorkspace(t *testing.T) {
	f, calls := revealFixture(t)
	for _, c := range []struct {
		who  caller
		path string
		want int
	}{
		{work, "/peers/s-perso/reveal", http.StatusNotFound},
		{work, "/peers/s-nobody/reveal", http.StatusNotFound},
		{perso, "/peers/s-work/reveal", http.StatusConflict}, // the master reads work; no tab
		{work, "/peers/s-work/reveal", http.StatusConflict},
	} {
		rec := doAs(t, f.r, c.who, "POST", c.path, nil)
		if rec.Code != c.want {
			t.Errorf("POST %s as %s = %d, want %d: %s", c.path, c.who, rec.Code, c.want, rec.Body.String())
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("cmux reveal ran %q before any allowed call", *calls)
	}
	// the 404 of another scope's session is the unknown one's, word for word
	other := doAs(t, f.r, work, "POST", "/peers/s-perso/reveal", nil).Body.String()
	unknown := doAs(t, f.r, work, "POST", "/peers/s-nobody/reveal", nil).Body.String()
	if other != unknown {
		t.Fatalf("another scope's session is told apart:\n%s\n%s", other, unknown)
	}
	// an agent session never takes the screen
	req := httptest.NewRequest("POST", "/peers/s-perso/reveal", nil)
	req.Header.Set(pigtail.HeaderScope, "perso")
	req.Header.Set(pigtail.HeaderSession, "s-perso")
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(*calls) != 0 {
		t.Fatalf("from an agent session = %d (calls %q), want 403 and no cmux", rec.Code, *calls)
	}
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/reveal", nil); rec.Code != http.StatusOK {
		t.Fatalf("POST as perso = %d: %s", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0] != revealWS {
		t.Fatalf("reveal calls = %q, want %s once", *calls, revealWS)
	}
}

func TestRevealRouteMasterAndCmuxErrors(t *testing.T) {
	f, calls := revealFixture(t)
	master := caller{scope: string(scope.Master())}
	if rec := doAs(t, f.r, master, "POST", "/peers/s-perso/reveal", nil); rec.Code != http.StatusOK || len(*calls) != 1 {
		t.Fatalf("master = %d (calls %q): %s", rec.Code, *calls, rec.Body.String())
	}
	for _, c := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: x", cmux.ErrRevealNotOpen), http.StatusConflict},
		{errors.New("cmux identify: connection refused"), http.StatusServiceUnavailable},
	} {
		f.rl.cfg.Reveal = func(string) error { return c.err }
		if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/reveal", nil); rec.Code != c.want {
			t.Errorf("reveal err %v = %d, want %d: %s", c.err, rec.Code, c.want, rec.Body.String())
		}
	}
}

// TestRevealRouteStoreErrorIs500: an unknown session is a 404, a store that
// fails reading the peer is a 500 (never the unknown session's 404), and cmux
// is not called either way.
func TestRevealRouteStoreErrorIs500(t *testing.T) {
	f, calls := revealFixture(t)
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-nobody/reveal", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	if rec := doAs(t, f.r, perso, "POST", "/peers/s-perso/reveal", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("cmux reveal ran %q on a store error", *calls)
	}
}

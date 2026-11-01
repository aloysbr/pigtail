package handlers

import (
	"net/http"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The Prio routes are gone (D3 of sub_x000000000000014: the prio is cmux's
// own pin, written by the session's CLI): PUT and DELETE /peers/{session}/prio
// answer like any unknown route.
func TestPrioRoutesGone(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: "W1"})
	for _, m := range []string{"PUT", "DELETE"} {
		rec := doJSON(t, f.r, m, "/peers/s1/prio", nil)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /peers/s1/prio = %d %s, want 404 or 405", m, rec.Code, rec.Body.String())
		}
	}
}

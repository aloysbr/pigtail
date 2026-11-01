package store

import (
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// UpsertPeer is the single point where a session's scope enters the store:
// the alias and any case are written canonical; an empty scope stays empty.
func TestUpsertPeerCanonicalScope(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, in := range map[string]string{"a": "gx", "b": " Globex ", "c": "PERSO", "d": "", "e": "Zeta"} {
		if err := s.UpsertPeer(&pigtail.PeerSession{SessionID: id, Scope: in}); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{"a": "globex", "b": "globex", "c": "perso", "d": "", "e": "zeta"} {
		p, err := s.GetPeer(id)
		if err != nil || p.Scope != want {
			t.Errorf("peer %s scope = %q (%v), want %q", id, p.Scope, err, want)
		}
	}
}

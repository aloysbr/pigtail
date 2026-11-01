package store

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestStoredScopes(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.UnixMilli(1)
	if err := s.CreateTheme(&pigtail.Theme{ID: "thm_t", Name: "t", Kind: "area", VaultLink: "Globex/x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO peer_sessions (session_id, scope, updated_at) VALUES ('a','zeta',1), ('b','',1)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.StoredScopes()
	if err != nil || strings.Join(got, ",") != "globex,zeta" {
		t.Fatalf("StoredScopes = %v, %v", got, err)
	}
}

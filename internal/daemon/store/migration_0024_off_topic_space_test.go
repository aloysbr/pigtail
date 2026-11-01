package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// 0024 (D1, sub_x000000000000019): an off-topic declaration made before the
// migration reads space = "" after it (no explicit space: inferred at read); a
// newline in a space is refused by the CHECK; a created space reads back.
func TestMigration0024OffTopicSpace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0024.db")
	db := migrateBefore(t, path, "0024")
	if _, err := db.Exec(`INSERT INTO workspace_off_topic (cmux_workspace_id, scope, reason, declared_at) VALUES ('W-old','perso','revue de sub_abc',1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0024): %v", err)
	}
	defer s.Close()
	o, err := s.GetOffTopic("W-old")
	if err != nil {
		t.Fatal(err)
	}
	if o.Space != "" || o.Reason != "revue de sub_abc" {
		t.Fatalf("after 0024: space=%q reason=%q, want empty space and the reason kept", o.Space, o.Reason)
	}
	if _, err := s.db.Exec(`UPDATE workspace_off_topic SET space = 'a' || char(10) || 'b' WHERE cmux_workspace_id = 'W-old'`); err == nil {
		t.Fatal("a space with a newline was accepted")
	}
	if err := s.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: "W-new", Scope: "perso", Space: "Terrace", DeclaredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOffTopic("W-new")
	if err != nil {
		t.Fatal(err)
	}
	if got.Space != "Terrace" {
		t.Fatalf("created space reads %q, want Terrace", got.Space)
	}
}

func newOffTopicStore(t *testing.T, space string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateOffTopic(&pigtail.WorkspaceOffTopic{CmuxWorkspaceID: "W", Scope: "perso", Space: space, DeclaredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetOffTopicSpaceUpdates(t *testing.T) {
	s := newOffTopicStore(t, "Terrace")
	if err := s.SetOffTopicSpace("W", "Hedge"); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetOffTopic("W")
	if err != nil {
		t.Fatal(err)
	}
	if o.Space != "Hedge" {
		t.Fatalf("space = %q, want Hedge", o.Space)
	}
}

func TestSetOffTopicSpaceRefusesNewline(t *testing.T) {
	s := newOffTopicStore(t, "Terrace")
	if err := s.SetOffTopicSpace("W", "a\nb"); err == nil {
		t.Fatal("a space with a newline was accepted")
	}
	o, err := s.GetOffTopic("W")
	if err != nil {
		t.Fatal(err)
	}
	if o.Space != "Terrace" {
		t.Fatalf("row changed by a refused write: space = %q", o.Space)
	}
}

func TestSetOffTopicSpaceUnknownWorkspace(t *testing.T) {
	s := newOffTopicStore(t, "")
	if err := s.SetOffTopicSpace("nope", "Terrace"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

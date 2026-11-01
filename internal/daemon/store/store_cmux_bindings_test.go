package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	wsA = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"
	wsB = "F8290864-F5B5-4B3F-9B93-0B027295DECC"
)

// bindingStore seeds one theme and two subjects, sub_a and sub_b.
func bindingStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.UnixMilli(1).UTC()
	if err := s.CreateTheme(&pigtail.Theme{ID: "thm_p", Name: "P", Kind: "area", VaultLink: "Perso/Areas/p", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sub_a", "sub_b"} {
		if err := s.CreateSubject(&pigtail.Subject{ID: id, ThemeID: "thm_p", Title: id, Slug: id + "-slug", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func bind(t *testing.T, s *Store, ws, sub string) {
	t.Helper()
	if err := s.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: sub, Source: pigtail.CmuxSourceAttach, BoundAt: time.UnixMilli(5).UTC()}); err != nil {
		t.Fatalf("bind %s → %s: %v", ws, sub, err)
	}
}

func TestCmuxBindingCreateGetList(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	bind(t, s, wsB, "sub_a") // a subject may have several workspaces

	b, err := s.GetCmuxBinding(wsA)
	if err != nil {
		t.Fatal(err)
	}
	if b.SubjectID != "sub_a" || b.Source != "attach" || b.TitleOwner != "pigtail" || b.DescriptionOwner != "pigtail" || !b.BoundAt.Equal(time.UnixMilli(5).UTC()) {
		t.Fatalf("binding = %+v", b)
	}
	list, err := s.ListCmuxBindings("sub_a")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list, _ := s.ListCmuxBindings("sub_b"); len(list) != 0 {
		t.Fatalf("sub_b list = %+v", list)
	}
	if _, err := s.GetCmuxBinding("00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ws = %v, want ErrNotFound", err)
	}
}

// First link wins: a second subject cannot take a bound workspace.
func TestCmuxBindingFirstLinkWins(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	err := s.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: wsA, SubjectID: "sub_b", Source: "ptnew", BoundAt: time.Now()})
	if !errors.Is(err, ErrCmuxBound) {
		t.Fatalf("second bind = %v, want ErrCmuxBound", err)
	}
	if b, _ := s.GetCmuxBinding(wsA); b.SubjectID != "sub_a" {
		t.Fatalf("first link lost: %+v", b)
	}
}

// Replace moves the workspace only when it is still bound to the subject the
// caller checked (compare-and-swap), and keeps the sidebar memory.
func TestCmuxBindingReplace(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	if _, err := s.db.Exec(`UPDATE subject_cmux_bindings SET prev_title = 'Old', prev_title_custom = 1, applied_title = 'A', title_owner = 'user' WHERE cmux_workspace_id = ?`, wsA); err != nil {
		t.Fatal(err)
	}
	// Stale expectation: someone else holds it now → refused.
	if err := s.ReplaceCmuxBinding(wsA, "sub_b", "sub_b", "attach", time.UnixMilli(9)); !errors.Is(err, ErrCmuxBound) {
		t.Fatalf("stale replace = %v, want ErrCmuxBound", err)
	}
	if err := s.ReplaceCmuxBinding(wsA, "sub_a", "sub_b", "subject-work", time.UnixMilli(9)); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetCmuxBinding(wsA)
	if b.SubjectID != "sub_b" || b.Source != "subject-work" || !b.BoundAt.Equal(time.UnixMilli(9).UTC()) {
		t.Fatalf("replaced = %+v", b)
	}
	if b.PrevTitle != "Old" || !b.PrevTitleCustom || b.AppliedTitle != "A" || b.TitleOwner != "user" {
		t.Fatalf("replace dropped the sidebar memory: %+v", b)
	}
}

func TestCmuxBindingDeleteAndResetOwners(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	if _, err := s.db.Exec(`UPDATE subject_cmux_bindings SET title_owner = 'user', description_owner = 'user' WHERE cmux_workspace_id = ?`, wsA); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetCmuxOwners(wsA); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.GetCmuxBinding(wsA); b.TitleOwner != "pigtail" || b.DescriptionOwner != "pigtail" {
		t.Fatalf("owners not reset: %+v", b)
	}
	// Deleting through the wrong subject is a miss, not a delete.
	if err := s.DeleteCmuxBinding("sub_b", wsA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete via other subject = %v, want ErrNotFound", err)
	}
	if err := s.DeleteCmuxBinding("sub_a", wsA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCmuxBinding(wsA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete = %v", err)
	}
}

// The explicit link fills a session's subject: it wins over the one deduced
// from the worktree, and shows as soon as it is written (the session row was
// upserted before). The join ignores the case of the stored workspace id.
func TestListPeersSubjectFromCmuxBinding(t *testing.T) {
	s := bindingStore(t)
	for _, p := range []*pigtail.PeerSession{
		{SessionID: "s1", CmuxWorkspaceID: wsA, SubjectID: "sub_a"},                // deduced sub_a, bound to sub_b below
		{SessionID: "s2", CmuxWorkspaceID: "f8290864-f5b5-4b3f-9b93-0b027295decc"}, // lower case, no deduced subject
		{SessionID: "s3", SubjectID: "sub_a"},                                      // not in cmux: deduction stays
	} {
		if err := s.UpsertPeer(p); err != nil {
			t.Fatal(err)
		}
	}
	bind(t, s, wsA, "sub_b")
	bind(t, s, wsB, "sub_b")
	ps, err := s.ListPeers(false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, p := range ps {
		got[p.SessionID] = [2]string{p.SubjectID, p.SubjectSlug}
	}
	want := map[string][2]string{
		"s1": {"sub_b", "sub_b-slug"},
		"s2": {"sub_b", "sub_b-slug"},
		"s3": {"sub_a", "sub_a-slug"},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s subject = %v, want %v", id, got[id], w)
		}
	}
	if p, _ := s.GetPeer("s1"); p.SubjectID != "sub_b" {
		t.Errorf("GetPeer s1 subject = %q, want the bound one", p.SubjectID)
	}
	// The subject's short title (pigtail.ShortTitle: the title "sub_b" here)
	// and whether the cmux workspace is bound are read on the join.
	for _, p := range ps {
		switch p.SessionID {
		case "s1", "s2":
			if !p.CmuxBound || p.SubjectShortTitle != "sub_b" {
				t.Errorf("%s bound: bound %v short %q", p.SessionID, p.CmuxBound, p.SubjectShortTitle)
			}
		case "s3":
			if p.CmuxBound || p.SubjectShortTitle != "sub_a" {
				t.Errorf("s3 deduced, not bound: bound %v short %q", p.CmuxBound, p.SubjectShortTitle)
			}
		}
	}
}

// TestCmuxBindingsMigration opens a database at schema 0011 holding a subject
// and a session: 0012 adds an empty table and nothing else changes.
func TestCmuxBindingsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-bindings.db")
	db := migrateBefore(t, path, "0012")
	if _, err := db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_p', 'P', 'area', 'Perso/Areas/p', 0, 1, 'perso')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO subjects (id, theme_id, title, slug, status, created_at, updated_at, source) VALUES ('sub_old', 'thm_p', 'Old', 'old', 'inbox', 1, 1, 'local')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO peer_sessions (session_id, cmux_workspace_id, subject_id, updated_at) VALUES ('s-old', ?, 'sub_old', 1)`, wsA); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0012): %v", err)
	}
	defer s.Close()
	if list, err := s.ListCmuxBindings("sub_old"); err != nil || len(list) != 0 {
		t.Fatalf("bindings after migration = %+v, %v", list, err)
	}
	p, err := s.GetPeer("s-old")
	if err != nil || p.SubjectID != "sub_old" || p.SubjectSlug != "old" {
		t.Fatalf("peer after migration = %+v, %v", p, err)
	}
	// The CHECK constraints hold.
	if _, err := s.db.Exec(`INSERT INTO subject_cmux_bindings (cmux_workspace_id, subject_id, source, bound_at) VALUES (?, 'sub_old', 'manual', 1)`, wsB); err == nil {
		t.Fatal("unknown source accepted")
	}
}

// TestUpsertPeerSubjectSetWritesEmpty: SubjectSet makes the upsert write the
// subject as given, ” included; without it the merge keeps the stored one.
// SubjectStamp is the raw column, never the binding-first read value.
func TestUpsertPeerSubjectSetWritesEmpty(t *testing.T) {
	s := bindingStore(t)
	if err := s.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: wsA, SubjectID: "sub_a"}); err != nil {
		t.Fatal(err)
	}
	bind(t, s, wsA, "sub_b")
	if got, err := peerStamp(s, "s1"); err != nil || got != "sub_a" {
		t.Fatalf("stamp = %q, %v; want the raw sub_a (the read value is sub_b)", got, err)
	}
	if err := s.UpsertPeer(&pigtail.PeerSession{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := peerStamp(s, "s1"); got != "sub_a" {
		t.Fatalf("upsert without the flag and '' = %q, want the stored sub_a kept", got)
	}
	if err := s.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", SubjectSet: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := peerStamp(s, "s1"); got != "" {
		t.Fatalf("upsert with SubjectSet and '' = %q, want ''", got)
	}
	if _, err := peerStamp(s, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stamp of an unknown id = %v, want ErrNotFound", err)
	}
}

// TestEndedPeerKeepsStampedSubject: an ended session reads the subject stamped
// on its row, even when its tab is now bound elsewhere or no longer bound; an
// unended one still reads the live binding.
func TestEndedPeerKeepsStampedSubject(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	if err := s.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: wsA, SubjectID: "sub_a", SubjectSet: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.EndPeer("s1", time.UnixMilli(8).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCmuxBinding(wsA, "sub_a", "sub_b", "attach", time.UnixMilli(9).UTC()); err != nil {
		t.Fatal(err)
	}
	check := func(step string) {
		t.Helper()
		got, err := s.GetPeer("s1")
		if err != nil {
			t.Fatal(err)
		}
		ps, err := s.ListPeers(true)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range append(ps, *got) {
			if p.SessionID != "s1" {
				continue
			}
			if p.SubjectID != "sub_a" || p.SubjectSlug != "sub_a-slug" || p.SubjectShortTitle != "sub_a" || p.CmuxBound {
				t.Fatalf("%s: s1 = subject %q slug %q short %q bound %v; want sub_a, sub_a-slug, sub_a, false", step, p.SubjectID, p.SubjectSlug, p.SubjectShortTitle, p.CmuxBound)
			}
		}
	}
	check("tab rebound to sub_b")
	if err := s.ForgetCmuxWorkspace(wsA); err != nil {
		t.Fatal(err)
	}
	check("tab forgotten")

	// An unended row still reads the live binding first.
	bind(t, s, wsB, "sub_b")
	if err := s.UpsertPeer(&pigtail.PeerSession{SessionID: "s2", CmuxWorkspaceID: wsB, SubjectID: "sub_a", SubjectSet: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetPeer("s2"); p.SubjectID != "sub_b" || !p.CmuxBound {
		t.Fatalf("unended s2 = %q bound %v; want the live binding sub_b", p.SubjectID, p.CmuxBound)
	}
}

// peerStamp reads a row's raw stamp (GetPeer's SubjectStamp), never the
// binding-first SubjectID.
func peerStamp(s *Store, id string) (string, error) {
	p, err := s.GetPeer(id)
	if err != nil {
		return "", err
	}
	return p.SubjectStamp, nil
}

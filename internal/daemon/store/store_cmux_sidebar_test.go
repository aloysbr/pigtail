package store

import (
	"errors"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func getBinding(t *testing.T, s *Store, ws string) pigtail.SubjectCmuxBinding {
	t.Helper()
	b, err := s.GetCmuxBinding(ws)
	if err != nil {
		t.Fatal(err)
	}
	return *b
}

func TestListAllCmuxBindings(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsB, "sub_b")
	bind(t, s, wsA, "sub_a")
	all, err := s.ListAllCmuxBindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].CmuxWorkspaceID != wsA || all[1].CmuxWorkspaceID != wsB {
		t.Fatalf("got %+v", all)
	}
}

// The previous title is captured once, before pigtail's first rename: a
// second capture (pigtail's own title, read back) must not overwrite it.
func TestCaptureCmuxPrevOnlyBeforeFirstWrite(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	if err := s.CaptureCmuxPrev(wsA, CmuxFieldTitle, "Metric login", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCmuxApplied(wsA, CmuxFieldTitle, "Sidebar pigtail"); err != nil {
		t.Fatal(err)
	}
	if err := s.CaptureCmuxPrev(wsA, CmuxFieldTitle, "Sidebar pigtail", true); err != nil {
		t.Fatal(err)
	}
	if err := s.CaptureCmuxPrev(wsA, CmuxFieldDescription, "old desc", false); err != nil {
		t.Fatal(err)
	}
	b := getBinding(t, s, wsA)
	if b.PrevTitle != "Metric login" || !b.PrevTitleCustom || b.AppliedTitle != "Sidebar pigtail" || b.PrevDescription != "old desc" {
		t.Fatalf("got %+v", b)
	}
}

func TestSetCmuxOwnerAndRelease(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	_ = s.CaptureCmuxPrev(wsA, CmuxFieldTitle, "x", true)
	_ = s.SetCmuxApplied(wsA, CmuxFieldTitle, "t")
	_ = s.SetCmuxApplied(wsA, CmuxFieldDescription, "d")
	if err := s.SetCmuxOwner(wsA, CmuxFieldTitle, pigtail.CmuxOwnerUser); err != nil {
		t.Fatal(err)
	}
	if b := getBinding(t, s, wsA); b.TitleOwner != pigtail.CmuxOwnerUser || b.DescriptionOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("owners %+v", b)
	}
	if err := s.SetCmuxOwner(wsA, CmuxFieldTitle, "nobody"); err == nil {
		t.Fatal("unknown owner accepted")
	}
	if err := s.ReleaseCmuxText(wsA); err != nil {
		t.Fatal(err)
	}
	b := getBinding(t, s, wsA)
	if b.AppliedTitle != "" || b.AppliedDescription != "" || b.PrevTitle != "" || b.PrevTitleCustom {
		t.Fatalf("not released: %+v", b)
	}
	if err := s.SetCmuxApplied("00000000-0000-0000-0000-000000000000", CmuxFieldTitle, "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
}

// Retitle hands back only what the user had taken: a field pigtail still owns
// keeps its applied text, so the next pass does not re-capture pigtail's own
// title as the "previous" one.
func TestResetCmuxOwnersClearsOnlyTakenFields(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	_ = s.SetCmuxApplied(wsA, CmuxFieldTitle, "t")
	_ = s.SetCmuxApplied(wsA, CmuxFieldDescription, "d")
	_ = s.SetCmuxOwner(wsA, CmuxFieldTitle, pigtail.CmuxOwnerUser)
	if err := s.ResetCmuxOwners(wsA); err != nil {
		t.Fatal(err)
	}
	b := getBinding(t, s, wsA)
	if b.TitleOwner != pigtail.CmuxOwnerPigtail || b.AppliedTitle != "" || b.AppliedDescription != "d" {
		t.Fatalf("got %+v", b)
	}
}

func TestCmuxMissingTicksAndForget(t *testing.T) {
	s := bindingStore(t)
	bind(t, s, wsA, "sub_a")
	if err := s.SetCmuxMissingTicks(wsA, 1); err != nil {
		t.Fatal(err)
	}
	if b := getBinding(t, s, wsA); b.MissingTicks != 1 {
		t.Fatalf("ticks %d", b.MissingTicks)
	}
	if err := s.ForgetCmuxWorkspace(wsA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCmuxBinding(wsA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still bound: %v", err)
	}
}

package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// withLoop wires a sidebar loop stand-in showing ws into the fixture.
func (f *relayFixture) withLoop(ws ...pigtail.CmuxSidebarWorkspace) {
	f.rl.svc.CmuxSidebar = &fakeSidebarCtl{state: pigtail.CmuxSidebarState{Enabled: true, CmuxAvailable: true, Workspaces: ws}}
}

func (f *relayFixture) bindWS(t *testing.T, ws, sub string) {
	t.Helper()
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: sub, Source: "attach", BoundAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
}

// boundPersoPeer seeds a perso subject "Relance cmux sûre", a session of
// scope sc in cmuxWS whose snapshot title is "ancien", and binds cmuxWS to
// the subject.
func boundPersoPeer(f *relayFixture, t *testing.T, sc string) pigtail.Subject {
	t.Helper()
	sub := f.seedScopedSubjectWorktree(t, perso, "Perso/Areas/relance", "Relance cmux sûre", "/wt/relance")
	f.peers(t, pigtail.PeerSession{SessionID: "s-bound", CmuxWorkspaceID: cmuxWS, CmuxTitle: "ancien", Name: "x-b", Scope: sc, CellID: "mt"})
	f.bindWS(t, cmuxWS, sub.ID)
	return sub
}

// An unbound tab renamed by hand "#201 relance": the CLI snapshot lost the
// "#"; the loop's live title gives it back, on the label and on cmux_title.
func TestPeersLabelLiveTitleUnbound(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t, pigtail.PeerSession{SessionID: "s-un", CmuxWorkspaceID: cmuxWS2, CmuxTitle: "201 relance", Name: "work-u", Scope: "gx", CellID: "mt"})
	if p := peersAs(t, f, perso, "GET", nil)["s-un"]; !strings.HasPrefix(p.Label, "201 relance · ") {
		t.Fatalf("no loop: snapshot title: %+v", p)
	}
	f.withLoop(pigtail.CmuxSidebarWorkspace{CmuxWorkspaceID: cmuxWS2, Title: "#201 relance"})
	p := peersAs(t, f, perso, "GET", nil)["s-un"]
	if !strings.HasPrefix(p.Label, "#201 relance · ") || p.CmuxTitle != "#201 relance" {
		t.Fatalf("live title: %+v", p)
	}
}

// Bound: the label is the short title, whatever the tab shows right now.
func TestPeersLabelBoundIsShortTitle(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	sub := boundPersoPeer(f, t, "gx")
	f.withLoop(pigtail.CmuxSidebarWorkspace{CmuxWorkspaceID: cmuxWS, Title: "fait", TitleOwner: "pigtail",
		Subject: &pigtail.CmuxSidebarSubject{ID: sub.ID, ShortTitle: "Relance cmux sûre", Scope: "perso"}})
	p := peersAs(t, f, perso, "GET", nil)["s-bound"]
	if !strings.HasPrefix(p.Label, "Relance cmux sûre · ") || p.CmuxTitle != "fait" {
		t.Fatalf("bound: %+v", p)
	}
}

// The same bound perso peer seen by work and none: neither its title, nor
// its short title, nor the link.
func TestPeersLabelRedactedKeepsNoTitle(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	sub := boundPersoPeer(f, t, "gx")
	f.withLoop(pigtail.CmuxSidebarWorkspace{CmuxWorkspaceID: cmuxWS, Title: "fait", TitleOwner: "pigtail",
		Subject: &pigtail.CmuxSidebarSubject{ID: sub.ID, ShortTitle: "Relance cmux sûre", Scope: "perso"}})
	for _, c := range []caller{work, none} {
		rec := doAs(t, f.r, c, "GET", "/peers", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", c, rec.Code)
		}
		p := peersAs(t, f, c, "GET", nil)["s-bound"]
		if p.SessionID == "" || strings.Contains(p.Label, "fait") || strings.Contains(p.Label, "Relance") || strings.Contains(p.Label, "ancien") || p.CmuxTitle != "" {
			t.Fatalf("%s: %+v", c, p)
		}
		for _, leak := range []string{"subject_short_title", "cmux_bound", "Relance", "fait"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Fatalf("%s: %q in %s", c, leak, rec.Body)
			}
		}
	}
}

// The loop binds the workspace to another subject than the peer's: its live
// title is not put on the peer.
func TestPeersLabelLoopBindsOtherSubject(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	boundPersoPeer(f, t, "gx")
	f.withLoop(pigtail.CmuxSidebarWorkspace{CmuxWorkspaceID: cmuxWS, Title: "Autre sujet", TitleOwner: "pigtail",
		Subject: &pigtail.CmuxSidebarSubject{ID: "sub_other", ShortTitle: "Autre sujet", Scope: "perso"}})
	if p := peersAs(t, f, perso, "GET", nil)["s-bound"]; p.CmuxTitle != "ancien" || strings.Contains(p.Label, "Autre") {
		t.Fatalf("live title of another subject applied: %+v", p)
	}
}

// The store binds the tab to a perso subject while the loop still shows it
// unbound (its pass is behind): the subject-ID check of liveTitle lets that
// title through, so only the TitleRedacted marker keeps it off the work and
// none callers' view.
func TestPeersLiveTitleNeverLandsOnRedactedPeer(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	boundPersoPeer(f, t, "gx")
	f.withLoop(pigtail.CmuxSidebarWorkspace{CmuxWorkspaceID: cmuxWS, Title: "Relance cmux sûre"})
	for _, c := range []caller{work, none} {
		p := peersAs(t, f, c, "GET", nil)["s-bound"]
		if p.CmuxTitle != "" || strings.Contains(p.Label, "Relance") || strings.Contains(p.Label, "ancien") {
			t.Fatalf("%s: live title landed on a redacted peer: %+v", c, p)
		}
	}
}

// An address written as the tab shows it ("#201 relance") resolves through
// the live title; without the loop, the snapshot lost the "#".
func TestSendResolvesShownNameWithLiveTitle(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	f.peers(t,
		pigtail.PeerSession{SessionID: "s-un", CmuxWorkspaceID: cmuxWS2, CmuxTitle: "201 relance", Name: "perso-u", Scope: "perso", CellID: "mp"},
		pigtail.PeerSession{SessionID: "s-from", Name: "perso-f", Scope: "perso", CellID: "mp"},
	)
	req := pigtail.SendMessageRequest{From: "s-from", To: "#201 relance", Body: "x"}
	if rec, _ := f.sendAs(t, perso, req); rec.Code != http.StatusNotFound || errCode(rec) != string(pigtail.ErrAddressUnresolved) {
		t.Fatalf("no loop: %d %s", rec.Code, rec.Body)
	}
	f.withLoop(pigtail.CmuxSidebarWorkspace{CmuxWorkspaceID: cmuxWS2, Title: "#201 relance"})
	rec, out := f.sendAs(t, perso, req)
	if rec.Code != http.StatusCreated || out.To.SessionID != "s-un" || out.MatchedBy != "name" || !strings.HasPrefix(out.To.Label, "#201 relance · ") {
		t.Fatalf("live title: %d %+v %s", rec.Code, out, rec.Body)
	}
}

// A bound session answers to its short title.
func TestSendResolvesBoundByShortTitle(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	boundPersoPeer(f, t, "perso")
	f.peers(t, pigtail.PeerSession{SessionID: "s-from", Name: "perso-f", Scope: "perso", CellID: "mp"})
	rec, out := f.sendAs(t, perso, pigtail.SendMessageRequest{From: "s-from", To: "Relance cmux sûre", Body: "x"})
	if rec.Code != http.StatusCreated || out.To.SessionID != "s-bound" || out.MatchedBy != "name" {
		t.Fatalf("%d %+v %s", rec.Code, out, rec.Body)
	}
}

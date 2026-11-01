package handlers

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const stampWS = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"

// rawStamp reads peer_sessions.subject_id through a side connection: the
// column itself, not the binding-first read value.
func rawStamp(t *testing.T, path, session string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id string
	if err := db.QueryRow(`SELECT subject_id FROM peer_sessions WHERE session_id = ?`, session).Scan(&id); err != nil {
		t.Fatalf("raw stamp of %s: %v", session, err)
	}
	return id
}

// TestPeerStampFollowsLiveBinding: a
// proven-alive upsert stamps the subject its cmux workspace is bound to (the
// worktree deduction, as is, when unbound); a row not proven alive keeps a
// non-empty stamp.
func TestPeerStampFollowsLiveBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pigtail.db")
	f := newRelayFixtureAt(t, path)
	alive := map[string]bool{}
	f.rl.cfg.Wake = &WakeDeps{SessionAlive: func(_ string, _ int, session string) bool { return alive[session] }}

	subA := f.seedSubjectWorktree(t, "alpha", "/wt/a")
	subB := f.seedSubjectWorktree(t, "bravo", "/wt/b")
	subC := f.seedSubjectWorktree(t, "charlie", "/wt/c")
	subD := f.seedSubjectWorktree(t, "delta", "/wt/d")
	wsD := "ws_" + subD.Slug

	bind := func(sub string) {
		if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: stampWS, SubjectID: sub, Source: pigtail.CmuxSourceAttach, BoundAt: time.UnixMilli(5).UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	bind(subB.ID)
	// s2 and s3 exist before the rebind, unended.
	if err := f.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s2", CmuxWorkspaceID: stampWS, SubjectID: subB.ID, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertPeer(&pigtail.PeerSession{SessionID: "s3", CmuxWorkspaceID: stampWS, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	check := func(step, session, want string) {
		t.Helper()
		if got := rawStamp(t, path, session); got != want {
			t.Fatalf("%s: raw %s = %q, want %q", step, session, got, want)
		}
	}

	// Hook upsert on a bound workspace: the binding wins over the incoming value.
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Source: "hook", CmuxWorkspaceID: stampWS, SubjectID: subA.ID})
	check("hook on bound", "s1", subB.ID)

	// Rebind: a snapshot of the alive s1 follows it.
	if err := f.st.ReplaceCmuxBinding(stampWS, subB.ID, subC.ID, "attach", time.UnixMilli(9).UTC()); err != nil {
		t.Fatal(err)
	}
	alive["s1"] = true
	if err := f.rl.RecordSnapshot([]pigtail.PeerSession{{SessionID: "s1", PID: 11, CmuxWorkspaceID: stampWS}}); err != nil {
		t.Fatal(err)
	}
	check("snapshot alive after rebind", "s1", subC.ID)

	// Not alive: the stored stamp stays, whatever comes in.
	if err := f.rl.RecordSnapshot([]pigtail.PeerSession{{SessionID: "s2", PID: 22, CmuxWorkspaceID: stampWS}}); err != nil {
		t.Fatal(err)
	}
	check("snapshot not alive", "s2", subB.ID)
	if err := f.rl.RecordSnapshot([]pigtail.PeerSession{{SessionID: "s2", PID: 22, CmuxWorkspaceID: stampWS, WorkspaceID: wsD}}); err != nil {
		t.Fatal(err)
	}
	check("not alive with a worktree deduction", "s2", subB.ID)

	// Not alive, raw '': today's fill of an empty stamp is kept.
	if err := f.rl.RecordSnapshot([]pigtail.PeerSession{{SessionID: "s3", PID: 33, CmuxWorkspaceID: stampWS, WorkspaceID: wsD}}); err != nil {
		t.Fatal(err)
	}
	check("not alive, empty stamp filled", "s3", subD.ID)

	// Alive with no cmux workspace: the deduction is written (SubjectSet).
	f.peers(t, pigtail.PeerSession{SessionID: "s4", Source: "hook", WorkspaceID: wsD})
	check("hook without cmux workspace", "s4", subD.ID)

	// Unbind: the next sign of life of s1 (no worktree) clears the stamp.
	if err := f.st.DeleteCmuxBinding(subC.ID, stampWS); err != nil {
		t.Fatal(err)
	}
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Source: "hook", CmuxWorkspaceID: stampWS})
	check("unbind then hook", "s1", "")
}

// TestPauseUndoKeepsStamp (Spec 6): a pause taken back re-upserts the ended
// row it read; the row reads its stamp, so the rebind of its tab since the
// pause does not overwrite it.
func TestPauseUndoKeepsStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pigtail.db")
	f := newRelayFixtureAt(t, path)
	f.rl.cfg.Wake = &WakeDeps{SessionAlive: func(string, int, string) bool { return true }}
	subA := f.seedSubjectWorktree(t, "alpha", "/wt/a")
	subB := f.seedSubjectWorktree(t, "bravo", "/wt/b")
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: stampWS, SubjectID: subA.ID, Source: pigtail.CmuxSourceAttach, BoundAt: time.UnixMilli(5).UTC()}); err != nil {
		t.Fatal(err)
	}
	f.peers(t, pigtail.PeerSession{SessionID: "s1", Source: "hook", PID: 7, CmuxWorkspaceID: stampWS})
	if got := rawStamp(t, path, "s1"); got != subA.ID {
		t.Fatalf("stamped = %q, want %q", got, subA.ID)
	}
	row := pigtail.SessionPause{SessionID: "s1", PID: 7, State: pigtail.PausePaused, Since: f.now}
	if err := f.st.BeginPause(row); err != nil {
		t.Fatal(err)
	}
	if err := f.st.EndPeer("s1", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReplaceCmuxBinding(stampWS, subA.ID, subB.ID, "attach", time.UnixMilli(9).UTC()); err != nil {
		t.Fatal(err)
	}
	f.rl.pauseUndo(row, "test", false)
	if p, err := f.st.GetPeer("s1"); err != nil || p.EndedAt != nil {
		t.Fatalf("peer after undo = %+v, %v; want revived", p, err)
	}
	if got := rawStamp(t, path, "s1"); got != subA.ID {
		t.Fatalf("raw stamp after pauseUndo = %q, want %q kept", got, subA.ID)
	}
}

// TestEndedRowRebindToUnreadableSubjectHidesTitle: an ended work row
// keeps its work stamp, but its tab is now linked to a perso subject whose
// short title is the stored cmux_title. The row reads the stamp (CmuxBound
// false), so the redaction must still blank the title for a work caller.
func TestEndedRowRebindToUnreadableSubjectHidesTitle(t *testing.T) {
	f := newRelayFixture(t)
	f.rl.cfg.Wake = &WakeDeps{SessionAlive: func(string, int, string) bool { return true }}
	workSub := f.seedScopedSubjectWorktree(t, work, "Globex/Projects/relay", "Work relay work", "/wt/work-relay")
	persoSub := f.seedScopedSubjectWorktree(t, perso, "Perso/Areas/garden", "Garden glazings", "/wt/feat-glazings")
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: stampWS, SubjectID: workSub.ID, Source: pigtail.CmuxSourceAttach, BoundAt: time.UnixMilli(5).UTC()}); err != nil {
		t.Fatal(err)
	}
	const leak = "leak-perso-subject-title"
	f.peers(t, pigtail.PeerSession{SessionID: "s-end", Source: "hook", Name: "work-e", Scope: "gx", CellID: "mt", CmuxWorkspaceID: stampWS, CmuxTitle: leak})
	if err := f.st.EndPeer("s-end", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReplaceCmuxBinding(stampWS, workSub.ID, persoSub.ID, "attach", time.UnixMilli(9).UTC()); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range allPeersAs(t, f, work) {
		if p.SessionID != "s-end" {
			continue
		}
		found = true
		if p.SubjectID != workSub.ID {
			t.Fatalf("ended row must read its work stamp, got %q", p.SubjectID)
		}
		if p.CmuxTitle != "" || strings.Contains(p.Label, leak) {
			t.Errorf("perso subject's title leaks to a work caller: %+v", p)
		}
	}
	if !found {
		t.Fatal("ended work row not listed")
	}
	if got := allPeersAs(t, f, perso); len(got) == 0 {
		t.Fatal("master view empty")
	}
}

// TestEndedRowStampedUnreadableRebindToReadableHidesTitle: a work
// session ran in a tab linked to a perso subject X, so it is stamped X and its
// stored cmux_title is X's short title. It ends; the tab is linked to a work
// subject T. The row reads its stamp X (unreadable) in a tab bound elsewhere:
// the stored title must not reach the label of a work caller.
func TestEndedRowStampedUnreadableRebindToReadableHidesTitle(t *testing.T) {
	f := newRelayFixture(t)
	f.rl.cfg.Wake = &WakeDeps{SessionAlive: func(string, int, string) bool { return true }}
	workSub := f.seedScopedSubjectWorktree(t, work, "Globex/Projects/relay", "Work relay work", "/wt/work-relay")
	persoSub := f.seedScopedSubjectWorktree(t, perso, "Perso/Areas/garden", "Garden glazings", "/wt/feat-glazings")
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: stampWS, SubjectID: persoSub.ID, Source: pigtail.CmuxSourceAttach, BoundAt: time.UnixMilli(5).UTC()}); err != nil {
		t.Fatal(err)
	}
	const leak = "leak-perso-subject-title"
	f.peers(t, pigtail.PeerSession{SessionID: "s-end", Source: "hook", Name: "work-e", Scope: "gx", CellID: "mt", CmuxWorkspaceID: stampWS, CmuxTitle: leak})
	if err := f.st.EndPeer("s-end", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReplaceCmuxBinding(stampWS, persoSub.ID, workSub.ID, "attach", time.UnixMilli(9).UTC()); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range allPeersAs(t, f, work) {
		if p.SessionID != "s-end" {
			continue
		}
		found = true
		if p.CmuxTitle != "" || strings.Contains(p.Label, leak) {
			t.Errorf("perso subject's title leaks to a work caller: %+v", p)
		}
	}
	if !found {
		t.Fatal("ended work row not listed")
	}
}

func allPeersAs(t *testing.T, f *relayFixture, c caller) []pigtail.PeerSession {
	t.Helper()
	rec := doAs(t, f.r, c, "GET", "/peers?all=1", nil)
	if rec.Code != 200 {
		t.Fatalf("GET /peers?all=1 = %d %s", rec.Code, rec.Body.String())
	}
	var ps []pigtail.PeerSession
	if err := json.Unmarshal(rec.Body.Bytes(), &ps); err != nil {
		t.Fatal(err)
	}
	return ps
}

// TestPeerStampFallbacksAndFaults covers what the main test leaves implicit:
// the stored workspace and config dir stand in for a missing incoming one,
// a malformed workspace never clears a stamp, and a live row without a
// workspace is unlinked (its worktree deduction is written as is).
func TestPeerStampFallbacksAndFaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pigtail.db")
	f := newRelayFixtureAt(t, path)
	var gotCfg []string
	f.rl.cfg.Wake = &WakeDeps{SessionAlive: func(cfg string, _ int, _ string) bool {
		gotCfg = append(gotCfg, cfg)
		return cfg == "/cfg/stored"
	}}
	subA := f.seedSubjectWorktree(t, "alpha", "/wt/a")
	subB := f.seedSubjectWorktree(t, "bravo", "/wt/b")
	subD := f.seedSubjectWorktree(t, "delta", "/wt/d")
	wsD := "ws_" + subD.Slug
	if err := f.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: stampWS, SubjectID: subB.ID, Source: pigtail.CmuxSourceAttach, BoundAt: time.UnixMilli(5).UTC()}); err != nil {
		t.Fatal(err)
	}
	check := func(step, session, want string) {
		t.Helper()
		if got := rawStamp(t, path, session); got != want {
			t.Fatalf("%s: raw %s = %q, want %q", step, session, got, want)
		}
	}

	// (a) A live upsert without a workspace falls back on the stored one.
	if err := f.st.UpsertPeer(&pigtail.PeerSession{SessionID: "sa", CmuxWorkspaceID: stampWS, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	f.peers(t, pigtail.PeerSession{SessionID: "sa", Source: "hook", SubjectID: subA.ID, WorkspaceID: wsD})
	check("stored workspace", "sa", subB.ID)

	// (b) A non-master caller's config dir is blanked: the stored one is what
	// the liveness probe receives, and its verdict decides the stamp.
	if err := f.st.UpsertPeer(&pigtail.PeerSession{SessionID: "sb", Scope: "gx", ConfigDir: "/cfg/stored", CmuxWorkspaceID: stampWS, SubjectID: subA.ID, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	rec := doAs(t, f.r, work, "POST", "/peers", pigtail.PeersRequest{Peers: []pigtail.PeerSession{{SessionID: "sb", Source: "snapshot", PID: 42, ConfigDir: "/cfg/other"}}})
	if rec.Code != 200 {
		t.Fatalf("POST /peers = %d %s", rec.Code, rec.Body.String())
	}
	if len(gotCfg) == 0 {
		t.Fatal("the liveness probe never ran")
	}
	for _, c := range gotCfg {
		if c != "/cfg/stored" {
			t.Fatalf("liveness probe received %q, want only the stored /cfg/stored", gotCfg)
		}
	}
	check("alive through the stored config dir", "sb", subB.ID)

	// (c) A malformed workspace is a fault: the stored stamp is kept.
	if err := f.st.UpsertPeer(&pigtail.PeerSession{SessionID: "sc", SubjectID: subA.ID, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	f.peers(t, pigtail.PeerSession{SessionID: "sc", Source: "hook", CmuxWorkspaceID: "not-a-workspace-id", WorkspaceID: wsD})
	check("malformed workspace", "sc", subA.ID)

	// (d) Live without a workspace is unlinked: the deduction replaces a
	// stored stamp (a fault would have kept sub A), and no worktree clears it.
	for _, id := range []string{"sd", "se"} {
		if err := f.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, SubjectID: subA.ID, UpdatedAt: f.now}); err != nil {
			t.Fatal(err)
		}
	}
	check("pre-stamped", "sd", subA.ID)
	f.peers(t, pigtail.PeerSession{SessionID: "sd", Source: "hook", WorkspaceID: wsD})
	check("live without workspace, with a worktree", "sd", subD.ID)
	f.peers(t, pigtail.PeerSession{SessionID: "se", Source: "hook"})
	check("live without workspace, without a worktree", "se", "")
}

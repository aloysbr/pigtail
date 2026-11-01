package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The Standby row of a session (sub_x000000000000041, D4, D24): set, the
// compaction state recorded, re-set from scratch, listed, cleared.
func TestSessionStandbySetReadClear(t *testing.T) {
	st := openMem(t)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	if sb, err := st.GetStandby("s1"); err != nil || sb != nil {
		t.Fatalf("before set = %+v, %v", sb, err)
	}
	if err := st.SetStandby("s1", t0); err != nil {
		t.Fatal(err)
	}
	sb, err := st.GetStandby("s1")
	if err != nil || sb == nil || sb.SessionID != "s1" || !sb.Since.Equal(t0) || sb.CompactSentAt != nil || sb.CompactTries != 0 {
		t.Fatalf("after set = %+v, %v", sb, err)
	}
	if err := st.MarkStandbyCompactSent("s1", t0.Add(time.Minute), 100, 40); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkStandbyCompactSent("s1", t0.Add(2*time.Minute), 200, 41); err != nil {
		t.Fatal(err)
	}
	sb, _ = st.GetStandby("s1")
	if sb.CompactTries != 2 || sb.CompactStatusAt != 200 || sb.CompactPID != 41 ||
		sb.CompactSentAt == nil || !sb.CompactSentAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("after two sends = %+v", sb)
	}
	if err := st.MarkStandbyCompacted("s1", t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkStandbyCompactFailed("s1", t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	sb, _ = st.GetStandby("s1")
	if sb.CompactedAt == nil || !sb.CompactedAt.Equal(t0.Add(3*time.Minute)) ||
		sb.CompactFailedAt == nil || !sb.CompactFailedAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("proof and failure = %+v", sb)
	}
	// A new /standby starts over: no compaction state, no missing tick.
	if err := st.SetStandby("s1", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sb, _ = st.GetStandby("s1")
	if sb.CompactSentAt != nil || sb.CompactedAt != nil || sb.CompactFailedAt != nil || sb.CompactTries != 0 ||
		sb.CompactStatusAt != 0 || sb.CompactPID != 0 || sb.MissingTicks != 0 || !sb.Since.Equal(t0.Add(time.Hour)) {
		t.Fatalf("after re-set = %+v", sb)
	}
	if err := st.SetStandby("s2", t0); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListStandby()
	if err != nil || len(all) != 2 || all[0].SessionID != "s1" || all[1].SessionID != "s2" {
		t.Fatalf("list = %+v, %v", all, err)
	}
	if gone, err := st.ClearStandby("s1", t0.Add(2*time.Hour)); err != nil || !gone {
		t.Fatalf("clear = %v, %v", gone, err)
	}
	if gone, err := st.ClearStandby("s1", t0.Add(2*time.Hour)); err != nil || gone {
		t.Fatalf("second clear = %v, %v", gone, err)
	}
	if sb, err := st.GetStandby("s1"); err != nil || sb != nil {
		t.Fatalf("after clear = %+v, %v", sb, err)
	}
	if err := st.SetStandby("", t0); err == nil {
		t.Fatal("a Standby without session id was accepted")
	}
}

// D24: a relaunch resumes the same session id, so the end of a session keeps
// its Standby row (the waits still go); the resumed registration finds it.
func TestEndPeerKeepsStandby(t *testing.T) {
	st := openMem(t)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionWait("s1", pigtail.SessionWait{Type: "review", Source: pigtail.WaitSourceDeclared, Since: t0}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStandby("s1", t0); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkStandbyCompacted("s1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.EndPeer("s1", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	sb, err := st.GetStandby("s1")
	if err != nil || sb == nil || sb.CompactedAt == nil || !sb.CompactedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after EndPeer = %+v, %v", sb, err)
	}
	if ws, _ := st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("EndPeer kept the waits: %+v", ws)
	}
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", UpdatedAt: t0.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if sb, err := st.GetStandby("s1"); err != nil || sb == nil {
		t.Fatalf("after resume = %+v, %v", sb, err)
	}
}

// D24: another conversation registered on a surface ends the Standby of the
// sessions it replaces there (surface compared without case), never its own.
func TestEndStandbyReplacedOnSurface(t *testing.T) {
	st := openMem(t)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for _, p := range []pigtail.PeerSession{
		{SessionID: "s1", CmuxSurfaceID: "abcd-ef", UpdatedAt: t0},
		{SessionID: "s2", CmuxSurfaceID: "ABCD-EF", UpdatedAt: t0},
		{SessionID: "s3", CmuxSurfaceID: "OTHER", UpdatedAt: t0},
	} {
		if err := st.UpsertPeer(&p); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"s1", "s2", "s3"} {
		if err := st.SetStandby(s, t0); err != nil {
			t.Fatal(err)
		}
	}
	gone, err := st.EndStandbyReplacedOnSurface("ABCD-EF", "s2", t0.Add(time.Minute))
	if err != nil || len(gone) != 1 || gone[0] != "s1" {
		t.Fatalf("ended = %v, %v; want [s1]", gone, err)
	}
	for s, want := range map[string]bool{"s1": false, "s2": true, "s3": true} {
		if sb, _ := st.GetStandby(s); (sb != nil) != want {
			t.Errorf("%s in Standby = %v, want %v", s, sb != nil, want)
		}
	}
	if gone, err := st.EndStandbyReplacedOnSurface("ABCD-EF", "s2", t0); err != nil || len(gone) != 0 {
		t.Fatalf("second call = %v, %v", gone, err)
	}
}

// Task 16 (relecture 4): the rows with the workspace their session registered
// on, in capitals, and their count of missed listings.
func TestListStandbyWorkspaces(t *testing.T) {
	st := openMem(t)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", CmuxWorkspaceID: "ws-a", UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2"} { // s2 never registered
		if err := st.SetStandby(id, t0); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListStandbyWorkspaces()
	if err != nil || len(got) != 2 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if got[0].SessionID != "s1" || got[0].CmuxWorkspaceID != "WS-A" || got[0].MissingTicks != 0 {
		t.Fatalf("s1 = %+v", got[0])
	}
	if got[1].SessionID != "s2" || got[1].CmuxWorkspaceID != "" {
		t.Fatalf("a row without registration has no workspace: %+v", got[1])
	}
	if err := st.SetStandbyMissingTicks("s1", 1); err != nil {
		t.Fatal(err)
	}
	if sb, _ := st.GetStandby("s1"); sb.MissingTicks != 1 {
		t.Fatalf("ticks = %d", sb.MissingTicks)
	}
	if got, _ := st.ListStandbyWorkspaces(); got[0].MissingTicks != 1 {
		t.Fatalf("ticks in the list = %+v", got[0])
	}
	if err := st.SetStandbyMissingTicks("nobody", 1); err == nil {
		t.Fatal("ticks on a missing row accepted")
	}
	if err := st.SetStandby("s1", t0.Add(time.Hour)); err != nil { // re-set starts over
		t.Fatal(err)
	}
	if got, _ := st.ListStandbyWorkspaces(); got[0].MissingTicks != 0 {
		t.Fatalf("SetStandby kept the ticks: %+v", got[0])
	}
}

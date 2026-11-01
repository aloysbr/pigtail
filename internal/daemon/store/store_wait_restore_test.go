package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestRestoreEndedWaitReopensSameRecord(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "s1", declared("arbitrage", "D1", tw0), WaitMeta{})
	mustEnd(t, st, "s1", tw0.Add(time.Minute))
	got, err := st.RestoreEndedWaits("s1")
	if err != nil || len(got) != 1 {
		t.Fatalf("RestoreEndedWaits = %v, %v", got, err)
	}
	if got[0].Type != "arbitrage" || got[0].Reason != "D1" || !got[0].Since.Equal(tw0) {
		t.Fatalf("restored wait = %+v", got[0])
	}
	r := mustGet(t, st, a.OpenedID)
	if r.State != pigtail.WaitStateOpen || r.ClosedAt != nil {
		t.Fatalf("record after restore = %+v", r)
	}
	wantClosedBy(t, st, a.OpenedID, "", "after restore")
	ws, _ := st.SessionWaits("s1")
	if len(ws) != 1 || ws[0].Type != "arbitrage" || ws[0].Source != pigtail.WaitSourceDeclared {
		t.Fatalf("session_waits = %+v", ws)
	}
	if ids := openIDs(t, st, "s1"); len(ids) != 1 || ids[0] != a.OpenedID {
		t.Fatalf("open ids = %v, want the same record", ids)
	}
}

func TestRestoreSkipsPromptClearedAndWrapped(t *testing.T) {
	st := openMem(t)
	mustSet(t, st, "p", declared("review", "PR", tw0), WaitMeta{})
	if _, err := st.ClearSessionWaits("p"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, st, "w", declared("wrapped", "", tw0), WaitMeta{})
	mustSet(t, st, "d", declared("done", "", tw0), WaitMeta{})
	mustSet(t, st, "a", auto("permission", "Bash", tw0), WaitMeta{})
	for _, s := range []string{"w", "d", "a"} {
		mustEnd(t, st, s, tw0.Add(time.Minute))
	}
	for _, s := range []string{"p", "w", "d", "a"} {
		got, err := st.RestoreEndedWaits(s)
		if err != nil || len(got) != 0 {
			t.Fatalf("RestoreEndedWaits(%s) = %v, %v; want nothing", s, got, err)
		}
		if ws, _ := st.SessionWaits(s); len(ws) != 0 {
			t.Fatalf("%s session_waits = %+v", s, ws)
		}
	}
}

func TestRestoreIdempotent(t *testing.T) {
	st := openMem(t)
	mustSet(t, st, "s1", declared("review", "PR", tw0), WaitMeta{})
	mustEnd(t, st, "s1", tw0.Add(time.Minute))
	if got, err := st.RestoreEndedWaits("s1"); err != nil || len(got) != 1 {
		t.Fatalf("first = %v, %v", got, err)
	}
	if got, err := st.RestoreEndedWaits("s1"); err != nil || len(got) != 0 {
		t.Fatalf("second = %v, %v; want nothing", got, err)
	}
	if ws, _ := st.SessionWaits("s1"); len(ws) != 1 {
		t.Fatalf("session_waits = %+v", ws)
	}
}

func TestRestoreSubjectOwnedRowOnly(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	mustEnd(t, st, "A", tw0.Add(time.Minute))
	if ws, _ := st.SessionWaits("A"); len(ws) != 0 {
		t.Fatalf("row survived the end: %+v", ws)
	}
	got, err := st.RestoreEndedWaits("A")
	if err != nil || len(got) != 1 {
		t.Fatalf("RestoreEndedWaits = %v, %v", got, err)
	}
	if ids := openIDs(t, st, "A"); len(ids) != 1 || ids[0] != a.OpenedID {
		t.Fatalf("open ids = %v", ids)
	}
	if ws, _ := st.SessionWaits("A"); len(ws) != 1 || ws[0].Type != "arbitrage" {
		t.Fatalf("session_waits = %+v", ws)
	}
}

func TestRestoreSkipsWhenOwnerReopenedElsewhere(t *testing.T) {
	st := openMem(t)
	c := mustSet(t, st, "C", declared("blocked", "x", tw0), WaitMeta{})
	mustEnd(t, st, "C", tw0.Add(time.Minute))
	// the owner declares again before the old record is restored: its open
	// record and its row are the truth, the closed one stays closed.
	mustSet(t, st, "C", declared("review", "new", tw0.Add(2*time.Minute)), WaitMeta{})
	got, err := st.RestoreEndedWaits("C")
	if err != nil || len(got) != 0 {
		t.Fatalf("RestoreEndedWaits = %v, %v; want nothing", got, err)
	}
	if r := mustGet(t, st, c.OpenedID); r.State != pigtail.WaitStateCleared {
		t.Fatalf("old record reopened: %+v", r)
	}
	if ids := openIDs(t, st, "C"); len(ids) != 1 || ids[0] == c.OpenedID {
		t.Fatalf("open ids = %v", ids)
	}
	ws, _ := st.SessionWaits("C")
	if len(ws) != 1 || ws[0].Reason != "new" {
		t.Fatalf("session_waits = %+v", ws)
	}
}

// The done command's clear (keep=subject) removes the row of a live session and
// keeps the subject's record open with closed_by NULL: a later snapshot must
// not give the row back (it would undo the clear within 30 s).
func TestRestoreIgnoresPromptKeepSubjectClear(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	if _, _, _, err := st.ClearSessionWaitsKeepingSubject("A"); err != nil {
		t.Fatal(err)
	}
	if ids := openIDs(t, st, "A"); len(ids) != 1 || ids[0] != a.OpenedID {
		t.Fatalf("record not kept open: %v", ids)
	}
	wantClosedBy(t, st, a.OpenedID, "", "keep=subject clear")
	got, err := st.RestoreEndedWaits("A")
	if err != nil || len(got) != 0 {
		t.Fatalf("RestoreEndedWaits = %v, %v; want nothing", got, err)
	}
	if ws, _ := st.SessionWaits("A"); len(ws) != 0 {
		t.Fatalf("row came back on a live session: %+v", ws)
	}
}

// A session end keeping a subject-owned record open stamps it session_end
// (state stays open); the resume gives the row back and resets closed_by.
func TestSessionEndKeepingSubjectRecordStampsThenRestoreResets(t *testing.T) {
	for _, path := range []string{"end", "gone"} {
		st := openMem(t)
		a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
		if path == "end" {
			mustEnd(t, st, "A", tw0.Add(time.Minute))
		} else if _, _, _, err := st.ClearSessionWaitsClosing("A", true); err != nil {
			t.Fatal(err)
		}
		if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen {
			t.Fatalf("%s: record state = %s, want open", path, r.State)
		}
		wantClosedBy(t, st, a.OpenedID, "session_end", path+" keeping the subject record")
		got, err := st.RestoreEndedWaits("A")
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: RestoreEndedWaits = %v, %v", path, got, err)
		}
		wantClosedBy(t, st, a.OpenedID, "", path+" after restore")
		if got, _ := st.RestoreEndedWaits("A"); len(got) != 0 {
			t.Fatalf("%s: second restore = %v", path, got)
		}
		if ws, _ := st.SessionWaits("A"); len(ws) != 1 {
			t.Fatalf("%s: session_waits = %+v", path, ws)
		}
	}
}

// D7 (the user): a `working` wait is never restored — its background job died
// with the restart, so the resumed session is closable after an idle hour.
// Both branches: the record cleared at the end (own owner) and the one its
// subject keeps open.
func TestRestoreNeverRestoresWorking(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta WaitMeta
	}{{"cleared record", WaitMeta{}}, {"subject-kept record", subjS}} {
		st := openMem(t)
		mustSet(t, st, "W", declared("working", "job", tw0), tc.meta)
		mustEnd(t, st, "W", tw0.Add(time.Minute))
		got, err := st.RestoreEndedWaits("W")
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: RestoreEndedWaits = %v, %v; want nothing", tc.name, got, err)
		}
		if ws, _ := st.SessionWaits("W"); len(ws) != 0 {
			t.Fatalf("%s: session_waits = %+v", tc.name, ws)
		}
	}
}

// Restore: the done command's prompt clears a subject's review with keep=subject
// (record open, closed_by NULL), the command then declares wrapped (or a bare
// done): the record stays open typed review, the row reads wrapped/done. A
// later session end must not stamp the record session_end — a resume would
// bring back a `review` row on a finished session.
func TestRestoreNeverBringsBackReviewAfterWrapOrDone(t *testing.T) {
	for _, typ := range []string{"wrapped", "done"} {
		for _, path := range []string{"end", "gone"} {
			st := openMem(t)
			a := mustSet(t, st, "A", declared("review", "PR", tw0), subjS)
			if _, _, _, err := st.ClearSessionWaitsKeepingSubject("A"); err != nil {
				t.Fatal(err)
			}
			mustSet(t, st, "A", declared(typ, "", tw0.Add(time.Minute)), subjS)
			if ids := openIDs(t, st, "A"); len(ids) != 1 || ids[0] != a.OpenedID {
				t.Fatalf("%s/%s: record not kept open: %v", typ, path, ids)
			}
			if path == "end" {
				mustEnd(t, st, "A", tw0.Add(2*time.Minute))
			} else if _, _, _, err := st.ClearSessionWaitsClosing("A", true); err != nil {
				t.Fatal(err)
			}
			wantClosedBy(t, st, a.OpenedID, "", typ+"/"+path+" record after the end")
			got, err := st.RestoreEndedWaits("A")
			if err != nil || len(got) != 0 {
				t.Fatalf("%s/%s: RestoreEndedWaits = %v, %v; want nothing", typ, path, got, err)
			}
			if ws, _ := st.SessionWaits("A"); len(ws) != 0 {
				t.Fatalf("%s/%s: session_waits = %+v", typ, path, ws)
			}
		}
	}
}

package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var tw0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func declared(typ, reason string, at time.Time) pigtail.SessionWait {
	return pigtail.SessionWait{Type: typ, Reason: reason, Source: pigtail.WaitSourceDeclared, Since: at}
}

func auto(typ, reason string, at time.Time) pigtail.SessionWait {
	return pigtail.SessionWait{Type: typ, Reason: reason, Source: pigtail.WaitSourceAuto, Since: at}
}

func mustSet(t *testing.T, st *Store, session string, w pigtail.SessionWait, meta WaitMeta) WaitTransition {
	t.Helper()
	tr, err := st.SetSessionWaitRecord(session, w, meta)
	if err != nil {
		t.Fatalf("SetSessionWaitRecord(%s, %+v): %v", session, w, err)
	}
	return tr
}

func mustGet(t *testing.T, st *Store, id string) *WaitRecord {
	t.Helper()
	r, err := st.GetWaitRecord(id)
	if err != nil {
		t.Fatalf("GetWaitRecord(%s): %v", id, err)
	}
	return r
}

func openIDs(t *testing.T, st *Store, session string) []string {
	t.Helper()
	ids, err := st.OpenWaitRecordIDs(session)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func countMessages(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sessionWaitOf(t *testing.T, st *Store, session, source string) *pigtail.SessionWait {
	t.Helper()
	ws, err := st.SessionWaits(session)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ws {
		if ws[i].Source == source {
			return &ws[i]
		}
	}
	return nil
}

// Review Focus 2: two declarations close together leave one open declared
// wait; a dialog after them is a second fact, of the other source.
func TestWaitRecordOneOpenPerSession(t *testing.T) {
	st := openMem(t)
	first := mustSet(t, st, "s1", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_1"})
	if !strings.HasPrefix(first.OpenedID, pigtail.PrefixWait) || len(first.OpenedID) != len(pigtail.PrefixWait)+16 || first.ReplacedID != "" {
		t.Fatalf("first transition = %+v", first)
	}
	second := mustSet(t, st, "s1", declared("working", "CI", tw0.Add(time.Second)), WaitMeta{Scope: "perso"})
	if second.ReplacedID != first.OpenedID || second.OpenedID == "" || second.OpenedID == first.OpenedID {
		t.Fatalf("second transition = %+v (first %+v)", second, first)
	}
	r := mustGet(t, st, first.OpenedID)
	if r.State != pigtail.WaitStateReplaced || r.ClosedAt == nil || !r.ClosedAt.Equal(tw0.Add(time.Second)) {
		t.Fatalf("first record = %+v", r)
	}
	if r.SubjectID != "sub_1" || r.Scope != "perso" || r.Type != "arbitrage" || r.Reason != "D1" || r.Source != pigtail.WaitSourceDeclared {
		t.Fatalf("first record fields = %+v", r)
	}
	if ids := openIDs(t, st, "s1"); len(ids) != 1 || ids[0] != second.OpenedID {
		t.Fatalf("open ids = %v, want [%s]", ids, second.OpenedID)
	}
	dialog := mustSet(t, st, "s1", auto("permission", "Bash", tw0.Add(2*time.Second)), WaitMeta{Scope: "perso"})
	if dialog.OpenedID == "" || dialog.ReplacedID != "" {
		t.Fatalf("dialog transition = %+v", dialog)
	}
	if ids := openIDs(t, st, "s1"); len(ids) != 2 {
		t.Fatalf("open ids = %v, want one per source", ids)
	}
	if r := mustGet(t, st, second.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("declared touched by the dialog: %+v", r)
	}
	if r := mustGet(t, st, dialog.OpenedID); r.Source != pigtail.WaitSourceAuto || r.State != pigtail.WaitStateOpen {
		t.Fatalf("dialog record = %+v", r)
	}
}

func TestWaitRecordClearedByEveryClearPath(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "s1", declared("review", "PR", tw0), WaitMeta{})
	n, err := st.ClearSessionWaits("s1")
	if err != nil || n != 1 {
		t.Fatalf("ClearSessionWaits = %d, %v", n, err)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateCleared || r.ClosedAt == nil {
		t.Fatalf("after ClearSessionWaits: %+v", r)
	}

	b := mustSet(t, st, "s2", declared("blocked", "login", tw0), WaitMeta{})
	if err := st.EndPeer("s2", tw0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r := mustGet(t, st, b.OpenedID); r.State != pigtail.WaitStateCleared || r.ClosedAt == nil {
		t.Fatalf("after EndPeer: %+v", r)
	}
	// D29-A: the end of the session no longer closes its subject's
	// answerable wait.
	sb := mustSet(t, st, "s5", declared("blocked", "login", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_s"})
	if err := st.EndPeer("s5", tw0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r := mustGet(t, st, sb.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("subject's wait after EndPeer: %+v", r)
	}

	d := mustSet(t, st, "s3", declared("arbitrage", "D1", tw0), WaitMeta{})
	q := mustSet(t, st, "s3", auto("question", "Date lib", tw0.Add(time.Second)), WaitMeta{})
	n, closed, _, err := st.ClearSessionWaitsClosing("s3", false, pigtail.WaitSourceAuto)
	if err != nil || n != 1 || len(closed) != 1 || closed[0] != q.OpenedID {
		t.Fatalf("ClearSessionWaitsClosing(auto) = %d, %v, %v", n, closed, err)
	}
	if r := mustGet(t, st, q.OpenedID); r.State != pigtail.WaitStateCleared {
		t.Fatalf("auto record = %+v", r)
	}
	if r := mustGet(t, st, d.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("declared record touched by an auto clear: %+v", r)
	}

	// closed_by: a session end (EndPeer, the gone path) says so; a prompt,
	// a clear, a keep-subject clear and a turn leave it NULL.
	wantClosedBy(t, st, a.OpenedID, "", "ClearSessionWaits")
	wantClosedBy(t, st, b.OpenedID, "session_end", "EndPeer")
	wantClosedBy(t, st, q.OpenedID, "", "ClearSessionWaitsClosing(auto)")
	g := mustSet(t, st, "s6", declared("review", "PR", tw0), WaitMeta{})
	if _, _, _, err := st.ClearSessionWaitsClosing("s6", true, pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	wantClosedBy(t, st, g.OpenedID, "session_end", "gone clear")
	k := mustSet(t, st, "s7", declared("review", "PR", tw0), WaitMeta{})
	if _, _, _, err := st.ClearSessionWaitsKeepingSubject("s7"); err != nil {
		t.Fatal(err)
	}
	wantClosedBy(t, st, k.OpenedID, "", "ClearSessionWaitsKeepingSubject")
	rp := mustSet(t, st, "s8", declared("review", "PR", tw0), WaitMeta{})
	mustSet(t, st, "s8", declared("arbitrage", "D", tw0.Add(time.Second)), WaitMeta{})
	wantClosedBy(t, st, rp.OpenedID, "", "replaced")
}

// Review Focus 7: a dialog told twice keeps one record and its cmd.
func TestAutoDialogRecord(t *testing.T) {
	st := openMem(t)
	first := mustSet(t, st, "s1", auto("permission", "Bash", tw0), WaitMeta{Details: []byte(`{"cmd":"make","where":"/w"}`)})
	r := mustGet(t, st, first.OpenedID)
	if r.Source != pigtail.WaitSourceAuto || r.State != pigtail.WaitStateOpen || r.Details == nil || r.Details.Cmd != "make" || r.Details.Where != "/w" {
		t.Fatalf("dialog record = %+v (details %+v)", r, r.Details)
	}
	again := mustSet(t, st, "s1", auto("permission", "Bash", tw0.Add(time.Second)), WaitMeta{})
	if again != (WaitTransition{}) {
		t.Fatalf("same dialog told again: transition %+v, want none", again)
	}
	if ids := openIDs(t, st, "s1"); len(ids) != 1 || ids[0] != first.OpenedID {
		t.Fatalf("open ids = %v", ids)
	}
	if r := mustGet(t, st, first.OpenedID); r.Details == nil || r.Details.Cmd != "make" {
		t.Fatalf("cmd lost by a bare follow-up: %+v", r.Details)
	}
	mustSet(t, st, "s1", auto("permission", "Bash", tw0.Add(2*time.Second)), WaitMeta{Details: []byte(`{"cmd":"make test"}`)})
	if r := mustGet(t, st, first.OpenedID); r.Details == nil || r.Details.Cmd != "make test" || r.Details.Where != "" {
		t.Fatalf("details not replaced: %+v", r.Details)
	}
	lim := mustSet(t, st, "s1", auto("limit", "", tw0.Add(3*time.Second)), WaitMeta{})
	if lim.OpenedID != "" || lim.ReplacedID != first.OpenedID {
		t.Fatalf("limit transition = %+v", lim)
	}
	if r := mustGet(t, st, first.OpenedID); r.State != pigtail.WaitStateReplaced {
		t.Fatalf("dialog record after a limit: %+v", r)
	}
	if ids := openIDs(t, st, "s1"); len(ids) != 0 {
		t.Fatalf("open ids after a limit = %v", ids)
	}
	alone := mustSet(t, st, "s2", auto("limit", "", tw0), WaitMeta{})
	if alone != (WaitTransition{}) {
		t.Fatalf("lone limit transition = %+v", alone)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM wait_records WHERE session_id = 's2'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("records of s2 = %d, %v", n, err)
	}
}

func TestAnswerWaitRefusesAuto(t *testing.T) {
	st := openMem(t)
	q := mustSet(t, st, "s1", auto("question", "Date lib", tw0), WaitMeta{})
	m := newMsg("msg_ans", pigtail.HumanSender, "s1", tw0)
	if _, err := st.AnswerWaitWithMessage(q.OpenedID, []byte(`{"text":"a"}`), tw0, m); !errors.Is(err, ErrWaitAuto) {
		t.Fatalf("answer to an auto wait: err = %v, want ErrWaitAuto", err)
	}
	if r := mustGet(t, st, q.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("auto record = %+v", r)
	}
	if sessionWaitOf(t, st, "s1", pigtail.WaitSourceAuto) == nil {
		t.Fatal("auto session_waits row went")
	}
	if n := countMessages(t, st); n != 0 {
		t.Fatalf("messages = %d", n)
	}
}

func TestAnswerWaitClosesAndDropsDeclared(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "s1", declared("arbitrage", "D1", tw0), WaitMeta{Details: []byte(`{"why":"w"}`)})
	answer := []byte(`{"choice":"A","at":"2026-10-20T09:05:00Z"}`)
	m := newMsg("msg_ans", pigtail.HumanSender, "s1", tw0.Add(5*time.Minute))
	r, err := st.AnswerWaitWithMessage(a.OpenedID, answer, tw0.Add(5*time.Minute), m)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != pigtail.WaitStateAnswered || r.ClosedAt == nil || !r.ClosedAt.Equal(tw0.Add(5*time.Minute)) {
		t.Fatalf("answered record = %+v", r)
	}
	if r.Answer == nil || r.Answer.Choice != "A" {
		t.Fatalf("answer = %+v", r.Answer)
	}
	var stored string
	if err := st.db.QueryRow(`SELECT answer FROM wait_records WHERE id = ?`, a.OpenedID).Scan(&stored); err != nil || stored != string(answer) {
		t.Fatalf("stored answer = %q, %v", stored, err)
	}
	if _, err := st.GetMessage(m.ID); err != nil {
		t.Fatalf("message: %v", err)
	}
	if r.MessageID != m.ID || r.DeliveredTo != "s1" || r.DeliveredAt == nil || !r.DeliveredAt.Equal(tw0.Add(5*time.Minute)) {
		t.Fatalf("delivery = %q to %q at %v", r.MessageID, r.DeliveredTo, r.DeliveredAt)
	}
	if sessionWaitOf(t, st, "s1", pigtail.WaitSourceDeclared) != nil {
		t.Fatal("declared session_waits row still there")
	}
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, answer, tw0, newMsg("msg_2", pigtail.HumanSender, "s1", tw0)); !errors.Is(err, ErrWaitClosed) {
		t.Fatalf("second answer: err = %v, want ErrWaitClosed", err)
	}
	if _, err := st.AnswerWaitWithMessage("wait_0000000000000000", answer, tw0, newMsg("msg_3", pigtail.HumanSender, "s1", tw0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrNotFound", err)
	}
	if n := countMessages(t, st); n != 1 {
		t.Fatalf("messages = %d, want the answer's only", n)
	}
}

// Review Focus 5: the message write fails, the wait stays open.
func TestAnswerWaitRollsBackOnMessageFailure(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "s1", declared("question", "which?", tw0), WaitMeta{})
	if err := st.CreateMessage(newMsg("msg_dup", "s2", "s1", tw0)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"text":"x"}`), tw0, newMsg("msg_dup", pigtail.HumanSender, "s1", tw0)); err == nil {
		t.Fatal("answer with a conflicting message id succeeded")
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen || r.Answer != nil || r.ClosedAt != nil {
		t.Fatalf("record after a failed answer = %+v", r)
	}
	if sessionWaitOf(t, st, "s1", pigtail.WaitSourceDeclared) == nil {
		t.Fatal("declared row went with a failed answer")
	}
	if n := countMessages(t, st); n != 1 {
		t.Fatalf("messages = %d, want the earlier one only", n)
	}
}

// Review Focus 3: the user's prompt clears the wait, the answer comes after.
func TestAnswerWaitRacesClear(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "s1", declared("review", "PR", tw0), WaitMeta{})
	if _, err := st.ClearSessionWaits("s1"); err != nil {
		t.Fatal(err)
	}
	m := newMsg("msg_ans", pigtail.HumanSender, "s1", tw0)
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"seen":true}`), tw0, m); !errors.Is(err, ErrWaitClosed) {
		t.Fatalf("answer after clear: err = %v, want ErrWaitClosed", err)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateCleared {
		t.Fatalf("record = %+v", r)
	}
	if _, err := st.GetMessage(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message of a cleared wait: %v", err)
	}
}

func TestPurgeClosedWaitRecords(t *testing.T) {
	st := openMem(t)
	now := tw0
	day := 24 * time.Hour
	old := mustSet(t, st, "s1", declared("done", "", now.Add(-32*day)), WaitMeta{})
	if _, _, _, err := st.clearAt("s1", now.Add(-31*day), false, false, pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	recent := mustSet(t, st, "s2", declared("done", "", now.Add(-30*day)), WaitMeta{})
	if _, _, _, err := st.clearAt("s2", now.Add(-29*day), false, false, pigtail.WaitSourceDeclared); err != nil {
		t.Fatal(err)
	}
	open := mustSet(t, st, "s3", declared("blocked", "", now.Add(-40*day)), WaitMeta{})
	n, never, err := st.PurgeClosedWaitRecords(now.Add(-30 * day))
	if err != nil || n != 1 || len(never) != 0 {
		t.Fatalf("purge = %d, %v, %v; want 1, none never delivered", n, never, err)
	}
	if _, err := st.GetWaitRecord(old.OpenedID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old closed record: %v", err)
	}
	mustGet(t, st, recent.OpenedID)
	mustGet(t, st, open.OpenedID)
}

func TestListOpenWaitRecordsScopeAndSnooze(t *testing.T) {
	st := openMem(t)
	tk := mustSet(t, st, "s1", declared("review", "PR", tw0), WaitMeta{Scope: "globex"})
	p1 := mustSet(t, st, "s2", declared("arbitrage", "D1", tw0.Add(time.Second)), WaitMeta{Scope: "perso"})
	p2 := mustSet(t, st, "s3", declared("question", "q", tw0.Add(2*time.Second)), WaitMeta{Scope: "perso"})
	// A closed one is never listed.
	mustSet(t, st, "s4", declared("done", "", tw0), WaitMeta{Scope: "perso"})
	if _, err := st.ClearSessionWaits("s4"); err != nil {
		t.Fatal(err)
	}
	now := tw0.Add(time.Minute)
	if _, err := st.SnoozeWait(p2.OpenedID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ids := func(rs []WaitRecord) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	list := func(f WaitRecordFilter) []string {
		t.Helper()
		rs, err := st.ListOpenWaitRecords(f)
		if err != nil {
			t.Fatal(err)
		}
		return ids(rs)
	}
	if got := list(WaitRecordFilter{Scope: scope.Any, Now: now}); strings.Join(got, ",") != tk.OpenedID+","+p1.OpenedID {
		t.Fatalf("all, now = %v", got)
	}
	if got := list(WaitRecordFilter{Scope: "perso", Now: now}); strings.Join(got, ",") != p1.OpenedID {
		t.Fatalf("perso, now = %v", got)
	}
	if got := list(WaitRecordFilter{Scope: "globex", Now: now}); strings.Join(got, ",") != tk.OpenedID {
		t.Fatalf("globex, now = %v", got)
	}
	if got := list(WaitRecordFilter{Scope: "perso", Now: now, IncludeSnoozed: true}); strings.Join(got, ",") != p1.OpenedID+","+p2.OpenedID {
		t.Fatalf("perso, snoozed included = %v", got)
	}
	if got := list(WaitRecordFilter{Scope: "perso", Now: now.Add(2 * time.Hour)}); strings.Join(got, ",") != p1.OpenedID+","+p2.OpenedID {
		t.Fatalf("perso, after the snooze = %v", got)
	}
	r := mustGet(t, st, p2.OpenedID)
	if r.SnoozedUntil == nil || !r.SnoozedUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("snoozed_until = %v", r.SnoozedUntil)
	}
	if _, err := st.SnoozeWait("wait_0000000000000000", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snooze unknown: %v", err)
	}
	closed, _ := st.OpenWaitRecordIDs("s4")
	if len(closed) != 0 {
		t.Fatalf("open ids of s4 = %v", closed)
	}
	var s4 string
	if err := st.db.QueryRow(`SELECT id FROM wait_records WHERE session_id = 's4'`).Scan(&s4); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SnoozeWait(s4, now); !errors.Is(err, ErrWaitClosed) {
		t.Fatalf("snooze closed: %v", err)
	}
}

func TestSessionWaitCarriesRecordID(t *testing.T) {
	st := openMem(t)
	d := mustSet(t, st, "s1", declared("arbitrage", "D1", tw0), WaitMeta{})
	p := mustSet(t, st, "s1", auto("permission", "Bash", tw0), WaitMeta{})
	if w := sessionWaitOf(t, st, "s1", pigtail.WaitSourceDeclared); w == nil || w.ID != d.OpenedID {
		t.Fatalf("declared wait = %+v, want id %s", w, d.OpenedID)
	}
	if w := sessionWaitOf(t, st, "s1", pigtail.WaitSourceAuto); w == nil || w.ID != p.OpenedID {
		t.Fatalf("auto dialog wait = %+v, want id %s", w, p.OpenedID)
	}
	all, err := st.ListSessionWaits()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range all["s1"] {
		if w.ID == "" {
			t.Fatalf("ListSessionWaits without id: %+v", w)
		}
	}
	mustSet(t, st, "s2", auto("limit", "", tw0), WaitMeta{})
	if err := st.SetSessionWait("s2", pigtail.SessionWait{Type: "mac", Source: pigtail.WaitSourceHeld, HolderPID: 42, Since: tw0}); err != nil {
		t.Fatal(err)
	}
	ws, err := st.SessionWaits("s2")
	if err != nil || len(ws) != 2 {
		t.Fatalf("s2 waits = %+v, %v", ws, err)
	}
	for _, w := range ws {
		if w.ID != "" {
			t.Fatalf("%s wait carries an id: %+v", w.Source, w)
		}
	}
}

// EndPeer closes the session's records at the clock it is given, not at the
// wall clock (like AnswerWaitWithMessage).
func TestEndPeerClosesRecordsAtItsClock(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "s1", declared("blocked", "login", tw0), WaitMeta{})
	at := tw0.Add(90 * time.Minute)
	if err := st.EndPeer("s1", at); err != nil {
		t.Fatal(err)
	}
	r := mustGet(t, st, a.OpenedID)
	if r.State != pigtail.WaitStateCleared || r.ClosedAt == nil || !r.ClosedAt.Equal(at) {
		t.Fatalf("closed_at = %v, want %v (record %+v)", r.ClosedAt, at, r)
	}
}

// --- D28, D29-B: the wait belongs to its subject in its scope ---------------

// TestOwnerRuleMatchesCheck: waitOwner and migration 0027's CHECK say the
// same thing for every declarable type and source, with and without a
// subject; a type added to waittype.AnswerableTypes without the migration
// fails here.
func TestOwnerRuleMatchesCheck(t *testing.T) {
	st := openMem(t)
	n := 0
	for _, typ := range waittype.Declarable {
		for _, src := range []string{pigtail.WaitSourceDeclared, pigtail.WaitSourceAuto} {
			for _, subject := range []string{"", "sub_s"} {
				n++
				session := fmt.Sprintf("s%d", n)
				sub := subject
				if sub != "" {
					sub = fmt.Sprintf("%s%d", subject, n)
				}
				good := waitOwner(session, src, "perso", sub, typ)
				bad := "session:" + session
				if good == bad {
					bad = "subject:perso:" + sub
				}
				ins := func(id, owner string) error {
					_, err := st.db.Exec(`INSERT INTO wait_records (id, session_id, source, scope, subject_id, type, owner, state, opened_at)
						VALUES (?, ?, ?, 'perso', ?, ?, ?, 'open', 1)`, id, session, src, sub, string(typ), owner)
					return err
				}
				if err := ins(fmt.Sprintf("wait_bad%d", n), bad); err == nil || !strings.Contains(err.Error(), "CHECK") {
					t.Errorf("%s %s subject %q: owner %q accepted (err %v), want CHECK", typ, src, sub, bad, err)
				}
				if err := ins(fmt.Sprintf("wait_good%d", n), good); err != nil {
					t.Errorf("%s %s subject %q: owner %q refused: %v", typ, src, sub, good, err)
				}
			}
		}
	}
}

// TestWaitRecordOneOpenPerSubject (D29-B): the last answerable declaration on
// a subject in a scope replaces the previous one, whatever its session; the
// other session's session_waits row (its pill) is not touched.
func TestWaitRecordOneOpenPerSubject(t *testing.T) {
	st := openMem(t)
	meta := WaitMeta{Scope: "perso", SubjectID: "sub_s"}
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), meta)
	b := mustSet(t, st, "B", declared("review", "PR", tw0.Add(time.Second)), meta)
	if b.ReplacedID != a.OpenedID || b.ReplacedSessionID != "A" || b.AlsoReplacedID != "" {
		t.Fatalf("B's transition = %+v (A opened %s)", b, a.OpenedID)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateReplaced {
		t.Fatalf("A's record = %+v", r)
	}
	open, err := st.ListOpenWaitRecords(WaitRecordFilter{Scope: scope.Any})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != b.OpenedID {
		t.Fatalf("open records = %+v, want B's only", open)
	}
	if w := sessionWaitOf(t, st, "A", pigtail.WaitSourceDeclared); w == nil || w.Type != "arbitrage" {
		t.Fatalf("A's declared row = %+v, want it intact", w)
	}

	c := mustSet(t, st, "C", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso"})
	d := mustSet(t, st, "D", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso"})
	if d.ReplacedID != "" || mustGet(t, st, c.OpenedID).State != pigtail.WaitStateOpen {
		t.Fatalf("waits without a subject replaced each other: %+v", d)
	}
}

// TestWaitRecordOneOpenPerSessionAcrossOwners: a session keeps at most one
// open record per source, whatever its owner.
func TestWaitRecordOneOpenPerSessionAcrossOwners(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_s"})
	w := mustSet(t, st, "A", declared("working", "CI", tw0.Add(time.Second)), WaitMeta{Scope: "perso", SubjectID: "sub_s"})
	if w.ReplacedID != a.OpenedID || w.ReplacedSessionID != "A" {
		t.Fatalf("working transition = %+v", w)
	}
	b := mustSet(t, st, "B", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_s"})
	q := mustSet(t, st, "B", declared("question", "Q", tw0.Add(time.Second)), WaitMeta{Scope: "perso", SubjectID: "sub_t"})
	if q.ReplacedID != b.OpenedID || mustGet(t, st, b.OpenedID).State != pigtail.WaitStateReplaced {
		t.Fatalf("question on T transition = %+v", q)
	}
}

// TestWorkingNeverReplacesSubjectWait (Review Focus 10): another session's
// working or done never takes the subject's wait.
func TestWorkingNeverReplacesSubjectWait(t *testing.T) {
	st := openMem(t)
	meta := WaitMeta{Scope: "perso", SubjectID: "sub_s"}
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), meta)
	for i, typ := range []string{"working", "done", "wrapped"} {
		tr := mustSet(t, st, "B", declared(typ, "x", tw0.Add(time.Duration(i+1)*time.Second)), meta)
		if tr.ReplacedID == a.OpenedID || tr.AlsoReplacedID == a.OpenedID {
			t.Fatalf("B's %s replaced A's arbitrage: %+v", typ, tr)
		}
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("A's arbitrage = %+v", r)
	}
}

// TestDeclareOnSubjectNeverReplacesAnotherScope (Review Focus 12): two
// scopes on one subject keep a wait each.
func TestDeclareOnSubjectNeverReplacesAnotherScope(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_s"})
	b := mustSet(t, st, "B", declared("arbitrage", "D1", tw0.Add(time.Second)), WaitMeta{Scope: "globex", SubjectID: "sub_s"})
	if b.ReplacedID != "" || b.AlsoReplacedID != "" {
		t.Fatalf("B replaced across scopes: %+v", b)
	}
	if mustGet(t, st, a.OpenedID).State != pigtail.WaitStateOpen || mustGet(t, st, b.OpenedID).State != pigtail.WaitStateOpen {
		t.Fatal("both waits should stay open")
	}
}

// --- D28, D29-A, D29-C: the subject's wait outlives its session; kept answers

var subjS = WaitMeta{Scope: "perso", SubjectID: "sub_s"}

func mustEnd(t *testing.T, st *Store, session string, at time.Time) (closed, reheld []string) {
	t.Helper()
	closed, reheld, err := st.EndPeerClosing(session, at)
	if err != nil {
		t.Fatalf("EndPeerClosing(%s): %v", session, err)
	}
	return closed, reheld
}

func mustPeer(t *testing.T, st *Store, session string) {
	t.Helper()
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: session, Scope: "perso", UpdatedAt: tw0}); err != nil {
		t.Fatal(err)
	}
}

func keptIDs(t *testing.T, st *Store) []string {
	t.Helper()
	ks, err := st.KeptAnswers()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range ks {
		out = append(out, k.ID)
	}
	return out
}

func TestSubjectWaitSurvivesEndPeer(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	p := mustSet(t, st, "A", auto("permission", "Bash", tw0), subjS)
	c := mustSet(t, st, "C", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso"})
	closed, reheld := mustEnd(t, st, "A", tw0.Add(time.Minute))
	if len(closed) != 1 || closed[0] != p.OpenedID || len(reheld) != 0 {
		t.Fatalf("closed %v reheld %v, want [%s] []", closed, reheld, p.OpenedID)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("subject's wait = %+v", r)
	}
	if ws, _ := st.SessionWaits("A"); len(ws) != 0 {
		t.Fatalf("A's rows = %+v", ws)
	}
	closed, _ = mustEnd(t, st, "C", tw0.Add(time.Minute))
	if len(closed) != 1 || closed[0] != c.OpenedID || mustGet(t, st, c.OpenedID).State != pigtail.WaitStateCleared {
		t.Fatalf("no-subject wait after its end: closed %v", closed)
	}
}

func TestDoneWaitClosesWithItsSession(t *testing.T) {
	st := openMem(t)
	d := mustSet(t, st, "A", declared("done", "finished", tw0), subjS)
	mustEnd(t, st, "A", tw0.Add(time.Minute))
	if r := mustGet(t, st, d.OpenedID); r.State != pigtail.WaitStateCleared {
		t.Fatalf("done wait = %+v", r)
	}
}

func TestClearClosesOnlyRecordsItEmitted(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	mustEnd(t, st, "A", tw0.Add(time.Minute))
	if n, err := st.ClearSessionWaits("B"); err != nil || n != 0 {
		t.Fatalf("ClearSessionWaits(B) = %d, %v", n, err)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("A's wait after B's clear = %+v", r)
	}
	b := mustSet(t, st, "B", declared("done", "finished", tw0.Add(2*time.Minute)), subjS)
	if _, err := st.ClearSessionWaits("B"); err != nil {
		t.Fatal(err)
	}
	if mustGet(t, st, b.OpenedID).State != pigtail.WaitStateCleared || mustGet(t, st, a.OpenedID).State != pigtail.WaitStateOpen {
		t.Fatal("B's clear closed A's wait, or not its own")
	}
	l := mustSet(t, st, "L", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_l"})
	if _, err := st.ClearSessionWaits("L"); err != nil {
		t.Fatal(err)
	}
	if r := mustGet(t, st, l.OpenedID); r.State != pigtail.WaitStateCleared {
		t.Fatalf("a live emitter's clear kept its wait: %+v", r)
	}
	g := mustSet(t, st, "G", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_g"})
	if n, err := st.ClearGoneSessionWaits("G", pigtail.WaitSourceDeclared); err != nil || n != 1 {
		t.Fatalf("ClearGoneSessionWaits = %d, %v", n, err)
	}
	if r := mustGet(t, st, g.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("a gone session's clear closed its subject's wait: %+v", r)
	}
	if sessionWaitOf(t, st, "G", pigtail.WaitSourceDeclared) != nil {
		t.Fatal("G's row stayed")
	}
}

// D24: the emitter resumed under the same id: its prompt clears nothing (its
// row went at its end), the inherited wait stays open.
func TestResumedEmitterPromptKeepsInheritedWait(t *testing.T) {
	st := openMem(t)
	mustPeer(t, st, "A")
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	mustEnd(t, st, "A", tw0.Add(time.Minute))
	mustPeer(t, st, "A")
	if n, err := st.ClearSessionWaits("A"); err != nil || n != 0 {
		t.Fatalf("ClearSessionWaits(A) = %d, %v", n, err)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("inherited wait = %+v", r)
	}
}

func TestAnswerKeptWithoutMessage(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	r, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"choice":"A"}`), tw0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != pigtail.WaitStateAnswered || r.MessageID != "" || r.DeliveredTo != "" {
		t.Fatalf("kept answer = %+v", r)
	}
	if ids := keptIDs(t, st); len(ids) != 1 || ids[0] != a.OpenedID {
		t.Fatalf("kept = %v", ids)
	}
	if n := countMessages(t, st); n != 0 {
		t.Fatalf("messages = %d", n)
	}
}

func TestDeliverKeptAnswerOnce(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"choice":"A"}`), tw0, nil); err != nil {
		t.Fatal(err)
	}
	at := tw0.Add(time.Hour)
	m := newMsg("msg_k1", pigtail.HumanSender, "B", at)
	if err := st.DeliverKeptAnswer(a.OpenedID, m, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMessage("msg_k1"); err != nil {
		t.Fatalf("message: %v", err)
	}
	r := mustGet(t, st, a.OpenedID)
	if r.MessageID != "msg_k1" || r.DeliveredTo != "B" || r.DeliveredAt == nil || !r.DeliveredAt.Equal(at) {
		t.Fatalf("delivery = %+v", r)
	}
	if ids := keptIDs(t, st); len(ids) != 0 {
		t.Fatalf("still kept: %v", ids)
	}
	if err := st.DeliverKeptAnswer(a.OpenedID, newMsg("msg_k2", pigtail.HumanSender, "B", at), at); !errors.Is(err, ErrWaitDelivered) {
		t.Fatalf("second delivery: err = %v, want ErrWaitDelivered", err)
	}
	if _, err := st.GetMessage("msg_k2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second message: %v", err)
	}
}

func TestDeliverKeptAnswerRollsBackOnMessageFailure(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("question", "Q", tw0), subjS)
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"text":"x"}`), tw0, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateMessage(newMsg("msg_dup", "s2", "B", tw0)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeliverKeptAnswer(a.OpenedID, newMsg("msg_dup", pigtail.HumanSender, "B", tw0), tw0); err == nil {
		t.Fatal("delivery with a conflicting message id succeeded")
	}
	if ids := keptIDs(t, st); len(ids) != 1 || mustGet(t, st, a.OpenedID).MessageID != "" {
		t.Fatalf("kept after a failed delivery = %v", ids)
	}
}

// Review Focus 11: never a message to an ended session.
func TestAnswerRacesRecipientEnd(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	mustPeer(t, st, "B")
	mustEnd(t, st, "B", tw0)
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"choice":"A"}`), tw0, newMsg("msg_r1", pigtail.HumanSender, "B", tw0)); !errors.Is(err, ErrRecipientGone) {
		t.Fatalf("answer to an ended recipient: err = %v, want ErrRecipientGone", err)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen || countMessages(t, st) != 0 {
		t.Fatalf("after a refused answer: %+v", r)
	}
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"choice":"A"}`), tw0, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeliverKeptAnswer(a.OpenedID, newMsg("msg_r2", pigtail.HumanSender, "B", tw0), tw0); !errors.Is(err, ErrRecipientGone) {
		t.Fatalf("delivery to an ended recipient: err = %v, want ErrRecipientGone", err)
	}
	if ids := keptIDs(t, st); len(ids) != 1 || countMessages(t, st) != 0 {
		t.Fatalf("after a refused delivery: kept %v", ids)
	}
	// No peer_sessions row: not ended.
	if err := st.DeliverKeptAnswer(a.OpenedID, newMsg("msg_r3", pigtail.HumanSender, "C", tw0), tw0); err != nil {
		t.Fatalf("delivery to an unregistered session: %v", err)
	}
}

func TestEndPeerReholdsUnreadAnswer(t *testing.T) {
	st := openMem(t)
	// A subject's answer delivered to B, unread: B's end keeps it again.
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	mustPeer(t, st, "B")
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"choice":"A"}`), tw0, newMsg("msg_u1", pigtail.HumanSender, "B", tw0)); err != nil {
		t.Fatal(err)
	}
	_, reheld := mustEnd(t, st, "B", tw0.Add(time.Minute))
	if len(reheld) != 1 || reheld[0] != a.OpenedID {
		t.Fatalf("reheld = %v", reheld)
	}
	if m, _ := st.GetMessage("msg_u1"); m == nil || m.State != pigtail.MessageStateExpired {
		t.Fatalf("message = %+v, want expired", m)
	}
	if ids := keptIDs(t, st); len(ids) != 1 || ids[0] != a.OpenedID {
		t.Fatalf("kept = %v", ids)
	}
	if r := mustGet(t, st, a.OpenedID); r.MessageID != "" || r.DeliveredTo != "" || r.DeliveredAt != nil {
		t.Fatalf("delivery not reset: %+v", r)
	}

	// Read (claimed): nothing kept again.
	b := mustSet(t, st, "A2", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_2"})
	mustPeer(t, st, "B2")
	if _, err := st.AnswerWaitWithMessage(b.OpenedID, []byte(`{"choice":"A"}`), tw0, newMsg("msg_u2", pigtail.HumanSender, "B2", tw0)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE messages SET state = 'delivered' WHERE id = 'msg_u2'`); err != nil {
		t.Fatal(err)
	}
	if _, reheld := mustEnd(t, st, "B2", tw0.Add(time.Minute)); len(reheld) != 0 {
		t.Fatalf("a read answer kept again: %v", reheld)
	}

	// A wait without a subject: never kept again (D24).
	c := mustSet(t, st, "A3", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso"})
	mustPeer(t, st, "B3")
	if _, err := st.AnswerWaitWithMessage(c.OpenedID, []byte(`{"choice":"A"}`), tw0, newMsg("msg_u3", pigtail.HumanSender, "B3", tw0)); err != nil {
		t.Fatal(err)
	}
	if _, reheld := mustEnd(t, st, "B3", tw0.Add(time.Minute)); len(reheld) != 0 {
		t.Fatalf("a no-subject answer kept again: %v", reheld)
	}
	if m, _ := st.GetMessage("msg_u3"); m == nil || m.State != pigtail.MessageStatePending {
		t.Fatalf("message = %+v, want pending", m)
	}
	if r := mustGet(t, st, c.OpenedID); r.MessageID != "msg_u3" || r.DeliveredTo != "B3" {
		t.Fatalf("no-subject delivery touched: %+v", r)
	}
}

func TestPurgeReportsNeverDeliveredAnswers(t *testing.T) {
	st := openMem(t)
	day := 24 * time.Hour
	old := tw0.Add(-31 * day)
	k := mustSet(t, st, "A", declared("arbitrage", "D1", old), subjS)
	if _, err := st.AnswerWaitWithMessage(k.OpenedID, []byte(`{"choice":"A"}`), old, nil); err != nil {
		t.Fatal(err)
	}
	d := mustSet(t, st, "B", declared("arbitrage", "D1", old), WaitMeta{Scope: "perso", SubjectID: "sub_d"})
	if _, err := st.AnswerWaitWithMessage(d.OpenedID, []byte(`{"choice":"A"}`), old, newMsg("msg_p", pigtail.HumanSender, "B", old)); err != nil {
		t.Fatal(err)
	}
	n, never, err := st.PurgeClosedWaitRecords(tw0.Add(-30 * day))
	if err != nil || n != 2 || len(never) != 1 || never[0] != k.OpenedID {
		t.Fatalf("purge = %d, %v, %v; want 2, [%s]", n, never, err, k.OpenedID)
	}
}

func TestReadNormalizesEnvelope(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	if _, err := st.db.Exec(`UPDATE wait_records SET details = '{"why":"w"}', answer = '{"choice":"A","at":"2026-10-20T09:05:00Z"}' WHERE id = ?`, a.OpenedID); err != nil {
		t.Fatal(err)
	}
	r := mustGet(t, st, a.OpenedID)
	if r.Details == nil || r.Details.Kind != "arbitrage" || r.Details.Schema != 1 {
		t.Fatalf("details = %+v", r.Details)
	}
	if r.Answer == nil || r.Answer.Kind != "arbitrage" || r.Answer.Schema != 1 {
		t.Fatalf("answer = %+v", r.Answer)
	}
}

// Review of PR #229: B is handed a subject's answer, then crashes without
// SessionEnd — the sidebar loop's gone clear keeps the unread answer again,
// like EndPeerClosing.
func TestGoneClearReholdsUnreadAnswer(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	mustPeer(t, st, "B")
	mustSet(t, st, "B", auto("limit", "5h", tw0), WaitMeta{Scope: "perso"})
	if _, err := st.AnswerWaitWithMessage(a.OpenedID, []byte(`{"choice":"A"}`), tw0, newMsg("msg_g1", pigtail.HumanSender, "B", tw0)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClearGoneSessionWaits("B"); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetMessage("msg_g1"); m == nil || m.State != pigtail.MessageStateExpired {
		t.Fatalf("message = %+v, want expired", m)
	}
	if ids := keptIDs(t, st); len(ids) != 1 || ids[0] != a.OpenedID {
		t.Fatalf("kept = %v", ids)
	}
	// A live session's clear (its prompt) never keeps an answer again.
	b := mustSet(t, st, "A2", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso", SubjectID: "sub_2"})
	mustPeer(t, st, "B2")
	if _, err := st.AnswerWaitWithMessage(b.OpenedID, []byte(`{"choice":"A"}`), tw0, newMsg("msg_g2", pigtail.HumanSender, "B2", tw0)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClearSessionWaits("B2"); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetMessage("msg_g2"); m == nil || m.State != pigtail.MessageStatePending {
		t.Fatalf("message = %+v, want pending", m)
	}
}

// The emitter's own closing — its done, then the wrapped the done command
// declares — never closes the subject's wait it emitted: the pill reads
// wrapped, the record stays open and outlives the session's end.
func TestOwnWrapNeverReplacesSubjectWait(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	for i, typ := range []string{"done", "wrapped"} {
		tr := mustSet(t, st, "A", declared(typ, "finished", tw0.Add(time.Duration(i+1)*time.Second)), subjS)
		if tr.ReplacedID == a.OpenedID || tr.AlsoReplacedID == a.OpenedID {
			t.Fatalf("A's %s replaced its subject's arbitrage: %+v", typ, tr)
		}
		if w := sessionWaitOf(t, st, "A", pigtail.WaitSourceDeclared); w == nil || w.Type != typ {
			t.Fatalf("A's declared row = %+v, want %s", w, typ)
		}
	}
	mustEnd(t, st, "A", tw0.Add(time.Minute))
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("subject's arbitrage after A's closing and end = %+v", r)
	}
	// Answered while its emitter still shows wrapped: the pill is not the
	// answered wait's, it stays.
	b := mustSet(t, st, "B", declared("arbitrage", "D2", tw0.Add(2*time.Minute)), WaitMeta{Scope: "perso", SubjectID: "sub_t"})
	mustSet(t, st, "B", declared("wrapped", "", tw0.Add(3*time.Minute)), WaitMeta{Scope: "perso", SubjectID: "sub_t"})
	if _, err := st.AnswerWaitWithMessage(b.OpenedID, []byte(`{"kind":"arbitrage","schema":1,"choice":"a"}`), tw0.Add(4*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if w := sessionWaitOf(t, st, "B", pigtail.WaitSourceDeclared); w == nil || w.Type != "wrapped" {
		t.Fatalf("B's declared row after the answer = %+v, want wrapped kept", w)
	}
	// A wait of the session's own (no subject) still goes as before.
	c := mustSet(t, st, "C", declared("arbitrage", "D1", tw0), WaitMeta{Scope: "perso"})
	if tr := mustSet(t, st, "C", declared("wrapped", "", tw0.Add(time.Second)), WaitMeta{Scope: "perso"}); tr.ReplacedID != c.OpenedID {
		t.Fatalf("C's wrapped = %+v, want it to replace C's own wait", tr)
	}
}

// The clear of the done command's prompt drops the session's rows and closes the
// records the session owns, never its subject's wait; nothing is kept again
// (the session lives on).
func TestWrapClearKeepsSubjectWait(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	p := mustSet(t, st, "A", auto("permission", "Bash", tw0), subjS)
	n, closed, reheld, err := st.ClearSessionWaitsKeepingSubject("A")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(closed) != 1 || closed[0] != p.OpenedID || len(reheld) != 0 {
		t.Fatalf("n %d closed %v reheld %v, want 2 [%s] []", n, closed, reheld, p.OpenedID)
	}
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateOpen {
		t.Fatalf("subject's wait = %+v", r)
	}
	if ws, _ := st.SessionWaits("A"); len(ws) != 0 {
		t.Fatalf("A's rows = %+v", ws)
	}
}

func TestListRecentClosedWaitRecordsOrder(t *testing.T) {
	st := openMem(t)
	meta := WaitMeta{Scope: "gx", SubjectID: "sub_x"}
	var ids []string
	for i := 0; i < 7; i++ {
		tr := mustSet(t, st, "s1", declared("review", fmt.Sprintf("r%d", i), tw0.Add(time.Duration(i)*time.Minute)), meta)
		ids = append(ids, tr.OpenedID)
	}
	// Another subject and another session: never in the filtered history.
	mustSet(t, st, "s2", declared("review", "other", tw0.Add(time.Hour)), WaitMeta{Scope: "gx", SubjectID: "sub_y"})
	mustSet(t, st, "s2", declared("review", "other 2", tw0.Add(2*time.Hour)), WaitMeta{Scope: "gx", SubjectID: "sub_y"})

	got, err := st.ListRecentClosedWaitRecords(WaitRecordFilter{Scope: scope.Any, SubjectID: "sub_x"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	// ids[6] is open; ids[0..5] were replaced in turn, the latest closed last.
	want := []string{ids[5], ids[4], ids[3], ids[2], ids[1]}
	if len(got) != len(want) {
		t.Fatalf("got %d records; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] || got[i].State == pigtail.WaitStateOpen {
			t.Fatalf("record %d = %s (%s); want %s, closed, most recent first", i, got[i].ID, got[i].State, want[i])
		}
	}
	if got, _ := st.ListRecentClosedWaitRecords(WaitRecordFilter{Scope: scope.Any, SessionID: "s2"}, 5); len(got) != 1 || got[0].Reason != "other" {
		t.Fatalf("by session = %+v; want s2's one closed record", got)
	}
	if got, _ := st.ListRecentClosedWaitRecords(WaitRecordFilter{Scope: scope.Any, SubjectID: "sub_x"}, 9); len(got) != 5 {
		t.Fatalf("n=9 → %d records; want capped at 5", len(got))
	}
	if got, _ := st.ListRecentClosedWaitRecords(WaitRecordFilter{Scope: scope.Any, SubjectID: "sub_x"}, 0); len(got) != 0 {
		t.Fatalf("n=0 → %d records; want none", len(got))
	}
	open, err := st.ListOpenWaitRecords(WaitRecordFilter{Scope: scope.Any, SubjectID: "sub_x", Now: tw0.Add(3 * time.Hour)})
	if err != nil || len(open) != 1 || open[0].ID != ids[6] {
		t.Fatalf("open by subject = %+v, %v", open, err)
	}
	open, _ = st.ListOpenWaitRecords(WaitRecordFilter{Scope: scope.Any, SessionID: "s2", Now: tw0.Add(3 * time.Hour)})
	if len(open) != 1 || open[0].Reason != "other 2" {
		t.Fatalf("open by session = %+v", open)
	}
}

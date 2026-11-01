package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func closedBy(t *testing.T, st *Store, id string) *string {
	t.Helper()
	var by *string
	if err := st.db.QueryRow(`SELECT closed_by FROM wait_records WHERE id = ?`, id).Scan(&by); err != nil {
		t.Fatalf("closed_by(%s): %v", id, err)
	}
	return by
}

func wantClosedBy(t *testing.T, st *Store, id, want, path string) {
	t.Helper()
	by := closedBy(t, st, id)
	switch {
	case want == "" && by != nil:
		t.Errorf("%s: closed_by = %q, want NULL", path, *by)
	case want != "" && (by == nil || *by != want):
		t.Errorf("%s: closed_by = %v, want %q", path, by, want)
	}
}

func TestEndPeerRecordsSessionEndCause(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "s1", declared("review", "PR", tw0), WaitMeta{})
	mustEnd(t, st, "s1", tw0.Add(time.Minute))
	wantClosedBy(t, st, a.OpenedID, "session_end", "EndPeer")
}

// A record stamped session_end that is then redeclared (replaced) or answered
// ends with closed_by NULL: NULL = an answer or a redeclare (the relay design).
func TestSessionEndStampIsResetByReplaceAndAnswer(t *testing.T) {
	st := openMem(t)
	a := mustSet(t, st, "A", declared("arbitrage", "D1", tw0), subjS)
	mustEnd(t, st, "A", tw0.Add(time.Minute))
	wantClosedBy(t, st, a.OpenedID, "session_end", "after the end")
	mustSet(t, st, "A", declared("review", "new", tw0.Add(2*time.Minute)), subjS)
	if r := mustGet(t, st, a.OpenedID); r.State != pigtail.WaitStateReplaced {
		t.Fatalf("old record = %+v, want replaced", r)
	}
	wantClosedBy(t, st, a.OpenedID, "", "replaced")

	b := mustSet(t, st, "B", declared("arbitrage", "D1", tw0), subjS)
	mustEnd(t, st, "B", tw0.Add(time.Minute))
	wantClosedBy(t, st, b.OpenedID, "session_end", "B after the end")
	if _, err := st.AnswerWaitWithMessage(b.OpenedID, []byte(`{"text":"ok"}`), tw0.Add(3*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	wantClosedBy(t, st, b.OpenedID, "", "answered")
}

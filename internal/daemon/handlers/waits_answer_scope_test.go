package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// waitLogLines counts the "wait" lines of subject id's log.
func waitLogLines(t *testing.T, f *relayFixture, id string) int {
	t.Helper()
	logs, err := f.st.ListLog(id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range logs {
		if l.Kind == "wait" {
			n++
		}
	}
	return n
}

// A work session working in a perso subject's worktree declares a wait, the
// master answers with --cross-scope: the wait, the subject log and the journal
// (read by work through GET /journal) must not name the perso subject — the
// rule of POST /journal (a subject the scope may not read is unknown).
func TestWaitOnOtherScopeSubjectNeverLeaks(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	_, persoSub := scopedPeers(f, t)
	postWaitAs(t, f, work, "s-onperso", pigtail.SessionWaitRequest{Type: "review", Reason: "work wait"})
	rec := openRecord(t, f, "s-onperso", pigtail.WaitSourceDeclared)
	if rec == nil {
		t.Fatal("no open record")
	}
	if rec.SubjectID != "" {
		t.Fatalf("record froze the invisible subject %q", rec.SubjectID)
	}
	if r, _ := answerAs(t, f, persoCross, rec.ID, pigtail.WaitAnswerRequest{Seen: true}); r.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", r.Code, r.Body.String())
	}
	if n := waitLogLines(t, f, persoSub.ID); n != 0 {
		t.Fatalf("perso subject log got %d wait lines", n)
	}
	js, err := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "wait-answer"})
	if err != nil || len(js) != 1 {
		t.Fatalf("journal = %+v %v", js, err)
	}
	if js[0].SubjectID != "" {
		t.Fatalf("journal entry names subject %q under scope %s", js[0].SubjectID, js[0].Scope)
	}
}

// The subject is the one frozen at declaration (docs/wire-protocol.md): a wait
// declared with none, whose session is bound afterwards, answers with none.
func TestAnswerKeepsSubjectFrozenAtDeclaration(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	workSub, _ := scopedPeers(f, t)
	postWaitAs(t, f, work, "s-mate", pigtail.SessionWaitRequest{Type: "review", Reason: "no subject yet"})
	rec := openRecord(t, f, "s-mate", pigtail.WaitSourceDeclared)
	if rec == nil || rec.SubjectID != "" {
		t.Fatalf("record = %+v", rec)
	}
	f.peers(t, pigtail.PeerSession{SessionID: "s-mate", Cwd: "/wt/work-relay", Name: "work-m", Scope: "gx", CellID: "it"})
	if r, _ := answerAs(t, f, workCross, rec.ID, pigtail.WaitAnswerRequest{Seen: true}); r.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", r.Code, r.Body.String())
	}
	if n := waitLogLines(t, f, workSub.ID); n != 0 {
		t.Fatalf("subject bound after the declaration got %d wait lines", n)
	}
	js, _ := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "wait-answer"})
	if len(js) != 1 || js[0].SubjectID != "" {
		t.Fatalf("journal = %+v", js)
	}
}

func postWaitAs(t *testing.T, f *relayFixture, c caller, session string, req pigtail.SessionWaitRequest) {
	t.Helper()
	if rec := doAs(t, f.r, c, "POST", "/peers/"+session+"/wait", req); rec.Code != http.StatusOK {
		t.Fatalf("POST wait = %d %s", rec.Code, rec.Body.String())
	}
}

package handlers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/handoffdraft"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// haFix is a relay fixture with a subject, a peer working on it and a draft
// directory of its own (newRelayFixture sets none).
type haFix struct {
	*relayFixture
	sub pigtail.Subject
	dir string
	sid string
}

func newHAFix(t *testing.T) *haFix {
	t.Helper()
	f := newRelayFixture(t)
	f.rl.cfg.DraftDir = t.TempDir()
	f.rl.cfg.DoneCommand = testDoneCommand
	sub := f.seedSubjectWorktree(t, "Handoff auto", "/wt/handoff-auto")
	f.peers(t, pigtail.PeerSession{SessionID: "s-ha", Cwd: "/wt/handoff-auto/cmd", Name: "perso-a1", Scope: "gx", CellID: "w1"})
	h := &haFix{relayFixture: f, sub: sub, dir: f.rl.cfg.DraftDir, sid: "s-ha"}
	if got := h.peer(t); got.SubjectID != sub.ID {
		t.Fatalf("control: peer subject = %q, want %q", got.SubjectID, sub.ID)
	}
	return h
}

func (h *haFix) peer(t *testing.T) pigtail.PeerSession {
	t.Helper()
	p, err := h.st.GetPeer(h.sid)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func (h *haFix) turnAt() time.Time { return h.clock().Add(-10 * time.Minute) }

func (h *haFix) draft(t *testing.T, turn, tldr string) handoffdraft.Draft {
	t.Helper()
	d := handoffdraft.Draft{SessionID: h.sid, TurnID: turn, TurnAt: h.turnAt(), Cwd: "/wt/handoff-auto/cmd", Branch: "feat/handoff-auto", TLDR: tldr}
	if err := handoffdraft.Write(h.dir, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (h *haFix) logOf(t *testing.T, kind string) []pigtail.SubjectLogEntry {
	t.Helper()
	all, err := h.st.ListLog(h.sub.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []pigtail.SubjectLogEntry
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (h *haFix) manual(t *testing.T, id string, ts time.Time) {
	t.Helper()
	if err := h.st.AppendLog(&pigtail.SubjectLogEntry{ID: id, SubjectID: h.sub.ID, Timestamp: ts, Kind: pigtail.LogHandoff, Text: "manual " + id}); err != nil {
		t.Fatal(err)
	}
}

func (h *haFix) failures(t *testing.T) []pigtail.JournalEntry {
	t.Helper()
	rows, err := h.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "handoff-auto-failed", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (h *haFix) wait(t *testing.T, typ, reason string, since time.Time) {
	t.Helper()
	_, err := h.st.SetSessionWaitRecord(h.sid, pigtail.SessionWait{Type: typ, Reason: reason, Source: pigtail.WaitSourceDeclared, Since: since},
		store.WaitMeta{Scope: "gx", SubjectID: h.sub.ID})
	if err != nil {
		t.Fatal(err)
	}
}

func (h *haFix) promote(t *testing.T, why promoteReason) error {
	t.Helper()
	return h.rl.promoteHandoffDraft(h.peer(t), why, h.clock())
}

func autoID(h *haFix, turn string) string { return handoffAutoID(h.sid, turn) }

func TestPromoteWritesHandoffAuto(t *testing.T) {
	h := newHAFix(t)
	h.wait(t, "review", "PR #142 · flip la PR ?", h.clock().Add(-2*time.Minute))
	h.draft(t, "t1", "shipped the thing")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || got[0].ID != autoID(h, "t1") {
		t.Fatalf("lines = %+v", got)
	}
	for _, want := range []string{"[automatique", "shipped the thing", "feat/handoff-auto", "review", "flip la PR", "#142", "fin: end", h.sid} {
		if !strings.Contains(got[0].Text, want) {
			t.Errorf("text lacks %q:\n%s", want, got[0].Text)
		}
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); !os.IsNotExist(err) {
		t.Fatalf("draft not removed: %v", err)
	}
	if handoffdraft.ReadHandled(h.dir, h.sid) != "t1" {
		t.Fatal("turn not marked handled")
	}
}

func TestPromoteSkipsWhenManualHandoffNewer(t *testing.T) {
	h := newHAFix(t)
	h.manual(t, "m1", h.turnAt().Add(time.Minute)) // inside the last turn, or after
	h.draft(t, "t1", "x")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("%d auto lines", n)
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); !os.IsNotExist(err) {
		t.Fatalf("draft kept: %v", err)
	}
}

func TestPromoteWritesWhenTurnAfterManualHandoff(t *testing.T) {
	h := newHAFix(t)
	h.manual(t, "m1", h.turnAt().Add(-time.Hour))
	h.draft(t, "t1", "x")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 1 {
		t.Fatalf("%d auto lines", n)
	}
}

func TestPromoteIdempotent(t *testing.T) {
	h := newHAFix(t)
	h.draft(t, "t1", "x")
	for i := 0; i < 2; i++ {
		if err := h.promote(t, "end"); err != nil {
			t.Fatal(err)
		}
	}
	h.draft(t, "t1", "x again") // same turn re-written: the id blocks the duplicate
	if err := h.promote(t, "gone"); err != nil {
		t.Fatal(err)
	}
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || strings.Contains(got[0].Text, "x again") {
		t.Fatalf("lines = %+v", got)
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); !os.IsNotExist(err) {
		t.Fatalf("draft kept: %v", err)
	}
}

func TestPromoteNoSubjectDropsDraft(t *testing.T) {
	h := newHAFix(t)
	h.draft(t, "t1", "x")
	p := h.peer(t)
	p.SubjectID = ""
	if err := h.rl.promoteHandoffDraft(p, "end", h.clock()); err != nil {
		t.Fatal(err)
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); !os.IsNotExist(err) {
		t.Fatalf("draft kept: %v", err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 || len(h.failures(t)) != 0 {
		t.Fatalf("auto=%d failures=%d", n, len(h.failures(t)))
	}
}

func TestPromoteNeverTouchesManualHandoff(t *testing.T) {
	h := newHAFix(t)
	h.manual(t, "m1", h.turnAt().Add(-time.Hour))
	h.draft(t, "t1", "x")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	last, err := h.st.LatestLogOfKind(h.sub.ID, pigtail.LogHandoff)
	if err != nil || last == nil || last.ID != "m1" || last.Text != "manual m1" {
		t.Fatalf("manual handoff = %+v err=%v", last, err)
	}
}

func TestPromoteFailureKeepsDraftAndJournalsOnce(t *testing.T) {
	h := newHAFix(t)
	var mu sync.Mutex
	var logs []string
	h.rl.svc.Logf = func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	h.rl.appendLogOnce = func(*pigtail.SubjectLogEntry) (bool, error) { return false, errors.New("disk full") }
	h.draft(t, "t1", "x")
	for i := 0; i < 3; i++ {
		if err := h.promote(t, "end"); err == nil {
			t.Fatal("no error")
		}
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); err != nil {
		t.Fatalf("draft lost: %v", err)
	}
	f := h.failures(t)
	if len(f) != 1 || f[0].ID != "handoff-auto:"+h.sid+":t1:failed" || f[0].SubjectID != h.sub.ID || !strings.Contains(f[0].Summary, "disk full") {
		t.Fatalf("failures = %+v", f)
	}
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "handoff-auto") {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d daemon log lines for 3 tries: %v", n, logs)
	}
}

func TestPromoteDoesNotRemoveNewerDraft(t *testing.T) {
	h := newHAFix(t)
	h.draft(t, "t1", "x")
	h.rl.appendLogOnce = func(e *pigtail.SubjectLogEntry) (bool, error) {
		h.draft(t, "t2", "newer") // the hook rewrote it between the read and the removal
		return h.st.AppendLogOnce(e)
	}
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 1 {
		t.Fatalf("%d lines", n)
	}
	d, err := handoffdraft.Read(h.dir, h.sid)
	if err != nil || d.TurnID != "t2" {
		t.Fatalf("newer draft lost: %+v %v", d, err)
	}
}

func TestPromoteResumedSessionNewTurnNewID(t *testing.T) {
	h := newHAFix(t)
	h.draft(t, "t1", "first")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	h.draft(t, "t2", "second")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("lines = %+v", got)
	}
}

func TestPromoteSkipsWrappedOrClosedSubject(t *testing.T) {
	h := newHAFix(t)
	h.wait(t, "wrapped", "", h.turnAt().Add(time.Minute))
	h.draft(t, "t1", "x")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("wrapped: %d lines", n)
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); !os.IsNotExist(err) {
		t.Fatal("wrapped: draft kept")
	}
	// closed subject
	h2 := newHAFix(t)
	sub, _ := h2.st.GetSubject(h2.sub.ID)
	sub.Status = pigtail.StatusDone
	if err := h2.st.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	h2.draft(t, "t1", "x")
	if err := h2.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h2.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("closed: %d lines", n)
	}
	if _, err := handoffdraft.Read(h2.dir, h2.sid); !os.IsNotExist(err) {
		t.Fatal("closed: draft kept")
	}
}

func TestPromoteTurnAtIsTurnStart(t *testing.T) {
	h := newHAFix(t)
	// the done command's case: the manual handoff is written during the last turn, after
	// its start and before its Stop.
	h.manual(t, "m1", h.turnAt().Add(5*time.Minute))
	h.draft(t, "t1", "x")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("%d lines", n)
	}
}

func TestPromoteMissingDraftAfterTurnJournalsFailure(t *testing.T) {
	h := newHAFix(t)
	// negative: no turn recorded
	if err := h.promote(t, "end"); err != nil || len(h.failures(t)) != 0 {
		t.Fatalf("err=%v failures=%d", err, len(h.failures(t)))
	}
	if err := h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop); err != nil {
		t.Fatal(err)
	}
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	f := h.failures(t)
	if len(f) != 1 || f[0].ID != "handoff-auto:"+h.sid+":t1:nodraft" || !strings.Contains(f[0].Summary, "draft missing") {
		t.Fatalf("failures = %+v", f)
	}
	if err := h.promote(t, "end"); err != nil || len(h.failures(t)) != 1 {
		t.Fatalf("second promotion repeated the line: %d", len(h.failures(t)))
	}
	// negative: a manual handoff newer than the turn start
	h2 := newHAFix(t)
	_ = h2.st.RecordTurnEnd(h2.sid, "gx", "t1", h2.turnAt(), h2.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h2.manual(t, "m1", h2.turnAt().Add(30*time.Second))
	if err := h2.promote(t, "end"); err != nil || len(h2.failures(t)) != 0 {
		t.Fatalf("manual covers the turn: err=%v failures=%d", err, len(h2.failures(t)))
	}
}

func TestPromoteStaleWrappedDoesNotSkip(t *testing.T) {
	h := newHAFix(t)
	h.wait(t, "wrapped", "", h.turnAt().Add(-time.Hour)) // opened before the turn
	h.draft(t, "t1", "x")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 1 {
		t.Fatalf("%d lines", n)
	}
}

func TestPromoteSecondEndAfterSkipNoFailure(t *testing.T) {
	h := newHAFix(t)
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h.manual(t, "m1", h.turnAt().Add(time.Second))
	h.draft(t, "t1", "x")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if h.logOf(t, pigtail.LogHandoffAuto) != nil || len(h.failures(t)) != 0 || handoffdraft.ReadHandled(h.dir, h.sid) != "t1" {
		t.Fatalf("auto=%d failures=%d handled=%q", len(h.logOf(t, pigtail.LogHandoffAuto)), len(h.failures(t)), handoffdraft.ReadHandled(h.dir, h.sid))
	}
}

func TestPromoteSecondEndAfterPromoteNoFailure(t *testing.T) {
	h := newHAFix(t)
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h.draft(t, "t1", "x")
	for i := 0; i < 2; i++ {
		if err := h.promote(t, "end"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 1 || len(h.failures(t)) != 0 {
		t.Fatalf("auto=%d failures=%d", n, len(h.failures(t)))
	}
	if ok, _ := h.st.LogExists(autoID(h, "t1")); !ok {
		t.Fatal("id missing")
	}
}

func TestPromoteNoDraftPreInstallTurnsNoFailure(t *testing.T) {
	h := newHAFix(t)
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t0", h.turnAt().Add(-time.Hour), h.turnAt().Add(-50*time.Minute), pigtail.TurnEventStop)
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h.manual(t, "m1", h.turnAt().Add(30*time.Second))
	if err := h.promote(t, "end"); err != nil || len(h.failures(t)) != 0 {
		t.Fatalf("err=%v failures=%d", err, len(h.failures(t)))
	}
	// positive control: without the manual handoff the line is written
	h2 := newHAFix(t)
	_ = h2.st.RecordTurnEnd(h2.sid, "gx", "t1", h2.turnAt(), h2.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	if err := h2.promote(t, "end"); err != nil || len(h2.failures(t)) != 1 {
		t.Fatalf("control: err=%v failures=%d", err, len(h2.failures(t)))
	}
}

func TestPromoteGoneReadsKeptSubjectWait(t *testing.T) {
	h := newHAFix(t)
	h.wait(t, "review", "PR #7 · flip la PR ?", h.turnAt().Add(2*time.Minute))
	if _, _, err := h.st.EndPeerClosing(h.sid, h.clock()); err != nil {
		t.Fatal(err)
	}
	// control: the two reads the old order used see nothing
	if ws, _ := h.st.SessionWaits(h.sid); len(ws) != 0 {
		t.Fatalf("control: session_waits = %+v", ws)
	}
	if rs, _ := h.st.ListRecentClosedWaitRecords(store.WaitRecordFilter{SessionID: h.sid, Scope: scope.Any}, 1); len(rs) != 0 {
		t.Fatalf("control: closed records = %+v", rs)
	}
	if rs, _ := h.st.ListOpenWaitRecords(store.WaitRecordFilter{SessionID: h.sid, Scope: scope.Any, IncludeSnoozed: true}); len(rs) != 1 {
		t.Fatalf("control: open records = %+v", rs)
	}
	h.draft(t, "t1", "x")
	if err := h.promote(t, "gone"); err != nil {
		t.Fatal(err)
	}
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || !strings.Contains(got[0].Text, "review — PR #7") {
		t.Fatalf("lines = %+v", got)
	}
}

func TestPromoteCorruptDraftMovedAside(t *testing.T) {
	h := newHAFix(t)
	if err := os.WriteFile(filepath.Join(h.dir, h.sid+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, h.sid+".json.bad")); err != nil {
		t.Fatalf("no .bad: %v", err)
	}
	ids, _ := handoffdraft.List(h.dir)
	if len(ids) != 0 || len(h.logOf(t, pigtail.LogHandoffAuto)) != 0 {
		t.Fatalf("ids=%v", ids)
	}
}

func TestComposeBoundsText(t *testing.T) {
	d := handoffdraft.Draft{SessionID: "s", TurnID: "t", TurnAt: time.Now(), TLDR: strings.Repeat("é", 30000)}
	out := composeHandoffAuto(d, pigtail.PeerSession{SessionID: "s"}, nil, "", "end")
	if len(out) > 8000 || !utf8.ValidString(out) {
		t.Fatalf("len=%d valid=%v", len(out), utf8.ValidString(out))
	}
	d = handoffdraft.Draft{SessionID: "s", TurnID: "t", TurnAt: time.Now(), Tail: "queue du message"}
	out = composeHandoffAuto(d, pigtail.PeerSession{SessionID: "s"}, nil, "", "gone")
	if !strings.Contains(out, "--- fin du dernier message ---") || !strings.Contains(out, "queue du message") {
		t.Fatalf("tail fallback missing:\n%s", out)
	}
}

func TestComposeFindsPRInWaitReason(t *testing.T) {
	d := handoffdraft.Draft{SessionID: "s", TurnID: "t", TurnAt: time.Now(), TLDR: "x"}
	w := &pigtail.SessionWait{Type: "review", Reason: "PR #142 · flip la PR ?", Source: "declared"}
	out := composeHandoffAuto(d, pigtail.PeerSession{SessionID: "s"}, w, "", "end")
	if !strings.Contains(out, "PR: #142") {
		t.Fatalf("no PR:\n%s", out)
	}
	out = composeHandoffAuto(d, pigtail.PeerSession{SessionID: "s"}, &pigtail.SessionWait{Type: "review", Reason: "plain"}, "", "end")
	if strings.Contains(out, "PR:") {
		t.Fatalf("PR invented:\n%s", out)
	}
}

// --- Task 5: the three triggers ------------------------------------------------

func (h *haFix) setAlive(v *bool) {
	h.rl.cfg.Alive = func(p pigtail.PeerSession) bool { return p.EndedAt == nil && *v }
}

func TestEndPeerPromotesDraftBeforeClosingWaits(t *testing.T) {
	h := newHAFix(t)
	h.wait(t, "review", "PR #9 · flip la PR ?", h.turnAt().Add(time.Minute))
	h.draft(t, "t1", "x")
	if err := h.rl.endPeer(h.peer(t), h.clock()); err != nil {
		t.Fatal(err)
	}
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || !strings.Contains(got[0].Text, "review — PR #9") {
		t.Fatalf("lines = %+v", got)
	}
	if ws, _ := h.st.SessionWaits(h.sid); len(ws) != 0 {
		t.Fatalf("waits not closed: %+v", ws)
	}
}

func TestSweepPromotesGoneAfterConfirm(t *testing.T) {
	h := newHAFix(t)
	alive := false
	h.setAlive(&alive)
	h.draft(t, "t1", "x")
	h.rl.sweepHandoffDrafts(h.clock(), false)
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("first reading promoted: %d", n)
	}
	h.advance(goneRecipientConfirm)
	h.rl.sweepHandoffDrafts(h.clock(), false)
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || !strings.Contains(got[0].Text, "fin: gone") {
		t.Fatalf("lines = %+v", got)
	}
}

func TestSweepAliveSessionKeepsDraft(t *testing.T) {
	h := newHAFix(t)
	alive := true
	h.setAlive(&alive)
	h.draft(t, "t1", "x")
	for i := 0; i < 3; i++ {
		h.rl.sweepHandoffDrafts(h.clock(), false)
		h.advance(time.Minute)
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); err != nil || len(h.logOf(t, pigtail.LogHandoffAuto)) != 0 {
		t.Fatalf("draft=%v", err)
	}
	// one unreadable reading must not accumulate: gone once, alive, gone once
	alive = false
	h.rl.sweepHandoffDrafts(h.clock(), false)
	alive = true
	h.advance(time.Minute)
	h.rl.sweepHandoffDrafts(h.clock(), false)
	alive = false
	h.advance(time.Minute)
	h.rl.sweepHandoffDrafts(h.clock(), false)
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("a flapping reading promoted: %d", n)
	}
}

func TestSweepStartupConfirmsAtFirstTick(t *testing.T) {
	h := newHAFix(t)
	alive := false
	h.setAlive(&alive)
	h.draft(t, "t1", "x")
	h.rl.sweepHandoffDrafts(h.clock(), true)
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("startup promoted at once: %d", n)
	}
	h.advance(time.Minute)
	h.rl.sweepHandoffDrafts(h.clock(), false)
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || !strings.Contains(got[0].Text, "fin: startup") {
		t.Fatalf("lines = %+v", got)
	}
}

func TestSweepStartupPromotesEndedAtOnce(t *testing.T) {
	h := newHAFix(t)
	h.draft(t, "t1", "x")
	if _, _, err := h.st.EndPeerClosing(h.sid, h.clock()); err != nil { // SessionEnd arrived, promotion lost
		t.Fatal(err)
	}
	h.rl.sweepHandoffDrafts(h.clock(), true)
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 1 {
		t.Fatalf("%d lines", n)
	}
}

func TestSweepEndedPeerPromotes(t *testing.T) {
	h := newHAFix(t)
	h.draft(t, "t1", "x")
	_, _, _ = h.st.EndPeerClosing(h.sid, h.clock())
	h.rl.sweepHandoffDrafts(h.clock(), false)
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || !strings.Contains(got[0].Text, "fin: end") {
		t.Fatalf("lines = %+v", got)
	}
}

func TestSweepEndedPeerWrappedKeptOpenSkips(t *testing.T) {
	h := newHAFix(t)
	h.wait(t, "wrapped", "", h.turnAt().Add(time.Minute))
	if _, _, err := h.st.EndPeerClosing(h.sid, h.clock()); err != nil {
		t.Fatal(err)
	}
	// control: after the end the wrap is no session_waits row; it is only a
	// closed record (wrapped is session-owned, so it is not kept open).
	if ws, _ := h.st.SessionWaits(h.sid); len(ws) != 0 {
		t.Fatalf("control: session_waits = %+v", ws)
	}
	if rs, _ := h.st.ListRecentClosedWaitRecords(store.WaitRecordFilter{SessionID: h.sid, Scope: scope.Any}, 1); len(rs) != 1 || rs[0].Type != "wrapped" {
		t.Fatalf("control: closed records = %+v", rs)
	}
	h.draft(t, "t1", "x")
	h.rl.sweepHandoffDrafts(h.clock(), false)
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("%d lines", n)
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); !os.IsNotExist(err) {
		t.Fatal("draft kept")
	}
}

func TestSweepUnknownPeerKeepsDraft(t *testing.T) {
	h := newHAFix(t)
	if err := handoffdraft.Write(h.dir, handoffdraft.Draft{SessionID: "ghost", TurnID: "t1", TurnAt: h.turnAt()}); err != nil {
		t.Fatal(err)
	}
	h.rl.sweepHandoffDrafts(h.clock(), false)
	if _, err := handoffdraft.Read(h.dir, "ghost"); err != nil {
		t.Fatalf("draft of an unknown peer lost: %v", err)
	}
}

func TestSweepPurgesOrphanDraftAfter30Days(t *testing.T) {
	h := newHAFix(t)
	var logs []string
	h.rl.svc.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	for _, c := range []struct {
		id   string
		age  time.Duration
		kept bool
	}{{"old", 31 * 24 * time.Hour, false}, {"young", 29 * 24 * time.Hour, true}} {
		_ = handoffdraft.Write(h.dir, handoffdraft.Draft{SessionID: c.id, TurnID: "t", TurnAt: h.turnAt()})
		mt := h.clock().Add(-c.age)
		if err := os.Chtimes(filepath.Join(h.dir, c.id+".json"), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	h.rl.sweepHandoffDrafts(h.clock(), false)
	if _, err := handoffdraft.Read(h.dir, "old"); !os.IsNotExist(err) {
		t.Fatalf("old kept: %v", err)
	}
	if _, err := handoffdraft.Read(h.dir, "young"); err != nil {
		t.Fatalf("young purged: %v", err)
	}
	purged := false
	for _, l := range logs {
		if strings.Contains(l, "old") && strings.Contains(l, "purged") {
			purged = true
		}
	}
	if !purged {
		t.Fatalf("no purge log line: %v", logs)
	}
}

// Documents the known limit (risk 1): a pause undone after the promotion
// leaves the line, which a later manual handoff masks at read time.
func TestPauseUndoMayLeaveAutoHandoff(t *testing.T) {
	h := newHAFix(t)
	h.draft(t, "t1", "x")
	if err := h.rl.endPeer(h.peer(t), h.clock()); err != nil {
		t.Fatal(err)
	}
	h.peers(t, pigtail.PeerSession{SessionID: h.sid, Cwd: "/wt/handoff-auto/cmd", Scope: "gx", CellID: "w1"}) // the session went on
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 1 {
		t.Fatalf("%d lines", n)
	}
}

func TestIdlePauseStillEndsPeerWhenPromotionFails(t *testing.T) {
	h := newHAFix(t)
	h.rl.appendLogOnce = func(*pigtail.SubjectLogEntry) (bool, error) { return false, errors.New("boom") }
	h.draft(t, "t1", "x")
	if err := h.rl.endPeer(h.peer(t), h.clock()); err != nil {
		t.Fatalf("endPeer failed with the promotion: %v", err)
	}
	if p := h.peer(t); p.EndedAt == nil {
		t.Fatal("peer not ended")
	}
}

func TestSweepPurgesOldHandledAndBadFiles(t *testing.T) {
	h := newHAFix(t)
	old := h.clock().Add(-31 * 24 * time.Hour)
	for _, n := range []string{"x.handled", "y.json.bad"} {
		p := filepath.Join(h.dir, n)
		if err := os.WriteFile(p, []byte("t"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	h.rl.sweepHandoffDrafts(h.clock(), false)
	for _, n := range []string{"x.handled", "y.json.bad"} {
		if _, err := os.Stat(filepath.Join(h.dir, n)); !os.IsNotExist(err) {
			t.Fatalf("%s kept after 30 days: %v", n, err)
		}
	}
}

func TestPromoteSkipsWrapSkillTurn(t *testing.T) {
	h := newHAFix(t)
	// a typed done command sets the skill at UserPromptExpansion, a few
	// hundred ms BEFORE the transcript's first promptId entry (the draft's TurnAt)
	if err := h.st.SetSessionSkill(h.sid, "finish", h.turnAt().Add(-300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h.draft(t, "t1", "wrapped by skill")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 0 {
		t.Fatalf("a done command turn was promoted (%d lines)", n)
	}
	if n := len(h.failures(t)); n != 0 {
		t.Fatalf("a done command turn journaled %d failures", n)
	}
	if _, err := handoffdraft.Read(h.dir, h.sid); !os.IsNotExist(err) {
		t.Fatalf("draft kept: %v", err)
	}
	// control: another skill's turn is promoted
	h2 := newHAFix(t)
	_ = h2.st.SetSessionSkill(h2.sid, "my-skill", h2.turnAt())
	_ = h2.st.RecordTurnEnd(h2.sid, "gx", "t1", h2.turnAt(), h2.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h2.draft(t, "t1", "x")
	if err := h2.promote(t, "end"); err != nil || len(h2.logOf(t, pigtail.LogHandoffAuto)) != 1 {
		t.Fatalf("control: err=%v", err)
	}
	// no done command set: the same skill's turn is promoted like any other
	h3 := newHAFix(t)
	h3.rl.cfg.DoneCommand = ""
	_ = h3.st.SetSessionSkill(h3.sid, "finish", h3.turnAt().Add(-300*time.Millisecond))
	_ = h3.st.RecordTurnEnd(h3.sid, "gx", "t1", h3.turnAt(), h3.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h3.draft(t, "t1", "x")
	if err := h3.promote(t, "end"); err != nil || len(h3.logOf(t, pigtail.LogHandoffAuto)) != 1 {
		t.Fatalf("unset: err=%v", err)
	}
}

func TestPromoteStaleDraftMarkedAndJournalsLaterTurn(t *testing.T) {
	h := newHAFix(t)
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t2", h.turnAt().Add(2*time.Minute), h.turnAt().Add(3*time.Minute), pigtail.TurnEventStop)
	h.draft(t, "t1", "first turn only") // the hook failed to write t2's draft
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || !strings.Contains(got[0].Text, "first turn only") || !strings.Contains(got[0].Text, "tour antérieur") {
		t.Fatalf("lines = %+v", got)
	}
	f := h.failures(t)
	if len(f) != 1 || f[0].ID != "handoff-auto:"+h.sid+":t2:nodraft" {
		t.Fatalf("failures = %+v", f)
	}
	// control: the up-to-date draft carries no mark and no failure
	h2 := newHAFix(t)
	_ = h2.st.RecordTurnEnd(h2.sid, "gx", "t1", h2.turnAt(), h2.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h2.draft(t, "t1", "current")
	if err := h2.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	g2 := h2.logOf(t, pigtail.LogHandoffAuto)
	if len(g2) != 1 || strings.Contains(g2[0].Text, "tour antérieur") || len(h2.failures(t)) != 0 {
		t.Fatalf("control: %+v failures=%d", g2, len(h2.failures(t)))
	}
}

func TestPromoteDraftWithoutTurnRowIsNotStale(t *testing.T) {
	h := newHAFix(t)
	// the previous turn is recorded; the draft's turn (pigtaild was down) is not
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t0", h.turnAt().Add(-time.Hour), h.turnAt().Add(-50*time.Minute), pigtail.TurnEventStop)
	h.draft(t, "t1", "current turn")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	got := h.logOf(t, pigtail.LogHandoffAuto)
	if len(got) != 1 || strings.Contains(got[0].Text, "tour antérieur") {
		t.Fatalf("lines = %+v", got)
	}
	if n := len(h.failures(t)); n != 0 {
		t.Fatalf("%d false failures", n)
	}
}

func TestPromoteWrapSkillOfEarlierTurnDoesNotDropLaterTurn(t *testing.T) {
	h := newHAFix(t)
	// a done command turn long ago; the skill row is never cleared, so the
	// plain turn that follows still reads skill=finish on its turn-end row.
	if err := h.st.SetSessionSkill(h.sid, "finish", h.turnAt().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop)
	h.draft(t, "t1", "plain turn after a wrap")
	if err := h.promote(t, "end"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.logOf(t, pigtail.LogHandoffAuto)); n != 1 {
		t.Fatalf("plain turn after a wrap turn dropped (%d lines)", n)
	}
}

func TestPromoteWrapSkillAfterPreviousTurnEndDrops(t *testing.T) {
	for _, c := range []struct {
		skillAt time.Duration // relative to the draft's TurnAt
		drop    bool
	}{{-30 * time.Minute, true}, {-2 * time.Hour, false}} {
		h := newHAFix(t)
		// the previous turn ended 1 h before this one began
		_ = h.st.RecordTurnEnd(h.sid, "gx", "t0", h.turnAt().Add(-70*time.Minute), h.turnAt().Add(-time.Hour), pigtail.TurnEventStop)
		_ = h.st.SetSessionSkill(h.sid, "finish", h.turnAt().Add(c.skillAt))
		_ = h.st.RecordTurnEnd(h.sid, "gx", "t1", h.turnAt(), h.turnAt().Add(time.Minute), pigtail.TurnEventStop)
		h.draft(t, "t1", "x")
		if err := h.promote(t, "end"); err != nil {
			t.Fatal(err)
		}
		if n := len(h.logOf(t, pigtail.LogHandoffAuto)); (n == 0) != c.drop {
			t.Fatalf("skill at %v: %d lines, drop=%v", c.skillAt, n, c.drop)
		}
	}
}

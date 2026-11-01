package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const respawnTestDetail = "gave up after 10m0s: background shell: `npm run dev` (12m) — the session was not touched"

func respawnReq(state, detail string) pigtail.CmuxRespawnRequest {
	return pigtail.CmuxRespawnRequest{State: state, SurfaceID: "5E5E5E5E-0000-4000-8000-0000000000A1",
		RunID: "20261013T101000.000-4242", SubjectID: "sub_p", SessionID: "s-perso", Detail: detail}
}

// respawnTrace is what one notify left: journal lines of kind, waits and
// pending messages of s-perso.
func respawnTrace(t *testing.T, f *relayFixture, kind string) ([]pigtail.JournalEntry, []pigtail.SessionWait, []pigtail.Message) {
	t.Helper()
	js, err := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.st.SessionWaits("s-perso")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := f.st.ListMessages(pigtail.MessageFilter{To: "s-perso"})
	if err != nil {
		t.Fatal(err)
	}
	return js, ws, ms
}

func TestNotifyRespawnEndTellsTheSession(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnAbandoned, respawnTestDetail))
	if got != (pigtail.CmuxRespawnNotice{Journal: "ok", Wait: "none", Notice: "ok"}) {
		t.Fatalf("notice = %+v", got)
	}
	js, ws, ms := respawnTrace(t, f, "respawn-abandoned")
	if len(js) != 1 || js[0].ID != "respawn:20261013T101000.000-4242:abandoned" || js[0].SubjectID != "sub_p" ||
		js[0].Source != "pigtail:respawn/20261013T101000.000-4242?session=s-perso" || !strings.Contains(js[0].Summary, "`npm run dev` (12m)") {
		t.Fatalf("journal = %+v", js)
	}
	if len(ws) != 0 {
		t.Fatalf("an abandoned respawn poses no wait: %+v", ws)
	}
	if len(ms) != 1 || ms[0].FromSession != pigtail.SystemSender || ms[0].Kind != pigtail.MessageKindInfo ||
		ms[0].State != pigtail.MessageStatePending || !strings.Contains(ms[0].Body, "20261013T101000.000-4242") ||
		!strings.Contains(ms[0].Body, "`npm run dev` (12m)") || !strings.Contains(ms[0].Body, "tell them now") ||
		!strings.Contains(ms[0].Body, "pigtail alerts ack 20261013T101000.000-4242") {
		t.Fatalf("messages = %+v", ms)
	}
	if _, err := os.Stat(filepath.Join(f.marker, "s-perso")); err != nil {
		t.Fatalf("inbox marker: %v (the prompt hook would never claim it)", err)
	}
}

func TestNotifyRespawnEndIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	req := respawnReq(pigtail.CmuxRespawnFailed, "SIGTERM to Claude (pid 4242) failed: denied — nothing relaunched")
	f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, req)
	again := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, req)
	if again != (pigtail.CmuxRespawnNotice{Journal: "duplicate", Wait: "duplicate", Notice: "duplicate"}) {
		t.Fatalf("second notify = %+v", again)
	}
	js, ws, ms := respawnTrace(t, f, "respawn-failed")
	if len(js) != 1 || len(ws) != 1 || len(ms) != 1 {
		t.Fatalf("after two notifies: %d lines, %d waits, %d messages — want one each", len(js), len(ws), len(ms))
	}
}

func TestNotifyRespawnEndSessionGone(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s-perso"}}}) // a perso session ends as perso
	got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, "the session ended on its own before the respawn — nothing relaunched"))
	if got.Journal != "ok" || !strings.HasPrefix(got.Wait, "skipped: ") || !strings.HasPrefix(got.Notice, "skipped: ") {
		t.Fatalf("notice = %+v", got)
	}
	js, ws, ms := respawnTrace(t, f, "respawn-failed")
	if len(js) != 1 || !strings.HasSuffix(js[0].Summary, "— no live session to tell") || len(ws) != 0 || len(ms) != 0 ||
		js[0].Source != "pigtail:respawn/20261013T101000.000-4242" { // nobody told: no session to ack as
		t.Fatalf("journal %+v, waits %+v, messages %+v", js, ws, ms)
	}
	// Another scope's session reads as unknown: same answer, nothing told.
	g := newRelayFixture(t)
	standardPeers(g, t)
	got = g.rl.NotifyRespawnEnd(scope.Canonical("globex"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnAbandoned, "x"))
	if !strings.HasPrefix(got.Notice, "skipped: ") {
		t.Fatalf("cross-scope target: notice = %+v", got)
	}
}

func TestNotifyRespawnEndWithholdsSecret(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, "gave up: `curl -H 'Authorization: Bearer abcdefgh12345'` (3m)"))
	js, ws, ms := respawnTrace(t, f, "respawn-failed")
	for _, s := range []string{js[0].Summary, ws[0].Reason, ms[0].Body} {
		if strings.Contains(s, "abcdefgh12345") || !strings.Contains(s, "withheld") {
			t.Fatalf("secret leaked or not marked: %q", s)
		}
	}
}

func TestNotifyRespawnEndClipsLongDetail(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	long := "no fresh Claude in surface:7 after 30s — the tab shows: " + strings.Repeat("é", 700) // 1400+ bytes
	got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, long))
	if got != (pigtail.CmuxRespawnNotice{Journal: "ok", Wait: "ok", Notice: "ok"}) {
		t.Fatalf("notice = %+v", got)
	}
	js, ws, ms := respawnTrace(t, f, "respawn-failed")
	if n := utf8.RuneCountInString(js[0].Summary); n > 280 || !utf8.ValidString(js[0].Summary) {
		t.Fatalf("summary: %d runes", n)
	}
	if len(ws[0].Reason) > waittype.MaxReasonBytes || !utf8.ValidString(ws[0].Reason) {
		t.Fatalf("reason: %d bytes", len(ws[0].Reason))
	}
	if !utf8.ValidString(ms[0].Body) || len(ms[0].Body) > 2048 {
		t.Fatalf("body: %d bytes", len(ms[0].Body))
	}
}

// Review Focus 11: a respawn armed with --cross-scope — the master cell on a
// work subject. The line takes the subject's scope (POST /journal would refuse
// any other), and the message names the --cross-scope the ack will need.
func TestNotifyRespawnEndCrossScopeTakesSubjectScope(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	sub := standardPeers(f, t) // a work subject whose worktree is /wt/feat-session-relay
	f.peers(t, pigtail.PeerSession{SessionID: "s-master-on-work", Cwd: "/wt/feat-session-relay", Scope: "perso", CellID: "mp"})
	req := respawnReq(pigtail.CmuxRespawnAbandoned, "gave up after 10m0s: draft in the input")
	req.SubjectID, req.SessionID = sub.ID, "s-master-on-work"
	if got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), true, cmuxWS, req); got.Journal != "ok" || got.Notice != "ok" {
		t.Fatalf("notice = %+v", got)
	}
	subScope, err := f.st.SubjectScope(sub.ID)
	if err != nil || subScope == scope.Canonical("perso") {
		t.Fatalf("fixture: the subject must sit outside perso, got %q %v", subScope, err)
	}
	js, _, _ := respawnTrace(t, f, "respawn-abandoned")
	if len(js) != 1 || js[0].Scope != string(subScope) {
		t.Fatalf("journal = %+v, want one line in the subject's scope %s", js, subScope)
	}
	ms, err := f.st.ListMessages(pigtail.MessageFilter{To: "s-master-on-work"})
	if err != nil || len(ms) != 1 || !strings.Contains(ms[0].Body, "pigtail alerts ack "+req.RunID+" --cross-scope") {
		t.Fatalf("messages = %+v, %v", ms, err)
	}
	// The same run without --cross-scope: the master may not write the work
	// line — refused and said; the session is told all the same.
	g := newRelayFixture(t)
	sub = standardPeers(g, t)
	g.peers(t, pigtail.PeerSession{SessionID: "s-master-on-work", Cwd: "/wt/feat-session-relay", Scope: "perso", CellID: "mp"})
	req.SubjectID = sub.ID
	got := g.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, req)
	if !strings.HasPrefix(got.Journal, "failed: ") || got.Notice != "ok" {
		t.Fatalf("no cross: notice = %+v", got)
	}
	if js, _, _ := respawnTrace(t, g, "respawn-abandoned"); len(js) != 0 {
		t.Fatalf("no cross: a line was written: %+v", js)
	}
}

// CA7: every notice failure is in the daemon's log (Task 6 puts it in the record).
func TestNotifyRespawnEndLogsEveryFailure(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	var logged []string
	f.rl.svc.Logf = func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }
	has := func(sub string) bool {
		for _, l := range logged {
			if strings.Contains(l, sub) {
				return true
			}
		}
		return false
	}
	doAs(t, f.r, perso, "POST", "/peers", pigtail.PeersRequest{End: true, Peers: []pigtail.PeerSession{{SessionID: "s-perso"}}}) // a perso session ends as perso
	f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, "d"))
	if !has("respawn 20261013T101000.000-4242 failed: nobody told") || !has("is gone") {
		t.Fatalf("session gone, log = %q", logged)
	}
	// A store that fails: the journal line is not written, and the log says so.
	logged = nil
	_ = f.st.Close()
	got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnAbandoned, "d"))
	if !strings.HasPrefix(got.Journal, "failed: ") || !has("journal line not written") {
		t.Fatalf("store down: notice %+v, log %q", got, logged)
	}
}

// The wait must not hold the idle wake (an auto wait that is not a failure):
// a declared one would leave the notice unread in a session with no watcher.
func TestRespawnWaitDoesNotHoldTheWake(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, "x"))
	_, ws, _ := respawnTrace(t, f, "respawn-failed")
	c := relay.Candidate{SurfaceID: "S", NativeOK: true, Alive: true, Native: claudesession.Native{Status: "shell", Kind: "interactive"}, Waits: ws, Now: f.clock()}
	if ok, why := relay.Eligible(c); !ok {
		t.Fatalf("respawn wait holds the wake: %s", why)
	}
}

// Zone already regressed (sub_x000000000000042, 2026-10-08: « deliver jette la
// raison de rl.wakeable »): the notice does not go through deliver, it queues
// the message and leaves the typing to the idle sweep. Proven end to end on
// the real sweep (fakeWake, relay_wake_test.go): the session, idle or in the
// native state "shell" of an abandon, is typed into once — and a failed cmux
// listing is logged with its reason, never swallowed.
func TestRespawnNoticeWakesTheIdleSession(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"idle", "shell"} {
		t.Run(status, func(t *testing.T) {
			f, fw, lg := newWakeFixture(t)
			n := fw.natives[7]
			n.Status = status
			fw.natives[7] = n
			f.wakePeers(t, alphaPeer())
			req := respawnReq(pigtail.CmuxRespawnAbandoned, "gave up after 10m0s: background shell: `npm run dev` (12m)")
			req.SubjectID, req.SessionID = "", "s-alpha"
			if got := f.rl.NotifyRespawnEnd(scope.Canonical("gx"), false, cmuxWS, req); got.Notice != "ok" || got.Wait != "none" {
				t.Fatalf("notice = %+v", got)
			}
			f.rl.sweepIdle()
			if !reflect.DeepEqual(fw.sent, []string{surf}) {
				t.Fatalf("sent = %v, want one typing in %s — the respawn wait or the system sender held the wake\n%s", fw.sent, surf, lg)
			}
		})
	}
	// The sweep's failure is logged with its reason (the 2026-10-08 defect).
	f, fw, lg := newWakeFixture(t)
	fw.listErr = errors.New("cmux: socket refused")
	f.wakePeers(t, alphaPeer())
	req := respawnReq(pigtail.CmuxRespawnFailed, "d")
	req.SubjectID, req.SessionID = "", "s-alpha"
	f.rl.NotifyRespawnEnd(scope.Canonical("gx"), false, cmuxWS, req)
	f.rl.sweepIdle()
	if len(fw.sent) != 0 || lg.count("wake: s-alpha: cmux sessions list: cmux: socket refused") != 1 {
		t.Fatalf("listing failed: sent %v, log:\n%s", fw.sent, lg)
	}
}

// CA7: a wait or a message the store refuses is said twice — in the report
// (so the worker's record shows `⚠ notice incomplete (wait failed: …)`) and
// in the daemon's log — and the other writes still happen. A real store error
// on one table (a trigger), not a closed store: the journal line is written.
func TestNotifyRespawnEndReportsWaitAndMessageFailures(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ table, part, log string }{
		{"session_waits", "wait", "wait not posed on s-perso"},
		{"messages", "notice", "message to s-perso not created"},
	} {
		t.Run(c.table, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pigtail.db")
			f := newRelayFixtureAt(t, path)
			standardPeers(f, t)
			var logged []string
			f.rl.svc.Logf = func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }
			failInserts(t, path, c.table)
			got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, "d"))
			part := map[string]string{"wait": got.Wait, "notice": got.Notice}[c.part]
			if got.Journal != "ok" || !strings.HasPrefix(part, "failed: ") || !strings.Contains(part, c.table+" refused") ||
				!strings.Contains(got.Problems(), c.part+" failed: ") {
				t.Fatalf("notice = %+v, Problems = %q", got, got.Problems())
			}
			if !strings.Contains(strings.Join(logged, "\n"), c.log) {
				t.Fatalf("log = %q, want %q", logged, c.log)
			}
			js, ws, ms := respawnTrace(t, f, "respawn-failed")
			if len(js) != 1 || (c.table == "session_waits" && (len(ws) != 0 || len(ms) != 1)) || (c.table == "messages" && (len(ws) != 1 || len(ms) != 0)) {
				t.Fatalf("after a %s failure: %d lines, %d waits, %d messages", c.table, len(js), len(ws), len(ms))
			}
		})
	}
}

// An abandoned run poses no wait, so a failing message is the only failure:
// the wait answers none and the record names the notice.
func TestNotifyRespawnEndAbandonedMessageFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pigtail.db")
	f := newRelayFixtureAt(t, path)
	standardPeers(f, t)
	failInserts(t, path, "messages")
	got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnAbandoned, "d"))
	if got.Journal != "ok" || got.Wait != "none" || !strings.HasPrefix(got.Notice, "failed: ") || !strings.Contains(got.Problems(), "notice failed") {
		t.Fatalf("notice = %+v, Problems = %q", got, got.Problems())
	}
	js, ws, ms := respawnTrace(t, f, "respawn-abandoned")
	if len(js) != 1 || len(ws) != 0 || len(ms) != 0 {
		t.Fatalf("%d lines, %d waits, %d messages", len(js), len(ws), len(ms))
	}
}

// D2b: an abandoned respawn leaves the session's own wait alone; a failed one
// still poses its blocked wait.
func TestNotifyRespawnEndAbandonedKeepsTheSessionWait(t *testing.T) {
	t.Parallel()
	seed := func(f *relayFixture) {
		if _, err := f.st.SetSessionWaitRecord("s-perso", pigtail.SessionWait{Type: string(waittype.Permission), Reason: "Bash",
			Source: pigtail.WaitSourceAuto, Since: f.clock()}, store.WaitMeta{Scope: "perso", Details: []byte(`{"cmd":"make"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	f := newRelayFixture(t)
	standardPeers(f, t)
	seed(f)
	got := f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnAbandoned, "x"))
	_, ws, _ := respawnTrace(t, f, "respawn-abandoned")
	if got.Wait != "none" || len(ws) != 1 || ws[0].Type != string(waittype.Permission) {
		t.Fatalf("abandoned: notice %+v, waits %+v, want the Permission kept", got, ws)
	}
	g := newRelayFixture(t)
	standardPeers(g, t)
	seed(g)
	got = g.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, "x"))
	_, ws, _ = respawnTrace(t, g, "respawn-failed")
	if got.Wait != "ok" || len(ws) != 1 || ws[0].Type != string(waittype.Blocked) || !strings.HasPrefix(ws[0].Reason, "respawn failed: x") {
		t.Fatalf("failed: notice %+v, waits %+v, want the blocked wait", got, ws)
	}
}

// failInserts makes every INSERT into table fail from now on (a store error
// on that write alone). The store's driver (modernc sqlite) is registered by
// internal/daemon/store.
func failInserts(t *testing.T, path, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER test_fail_` + table + ` BEFORE INSERT ON ` + table +
		` BEGIN SELECT RAISE(ABORT, 'test: ` + table + ` refused'); END`); err != nil {
		t.Fatal(err)
	}
}

// The secret check runs on the whole detail, before the 1500-byte clip: a token
// straddling the cut must not leave as a partial prefix.
func TestRespawnDetailSecretStraddlingTheClipIsWithheld(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	secret := "sk-" + strings.Repeat("A1b2C3d4", 8)
	detail := strings.Repeat("x", respawnDetailMax-12) + " " + secret + " tail"
	req := respawnReq(pigtail.CmuxRespawnFailed, detail)
	if msg := normalizeRespawnRequest(&req); msg != "" {
		t.Fatal(msg)
	}
	f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, req)
	js, ws, ms := respawnTrace(t, f, "respawn-failed")
	for _, s := range []string{js[0].Summary, ws[0].Reason, ms[0].Body} {
		if strings.Contains(s, "A1b2") || !strings.Contains(s, "withheld") {
			t.Fatalf("secret prefix leaked or not marked: %q", s)
		}
	}
}

// The respawn's blocked auto wait replaces an open dialog record of the same
// session (one open record per source): the replaced record is told
// (wait.closed replaced), and blocked opens none — it is not a dialog type.
func TestRespawnBlockedClosesOpenDialogRecord(t *testing.T) {
	t.Parallel()
	f := newRelayFixture(t)
	standardPeers(f, t)
	f.rl.svc.Bus = events.New()
	tr, err := f.st.SetSessionWaitRecord("s-perso", pigtail.SessionWait{Type: string(waittype.Permission), Reason: "Bash",
		Source: pigtail.WaitSourceAuto, Since: f.clock()}, store.WaitMeta{Scope: "perso", Details: []byte(`{"cmd":"make"}`)})
	if err != nil || tr.OpenedID == "" {
		t.Fatalf("seed: %+v, %v", tr, err)
	}
	f.rl.NotifyRespawnEnd(scope.Canonical("perso"), false, cmuxWS, respawnReq(pigtail.CmuxRespawnFailed, "x"))
	evs := f.rl.svc.Bus.Replay("", []string{"wait.*"})
	if len(evs) != 1 || evs[0].Type != pigtail.EventWaitClosed || evs[0].Data["wait_id"] != tr.OpenedID ||
		evs[0].Data["state"] != pigtail.WaitStateReplaced {
		t.Fatalf("wait events = %+v", evs)
	}
	old, err := f.st.GetWaitRecord(tr.OpenedID)
	if err != nil || old.State != pigtail.WaitStateReplaced {
		t.Fatalf("old record = %+v, %v", old, err)
	}
	if ids, _ := f.st.OpenWaitRecordIDs("s-perso"); len(ids) != 0 {
		t.Fatalf("open records = %v, want none (blocked is no dialog)", ids)
	}
	_, ws, _ := respawnTrace(t, f, "respawn-failed")
	if len(ws) != 1 || ws[0].Type != string(waittype.Blocked) || ws[0].Source != pigtail.WaitSourceAuto || ws[0].ID != "" {
		t.Fatalf("waits = %+v", ws)
	}
}
